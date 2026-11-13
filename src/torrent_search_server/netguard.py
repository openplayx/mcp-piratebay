"""Outbound request policy, checked on every upstream request.

The upstream library builds URLs from the configured base URL; this guard is
a second line: scheme, host allowlist, port, and resolved addresses
(no loopback, private, link-local/metadata or other non-global targets unless
explicitly enabled for LAN fixtures). Redirects are never followed.

Residual risk: the address check and the connection resolve separately, so a
hostile DNS server for an allowed host could still rebind between the two.
Allowed hosts are administrator-chosen, which keeps this out of reach of API
callers.
"""

from __future__ import annotations

import ipaddress
import socket
from collections.abc import Callable

import httpx

from . import errors
from .config import SourceConfig

Resolver = Callable[[str, int], list[str]]


def system_resolver(host: str, port: int) -> list[str]:
    infos = socket.getaddrinfo(host, port, type=socket.SOCK_STREAM)
    return sorted({info[4][0] for info in infos})


def _address_ok(addr: str) -> bool:
    ip = ipaddress.ip_address(addr.split("%", 1)[0])
    if isinstance(ip, ipaddress.IPv6Address) and ip.ipv4_mapped:
        ip = ip.ipv4_mapped
    return ip.is_global and not ip.is_multicast


def check_url(url: httpx.URL, source: SourceConfig, resolver: Resolver) -> None:
    allowed_schemes = ("https", "http") if source.allow_http else ("https",)
    if url.scheme not in allowed_schemes:
        raise errors.source_blocked()
    host = (url.host or "").lower()
    if host not in source.allowed_hosts:
        raise errors.source_blocked()
    port = url.port or (443 if url.scheme == "https" else 80)
    if port != source.port:
        raise errors.source_blocked()
    if source.allow_private_addresses:
        return
    try:
        literal = ipaddress.ip_address(host.strip("[]"))
    except ValueError:
        literal = None
    if literal is not None:
        addresses = [str(literal)]
    else:
        try:
            addresses = resolver(host, port)
        except OSError as exc:
            raise errors.upstream_unavailable(
                "The source host does not resolve."
            ) from exc
    if not addresses or not all(_address_ok(a) for a in addresses):
        raise errors.source_blocked()
