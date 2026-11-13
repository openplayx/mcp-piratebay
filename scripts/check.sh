#!/usr/bin/env bash
# Offline checks used locally and by CI: lint the service package, run the
# upstream and service test suites, verify the committed OpenAPI contract.
set -euo pipefail
cd "$(dirname "$0")/.."
uv sync --frozen --all-groups --extra server
uv run ruff check src/torrent_search_server tests/server
uv run pytest -q
uv run python -m torrent_search_server --export-openapi /tmp/torrent-search-openapi.json
diff -u api/openapi.json /tmp/torrent-search-openapi.json
echo "all checks passed"
