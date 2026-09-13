import snapshot from './allergens.snapshot.json'
import type { ProductAllergens } from './allergens'

/**
 * Allergen data read from a committed snapshot of Open Food Facts, taken by
 * tools/offsnapshot. Nothing here touches the network.
 *
 * The snapshot holds two different populations and the difference matters:
 *
 *   - the clean catalogue, filtered to COMPLETE, because it is the pool the
 *     rescue flow draws substitutes from;
 *   - every product in the seedshop seed, kept whatever OFF knows about it,
 *     because a recalled product missing from the file would be indistinguishable
 *     from one that was never recalled. Those entries can be ABSENT with no
 *     product name and no allergens. That is a real answer, not a broken row.
 */

const entries = snapshot as ProductAllergens[]

/**
 * normalizeGtin converts any GTIN-8/12/13/14 to zero-padded GTIN-14, returning
 * "" when the input is not a valid GTIN (check digit included).
 *
 * This is a port of libs/core/matching.NormalizeGTIN, and it has to stay one.
 * The snapshot is keyed on the 14-digit form while the storefront passes the
 * 12-digit barcode printed on a pack; comparing those as strings never matches,
 * and a lookup that never matches returns ABSENT for everything — which on screen
 * is indistinguishable from Open Food Facts having no data at all. So both sides
 * of the lookup are normalised, never just one.
 */
export function normalizeGtin(raw: string): string {
    const digits = raw.replace(/\D/g, '')
    if (![8, 12, 13, 14].includes(digits.length)) return ''
    if (!hasValidCheckDigit(digits)) return ''
    return digits.padStart(14, '0')
}

function hasValidCheckDigit(digits: string): boolean {
    let sum = 0
    // Weights alternate 3,1 from the rightmost digit before the check digit.
    for (let i = digits.length - 2, w = 3; i >= 0; i--, w = 4 - w) {
        sum += Number(digits[i]) * w
    }
    const check = (10 - (sum % 10)) % 10
    return check === Number(digits[digits.length - 1])
}

/**
 * byGtin is keyed on the normalised form of each entry's own gtin, not on the
 * string as written. The generator already normalises, but keying on the result
 * of the same function used for lookups means the two can never disagree.
 */
const byGtin = new Map<string, ProductAllergens>(
    entries.map((entry) => [normalizeGtin(entry.gtin) || entry.gtin, entry])
)

/**
 * SNAPSHOT_AS_OF is the most recent fetch in the file: when this data was true.
 *
 * A miss reports this rather than the current time. Nothing is fetched at
 * lookup, so stamping "now" on a result would claim a freshness the answer does
 * not have — and would hand back a new object identity on every call.
 */
export const SNAPSHOT_AS_OF: string = entries.reduce(
    (latest, entry) => (entry.fetched_at > latest ? entry.fetched_at : latest),
    ''
)

/** How many products the snapshot covers. Useful in a diagnostic panel. */
export const SNAPSHOT_SIZE = entries.length

/**
 * getProductAllergens answers from the snapshot and never from the network.
 *
 * A barcode that is not in the file comes back ABSENT in exactly the shape of
 * any other answer, so callers have one code path. There is deliberately no
 * live fallback: a page that quietly reaches Open Food Facts when the snapshot
 * misses would be fast and correct in development and slow and rate-limited in
 * front of a customer.
 *
 * It stays async so the call site does not have to change shape, and so a future
 * source that genuinely is asynchronous can be dropped in behind it.
 */
export async function getProductAllergens(gtin: string): Promise<ProductAllergens> {
    const key = normalizeGtin(gtin)
    const found = key ? byGtin.get(key) : undefined
    if (found) return found

    return {
        gtin: key || gtin,
        source: 'OPEN_FOOD_FACTS',
        fetched_at: SNAPSHOT_AS_OF,
        coverage: 'ABSENT',
        allergens: [],
    }
}
