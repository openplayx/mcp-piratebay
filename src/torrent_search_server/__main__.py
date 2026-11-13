"""Entry point: ``python -m torrent_search_server --config ... --secrets ...``."""

from __future__ import annotations

import argparse
import logging
import os
import sys


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(prog="torrent-search-server")
    parser.add_argument("--config", default=os.environ.get("TORRENT_SEARCH_CONFIG"))
    parser.add_argument("--secrets", default=os.environ.get("TORRENT_SEARCH_SECRETS"))
    parser.add_argument("--host", help="Overrides server.host")
    parser.add_argument("--port", type=int, help="Overrides server.port")
    parser.add_argument(
        "--export-openapi", metavar="FILE", help="Write the OpenAPI document and exit."
    )
    args = parser.parse_args(argv)

    from .app import create_app
    from .config import load_config, load_secrets

    config = load_config(args.config)
    secrets = load_secrets(args.secrets)
    logging.basicConfig(
        level=config.server.log_level,
        format="%(asctime)s %(levelname)s %(name)s %(message)s",
        stream=sys.stderr,
    )

    if args.export_openapi:
        from .openapi import export

        export(args.export_openapi)
        return 0

    if not secrets.tokens:
        logging.getLogger("torrent_search_server").warning(
            "no API tokens configured; every /v1 request will be rejected"
        )

    import uvicorn

    uvicorn.run(
        create_app(config, secrets),
        host=args.host or config.server.host,
        port=args.port or config.server.port,
        proxy_headers=False,
        server_header=False,
        log_config=None,
        access_log=False,
    )
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
