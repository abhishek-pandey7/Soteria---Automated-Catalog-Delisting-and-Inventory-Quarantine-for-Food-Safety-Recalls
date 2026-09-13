import { useLayoutEffect, useRef } from 'react'
import { Link, NavLink, Outlet, useLocation, useNavigate } from 'react-router-dom'
import { BackendStatus } from './features/status/BackendStatus'
import { Tabs } from './components/Tabs'
import { useBag } from './store/useBag'

const SECTIONS = [
    { id: '/', label: 'Shop' },
    { id: '/pantry', label: 'Pantry' },
    { id: '/fresh', label: 'Fresh' },
    { id: '/admin', label: 'Recall desk' },
]

/**
 * The store chrome: capsule section tabs on the left, wordmark centred, utility
 * on the right, then the page.
 *
 * Two things keep navigation from feeling like a page load. The route key on
 * the outlet restarts the enter animation, and scroll is reset on layout rather
 * than after paint, so the new page is already at the top when its first frame
 * is drawn instead of visibly snapping there.
 */
export function Shell() {
    const bag = useBag()
    const navigate = useNavigate()
    const location = useLocation()
    const main = useRef<HTMLDivElement>(null)

    const active =
        SECTIONS.find((s) => s.id !== '/' && location.pathname.startsWith(s.id))?.id ??
        (location.pathname === '/' ? '/' : '')

    useLayoutEffect(() => {
        window.scrollTo({ top: 0, left: 0, behavior: 'auto' })
    }, [location.pathname])

    const utility = ({ isActive }: { isActive: boolean }) => `link ${isActive ? '' : 'link-muted'}`

    return (
        <div className="flex min-h-screen flex-col bg-warm-cream text-obsidian">
            <header className="shell grid grid-cols-1 items-center gap-4 py-5 md:grid-cols-[1fr_auto_1fr]">
                <div className="order-2 md:order-1">
                    <Tabs
                        items={SECTIONS}
                        active={active}
                        onChange={(id) => navigate(id)}
                        aria-label="Store sections"
                    />
                </div>

                <Link
                    to="/"
                    className="wordmark order-1 md:order-2 md:justify-self-center"
                    aria-label="Sotería, home"
                >
                    Sotería
                </Link>

                <div className="order-3 flex items-center justify-start gap-6 md:justify-end">
                    <BackendStatus />
                    <NavLink to="/orders" className={utility}>
                        Orders
                    </NavLink>
                    <NavLink to="/bag" className={utility}>
                        Bag{bag.count > 0 ? ` (${bag.count})` : ''}
                    </NavLink>
                </div>
            </header>

            <div ref={main} key={location.pathname} className="page flex-1">
                <Outlet />
            </div>

            <footer className="shell pb-14 pt-24">
                <div className="hairline mb-6" />
                <div className="flex flex-wrap items-baseline justify-between gap-x-10 gap-y-3">
                    <span className="wordmark text-obsidian">Sotería</span>
                    <span className="text-body-sm text-pebble max-w-[560px]">
                        Every lot on every shelf is checked against live recall feeds. Allergen data
                        © Open Food Facts contributors, ODbL.
                    </span>
                </div>
            </footer>
        </div>
    )
}
