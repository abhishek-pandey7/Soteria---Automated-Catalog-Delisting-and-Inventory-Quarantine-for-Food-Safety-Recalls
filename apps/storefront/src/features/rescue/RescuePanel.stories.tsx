import type { Meta, StoryObj } from '@storybook/react-vite'
import { RescuePanel } from './RescuePanel'
import { useRescue } from './useRescue'
import type { Rescue } from '../../api/rescue'

const meta: Meta<typeof RescuePanel> = {
    title: 'Storefront/RescuePanel',
    component: RescuePanel,
    args: { onConfirm: () => { } },
}
export default meta

type Story = StoryObj<typeof RescuePanel>

const base: Rescue = {
    rescue_id: 'r-1',
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

export const Proposed: Story = { args: { rescue: base } }

export const Loading: Story = { args: { rescue: null, loading: true } }

export const Unavailable: Story = { args: { rescue: null, error: true } }

/** Confirming one option must not let the customer press another. */
export const Confirming: Story = {
    args: { rescue: base, confirming: 'sub-00012345678905' },
}

/** No substitute cleared the allergen and price checks, so none is offered. */
export const NoSafeSubstitute: Story = {
    args: { rescue: { ...base, options: base.options.filter((o) => o.kind !== 'SUBSTITUTE') } },
}
/**
 * A substitute the service could NOT clear for allergens. It must not be
 * offered as a choice — the customer is being swapped off a peanut recall.
 */
export const UnsafeSubstitute: Story = {
    args: {
        rescue: {
            ...base,
            options: [
                {
                    option_id: 'sub-00099887766554',
                    kind: 'SUBSTITUTE',
                    gtin: '00099887766554',
                    product_title: 'Trailhead Almond Crunch Bars 12 ct',
                    unit_price: { amount_minor: 449, currency: 'USD' },
                    allergen_safe: false,
                    allergens: ['tree nuts', 'almonds'],
                    rationale: 'closest match by category, but contains tree nuts',
                },
                ...base.options.filter((o) => o.kind !== 'SUBSTITUTE'),
            ],
        },
    },
}

/**
 * allergen_safe is optional in the contract. Absent means "not verified",
 * which is not the same as safe, and must not render as one.
 */
export const UnverifiedSubstitute: Story = {
    args: {
        rescue: {
            ...base,
            options: [
                {
                    option_id: 'sub-00055443322119',
                    kind: 'SUBSTITUTE',
                    gtin: '00055443322119',
                    product_title: 'Mill Street Seed Bars 12 ct',
                    unit_price: { amount_minor: 449, currency: 'USD' },
                    rationale: 'same category and price; allergen data unavailable',
                },
                ...base.options.filter((o) => o.kind !== 'SUBSTITUTE'),
            ],
        },
    },
}

/** The lot could not be pinned to this line, so the copy must not name one. */
export const UnattributedLot: Story = {
    args: { rescue: { ...base, affected_line: { ...base.affected_line, lot_code: 'UNATTRIBUTED' } } },
}

export const ConfirmedSubstitute: Story = {
    args: {
        rescue: {
            ...base,
            status: 'CONFIRMED',
            chosen_option_id: 'sub-00012345678905',
            confirmed_at: '2026-09-12T10:05:00Z',
        },
    },
}

export const Expired: Story = { args: { rescue: { ...base, status: 'EXPIRED' } } }

export const ConsentRefused: Story = {
    args: { rescue: base, message: 'This confirmation link is not valid.' },
}

function LiveRescue({ orderId }: { orderId: string }) {
    const r = useRescue({ orderId })
    return (
        <RescuePanel
            rescue={r.rescue}
            loading={r.loading}
            error={r.error}
            confirming={r.confirming}
            message={r.message}
            onConfirm={r.confirm}
        />
    )
}

/** Hits the running order-rescue-service on :8083. */
export const LiveOrder: Story = { render: () => <LiveRescue orderId="ORD-1001" /> }
