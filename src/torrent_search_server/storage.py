"""SQLite store: short-TTL source cache and result references.

A result reference is an opaque random id bound to the calling token, the
source, the source item, its info hash and title revision, with an expiry.
References survive restarts until they expire; an unknown or expired id is
reported as such and never re-pointed at a different item.
"""

from __future__ import annotations

import hashlib
import json
import secrets
import sqlite3
import threading
import time
from collections.abc import Callable
from dataclasses import dataclass
from pathlib import Path

_SCHEMA = """
CREATE TABLE IF NOT EXISTS cache (
    key TEXT PRIMARY KEY,
    value TEXT NOT NULL,
    fetched_at REAL NOT NULL,
    expires_at REAL NOT NULL
);
CREATE INDEX IF NOT EXISTS cache_expires ON cache(expires_at);
CREATE TABLE IF NOT EXISTS candidates (
    ref TEXT PRIMARY KEY,
    principal TEXT NOT NULL,
    source_id TEXT NOT NULL,
    item_id TEXT NOT NULL,
    info_hash TEXT,
    title TEXT NOT NULL,
    revision TEXT NOT NULL,
    created_at REAL NOT NULL,
    expires_at REAL NOT NULL,
    UNIQUE (principal, source_id, item_id, revision)
);
CREATE INDEX IF NOT EXISTS candidates_expires ON candidates(expires_at);
"""


@dataclass(frozen=True)
class Candidate:
    ref: str
    principal: str
    source_id: str
    item_id: str
    info_hash: str | None
    title: str
    revision: str
    expires_at: float


@dataclass(frozen=True)
class CacheEntry:
    value: dict
    fetched_at: float
    expires_at: float


def revision(source_id: str, item_id: str, info_hash: str | None, title: str) -> str:
    raw = json.dumps([source_id, item_id, info_hash, title], ensure_ascii=False)
    return hashlib.sha256(raw.encode()).hexdigest()[:16]


class Store:
    def __init__(
        self,
        path: str,
        *,
        max_entries: int = 5000,
        clock: Callable[[], float] = time.time,
    ) -> None:
        if path != ":memory:":
            Path(path).parent.mkdir(parents=True, exist_ok=True)
        self._db = sqlite3.connect(path, check_same_thread=False, isolation_level=None)
        self._db.execute("PRAGMA journal_mode=WAL")
        self._db.executescript(_SCHEMA)
        self._lock = threading.Lock()
        self._max = max_entries
        self._clock = clock
        self._writes = 0

    def close(self) -> None:
        with self._lock:
            self._db.close()

    def ping(self) -> bool:
        with self._lock:
            self._db.execute("SELECT 1").fetchone()
            self._db.execute("BEGIN IMMEDIATE")
            self._db.execute("ROLLBACK")
        return True

    def _maintain(self) -> None:
        self._writes += 1
        if self._writes % 100:
            return
        now = self._clock()
        self._db.execute("DELETE FROM cache WHERE expires_at < ?", (now,))
        self._db.execute("DELETE FROM candidates WHERE expires_at < ?", (now - 86400,))
        for table, order in (("cache", "fetched_at"), ("candidates", "created_at")):
            (count,) = self._db.execute(f"SELECT COUNT(*) FROM {table}").fetchone()
            if count > self._max:
                self._db.execute(
                    f"DELETE FROM {table} WHERE rowid IN "
                    f"(SELECT rowid FROM {table} ORDER BY {order} LIMIT ?)",
                    (count - self._max,),
                )

    # -- cache ------------------------------------------------------------

    def cache_get(self, key: str) -> CacheEntry | None:
        with self._lock:
            row = self._db.execute(
                "SELECT value, fetched_at, expires_at FROM cache WHERE key=?", (key,)
            ).fetchone()
        if row is None or row[2] <= self._clock():
            return None
        return CacheEntry(json.loads(row[0]), row[1], row[2])

    def cache_put(self, key: str, value: dict, ttl: int) -> CacheEntry:
        now = self._clock()
        entry = CacheEntry(value, now, now + ttl)
        if ttl <= 0:
            return entry
        with self._lock:
            self._db.execute(
                "INSERT OR REPLACE INTO cache(key, value, fetched_at, expires_at) "
                "VALUES (?, ?, ?, ?)",
                (key, json.dumps(value, ensure_ascii=False), now, now + ttl),
            )
            self._maintain()
        return entry

    # -- result references ------------------------------------------------

    def issue(
        self,
        principal: str,
        source_id: str,
        item_id: str,
        info_hash: str | None,
        title: str,
        ttl: int,
    ) -> Candidate:
        """Returns the live reference for this exact item revision or mints
        one; either way its expiry is extended to now + ttl."""
        rev = revision(source_id, item_id, info_hash, title)
        now = self._clock()
        expires = now + ttl
        with self._lock:
            row = self._db.execute(
                "SELECT ref, expires_at FROM candidates WHERE principal=? AND "
                "source_id=? AND item_id=? AND revision=?",
                (principal, source_id, item_id, rev),
            ).fetchone()
            if row is not None and row[1] > now:
                ref = row[0]
                self._db.execute(
                    "UPDATE candidates SET expires_at=? WHERE ref=?", (expires, ref)
                )
            else:
                if row is not None:
                    self._db.execute("DELETE FROM candidates WHERE ref=?", (row[0],))
                ref = "tsr_" + secrets.token_urlsafe(18)
                self._db.execute(
                    "INSERT INTO candidates(ref, principal, source_id, item_id, "
                    "info_hash, title, revision, created_at, expires_at) "
                    "VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)",
                    (
                        ref,
                        principal,
                        source_id,
                        item_id,
                        info_hash,
                        title,
                        rev,
                        now,
                        expires,
                    ),
                )
                self._maintain()
        return Candidate(
            ref, principal, source_id, item_id, info_hash, title, rev, expires
        )

    def lookup(self, ref: str, principal: str) -> Candidate | None:
        """The candidate even when expired (callers distinguish expired from
        unknown); ``None`` for unknown refs or refs of another token."""
        with self._lock:
            row = self._db.execute(
                "SELECT ref, principal, source_id, item_id, info_hash, title, "
                "revision, expires_at FROM candidates WHERE ref=?",
                (ref,),
            ).fetchone()
        if row is None or row[1] != principal:
            return None
        return Candidate(*row)

    def expired(self, candidate: Candidate) -> bool:
        return candidate.expires_at <= self._clock()
