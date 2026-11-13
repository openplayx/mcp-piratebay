"""Search service: cache, in-flight de-duplication, bounded worker pool,
result references and detail/resolve consistency checks."""

from __future__ import annotations

import asyncio
import hashlib
import json
import logging
import time
from collections.abc import Callable
from datetime import datetime, timezone

import anyio
import anyio.to_thread

from . import categories, errors
from .adapter import TpbAdapter
from .auth import Principal
from .config import Config
from .storage import CacheEntry, Candidate, Store

log = logging.getLogger("torrent_search_server")

# Bumped whenever parsing or normalisation output changes, so cached pages
# from an older revision are never served.
PARSER_REVISION = "1"


def iso(ts: float) -> str:
    return datetime.fromtimestamp(ts, timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")


def _cache_info(entry: CacheEntry, hit: bool) -> dict:
    return {
        "hit": hit,
        "fetched_at": iso(entry.fetched_at),
        "expires_at": iso(entry.expires_at),
    }


class SearchService:
    def __init__(
        self,
        config: Config,
        adapter: TpbAdapter,
        store: Store,
        clock: Callable[[], float] = time.time,
    ) -> None:
        self.config = config
        self.adapter = adapter
        self.store = store
        self._clock = clock
        self._limiter = anyio.CapacityLimiter(config.source.max_concurrency)
        self._inflight: dict[str, asyncio.Future] = {}

    @property
    def source_id(self) -> str:
        return self.config.source.id

    def _key(self, *parts: object) -> str:
        raw = json.dumps([PARSER_REVISION, self.source_id, *parts], ensure_ascii=False)
        return hashlib.sha256(raw.encode()).hexdigest()

    async def _cached(
        self, key: str, ttl: int, fetch: Callable[[], dict]
    ) -> tuple[dict, dict]:
        entry = self.store.cache_get(key)
        if entry is not None:
            return entry.value, _cache_info(entry, True)
        pending = self._inflight.get(key)
        if pending is not None:
            entry = await asyncio.shield(pending)
            return entry.value, _cache_info(entry, False)
        future: asyncio.Future = asyncio.get_running_loop().create_future()
        self._inflight[key] = future
        try:
            started = time.monotonic()
            value = await anyio.to_thread.run_sync(fetch, limiter=self._limiter)
            log.info(
                "source fetch ok source=%s ms=%d",
                self.source_id,
                (time.monotonic() - started) * 1000,
            )
            entry = self.store.cache_put(key, value, ttl)
            future.set_result(entry)
            return value, _cache_info(entry, False)
        except asyncio.CancelledError:
            future.cancel()
            raise
        except BaseException as exc:
            code = exc.code if isinstance(exc, errors.ApiError) else type(exc).__name__
            log.warning("source fetch failed source=%s error=%s", self.source_id, code)
            future.set_exception(exc)
            future.exception()  # mark retrieved when nobody else waits
            raise
        finally:
            self._inflight.pop(key, None)

    def _page_response(
        self, principal: Principal, operation: str, page: dict, cache: dict
    ) -> dict:
        ttl = self.config.cache.result_ttl_seconds
        limit = self.config.limits.max_results
        items = page["items"]
        out = []
        for item in items[:limit]:
            cand = self.store.issue(
                principal.name,
                self.source_id,
                item["source_item_id"],
                item["info_hash"],
                item["title"],
                ttl,
            )
            out.append(
                {
                    "result_ref": cand.ref,
                    "source_id": self.source_id,
                    "source_item_id": item["source_item_id"],
                    "title": item["title"],
                    "size_bytes": item["size_bytes"],
                    "size_text": item["size_text"],
                    "seeders": item["seeders"],
                    "leechers": item["leechers"],
                    "uploaded_at": None,
                    "uploaded_date": item["uploaded_date"],
                    "uploaded_text": item["uploaded_text"],
                    "category": item["category"],
                    "uploader": item["uploader"],
                    "info_hash": item["info_hash"],
                    "has_magnet": item["magnet"] is not None,
                    "expires_at": iso(cand.expires_at),
                }
            )
        return {
            "operation": operation,
            "source_id": self.source_id,
            "source_page": page["source_page"],
            "page_count": page["page_count"],
            "has_next_page": page["source_page"] < page["page_count"],
            "result_count": len(out),
            "truncated": len(items) > limit,
            "items": out,
            "cache": cache,
        }

    def _page_arg(self, page: int) -> int:
        if page > self.config.limits.max_page:
            raise errors.invalid_request(
                f"page must be <= {self.config.limits.max_page}."
            )
        return page

    def _category(self, key: str) -> categories.Category:
        return categories.lookup(key, self.config.allow_adult_categories)

    # -- list operations --------------------------------------------------

    async def search(
        self, principal: Principal, query: str, category: str, sort: str, page: int
    ) -> dict:
        principal.require("search")
        max_length = self.config.limits.query_max_length
        if len(query) > max_length:
            raise errors.invalid_request(
                f"query must be at most {max_length} characters."
            )
        cat = self._category(category)
        code = categories.sort_code(sort, "search", categories.SEARCH_SORTS)
        page = self._page_arg(page)
        norm_query = " ".join(query.split()).casefold()
        value, cache = await self._cached(
            self._key("search", norm_query, cat.code, code, page),
            self.config.cache.search_ttl_seconds,
            lambda: self.adapter.search(norm_query, cat.code, code, page),
        )
        return self._page_response(principal, "search", value, cache)

    async def browse(
        self, principal: Principal, category: str, sort: str, page: int
    ) -> dict:
        principal.require("search")
        cat = self._category(category)
        if cat.code == 0:
            raise errors.invalid_request("browse requires a specific category.")
        code = categories.sort_code(sort, "browse", categories.BROWSE_SORTS)
        page = self._page_arg(page)
        value, cache = await self._cached(
            self._key("browse", cat.code, code, page),
            self.config.cache.search_ttl_seconds,
            lambda: self.adapter.browse(cat.code, code, page),
        )
        return self._page_response(principal, "browse", value, cache)

    async def top(self, principal: Principal, category: str) -> dict:
        principal.require("search")
        cat = self._category(category)
        value, cache = await self._cached(
            self._key("top", cat.code),
            self.config.cache.search_ttl_seconds,
            lambda: self.adapter.top(cat.code),
        )
        return self._page_response(principal, "top", value, cache)

    async def recent(self, principal: Principal, page: int) -> dict:
        principal.require("search")
        page = self._page_arg(page)
        value, cache = await self._cached(
            self._key("recent", page),
            self.config.cache.search_ttl_seconds,
            lambda: self.adapter.recent(page),
        )
        return self._page_response(principal, "recent", value, cache)

    # -- candidates -------------------------------------------------------

    def _candidate(self, principal: Principal, ref: str) -> Candidate:
        if not ref.startswith("tsr_") or len(ref) > 64:
            raise errors.not_found()
        cand = self.store.lookup(ref, principal.name)
        if cand is None or cand.source_id != self.source_id:
            raise errors.not_found()
        if self.store.expired(cand):
            raise errors.result_expired()
        return cand

    async def _detail(
        self, cand: Candidate, *, fresh: bool = False
    ) -> tuple[dict, dict]:
        key = self._key("detail", cand.item_id)
        ttl = 0 if fresh else self.config.cache.detail_ttl_seconds
        if fresh:
            value = await anyio.to_thread.run_sync(
                lambda: self.adapter.detail(cand.item_id), limiter=self._limiter
            )
            entry = self.store.cache_put(
                key, value, self.config.cache.detail_ttl_seconds
            )
            return value, _cache_info(entry, False)
        return await self._cached(key, ttl, lambda: self.adapter.detail(cand.item_id))

    @staticmethod
    def _changed(cand: Candidate, detail: dict) -> bool:
        hashes = {h for h in (detail["info_hash"], detail["page_info_hash"]) if h}
        if len(hashes) > 1:
            return True
        if cand.info_hash and hashes and cand.info_hash not in hashes:
            return True
        return False

    async def detail(self, principal: Principal, ref: str) -> dict:
        principal.require("search")
        cand = self._candidate(principal, ref)
        d, cache = await self._detail(cand)
        return {
            "result_ref": cand.ref,
            "source_id": cand.source_id,
            "source_item_id": cand.item_id,
            "title": d["title"],
            "size_bytes": d["size_bytes"],
            "size_exact": d["size_exact"],
            "size_text": d["size_text"],
            "file_count": d["file_count"],
            "uploaded_at": d["uploaded_at"],
            "uploaded_text": d["uploaded_text"],
            "uploader": d["uploader"],
            "category": d["category"],
            "seeders": d["seeders"],
            "leechers": d["leechers"],
            "info_hash": d["info_hash"] or d["page_info_hash"],
            "has_magnet": d["magnet"] is not None,
            "changed": self._changed(cand, d),
            "source_trusted": d["source_trusted"],
            "source_vip": d["source_vip"],
            "description": d["description"],
            "description_truncated": d["description_truncated"],
            "image_count": d["image_count"],
            "cache": cache,
        }

    async def resolve(self, principal: Principal, ref: str) -> dict:
        principal.require("resolve")
        cand = self._candidate(principal, ref)
        # Always re-read the page: a download input must match the source now.
        d, _ = await self._detail(cand, fresh=True)
        if self._changed(cand, d):
            raise errors.candidate_changed()
        m = d["magnet"]
        if m is None:
            raise errors.no_magnet()
        if (
            cand.info_hash is None
            and d["page_info_hash"]
            and d["page_info_hash"] != m["info_hash"]
        ):
            raise errors.candidate_changed()
        log.info(
            "resolved source=%s item=%s principal=%s",
            cand.source_id,
            cand.item_id,
            principal.name,
        )
        return {
            "result_ref": cand.ref,
            "source_id": cand.source_id,
            "source_item_id": cand.item_id,
            "title": cand.title,
            "info_hash": m["info_hash"],
            "magnet": m["uri"],
            "display_name": m["display_name"],
            "tracker_count": len(m["trackers"]),
            "resolved_at": iso(self._clock()),
        }
