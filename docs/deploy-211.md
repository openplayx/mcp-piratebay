# 211 test deployment

The target is the LAN test host `192.168.1.211` (x86_64, Docker 29, Compose v5).
It is shared with unrelated services, so this deployment stays inside its
own Compose project and directory.

| | |
|---|---|
| Compose project | `openplayx-torrent-search` |
| Host directory | `~/openplayx-torrent-search/` (`releases/<tag>/` sources, `deploy/` config, secrets, compose) |
| Listen | `192.168.1.211:18790` maps to container port 8790 |
| Data | Named volume `openplayx-torrent-search_data` (SQLite cache and refs) |
| Image | `openplayx/torrent-search:<first 12 chars of commit>`, built on the host |

## Deploy

```bash
scripts/deploy-211.sh            # deploys the committed HEAD
scripts/deploy-211.sh <commit>
```

The script archives the commit (`git archive`), copies it to the host,
builds the image there, and brings the project up. It then waits for
`/readyz` and prints the image id.

On the first deployment, the script copies `config.json` from
`deploy/config.example.json`. It also generates `secrets.json` on the host
with one token (`openplayx-app`, scopes `search` and `resolve`). Only the
token's SHA-256 goes into `secrets.json`. The plain token is written to
`~/openplayx-torrent-search/deploy/app-token.txt` with mode 0600, for
transfer to the app's secure storage. Delete it once transferred. The token
is never printed or committed.

## Rollback

```bash
IMAGE_TAG=<previous tag> scripts/deploy-211.sh --rollback
```

Old images and `releases/<tag>/` directories are kept until removed by hand.
Never run a global `docker system prune` on this host.

## Checks after deploying

```bash
curl -s http://192.168.1.211:18790/healthz
curl -s http://192.168.1.211:18790/readyz
curl -s -H "Authorization: Bearer $TOKEN" http://192.168.1.211:18790/v1/capabilities
# optional real-source smoke test with freely redistributable content
curl -s -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"query":"ubuntu"}' http://192.168.1.211:18790/v1/search
```

The real-source smoke test is optional. If the public source is down, the
service answers `upstream_unavailable` or `upstream_timeout`, and the
service is still working. Do not record a mock-only run as "source
available".
