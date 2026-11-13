"""config.json / secrets.json loading.

config.json is versioned-safe (no credentials); secrets.json holds API tokens
and proxy URLs and is never committed. Source addresses and proxies are an
administrator decision only; nothing here is reachable through the API.
"""

from __future__ import annotations

import hashlib
import json
import os
from pathlib import Path
from typing import Literal
from urllib.parse import urlsplit

from pydantic import BaseModel, ConfigDict, Field, field_validator, model_validator

Scope = Literal["search", "resolve"]


class _Strict(BaseModel):
    model_config = ConfigDict(extra="forbid")


class ServerConfig(_Strict):
    host: str = "127.0.0.1"
    port: int = Field(8790, ge=1, le=65535)
    docs_public: bool = True
    log_level: Literal["DEBUG", "INFO", "WARNING", "ERROR"] = "INFO"


class SourceConfig(_Strict):
    id: str = Field("tpb", pattern=r"^[a-z][a-z0-9_-]{0,31}$")
    name: str = Field("The Pirate Bay (mirror)", min_length=1, max_length=80)
    base_url: str = "https://thepiratebay10.info"
    # Hosts requests may go to. Defaults to the base_url host only.
    allowed_hosts: list[str] = Field(default_factory=list)
    # Test fixtures on a LAN only; production keeps public addresses only.
    allow_private_addresses: bool = False
    allow_http: bool = False
    # Key into secrets.json "proxies"; the URL may carry credentials.
    proxy_ref: str | None = None
    timeout_seconds: float = Field(10.0, gt=0, le=60)
    max_concurrency: int = Field(2, ge=1, le=16)
    min_interval_ms: int = Field(500, ge=0, le=60000)
    max_response_bytes: int = Field(4 * 1024 * 1024, ge=64 * 1024, le=32 * 1024 * 1024)
    user_agent: str = Field(
        "Mozilla/5.0 (compatible; OpenPlayX-TorrentSearch/0.1)", max_length=200
    )

    @field_validator("base_url")
    @classmethod
    def _base(cls, v: str) -> str:
        parts = urlsplit(v)
        if parts.scheme not in ("https", "http") or not parts.hostname:
            raise ValueError("base_url must be an absolute http(s) URL")
        if parts.path not in ("", "/") or parts.query or parts.fragment:
            raise ValueError("base_url must not contain a path, query or fragment")
        if parts.username or parts.password:
            raise ValueError("base_url must not contain credentials")
        return v.rstrip("/")

    @model_validator(mode="after")
    def _hosts(self) -> SourceConfig:
        parts = urlsplit(self.base_url)
        if parts.scheme == "http" and not self.allow_http:
            raise ValueError("http base_url requires allow_http")
        host = parts.hostname.lower()
        self.allowed_hosts = sorted({h.lower() for h in self.allowed_hosts} | {host})
        return self

    @property
    def host(self) -> str:
        return urlsplit(self.base_url).hostname.lower()

    @property
    def port(self) -> int:
        parts = urlsplit(self.base_url)
        return parts.port or (443 if parts.scheme == "https" else 80)


class CacheConfig(_Strict):
    path: str = "data/torrent-search.sqlite3"
    search_ttl_seconds: int = Field(300, ge=0, le=86400)
    detail_ttl_seconds: int = Field(600, ge=0, le=86400)
    result_ttl_seconds: int = Field(3600, ge=60, le=7 * 86400)
    max_entries: int = Field(5000, ge=100, le=1_000_000)


class LimitsConfig(_Strict):
    query_max_length: int = Field(120, ge=1, le=500)
    max_page: int = Field(50, ge=1, le=1000)
    # Upper bound of items returned for one source page (the source decides
    # the page size: ~30 for lists, 100 for top). Exceeding it is reported.
    max_results: int = Field(100, ge=1, le=500)
    requests_per_minute: int = Field(60, ge=1, le=10000)


class Config(_Strict):
    schema_version: Literal[1] = 1
    server: ServerConfig = Field(default_factory=ServerConfig)
    source: SourceConfig = Field(default_factory=SourceConfig)
    cache: CacheConfig = Field(default_factory=CacheConfig)
    limits: LimitsConfig = Field(default_factory=LimitsConfig)
    allow_adult_categories: bool = False


class TokenEntry(_Strict):
    name: str = Field(pattern=r"^[A-Za-z0-9_.-]{1,64}$")
    # Either the plain token or its lowercase hex SHA-256.
    token: str | None = Field(None, min_length=24, max_length=512)
    token_sha256: str | None = Field(None, pattern=r"^[0-9a-f]{64}$")
    scopes: list[Scope] = Field(default_factory=lambda: ["search"])

    @model_validator(mode="after")
    def _one(self) -> TokenEntry:
        if (self.token is None) == (self.token_sha256 is None):
            raise ValueError("set exactly one of token / token_sha256")
        return self

    @property
    def digest(self) -> str:
        if self.token_sha256:
            return self.token_sha256
        return hashlib.sha256(self.token.encode()).hexdigest()


class Secrets(_Strict):
    tokens: list[TokenEntry] = Field(default_factory=list)
    proxies: dict[str, str] = Field(default_factory=dict)

    @model_validator(mode="after")
    def _unique(self) -> Secrets:
        names = [t.name for t in self.tokens]
        if len(names) != len(set(names)):
            raise ValueError("token names must be unique")
        return self


def load_config(path: str | os.PathLike | None) -> Config:
    if path is None:
        return Config()
    return Config.model_validate(json.loads(Path(path).read_text(encoding="utf-8")))


def load_secrets(path: str | os.PathLike | None) -> Secrets:
    if path is None:
        return Secrets()
    return Secrets.model_validate(json.loads(Path(path).read_text(encoding="utf-8")))


def resolve_proxy(config: Config, secrets: Secrets) -> str | None:
    ref = config.source.proxy_ref
    if ref is None:
        return None
    if ref not in secrets.proxies:
        raise ValueError(f"proxy_ref {ref!r} is not defined in secrets")
    return secrets.proxies[ref]
