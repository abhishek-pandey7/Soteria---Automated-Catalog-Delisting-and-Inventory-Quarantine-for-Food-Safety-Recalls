"""SQLite snapshot history + emitted-signal dedup."""

from __future__ import annotations

import json
import os
import sqlite3
from datetime import datetime, timezone

from .sources import Row

SCHEMA = """
CREATE TABLE IF NOT EXISTS snapshots (
    id        INTEGER PRIMARY KEY AUTOINCREMENT,
    source    TEXT NOT NULL,
    taken_at  TEXT NOT NULL,
    row_count INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS snapshots_source ON snapshots (source, id);
CREATE TABLE IF NOT EXISTS rows (
    snapshot_id INTEGER NOT NULL REFERENCES snapshots(id) ON DELETE CASCADE,
    key         TEXT NOT NULL,
    data        TEXT NOT NULL,
    PRIMARY KEY (snapshot_id, key)
);
CREATE TABLE IF NOT EXISTS emitted (
    source     TEXT NOT NULL,
    key        TEXT NOT NULL,
    event_id   TEXT NOT NULL,
    emitted_at TEXT NOT NULL,
    PRIMARY KEY (source, key)
);
"""


def _now() -> str:
    return datetime.now(timezone.utc).isoformat(timespec="seconds").replace("+00:00", "Z")


class Store:
    def __init__(self, path: str = ":memory:"):
        if path != ":memory:":
            os.makedirs(os.path.dirname(os.path.abspath(path)), exist_ok=True)
        # /healthz reads counts() from the HTTP thread while the poll loop
        # writes; Python's sqlite3 serialises access itself, it only needs
        # telling the connection is shared.
        self.db = sqlite3.connect(path, check_same_thread=False)
        self.db.execute("PRAGMA foreign_keys = ON")
        if path != ":memory:":
            self.db.execute("PRAGMA journal_mode = WAL")
        self.db.executescript(SCHEMA)
        # The signal payload itself, so /v1/signals can serve what was
        # published (added after the first release: migrate older stores).
        cols = {r[1] for r in self.db.execute("PRAGMA table_info(emitted)")}
        if "data" not in cols:
            self.db.execute("ALTER TABLE emitted ADD COLUMN data TEXT NOT NULL DEFAULT '{}'")
            self.db.commit()

    def close(self) -> None:
        self.db.close()

    # ---- snapshots

    def save_snapshot(self, source: str, rows: list[Row], taken_at: str | None = None) -> int:
        taken_at = taken_at or _now()
        cur = self.db.execute("INSERT INTO snapshots (source, taken_at, row_count) VALUES (?, ?, ?)", (source, taken_at, len(rows)))
        sid = cur.lastrowid
        self.db.executemany(
            "INSERT OR REPLACE INTO rows (snapshot_id, key, data) VALUES (?, ?, ?)",
            [(sid, r.key, json.dumps(r.to_dict(), separators=(",", ":"))) for r in rows],
        )
        self.db.commit()
        return sid

    def latest_snapshots(self, source: str, n: int = 2) -> list[tuple[int, str]]:
        """Newest first: [(id, taken_at), ...]."""
        return self.db.execute(
            "SELECT id, taken_at FROM snapshots WHERE source = ? ORDER BY id DESC LIMIT ?", (source, n)
        ).fetchall()

    def rows(self, snapshot_id: int) -> dict[str, Row]:
        out: dict[str, Row] = {}
        for key, data in self.db.execute("SELECT key, data FROM rows WHERE snapshot_id = ?", (snapshot_id,)):
            out[key] = Row.from_dict(json.loads(data))
        return out

    def prune(self, source: str, keep: int = 30) -> None:
        ids = [r[0] for r in self.db.execute("SELECT id FROM snapshots WHERE source = ? ORDER BY id DESC", (source,)).fetchall()]
        for sid in ids[keep:]:
            self.db.execute("DELETE FROM rows WHERE snapshot_id = ?", (sid,))
            self.db.execute("DELETE FROM snapshots WHERE id = ?", (sid,))
        self.db.commit()

    # ---- emitted-signal dedup

    def already_emitted(self, source: str, key: str) -> bool:
        return self.db.execute("SELECT 1 FROM emitted WHERE source = ? AND key = ?", (source, key)).fetchone() is not None

    def mark_emitted(self, source: str, key: str, event_id: str, payload: dict | None = None) -> None:
        self.db.execute(
            "INSERT OR REPLACE INTO emitted (source, key, event_id, emitted_at, data) VALUES (?, ?, ?, ?, ?)",
            (source, key, event_id, _now(), json.dumps(payload or {})),
        )
        self.db.commit()

    def signals(self, source: str = "", limit: int = 100) -> list[dict]:
        """Published catalog.sku.vanished.v1 payloads, newest first, for the ops console."""
        rows = self.db.execute(
            "SELECT source, event_id, emitted_at, data FROM emitted ORDER BY emitted_at DESC LIMIT ?", (max(1, min(limit, 1000)),)
        ).fetchall()
        out = []
        for src, event_id, at, data in rows:
            if source and src != source:
                continue
            p = json.loads(data)
            if not p:
                continue
            p.setdefault("source", src)
            p.setdefault("event_id", event_id)
            p.setdefault("emitted_at", at)
            out.append(p)
        return out

    def clear_emitted(self, source: str, keys: list[str]) -> None:
        """A key that reappears is no longer 'vanished'; a later vanish must emit again."""
        self.db.executemany("DELETE FROM emitted WHERE source = ? AND key = ?", [(source, k) for k in keys])
        self.db.commit()

    def counts(self) -> dict:
        snaps = self.db.execute("SELECT COUNT(*) FROM snapshots").fetchone()[0]
        emitted = self.db.execute("SELECT COUNT(*) FROM emitted").fetchone()[0]
        return {"snapshots": snaps, "emitted": emitted}
