import type { Meta, StoryObj } from '@storybook/react-vite'
import { AllergenPanel } from './AllergenPanel'
import { useProductAllergens } from './useProductAllergens'

const meta: Meta<typeof AllergenPanel> = {
    title: 'Storefront/AllergenPanel',
    component: AllergenPanel,
}
export default meta

type Story = StoryObj<typeof AllergenPanel>

const base = {
    gtin: '03017620422003',
    source: 'OPEN_FOOD_FACTS' as const,
    fetched_at: '2026-09-12T13:46:32.555Z',
}

export const CompleteWithAllergens: Story = {
    args: {
        data: {
            ...base,
            product_name: 'Nutella',
            brand: 'Nutella, Ferrero',
            coverage: 'COMPLETE',
            allergens: ['en:milk', 'en:nuts', 'en:soybeans'],
            traces: [],
            ingredients_text: 'Sugar, palm oil, hazelnuts 13%, skimmed milk powder, cocoa',
        },
    },
}

export const CompleteNoAllergens: Story = {
    args: {
        data: {
            ...base,
            product_name: 'Spring Water',
            coverage: 'COMPLETE',
            allergens: [],
            traces: [],
            ingredients_text: 'Water',
        },
    },
}

export const WithTraces: Story = {
    args: {
        data: {
            ...base,
            product_name: 'Dark Chocolate',
            coverage: 'COMPLETE',
            allergens: ['en:soybeans'],
            traces: ['en:milk', 'en:nuts'],
            ingredients_text: 'Cocoa mass, sugar, cocoa butter, soy lecithin',
        },
    },
}

export const Partial: Story = {
    args: {
        data: {
            ...base,
            product_name: 'Unknown Brand Crackers',
            coverage: 'PARTIAL',
            allergens: [],
            ingredients_text: 'Wheat flour, vegetable oil, salt',
        },
    },
}

export const Absent: Story = {
    args: {
        data: {
            ...base,
            coverage: 'ABSENT',
            allergens: [],
        },
    },
}

export const Loading: Story = {
    args: { data: null, loading: true },
}
/**
 * The stories below read the committed snapshot through the real hook. Nothing
 * is mocked and nothing is fetched — the barcodes are genuine products in
 * src/api/allergens.snapshot.json.
 */
function LivePanel({ gtin }: { gtin: string }) {
    const { data, loading } = useProductAllergens(gtin)
    return <AllergenPanel data={data} loading={loading} />
}

/**
 * A 12-digit barcode, as printed on a pack, against a snapshot keyed on the
 * 14-digit form. This is the story that proves the normalisation: without it the
 * lookup misses and the panel shows ABSENT, which looks exactly like Open Food
 * Facts having nothing.
 */
export const LiveTwelveDigitBarcode: Story = {
    render: () => <LivePanel gtin="860864000307" />,
}

/** A 13-digit EAN, and one of the few snapshot entries carrying traces. */
export const LiveWithTraces: Story = {
    render: () => <LivePanel gtin="3175680011480" />,
}

/**
 * A recalled product Open Food Facts has nothing for. It is in the snapshot on
 * purpose, as ABSENT with no product name — so "recalled, and we have no
 * allergen data" is a row rather than a silence.
 */
export const LiveRecalledWithNoData: Story = {
    render: () => <LivePanel gtin="858792003323" />,
}

/** A barcode the snapshot does not cover: ABSENT, same shape, no network call. */
export const LiveNotInSnapshot: Story = {
    render: () => <LivePanel gtin="0000000000000" />,
}