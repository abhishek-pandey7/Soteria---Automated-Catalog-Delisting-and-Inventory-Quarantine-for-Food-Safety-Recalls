"""store_catalog.json + off_catalog.json -> src/catalog.ts (+ allergen snapshot rows)

The shelves are the Shopify store. resolution-service matches every recall
against the live store's catalog and lot ledger, so a storefront selling
anything else would show products the recall chain cannot see.

    store_catalog.json  barcode, vendor, title, price and lot ledger for every
                        variant in the store (export of shopify.Client.Catalog)
    off_catalog.json    Open Food Facts records for those barcodes, where OFF
                        has one (ingredients, allergens, a front photo)

Store facts win: barcode, price and lots always come from the store. SHELF
below only tidies the FDA-style product titles into shelf names. Photos come
from OFF when it has one, otherwise from public/products/<barcode>.jpg (the
maker's or a retailer's packshot, source recorded in PHOTOS), otherwise the
storefront draws a named frame.

    python gen_catalog.py store_catalog.json off_catalog.json ../src/catalog.ts ../src/api/allergens.snapshot.json
"""
import json, re, sys
from datetime import datetime, timezone

store_path, off_path, out_ts, snapshot_path = sys.argv[1:5]
store = json.load(open(store_path, encoding="utf-8"))
off = {p["code"].lstrip("0"): p for p in json.load(open(off_path, encoding="utf-8"))}

# Shelf presentation for each store barcode. A barcode not listed here is not
# put on the shelves (the rescue substitute pack, for one).
SHELF = {
    "085315054108": ("Sun Noodle", "Sura Tanmen Hot & Sour Noodles & Soup Base", "15.4 oz (436 g)", "noodles"),
    "850073087008": ("Bakr", "Brown Butter Chocolate Chunk Cookie Dough", "8 oz, 12 cookies", "baking"),
    "860864000307": ("Gifford's", "Power Play Fudge Ice Cream", "1 qt (946 mL)", "ice-creams"),
    "858792003323": ("Mellish Island", "Super Skin Dietary Supplement", "60 capsules", "supplements"),
    "175029": ("Island Pacific", "Imitation Crab Sticks", "1 lb (454 g)", "seafood"),
    "042272005833": ("Amy's", "Organic Lentil Soup, Light in Sodium", "14.5 oz", "soups"),
    "100000010150": ("Fresh to You", "Burger Chicken Fillet", "5 oz", "poultry"),
    "4056489125846": ("Eridanous", "Greek Style Shortbread Cookies with Apricot Filling", "330 g", "cookies"),
}

# Packshots for barcodes Open Food Facts has no photo of.
PHOTOS = {
    "085315054108": "https://sunnoodle.com/products/suratanmen/",
    "860864000307": "https://www.giffordsicecream.com/flavors/power-play-fudge/",
    "858792003323": "https://selectups.com/products/mellish-island-super-skin-collagen-supplements",
    "175029": "https://shop.islandpacificmarket.com/shop/seafood/other_seafood/other_seafood/island_pacific_imitation_crab_sticks_16_oz/p/1564405684711959425",
}


def key(code):
    return code.lstrip("0")


def gtin14(code):
    return re.sub(r"\D", "", code).zfill(14)


def shelf(code):
    return next((v for k, v in SHELF.items() if key(k) == key(code)), None)


def photo_source(code):
    return next((v for k, v in PHOTOS.items() if key(k) == key(code)), None)


def english(tags):
    return sorted({t for t in (tags or []) if t.startswith("en:")})


items, snapshot_rows = [], []
for v in store:
    code = v["barcode"]
    s = shelf(code)
    if not s:
        print("  not shelved:", code, v["title"])
        continue
    brand, name, size, category = s
    rec = off.get(key(code), {})
    ingredients = (rec.get("ingredients_text_en") or rec.get("ingredients_text") or "").strip()

    if rec.get("image_front_url"):
        image, small = rec["image_front_url"], rec.get("image_front_small_url") or rec["image_front_url"]
    elif photo_source(code):
        image, small = f"/products/{code}.jpg", f"/products/{code}-sm.jpg"
    else:
        image, small = "", ""

    items.append({
        "gtin": code,
        "name": name,
        "brand": brand,
        "size": size,
        "price": f"${float(v['price']):.2f}",
        "image": image,
        "imageSmall": small,
        "category": category,
        "allergens": sorted(t.replace("en:", "") for t in english(rec.get("allergens_tags"))),
        "ingredients": re.sub(r"\s+", " ", ingredients),
        "lots": [l["code"] for l in v["lots"]],
    })

    # Only a record with ingredients on file says anything about allergens; an
    # empty tag list on a record with no ingredients means nobody typed them in.
    if ingredients and rec.get("allergens_tags") is not None:
        snapshot_rows.append({
            "gtin": gtin14(code),
            "product_name": f"{brand} {name}",
            "brand": brand,
            "source": "OPEN_FOOD_FACTS",
            "fetched_at": datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ"),
            "coverage": "COMPLETE",
            "allergens": english(rec.get("allergens_tags")),
            "traces": english(rec.get("traces_tags")),
            "ingredients_text": ingredients,
        })

# Photographed products first, so the shelf opens on pictures.
items.sort(key=lambda i: (0 if i["image"] else 1, i["category"], i["name"]))

ts = [
    "// GENERATED by scripts/gen_catalog.py — do not hand-edit.",
    "// The shelves are the Shopify store: barcode, price and lot ledger come from",
    "// the store, so every product here is one the recall chain can match and hold.",
    "// Ingredients, allergens and most photos are Open Food Facts data for that",
    "// barcode (ODbL, © Open Food Facts contributors); the rest of the photos are",
    "// the maker's or a retailer's packshot (sources in scripts/gen_catalog.py).",
    "//",
    "// Whether a product is under recall is not recorded here. That is live: see",
    "// features/recalls/useActiveRecalls.",
    "",
    "export type Product = {",
    "    gtin: string", "    name: string", "    brand: string", "    size: string", "    price: string",
    "    /** empty when no photograph of this barcode exists */",
    "    image: string", "    imageSmall: string", "    category: string", "    allergens: string[]", "    ingredients: string",
    "    lots: string[]", "}", "",
    "export const CATALOG: Product[] = " + json.dumps(items, indent=4, ensure_ascii=False), "",
    "export function findProduct(gtin: string): Product | undefined {",
    "    const g = gtin.replace(/^0+/, '')",
    "    return CATALOG.find((p) => p.gtin.replace(/^0+/, '') === g)",
    "}", "",
]
open(out_ts, "w", encoding="utf-8", newline="\n").write("\n".join(ts))

# Append to the allergen snapshot only the barcodes it does not already cover.
# The file belongs to tools/offsnapshot (Go's encoder), so existing rows are
# left byte-for-byte and new ones are written the way Go would write them.
text = open(snapshot_path, encoding="utf-8").read().rstrip()
have = {gtin14(r["gtin"]) for r in json.loads(text)}
added = [r for r in snapshot_rows if r["gtin"] not in have]
if added:
    def go_json(row):
        s = json.dumps(row, indent=2, ensure_ascii=False)
        s = s.replace("&", "\\u0026").replace("<", "\\u003c").replace(">", "\\u003e")
        return "\n".join("  " + line for line in s.split("\n"))
    assert text.endswith("]")
    text = text[:-1].rstrip() + ",\n" + ",\n".join(go_json(r) for r in added) + "\n]"
    open(snapshot_path, "w", encoding="utf-8", newline="\n").write(text)
snapshot_rows = added

print(len(items), "products ->", out_ts, "|", len(snapshot_rows), "allergen rows ->", snapshot_path)
for i in items:
    print(f"  {i['gtin']:14} {i['brand'][:14]:14} {i['name'][:40]:40} {i['category']:12} photo={'y' if i['image'] else 'n'} lots={i['lots']}")
