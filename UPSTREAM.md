# Upstream

The page parser in `internal/tpb/` is a Go port of the parsers of the
upstream Python library. This repository started as a full import of that
library (its git history is kept); the Python code was replaced by the Go
service on 2026-11-15.

| | |
|---|---|
| Upstream | https://github.com/9BitDaniel/thepiratebay-api (MIT) |
| Ported from | commit `2580c7f339aed26fb790d694cd3a19aa08f9c826` (release 0.1.0), `thepiratebay_api/parsers.py` and the URL helpers in `client.py` / `helpers.py` |
| Remote | `upstream` points at the original repository |

What the port keeps, so results are the same as before:

- Selectors and text extraction of `_parse_search_results`,
  `_parse_torrent_page`, `_parse_dl` and `_parse_pagination`
  (BeautifulSoup `get_text(strip=True)` semantics).
- A seeder, leecher or file count that is not an integer makes the page
  unreadable (`parse_changed`), as the library's model validation did.
- Search paths `/search/<query>/<page>/<sort>/<category>` with `/` in the
  query replaced by a space and everything except unreserved characters
  percent-encoded.

`internal/tpb/testdata/` holds the upstream HTML fixtures together with the
output of the Python parsers on them (`*.golden.json`); the Go parser must
reproduce it exactly.

Two upstream behaviours are deliberately not ported:

- `top(Category.ALL)` requested `/top/0`, which the source answers with 404.
  The service uses `/top/all`.
- `browse()` with the relevance order requested a URL the source answers with
  404. The service offers browse only with explicit date, size or seeders
  orders.

`TorrentClient.mirrors()` (fetching a third-party mirror list and probing
every URL on it) was never used by the service and is not ported.

## Following upstream changes

1. `git fetch upstream` and review the diff of `thepiratebay_api/parsers.py`.
2. Port relevant parser changes to `internal/tpb/`, regenerate the golden
   files with the upstream code if its fixtures changed, and run
   `scripts/check.sh`.
3. If parsing or normalisation output changes, bump `ParserRevision` in
   `internal/service/service.go` so pages cached by the old parser are not
   served.
4. Update this file and the `upstream` block in `/v1/capabilities`
   (`internal/server/server.go`).
