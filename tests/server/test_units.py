from __future__ import annotations

import json
from pathlib import Path

import httpx
import pytest
from pydantic import ValidationError

from thepiratebay_api import models as upstream
from torrent_search_server import categories, normalize
from torrent_search_server.config import Config, Secrets, SourceConfig, resolve_proxy
from torrent_search_server.errors import ApiError
from torrent_search_server.netguard import check_url
from torrent_search_server.storage import Store

# -- normalisation -------------------------------------------------------------


@pytest.mark.parametrize(
    "display,expected",
    [
        ("2.99\xa0GiB", int(round(2.99 * 1024**3))),
        ("956.66 MiB", int(round(956.66 * 1024**2))),
        ("12 B", 12),
        ("1 TiB", 1024**4),
        ("", None),
        ("huge", None),
        ("1.5 GB", None),
    ],
)
def test_size_bytes(display, expected):
    assert normalize.size_bytes(display) == expected


def test_exact_size():
    assert (
        normalize.exact_size_bytes("6.07\xa0GiB\xa0(6518974464\xa0Bytes)") == 6518974464
    )
    assert normalize.exact_size_bytes("6.07 GiB") is None


@pytest.mark.parametrize(
    "raw,expected",
    [
        ("04-01\xa02019", "2019-04-01"),
        ("12-15 2018", "2018-12-15"),
        ("02-30 2020", None),
        ("11-11\xa009:55", None),
        ("Today\xa001:00", None),
        ("Y-day 10:00", None),
        ("3\xa0mins\xa0ago", None),
        (None, None),
    ],
)
def test_list_date(raw, expected):
    assert normalize.list_date(raw) == expected


def test_detail_datetime():
    assert (
        normalize.detail_datetime("2026-04-25 16:35:39 GMT") == "2026-04-25T16:35:39Z"
    )
    assert normalize.detail_datetime("2026-04-25 16:35:39") is None
    assert normalize.detail_datetime("yesterday") is None


def test_info_hash_forms():
    assert normalize.info_hash("A" * 40) == "a" * 40
    # base32 of 20 zero bytes
    assert normalize.info_hash("A" * 32) == "00" * 20
    assert normalize.info_hash("xyz") is None
    assert normalize.info_hash("g" * 40) is None


def test_magnet_rebuilt_from_validated_parts():
    m = normalize.magnet(
        "magnet:?xt=urn:btih:"
        + "AB" * 20
        + "&dn=My+File%0A&tr=udp%3A%2F%2Ft.example%3A80&tr=file%3A%2F%2F%2Fetc%2Fpasswd"
        + "&xs=http%3A%2F%2Fevil%2Fx.torrent&as=http%3A%2F%2Fevil"
    )
    assert m["info_hash"] == "ab" * 20
    assert m["display_name"] == "My File"
    assert m["trackers"] == ["udp://t.example:80"]
    assert (
        m["uri"]
        == "magnet:?xt=urn:btih:"
        + "ab" * 20
        + "&dn=My%20File&tr=udp%3A%2F%2Ft.example%3A80"
    )
    assert "evil" not in m["uri"]


@pytest.mark.parametrize(
    "uri",
    [
        None,
        "",
        "http://example.com/x.torrent",
        "magnet:?dn=no-hash",
        "magnet:?xt=urn:btih:short",
        "magnet:?xt=urn:btih:" + "a" * 40 + "&xt=urn:btih:" + "b" * 40,
        "magnet:?xt=urn:btmh:1220" + "a" * 64,
        "magnet:?xt=urn:btih:" + "a" * 40 + "&tr=" + "u" * 9000,
    ],
)
def test_invalid_magnets(uri):
    assert normalize.magnet(uri) is None


def test_tracker_count_is_capped():
    trs = "".join(f"&tr=udp%3A%2F%2Ft{i}.example%3A80" for i in range(50))
    m = normalize.magnet("magnet:?xt=urn:btih:" + "a" * 40 + trs)
    assert len(m["trackers"]) == normalize.MAX_TRACKERS


def test_text_sanitising():
    assert normalize.text(" a\u202eb\x00c\n d ", 50) == "abc d"
    assert (
        normalize.text("line1\r\nline2\x1b[31m", 50, multiline=True)
        == "line1\nline2[31m"
    )
    assert normalize.text("   ", 10) is None
    assert normalize.text("x" * 20, 5) == "xxxxx"


# -- categories ----------------------------------------------------------------


def _upstream_codes() -> set[int]:
    codes = {0}
    for enum in (
        upstream._Audio,
        upstream._Video,
        upstream._Apps,
        upstream._Games,
        upstream._XXX,
        upstream._Other,
    ):
        codes |= {int(m) for m in enum}
    return codes


def test_categories_match_upstream_codes():
    assert {c.code for c in categories.CATEGORIES} == _upstream_codes()
    assert len({c.key for c in categories.CATEGORIES}) == len(categories.CATEGORIES)


def test_sorts_match_upstream():
    assert set(categories.SORTS.values()) == {int(s) for s in upstream.SortBy}


def test_category_lookup():
    assert categories.lookup("video.hd_movies", False).code == 207
    with pytest.raises(ApiError):
        categories.lookup("adult", False)
    assert categories.lookup("adult", True).code == 500
    with pytest.raises(ApiError):
        categories.lookup("VIDEO", False)


# -- outbound policy -----------------------------------------------------------


def _src(**kw) -> SourceConfig:
    return SourceConfig(base_url="https://tpb.test", **kw)


def _ok(url: str, src: SourceConfig, addrs=("93.184.216.34",)) -> bool:
    try:
        check_url(httpx.URL(url), src, lambda h, p: list(addrs))
        return True
    except ApiError as e:
        assert e.code in ("source_blocked", "upstream_unavailable")
        return False


def test_netguard():
    src = _src()
    assert _ok("https://tpb.test/search/x/1/99/0", src)
    assert not _ok("http://tpb.test/search", src)
    assert not _ok("https://evil.test/", src)
    assert not _ok("https://tpb.test:8443/", src)
    for addr in (
        "127.0.0.1",
        "10.1.2.3",
        "192.168.1.211",
        "169.254.169.254",
        "::1",
        "fc00::1",
        "::ffff:127.0.0.1",
        "0.0.0.0",
    ):
        assert not _ok("https://tpb.test/", src, (addr,)), addr
    # One bad address among several is enough to refuse.
    assert not _ok("https://tpb.test/", src, ("93.184.216.34", "10.0.0.1"))
    assert _ok(
        "https://tpb.test/", _src(allow_private_addresses=True), ("192.168.1.211",)
    )
    assert _ok("https://mirror.test/", _src(allowed_hosts=["mirror.test"]))


def test_unresolvable_host_is_unavailable():
    def boom(host, port):
        raise OSError("nxdomain")

    with pytest.raises(ApiError) as e:
        check_url(httpx.URL("https://tpb.test/"), _src(), boom)
    assert e.value.code == "upstream_unavailable"


# -- configuration -------------------------------------------------------------


def test_config_validation():
    with pytest.raises(ValidationError):
        SourceConfig(base_url="http://tpb.test")
    assert SourceConfig(base_url="http://tpb.test", allow_http=True).port == 80
    with pytest.raises(ValidationError):
        SourceConfig(base_url="https://tpb.test/path")
    with pytest.raises(ValidationError):
        SourceConfig(base_url="https://user:pw@tpb.test")
    with pytest.raises(ValidationError):
        Config.model_validate({"unknown": 1})
    assert SourceConfig(base_url="https://TPB.test/").allowed_hosts == ["tpb.test"]


def test_secrets_validation_and_proxy_ref():
    with pytest.raises(ValidationError):
        Secrets.model_validate({"tokens": [{"name": "a", "scopes": ["search"]}]})
    with pytest.raises(ValidationError):
        Secrets.model_validate({"tokens": [{"name": "a", "token": "short"}]})
    with pytest.raises(ValidationError):
        Secrets.model_validate(
            {"tokens": [{"name": "a", "token": "x" * 30, "scopes": ["admin"]}]}
        )
    cfg = Config.model_validate({"source": {"proxy_ref": "home"}})
    with pytest.raises(ValueError):
        resolve_proxy(cfg, Secrets())
    assert (
        resolve_proxy(cfg, Secrets(proxies={"home": "socks5://127.0.0.1:1080"}))
        == "socks5://127.0.0.1:1080"
    )


def test_example_config_is_valid():
    root = Path(__file__).resolve().parents[2]
    Config.model_validate(json.loads((root / "deploy/config.example.json").read_text()))
    Secrets.model_validate(
        json.loads((root / "deploy/secrets.example.json").read_text())
    )


# -- storage -------------------------------------------------------------------


def test_store_refs_and_cache():
    now = [1000.0]
    store = Store(":memory:", clock=lambda: now[0])
    a = store.issue("p", "tpb", "1", "a" * 40, "T", ttl=60)
    assert store.issue("p", "tpb", "1", "a" * 40, "T", ttl=60).ref == a.ref
    # A different revision (title or hash changed) is a different reference.
    assert store.issue("p", "tpb", "1", "b" * 40, "T", ttl=60).ref != a.ref
    assert store.lookup(a.ref, "other") is None
    now[0] += 61
    assert store.expired(store.lookup(a.ref, "p"))
    assert store.issue("p", "tpb", "1", "a" * 40, "T", ttl=60).ref != a.ref
    store.cache_put("k", {"v": 1}, ttl=10)
    assert store.cache_get("k").value == {"v": 1}
    now[0] += 11
    assert store.cache_get("k") is None
    store.cache_put("z", {"v": 1}, ttl=0)
    assert store.cache_get("z") is None
