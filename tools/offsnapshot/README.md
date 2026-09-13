# offsnapshot

Builds a committed snapshot of real Open Food Facts products, so the storefront can show real
allergen data without calling OFF on every page load.

```sh
cd tools/offsnapshot
GOWORK=off go run . -count 50
GOWORK=off go run . -count 50 -categories biscuits,soups -verbose
GOWORK=off go test ./...
```

`GOWORK=off` is needed because `tools/` is deliberately outside the root `go.work`, the same as
`tools/seedshop` and `tools/demo`.

Output: `allergens.snapshot.json`, an array in exactly the shape of the storefront's
`ProductAllergens` (`apps/storefront/src/api/allergens.ts`), so it deserialises there with no new
type.

## What gets kept

A product is only useful to the rescue flow if someone actually recorded its allergens. Five things
must all be true; anything else is rejected and counted:

| Requirement | Why |
|---|---|
| barcode present and **check-digit valid** | Validated with `libs/core/matching.NormalizeGTIN`, the same function resolution-service and seedshop use. A product no recall could match is not worth storing. |
| **non-empty** `allergens_tags` **or** `traces_tags` | The whole point — see below. |
| non-empty `ingredients_text` | The storefront shows it. |
| a `product_name` | A row with no name is not something to put in front of a shopper. |
| not already seen | Deduplicated on the normalised GTIN, so the same product under a different printed width is one entry. |

**An empty `allergens_tags` is never COMPLETE.** OFF returns the same empty array for "a contributor
checked and there are none" and for "nobody has filled this product in". The two payloads are
byte-identical, so an empty array can never be read as a verified absence. This is the rule in
`apps/storefront/src/api/openFoodFacts.ts`, transliterated in `filter.go` and pinned by
`TestEmptyAllergensIsNotComplete`; the two must not drift.

Because the filter demands non-empty tags, **every product in the snapshot is `COMPLETE`**. The
coverage check still runs, and a product that somehow came out `PARTIAL` or `ABSENT` would be
rejected — the invariant is enforced rather than assumed.

English `ingredients_text_en` is preferred when OFF has one, falling back to `ingredients_text`.

## Recalled products

```sh
GOWORK=off go run . -count 50 -seed ../seedshop/seed.json
```

With `-seed`, every barcode in a seedshop seed file is looked up individually and merged into the
snapshot **alongside** the clean catalogue.

These deliberately **skip the filter above**. The clean catalogue is a pool of substitutes, so it is
restricted to COMPLETE: offering a replacement whose allergens nobody recorded is the failure the
whole system exists to prevent. A recalled product is the opposite case — it is being withdrawn, not
offered, and it has to be in the file whatever OFF knows about it. Drop it and the storefront cannot
tell *"recalled, and we have no allergen data"* from *"not recalled"*: both are a missing row.

Coverage is still computed by the same rule, so an entry never claims to know more than it does:

| OFF's answer | Coverage |
|---|---|
| allergens or traces recorded | `COMPLETE` |
| only ingredients text | `PARTIAL` |
| found, but nothing usable | `ABSENT` |
| `status: 0` — not in OFF at all | `ABSENT`, entry is a bare GTIN |

A product OFF does not have is stored as a GTIN, a coverage of `ABSENT` and an empty allergens
array, and nothing else. The seed's own title is **not** copied in: the record declares
`source: OPEN_FOOD_FACTS`, and every field in it has to have actually come from there.

Dedup is on the normalised GTIN, so a recalled barcode already in the clean set is not stored twice.

The report lists each barcode with **both** the seed's title and OFF's, because OFF is keyed on
barcode alone. A barcode that has been reused, or mistyped into the seed, produces a confident
answer about the wrong product that looks exactly like a right one — the two columns side by side
are the cheapest way to see it. On the current seed, two rows show this: `4056489125846` is
shortbread in the seed and Austrian apricot biscuits in OFF, and the placeholder `012345678905` is
an unrelated French product.

## Categories

Default: `biscuits, soups, ice-creams, noodles, canned-fish, prepared-meats` — chosen to mirror the
recalled products in `tools/seedshop/seed.example.json` (noodles, ice cream, fish, a supplement), so
a substitute the rescue flow offers is plausibly the same kind of thing as the item it replaces.

Categories are walked round-robin with a per-pass quota of `count / categories`, so one well-stocked
shelf cannot fill the whole target before the others are asked. Without that, a run of 50 comes back
as 50 biscuits.

## The report

Every run prints the funnel: candidates fetched, kept, rejected, and rejections by reason. A filter
this strict is only trustworthy if it says how much it threw away.

Per category it distinguishes three different silences, which look identical in a bare zero:

- `(OFF did not answer …)` — retries were spent on 429/5xx. The snapshot is thin here, not the shelf.
- `(not reached — target met first)` — the run finished before asking.
- `(nothing kept — check the tag exists on OFF)` — asked, answered, nothing usable. Usually a
  category tag that has drifted.

## Notes

- OFF is rate-limited and sheds load freely. `-delay` (default 700ms) paces requests and `-retries`
  (default 3) backs off on 429/5xx. A 503 means "ask again", not "this category is empty".
- `states_tags=en:ingredients-completed` is sent as a hint only. Every product is still filtered
  locally, because the state flag and the actual fields disagree often enough to matter.
- **OFF is contributor-edited and occasionally contains test edits.** One product in the first run
  came back with an `ingredients_text` of `实验-Ingredients-EN` — someone's scratch edit, not an
  ingredient list — and was removed from the snapshot by hand. There is no filter for this: the
  obvious mechanical rules (a minimum length, requiring Latin-script characters) do not catch a
  mixed-script string like that one, and on a real batch they rejected a legitimate Arabic entry and
  no junk at all. **Eyeball the snapshot after a regenerate** rather than trusting a rule to do it.
  Sorting by `ingredients_text` length surfaces most of what is worth a second look.
- `gtin` is stored **normalised to GTIN-14**, the repo's canonical form. A consumer looking a product
  up by a 12- or 13-digit barcode must normalise the query first; string equality against the printed
  width will not match.
- The snapshot is committed on purpose. It is a point-in-time copy: `fetched_at` records when, and
  re-running overwrites it.
