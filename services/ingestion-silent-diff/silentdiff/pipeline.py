"""snapshot → diff → dedup → enrich → publish, per source."""

from __future__ import annotations

import logging
import threading
import time
from dataclasses import dataclass, field
from datetime import datetime, timezone

from . import events
from .diff import DiffResult, diff
from .enrich import OFFEnricher
from .sources import FetchError, Row, Source, fetch
from .store import Store

log = logging.getLogger("silentdiff.pipeline")


@dataclass
class SourceState:
    last_success_at: str = ""
    last_error: str = ""
    consecutive_failures: int = 0
    last_row_count: int = 0
    signals_emitted: int = 0
    last_anomaly: str = ""


@dataclass
class Health:
    started_at: str = field(default_factory=lambda: datetime.now(timezone.utc).isoformat(timespec="seconds"))
    sources: dict[str, SourceState] = field(default_factory=dict)
    broker_connected: bool = True
    lock: threading.Lock = field(default_factory=threading.Lock)

    def healthy(self, max_failures: int = 3) -> bool:
        with self.lock:
            return self.broker_connected and all(s.consecutive_failures < max_failures for s in self.sources.values())


class Pipeline:
    def __init__(
        self,
        store: Store,
        publisher,
        sources: list[Source],
        min_confidence: float = 0.5,
        enricher: OFFEnricher | None = None,
        health: Health | None = None,
    ):
        self.store = store
        self.publisher = publisher
        self.sources = sources
        self.min_confidence = min_confidence
        self.enricher = enricher
        self.health = health or Health()
        for s in sources:
            self.health.sources.setdefault(s.name, SourceState())

    # ---- one tick over every source

    def tick(self) -> list[dict]:
        emitted: list[dict] = []
        for src in self.sources:
            st = self.health.sources[src.name]
            try:
                rows = fetch(src)
            except FetchError as e:
                with self.health.lock:
                    st.consecutive_failures += 1
                    st.last_error = str(e)
                log.error("fetch failed source=%s err=%s", src.name, e)
                continue
            emitted.extend(self.ingest(src, rows))
        return emitted

    def ingest(self, src: Source, rows: list[Row], taken_at: str | None = None) -> list[dict]:
        """Store a snapshot and diff it against the previous one."""
        st = self.health.sources.setdefault(src.name, SourceState())
        sid = self.store.save_snapshot(src.name, rows, taken_at)
        snaps = self.store.latest_snapshots(src.name, 2)
        with self.health.lock:
            st.consecutive_failures = 0
            st.last_error = ""
            st.last_success_at = snaps[0][1]
            st.last_row_count = len(rows)
        self.store.prune(src.name)
        if len(snaps) < 2:
            log.info("first snapshot stored source=%s rows=%d (nothing to diff yet)", src.name, len(rows))
            return []
        (after_id, after_at), (before_id, before_at) = snaps[0], snaps[1]
        before, after = self.store.rows(before_id), self.store.rows(after_id)
        return self.emit(src, diff(src.name, before, after, before_at, after_at))

    def emit(self, src: Source, res: DiffResult) -> list[dict]:
        st = self.health.sources.setdefault(src.name, SourceState())
        if res.anomaly:
            with self.health.lock:
                st.last_anomaly = res.anomaly
            log.warning("diff suppressed source=%s reason=%s", src.name, res.anomaly)
            return []
        # Keys that came back are eligible to be reported again if they vanish later.
        self.store.clear_emitted(src.name, [r.key for r in res.appeared])
        out: list[dict] = []
        for v in res.vanished:
            if v.confidence < self.min_confidence:
                log.info("below threshold source=%s key=%s conf=%.2f reasons=%s", src.name, v.row.key, v.confidence, v.reasons)
                continue
            if self.store.already_emitted(src.name, v.row.key):
                continue
            upc = ""
            if self.enricher:
                upc = self.enricher.upc_for(src.brand or v.row.vendor, v.row.title)
            env = events.envelope(events.payload(v, upc=upc, brand=src.brand))
            try:
                self.publisher.publish(env)
            except Exception as e:  # noqa: BLE001 — broker down: do not mark emitted; next tick retries
                with self.health.lock:
                    self.health.broker_connected = False
                log.error("publish failed source=%s key=%s err=%s", src.name, v.row.key, e)
                break
            with self.health.lock:
                self.health.broker_connected = True
                st.signals_emitted += 1
            self.store.mark_emitted(src.name, v.row.key, env["event_id"], env["payload"])
            log.info("vanished source=%s sku=%s title=%r conf=%.2f reasons=%s", src.name, v.row.sku, v.row.title, v.confidence, "; ".join(v.reasons))
            out.append(env)
        log.info("diff source=%s before=%d after=%d vanished=%d appeared=%d emitted=%d", src.name, res.before_count, res.after_count, len(res.vanished), len(res.appeared), len(out))
        return out

    # ---- long-running loop

    def run(self, interval: float, stop: threading.Event) -> None:
        while not stop.is_set():
            started = time.monotonic()
            try:
                self.tick()
            except Exception as e:  # noqa: BLE001
                log.exception("tick failed: %s", e)
            elapsed = time.monotonic() - started
            stop.wait(max(1.0, interval - elapsed))
