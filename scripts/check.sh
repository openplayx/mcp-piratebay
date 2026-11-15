#!/usr/bin/env bash
# Offline checks used locally and by CI: formatting, vet and tests. The
# tests use self-written fixtures and a fake source; they never touch the
# network.
set -euo pipefail
cd "$(dirname "$0")/.."
unformatted="$(gofmt -l .)"
if [ -n "$unformatted" ]; then
  echo "gofmt needed:" >&2
  echo "$unformatted" >&2
  exit 1
fi
go vet ./...
go test -race -count=1 ./...
echo "all checks passed"
