"""The committed api/openapi.json is the published contract. Regenerate with
``uv run python -m torrent_search_server --export-openapi api/openapi.json``."""

from __future__ import annotations

import json
from pathlib import Path

from torrent_search_server.openapi import document, render

ROOT = Path(__file__).resolve().parents[2]


def test_openapi_matches_committed_contract():
    committed = (ROOT / "api/openapi.json").read_text(encoding="utf-8")
    assert committed == render(), "API changed: re-export api/openapi.json"


def test_openapi_surface():
    doc = document()
    paths = set(doc["paths"])
    assert paths == {
        "/healthz",
        "/readyz",
        "/v1/capabilities",
        "/v1/categories",
        "/v1/search",
        "/v1/browse",
        "/v1/top",
        "/v1/recent",
        "/v1/search-results/{result_ref}",
        "/v1/search-results/{result_ref}/resolve",
    }
    search_body = doc["components"]["schemas"]["SearchRequest"]
    assert search_body["additionalProperties"] is False
    assert set(search_body["properties"]) == {"query", "category", "sort", "page"}
    # No operation accepts a URL, proxy or arbitrary source option.
    text = json.dumps(doc).lower()
    for forbidden in ("base_url", "proxy", "mirror", "httpx"):
        assert forbidden not in text, forbidden
