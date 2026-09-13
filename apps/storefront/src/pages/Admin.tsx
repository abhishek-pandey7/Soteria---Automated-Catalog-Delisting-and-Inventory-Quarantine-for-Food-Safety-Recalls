import { useEffect, useMemo, useState } from 'react'
import { Tabs } from '../components/Tabs'
import {
    dossierPdfUrl,
    getConfig,
    listActions,
    listDossiers,
    listResolutions,
    unitsOf,
    verifyChain,
    type ContainmentAction,
    type ContainmentConfig,
    type DossierRow,
    type Verification,
} from '../api/admin'
import type { Resolution } from '../api/resolution'

type TabId = 'overview' | 'incidents' | 'queue' | 'proof'

/**
 * The recall desk: what has been detected, what was held, what is waiting on a
 * person, and the evidence produced.
 *
 * Read-only by design. Approving a hold changes what the shop is selling, so
 * that action lives behind operator authentication rather than on a page the
 * storefront can open.
 *
 * All four tabs are rendered once and swapped by visibility, so switching does
 * not remount, refetch or change the page height.
 */
export function Admin() {
    const [tab, setTab] = useState<TabId>('overview')
    const [resolutions, setResolutions] = useState<Resolution[]>([])
    const [actions, setActions] = useState<ContainmentAction[]>([])
    const [dossiers, setDossiers] = useState<DossierRow[]>([])
    const [config, setConfig] = useState<ContainmentConfig | null>(null)
    const [state, setState] = useState<'loading' | 'ready' | 'offline'>('loading')

    useEffect(() => {
        let cancelled = false

        const load = async () => {
            try {
                const [r, a, d, c] = await Promise.all([
                    listResolutions(),
                    listActions(),
                    listDossiers(),
                    getConfig(),
                ])
                if (cancelled) return
                setResolutions(r)
                setActions(a)
                setDossiers(d)
                setConfig(c)
                setState('ready')
            } catch {
                if (!cancelled) setState('offline')
            }
        }

        load()
        const timer = setInterval(load, 10000)
        return () => {
            cancelled = true
            clearInterval(timer)
        }
    }, [])

    const pending = useMemo(
        () => actions.filter((a) => a.status === 'PENDING_REVIEW'),
        [actions],
    )
    const totals = useMemo(() => {
        let held = 0
        let sellable = 0
        for (const a of actions) {
            const u = unitsOf(a)
            held += u.held
            sellable += u.sellable
        }
        return { held, sellable }
    }, [actions])

    return (
        <main className="shell">
            <header className="section-tight">
                <p className="eyebrow mb-4">Operations</p>
                <div className="flex flex-wrap items-end justify-between gap-6">
                    <h1 className="headline-lg max-w-[14ch]">Recall desk</h1>
                    <p className="copy">
                        Live view of what the detection layer has found, what was contained, and the
                        evidence produced for each incident.
                    </p>
                </div>

                <div className="mt-8 flex flex-wrap items-center gap-4">
                    <Tabs
                        items={[
                            { id: 'overview', label: 'Overview' },
                            { id: 'incidents', label: 'Incidents', count: resolutions.length },
                            { id: 'queue', label: 'Review queue', count: pending.length },
                            { id: 'proof', label: 'Proof', count: dossiers.length },
                        ]}
                        active={tab}
                        onChange={(id) => setTab(id as TabId)}
                        aria-label="Recall desk sections"
                    />
                    <span className="text-body-sm text-pebble">
                        {state === 'loading' && 'Connecting…'}
                        {state === 'offline' && 'Services unreachable. Nothing below is live.'}
                        {state === 'ready' && 'Refreshes every 10 seconds'}
                    </span>
                </div>
            </header>

            <div className="hairline" />

            <section className="pb-4 pt-10" hidden={tab !== 'overview'}>
                <div className="fade-in">
                    <div className="stagger grid grid-cols-2 gap-x-8 gap-y-10 md:grid-cols-4">
                        <Figure value={String(resolutions.length)} label="Incidents resolved" />
                        <Figure value={String(totals.held)} label="Units withdrawn" />
                        <Figure
                            value={String(totals.sellable)}
                            label="Units kept sellable"
                            detail="Stock a whole-SKU pull would have destroyed"
                        />
                        <Figure value={String(pending.length)} label="Awaiting a decision" />
                    </div>

                    {config && (
                        <>
                            <div className="hairline mt-12 mb-8" />
                            <div className="grid gap-10 md:grid-cols-[minmax(0,1fr)_minmax(0,1.2fr)] md:gap-20">
                                <div>
                                    <p className="eyebrow mb-4">Thresholds in force</p>
                                    <p className="copy">
                                        Above the threshold a hold is automatic. Below it, a person
                                        decides. Removing an entire product line carries the higher
                                        bar of the two.
                                    </p>
                                </div>
                                <dl className="grid gap-0 self-start">
                                    <Row
                                        term="Lot-level hold"
                                        value={config.auto_hold_threshold?.toFixed(2) ?? '—'}
                                    />
                                    <Row
                                        term="Whole-SKU hold"
                                        value={config.sku_scope_threshold?.toFixed(2) ?? '—'}
                                    />
                                    <Row term="Last changed by" value={config.updated_by ?? 'default'} last />
                                </dl>
                            </div>
                        </>
                    )}
                </div>
            </section>

            <section className="pb-4 pt-10" hidden={tab !== 'incidents'}>
                <div className="fade-in">
                    {resolutions.length === 0 ? (
                        <Empty>No incident has matched a product in this catalog yet.</Empty>
                    ) : (
                        <>
                            <div className="table-head grid grid-cols-[minmax(0,2fr)_minmax(0,1.4fr)_auto_auto] gap-4">
                                <span>Incident</span>
                                <span>Product</span>
                                <span className="text-right">Scope</span>
                                <span className="text-right">Confidence</span>
                            </div>
                            <div className="stagger">
                                {resolutions.map((r) => {
                                    const match = r.matches?.[0]
                                    return (
                                        <div
                                            key={r.resolution_id}
                                            className="table-row grid-cols-[minmax(0,2fr)_minmax(0,1.4fr)_auto_auto]"
                                        >
                                            <div className="min-w-0">
                                                <p className="truncate text-body">{r.hazard ?? 'Recall'}</p>
                                                <p className="mono truncate text-caption text-pebble">
                                                    {r.incident_id}
                                                </p>
                                            </div>
                                            <div className="min-w-0">
                                                <p className="truncate text-body-sm">
                                                    {match?.product_title ?? '—'}
                                                </p>
                                                <p className="mono truncate text-caption text-pebble">
                                                    {match?.lot_codes?.join(', ') || 'no lot codes'}
                                                </p>
                                            </div>
                                            <span className="status justify-self-end">
                                                {r.scope === 'LOT' ? 'Lot' : 'Whole SKU'}
                                            </span>
                                            <span className="mono justify-self-end text-body-sm">
                                                {r.confidence?.toFixed(2) ?? '—'}
                                            </span>
                                        </div>
                                    )
                                })}
                            </div>
                        </>
                    )}
                </div>
            </section>

            <section className="pb-4 pt-10" hidden={tab !== 'queue'}>
                <div className="fade-in">
                    {actions.length === 0 ? (
                        <Empty>Nothing has been contained yet.</Empty>
                    ) : (
                        <>
                            <p className="copy mb-8">
                                Actions below the confidence threshold wait here for an operator.
                                Decisions are made in the operator console, not from the storefront.
                            </p>
                            <div className="table-head grid grid-cols-[minmax(0,2fr)_auto_auto_auto] gap-4">
                                <span>Action</span>
                                <span className="text-right">Held</span>
                                <span className="text-right">Sellable</span>
                                <span className="text-right">Status</span>
                            </div>
                            <div className="stagger">
                                {actions.map((a) => {
                                    const u = unitsOf(a)
                                    const waiting = a.status === 'PENDING_REVIEW'
                                    return (
                                        <div
                                            key={a.action_id}
                                            className="table-row grid-cols-[minmax(0,2fr)_auto_auto_auto]"
                                        >
                                            <div className="min-w-0">
                                                <p className="truncate text-body">
                                                    {a.targets?.[0]?.product_title ?? a.hazard ?? 'Containment'}
                                                </p>
                                                <p className="mono truncate text-caption text-pebble">
                                                    {a.reason ?? `confidence ${a.confidence?.toFixed(2)} against ${a.threshold?.toFixed(2)}`}
                                                </p>
                                            </div>
                                            <span className="mono justify-self-end text-body-sm">{u.held}</span>
                                            <span className="mono justify-self-end text-body-sm">{u.sellable}</span>
                                            <span
                                                className={`status justify-self-end ${waiting ? 'status-review' : 'status-held'}`}
                                            >
                                                {label(a.status)}
                                            </span>
                                        </div>
                                    )
                                })}
                            </div>
                        </>
                    )}
                </div>
            </section>

            <section className="pb-4 pt-10" hidden={tab !== 'proof'}>
                <div className="fade-in">
                    {dossiers.length === 0 ? (
                        <Empty>No dossier has been generated yet.</Empty>
                    ) : (
                        <>
                            <p className="copy mb-8">
                                Every event of an incident is hash-chained and the chain head is
                                anchored in time, so the sequence can be shown to be unaltered.
                            </p>
                            <div className="stagger grid gap-4">
                                {dossiers.map((d) => (
                                    <DossierCard key={d.incident_id} row={d} />
                                ))}
                            </div>
                        </>
                    )}
                </div>
            </section>
        </main>
    )
}

function DossierCard({ row }: { row: DossierRow }) {
    const [check, setCheck] = useState<Verification | null>(null)
    const [busy, setBusy] = useState(false)

    const verify = async () => {
        setBusy(true)
        try {
            setCheck(await verifyChain(row.incident_id))
        } catch {
            setCheck({ incident_id: row.incident_id, verified: false, events: 0, problem: 'unreachable' })
        } finally {
            setBusy(false)
        }
    }

    return (
        <article className="card">
            <div className="flex flex-wrap items-start justify-between gap-4">
                <div className="min-w-0">
                    <p className="text-body-lg truncate">{row.incident_id}</p>
                    <p className="mono mt-1 truncate text-caption text-pebble">
                        {row.content_hash ? `sha256 ${row.content_hash.slice(0, 32)}…` : 'no chain head'}
                    </p>
                </div>
                <span className="status">{row.event_count} events</span>
            </div>

            <div className="mt-5 flex flex-wrap items-center gap-3">
                <button type="button" className="pill pill-quiet" onClick={verify} disabled={busy}>
                    {busy ? 'Verifying…' : 'Verify chain'}
                </button>
                {row.has_dossier && (
                    <a
                        className="pill pill-quiet"
                        href={dossierPdfUrl(row.incident_id)}
                        target="_blank"
                        rel="noreferrer"
                    >
                        Open dossier
                    </a>
                )}
                {check && (
                    <span className={`status ${check.verified ? 'status-held' : 'status-review'}`}>
                        {check.verified
                            ? `Intact across ${check.events} events`
                            : `Not verified: ${check.problem ?? 'chain broken'}`}
                    </span>
                )}
            </div>
        </article>
    )
}

function Figure({ value, label, detail }: { value: string; label: string; detail?: string }) {
    return (
        <div>
            <p className="stat-figure">{value}</p>
            <p className="mt-3 text-body">{label}</p>
            {detail && <p className="mt-1 text-body-sm text-pebble">{detail}</p>}
        </div>
    )
}

function Row({ term, value, last }: { term: string; value: string; last?: boolean }) {
    return (
        <div
            className={`flex items-baseline justify-between gap-6 py-4 ${last ? '' : 'border-b border-mist'}`}
        >
            <dt className="text-body-sm text-pebble">{term}</dt>
            <dd className="mono text-body">{value}</dd>
        </div>
    )
}

function Empty({ children }: { children: React.ReactNode }) {
    return <p className="copy py-10">{children}</p>
}

function label(status?: string): string {
    switch (status) {
        case 'PENDING_REVIEW':
            return 'Awaiting review'
        case 'AUTO_HELD':
            return 'Held automatically'
        case 'HUMAN_CONFIRMED':
            return 'Held, confirmed'
        case 'HUMAN_REJECTED':
            return 'Not held'
        case 'FAILED':
            return 'Write failed'
        default:
            return status ?? '—'
    }
}
