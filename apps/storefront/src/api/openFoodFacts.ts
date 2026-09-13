import type { ProductAllergens, Coverage } from './allergens'

/**
 * The live Open Food Facts client.
 *
 * NOT WIRED INTO THE APP. The allergen layer reads ./allergensSnapshot instead,
 * so no page fetches OFF at render time. This file is kept because it is the
 * reference for the coverage rule — tools/offsnapshot transliterates the block
 * below, and the two have to agree — and because it is what a refresh path would
 * be built from if the snapshot ever needs updating from inside the app.
 *
 * Nothing imports it today. Check that before assuming a change here has any
 * effect on what a customer sees.
 */

const OFF_BASE = 'https://world.openfoodfacts.org/api/v2'

type OffProduct = {
    product_name?: string
    brands?: string
    allergens_tags?: string[]
    traces_tags?: string[]
    ingredients_text?: string
}

type OffResponse = {
    status: number
    product?: OffProduct
}

export async function getProductAllergens(
    gtin: string
): Promise<ProductAllergens> {
    const fields = 'product_name,brands,allergens_tags,traces_tags,ingredients_text'
    const res = await fetch(`${OFF_BASE}/product/${gtin}?fields=${fields}`)

    const base = {
        gtin,
        source: 'OPEN_FOOD_FACTS' as const,
        fetched_at: new Date().toISOString(),
    }

    if (!res.ok) {
        return { ...base, coverage: 'ABSENT', allergens: [] }
    }

    const data: OffResponse = await res.json()

    if (data.status !== 1 || !data.product) {
        return { ...base, coverage: 'ABSENT', allergens: [] }
    }

    const p = data.product
    const allergens = p.allergens_tags ?? []
    const traces = p.traces_tags ?? []

    // OFF returns allergens_tags: [] both for "a contributor checked and there
    // are none" and for "nobody has filled this product in". The two payloads
    // are byte-identical, so an empty array can never be read as a verified
    // absence — only a non-empty list is evidence that anyone looked.
    //
    // ingredients_text means the product has *some* contributed data, which is
    // weaker evidence: PARTIAL, not COMPLETE. Nothing at all is ABSENT.
    //
    // This deliberately mislabels genuinely allergen-free products as PARTIAL.
    // That trade is intentional: a false PARTIAL costs a customer an unneeded
    // caution, a false COMPLETE tells an allergic customer a product is clear
    // when nobody ever checked.
    let coverage: Coverage
    if (allergens.length > 0 || traces.length > 0) {
        coverage = 'COMPLETE'
    } else if (p.ingredients_text && p.ingredients_text.trim().length > 0) {
        coverage = 'PARTIAL'
    } else {
        coverage = 'ABSENT'
    }

    return {
        ...base,
        product_name: p.product_name,
        brand: p.brands,
        coverage,
        allergens,
        traces,
        ingredients_text: p.ingredients_text,
    }
}