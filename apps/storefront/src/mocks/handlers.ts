import { http, HttpResponse } from 'msw'

const RESOLUTION = 'http://localhost:8081'
const RESCUE = 'http://localhost:8083'

const rescue = {
    rescue_id: 'r-mock-1',
    incident_id: 'inc-fda_enforcement-h-1245-2026',
    order_id: 'ORD-1001',
    status: 'PROPOSED',
    hazard: 'Potential foreign object contamination: rubber pieces',
    proposed_at: '2026-09-12T10:00:00Z',
    expires_at: '2026-09-14T10:00:00Z',
    affected_line: {
        line_item_id: 'li-1',
        gtin: '00860864000307',
        sku: 'SOT-000307',
        product_title: 'Power play fudge ice cream',
        lot_code: '26184',
        quantity: 2,
        unit_price: { amount_minor: 449, currency: 'USD' },
    },
    options: [
        {
            option_id: 'sub-00012345678905',
            kind: 'SUBSTITUTE',
            gtin: '00012345678905',
            product_title: 'Harvest Lane Oat and Honey Granola Bars 12 ct',
            unit_price: { amount_minor: 449, currency: 'USD' },
            allergen_safe: true,
            allergens: ['gluten'],
            rationale: 'same price, complete allergen data, no recall hazard allergen',
        },
        { option_id: 'refund', kind: 'REFUND', rationale: 'Refund this item and keep the rest of the order.' },
        { option_id: 'cancel', kind: 'CANCEL', rationale: 'Cancel the whole order.' },
    ],
}

export const handlers = [
    http.get(`${RESOLUTION}/v1/lots/status`, ({ request }) => {
        const url = new URL(request.url)
        const gtin = url.searchParams.get('gtin') ?? ''
        const lot = url.searchParams.get('lot_code') ?? undefined

        // Real recalled lot codes from the seeded catalog (Sept 2026 FDA notices)
        // plus the original fixture code, so the demo store can show a hit.
        const RECALLED: Record<string, string> = {
            L2408B: 'Undeclared peanut',
            LB028ACP04: 'Salmonella — Botticelli Foods / bettergoods Pistachio Nut Butter (FDA H-1273-2026)',
            LLA618203: 'Foreign material (glass pieces) — Outshine Fruit Bars (FDA H-1265-2026)',
            '1226183': 'Undeclared fish — Sun Noodle Sura Tanmen (FDA H-1258-2026)',
            '8H-1132': 'Listeria monocytogenes',
            'P-1950': 'Salmonella Enteritidis — Kroger Grade A eggs (FDA H-1230-2026)',
        }
        if (lot && RECALLED[lot]) {
            return HttpResponse.json({
                gtin,
                lot_code: lot,
                verdict: 'AFFECTED',
                incident_id: 'inc-fda_enforcement-h-1245-2026',
                hazard: 'Potential foreign object contamination: rubber pieces',
                confidence: 0.97,
                checked_at: new Date().toISOString(),
            })
        }

        if (lot === 'CLEAN-A' || lot === 'CLEAN-B') {
            return HttpResponse.json({
                gtin,
                lot_code: lot,
                verdict: 'SAFE',
                checked_at: new Date().toISOString(),
            })
        }

        if (lot === 'BOOM') {
            return new HttpResponse(null, { status: 500 })
        }

        // SafeLotBadge's Live stories drive the badge on these codes. They are
        // kept working deliberately: those stories are out of scope to edit, and
        // the fall-through below would otherwise turn LiveSafe and LiveAffected
        // into UNKNOWN_LOT.
        if (lot === 'L2408B') {
            return HttpResponse.json({
                gtin,
                lot_code: lot,
                verdict: 'AFFECTED',
                incident_id: 'INC-2026-0412',
                hazard: 'Undeclared peanut',
                confidence: 0.97,
                checked_at: new Date().toISOString(),
            })
        }

        if (lot === 'L2408A') {
            return HttpResponse.json({
                gtin,
                lot_code: lot,
                verdict: 'SAFE',
                checked_at: new Date().toISOString(),
            })
        }

        // Anything else, including the ZZ-0000 preset, L9999Z and a typo: a code
        // this store cannot place is not a safe code. The real service says the
        // same ("an unidentified lot is not a safe lot"), and SAFE as a catch-all
        // is what let the recalled lot read as fine.
        return HttpResponse.json({
            gtin,
            lot_code: lot,
            verdict: 'UNKNOWN_LOT',
            checked_at: new Date().toISOString(),
        })
    }),
    // No Open Food Facts handler. The allergen layer reads the committed
    // snapshot (src/api/allergens.snapshot.json) and never calls OFF, so there is
    // no request left to intercept. Mocking it would only hide a regression: if a
    // live call ever comes back, it should fail loudly rather than be answered
    // here.
    http.get(`${RESCUE}/healthz`, () =>
        HttpResponse.json({ status: 'ok', service: 'order-rescue-service', bus: 'up' })
    ),
    http.get(`${RESOLUTION}/healthz`, () =>
        HttpResponse.json({ status: 'ok', service: 'resolution-service', bus: 'up' })
    ),
    http.get(`${RESCUE}/v1/rescues`, ({ request }) => {
        const orderId = new URL(request.url).searchParams.get('order_id')
        // ORD-1002 holds a clean lot: nothing to rescue, and the API says so
        // with an empty list rather than an error.
        if (orderId && orderId !== 'ORD-1001') {
            return HttpResponse.json({ items: [] })
        }
        return HttpResponse.json({ items: [rescue] })
    }),
    http.get(`${RESCUE}/v1/rescues/:rescueId`, ({ params }) =>
        HttpResponse.json({ ...rescue, rescue_id: params.rescueId as string })
    ),
    http.post(`${RESCUE}/v1/rescues/:rescueId/confirm`, async ({ request, params }) => {
        const body = (await request.json()) as { option_id?: string; consent_token?: string }
        // The real service refuses a confirmation without the token it issued;
        // the mock refuses too, so the UI's 403 path is exercised offline.
        if (!body.consent_token || body.consent_token === 'forged') {
            return HttpResponse.json(
                { code: 'invalid_consent', message: 'consent token does not match this rescue' },
                { status: 403 }
            )
        }
        return HttpResponse.json({
            ...rescue,
            rescue_id: params.rescueId as string,
            status: 'CONFIRMED',
            chosen_option_id: body.option_id,
            confirmed_at: new Date().toISOString(),
        })
    }),
]