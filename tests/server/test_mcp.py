"""MCP entry (POST /mcp) over the same service, tokens and errors."""

from __future__ import annotations

from .conftest import RESOLVE_TOKEN, SEARCH_TOKEN, auth

HEADERS = {"Accept": "application/json, text/event-stream"}


def rpc(client, method, params=None, *, token=RESOLVE_TOKEN, rid=1, **headers):
    msg = {"jsonrpc": "2.0", "id": rid, "method": method}
    if params is not None:
        msg["params"] = params
    return client.post("/mcp", json=msg, headers={**HEADERS, **auth(token), **headers})


def call(client, name, arguments=None, token=RESOLVE_TOKEN):
    r = rpc(
        client, "tools/call", {"name": name, "arguments": arguments or {}}, token=token
    )
    assert r.status_code == 200, r.text
    return r.json()["result"]


def test_initialize_negotiates_version_and_advertises_tools(client):
    r = rpc(
        client,
        "initialize",
        {
            "protocolVersion": "2025-06-18",
            "capabilities": {},
            "clientInfo": {"name": "test", "version": "1"},
        },
    )
    assert r.status_code == 200
    result = r.json()["result"]
    assert result["protocolVersion"] == "2025-06-18"
    assert result["capabilities"] == {"tools": {"listChanged": False}}
    assert result["serverInfo"]["name"] == "openplayx-torrent-search"
    assert "mcp-session-id" not in r.headers
    # Unknown versions fall back to the newest supported one.
    r = rpc(client, "initialize", {"protocolVersion": "1999-01-01"})
    assert r.json()["result"]["protocolVersion"] == "2025-11-25"


def test_notifications_are_accepted_without_body(client):
    r = client.post(
        "/mcp",
        json={"jsonrpc": "2.0", "method": "notifications/initialized"},
        headers={**HEADERS, **auth()},
    )
    assert r.status_code == 202
    assert r.content == b""


def test_requires_the_same_bearer_token(client):
    r = client.post(
        "/mcp", json={"jsonrpc": "2.0", "id": 1, "method": "ping"}, headers=HEADERS
    )
    assert r.status_code == 401
    assert r.headers["www-authenticate"] == "Bearer"
    assert rpc(client, "ping", token="wrong-token-0123456789").status_code == 401


def test_tools_follow_token_scopes(client):
    names = {t["name"] for t in rpc(client, "tools/list").json()["result"]["tools"]}
    assert names == {"search", "get_details", "resolve_candidate", "list_categories"}
    limited = rpc(client, "tools/list", token=SEARCH_TOKEN).json()["result"]["tools"]
    assert "resolve_candidate" not in {t["name"] for t in limited}
    search = next(t for t in limited if t["name"] == "search")
    assert search["inputSchema"]["required"] == ["query"]
    assert search["annotations"]["readOnlyHint"] is True
    # A tool outside the token's scopes is unknown, not forbidden-but-listed.
    r = rpc(
        client,
        "tools/call",
        {"name": "resolve_candidate", "arguments": {"result_ref": "tsr_x"}},
        token=SEARCH_TOKEN,
    )
    assert r.json()["error"]["code"] == -32602


def test_search_details_and_resolve_round_trip(client, env):
    page = call(client, "search", {"query": "ubuntu"})
    assert page["isError"] is False
    items = page["structuredContent"]["items"]
    assert items and "magnet:" not in page["content"][0]["text"]
    ref = items[0]["result_ref"]
    detail = call(client, "get_details", {"result_ref": ref})
    assert detail["isError"] is False
    assert detail["structuredContent"]["result_ref"] == ref
    resolved = call(client, "resolve_candidate", {"result_ref": ref})
    assert resolved["isError"] is False
    assert resolved["structuredContent"]["magnet"].startswith("magnet:?")
    # Same references and cache as the HTTP API.
    http = client.post("/v1/search", json={"query": "ubuntu"}, headers=auth()).json()
    assert http["items"][0]["result_ref"] == ref
    assert len(env.source.requests) == 3  # search, detail, fresh resolve


def test_business_and_validation_errors_are_tool_errors(client):
    bad = call(client, "search", {"query": "secret-term", "page": "x"})
    assert bad["isError"] is True
    assert bad["structuredContent"]["error"]["code"] == "invalid_request"
    assert "secret-term" not in bad["content"][0]["text"]
    extra = call(client, "search", {"query": "ubuntu", "magnet": "x"})
    assert extra["isError"] is True
    missing = call(client, "get_details", {"result_ref": "tsr_unknown"})
    assert missing["isError"] is True
    assert missing["structuredContent"]["error"]["code"] == "not_found"


def test_protocol_errors(client):
    assert rpc(client, "resources/list").json()["error"]["code"] == -32601
    r = client.post("/mcp", content=b"{not json", headers={**HEADERS, **auth()})
    assert r.status_code == 400
    assert r.json()["error"]["code"] == -32700
    r = client.post(
        "/mcp",
        json=[{"jsonrpc": "2.0", "id": 1, "method": "ping"}],
        headers={**HEADERS, **auth()},
    )
    assert r.status_code == 400
    assert (
        rpc(client, "ping", **{"MCP-Protocol-Version": "1999-01-01"}).status_code == 400
    )
    assert client.get("/mcp", headers=auth()).status_code == 405
    assert rpc(client, "ping", Origin="https://evil.example").status_code == 403
    assert rpc(client, "ping", Accept="text/html").status_code == 406
