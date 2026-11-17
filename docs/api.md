# API notes for OpenPlayX integration

The machine-readable contract is `src/api/openapi.json`, which a test keeps in sync
with the code. This page covers the behaviour the schema cannot express.

## Authentication and scopes

`Authorization: Bearer <token>`. Tokens are defined in `secrets.json`:

- `search` covers capabilities, categories, search, browse, top, recent and detail.
- `resolve` is needed only for `POST /v1/search-results/{ref}/resolve`.

Each token has its own request budget (`limits.requests_per_minute`). Going
over it returns `429 rate_limited` with `Retry-After`.

## Paging

Each response is exactly one source page. The source decides the page size:
about 30 for search, browse and recent, and 100 for top. There is no `limit`
parameter, so no page is ever cut short and silently skipped. If a page
holds more than `limits.max_results` items, the response says
`truncated: true`. `page_count` comes from the source's pagination bar and
is approximate. `has_next_page` is `source_page < page_count`.

## Result references

`result_ref` (`tsr_…`) is opaque and random. It is bound to the token that
received it, and to the source, the item id, and the item's info hash and
title revision. It expires `cache.result_ttl_seconds` after it was last
listed.

- A ref unknown to this token returns `404 not_found`.
- An expired ref returns `410 result_expired`. Run the search again
  explicitly; the service never re-points a ref at another item.
- Refs persist in SQLite, so they survive restarts until they expire.

## Detail and resolve

- Detail uses a short cache. `changed: true` means the page's info hash no
  longer matches the listed item.
- Resolve always re-reads the source page. It fails with
  `409 candidate_changed` if the item changed, or with
  `422 no_valid_magnet` if the item has no valid v1 magnet.
- The returned `magnet` is rebuilt from validated parts: the info hash, the
  display name, and at most 20 udp/http(s)/ws(s) trackers. Everything else
  in the source string is dropped. Hand it to the download plan unmodified.

## Normalisation

| Field | Rule |
|---|---|
| `size_bytes` (list) | Approximate, parsed from the display size (`2.99 GiB`) |
| `size_bytes` (detail) | Exact when the page shows `(N Bytes)` (`size_exact`) |
| `uploaded_date` (list) | Only for `MM-DD YYYY`; source-local forms (`Today 01:00`, `11-11 09:55`) give `null` |
| `uploaded_at` (detail) | UTC from `YYYY-MM-DD HH:MM:SS GMT` |
| text fields | Plain text: NFC, control and bidi characters removed, capped length |
| `description` | At most 4000 characters (`description_truncated`) |

Unknown values are `null`, never 0 or the current time. `description`,
`title` and `uploader` are untrusted input. Escape them when rendering, and
never treat them as instructions. Image URLs from descriptions are not
returned; only `image_count` is.

## Errors

The body is always `{"error": {"code", "message", "retryable"}, "request_id"}`.

| HTTP | code | Meaning |
|---|---|---|
| 400 | `invalid_request`, `category_unsupported`, `sort_unsupported` | Bad parameters |
| 401 / 403 | `unauthorized` / `forbidden` | Token missing or wrong / lacks the scope |
| 404 | `not_found`, `source_item_gone` | Unknown ref / the source no longer has the item |
| 409 | `candidate_changed` | Not resolved because the item changed |
| 410 | `result_expired` | Search again |
| 422 | `invalid_request`, `no_valid_magnet` | Schema violation / nothing to resolve |
| 429 | `rate_limited` | Token budget exceeded |
| 502 | `upstream_unavailable`, `parse_changed`, `source_blocked` | Source down or redirecting / page structure not recognised / address refused by policy |
| 503 | `upstream_rate_limited` | The source is throttling |
| 504 | `upstream_timeout` | The source was too slow |

A search with no matches is `200` with `items: []`. A page the parser
cannot read is `parse_changed`, never an empty list.

## Outbound policy

Only the configured `source.base_url` host (plus any `allowed_hosts`) is
contacted, on its configured scheme and port. Before each request, the
resolved addresses must all be public, unless `allow_private_addresses` is
set for LAN fixtures. Redirects are not followed. Responses are capped at
`max_response_bytes`. Requests are paced by `min_interval_ms` and limited to
`max_concurrency`, and identical concurrent requests share one fetch.
Mirror discovery, source switching and proxies are configuration-only; no
API parameter reaches them.

## Logging

One line per request, with request id, method, path, status, duration,
token name and error code. Search terms travel in POST bodies, so they never
appear in logs. Logs never contain tokens or magnets.
