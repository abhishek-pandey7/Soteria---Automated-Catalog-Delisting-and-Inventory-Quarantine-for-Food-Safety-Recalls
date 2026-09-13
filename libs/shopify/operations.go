package shopify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ---- read -------------------------------------------------------------------

const qLocations = `query Locations {
  locations(first: 50, includeInactive: true) {
    nodes { id name isActive fulfillsOnlineOrders }
  }
}`

func (c *Client) Locations(ctx context.Context) ([]Location, error) {
	var out struct {
		Locations struct {
			Nodes []struct {
				ID                   string `json:"id"`
				Name                 string `json:"name"`
				IsActive             bool   `json:"isActive"`
				FulfillsOnlineOrders bool   `json:"fulfillsOnlineOrders"`
			} `json:"nodes"`
		} `json:"locations"`
	}
	if err := c.Do(ctx, qLocations, nil, &out); err != nil {
		return nil, err
	}
	locs := make([]Location, 0, len(out.Locations.Nodes))
	for _, n := range out.Locations.Nodes {
		locs = append(locs, Location{ID: n.ID, Name: n.Name, IsActive: n.IsActive, Fulfills: n.FulfillsOnlineOrders})
	}
	return locs, nil
}

// variantFields is the selection shared by every variant query.
const variantFields = `
  id title sku barcode price
  product { id title status handle vendor tags }
  lots: metafield(namespace: "soteria", key: "lots") { value }
  inventoryItem {
    id
    inventoryLevels(first: 20) {
      nodes { location { id } quantities(names: ["available"]) { name quantity } }
    }
  }`

const qCatalog = `query Catalog($cursor: String) {
  productVariants(first: 100, after: $cursor) {
    pageInfo { hasNextPage endCursor }
    nodes {` + variantFields + `}
  }
}`

const qVariantsByBarcode = `query VariantsByBarcode($q: String!) {
  productVariants(first: 100, query: $q) {
    nodes {` + variantFields + `}
  }
}`

const qVariant = `query Variant($id: ID!) {
  productVariant(id: $id) {` + variantFields + `}
}`

type variantNode struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	SKU     string `json:"sku"`
	Barcode string `json:"barcode"`
	Price   string `json:"price"`
	Product struct {
		ID     string   `json:"id"`
		Title  string   `json:"title"`
		Status string   `json:"status"`
		Handle string   `json:"handle"`
		Vendor string   `json:"vendor"`
		Tags   []string `json:"tags"`
	} `json:"product"`
	Lots *struct {
		Value string `json:"value"`
	} `json:"lots"`
	InventoryItem struct {
		ID              string `json:"id"`
		InventoryLevels struct {
			Nodes []struct {
				Location struct {
					ID string `json:"id"`
				} `json:"location"`
				Quantities []struct {
					Name     string `json:"name"`
					Quantity int    `json:"quantity"`
				} `json:"quantities"`
			} `json:"nodes"`
		} `json:"inventoryLevels"`
	} `json:"inventoryItem"`
}

func (n variantNode) toVariant() Variant {
	v := Variant{
		ID: n.ID, Title: n.Title, SKU: n.SKU, Barcode: n.Barcode, Price: n.Price,
		ProductID: n.Product.ID, ProductTitle: n.Product.Title, ProductStatus: n.Product.Status,
		ProductHandle: n.Product.Handle, Vendor: n.Product.Vendor, Tags: n.Product.Tags,
		InventoryItemID: n.InventoryItem.ID,
	}
	if n.Lots != nil && n.Lots.Value != "" {
		// A malformed ledger is not fatal for reads; containment refuses to
		// act on a variant whose lots cannot be parsed (Lots stays nil).
		_ = json.Unmarshal([]byte(n.Lots.Value), &v.Lots)
	}
	for _, l := range n.InventoryItem.InventoryLevels.Nodes {
		lvl := InventoryLevel{LocationID: l.Location.ID}
		for _, q := range l.Quantities {
			if q.Name == "available" {
				lvl.Available = q.Quantity
			}
		}
		v.Inventory = append(v.Inventory, lvl)
	}
	return v
}

func (c *Client) Catalog(ctx context.Context) ([]Variant, error) {
	var all []Variant
	var cursor *string
	for page := 0; page < 1000; page++ {
		var out struct {
			ProductVariants struct {
				PageInfo struct {
					HasNextPage bool   `json:"hasNextPage"`
					EndCursor   string `json:"endCursor"`
				} `json:"pageInfo"`
				Nodes []variantNode `json:"nodes"`
			} `json:"productVariants"`
		}
		vars := map[string]any{}
		if cursor != nil {
			vars["cursor"] = *cursor
		}
		if err := c.Do(ctx, qCatalog, vars, &out); err != nil {
			return nil, err
		}
		for _, n := range out.ProductVariants.Nodes {
			all = append(all, n.toVariant())
		}
		if !out.ProductVariants.PageInfo.HasNextPage {
			break
		}
		cur := out.ProductVariants.PageInfo.EndCursor
		cursor = &cur
	}
	return all, nil
}

func (c *Client) VariantsByBarcode(ctx context.Context, barcodes []string) ([]Variant, error) {
	var found []Variant
	// Shopify search strings have a practical length limit; batch by 25.
	for i := 0; i < len(barcodes); i += 25 {
		batch := barcodes[i:min(i+25, len(barcodes))]
		terms := make([]string, 0, len(batch))
		for _, b := range batch {
			b = strings.TrimSpace(b)
			if b == "" {
				continue
			}
			terms = append(terms, "barcode:"+strings.ReplaceAll(b, `"`, ""))
		}
		if len(terms) == 0 {
			continue
		}
		var out struct {
			ProductVariants struct {
				Nodes []variantNode `json:"nodes"`
			} `json:"productVariants"`
		}
		if err := c.Do(ctx, qVariantsByBarcode, map[string]any{"q": strings.Join(terms, " OR ")}, &out); err != nil {
			return nil, err
		}
		want := make(map[string]bool, len(batch))
		for _, b := range batch {
			want[strings.TrimSpace(b)] = true
		}
		for _, n := range out.ProductVariants.Nodes {
			// The search is a prefix/contains match server-side; keep exact hits only.
			if want[n.Barcode] {
				found = append(found, n.toVariant())
			}
		}
	}
	return found, nil
}

func (c *Client) Variant(ctx context.Context, variantID string) (Variant, error) {
	var out struct {
		ProductVariant *variantNode `json:"productVariant"`
	}
	if err := c.Do(ctx, qVariant, map[string]any{"id": variantID}, &out); err != nil {
		return Variant{}, err
	}
	if out.ProductVariant == nil {
		return Variant{}, fmt.Errorf("shopify: variant %s not found", variantID)
	}
	return out.ProductVariant.toVariant(), nil
}

const qOrders = `query OrdersSince($q: String!, $cursor: String) {
  orders(first: 50, after: $cursor, query: $q, sortKey: CREATED_AT, reverse: true) {
    pageInfo { hasNextPage endCursor }
    nodes {
      id name createdAt email
      customer { id }
      displayFulfillmentStatus displayFinancialStatus
      lineItems(first: 100) { nodes { id sku title quantity variant { id } } }
    }
  }
}`

func (c *Client) OrdersSince(ctx context.Context, since time.Time) ([]Order, error) {
	var all []Order
	var cursor *string
	q := fmt.Sprintf("created_at:>='%s'", since.UTC().Format(time.RFC3339))
	for page := 0; page < 200; page++ {
		var out struct {
			Orders struct {
				PageInfo struct {
					HasNextPage bool   `json:"hasNextPage"`
					EndCursor   string `json:"endCursor"`
				} `json:"pageInfo"`
				Nodes []struct {
					ID        string    `json:"id"`
					Name      string    `json:"name"`
					CreatedAt time.Time `json:"createdAt"`
					Email     string    `json:"email"`
					Customer  *struct {
						ID string `json:"id"`
					} `json:"customer"`
					DisplayFulfillmentStatus string `json:"displayFulfillmentStatus"`
					DisplayFinancialStatus   string `json:"displayFinancialStatus"`
					LineItems                struct {
						Nodes []struct {
							ID       string `json:"id"`
							SKU      string `json:"sku"`
							Title    string `json:"title"`
							Quantity int    `json:"quantity"`
							Variant  *struct {
								ID string `json:"id"`
							} `json:"variant"`
						} `json:"nodes"`
					} `json:"lineItems"`
				} `json:"nodes"`
			} `json:"orders"`
		}
		vars := map[string]any{"q": q}
		if cursor != nil {
			vars["cursor"] = *cursor
		}
		if err := c.Do(ctx, qOrders, vars, &out); err != nil {
			return nil, err
		}
		for _, n := range out.Orders.Nodes {
			o := Order{ID: n.ID, Name: n.Name, CreatedAt: n.CreatedAt, Email: n.Email,
				FulfillmentStatus: n.DisplayFulfillmentStatus, FinancialStatus: n.DisplayFinancialStatus}
			if n.Customer != nil {
				o.CustomerID = n.Customer.ID
			}
			for _, li := range n.LineItems.Nodes {
				item := LineItem{ID: li.ID, SKU: li.SKU, Title: li.Title, Quantity: li.Quantity}
				if li.Variant != nil {
					item.VariantID = li.Variant.ID
				}
				o.LineItems = append(o.LineItems, item)
			}
			all = append(all, o)
		}
		if !out.Orders.PageInfo.HasNextPage {
			break
		}
		cur := out.Orders.PageInfo.EndCursor
		cursor = &cur
	}
	return all, nil
}

// ---- inventory --------------------------------------------------------------

const mSetQuantities = `mutation SetAvailable($input: InventorySetQuantitiesInput!) {
  inventorySetQuantities(input: $input) {
    inventoryAdjustmentGroup { id }
    userErrors { field message code }
  }
}`

func (c *Client) SetAvailable(ctx context.Context, inventoryItemID, locationID string, quantity int, reason string) error {
	if reason == "" {
		reason = "correction"
	}
	var out struct {
		R struct {
			UserErrors []UserError `json:"userErrors"`
		} `json:"inventorySetQuantities"`
	}
	input := map[string]any{
		"name":                  "available",
		"reason":                reason,
		"ignoreCompareQuantity": true,
		"referenceDocumentUri":  referenceURI("set"),
		"quantities": []map[string]any{{
			"inventoryItemId": inventoryItemID,
			"locationId":      locationID,
			"quantity":        quantity,
		}},
	}
	if err := c.Do(ctx, mSetQuantities, map[string]any{"input": input}, &out); err != nil {
		return err
	}
	return userErrors("inventorySetQuantities", out.R.UserErrors)
}

// inventoryMoveQuantities cannot do this: it only moves units between quantity
// names (available -> reserved) at ONE location and rejects a cross-location
// move outright ("The quantities can't be moved between different locations").
// A selling -> Quarantine move is a paired adjustment, -N here and +N there,
// in a single adjustment group so the store never sees one half without the
// other.
const mAdjustQuantities = `mutation MoveAvailable($input: InventoryAdjustQuantitiesInput!) {
  inventoryAdjustQuantities(input: $input) {
    inventoryAdjustmentGroup { id }
    userErrors { field message code }
  }
}`

const mActivateInventory = `mutation Activate($inventoryItemId: ID!, $locationId: ID!) {
  inventoryActivate(inventoryItemId: $inventoryItemId, locationId: $locationId, available: 0) {
    inventoryLevel { id }
    userErrors { field message }
  }
}`

// MoveAvailable moves quantity units of "available" stock from one location to
// another. A destination that has never stocked the item (a freshly created
// Quarantine location) is activated on demand.
func (c *Client) MoveAvailable(ctx context.Context, inventoryItemID, fromLocationID, toLocationID string, quantity int, reason string) error {
	if quantity <= 0 {
		return errors.New("shopify: move quantity must be positive")
	}
	if reason == "" {
		reason = "correction"
	}
	err := c.adjust(ctx, inventoryItemID, fromLocationID, toLocationID, quantity, reason)
	var ue *UserErrors
	if errors.As(err, &ue) && ue.Errors[0].Code == "ITEM_NOT_STOCKED_AT_LOCATION" {
		if aerr := c.ActivateInventory(ctx, inventoryItemID, toLocationID); aerr != nil {
			return aerr
		}
		err = c.adjust(ctx, inventoryItemID, fromLocationID, toLocationID, quantity, reason)
	}
	return err
}

func (c *Client) adjust(ctx context.Context, inventoryItemID, fromLocationID, toLocationID string, quantity int, reason string) error {
	var out struct {
		R struct {
			UserErrors []UserError `json:"userErrors"`
		} `json:"inventoryAdjustQuantities"`
	}
	input := map[string]any{
		"name":                 "available",
		"reason":               reason,
		"referenceDocumentUri": referenceURI("move"),
		"changes": []map[string]any{
			{"inventoryItemId": inventoryItemID, "locationId": fromLocationID, "delta": -quantity},
			{"inventoryItemId": inventoryItemID, "locationId": toLocationID, "delta": quantity},
		},
	}
	if err := c.Do(ctx, mAdjustQuantities, map[string]any{"input": input}, &out); err != nil {
		return err
	}
	return userErrors("inventoryAdjustQuantities", out.R.UserErrors)
}

// ActivateInventory makes the item stockable at a location, at zero.
func (c *Client) ActivateInventory(ctx context.Context, inventoryItemID, locationID string) error {
	var out struct {
		R struct {
			UserErrors []UserError `json:"userErrors"`
		} `json:"inventoryActivate"`
	}
	vars := map[string]any{"inventoryItemId": inventoryItemID, "locationId": locationID}
	if err := c.Do(ctx, mActivateInventory, vars, &out); err != nil {
		return err
	}
	return userErrors("inventoryActivate", out.R.UserErrors)
}

// referenceURI tags inventory changes so the audit dossier can trace them.
func referenceURI(kind string) string {
	return fmt.Sprintf("soteria://containment/%s/%d", kind, time.Now().UnixNano())
}

// ---- product ----------------------------------------------------------------

const mProductUpdate = `mutation SetStatus($product: ProductUpdateInput!) {
  productUpdate(product: $product) {
    product { id status }
    userErrors { field message }
  }
}`

func (c *Client) SetProductStatus(ctx context.Context, productID, status string) error {
	switch status {
	case StatusActive, StatusDraft, StatusArchived:
	default:
		return fmt.Errorf("shopify: invalid product status %q", status)
	}
	var out struct {
		R struct {
			UserErrors []UserError `json:"userErrors"`
		} `json:"productUpdate"`
	}
	if err := c.Do(ctx, mProductUpdate, map[string]any{"product": map[string]any{"id": productID, "status": status}}, &out); err != nil {
		return err
	}
	return userErrors("productUpdate", out.R.UserErrors)
}

const mTagsAdd = `mutation AddTags($id: ID!, $tags: [String!]!) {
  tagsAdd(id: $id, tags: $tags) { node { id } userErrors { field message } }
}`

const mTagsRemove = `mutation RemoveTags($id: ID!, $tags: [String!]!) {
  tagsRemove(id: $id, tags: $tags) { node { id } userErrors { field message } }
}`

func (c *Client) AddTags(ctx context.Context, productID string, tags []string) error {
	var out struct {
		R struct {
			UserErrors []UserError `json:"userErrors"`
		} `json:"tagsAdd"`
	}
	if err := c.Do(ctx, mTagsAdd, map[string]any{"id": productID, "tags": tags}, &out); err != nil {
		return err
	}
	return userErrors("tagsAdd", out.R.UserErrors)
}

func (c *Client) RemoveTags(ctx context.Context, productID string, tags []string) error {
	var out struct {
		R struct {
			UserErrors []UserError `json:"userErrors"`
		} `json:"tagsRemove"`
	}
	if err := c.Do(ctx, mTagsRemove, map[string]any{"id": productID, "tags": tags}, &out); err != nil {
		return err
	}
	return userErrors("tagsRemove", out.R.UserErrors)
}

const mMetafieldsSet = `mutation SetMetafields($metafields: [MetafieldsSetInput!]!) {
  metafieldsSet(metafields: $metafields) {
    metafields { id }
    userErrors { field message code }
  }
}`

const mMetafieldsDelete = `mutation DeleteMetafields($metafields: [MetafieldIdentifierInput!]!) {
  metafieldsDelete(metafields: $metafields) {
    deletedMetafields { ownerId namespace key }
    userErrors { field message }
  }
}`

// SetMetafields writes the fields; a field with an empty value is deleted,
// because Shopify refuses to store one ("Value can't be blank") and a cleared
// badge should read as absent, not as an empty string.
func (c *Client) SetMetafields(ctx context.Context, fields []Metafield) error {
	var set, del []map[string]any
	for _, f := range fields {
		if f.Value == "" {
			del = append(del, map[string]any{"ownerId": f.OwnerID, "namespace": f.Namespace, "key": f.Key})
			continue
		}
		t := f.Type
		if t == "" {
			t = "single_line_text_field"
		}
		set = append(set, map[string]any{"ownerId": f.OwnerID, "namespace": f.Namespace, "key": f.Key, "type": t, "value": f.Value})
	}
	if len(set) > 0 {
		var out struct {
			R struct {
				UserErrors []UserError `json:"userErrors"`
			} `json:"metafieldsSet"`
		}
		if err := c.Do(ctx, mMetafieldsSet, map[string]any{"metafields": set}, &out); err != nil {
			return err
		}
		if err := userErrors("metafieldsSet", out.R.UserErrors); err != nil {
			return err
		}
	}
	if len(del) > 0 {
		var out struct {
			R struct {
				UserErrors []UserError `json:"userErrors"`
			} `json:"metafieldsDelete"`
		}
		if err := c.Do(ctx, mMetafieldsDelete, map[string]any{"metafields": del}, &out); err != nil {
			return err
		}
		// Deleting a metafield that does not exist is not an error to us.
		var ue []UserError
		for _, e := range out.R.UserErrors {
			if !strings.Contains(strings.ToLower(e.Message), "not found") && !strings.Contains(strings.ToLower(e.Message), "does not exist") {
				ue = append(ue, e)
			}
		}
		return userErrors("metafieldsDelete", ue)
	}
	return nil
}

// ---- order editing ----------------------------------------------------------

const mOrderEditBegin = `mutation Begin($id: ID!) {
  orderEditBegin(id: $id) {
    calculatedOrder { id lineItems(first: 100) { nodes { id quantity sku variant { id } } } }
    userErrors { field message }
  }
}`

const mOrderEditSetQuantity = `mutation SetQty($id: ID!, $lineItemId: ID!, $quantity: Int!) {
  orderEditSetQuantity(id: $id, lineItemId: $lineItemId, quantity: $quantity, restock: false) {
    calculatedLineItem { id quantity }
    userErrors { field message }
  }
}`

const mOrderEditAddVariant = `mutation AddVariant($id: ID!, $variantId: ID!, $quantity: Int!) {
  orderEditAddVariant(id: $id, variantId: $variantId, quantity: $quantity, allowDuplicates: true) {
    calculatedLineItem { id }
    userErrors { field message }
  }
}`

const mOrderEditCommit = `mutation Commit($id: ID!, $notify: Boolean!, $note: String) {
  orderEditCommit(id: $id, notifyCustomer: $notify, staffNote: $note) {
    order { id }
    userErrors { field message }
  }
}`

// ReplaceLineItem performs begin → set original quantity → add substitute →
// commit. Shopify order edits are transactional: nothing changes on the
// order until commit succeeds.
func (c *Client) ReplaceLineItem(ctx context.Context, orderID, lineItemID, newVariantID string, quantity int, notify bool) error {
	if quantity <= 0 {
		return errors.New("shopify: replace quantity must be positive")
	}
	var begin struct {
		R struct {
			CalculatedOrder *struct {
				ID        string `json:"id"`
				LineItems struct {
					Nodes []struct {
						ID       string `json:"id"`
						Quantity int    `json:"quantity"`
						SKU      string `json:"sku"`
						Variant  *struct {
							ID string `json:"id"`
						} `json:"variant"`
					} `json:"nodes"`
				} `json:"lineItems"`
			} `json:"calculatedOrder"`
			UserErrors []UserError `json:"userErrors"`
		} `json:"orderEditBegin"`
	}
	if err := c.Do(ctx, mOrderEditBegin, map[string]any{"id": orderID}, &begin); err != nil {
		return err
	}
	if err := userErrors("orderEditBegin", begin.R.UserErrors); err != nil {
		return err
	}
	if begin.R.CalculatedOrder == nil {
		return errors.New("shopify: orderEditBegin returned no calculatedOrder")
	}
	calcID := begin.R.CalculatedOrder.ID

	// CalculatedLineItem ids mirror the LineItem numeric id.
	wantNum := numericID(lineItemID)
	var calcLine string
	var current int
	for _, li := range begin.R.CalculatedOrder.LineItems.Nodes {
		if numericID(li.ID) == wantNum {
			calcLine, current = li.ID, li.Quantity
			break
		}
	}
	if calcLine == "" {
		return fmt.Errorf("shopify: line item %s not found on order %s", lineItemID, orderID)
	}
	if quantity > current {
		return fmt.Errorf("shopify: cannot replace %d units, line item has %d", quantity, current)
	}

	var setQ struct {
		R struct {
			UserErrors []UserError `json:"userErrors"`
		} `json:"orderEditSetQuantity"`
	}
	if err := c.Do(ctx, mOrderEditSetQuantity, map[string]any{"id": calcID, "lineItemId": calcLine, "quantity": current - quantity}, &setQ); err != nil {
		return err
	}
	if err := userErrors("orderEditSetQuantity", setQ.R.UserErrors); err != nil {
		return err
	}

	var add struct {
		R struct {
			UserErrors []UserError `json:"userErrors"`
		} `json:"orderEditAddVariant"`
	}
	if err := c.Do(ctx, mOrderEditAddVariant, map[string]any{"id": calcID, "variantId": newVariantID, "quantity": quantity}, &add); err != nil {
		return err
	}
	if err := userErrors("orderEditAddVariant", add.R.UserErrors); err != nil {
		return err
	}

	var commit struct {
		R struct {
			UserErrors []UserError `json:"userErrors"`
		} `json:"orderEditCommit"`
	}
	note := "Sotería recall substitution: customer-confirmed allergen-safe replacement"
	if err := c.Do(ctx, mOrderEditCommit, map[string]any{"id": calcID, "notify": notify, "note": note}, &commit); err != nil {
		return err
	}
	return userErrors("orderEditCommit", commit.R.UserErrors)
}

// numericID extracts the trailing numeric part of a gid.
func numericID(gid string) string {
	if i := strings.LastIndex(gid, "/"); i >= 0 {
		gid = gid[i+1:]
	}
	if i := strings.Index(gid, "?"); i >= 0 {
		gid = gid[:i]
	}
	return gid
}

var _ API = (*Client)(nil)

// ---- orders (write) -----------------------------------------------------------

const mOrderCreate = `mutation CreateOrder($order: OrderCreateOrderInput!, $options: OrderCreateOptionsInput) {
  orderCreate(order: $order, options: $options) {
    order { id name email createdAt displayFulfillmentStatus displayFinancialStatus
      lineItems(first: 20) { nodes { id sku title quantity variant { id } } } }
    userErrors { field message }
  }
}`

// OrderLine is one line of an order to create.
type OrderLine struct {
	VariantID string
	Quantity  int
}

// CreateOrder places a paid, unfulfilled order for the given lines — the shape
// the rescue flow looks for. It is deliberately not on the API interface: the
// domain services never create orders; demo and seeding tools do. With test
// set, Shopify marks the order as a test order (no real payment, hidden from
// most reports); use it for anything that is not a real sale.
func (c *Client) CreateOrder(ctx context.Context, email string, lines []OrderLine, test bool) (Order, error) {
	if len(lines) == 0 {
		return Order{}, errors.New("shopify: order needs at least one line")
	}
	items := make([]map[string]any, 0, len(lines))
	for _, l := range lines {
		if l.Quantity <= 0 {
			return Order{}, errors.New("shopify: order line quantity must be positive")
		}
		items = append(items, map[string]any{"variantId": l.VariantID, "quantity": l.Quantity})
	}
	var out struct {
		R struct {
			Order *struct {
				ID                       string    `json:"id"`
				Name                     string    `json:"name"`
				Email                    string    `json:"email"`
				CreatedAt                time.Time `json:"createdAt"`
				DisplayFulfillmentStatus string    `json:"displayFulfillmentStatus"`
				DisplayFinancialStatus   string    `json:"displayFinancialStatus"`
				LineItems                struct {
					Nodes []struct {
						ID       string `json:"id"`
						SKU      string `json:"sku"`
						Title    string `json:"title"`
						Quantity int    `json:"quantity"`
						Variant  *struct {
							ID string `json:"id"`
						} `json:"variant"`
					} `json:"nodes"`
				} `json:"lineItems"`
			} `json:"order"`
			UserErrors []UserError `json:"userErrors"`
		} `json:"orderCreate"`
	}
	order := map[string]any{
		"email":           email,
		"lineItems":       items,
		"financialStatus": "PAID",
		"test":            test,
		"tags":            []string{"soteria-demo"},
		"note":            "Created by Sotería democtl for the order-rescue flow.",
	}
	options := map[string]any{"inventoryBehaviour": "DECREMENT_OBEYING_POLICY", "sendReceipt": false}
	if err := c.Do(ctx, mOrderCreate, map[string]any{"order": order, "options": options}, &out); err != nil {
		return Order{}, err
	}
	if err := userErrors("orderCreate", out.R.UserErrors); err != nil {
		return Order{}, err
	}
	if out.R.Order == nil {
		return Order{}, errors.New("shopify: orderCreate returned no order")
	}
	o := Order{ID: out.R.Order.ID, Name: out.R.Order.Name, Email: out.R.Order.Email, CreatedAt: out.R.Order.CreatedAt,
		FulfillmentStatus: out.R.Order.DisplayFulfillmentStatus, FinancialStatus: out.R.Order.DisplayFinancialStatus}
	for _, li := range out.R.Order.LineItems.Nodes {
		l := LineItem{ID: li.ID, SKU: li.SKU, Title: li.Title, Quantity: li.Quantity}
		if li.Variant != nil {
			l.VariantID = li.Variant.ID
		}
		o.LineItems = append(o.LineItems, l)
	}
	return o, nil
}
