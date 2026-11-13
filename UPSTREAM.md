# Upstream

This repository is a full import of the upstream project plus the OpenPlayX
HTTP API server. The upstream library stays in `src/thepiratebay_api/`,
can be imported on its own, and has not been modified.

| | |
|---|---|
| Upstream | https://github.com/9BitDaniel/thepiratebay-api (MIT) |
| PyPI | https://pypi.org/project/thepiratebay-api/ |
| Imported commit | `2580c7f339aed26fb790d694cd3a19aa08f9c826` (upstream `main`, 17 commits, full history kept) |
| Release tag | `v0.1.0` = `7a83bb8215d05d53ecde225c408d31cbeb157d2e`; it differs from the imported commit only in `README.md` |
| Imported on | 2026-11-13 |
| PyPI 0.1.0 wheel | `thepiratebay_api-0.1.0-py3-none-any.whl`, sha256 `de3eb1370571d41c0073208ddfca7158f58736305eb1e024508bdc802712901a`. Its `thepiratebay_api/` is byte-identical to `src/thepiratebay_api/` at the imported commit |
| PyPI 0.1.0 sdist | `thepiratebay_api-0.1.0.tar.gz`, sha256 `fcc82b2a7359c598227a397f8a53d80bb2d49dfaed98ab205f444d51ee926be5` |

The history was imported by cloning upstream without `--depth`. The
OpenPlayX commits sit on top of it. The remote `upstream` points at the
original repository.

## Review notes (imported commit)

Every file in `src/thepiratebay_api/` was read before the import. It has no
`exec`, `eval`, subprocess, filesystem, environment or socket access, no
`.pth` or entry points, and its build backend is hatchling with no
`setup.py`. It makes HTTP GET requests with httpx and parses them with
BeautifulSoup/lxml. `TorrentClient.mirrors()` fetches a third-party mirror
list and probes every URL on it with redirects enabled. The service never
calls it.

## Changes to upstream-owned files

| File | Change | Reason |
|---|---|---|
| `README.md` | Moved, unchanged, to `docs/upstream/README.md` | The repository front page now describes the service |
| `pyproject.toml` | `readme` path updated; added the `server` extra, a console script, the server package in the wheel, and server test dependencies in `dev` | The library's own dependency list is unchanged |
| `uv.lock` | Re-locked for the added dependencies | Reproducible builds |
| `.github/workflows/tests.yml` | Removed the Codecov upload step (the original author's token); runs `scripts/check.sh` | No upstream credentials or publishing |
| `.gitignore` | Appended service data/secret paths | |

No parser patches. Two upstream behaviours are handled in
`src/torrent_search_server/adapter.py` and not patched in the library:

- `TorrentClient.top(Category.ALL)` requests `/top/0`, which the source
  answers with 404. The adapter passes `"all"`, giving `/top/all`.
- `browse()` with the default `SortBy.RELEVANCE` requests a URL the source
  answers with 404. The service offers browse only with explicit date, size
  or seeders orders.

## Upgrading upstream

1. `git fetch upstream` and review the diff of `src/thepiratebay_api/`
   file by file.
2. Merge (never a mirror push), then run `scripts/check.sh`. The service
   tests use self-written fixtures, and the upstream tests use upstream's own
   HTML fixtures.
3. If parsing output changes, bump `PARSER_REVISION` in `service.py` so that
   cached pages from the old parser are not served.
4. Update this file: commit, version, date, and wheel/sdist checksums if a new
   release exists.
