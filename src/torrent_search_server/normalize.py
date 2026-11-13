"""Turns upstream models into the service's normalised, untrusted-safe form.

Rules: a value that cannot be parsed reliably becomes ``None`` (never 0 or
"now"); raw source strings are kept beside parsed values; every free text
field is plain text with control characters removed and a length cap.
"""

from __future__ import annotations

import base64
import re
import unicodedata
from datetime import datetime, timezone
from urllib.parse import parse_qs, quote, urlsplit

_UNITS = {"B": 0, "KIB": 1, "MIB": 2, "GIB": 3, "TIB": 4, "PIB": 5}
_SIZE = re.compile(r"^\s*(\d+(?:\.\d+)?)\s*([KMGTP]?I?B)\s*$", re.IGNORECASE)
_EXACT_BYTES = re.compile(r"\((\d{1,20})\s*Bytes\)", re.IGNORECASE)
_LIST_DATE = re.compile(r"^(\d{2})-(\d{2})\s+(\d{4})$")
_DETAIL_DATE = re.compile(r"^(\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}) GMT$")
_HEX40 = re.compile(r"^[0-9a-fA-F]{40}$")
_B32 = re.compile(r"^[A-Za-z2-7]{32}$")
_CONTROL = re.compile(
    r"[\x00-\x08\x0b-\x1f\x7f-\x9f\u200b-\u200f\u202a-\u202e\u2066-\u2069]"
)
_TRACKER_SCHEMES = ("udp", "http", "https", "ws", "wss")
MAX_TRACKERS = 20


def text(value: object, max_length: int, *, multiline: bool = False) -> str | None:
    """Plain text: NFC, no control/bidi characters, collapsed, capped."""
    if value is None:
        return None
    s = unicodedata.normalize("NFC", str(value)).replace("\xa0", " ")
    s = s.replace("\r\n", "\n").replace("\r", "\n")
    s = _CONTROL.sub("", s if multiline else s.replace("\n", " ").replace("\t", " "))
    if not multiline:
        s = re.sub(r"\s+", " ", s)
    s = s.strip()
    if not s:
        return None
    return s[:max_length]


def size_bytes(display: str | None) -> int | None:
    """Approximate bytes from a display size such as ``2.99 GiB``."""
    if not display:
        return None
    m = _SIZE.match(display.replace("\xa0", " "))
    if not m:
        return None
    unit = m.group(2).upper()
    if unit not in _UNITS:
        return None
    return int(round(float(m.group(1)) * 1024 ** _UNITS[unit]))


def exact_size_bytes(display: str | None) -> int | None:
    """Exact bytes from a detail size such as ``6.07 GiB (6518974464 Bytes)``."""
    if not display:
        return None
    m = _EXACT_BYTES.search(display.replace("\xa0", " "))
    return int(m.group(1)) if m else None


def list_date(raw: str | None) -> str | None:
    """``MM-DD YYYY`` -> ``YYYY-MM-DD``. Forms without a year or with a
    source-local clock time (``Today 01:00``, ``11-11 09:55``) have no
    reliable date and give ``None``."""
    if not raw:
        return None
    m = _LIST_DATE.match(raw.replace("\xa0", " ").strip())
    if not m:
        return None
    month, day, year = m.groups()
    try:
        return datetime(int(year), int(month), int(day)).date().isoformat()
    except ValueError:
        return None


def detail_datetime(raw: str | None) -> str | None:
    """``2026-04-25 16:35:39 GMT`` -> ``2026-04-25T16:35:39Z``."""
    if not raw:
        return None
    m = _DETAIL_DATE.match(raw.replace("\xa0", " ").strip())
    if not m:
        return None
    try:
        dt = datetime.strptime(m.group(1), "%Y-%m-%d %H:%M:%S")
    except ValueError:
        return None
    return dt.replace(tzinfo=timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")


def info_hash(value: str | None) -> str | None:
    """Lowercase hex v1 info hash from 40 hex or 32 base32 characters."""
    if not value:
        return None
    v = value.strip()
    if _HEX40.match(v):
        return v.lower()
    if _B32.match(v):
        return base64.b32decode(v.upper()).hex()
    return None


def magnet(uri: str | None) -> dict | None:
    """Validates a magnet URI and rebuilds a canonical one from its parts.

    Only ``xt=urn:btih:`` (v1), ``dn`` and tracker URLs with known schemes
    survive; anything else in the source string is dropped.
    """
    if not uri or len(uri) > 8192 or not uri.startswith("magnet:?"):
        return None
    try:
        params = parse_qs(uri[len("magnet:?") :], strict_parsing=False)
    except ValueError:
        return None
    hashes = {
        info_hash(xt[len("urn:btih:") :])
        for xt in params.get("xt", [])
        if xt.lower().startswith("urn:btih:")
    }
    hashes.discard(None)
    if len(hashes) != 1:
        return None
    ih = hashes.pop()
    name = text((params.get("dn") or [None])[0], 300)
    trackers: list[str] = []
    for tr in params.get("tr", []):
        tr = tr.strip()
        try:
            parts = urlsplit(tr)
        except ValueError:
            continue
        if (
            parts.scheme in _TRACKER_SCHEMES
            and parts.hostname
            and len(tr) <= 300
            and not _CONTROL.search(tr)
            and tr not in trackers
        ):
            trackers.append(tr)
        if len(trackers) >= MAX_TRACKERS:
            break
    canonical = f"magnet:?xt=urn:btih:{ih}"
    if name:
        canonical += "&dn=" + quote(name, safe="")
    for tr in trackers:
        canonical += "&tr=" + quote(tr, safe="")
    return {
        "info_hash": ih,
        "display_name": name,
        "trackers": trackers,
        "uri": canonical,
    }


def brief(t) -> dict | None:
    """Normalises an upstream ``BriefTorrent`` into the cached item form."""
    if t.torrent_id is None or t.torrent_id <= 0:
        return None
    title = text(t.title, 300)
    if title is None:
        return None
    m = magnet(t.magnet_link)
    size_text = text(t.size, 40)
    uploaded_text = text(t.date, 40)
    return {
        "source_item_id": str(t.torrent_id),
        "title": title,
        "size_text": size_text,
        "size_bytes": size_bytes(size_text),
        "seeders": t.seeders if t.seeders is not None and t.seeders >= 0 else None,
        "leechers": t.leechers if t.leechers is not None and t.leechers >= 0 else None,
        "uploaded_text": uploaded_text,
        "uploaded_date": list_date(uploaded_text),
        "category": text(t.category, 80),
        "uploader": text(t.uploader, 64),
        "info_hash": m["info_hash"] if m else None,
        "magnet": m["uri"] if m else None,
    }


DESCRIPTION_MAX = 4000


def full(t) -> dict:
    """Normalises an upstream ``FullTorrent`` (detail page)."""
    m = magnet(t.magnet_link)
    page_hash = info_hash(t.info_hash)
    description = text(t.description, DESCRIPTION_MAX + 1, multiline=True)
    truncated = description is not None and len(description) > DESCRIPTION_MAX
    return {
        "title": text(t.title, 300),
        "size_text": text(t.size, 80),
        "size_bytes": exact_size_bytes(t.size) or size_bytes(text(t.size, 80)),
        "size_exact": exact_size_bytes(t.size) is not None,
        "file_count": t.num_files
        if t.num_files is not None and t.num_files >= 0
        else None,
        "uploaded_text": text(t.date, 40),
        "uploaded_at": detail_datetime(t.date),
        "uploader": text(t.uploader, 64),
        "category": text(t.category, 80),
        "seeders": t.seeders if t.seeders is not None and t.seeders >= 0 else None,
        "leechers": t.leechers if t.leechers is not None and t.leechers >= 0 else None,
        "page_info_hash": page_hash,
        "info_hash": m["info_hash"] if m else None,
        "magnet": m,
        "source_trusted": bool(t.is_trusted),
        "source_vip": bool(t.is_vip),
        "description": description[:DESCRIPTION_MAX] if description else None,
        "description_truncated": truncated,
        "image_count": len(t.images or []),
    }
