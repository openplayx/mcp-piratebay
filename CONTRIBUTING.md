# Contributing

Thanks for your interest. Bug reports, fixes and small improvements are welcome.

## Issues

- Search existing issues first.
- For bugs, include the version or commit, how you run it (binary, Docker,
  stdio), the request you sent and the response or log you got. Remove tokens
  and private addresses before posting.
- Security problems: do **not** open a public issue, see [SECURITY.md](SECURITY.md).

## Pull requests

1. Open an issue first for anything larger than a small fix, so we can agree
   on the approach.
2. Keep changes focused; one topic per pull request.
3. Add or update tests, and run the checks before pushing:

   ```bash
   scripts/check.sh              # ruff, offline tests, OpenAPI contract
   ```

4. Update the README in both languages (`README.md` and `README.zh-CN.md`)
   when behaviour, configuration or tools change.
5. Tool names, arguments and error codes are a contract with MCP clients:
   change them only with a good reason and say so in the pull request.

`src/thepiratebay_api/` is the unmodified upstream library: fix parser bugs upstream, or work around them in `src/torrent_search_server/adapter.py` (see [UPSTREAM.md](UPSTREAM.md)).

By contributing you agree that your contribution is licensed under this
repository's license.
