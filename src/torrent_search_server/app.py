"""FastAPI application factory."""

from __future__ import annotations

import logging
import re
import time
import uuid
from contextlib import asynccontextmanager

import httpx
from fastapi import FastAPI, Request
from fastapi.exceptions import RequestValidationError
from fastapi.responses import JSONResponse

from . import __version__
from .adapter import TpbAdapter
from .auth import Authenticator
from .config import Config, Secrets, resolve_proxy
from .errors import ApiError
from .netguard import Resolver, system_resolver
from .routes import router
from .service import SearchService
from .storage import Store

log = logging.getLogger("torrent_search_server")
_REQUEST_ID = re.compile(r"^[A-Za-z0-9._-]{1,64}$")


def create_app(
    config: Config,
    secrets: Secrets,
    *,
    transport: httpx.BaseTransport | None = None,
    resolver: Resolver = system_resolver,
    store: Store | None = None,
) -> FastAPI:
    """``transport``/``resolver``/``store`` are injection points for tests."""
    proxy = resolve_proxy(config, secrets)

    @asynccontextmanager
    async def lifespan(app: FastAPI):
        db = store or Store(config.cache.path, max_entries=config.cache.max_entries)
        adapter = TpbAdapter(
            config.source, proxy=proxy, transport=transport, resolver=resolver
        )
        app.state.service = SearchService(config, adapter, db)
        app.state.auth = Authenticator(secrets, config.limits.requests_per_minute)
        try:
            yield
        finally:
            if store is None:
                db.close()

    public = config.server.docs_public
    app = FastAPI(
        title="OpenPlayX Torrent Search API",
        version=__version__,
        summary="Read-only search over an administrator-configured torrent index.",
        description=(
            "Results are external, unverified index data. Authenticate with "
            "`Authorization: Bearer <token>`. Searching never creates downloads; "
            "a chosen candidate is resolved to a magnet that the OpenPlayX host "
            "hands to its own download planning."
        ),
        lifespan=lifespan,
        docs_url="/docs" if public else None,
        redoc_url=None,
        openapi_url="/openapi.json" if public else None,
    )
    app.include_router(router)

    @app.middleware("http")
    async def request_context(request: Request, call_next):
        rid = request.headers.get("x-request-id", "")
        if not _REQUEST_ID.match(rid):
            rid = uuid.uuid4().hex
        request.state.request_id = rid
        started = time.monotonic()
        response = await call_next(request)
        response.headers["X-Request-ID"] = rid
        # Paths carry no search terms (search is a POST body); never log bodies.
        log.info(
            "request id=%s method=%s path=%s status=%d ms=%d principal=%s error=%s",
            rid,
            request.method,
            request.url.path,
            response.status_code,
            (time.monotonic() - started) * 1000,
            getattr(request.state, "principal", "-"),
            getattr(request.state, "error_code", "-"),
        )
        return response

    @app.exception_handler(ApiError)
    async def api_error(request: Request, exc: ApiError):
        request.state.error_code = exc.code
        return JSONResponse(
            exc.body(getattr(request.state, "request_id", None)),
            status_code=exc.status,
            headers=exc.headers,
        )

    @app.exception_handler(RequestValidationError)
    async def validation_error(request: Request, exc: RequestValidationError):
        request.state.error_code = "invalid_request"
        # Field locations and messages only; inputs are not echoed back.
        details = "; ".join(
            f"{'.'.join(str(p) for p in e.get('loc', ()))}: {e.get('msg', '')}"
            for e in exc.errors()[:5]
        )
        err = ApiError("invalid_request", 422, details[:500] or "Invalid request.")
        return JSONResponse(
            err.body(getattr(request.state, "request_id", None)), status_code=422
        )

    return app
