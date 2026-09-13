# ingestion-silent-diff

Silent-recall detection (PRD §7 P1.B, Python). A meaningful share of contamination events never get a formal notice: the manufacturer just pulls the product. This service snapshots manufacturer catalogs on a schedule, diffs consecutive snapshots, and publishes `catalog.sku.vanished.v1` for SKUs that disappeared — with a confidence that separates "quietly withdrawn" from routine housekeeping.

## Real data, no keys

Every public Shopify storefront exposes `/products.json` (products, variants, SKUs, vendor, availability, timestamps). Thousands of food brands sell direct on Shopify; `sources.yaml` ships with eight, all verified live. Non-Shopify brands can be watched via `kind: sitemap` (product URLs). Barcodes are not public, so `OFF_ENRICH=1` optionally looks vanished SKUs up on Open Food Facts (rate-limited, cached) to add a `upc`.

## Confidence

| Signal | Effect |
|---|---|
| variant missing from new snapshot | base 0.50 |
| whole product gone (every size/flavour) | +0.20 |
| was in stock when it vanished | +0.15 |
| vendor still lists other products | +0.10 |
| near-identical new listing appeared (≥90 fuzzy) | **cap 0.35** — a rename, not a withdrawal |
| similar new listing (≥75) | −0.15 |
| seasonal / limited-edition wording | −0.15 |
| listed < 14 days | −0.10 |

Only signals ≥ `MIN_CONFIDENCE` (0.5) are published. A snapshot that shrank by more than 40% is treated as a fetch anomaly (bot wall, outage, pagination glitch) and produces nothing — one missed hour beats hundreds of false alarms. Each `(source, key)` is reported once; if the item reappears and vanishes again, it is reported again.

Fixture (`fixtures/lesserevil_*.json`, derived from a live probe) exercises all four cases:

```
0.95  Fiery Hot Organic Popcorn            whole in-stock product withdrawn
0.75  Himalayan Gold ... Single / 14oz     one size pulled, product still sold
0.55  Spooky Space Balls Snack Pack        seasonal, out of stock
0.35  Cowboy Cheddar Cheezmos              renamed to "... Puffs" → suppressed
```

## Run

```sh
pip install -r requirements-dev.txt
python -m pytest -q

python -m silentdiff replay fixtures/lesserevil_before.json fixtures/lesserevil_after.json --dry-run   # print events
python -m silentdiff once --dry-run                          # live snapshot of every source (first run only stores)
python -m silentdiff run --interval 3600                     # poll hourly, publish to RABBITMQ_URL, /healthz on :8085
python -m silentdiff probe https://www.chomps.com > snap.json  # dump one catalog (feed it to replay later)
```

Config: `RABBITMQ_URL`, `DB_PATH`, `SOURCES`, `MIN_CONFIDENCE`, `OFF_ENRICH`, `POLL_INTERVAL_SECONDS`, `PORT`, `LOG_LEVEL` (a `.env` here or at the repo root is read).

Output: envelope-wrapped `ingestion.catalog.sku.vanished.v1` on `ingestion.x`, persistent, publisher confirms. The resolution-service already binds to it (`resolution.recall-raw` queue).

## Health

`/healthz` → 503 when any source has failed 3 consecutive fetches or the broker rejected a publish; per-source `last_success_at`, `last_row_count`, `signals_emitted`, `last_anomaly`. `/metrics` in Prometheus text.

## Read API

`GET /v1/signals?source=&limit=` returns every `catalog.sku.vanished.v1` this
service published (payloads, newest first) for the ops console. CORS is open:
read-only, no credentials.
