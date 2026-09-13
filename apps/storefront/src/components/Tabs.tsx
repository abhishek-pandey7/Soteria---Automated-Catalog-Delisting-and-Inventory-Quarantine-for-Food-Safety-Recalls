import { useLayoutEffect, useRef, useState } from 'react'

export type TabItem = {
    id: string
    label: string
    /** count renders as a quiet numeral inside the capsule. */
    count?: number
}

type Props = {
    items: TabItem[]
    active: string
    onChange: (id: string) => void
    'aria-label': string
}

/**
 * A capsule tab bar with one sliding thumb.
 *
 * The thumb is a single absolutely positioned element moved by transform, so
 * switching tabs never re-lays-out the row and the labels underneath do not
 * shift by a pixel. Measuring on layout (not on paint) means the thumb is in
 * place before the first frame, so there is no visible jump on mount either.
 */
export function Tabs({ items, active, onChange, 'aria-label': label }: Props) {
    const refs = useRef(new Map<string, HTMLButtonElement>())
    const [thumb, setThumb] = useState({ x: 0, w: 0 })

    useLayoutEffect(() => {
        const el = refs.current.get(active)
        if (!el) return

        const measure = () => setThumb({ x: el.offsetLeft, w: el.offsetWidth })
        measure()

        // Fonts and container width both change the geometry after mount.
        const observer = new ResizeObserver(measure)
        observer.observe(el)
        if (el.parentElement) observer.observe(el.parentElement)
        return () => observer.disconnect()
    }, [active, items])

    return (
        <div className="tabs" role="tablist" aria-label={label}>
            <span
                className="tabs-thumb"
                aria-hidden
                style={{ width: thumb.w, transform: `translateX(${thumb.x}px)` }}
            />
            {items.map((item) => (
                <button
                    key={item.id}
                    ref={(node) => {
                        if (node) refs.current.set(item.id, node)
                        else refs.current.delete(item.id)
                    }}
                    type="button"
                    role="tab"
                    aria-selected={item.id === active}
                    className={`tab ${item.id === active ? 'is-active' : ''}`}
                    onClick={() => onChange(item.id)}
                >
                    {item.label}
                    {item.count !== undefined && <span className="tab-count">{item.count}</span>}
                </button>
            ))}
        </div>
    )
}
