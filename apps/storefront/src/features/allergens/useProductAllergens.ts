import { useEffect, useState } from 'react'
import { getProductAllergens } from '../../api/allergensSnapshot'
import type { ProductAllergens } from '../../api/allergens'

export function useProductAllergens(gtin?: string) {
    const [result, setResult] = useState<{
        gtin: string
        data: ProductAllergens
    } | null>(null)

    useEffect(() => {
        if (!gtin) return

        let cancelled = false

        getProductAllergens(gtin)
            .then((r) => {
                if (!cancelled) setResult({ gtin, data: r })
            })
            .catch(() => {
                if (!cancelled) {
                    setResult({
                        gtin,
                        data: {
                            gtin,
                            source: 'OPEN_FOOD_FACTS',
                            fetched_at: new Date().toISOString(),
                            coverage: 'ABSENT',
                            allergens: [],
                        },
                    })
                }
            })

        return () => {
            cancelled = true
        }
    }, [gtin])

    // Storing the gtin alongside the result means loading is derived, not a
    // second piece of state that can disagree with it. A result for a previous
    // gtin reads as loading, which is what it is — not stale data presented
    // as current.
    const data = result && result.gtin === gtin ? result.data : null
    const loading = Boolean(gtin) && data === null

    return { data, loading }
}