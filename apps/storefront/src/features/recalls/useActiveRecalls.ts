import { useEffect, useState } from 'react'
import { listResolutions } from '../../api/admin'

/** Barcodes (leading zeros stripped) under an active recall. */
export type ActiveRecalls = Set<string>

const POLL_MS = 15_000

export function recallKey(gtin: string): string {
    return gtin.replace(/\D/g, '').replace(/^0+/, '')
}

/**
 * useActiveRecalls: which products on the shelves are under a recall right now.
 *
 * Nothing about recalls is baked into the catalog. The answer comes from
 * resolution-service, which is what actually matched a notice to the store, and
 * it is polled so a shelf left open during an incident changes without a reload.
 * A failed poll keeps the last answer rather than clearing every warning.
 */
export function useActiveRecalls(): ActiveRecalls {
    const [recalls, setRecalls] = useState<ActiveRecalls>(new Set())

    useEffect(() => {
        let cancelled = false

        const load = () =>
            listResolutions(100)
                .then((resolutions) => {
                    if (cancelled) return
                    const next: ActiveRecalls = new Set()
                    for (const r of resolutions) {
                        for (const m of r.matches ?? []) next.add(recallKey(m.gtin))
                    }
                    setRecalls(next)
                })
                .catch(() => {})

        load()
        const timer = window.setInterval(load, POLL_MS)
        return () => {
            cancelled = true
            window.clearInterval(timer)
        }
    }, [])

    return recalls
}
