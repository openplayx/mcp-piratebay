# mcp-piratebay

[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

Torrent search for [OpenPlayX](https://github.com/openplayx/openplayx) as an
MCP server and HTTP API (formerly *torrent-search*). A read-only service for searching an administrator-configured torrent
index, built for the OpenPlayX app and its AI tools. It wraps the upstream
[`thepiratebay_api`](docs/upstream/README.md) library, which is imported in
full and kept unmodified; see [UPSTREAM.md](UPSTREAM.md).

The service only searches. It does not run a download engine, create
downloads or store media. In OpenPlayX, the app shows the search candidates.
After the user picks one and asks for a download, the app resolves it here
and hands the magnet to [mcp-qbittorrent](https://github.com/openplayx/mcp-qbittorrent)'s plan/add flow.

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

## MCP

`POST /mcp` exposes `search`, `get_details`, `resolve_candidate` and
`list_categories`; `tools/list` only shows the tools the token's scopes allow
(`resolve_candidate` needs `resolve`). Searching never downloads.

Streamable HTTP subset: single JSON-RPC 2.0 messages over `POST`, answered
with `application/json` (no SSE, no server-initiated messages); stateless (no
`Mcp-Session-Id`); `initialize`, `ping`, `tools/list`, `tools/call` and
notifications; protocol versions 2025-11-25, 2025-06-18 and 2025-03-26. The
same bearer token as the HTTP API is required on every request; requests with
a foreign `Origin` are refused. Business failures are tool results with
`isError: true` and the HTTP API's error codes. The MCP layer calls the same
service code as the HTTP handlers (no second state, no loopback request).

## Quick start

Requirements: Docker, or Python 3.12 with [uv](https://docs.astral.sh/uv/)
for local development.

1. Create a token and the configuration:

   ```bash
   cd deploy
   cp config.example.json config.json      # set source.base_url to the index you want
   cp secrets.example.json secrets.json
   TOKEN=$(python3 -c 'import secrets; print(secrets.token_urlsafe(32))')
   printf '%s' "$TOKEN" | shasum -a 256    # put the digest into secrets.json → token_sha256
   ```

   Instead of a digest, `"token": "<at least 24 chars>"` also works.

2. Build and start:

   ```bash
   docker build -t openplayx/torrent-search:local ..
   TORRENT_SEARCH_IMAGE=openplayx/torrent-search:local docker compose up -d
   ```

   The service listens on `127.0.0.1:18790` (`BIND` and `PORT` change it).
   The image is built from pinned sources and `uv.lock`, runs as non-root
   with a read-only root filesystem, and keeps its cache in a named volume.

3. Check it:

   ```bash
   curl -s http://127.0.0.1:18790/readyz
   curl -s -H "Authorization: Bearer $TOKEN" http://127.0.0.1:18790/v1/capabilities
   ```

## Configuration

| File | Content |
|---|---|
| `config.json` ([example](deploy/config.example.json)) | source (`base_url`, allowed hosts, proxy, timeouts, request spacing), cache TTLs, limits, `allow_adult_categories` |
| `secrets.json` ([example](deploy/secrets.example.json)) | tokens (name, `token` or `token_sha256`, scopes `search` / `resolve`), optional proxy URLs |

Both files are git-ignored. Without the `resolve` scope a token can search but
not obtain magnets.

## Using it from an MCP client

```json
{
  "mcpServers": {
    "piratebay": {
      "type": "http",
      "url": "http://127.0.0.1:18790/mcp",
      "headers": { "Authorization": "Bearer <token>" }
    }
  }
}
```

In OpenPlayX, add it under **AI → 外部能力** with the service root URL and the token.

## Development

```bash
uv sync --all-groups --extra server
scripts/check.sh                       # lint, all offline tests, OpenAPI contract
uv run torrent-search-server --config deploy/config.local.json --secrets deploy/secrets.json
```

`config.local.json` and `secrets.json` are git-ignored. `scripts/deploy-211.sh`
is the maintainers' helper for building and running a committed revision on a
remote Docker host over SSH (`DEPLOY_HOST`, `BIND`). Upgrading the upstream
library: [UPSTREAM.md](UPSTREAM.md).

## Contributing

Issues and pull requests are welcome, see [CONTRIBUTING.md](CONTRIBUTING.md).
Report security problems privately as described in [SECURITY.md](SECURITY.md).

## Disclaimer

This project does not host, index or distribute any content. It queries a
third-party index that the operator configures and returns what that index
lists, unverified. You are responsible for what you search for and download:
only download content you have the right to, and follow the law that applies
to you. The project is not affiliated with The Pirate Bay or any index.

## License

[MIT](LICENSE). The upstream library is © 2026 Daniel; the OpenPlayX additions
(`src/torrent_search_server/`, `tests/server/`, `api/`, `deploy/`, `scripts/`
and `docs/` apart from `docs/upstream/`) are © 2026 OpenPlayX contributors,
under the same license.
