package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"soteria/libs/shopify"
)

// graphQL is the raw Admin API seam. The shared client's typed methods cover
// everything the services need at runtime; creating locations, metafield
// definitions and products is setup-only, so those mutations live here rather
// than widening the shared API.
type graphQL interface {
	Do(ctx context.Context, query string, variables map[string]any, out any) error
}

// store is what apply needs: typed reads plus raw mutations.
type store interface {
	graphQL
	Locations(ctx context.Context) ([]shopify.Location, error)
	VariantsByBarcode(ctx context.Context, barcodes []string) ([]shopify.Variant, error)
	SetMetafields(ctx context.Context, fields []shopify.Metafield) error
}

func runApply(ctx context.Context, args []string) error {
	fs := flags("apply", args)
	seedPath := fs.String("seed", "seed.json", "seed file")
	dryRun := fs.Bool("dry-run", false, "print what would change, touch nothing")
	if err := fs.Parse(args); err != nil {
		return err
	}

	seed, err := loadSeed(*seedPath)
	if err != nil {
		return err
	}
	c, err := client()
	if err != nil {
		return err
	}
	return Apply(ctx, c, seed, *dryRun)
}

// Apply makes the store match the seed. Every step is idempotent: it checks for
// the thing first and only creates what is missing, so re-running after a
// partial failure is safe.
func Apply(ctx context.Context, s store, seed Seed, dryRun bool) error {
	if dryRun {
		fmt.Println("dry run: nothing will be changed")
	}

	selling, quarantine, err := ensureQuarantine(ctx, s, dryRun)
	if err != nil {
		return err
	}
	fmt.Printf("locations: selling=%s quarantine=%s\n", short(selling), short(quarantine))

	if err := ensureDefinitions(ctx, s, dryRun); err != nil {
		return err
	}

	for _, p := range seed.Products {
		if err := ensureProduct(ctx, s, p, selling, dryRun); err != nil {
			return fmt.Errorf("%s: %w", p.Title, err)
		}
	}

	fmt.Println("\ndone. Verify with: seedshop verify")
	return nil
}

// ---------------------------------------------------------------- locations

const mLocationAdd = `mutation LocationAdd($input: LocationAddInput!) {
  locationAdd(input: $input) {
    location { id name }
    userErrors { field message }
  }
}`

// ensureQuarantine returns the selling and quarantine location ids, creating the
// quarantine location if the shop does not have one.
func ensureQuarantine(ctx context.Context, s store, dryRun bool) (selling, quarantine string, err error) {
	locations, err := s.Locations(ctx)
	if err != nil {
		return "", "", fmt.Errorf("list locations: %w", err)
	}

	for _, l := range locations {
		switch {
		case strings.EqualFold(l.Name, QuarantineName):
			quarantine = l.ID
		case l.IsActive && l.Fulfills && selling == "":
			selling = l.ID
		}
	}
	if selling == "" {
		return "", "", fmt.Errorf("no active location fulfills online orders; containment has nowhere to move stock from")
	}
	if quarantine != "" {
		return selling, quarantine, nil
	}

	fmt.Printf("creating the %q location\n", QuarantineName)
	if dryRun {
		return selling, "(would be created)", nil
	}

	var out struct {
		LocationAdd struct {
			Location   struct{ ID, Name string }
			UserErrors []struct {
				Field   []string
				Message string
			}
		} `json:"locationAdd"`
	}
	input := map[string]any{
		"name": QuarantineName,
		// Shopify requires an address; this one is deliberately labelled so nobody
		// mistakes it for a real fulfilment site.
		"address": map[string]any{
			"address1":    "Soteria quarantine hold",
			"city":        "Quarantine",
			"countryCode": "US",
			"zip":         "00000",
		},
		"fulfillsOnlineOrders": false,
	}
	if err := s.Do(ctx, mLocationAdd, map[string]any{"input": input}, &out); err != nil {
		return "", "", fmt.Errorf("create quarantine location: %w", err)
	}
	if len(out.LocationAdd.UserErrors) > 0 {
		return "", "", fmt.Errorf("create quarantine location: %s", out.LocationAdd.UserErrors[0].Message)
	}
	return selling, out.LocationAdd.Location.ID, nil
}

// ---------------------------------------------------------------- metafields

const mDefinitionCreate = `mutation DefCreate($definition: MetafieldDefinitionInput!) {
  metafieldDefinitionCreate(definition: $definition) {
    createdDefinition { id }
    userErrors { field message code }
  }
}`

type definition struct {
	name, namespace, key, typ, owner, description string
}

// ensureDefinitions creates the two metafield definitions the system reads and
// writes. Shopify accepts metafields without a definition, but without one they
// are invisible in the admin, which makes the lot ledger impossible for a human
// to maintain.
func ensureDefinitions(ctx context.Context, s store, dryRun bool) error {
	defs := []definition{
		{
			name: "Soteria lots", namespace: shopify.MetafieldNamespace, key: shopify.LotsKey,
			typ: "json", owner: "PRODUCTVARIANT",
			description: "Lot ledger: [{\"code\":\"8H-1132\",\"units\":40}]. Units must match stock at the selling location.",
		},
		{
			name: "Soteria badge", namespace: shopify.MetafieldNamespace, key: shopify.BadgeKey,
			typ: "single_line_text_field", owner: "PRODUCT",
			description: "Storefront recall badge text, written by containment.",
		},
	}

	for _, d := range defs {
		if dryRun {
			fmt.Printf("would ensure metafield definition %s.%s (%s on %s)\n", d.namespace, d.key, d.typ, d.owner)
			continue
		}
		var out struct {
			MetafieldDefinitionCreate struct {
				UserErrors []struct{ Message, Code string }
			} `json:"metafieldDefinitionCreate"`
		}
		input := map[string]any{
			"name": d.name, "namespace": d.namespace, "key": d.key,
			"type": d.typ, "ownerType": d.owner, "description": d.description,
		}
		if err := s.Do(ctx, mDefinitionCreate, map[string]any{"definition": input}, &out); err != nil {
			return fmt.Errorf("metafield definition %s.%s: %w", d.namespace, d.key, err)
		}
		for _, ue := range out.MetafieldDefinitionCreate.UserErrors {
			// Re-running is expected, so an existing definition is success.
			if ue.Code == "TAKEN" || strings.Contains(strings.ToLower(ue.Message), "already") {
				fmt.Printf("metafield definition %s.%s already exists\n", d.namespace, d.key)
				continue
			}
			return fmt.Errorf("metafield definition %s.%s: %s", d.namespace, d.key, ue.Message)
		}
		fmt.Printf("metafield definition %s.%s ready\n", d.namespace, d.key)
	}
	return nil
}

// ---------------------------------------------------------------- products

const mProductCreate = `mutation ProductCreate($product: ProductCreateInput!) {
  productCreate(product: $product) {
    product { id title variants(first: 1) { nodes { id inventoryItem { id } } } }
    userErrors { field message }
  }
}`

const mVariantsUpdate = `mutation VariantsUpdate($productId: ID!, $variants: [ProductVariantsBulkInput!]!) {
  productVariantsBulkUpdate(productId: $productId, variants: $variants) {
    productVariants { id barcode sku inventoryItem { id } }
    userErrors { field message }
  }
}`

const mActivateInventory = `mutation Activate($inventoryItemId: ID!, $locationId: ID!, $available: Int) {
  inventoryActivate(inventoryItemId: $inventoryItemId, locationId: $locationId, available: $available) {
    inventoryLevel { id }
    userErrors { field message }
  }
}`

// ensureProduct creates the product when its barcode is not already in the shop,
// then sets stock and the lot ledger. Matching on barcode rather than title is
// deliberate: the barcode is the identity a recall uses, so if it already exists
// the product exists, whatever it is called.
func ensureProduct(ctx context.Context, s store, p SeedProduct, sellingLoc string, dryRun bool) error {
	existing, err := s.VariantsByBarcode(ctx, barcodeForms(p.Barcode))
	if err != nil {
		return fmt.Errorf("look up barcode: %w", err)
	}

	if len(existing) > 0 {
		v := existing[0]
		fmt.Printf("%-14s exists (%s)\n", p.Barcode, truncate(v.ProductTitle, 40))
		if dryRun {
			return nil
		}
		return setLots(ctx, s, v.ID, p)
	}

	fmt.Printf("%-14s creating %q\n", p.Barcode, truncate(p.Title, 40))
	if dryRun {
		return nil
	}

	var created struct {
		ProductCreate struct {
			Product struct {
				ID       string
				Variants struct {
					Nodes []struct {
						ID            string
						InventoryItem struct{ ID string }
					}
				}
			}
			UserErrors []struct{ Message string }
		} `json:"productCreate"`
	}
	product := map[string]any{"title": p.Title, "vendor": p.Vendor, "status": "ACTIVE"}
	if err := s.Do(ctx, mProductCreate, map[string]any{"product": product}, &created); err != nil {
		return fmt.Errorf("create product: %w", err)
	}
	if len(created.ProductCreate.UserErrors) > 0 {
		return fmt.Errorf("create product: %s", created.ProductCreate.UserErrors[0].Message)
	}
	if len(created.ProductCreate.Product.Variants.Nodes) == 0 {
		return fmt.Errorf("create product: no default variant returned")
	}
	productID := created.ProductCreate.Product.ID
	variantID := created.ProductCreate.Product.Variants.Nodes[0].ID
	inventoryItemID := created.ProductCreate.Product.Variants.Nodes[0].InventoryItem.ID

	var updated struct {
		ProductVariantsBulkUpdate struct {
			UserErrors []struct{ Message string }
		} `json:"productVariantsBulkUpdate"`
	}
	variants := []map[string]any{{
		"id":      variantID,
		"barcode": p.Barcode,
		"price":   p.Price,
		"inventoryItem": map[string]any{
			"sku":     p.SKU,
			"tracked": true,
		},
	}}
	if err := s.Do(ctx, mVariantsUpdate, map[string]any{"productId": productID, "variants": variants}, &updated); err != nil {
		return fmt.Errorf("set barcode and price: %w", err)
	}
	if len(updated.ProductVariantsBulkUpdate.UserErrors) > 0 {
		return fmt.Errorf("set barcode and price: %s", updated.ProductVariantsBulkUpdate.UserErrors[0].Message)
	}

	// Stock at the selling location must equal the ledger, or a hold will try to
	// move units that are not there.
	var activated struct {
		InventoryActivate struct {
			UserErrors []struct{ Message, Code string }
		} `json:"inventoryActivate"`
	}
	vars := map[string]any{"inventoryItemId": inventoryItemID, "locationId": sellingLoc, "available": p.Units()}
	if err := s.Do(ctx, mActivateInventory, vars, &activated); err != nil {
		return fmt.Errorf("set stock: %w", err)
	}
	if len(activated.InventoryActivate.UserErrors) > 0 {
		msg := activated.InventoryActivate.UserErrors[0].Message
		// If inventory is already active, skip this product - it's a retry
		if strings.Contains(msg, "already active at the location") {
			fmt.Printf("    (inventory already active, skipped)\n")
		} else {
			return fmt.Errorf("set stock: %s", msg)
		}
	}

	return setLots(ctx, s, variantID, p)
}

// setLots writes the lot ledger onto the variant.
func setLots(ctx context.Context, s store, variantID string, p SeedProduct) error {
	value, err := json.Marshal(p.Lots)
	if err != nil {
		return err
	}
	return s.SetMetafields(ctx, []shopify.Metafield{{
		OwnerID:   variantID,
		Namespace: shopify.MetafieldNamespace,
		Key:       shopify.LotsKey,
		Type:      "json",
		Value:     string(value),
	}})
}

// barcodeForms lists the equivalent zero-padded widths of one GTIN, because
// barcode lookup is an exact string match and a store may hold any of them.
func barcodeForms(gtin string) []string {
	digits := strings.Map(func(r rune) rune {
		if r >= '0' && r <= '9' {
			return r
		}
		return -1
	}, gtin)
	if digits == "" {
		return []string{gtin}
	}
	seen := map[string]bool{}
	var forms []string
	add := func(v string) {
		if v != "" && !seen[v] {
			seen[v] = true
			forms = append(forms, v)
		}
	}
	add(digits)
	for _, width := range []int{14, 13, 12, 8} {
		switch {
		case len(digits) == width:
		case len(digits) > width:
			if strings.Trim(digits[:len(digits)-width], "0") == "" {
				add(digits[len(digits)-width:])
			}
		default:
			add(strings.Repeat("0", width-len(digits)) + digits)
		}
	}
	return forms
}

func short(id string) string {
	if i := strings.LastIndex(id, "/"); i >= 0 {
		return id[i+1:]
	}
	return id
}
