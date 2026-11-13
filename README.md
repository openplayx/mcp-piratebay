# OpenPlayX Torrent Search

A read-only HTTP API for searching an administrator-configured torrent
index, built for the OpenPlayX app and its AI tools. It wraps the upstream
[`thepiratebay_api`](docs/upstream/README.md) library, which is imported in
full and kept unmodified; see [UPSTREAM.md](UPSTREAM.md).

The service only searches. It does not run a download engine, create
downloads or store media. In OpenPlayX, the app shows the search candidates.
After the user picks one and asks for a download, the app resolves it here
and hands the magnet to bittorrent-server's plan/add flow.

Results are external index data. Titles, sizes and peer counts are what the
source declares and are not verified. Source badges such as "trusted" are
not safety checks.

## API

| Endpoint | Purpose |
|---|---|
| `GET /healthz` | Liveness (never contacts the source) |
| `GET /readyz` | Tokens configured, cache store writable |
| `GET /v1/capabilities` | Operations for this token, sorts, limits, versions |
| `GET /v1/categories` | Category keys (adult categories off by default) |
| `POST /v1/search` | `{"query", "category", "sort", "page"}` returns one source page |
| `GET /v1/browse?category=&sort=&page=` | Browse one specific category |
| `GET /v1/top?category=` | The source's top list |
| `GET /v1/recent?page=` | Newest items |
| `GET /v1/search-results/{result_ref}` | Normalised detail of a listed item |
| `POST /v1/search-results/{result_ref}/resolve` | Validated magnet for a chosen item (`resolve` scope) |
| `GET /openapi.json`, `GET /docs` | Contract and browsable docs ([api/openapi.json](api/openapi.json)) |

Every `/v1` call needs `Authorization: Bearer <token>`. More detail is in
[docs/api.md](docs/api.md).

## Development

```bash
uv sync --all-groups --extra server
scripts/check.sh                       # lint, all offline tests, OpenAPI contract
uv run torrent-search-server --config deploy/config.local.json --secrets deploy/secrets.json
```

`config.local.json` and `secrets.json` are git-ignored. Start from the
`deploy/*.example.json` files. For a plain token, set
`"token": "<at least 24 chars>"`; to store it hashed, use `"token_sha256"`.

## Deployment

The image comes from the `Dockerfile` (pinned sources and `uv.lock`, non-root,
read-only root filesystem). [deploy/compose.yaml](deploy/compose.yaml) runs it
as the independent Compose project `openplayx-torrent-search`. The 211 test
deployment uses `scripts/deploy-211.sh`, described in
[docs/deploy-211.md](docs/deploy-211.md).

## License

The upstream library is MIT, © 2026 Daniel ([LICENSE](LICENSE)). OpenPlayX
additions are in `src/torrent_search_server/`, `tests/server/`, `api/`,
`deploy/`, `scripts/` and `docs/`, apart from `docs/upstream/`.
