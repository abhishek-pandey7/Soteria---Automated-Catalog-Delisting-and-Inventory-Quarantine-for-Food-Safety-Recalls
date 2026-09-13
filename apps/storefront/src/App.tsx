import { BrowserRouter, Route, Routes } from 'react-router-dom'
import { Shell } from './Shell'
import { Home } from './pages/Home'
import { Bag, Orders } from './pages/Orders'
import { ProductPage } from './pages/ProductPage'
import { Admin } from './pages/Admin'
import { BagProvider } from './store/bag'

const FRESH = new Set(['yogurts', 'cheeses', 'ice-creams', 'breads', 'fruit-juices', 'plant-based-milks', 'hummus'])

export default function App() {
    return (
        <BagProvider>
            <BrowserRouter>
                <Routes>
                    <Route element={<Shell />}>
                        <Route index element={<Home />} />
                        <Route path="pantry" element={<Home title="Pantry" filter={(p) => !FRESH.has(p.category)} />} />
                        <Route path="fresh" element={<Home title="Fresh" filter={(p) => FRESH.has(p.category)} />} />
                        <Route path="product/:gtin" element={<ProductPage />} />
                        <Route path="admin" element={<Admin />} />
                        <Route path="orders" element={<Orders />} />
                        <Route path="bag" element={<Bag />} />
                    </Route>
                </Routes>
            </BrowserRouter>
        </BagProvider>
    )
}
