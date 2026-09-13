import type { Product } from '../catalog'

type Props = {
    product: Product
    small?: boolean
    /** classes for the <img>; the frame itself is sized by the parent */
    className?: string
}

/**
 * The product's photograph, contained in whatever frame the parent draws.
 *
 * When no photograph of this barcode exists the frame is named instead: brand,
 * product, barcode. That is better than a broken image and better than
 * borrowing another product's picture.
 */
export function ProductPhoto({ product, small, className = '' }: Props) {
    const src = small ? product.imageSmall : product.image

    if (src) {
        return (
            <img
                src={src}
                alt={product.name}
                loading="lazy"
                className={`h-full w-full ${className}`}
                style={{ objectFit: 'contain' }}
            />
        )
    }

    if (small) {
        return <span className="label flex h-full w-full items-center justify-center">{product.brand}</span>
    }

    return (
        <div className="absolute inset-0 flex flex-col justify-between p-6">
            <span className="label">{product.brand}</span>
            <span className="text-body-lg leading-tight">{product.name}</span>
            <span className="mono text-caption text-pebble">{product.gtin}</span>
        </div>
    )
}
