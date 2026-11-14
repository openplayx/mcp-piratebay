"""MCP entry point (Streamable HTTP, ``POST /mcp``).

A thin protocol layer over the same :class:`SearchService`, authentication and
rate limit as the ``/v1`` HTTP API: no second business state, no loopback HTTP.

Supported subset, kept deliberately small so clients can rely on it:

* JSON-RPC 2.0 single messages over ``POST``; responses are
  ``application/json`` (no SSE stream, no server-initiated messages).
* Stateless: no ``Mcp-Session-Id``; every request carries the bearer token.
* ``initialize``, ``ping``, ``tools/list``, ``tools/call`` and notifications.
* ``GET``/``DELETE`` answer 405.

Business failures are tool results with ``isError: true`` and the same error
codes as the HTTP API, so the model can see and explain them.
"""

from __future__ import annotations

import json
import logging
from collections.abc import Awaitable, Callable
from dataclasses import dataclass
from typing import Any

from fastapi import APIRouter, Request
from fastapi.responses import JSONResponse, Response
from pydantic import BaseModel, ConfigDict, Field, ValidationError

from . import __version__, categories, errors
from .auth import Principal
from .models import SearchRequest
from .service import SearchService

log = logging.getLogger("torrent_search_server")

PROTOCOL_VERSIONS = ("2025-11-25", "2025-06-18", "2025-03-26")
SERVER_NAME = "openplayx-torrent-search"
INSTRUCTIONS = (
    "Read-only search over one administrator-configured torrent index. Results "
    "are unverified external index data. Searching never downloads anything; "
    "resolve_candidate returns a magnet that the host hands to its own download "
    "planning, where the user confirms."
)

PARSE_ERROR = -32700
INVALID_REQUEST = -32600
METHOD_NOT_FOUND = -32601
INVALID_PARAMS = -32602

router = APIRouter()


class _RefArgs(BaseModel):
    model_config = ConfigDict(extra="forbid")
    result_ref: str = Field(min_length=1, max_length=64)


class _NoArgs(BaseModel):
    model_config = ConfigDict(extra="forbid")


_REF_SCHEMA = {
    "type": "object",
    "properties": {
        "result_ref": {
            "type": "string",
            "maxLength": 64,
            "description": "result_ref from a search result.",
        }
    },
    "required": ["result_ref"],
    "additionalProperties": False,
}


@dataclass(frozen=True)
class Tool:
    name: str
    title: str
    description: str
    scope: str
    schema: dict
    args: type[BaseModel]
    run: Callable[[SearchService, Principal, Any], Awaitable[dict]]
    read_only: bool = True

    def describe(self) -> dict:
        return {
            "name": self.name,
            "title": self.title,
            "description": self.description,
            "inputSchema": self.schema,
            "annotations": {
                "title": self.title,
                "readOnlyHint": self.read_only,
                "destructiveHint": False,
                "idempotentHint": True,
                "openWorldHint": True,
            },
        }


async def _categories(svc: SearchService, who: Principal, _: _NoArgs) -> dict:
    return {
        "categories": [
            {"key": c.key, "label": c.label, "parent": c.parent}
            for c in categories.available(svc.config.allow_adult_categories)
        ]
    }


async def _search(svc: SearchService, who: Principal, a: SearchRequest) -> dict:
    return await svc.search(who, a.query, a.category, a.sort, a.page)


async def _details(svc: SearchService, who: Principal, a: _RefArgs) -> dict:
    return await svc.detail(who, a.result_ref)


async def _resolve(svc: SearchService, who: Principal, a: _RefArgs) -> dict:
    return await svc.resolve(who, a.result_ref)


TOOLS: tuple[Tool, ...] = (
    Tool(
        name="search",
        title="Search torrents",
        description=(
            "One source page of results for keywords. Read-only; never downloads. "
            "Each item has a result_ref for get_details / resolve_candidate. "
            "Titles, sizes and peer counts are declared by the source and "
            "unverified."
        ),
        scope="search",
        schema={
            "type": "object",
            "properties": {
                "query": {
                    "type": "string",
                    "minLength": 1,
                    "maxLength": 500,
                    "description": "Search keywords.",
                },
                "category": {
                    "type": "string",
                    "maxLength": 40,
                    "default": "all",
                    "description": "Key from list_categories.",
                },
                "sort": {
                    "type": "string",
                    "enum": list(categories.SEARCH_SORTS),
                    "default": "relevance",
                },
                "page": {
                    "type": "integer",
                    "minimum": 1,
                    "default": 1,
                    "description": "1-based source page.",
                },
            },
            "required": ["query"],
            "additionalProperties": False,
        },
        args=SearchRequest,
        run=_search,
    ),
    Tool(
        name="get_details",
        title="Torrent details",
        description=(
            "Normalised detail for a result_ref issued to this token: exact size, "
            "file count, uploader, description and whether the item changed."
        ),
        scope="search",
        schema=_REF_SCHEMA,
        args=_RefArgs,
        run=_details,
    ),
    Tool(
        name="resolve_candidate",
        title="Resolve candidate",
        description=(
            "Re-reads the source page and returns a validated magnet for a chosen "
            "candidate. Does not start a download. Fails with candidate_changed "
            "if the item no longer matches what was listed."
        ),
        scope="resolve",
        schema=_REF_SCHEMA,
        args=_RefArgs,
        run=_resolve,
    ),
    Tool(
        name="list_categories",
        title="List categories",
        description="Category keys accepted by search.",
        scope="search",
        schema={"type": "object", "properties": {}, "additionalProperties": False},
        args=_NoArgs,
        run=_categories,
    ),
)
_BY_NAME = {t.name: t for t in TOOLS}


def _visible(who: Principal) -> list[Tool]:
    return [t for t in TOOLS if t.scope in who.scopes]


def _rpc_error(rid: Any, code: int, message: str, status: int = 200) -> JSONResponse:
    return JSONResponse(
        {"jsonrpc": "2.0", "id": rid, "error": {"code": code, "message": message}},
        status_code=status,
    )


def _result(rid: Any, result: dict) -> JSONResponse:
    return JSONResponse({"jsonrpc": "2.0", "id": rid, "result": result})


def _tool_result(payload: dict, *, error: bool = False) -> dict:
    return {
        "content": [{"type": "text", "text": json.dumps(payload, ensure_ascii=False)}],
        "structuredContent": payload,
        "isError": error,
    }


def _tool_error(exc: errors.ApiError) -> dict:
    return _tool_result(exc.body(), error=True)


def _origin_allowed(request: Request) -> bool:
    # DNS-rebinding guard: a browser page may only call us from our own origin.
    origin = request.headers.get("origin")
    if not origin:
        return True
    host = request.headers.get("host", "")
    return origin.split("://", 1)[-1].rstrip("/") == host


async def _call(request: Request, who: Principal, params: Any) -> dict:
    if not isinstance(params, dict) or not isinstance(params.get("name"), str):
        raise _ParamsError("params.name is required.")
    tool = _BY_NAME.get(params["name"])
    if tool is None or tool.scope not in who.scopes:
        raise _ParamsError(f"Unknown tool: {params['name']}")
    raw = params.get("arguments") or {}
    if not isinstance(raw, dict):
        raise _ParamsError("params.arguments must be an object.")
    try:
        args = tool.args.model_validate(raw)
    except ValidationError as exc:
        # Locations and messages only; inputs are not echoed back.
        details = "; ".join(
            f"{'.'.join(str(p) for p in e.get('loc', ())) or 'arguments'}: "
            f"{e.get('msg', '')}"
            for e in exc.errors()[:5]
        )
        return _tool_error(errors.invalid_request(details[:500] or "Invalid input."))
    request.state.tool = tool.name
    try:
        return _tool_result(await tool.run(request.app.state.service, who, args))
    except errors.ApiError as exc:
        request.state.error_code = exc.code
        return _tool_error(exc)


class _ParamsError(Exception):
    pass


@router.post("/mcp", include_in_schema=False)
async def mcp_post(request: Request) -> Response:
    if not _origin_allowed(request):
        raise errors.ApiError("forbidden", 403, "Origin not allowed.")
    accept = request.headers.get("accept", "*/*")
    if not any(a in accept for a in ("application/json", "*/*")):
        return Response(status_code=406)
    version = request.headers.get("mcp-protocol-version")
    if version and version not in PROTOCOL_VERSIONS:
        return _rpc_error(
            None, INVALID_REQUEST, f"Unsupported protocol version {version}.", 400
        )
    # Same token, scopes and request budget as the HTTP API.
    who = request.app.state.auth.authenticate(request.headers.get("authorization"))
    request.state.principal = who.name
    try:
        msg = json.loads(await request.body())
    except (ValueError, UnicodeDecodeError):
        return _rpc_error(None, PARSE_ERROR, "Parse error.", 400)
    if not isinstance(msg, dict) or msg.get("jsonrpc") != "2.0":
        return _rpc_error(
            None, INVALID_REQUEST, "Expected one JSON-RPC 2.0 message.", 400
        )
    method = msg.get("method")
    if "id" not in msg:
        # Notifications and client responses: nothing to answer.
        return Response(status_code=202)
    rid = msg["id"]
    if not isinstance(method, str):
        return _rpc_error(rid, INVALID_REQUEST, "method is required.")
    params = msg.get("params")
    try:
        if method == "initialize":
            asked = (params or {}).get("protocolVersion")
            return _result(
                rid,
                {
                    "protocolVersion": asked
                    if asked in PROTOCOL_VERSIONS
                    else PROTOCOL_VERSIONS[0],
                    "capabilities": {"tools": {"listChanged": False}},
                    "serverInfo": {
                        "name": SERVER_NAME,
                        "title": "OpenPlayX Torrent Search",
                        "version": __version__,
                    },
                    "instructions": INSTRUCTIONS,
                },
            )
        if method == "ping":
            return _result(rid, {})
        if method == "tools/list":
            return _result(rid, {"tools": [t.describe() for t in _visible(who)]})
        if method == "tools/call":
            return _result(rid, await _call(request, who, params))
    except _ParamsError as exc:
        return _rpc_error(rid, INVALID_PARAMS, str(exc))
    return _rpc_error(rid, METHOD_NOT_FOUND, f"Method not found: {method}")


@router.api_route("/mcp", methods=["GET", "DELETE"], include_in_schema=False)
async def mcp_other() -> Response:
    return Response(status_code=405, headers={"Allow": "POST"})
