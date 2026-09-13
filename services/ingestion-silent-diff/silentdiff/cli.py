"""ingestion-silent-diff command line.

    silentdiff run      [--interval 3600] [--dry-run]   poll every source forever, /healthz on PORT
    silentdiff once     [--dry-run]                     one snapshot+diff pass over every source, exit
    silentdiff replay   BEFORE.json AFTER.json [--dry-run]
                                                        diff two saved catalogs (fixtures or `probe` output)
    silentdiff probe    URL                             fetch a Shopify /products.json and print rows as JSON

Environment: RABBITMQ_URL (empty → --dry-run behaviour), DB_PATH, SOURCES
(path to sources.yaml), MIN_CONFIDENCE (0.5), OFF_ENRICH (1 to look up UPCs
on Open Food Facts), PORT (8085), LOG_LEVEL.
"""

from __future__ import annotations

import argparse
import json
import logging
import os
import signal
import sys
import threading
from datetime import datetime, timezone
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.parse import parse_qs, urlparse

import yaml

from . import events
from .enrich import OFFEnricher
from .pipeline import Health, Pipeline
from .sources import Row, Source, fetch
from .store import Store

log = logging.getLogger("silentdiff")


def load_env_file(path: str) -> None:
    """Tiny .env loader (no python-dotenv dependency)."""
    try:
        with open(path, encoding="utf-8") as f:
            for line in f:
                line = line.strip()
                if not line or line.startswith("#") or "=" not in line:
                    continue
                k, v = line.split("=", 1)
                os.environ.setdefault(k.strip(), v.strip().strip('"').strip("'"))
    except FileNotFoundError:
        pass


def load_sources(path: str) -> list[Source]:
    with open(path, encoding="utf-8") as f:
        doc = yaml.safe_load(f) or {}
    out = []
    for s in doc.get("sources", []):
        out.append(Source(name=s["name"], kind=s.get("kind", "shopify"), url=s["url"], brand=s.get("brand", "")))
    if not out:
        raise SystemExit(f"{path}: no sources defined")
    return out


def make_publisher(dry_run: bool):
    url = os.environ.get("RABBITMQ_URL", "")
    if dry_run or not url:
        if not dry_run:
            log.warning("RABBITMQ_URL not set: printing events instead of publishing")
        return events.StdoutPublisher()
    return events.AMQPPublisher(url)


def health_server(port: int, health: Health, store: Store) -> ThreadingHTTPServer:
    class H(BaseHTTPRequestHandler):
        def log_message(self, *a):  # quiet
            pass

        def do_GET(self):
            if self.path == "/healthz":
                ok = health.healthy()
                with health.lock:
                    body = {
                        "healthy": ok,
                        "broker_connected": health.broker_connected,
                        "started_at": health.started_at,
                        "store": store.counts(),
                        "sources": {k: v.__dict__ for k, v in health.sources.items()},
                    }
                self._json(200 if ok else 503, body)
            elif self.path == "/readyz":
                self._text(200, "ok\n")
            elif self.path.startswith("/v1/signals"):
                # Read API for the ops console: every catalog.sku.vanished.v1
                # this service published. ?source= narrows, ?limit= caps (100).
                q = parse_qs(urlparse(self.path).query)
                self._json(200, {"items": store.signals(q.get("source", [""])[0], int(q.get("limit", ["100"])[0] or 100))})
            elif self.path == "/metrics":
                ok = health.healthy()
                lines = [f"# TYPE soteria_silentdiff_up gauge", f"soteria_silentdiff_up {int(ok)}"]
                with health.lock:
                    for name, s in health.sources.items():
                        lines.append(f'soteria_silentdiff_consecutive_failures{{source="{name}"}} {s.consecutive_failures}')
                        lines.append(f'soteria_silentdiff_rows{{source="{name}"}} {s.last_row_count}')
                        lines.append(f'soteria_silentdiff_signals_emitted_total{{source="{name}"}} {s.signals_emitted}')
                self._text(200, "\n".join(lines) + "\n")
            else:
                self._text(404, "not found\n")

        def do_OPTIONS(self):
            self.send_response(204)
            self.send_header("Access-Control-Allow-Origin", "*")
            self.send_header("Access-Control-Allow-Methods", "GET, OPTIONS")
            self.send_header("Access-Control-Allow-Headers", "Content-Type")
            self.end_headers()

        def _json(self, code, body):
            data = json.dumps(body).encode()
            self.send_response(code)
            self.send_header("Content-Type", "application/json")
            # Read-only, no credentials: the ops console may call from any origin.
            self.send_header("Access-Control-Allow-Origin", "*")
            self.send_header("Content-Length", str(len(data)))
            self.end_headers()
            self.wfile.write(data)

        def _text(self, code, text):
            data = text.encode()
            self.send_response(code)
            self.send_header("Content-Type", "text/plain; version=0.0.4")
            self.send_header("Content-Length", str(len(data)))
            self.end_headers()
            self.wfile.write(data)

    srv = ThreadingHTTPServer(("", port), H)
    threading.Thread(target=srv.serve_forever, daemon=True).start()
    return srv


def _rows_from_file(path: str, source_name: str) -> tuple[list[Row], str]:
    """Accept either `probe` output ({"source":..,"taken_at":..,"rows":[..]}) or a raw
    Shopify products.json ({"products":[..]})."""
    with open(path, encoding="utf-8") as f:
        doc = json.load(f)
    if "rows" in doc:
        rows = [Row.from_dict(r) for r in doc["rows"]]
        for r in rows:
            r.source = source_name
        return rows, doc.get("taken_at", "")
    from .sources import parse_shopify_products

    src = Source(name=source_name, kind="shopify", url=doc.get("url", "https://example.invalid"))
    return parse_shopify_products(src, doc.get("products", [])), doc.get("taken_at", "")


def main(argv: list[str] | None = None) -> int:
    load_env_file(".env")
    load_env_file(os.path.join("..", "..", ".env"))
    logging.basicConfig(level=os.environ.get("LOG_LEVEL", "INFO").upper(), format='{"time":"%(asctime)s","level":"%(levelname)s","logger":"%(name)s","msg":%(message)r}')

    ap = argparse.ArgumentParser(prog="silentdiff", description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    sub = ap.add_subparsers(dest="cmd", required=True)
    for name in ("run", "once"):
        p = sub.add_parser(name)
        p.add_argument("--dry-run", action="store_true")
        p.add_argument("--sources", default=os.environ.get("SOURCES", "sources.yaml"))
        if name == "run":
            p.add_argument("--interval", type=float, default=float(os.environ.get("POLL_INTERVAL_SECONDS", "3600")))
    p = sub.add_parser("replay")
    p.add_argument("before")
    p.add_argument("after")
    p.add_argument("--source", default="fixture:replay")
    p.add_argument("--dry-run", action="store_true")
    p = sub.add_parser("probe")
    p.add_argument("url")
    p.add_argument("--name", default="")
    args = ap.parse_args(argv)

    min_conf = float(os.environ.get("MIN_CONFIDENCE", "0.5"))
    enricher = OFFEnricher() if os.environ.get("OFF_ENRICH", "") in ("1", "true", "yes") else None

    if args.cmd == "probe":
        src = Source(name=args.name or f"shopify:{args.url.split('//')[-1].strip('/')}", kind="shopify", url=args.url)
        rows = fetch(src)
        json.dump({"source": src.name, "url": src.url, "taken_at": datetime.now(timezone.utc).isoformat(timespec="seconds"), "rows": [r.to_dict() for r in rows]}, sys.stdout)
        sys.stdout.write("\n")
        return 0

    dry = getattr(args, "dry_run", False)
    if args.cmd == "replay":
        store = Store(":memory:")
        pub = make_publisher(dry)
        src = Source(name=args.source, kind="shopify", url="")
        pipe = Pipeline(store, pub, [src], min_confidence=min_conf, enricher=enricher)
        before, before_at = _rows_from_file(args.before, args.source)
        after, after_at = _rows_from_file(args.after, args.source)
        pipe.ingest(src, before, before_at or "2026-01-01T00:00:00Z")
        emitted = pipe.ingest(src, after, after_at or None)
        pub.close()
        log.info("replay emitted %d signal(s)", len(emitted))
        return 0

    store = Store(":memory:" if dry else os.environ.get("DB_PATH", os.path.join("data", "silent-diff.db")))
    pub = make_publisher(dry)
    sources = load_sources(args.sources)
    pipe = Pipeline(store, pub, sources, min_confidence=min_conf, enricher=enricher)
    if args.cmd == "once":
        pipe.tick()
        pub.close()
        return 0

    stop = threading.Event()
    for sig in (signal.SIGINT, signal.SIGTERM):
        signal.signal(sig, lambda *_: stop.set())
    srv = health_server(int(os.environ.get("PORT", "8085")), pipe.health, store)
    log.info("silent-diff running sources=%d interval=%ss port=%s", len(sources), args.interval, srv.server_port)
    try:
        pipe.run(args.interval, stop)
    finally:
        srv.shutdown()
        pub.close()
        store.close()
    return 0


if __name__ == "__main__":  # pragma: no cover
    sys.exit(main())
