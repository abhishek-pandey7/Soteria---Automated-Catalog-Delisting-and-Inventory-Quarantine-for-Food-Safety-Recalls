import type { components as ContainmentSchema } from './containment.types'
import type { components as AuditSchema } from './audit.types'
import type { Resolution } from './resolution'

export type ContainmentAction = ContainmentSchema['schemas']['ContainmentAction']
export type ContainmentConfig = ContainmentSchema['schemas']['ContainmentConfig']
export type Dossier = AuditSchema['schemas']['Dossier']

const RESOLUTION = import.meta.env.VITE_RESOLUTION_API_URL
const CONTAINMENT = import.meta.env.VITE_CONTAINMENT_API_URL ?? 'http://localhost:8082'
const AUDIT = import.meta.env.VITE_AUDIT_API_URL ?? 'http://localhost:8085'

export const AUDIT_BASE = AUDIT

async function getJSON<T>(url: string): Promise<T> {
    const res = await fetch(url)
    if (!res.ok) throw new Error(`${url}: ${res.status}`)
    return res.json()
}

/**
 * The recall desk is read-only from the storefront.
 *
 * Approving or rejecting a hold changes what a shop is selling, so it belongs
 * behind operator authentication rather than on a page anyone can open. This
 * client deliberately exposes no write.
 */
export async function listResolutions(limit = 25): Promise<Resolution[]> {
    const body = await getJSON<{ items?: Resolution[] | null }>(
        `${RESOLUTION}/v1/resolutions?limit=${limit}`,
    )
    return body.items ?? []
}

export async function listActions(status?: string): Promise<ContainmentAction[]> {
    const query = status ? `?status=${encodeURIComponent(status)}` : ''
    const body = await getJSON<{ items?: ContainmentAction[] | null }>(
        `${CONTAINMENT}/v1/containment/actions${query}`,
    )
    return body.items ?? []
}

export function getConfig(): Promise<ContainmentConfig> {
    return getJSON<ContainmentConfig>(`${CONTAINMENT}/v1/containment/config`)
}

export type DossierRow = {
    incident_id: string
    has_dossier: boolean
    event_count: number
    content_hash?: string
}

export async function listDossiers(): Promise<DossierRow[]> {
    const body = await getJSON<{ items?: DossierRow[] | null }>(`${AUDIT}/v1/dossiers`)
    return body.items ?? []
}

export type Verification = {
    incident_id: string
    verified: boolean
    events: number
    content_hash?: string
    problem?: string
}

export function verifyChain(incidentId: string): Promise<Verification> {
    return getJSON<Verification>(`${AUDIT}/v1/dossiers/${encodeURIComponent(incidentId)}/verify`)
}

export function dossierPdfUrl(incidentId: string): string {
    return `${AUDIT}/v1/dossiers/${encodeURIComponent(incidentId)}.pdf`
}

/** Units held and left sellable across an action's targets. */
export function unitsOf(action: ContainmentAction): { held: number; sellable: number } {
    let held = 0
    let sellable = 0
    for (const r of action.results ?? []) {
        held += r.units_held ?? 0
        sellable += r.units_left_sellable ?? 0
    }
    return { held, sellable }
}
