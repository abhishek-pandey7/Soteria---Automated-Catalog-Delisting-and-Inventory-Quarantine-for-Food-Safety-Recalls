# democtl

Makes the recall demo repeatable against the live store and the real broker.

```sh
cd tools/democtl
go run . status                    # what is held / tagged / drafted right now
go run . reset --restart           # release every hold, clear tags + badges, restart the in-memory services
go run . order 085315054108 --qty 2   # a paid, unfulfilled TEST order so order-rescue has something to rescue
go run . inject H-1258-2026        # publish that real FDA notice on ingestion.x and watch the chain run
```

`inject` takes any case id from `services/recall-extractor/eval/cases.json`
(sixteen real notices) or a JSON file holding a flat `recall.raw.received.v1`.
Each injection gets a fresh `event_id`, so a re-run is a new delivery of the same
incident.

`reset` uses the same `libs/shopify/hold` adapter containment used, in
reverse, then removes whatever a partial run left behind (tags, badge, DRAFT).
`--restart` bounces resolution, containment, order-rescue and audit, which keep
incident state in memory; the notification and evasion ledgers are history and
are left alone.

`order` creates a Shopify **test** order (`--real` for a real one) for
`customer@example.com` (`--email` to change). The rescue scan looks at
unfulfilled orders from the last 30 days.

Env: `SHOPIFY_SHOP`, `SHOPIFY_ACCESS_TOKEN`, `SHOPIFY_API_VERSION`,
`QUARANTINE_LOCATION`, `RABBITMQ_URL`, read from the nearest `.env`.

## A full demo run

```sh
go run . reset --restart
go run . order 085315054108
go run . inject H-1258-2026
curl "localhost:8081/v1/lots/status?gtin=085315054108&lot_code=1226183"   # AFFECTED
curl localhost:8082/v1/containment/actions                                # AUTO_HELD, 40 units to Quarantine
curl localhost:8083/v1/rescues                                            # PROPOSED for order #1001
curl localhost:8087/v1/flags                                              # marketplace listings of the lot
curl localhost:8085/v1/dossiers/inc-fda_enforcement-h-1258-2026/verify    # verified: true
```
