"""Public HTTP protocol (/v1). Only these operations are exposed; mirror
discovery, source switching, proxies and httpx options are not."""

from __future__ import annotations

from typing import Annotated

from fastapi import APIRouter, Depends, Header, Query, Request

from . import UPSTREAM, __version__, categories
from .auth import Principal
from .categories import BROWSE_SORTS
from .models import (
    CategoryList,
    ErrorResponse,
    Health,
    ResolvedCandidate,
    ResultDetail,
    ResultPage,
    SearchRequest,
)
from .service import SearchService

ERRORS = {
    code: {"model": ErrorResponse}
    for code in (400, 401, 403, 404, 409, 410, 422, 429, 502, 503, 504)
}
router = APIRouter()


def service(request: Request) -> SearchService:
    return request.app.state.service


def principal(
    request: Request,
    authorization: Annotated[str | None, Header(include_in_schema=False)] = None,
) -> Principal:
    who = request.app.state.auth.authenticate(authorization)
    request.state.principal = who.name
    return who


Svc = Annotated[SearchService, Depends(service)]
Who = Annotated[Principal, Depends(principal)]
CategoryQ = Annotated[str, Query(max_length=40, description="Key from /v1/categories.")]
PageQ = Annotated[int, Query(ge=1, description="1-based source page.")]


@router.get("/healthz", response_model=Health, tags=["ops"])
async def healthz() -> dict:
    """Process liveness. Never contacts the source."""
    return {"status": "ok"}


@router.get(
    "/readyz",
    response_model=Health,
    tags=["ops"],
    responses={503: {"model": ErrorResponse}},
)
async def readyz(request: Request) -> dict:
    """Configuration, tokens and the cache store are usable. A source outage
    does not make the service unready."""
    from .errors import ApiError

    if not request.app.state.auth.configured:
        raise ApiError("not_ready", 503, "No API tokens are configured.")
    try:
        request.app.state.service.store.ping()
    except Exception as exc:
        raise ApiError("not_ready", 503, "The cache store is not writable.") from exc
    return {"status": "ready"}


@router.get("/v1/capabilities", tags=["meta"], responses=ERRORS)
async def capabilities(svc: Svc, who: Who) -> dict:
    """Operations, sources, sort orders and limits available to this token."""
    cfg = svc.config
    return {
        "service": {
            "name": "openplayx-torrent-search",
            "version": __version__,
            "api": "v1",
        },
        "upstream": UPSTREAM,
        "sources": [{"source_id": cfg.source.id, "name": cfg.source.name}],
        "operations": {
            "search": "search" in who.scopes,
            "browse": "search" in who.scopes,
            "top": "search" in who.scopes,
            "recent": "search" in who.scopes,
            "detail": "search" in who.scopes,
            "resolve": "resolve" in who.scopes,
        },
        "sorts": {
            "search": list(categories.SEARCH_SORTS),
            "browse": list(categories.BROWSE_SORTS),
            "top": [],
            "recent": [],
        },
        "limits": {
            "query_max_length": cfg.limits.query_max_length,
            "max_page": cfg.limits.max_page,
            "max_results": cfg.limits.max_results,
            "page_size": "source-defined (about 30; top lists 100)",
            "requests_per_minute": cfg.limits.requests_per_minute,
            "result_ttl_seconds": cfg.cache.result_ttl_seconds,
        },
        "adult_categories": cfg.allow_adult_categories,
    }


@router.get(
    "/v1/categories", response_model=CategoryList, tags=["meta"], responses=ERRORS
)
async def list_categories(svc: Svc, who: Who) -> dict:
    return {
        "categories": [
            {"key": c.key, "label": c.label, "parent": c.parent, "source_code": c.code}
            for c in categories.available(svc.config.allow_adult_categories)
        ]
    }


@router.post("/v1/search", response_model=ResultPage, tags=["search"], responses=ERRORS)
async def search(body: SearchRequest, svc: Svc, who: Who) -> dict:
    """One source page of results for keywords. Read-only; never downloads."""
    return await svc.search(who, body.query, body.category, body.sort, body.page)


@router.get("/v1/browse", response_model=ResultPage, tags=["search"], responses=ERRORS)
async def browse(
    svc: Svc,
    who: Who,
    category: CategoryQ,
    sort: Annotated[
        str, Query(description="One of: " + ", ".join(BROWSE_SORTS))
    ] = "date_desc",
    page: PageQ = 1,
) -> dict:
    """Browse one specific category page by page."""
    return await svc.browse(who, category, sort, page)


@router.get("/v1/top", response_model=ResultPage, tags=["search"], responses=ERRORS)
async def top(svc: Svc, who: Who, category: CategoryQ = "all") -> dict:
    """The source's top list for a category (single page)."""
    return await svc.top(who, category)


@router.get("/v1/recent", response_model=ResultPage, tags=["search"], responses=ERRORS)
async def recent(svc: Svc, who: Who, page: PageQ = 1) -> dict:
    """Most recently added items."""
    return await svc.recent(who, page)


@router.get(
    "/v1/search-results/{result_ref}",
    response_model=ResultDetail,
    tags=["results"],
    responses=ERRORS,
)
async def result_detail(result_ref: str, svc: Svc, who: Who) -> dict:
    """Normalised detail for a live reference issued to this token."""
    return await svc.detail(who, result_ref)


@router.post(
    "/v1/search-results/{result_ref}/resolve",
    response_model=ResolvedCandidate,
    tags=["results"],
    responses=ERRORS,
)
async def resolve(result_ref: str, svc: Svc, who: Who) -> dict:
    """Re-reads the source page and returns a validated magnet for a chosen
    candidate (requires the ``resolve`` scope). Fails with candidate_changed
    if the item no longer matches what was listed."""
    return await svc.resolve(who, result_ref)
