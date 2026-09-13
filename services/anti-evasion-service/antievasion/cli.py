"""anti-evasion-service command line.

    antievasion run     [--interval 3600] [--dry-run]     poll marketplaces forever; consume containment events
                                                          from RabbitMQ into the watch-list; /healthz on PORT
    antievasion once    [--dry-run]                       one pass over every source, exit
    antievasion replay  LISTINGS.json [--dry-run]         judge a listings file against the watch-list, exit
    antievasion judge   --recall K --listing L            judge one pair interactively (prints the verdict)

Environment: RABBITMQ_URL (empty → in-process; --dry-run behaviour), GROQ_API_KEY (LLM judge; without it a
rules-only judge is used), GROQ_MODEL, DB_PATH, SOURCES (sources.yaml), WATCHLIST (watchlist.yaml, seeded on
boot), MIN_SCORE (0.35), PORT (8087), EBAY_CLIENT_ID/EBAY_CLIENT_SECRET (optional), LOG_LEVEL.
"""

from __future__ import annotations

import argparse
import json
import logging
import os
import signal
import sys
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

import yaml

from .judge import GroqJudge, Judge
from .pipeline import AMQPPublisher, Health, Pipeline, StdoutPublisher, Watchlist
from .sources import SourceSpec, load_listings

log = logging.getLogger("antievasion")


def load_env_file(path: str) -> None:
    try:
        with open(path, encoding="utf-8") as f:
            for line in f:
                line = line.strip()
                if line and not line.startswith("#") and "=" in line:
                    k, v = line.split("=", 1)
                    os.environ.setdefault(k.strip(), v.strip().strip('"').strip("'"))
    except FileNotFoundError:
        pass


def load_sources(path: str) -> list[SourceSpec]:
    with open(path, encoding="utf-8") as f:
        doc = yaml.safe_load(f) or {}
    return [SourceSpec(name=s["name"], kind=s.get("kind", "file"), url=s.get("url", ""), query=s.get("query", "")) for s in doc.get("sources", [])]


def make_judge(db_path: str) -> Judge:
    key = os.environ.get("GROQ_API_KEY", "")
    if not key:
        log.warning("GROQ_API_KEY not set: rules-only judge (GTIN / lot hits flag; no LLM adjudication)")
        return Judge()
    return GroqJudge(key, os.environ.get("GROQ_MODEL", "openai/gpt-oss-120b"), cache_path=db_path.replace(".db", "-judge.db") if db_path != ":memory:" else ":memory:")


def make_publisher(dry: bool):
    url = os.environ.get("RABBITMQ_URL", "")
    if dry or not url:
        if not dry:
            log.warning("RABBITMQ_URL not set: printing events instead of publishing")
        return StdoutPublisher()
    return AMQPPublisher(url)


def consume_containment(url: str, watchlist: Watchlist, stop: threading.Event) -> None:
    """Subscribe to containment.action.taken.v1 and grow the watch-list."""
    import pika

    while not stop.is_set():
        try:
            params = pika.URLParameters(url)
            params.heartbeat = 30
            conn = pika.BlockingConnection(params)
            ch = conn.channel()
            ch.exchange_declare(exchange="containment.x", exchange_type="topic", durable=True)
            ch.exchange_declare(exchange="evasion.dlx", exchange_type="topic", durable=True)
            # Same shape as every other consumer queue (libs/core/bus and
            # infra/rabbitmq/definitions.json): quorum, so the broker counts
            # deliveries and dead-letters after 5, with a .dlq that catches them.
            ch.queue_declare(queue="evasion.containment-taken.dlq", durable=True)
            ch.queue_bind(queue="evasion.containment-taken.dlq", exchange="evasion.dlx", routing_key="#")
            ch.queue_declare(queue="evasion.containment-taken", durable=True, arguments={
                "x-dead-letter-exchange": "evasion.dlx", "x-queue-type": "quorum", "x-delivery-limit": 5})
            ch.queue_bind(queue="evasion.containment-taken", exchange="containment.x", routing_key="containment.action.taken.v1")

            def on_msg(chan, method, props, body):
                try:
                    env = json.loads(body)
                    payload = env.get("payload") or env
                    n = watchlist.add_from_containment(payload, env.get("occurred_at", ""))
                    log.info("watch-list += %d from incident %s", n, payload.get("incident_id"))
                    chan.basic_ack(method.delivery_tag)
                except Exception as e:  # noqa: BLE001
                    log.error("bad containment event: %s", e)
                    chan.basic_reject(method.delivery_tag, requeue=False)

            ch.basic_consume(queue="evasion.containment-taken", on_message_callback=on_msg)
            log.info("consuming containment.action.taken.v1 into the watch-list")
            while not stop.is_set():
                conn.process_data_events(time_limit=1)
            conn.close()
        except Exception as e:  # noqa: BLE001
            log.warning("containment consumer error: %s; retrying in 5s", e)
            stop.wait(5)


def health_server(port: int, health: Health, watchlist: Watchlist) -> ThreadingHTTPServer:
    class H(BaseHTTPRequestHandler):
        def log_message(self, *a):
            pass

        def do_GET(self):
            if self.path == "/healthz":
                ok = health.healthy()
                with health.lock:
                    body = {k: v for k, v in health.__dict__.items() if k != "lock"}
                body.update(healthy=ok, watchlist=len(watchlist.all()))
                self._send(200 if ok else 503, "application/json", json.dumps(body))
            elif self.path == "/readyz":
                self._send(200, "text/plain", "ok\n")
            elif self.path == "/metrics":
                ok = health.healthy()
                with health.lock:
                    h = health
                    txt = (f"# TYPE soteria_antievasion_up gauge\nsoteria_antievasion_up {int(ok)}\n"
                           f"soteria_antievasion_listings_seen_total {h.listings_seen}\nsoteria_antievasion_candidates_total {h.candidates}\n"
                           f"soteria_antievasion_judged_total {h.judged}\nsoteria_antievasion_flagged_total {h.flagged}\n")
                self._send(200, "text/plain; version=0.0.4", txt)
            else:
                self._send(404, "text/plain", "not found\n")

        def _send(self, code, ctype, text):
            data = text.encode()
            self.send_response(code)
            self.send_header("Content-Type", ctype)
            self.send_header("Content-Length", str(len(data)))
            self.end_headers()
            self.wfile.write(data)

    srv = ThreadingHTTPServer(("", port), H)
    threading.Thread(target=srv.serve_forever, daemon=True).start()
    return srv


def main(argv: list[str] | None = None) -> int:
    load_env_file(".env")
    load_env_file(os.path.join("..", "..", ".env"))
    logging.basicConfig(level=os.environ.get("LOG_LEVEL", "INFO").upper(), format='{"time":"%(asctime)s","level":"%(levelname)s","logger":"%(name)s","msg":%(message)r}')

    ap = argparse.ArgumentParser(prog="antievasion", description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    sub = ap.add_subparsers(dest="cmd", required=True)
    for name in ("run", "once"):
        p = sub.add_parser(name)
        p.add_argument("--dry-run", action="store_true")
        p.add_argument("--sources", default=os.environ.get("SOURCES", "sources.yaml"))
        p.add_argument("--watchlist", default=os.environ.get("WATCHLIST", "fixtures/watchlist.yaml"))
        if name == "run":
            p.add_argument("--interval", type=float, default=float(os.environ.get("POLL_INTERVAL_SECONDS", "3600")))
    p = sub.add_parser("replay")
    p.add_argument("listings")
    p.add_argument("--watchlist", default=os.environ.get("WATCHLIST", "fixtures/watchlist.yaml"))
    p.add_argument("--dry-run", action="store_true")
    args = ap.parse_args(argv)

    dry = getattr(args, "dry_run", False)
    db_path = ":memory:" if dry or args.cmd == "replay" else os.environ.get("DB_PATH", os.path.join("data", "anti-evasion.db"))
    watchlist = Watchlist(db_path)
    if args.watchlist and os.path.exists(args.watchlist):
        log.info("watch-list seeded with %d recalls from %s", watchlist.load_yaml(args.watchlist), args.watchlist)
    judge = make_judge(db_path)
    pub = make_publisher(dry)
    min_score = float(os.environ.get("MIN_SCORE", "0.35"))

    if args.cmd == "replay":
        listings = load_listings(args.listings)
        pipe = Pipeline(watchlist, judge, pub, [], min_score)
        flagged = pipe.process(listings)
        pub.close()
        log.info("replay: listings=%d candidates=%d judged=%d flagged=%d", len(listings), pipe.health.candidates, pipe.health.judged, len(flagged))
        return 0

    sources = load_sources(args.sources)
    pipe = Pipeline(watchlist, judge, pub, sources, min_score)
    if args.cmd == "once":
        flagged = pipe.tick()
        pub.close()
        log.info("once: flagged=%d", len(flagged))
        return 0

    stop = threading.Event()
    for sig in (signal.SIGINT, signal.SIGTERM):
        signal.signal(sig, lambda *_: stop.set())
    if url := os.environ.get("RABBITMQ_URL", ""):
        threading.Thread(target=consume_containment, args=(url, watchlist, stop), daemon=True).start()
    srv = health_server(int(os.environ.get("PORT", "8087")), pipe.health, watchlist)
    log.info("anti-evasion running sources=%d interval=%ss port=%s judge=%s", len(sources), args.interval, srv.server_port, judge.name)
    try:
        while not stop.is_set():
            started = time.monotonic()
            try:
                pipe.tick()
            except Exception as e:  # noqa: BLE001
                log.exception("tick failed: %s", e)
            stop.wait(max(1.0, args.interval - (time.monotonic() - started)))
    finally:
        srv.shutdown()
        pub.close()
    return 0


if __name__ == "__main__":  # pragma: no cover
    sys.exit(main())
