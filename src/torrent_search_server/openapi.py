"""Deterministic OpenAPI export for api/openapi.json and the contract test."""

from __future__ import annotations

import json
from pathlib import Path

from .app import create_app
from .config import Config, Secrets


def document() -> dict:
    # Default configuration: the document must not depend on deployment config.
    return create_app(Config(), Secrets()).openapi()


def render() -> str:
    return json.dumps(document(), indent=2, sort_keys=True, ensure_ascii=False) + "\n"


def export(path: str) -> None:
    Path(path).parent.mkdir(parents=True, exist_ok=True)
    Path(path).write_text(render(), encoding="utf-8")
