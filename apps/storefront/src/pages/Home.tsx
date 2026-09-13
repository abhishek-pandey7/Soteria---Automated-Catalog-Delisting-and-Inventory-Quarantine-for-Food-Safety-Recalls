import { Link } from 'react-router-dom'
import { CATALOG, type Product } from '../catalog'
import { ProductPhoto } from '../components/ProductPhoto'
import { recallKey, useActiveRecalls } from '../features/recalls/useActiveRecalls'

/**
 * Home doubles as the landing page: a display-type opener, the claim stated
 * once in figures, then the shelves.
 *
 * The shelf grid is deliberately uniform. Product photography here comes from
 * Open Food Facts, shot by contributors on whatever was to hand, so the images
 * differ wildly in aspect ratio, crop and background. An asymmetric mosaic
 * amplifies that into visual noise; a single square frame per product, with the
 * image contained and centred inside it, makes inconsistent photographs read as
 * one shelf.
 */
export function Home({ filter, title }: { filter?: (p: Product) => boolean; title?: string }) {
    const products = filter ? CATALOG.filter(filter) : CATALOG

    if (filter) {
        return (
            <main className="shell">
                <header className="section-tight flex flex-wrap items-baseline justify-between gap-4">
                    <h1 className="headline-lg">{title ?? 'Shelves'}</h1>
                    <p className="text-body-sm text-pebble">
                        {products.length} {products.length === 1 ? 'product' : 'products'}
                    </p>
                </header>
                <div className="hairline" />
                <ProductGrid products={products} className="pt-10 pb-4" />
            </main>
        )
    }

    return (
        <main className="shell">
            <section className="flex min-h-[44vh] flex-col justify-end gap-8 pb-14 pt-8 md:flex-row md:items-end md:justify-between md:gap-16">
                <div>
                    <p className="eyebrow mb-5">Grocery, with the recall desk built in</p>
                    <h1 className="display max-w-[10ch]">Good food, checked.</h1>
                </div>
                <div className="md:pb-2 md:text-right">
                    <p className="copy-lg md:ml-auto">
                        Every item carries its lot number. When a recall lands, only the affected lot
                        comes off the shelf.
                    </p>
                    <div className="mt-6 flex flex-wrap gap-3 md:justify-end">
                        <Link to="/pantry" className="pill pill-solid">
                            Browse the shelves
                        </Link>
                        <Link to="/admin" className="pill pill-quiet">
                            See the recall desk
                        </Link>
                    </div>
                </div>
            </section>

            <div className="hairline-dark" />
            <section className="stagger grid grid-cols-2 gap-x-8 gap-y-10 py-12 md:grid-cols-4">
                <Figure value="3" label="Agency feeds watched" detail="FDA, USDA FSIS and EU RASFF, continuously" />
                <Figure value="Lot" label="Level of containment" detail="Not the whole product line" />
                <Figure value="48%" label="Stock kept sellable" detail="In the reference incident: 60 of 125 units" />
                <Figure value="Every" label="Step recorded" detail="Hash-chained and timestamped" />
            </section>
            <div className="hairline" />

            <section className="section">
                <div className="mb-10 flex flex-wrap items-baseline justify-between gap-4">
                    <h2 className="headline">On the shelves</h2>
                    <Link to="/pantry" className="link link-muted">
                        See everything
                    </Link>
                </div>
                <ProductGrid products={products} />
            </section>

            <div className="hairline" />
            <section className="section grid gap-12 md:grid-cols-[minmax(0,1fr)_minmax(0,1.1fr)] md:items-start md:gap-20">
                <div>
                    <p className="eyebrow mb-5">How it works</p>
                    <h2 className="headline-lg max-w-[16ch]">
                        Nothing on our shelves that shouldn’t be.
                    </h2>
                </div>
                <ol className="stagger grid gap-0">
                    <Step
                        n="01"
                        title="A notice arrives"
                        body="Agency feeds are polled continuously. The earliest signal is usually a press release, days before the structured report."
                    />
                    <Step
                        n="02"
                        title="It is resolved to a lot"
                        body="The notice is matched to a product and its lot codes, with a confidence score and the evidence behind it."
                    />
                    <Step
                        n="03"
                        title="Only that lot is held"
                        body="Affected units move to quarantine. Everything else keeps selling, with its lot verified on the product page."
                    />
                    <Step
                        n="04"
                        title="Affected orders are rescued"
                        body="Anyone already holding a bad lot is offered a same-price, allergen-checked replacement. Never swapped without asking."
                        last
                    />
                </ol>
            </section>
        </main>
    )
}

function ProductGrid({ products, className = '' }: { products: Product[]; className?: string }) {
    const recalls = useActiveRecalls()
    return (
        <div
            className={`stagger grid grid-cols-2 gap-x-5 gap-y-10 md:grid-cols-3 md:gap-x-6 lg:grid-cols-4 ${className}`}
        >
            {products.map((p) => (
                <Tile key={p.gtin} product={p} recalled={recalls.has(recallKey(p.gtin))} />
            ))}
        </div>
    )
}

function Tile({ product, recalled }: { product: Product; recalled: boolean }) {
    return (
        <Link to={`/product/${product.gtin}`} className="group flex flex-col">
            {/* One square frame for every product, whatever the photograph is. */}
            <div className="photo relative aspect-square">
                <ProductPhoto
                    product={product}
                    className="absolute inset-0 p-7 transition-transform duration-500 ease-out group-hover:scale-[1.04] md:p-9"
                />
                {recalled && (
                    <span className="status status-review absolute left-3 top-3 bg-warm-cream">
                        Recall active
                    </span>
                )}
            </div>

            {/* Fixed-height caption so every row's baselines line up. */}
            <div className="mt-4 flex min-h-[52px] items-start justify-between gap-4">
                <p className="text-body-sm leading-[1.35]">
                    <span className="text-pebble">{product.brand}</span>
                    <br />
                    <span className="line-clamp-2">{product.name}</span>
                </p>
                <p className="shrink-0 text-body-sm text-pebble">{product.price}</p>
            </div>
        </Link>
    )
}

function Figure({ value, label, detail }: { value: string; label: string; detail: string }) {
    return (
        <div>
            <p className="stat-figure">{value}</p>
            <p className="mt-3 text-body">{label}</p>
            <p className="mt-1 text-body-sm text-pebble">{detail}</p>
        </div>
    )
}

function Step({ n, title, body, last }: { n: string; title: string; body: string; last?: boolean }) {
    return (
        <li className={`grid grid-cols-[auto_minmax(0,1fr)] gap-6 py-6 ${last ? '' : 'border-b border-mist'}`}>
            <span className="label mono pt-1">{n}</span>
            <div>
                <p className="text-body-lg">{title}</p>
                <p className="copy mt-2">{body}</p>
            </div>
        </li>
    )
}
