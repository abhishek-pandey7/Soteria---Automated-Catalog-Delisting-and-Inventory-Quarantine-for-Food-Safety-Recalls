import { Link, useSearchParams } from 'react-router-dom'
import { ProductPhoto } from '../components/ProductPhoto'
import { RescuePanel } from '../features/rescue/RescuePanel'
import { useRescue } from '../features/rescue/useRescue'
import { useBag } from '../store/useBag'

/**
 * Orders: the customer's recent order. If a recalled lot is in it, the rescue
 * offer (substitute / refund / cancel) is here — reachable from the emailed
 * link (?rescue=…&token=…) or from the account page (?order=…).
 */
export function Orders() {
    const [params] = useSearchParams()
    const rescueId = params.get('rescue') ?? undefined
    const consentToken = params.get('token') ?? undefined
    const orderId = params.get('order') ?? 'ORD-1001'
    const rescue = useRescue({ rescueId, orderId, consentToken })

    return (
        <main className="mx-auto max-w-[1440px] px-6">
            <section className="grid gap-10 pt-10 md:grid-cols-[0.8fr_1.2fr] md:gap-16">
                <div>
                    <p className="label">Your orders</p>
                    <h1 className="headline-lg mt-3">Order {orderId}</h1>
                    <p className="copy mt-6">
                        If anything in an order is caught by a recall before it ships, we hold that item
                        and offer a like-for-like substitute checked against the same allergens — or a
                        refund. Nothing changes until you choose.
                    </p>
                    {!consentToken && rescue.rescue?.status === 'PROPOSED' && (
                        <p className="mt-6 text-caption text-pebble">
                            Confirming needs the link from your email (<span className="mono">?rescue=…&amp;token=…</span>).
                            Without it the change is refused, by design.
                        </p>
                    )}
                    <Link to="/" className="link link-muted mt-8">Continue shopping</Link>
                </div>
                <div className="md:pt-12">
                    <RescuePanel
                        rescue={rescue.rescue}
                        loading={rescue.loading}
                        error={rescue.error}
                        confirming={rescue.confirming}
                        message={rescue.message}
                        onConfirm={rescue.confirm}
                    />
                </div>
            </section>
        </main>
    )
}

export function Bag() {
    const bag = useBag()
    const total = bag.lines.reduce((s, l) => s + l.qty * parseFloat(l.product.price.replace(/[^0-9.]/g, '')), 0)
    return (
        <main className="mx-auto max-w-[1440px] px-6">
            <section className="pt-10">
                <p className="label">Bag</p>
                <h1 className="headline-lg mt-3">{bag.count === 0 ? 'Nothing in your bag yet.' : `${bag.count} item${bag.count > 1 ? 's' : ''}.`}</h1>
                {bag.lines.length > 0 && (
                    <div className="mt-10 max-w-3xl">
                        {bag.lines.map((l) => (
                            <div key={l.product.gtin} className="flex items-center gap-6 py-4">
                                <div className="photo h-20 w-20 shrink-0 p-2">
                                    <ProductPhoto product={l.product} small />
                                </div>
                                <div className="flex-1">
                                    <p className="text-body"><span className="text-pebble">{l.product.brand} </span>{l.product.name}</p>
                                    <p className="text-body-sm text-pebble">{l.product.size} · qty {l.qty}</p>
                                </div>
                                <p className="text-body">{l.product.price}</p>
                                <button type="button" className="link link-muted" onClick={() => bag.remove(l.product.gtin)}>Remove</button>
                            </div>
                        ))}
                        <div className="hairline-dark my-6" />
                        <div className="flex items-baseline justify-between">
                            <p className="text-body-lg">Total</p>
                            <p className="headline">${total.toFixed(2)}</p>
                        </div>
                        <p className="mt-6 text-body-sm text-pebble">Checkout is not part of this demo store.</p>
                    </div>
                )}
                <Link to="/" className="link link-muted mt-8">Continue shopping</Link>
            </section>
        </main>
    )
}
