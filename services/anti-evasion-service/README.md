# anti-evasion-service

Recall arbitrage detection (PRD Layer 4, P1.C). When a lot is recalled, someone buys it cheap and resells it on liquidation and resale marketplaces that never saw the notice. This service watches those marketplaces for the recalled lots and raises `evasion.flagged.v1` — the notification-service turns it into an ops alert, the audit service records it.

```
containment.action.taken.v1 ──▶ watch-list (GTIN, lot codes, best-by dates, recall date)
marketplace listings ─────────▶ pre-filter (rules) ──▶ LLM judge ──▶ guardrail ──▶ evasion.flagged.v1
```

## Two-stage matching

**Stage 1 — deterministic pre-filter** (`match.py`), explainable and free:

| signal | weight |
|---|---|
| recalled GTIN in the listing (barcode field or text) | +0.60 |
| recalled lot code verbatim in title/description/photo text | +0.35 |
| recalled best-by / expiry date in the listing (dates normalised across formats) | +0.25 |
| brand present | +0.15 |
| title similarity × 0.30 | ≤ +0.30 |
| listing posted **before** the recall date | −0.50 |

**Stage 2 — LLM judge** (`judge.py`, Groq `openai/gpt-oss-120b`, JSON mode, cached) sees the recall record and the listing side by side and answers *is this the recalled lot?* — not merely the same product — with reasons and verbatim evidence.

**Guardrail** (`decide`): hard evidence (GTIN or lot code verbatim) flags regardless of the judge, unless the listing predates the recall; soft evidence (resemblance / matching date) flags only when the judge is ≥ 0.75 confident and the pre-score was already plausible. The judge cannot flag what the rules found implausible; evidence quotes must be verbatim from the listing.

Fixture (`fixtures/listings/`), against three real Sept-2026 recalls, with the live judge:

```
0.99  Outshine Watermelon bars     lot LLA618203 in photo text + best-by → flag
0.97  bettergoods pistachio butter lot LB028ACP04 in description       → flag
0.92  Sun Noodle Sura Tanmen       production lot 1226183              → flag
0.82  bettergoods pistachio butter no lot; expiry = recalled lot's     → flag (judge + date)
0.69  pistachio butter, UPC only   no lot info at all                  → flag, lower
 —    bettergoods pistachio "best by Aug 2027, new lot"                → rejected by the judge
 —    bettergoods ALMOND butter    same brand, different product       → rejected
 —    Outshine listed in July      before the recall                   → never a candidate
```

## Sources

`sources.yaml`: `file` (listing exports / mocked feed folder), `shopify` (a discount grocer's public `/products.json` — real, keyless), `ebay` (Browse API; free developer account, `EBAY_CLIENT_ID` / `EBAY_CLIENT_SECRET`). There is no free liquidation-marketplace API; the `file` source is how exports from B-Stock, Liquidation.com or a broker's spreadsheet get in.

## Run

```sh
pip install -r requirements-dev.txt && python -m pytest -q
python -m antievasion replay fixtures/listings/liquidation_2026-09-12.json --dry-run    # rules + LLM if GROQ_API_KEY set
python -m antievasion once --dry-run
python -m antievasion run            # poll sources; consume containment events into the watch-list; /healthz on :8087
```

Config: `RABBITMQ_URL`, `GROQ_API_KEY` (without it: rules-only judge), `GROQ_MODEL`, `DB_PATH`, `SOURCES`, `WATCHLIST`, `MIN_SCORE`, `POLL_INTERVAL_SECONDS`, `PORT`.

Every (listing text, recall) verdict is remembered, so an unchanged listing is never re-judged; a flagged listing is reported once. Publish failures leave nothing marked, so the next poll retries.

## Read API

`GET /v1/flags?incident_id=&limit=` returns every `evasion.flagged.v1` this
service published (payloads, newest first); `GET /v1/watchlist` the recalls it
is hunting for. CORS is open: read-only, no credentials.
