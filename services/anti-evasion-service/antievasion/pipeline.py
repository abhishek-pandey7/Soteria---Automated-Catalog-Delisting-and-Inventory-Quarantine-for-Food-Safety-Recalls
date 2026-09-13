"""watch-list × listings → candidates → judge → guardrail → evasion.flagged.v1"""

from __future__ import annotations

import hashlib
import json
import logging
import os
import sqlite3
import sys
import threading
import uuid
from dataclasses import dataclass, field

import yaml

from .judge import Judge, decide
from .match import candidates
from .model import Flag, Listing, Recall, gtin14, now_iso
from .sources import SourceSpec, fetch

log = logging.getLogger("antievasion.pipeline")

EXCHANGE = "evasion.x"
EVENT_TYPE = "evasion.flagged.v1"
PRODUCER = "anti-evasion-service"


# ---- watch-list ------------------------------------------------------------

class Watchlist:
    """Recalled products to hunt for, from containment events and/or YAML."""

    def __init__(self, path: str = ":memory:"):
        if path != ":memory:":
            os.makedirs(os.path.dirname(os.path.abspath(path)), exist_ok=True)
        self.db = sqlite3.connect(path, check_same_thread=False)
        self.lock = threading.Lock()
        self.db.executescript("""
CREATE TABLE IF NOT EXISTS recalls (key TEXT PRIMARY KEY, data TEXT NOT NULL, added_at TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS flagged (listing_id TEXT NOT NULL, recall_key TEXT NOT NULL, flag_id TEXT NOT NULL, flagged_at TEXT NOT NULL, PRIMARY KEY (listing_id, recall_key));
CREATE TABLE IF NOT EXISTS judged (listing_id TEXT NOT NULL, recall_key TEXT NOT NULL, text_hash TEXT NOT NULL, flagged INTEGER NOT NULL, judged_at TEXT NOT NULL, PRIMARY KEY (listing_id, recall_key));
""")
        # The flag payload itself, so /v1/flags can serve what was published
        # (added after the first release: migrate a store that predates it).
        cols = {r[1] for r in self.db.execute("PRAGMA table_info(flagged)")}
        if "data" not in cols:
            self.db.execute("ALTER TABLE flagged ADD COLUMN data TEXT NOT NULL DEFAULT '{}'")
            self.db.commit()

    def add(self, r: Recall) -> None:
        with self.lock:
            self.db.execute("INSERT OR REPLACE INTO recalls (key, data, added_at) VALUES (?, ?, ?)", (r.key, json.dumps(r.__dict__), now_iso()))
            self.db.commit()

    def all(self) -> list[Recall]:
        with self.lock:
            rows = self.db.execute("SELECT data FROM recalls").fetchall()
        return [Recall(**json.loads(d)) for (d,) in rows]

    def already_flagged(self, listing_id: str, recall_key: str) -> bool:
        with self.lock:
            return self.db.execute("SELECT 1 FROM flagged WHERE listing_id = ? AND recall_key = ?", (listing_id, recall_key)).fetchone() is not None

    def mark_flagged(self, listing_id: str, recall_key: str, flag_id: str, payload: dict | None = None) -> None:
        with self.lock:
            self.db.execute("INSERT OR REPLACE INTO flagged (listing_id, recall_key, flag_id, flagged_at, data) VALUES (?, ?, ?, ?, ?)",
                            (listing_id, recall_key, flag_id, now_iso(), json.dumps(payload or {})))
            self.db.commit()

    def flags(self, incident_id: str = "", limit: int = 100) -> list[dict]:
        """Published flags, newest first, for the ops console."""
        with self.lock:
            rows = self.db.execute("SELECT data, flagged_at FROM flagged ORDER BY flagged_at DESC LIMIT ?", (max(1, min(limit, 1000)),)).fetchall()
        out = []
        for data, at in rows:
            f = json.loads(data)
            if not f:
                continue
            if incident_id and f.get("incident_id") != incident_id:
                continue
            f.setdefault("flagged_at", at)
            out.append(f)
        return out

    def already_judged(self, listing_id: str, recall_key: str, text_hash: str) -> bool:
        """True when this exact listing text was already judged for this recall (edits re-judge)."""
        with self.lock:
            row = self.db.execute("SELECT text_hash FROM judged WHERE listing_id = ? AND recall_key = ?", (listing_id, recall_key)).fetchone()
        return bool(row) and row[0] == text_hash

    def mark_judged(self, listing_id: str, recall_key: str, text_hash: str, flagged: bool) -> None:
        with self.lock:
            self.db.execute("INSERT OR REPLACE INTO judged VALUES (?, ?, ?, ?, ?)", (listing_id, recall_key, text_hash, int(flagged), now_iso()))
            self.db.commit()

    def load_yaml(self, path: str) -> int:
        with open(path, encoding="utf-8") as f:
            doc = yaml.safe_load(f) or {}
        n = 0
        for r in doc.get("recalls", []):
            self.add(Recall(incident_id=r["incident_id"], gtin=gtin14(r.get("gtin", "")), lot_codes=[str(x) for x in r.get("lot_codes", [])],
                            date_codes=[str(x) for x in r.get("date_codes", [])],
                            brand=r.get("brand", ""), product_title=r.get("product_title", ""), hazard=r.get("hazard", ""),
                            recalled_at=r.get("recalled_at", ""), source="watchlist"))
            n += 1
        return n

    def add_from_containment(self, payload: dict, occurred_at: str) -> int:
        """containment.action.taken.v1 → one Recall per HELD target."""
        held = {r.get("gtin") for r in payload.get("results", []) if r.get("status") == "HELD"}
        n = 0
        for t in payload.get("targets", []):
            if t.get("gtin") not in held:
                continue
            self.add(Recall(incident_id=payload.get("incident_id", ""), gtin=gtin14(t.get("gtin", "")), lot_codes=[str(x) for x in t.get("lot_codes", [])],
                            brand="", product_title=t.get("product_title", ""), hazard=payload.get("hazard", ""),
                            recalled_at=payload.get("taken_at") or occurred_at, source="containment"))
            n += 1
        return n


# ---- events -----------------------------------------------------------------

def envelope(flag: Flag, causation_id: str = "") -> dict:
    env = {
        "event_id": str(uuid.uuid4()), "event_type": EVENT_TYPE, "event_version": 1, "occurred_at": now_iso(),
        "producer": PRODUCER, "correlation_id": flag.incident_id, "payload": flag.payload(),
    }
    if causation_id:
        env["causation_id"] = causation_id
    return env


class StdoutPublisher:
    def publish(self, env: dict) -> None:
        sys.stdout.write(json.dumps(env, separators=(",", ":")) + "\n")
        sys.stdout.flush()

    def close(self) -> None:  # pragma: no cover
        pass


class AMQPPublisher:
    def __init__(self, url: str):
        import pika  # local import keeps tests free of a broker dependency

        self.pika = pika
        self.url = url
        self._connect()

    def _connect(self) -> None:
        params = self.pika.URLParameters(self.url)
        params.heartbeat = 30
        self.conn = self.pika.BlockingConnection(params)
        self.ch = self.conn.channel()
        self.ch.exchange_declare(exchange=EXCHANGE, exchange_type="topic", durable=True)
        self.ch.confirm_delivery()
        log.info("amqp connected exchange=%s", EXCHANGE)

    def publish(self, env: dict) -> None:
        body = json.dumps(env, separators=(",", ":")).encode()
        props = self.pika.BasicProperties(content_type="application/json", delivery_mode=2, message_id=env["event_id"], type=env["event_type"], app_id=env["producer"])
        for attempt in range(3):
            try:
                self.ch.basic_publish(exchange=EXCHANGE, routing_key=env["event_type"], body=body, properties=props)
                return
            except Exception as e:  # noqa: BLE001
                log.warning("publish failed (attempt %d): %s", attempt + 1, e)
                try:
                    self._connect()
                except Exception as ce:  # noqa: BLE001
                    log.warning("reconnect failed: %s", ce)
        raise RuntimeError("could not publish after 3 attempts")

    def close(self) -> None:
        try:
            self.conn.close()
        except Exception:  # noqa: BLE001
            pass


# ---- pipeline --------------------------------------------------------------------

@dataclass
class Health:
    started_at: str = field(default_factory=now_iso)
    listings_seen: int = 0
    candidates: int = 0
    judged: int = 0
    flagged: int = 0
    last_error: str = ""
    consecutive_failures: int = 0
    broker_connected: bool = True
    lock: threading.Lock = field(default_factory=threading.Lock)

    def healthy(self) -> bool:
        with self.lock:
            return self.broker_connected and self.consecutive_failures < 3


class Pipeline:
    def __init__(self, watchlist: Watchlist, judge: Judge, publisher, sources: list[SourceSpec], min_score: float = 0.35, health: Health | None = None):
        self.watchlist, self.judge, self.publisher, self.sources = watchlist, judge, publisher, sources
        self.min_score = min_score
        self.health = health or Health()

    def tick(self) -> list[dict]:
        listings: list[Listing] = []
        for spec in self.sources:
            try:
                got = fetch(spec)
                listings.extend(got)
                log.info("fetched source=%s listings=%d", spec.name, len(got))
                with self.health.lock:
                    self.health.consecutive_failures = 0
            except Exception as e:  # noqa: BLE001
                with self.health.lock:
                    self.health.consecutive_failures += 1
                    self.health.last_error = str(e)
                log.error("fetch failed source=%s err=%s", spec.name, e)
        return self.process(listings)

    def process(self, listings: list[Listing]) -> list[dict]:
        recalls = self.watchlist.all()
        with self.health.lock:
            self.health.listings_seen += len(listings)
        if not recalls or not listings:
            log.info("nothing to do recalls=%d listings=%d", len(recalls), len(listings))
            return []
        cands = candidates(recalls, listings, self.min_score)
        with self.health.lock:
            self.health.candidates += len(cands)
        out: list[dict] = []
        for c in cands:
            if self.watchlist.already_flagged(c.listing.listing_id, c.recall.key):
                continue
            text_hash = hashlib.sha256(c.listing.text.encode()).hexdigest()
            if self.watchlist.already_judged(c.listing.listing_id, c.recall.key, text_hash):
                continue
            v = self.judge.judge(c)
            with self.health.lock:
                self.health.judged += 1
            flag_it, conf = decide(c, v)
            log.info("judged listing=%s recall=%s pre=%.2f judge=%s/%.2f flag=%s conf=%.2f signals=%s", c.listing.listing_id, c.recall.key, c.score, v.match, v.confidence, flag_it, conf, "; ".join(c.signals))
            if not flag_it:
                self.watchlist.mark_judged(c.listing.listing_id, c.recall.key, text_hash, False)
                continue
            flag = Flag(
                incident_id=c.recall.incident_id, flag_id=str(uuid.uuid4()), marketplace=c.listing.marketplace,
                listing_url=c.listing.url, listing_title=c.listing.title, seller=c.listing.seller, gtin=c.recall.gtin.lstrip("0") if c.gtin_hit else "",
                lot_code=c.lot_hit, observed_at=now_iso(), confidence=conf,
                evidence=(c.signals + v.reasons)[:8],
            )
            env = envelope(flag)
            try:
                self.publisher.publish(env)
            except Exception as e:  # noqa: BLE001
                with self.health.lock:
                    self.health.broker_connected = False
                log.error("publish failed: %s", e)
                break
            with self.health.lock:
                self.health.broker_connected = True
                self.health.flagged += 1
            self.watchlist.mark_flagged(c.listing.listing_id, c.recall.key, flag.flag_id, env["payload"])
            self.watchlist.mark_judged(c.listing.listing_id, c.recall.key, text_hash, True)
            out.append(env)
        return out
