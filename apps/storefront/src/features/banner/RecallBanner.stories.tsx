import type { Meta, StoryObj } from '@storybook/react-vite'
import { RecallBanner } from './RecallBanner'

const meta: Meta<typeof RecallBanner> = {
    title: 'Storefront/RecallBanner',
    component: RecallBanner,
}
export default meta

type Story = StoryObj<typeof RecallBanner>

export const Affected: Story = {
    args: {
        status: {
            gtin: '00860864000307',
            lot_code: '26184',
            verdict: 'AFFECTED',
            incident_id: 'inc-fda_enforcement-h-1245-2026',
            hazard: 'Potential foreign object contamination: rubber pieces',
            confidence: 0.99,
            checked_at: '2026-09-12T10:00:00Z',
        },
    },
}

export const UnknownLot: Story = {
    args: {
        status: {
            gtin: '00860864000307',
            lot_code: 'ZZ-0000',
            verdict: 'UNKNOWN_LOT',
            checked_at: '2026-09-12T10:00:00Z',
        },
    },
}

/** A safe lot renders nothing: no banner is the correct output. */
export const SafeRendersNothing: Story = {
    args: {
        status: {
            gtin: '00860864000307',
            lot_code: 'CLEAN-A',
            verdict: 'SAFE',
            checked_at: '2026-09-12T10:00:00Z',
        },
    },
}
