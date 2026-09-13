# shopify

Sotería's shared client for the Shopify Admin **GraphQL** API (the REST Admin API is legacy). Owned by Person 1 (PRD §7 P1.D); consumed by Person 3's containment-service and order-rescue-service.

```go
import "soteria/libs/shopify"

c, err := shopify.New("soteria-dev.myshopify.com", os.Getenv("SHOPIFY_ACCESS_TOKEN"))
variants, err := c.VariantsByBarcode(ctx, []string{"012345678905"})
err = c.MoveAvailable(ctx, variants[0].InventoryItemID, mainLocID, quarantineLocID, 12, "quality_control")
```

**Depend on the `shopify.API` interface, not `*shopify.Client`**, so your service can run against `fake.Store` in tests and offline:

```go
store := fake.New()
store.AddLocation(...); store.AddVariant(...); store.AddOrder(...)
var api shopify.API = store
```

## Operations

| Method | Shopify operation | Used for |
|---|---|---|
| `Locations` | `locations` | find the Main and **Quarantine** locations |
| `Catalog` | `productVariants` (paginated) | snapshot for recall matching: id, sku, **barcode**, product status/tags, per-location available qty |
| `VariantsByBarcode` | `productVariants(query: "barcode:…")` | exact UPC/EAN lookup (server search is fuzzy; client keeps exact hits only) |
| `Variant` | `productVariant` | single lookup |
| `OrdersSince` | `orders(query: "created_at:>=…")` | affected-customer scan (line items + fulfillment status) |
| `SetAvailable` | `inventorySetQuantities` | full-SKU hold: available = 0 |
| `MoveAvailable` | `inventoryAdjustQuantities` (paired −N/+N; activates the destination on demand) | **lot-level quarantine**: move N units Main → Quarantine, leave the rest sellable |
| `SetProductStatus` | `productUpdate` | `DRAFT` unpublishes |
| `AddTags` / `RemoveTags` | `tagsAdd` / `tagsRemove` | `RECALL_HAZARD`, `RECALL_LOT:<code>` |
| `SetMetafields` | `metafieldsSet` | `soteria.badge` = "Verified Safe Lot …" for the storefront |
| `ReplaceLineItem` | `orderEditBegin → SetQuantity → AddVariant → Commit` | customer-confirmed substitution on an unfulfilled order |

Every mutation returns `*shopify.UserErrors` for Shopify validation failures (`errors.As`), `shopify.GraphQLErrors` for top-level errors (`ACCESS_DENIED` means a missing scope), and `*shopify.HTTPError` for non-GraphQL failures.

## Rate limiting

Shopify GraphQL is a **cost bucket**, not requests/second (Basic plan: 1000 points, refills 100/s). The client reads `extensions.cost.throttleStatus` on every response, waits before a request the bucket cannot afford, and retries `THROTTLED` and 5xx with backoff. Paginated reads use 100-node pages so a full catalog pull stays well inside the bucket.

## Required Admin API scopes

`read_products write_products read_inventory write_inventory read_locations read_orders write_orders read_customers`

Missing scope → `ACCESS_DENIED` naming the required scope.

## Verifying against the dev store

```sh
cd libs/shopify
SHOPIFY_SHOP=soteria-dev.myshopify.com SHOPIFY_ACCESS_TOKEN=shpat_... go run ./cmd/shopctl locations
go run ./cmd/shopctl catalog
go run ./cmd/shopctl barcode 012345678905
go run ./cmd/shopctl orders 7
# writes (explicit):
go run ./cmd/shopctl move gid://shopify/InventoryItem/… gid://shopify/Location/… gid://shopify/Location/… 12
```

`shopctl` also reads a `.env` in the current directory. The token is never logged.

## `hold` — the containment seam (for Person 3)

`hold.Adapter` implements `libs/core/shopify.InventoryClient` (`HoldLots` / `ReleaseLots`), so the containment-service plugs the real store in with three lines:

```go
sel, quar, err := hold.ResolveLocations(ctx, client, "Quarantine")
inv := hold.New(client, sel, quar)          // satisfies core/shopify.InventoryClient
svc := containment.New(store, inv, bus, log)
```

How a hold is executed — Shopify has **no native lot tracking**, so:

| Piece | Where it lives | What the adapter does |
|---|---|---|
| Lot ledger | variant metafield `soteria.lots` (JSON: `[{"code":"8H-1132","units":40,"expiry":"2027-03","held":false}]`) | reads it via `Variant.Lots`; marks `held` on hold/release |
| Quarantine | second Shopify location named **Quarantine** | `inventoryAdjustQuantities` −N selling / +N Quarantine for the held lots' units (capped at physical stock); reversed on release |
| Storefront signal | product tags + `soteria.badge` metafield + status | partial hold: `RECALL_LOT:<code>` tags + "Verified Safe Lot" badge, product stays ACTIVE; full hold (all lots, or no ledger, or no `LotCodes`): `RECALL_HAZARD` + DRAFT; release reverses |

Rules: unknown lot codes → error (never silently hold nothing); barcode matching multiple variants → error; unknown barcode → SKU fallback; a failed move leaves the ledger untouched; repeat holds are idempotent.

**Person 2 seeding checklist:** every variant needs `barcode` (GTIN) and, for lot precision, the `soteria.lots` metafield; the store needs a location named `Quarantine`.

Other notes: `ReplaceLineItem` only works on unfulfilled orders and must be called **after** the customer confirmed the substitute (PRD: never silently substitute). API version defaults to `2026-01`; override with `shopify.WithAPIVersion` or `SHOPIFY_API_VERSION`.
