import { useEffect, useState } from 'react'
import { Link, useParams, useSearchParams } from 'react-router-dom'
import { CATALOG, findProduct } from '../catalog'
import { ProductPhoto } from '../components/ProductPhoto'
import { AllergenPanel } from '../features/allergens/AllergenPanel'
import { useProductAllergens } from '../features/allergens/useProductAllergens'
import { SafeLotBadge } from '../features/badge/SafeLotBadge'
import { useLotStatus } from '../features/badge/useLotStatus'
import { RecallBanner } from '../features/banner/RecallBanner'
import { recallKey, useActiveRecalls } from '../features/recalls/useActiveRecalls'
import { useBag } from '../store/useBag'

/**
 * A product page that is, first, a product page: photo, name, size, price,
 * add to bag, what's in it. The recall machinery is the quiet "check your
 * pack" line at the bottom — until a lot comes back AFFECTED, at which point
 * the banner takes the top of the page and the verdict replaces the price.
 */
export function ProductPage() {
    const { gtin = '' } = useParams()
    const [params] = useSearchParams()
    const product = findProduct(gtin)
    const bag = useBag()

    const [lot, setLot] = useState(params.get('lot') ?? '')
    const [input, setInput] = useState(lot)
    const { status, loading, error } = useLotStatus(product?.gtin, lot || undefined)
    const allergens = useProductAllergens(product?.gtin)
    const recalls = useActiveRecalls()

    useEffect(() => {
        document.title = product ? `${product.name} — Sotería` : 'Sotería'
    }, [product])

    if (!product) {
        return (
            <main className="mx-auto max-w-[1440px] px-6 py-[100px]">
                <h1 className="headline">We don’t carry that one.</h1>
                <Link to="/" className="link mt-6">Back to the shop</Link>
            </main>
        )
    }

    const affected = status?.verdict === 'AFFECTED'
    const recalled = recalls.has(recallKey(product.gtin))
    const related = CATALOG.filter((p) => p.category === product.category && p.gtin !== product.gtin).slice(0, 3)

    return (
        <>
            {lot && <RecallBanner status={status} />}
            {!lot && recalled && (
                <div role="status" className="mx-auto max-w-[1440px] px-6 pb-6">
                    <div className="hairline-dark" />
                    <p className="pt-3 text-body-sm">
                        There is an active recall on this product. Check the lot code on your pack
                        below before you eat it.
                    </p>
                </div>
            )}
            <main className="mx-auto max-w-[1440px] px-6">
                <p className="text-body-sm text-pebble">
                    <Link to="/" className="link link-muted">Shop</Link>
                    <span className="mx-2">/</span>
                    <span className="capitalize">{product.category.replace(/-/g, ' ')}</span>
                </p>

                <section className="grid gap-10 pt-8 md:grid-cols-[1.15fr_1fr] md:gap-16">
                    <div className="photo relative aspect-[4/5] p-10 md:p-16">
                        <ProductPhoto product={product} />
                    </div>

                    <div className="md:pt-10">
                        <p className="label">{product.brand}</p>
                        <h1 className="headline-lg mt-3">{product.name}</h1>
                        <p className="mt-4 text-body-lg text-pebble">{product.size}</p>

                        {affected ? (
                            <div className="mt-10">
                                <SafeLotBadge status={status} loading={loading} error={error} />
                            </div>
                        ) : (
                            <div className="mt-10 flex flex-wrap items-center gap-6">
                                <p className="headline">{product.price}</p>
                                <button type="button" className="pill pill-solid" onClick={() => bag.add(product)}>
                                    Add to bag
                                </button>
                            </div>
                        )}

                        <div className="hairline my-10" />

                        <h2 className="text-subheading">What’s in it</h2>
                        <p className="mt-2 text-body-sm text-pebble">
                            Ingredients and allergens as recorded on Open Food Facts for this barcode.
                        </p>
                        <div className="mt-4">
                            <AllergenPanel data={allergens.data} loading={allergens.loading} />
                        </div>

                        <div className="hairline my-10" />

                        {/* The recall capability, kept quiet. */}
                        <form
                            onSubmit={(e) => {
                                e.preventDefault()
                                setLot(input.trim())
                            }}
                        >
                            <h2 className="text-subheading">Check your pack</h2>
                            <p className="mt-2 text-body-sm text-pebble">
                                Type the lot code printed on the packaging and we’ll check it against every
                                active recall.
                            </p>
                            <div className="mt-4 flex max-w-md gap-3">
                                <input
                                    value={input}
                                    onChange={(e) => setInput(e.target.value)}
                                    placeholder={product.lots[0] ? `e.g. ${product.lots[0]}` : 'Lot code'}
                                    className="field"
                                    aria-label="Lot code"
                                />
                                <button type="submit" className="pill">Check</button>
                            </div>
                            {product.lots.length > 0 && (
                                <p className="mt-3 text-caption text-pebble">
                                    Lots on our shelf right now:{' '}
                                    {product.lots.map((l, i) => (
                                        <button
                                            key={l}
                                            type="button"
                                            className="link link-muted mono"
                                            onClick={() => {
                                                setInput(l)
                                                setLot(l)
                                            }}
                                        >
                                            {l}
                                            {i < product.lots.length - 1 ? ',' : ''}
                                        </button>
                                    ))}
                                </p>
                            )}
                            {lot && !affected && (
                                <div className="mt-4">
                                    <SafeLotBadge status={status} loading={loading} error={error} />
                                </div>
                            )}
                            <p className="mt-2 text-caption text-pebble">
                                GTIN <span className="mono">{product.gtin}</span>
                            </p>
                        </form>
                    </div>
                </section>

                {related.length > 0 && (
                    <section className="pt-[100px]">
                        <div className="flex items-baseline justify-between">
                            <h2 className="headline">Also in {product.category.replace(/-/g, ' ')}</h2>
                            <Link to="/" className="link link-muted">All products</Link>
                        </div>
                        <div className="mt-8 grid grid-cols-2 gap-6 md:grid-cols-3">
                            {related.map((p, i) => (
                                <Link key={p.gtin} to={`/product/${p.gtin}`} className={`group block ${i === 1 ? 'md:mt-10' : ''}`}>
                                    <div className="photo relative aspect-square p-8">
                                        <ProductPhoto product={p} className="transition-transform duration-500 group-hover:scale-[1.03]" />
                                    </div>
                                    <p className="mt-3 text-body"><span className="text-pebble">{p.brand} </span>{p.name}</p>
                                    <p className="text-body-sm text-pebble">{p.price}</p>
                                </Link>
                            ))}
                        </div>
                    </section>
                )}
            </main>
        </>
    )
}
