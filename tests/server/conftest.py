from __future__ import annotations

import pytest
from fastapi.testclient import TestClient

from torrent_search_server.app import create_app
from torrent_search_server.config import Config, Secrets
from torrent_search_server.storage import Store

from .fakesource import BASE, standard

SEARCH_TOKEN = "search-token-0123456789abcdef"
RESOLVE_TOKEN = "resolve-token-0123456789abcdef"


def public_resolver(host: str, port: int) -> list[str]:
    return ["93.184.216.34"]


def make_config(**overrides) -> Config:
    raw = {
        "source": {"base_url": BASE, "min_interval_ms": 0},
        "cache": {"path": ":memory:"},
    }
    for key, value in overrides.items():
        raw.setdefault(key, {})
        if isinstance(value, dict):
            raw[key].update(value)
        else:
            raw[key] = value
    return Config.model_validate(raw)


def make_secrets() -> Secrets:
    return Secrets.model_validate(
        {
            "tokens": [
                {"name": "app-search", "token": SEARCH_TOKEN, "scopes": ["search"]},
                {
                    "name": "app",
                    "token": RESOLVE_TOKEN,
                    "scopes": ["search", "resolve"],
                },
            ]
        }
    )


class Env:
    def __init__(
        self, config: Config | None = None, resolver=public_resolver, source=None
    ):
        self.source = source or standard()
        self.store = Store(":memory:")
        self.app = create_app(
            config or make_config(),
            make_secrets(),
            transport=self.source.transport,
            resolver=resolver,
            store=self.store,
        )

    def client(self) -> TestClient:
        return TestClient(self.app, raise_server_exceptions=True)


def auth(token: str = RESOLVE_TOKEN) -> dict:
    return {"Authorization": f"Bearer {token}"}


@pytest.fixture
def env() -> Env:
    return Env()


@pytest.fixture
def client(env: Env):
    with env.client() as c:
        yield c
