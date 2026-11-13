"""Adapter over the unmodified upstream ``TorrentClient``.

The upstream client is synchronous; methods here block and are run by the
service on a bounded worker pool. Each call uses a request-scoped client
(created and closed here) carrying httpx event hooks that enforce the
outbound policy, pacing, status handling and a response size cap, so a
failure is reported as such instead of an empty result list.

Local adaptations (the library is not patched, see UPSTREAM.md):
- top "all" uses the source path ``/top/all``; upstream sends ``/top/0``,
  which the source answers with 404;
- browse requires an explicit category and sort (relevance gives 404).
"""

from __future__ import annotations

import threading
import time
from collections.abc import Callable
from typing import Any

import httpx
from pydantic import ValidationError

from thepiratebay_api import TorrentClient

from . import errors, normalize
from .config import SourceConfig
from .errors import ApiError
from .netguard import Resolver, check_url, system_resolver

_REDIRECTS = {301, 302, 303, 307, 308}


class TpbAdapter:
    def __init__(
        self,
        source: SourceConfig,
        *,
        proxy: str | None = None,
        transport: httpx.BaseTransport | None = None,
        resolver: Resolver = system_resolver,
        sleep: Callable[[float], None] = time.sleep,
        monotonic: Callable[[], float] = time.monotonic,
    ) -> None:
        self.source = source
        self._proxy = proxy
        self._transport = transport
        self._resolver = resolver
        self._sleep = sleep
        self._monotonic = monotonic
        self._lock = threading.Lock()
        self._next_slot = 0.0

    # -- plumbing ---------------------------------------------------------

    def _throttle(self) -> None:
        interval = self.source.min_interval_ms / 1000
        if interval <= 0:
            return
        with self._lock:
            wait = self._next_slot - self._monotonic()
            if wait > 0:
                self._sleep(wait)
            self._next_slot = self._monotonic() + interval

    def _client(self, captured: dict[str, Any]) -> TorrentClient:
        source = self.source

        def on_request(request: httpx.Request) -> None:
            check_url(request.url, source, self._resolver)
            self._throttle()

        def on_response(response: httpx.Response) -> None:
            status = response.status_code
            captured["status"] = status
            if status in _REDIRECTS:
                raise errors.upstream_unavailable(
                    "The source answered with a redirect, which is not followed."
                )
            if status == 429:
                raise errors.upstream_rate_limited()
            if status >= 400 and status != 404:
                raise errors.upstream_unavailable(f"The source answered HTTP {status}.")
            length = response.headers.get("content-length")
            if length and length.isdigit() and int(length) > source.max_response_bytes:
                raise errors.upstream_unavailable("The source response is too large.")
            response.read()
            if len(response.content) > source.max_response_bytes:
                raise errors.upstream_unavailable("The source response is too large.")
            captured["html"] = response.text

        kwargs: dict[str, Any] = {
            "timeout": httpx.Timeout(source.timeout_seconds),
            "headers": {"User-Agent": source.user_agent},
            "event_hooks": {"request": [on_request], "response": [on_response]},
            "follow_redirects": False,
            "trust_env": False,
        }
        if self._proxy:
            kwargs["proxy"] = self._proxy
        if self._transport is not None:
            kwargs["transport"] = self._transport
        return TorrentClient(url=source.base_url, **kwargs)

    def _run(self, call: Callable[[TorrentClient], Any]) -> tuple[Any, dict[str, Any]]:
        captured: dict[str, Any] = {}
        try:
            with self._client(captured) as client:
                return call(client), captured
        except ApiError:
            raise
        except httpx.TimeoutException as exc:
            raise errors.upstream_timeout() from exc
        except httpx.HTTPError as exc:
            raise errors.upstream_unavailable() from exc
        except (
            ValidationError,
            AttributeError,
            TypeError,
            KeyError,
            IndexError,
            ValueError,
        ) as exc:
            # The upstream parser met HTML it does not understand.
            raise errors.parse_changed() from exc

    @staticmethod
    def _page(result: Any, captured: dict[str, Any], page: int) -> dict:
        if captured.get("status") == 404:
            raise errors.upstream_unavailable(
                "The source rejected this listing (HTTP 404)."
            )
        html = captured.get("html", "")
        items = [i for i in (normalize.brief(t) for t in result.torrents) if i]
        if not items:
            has_table = 'id="searchResult"' in html
            has_magnets = "magnet:?" in html
            no_hits = "No hits" in html
            if has_magnets or not (has_table or no_hits):
                raise errors.parse_changed()
        return {
            "items": items,
            "source_page": page,
            "page_count": max(result.page_count, page) if items else page,
        }

    # -- operations -------------------------------------------------------

    def search(self, query: str, category: int, sort: int, page: int) -> dict:
        result, captured = self._run(
            lambda c: c.search(query, category=category, sort_by=sort, page=page)
        )
        return self._page(result, captured, page)

    def browse(self, category: int, sort: int, page: int) -> dict:
        result, captured = self._run(
            lambda c: c.browse(category, sort_by=sort, page=page)
        )
        return self._page(result, captured, page)

    def top(self, category: int) -> dict:
        path_category: Any = "all" if category == 0 else category
        result, captured = self._run(lambda c: c.top(path_category))
        page = self._page(result, captured, 1)
        page["page_count"] = 1
        return page

    def recent(self, page: int) -> dict:
        result, captured = self._run(lambda c: c.recent(page=page))
        return self._page(result, captured, page)

    def detail(self, item_id: str) -> dict:
        result, captured = self._run(lambda c: c.detail(int(item_id)))
        if captured.get("status") == 404:
            raise errors.source_item_gone()
        if result is None:
            raise errors.parse_changed()
        return normalize.full(result)
