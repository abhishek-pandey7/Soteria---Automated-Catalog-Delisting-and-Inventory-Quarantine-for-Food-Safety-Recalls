# infra

Everything needed to run Sotería as a system rather than as a set of processes.

```sh
cd infra
docker compose up -d            # broker + every service
docker compose logs -f rabbitmq # watch the topology import
```

`infra/.env` (git-ignored; start from `infra/.env.example`) supplies
`RABBITMQ_USER` / `RABBITMQ_PASSWORD` — compose refuses to start without the
password, there is no default — and optionally `SHOPIFY_SHOP` / `SHOPIFY_ACCESS_TOKEN` / `QUARANTINE_LOCATION`
(without them the domain services run on fixtures), `GROQ_API_KEY` (without it
the extractor and the evasion judge use deterministic fakes), and
`SLACK_WEBHOOK_URL` / `RESEND_API_KEY` (without them notifications are logged).

| Port | What |
|---|---|
| 5672 | AMQP |
| 15672 | RabbitMQ management UI (credentials from `infra/.env`) |
| 8081–8085 | resolution, containment, order rescue, notification, audit |
| 8086–8088 | recall extractor, anti-evasion, silent-diff |
| 8091 | ingestion-fda (health/metrics) |

On Windows with Docker Desktop, BuildKit rejects a repo path containing
non-ASCII characters (`Sotería`); build with the classic builder instead:
`DOCKER_BUILDKIT=0 COMPOSE_DOCKER_CLI_BUILD=0 docker compose build`.

## Replaying a real recall

`ingestion-fda` starts polling openFDA and the FDA press feed as soon as the
broker is up, so notices flow on their own. To push one specific notice through
the chain, publish a `recall.raw.received.v1` on `ingestion.x` with routing
key `ingestion.recall.raw.received.v1` (the extractor's `eval/cases.json` has
sixteen real ones) and follow it:

```sh
curl "localhost:8081/v1/lots/status?gtin=085315054108&lot_code=1226183"   # AFFECTED
curl localhost:8082/v1/containment/actions                                # AUTO_HELD, 40 units
curl localhost:8084/v1/incidents/inc-fda_enforcement-h-1258-2026/deliveries
curl localhost:8085/v1/dossiers/inc-fda_enforcement-h-1258-2026/verify    # verified: true
```

## The topology is declarative

`rabbitmq/definitions.json` is imported at boot, so every exchange, queue,
dead-letter pair and binding exists before any service connects. Services still
declare their own queues idempotently on startup, which means a service can be
deployed without an infra change — and it means the two declarations can drift.

`rabbitmq/topology_test.go` compares three sources that must agree: the registry
in `contracts/rabbitmq-topology.md`, these definitions, and what the services
actually subscribe to. It also enforces the properties the audit trail depends
on: every consumer queue is durable, has a dead-letter exchange, and has a bound
`.dlq` to catch what lands there; and `audit.ledger` is bound to every exchange
that carries incident events, because a dossier assembled from a subset is
evidence of nothing.

```sh
cd infra && go test ./...      # no broker needed
```

## Proving it against a real broker

The in-process bus implements the same routing rules, but cannot tell you whether
queues really get declared, whether bindings match the keys services publish on,
or whether a failing handler actually dead-letters. `tests/integration` does:

```sh
docker compose up -d rabbitmq
RABBITMQ_URL=amqp://$RABBITMQ_USER:$RABBITMQ_PASSWORD@localhost:5672/ go test ./tests/integration/...
```

Without `RABBITMQ_URL` those tests skip, so CI without a broker stays green.

## Credentials

Nothing here touches a real store or sends a real message unless you supply
credentials. Copy `.env.example` to `infra/.env` and set what you need:

| Variable | Effect when unset |
|---|---|
| `RABBITMQ_USER`, `RABBITMQ_PASSWORD` | a development account is created; fine on a laptop, never elsewhere |
| `SHOPIFY_SHOP`, `SHOPIFY_ACCESS_TOKEN` | services run on bundled fixtures and log that their holds are not real |
| `CONSENT_SECRET` | order rescue starts with a development secret and warns |
| `TSA_URL` | dossiers are timestamped by DigiCert's public authority |
| `TSA_DISABLED=1` | dossiers are hash-chained but not anchored in time |

Setting only one half of a Shopify pair is a startup failure, not a silent
downgrade: a service that believes it is writing to a store while running on
fixtures would publish containment events describing holds that never happened.

Before deploying anywhere shared, read `docs/security-review.md`. The defaults
here are chosen for a laptop.
