package shopify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// mockShop is a tiny GraphQL responder keyed on the operation name.
type mockShop struct {
	t        *testing.T
	mu       sync.Mutex
	requests []gqlRequest
	handlers map[string]func(vars map[string]any, n int) (status int, body string)
	counts   map[string]int
}

func newMockShop(t *testing.T) (*mockShop, *Client) {
	m := &mockShop{t: t, handlers: map[string]func(map[string]any, int) (int, string){}, counts: map[string]int{}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Shopify-Access-Token") != "shpat_test" {
			http.Error(w, `{"errors":"[API] Invalid API key or access token"}`, 401)
			return
		}
		if !strings.HasPrefix(r.URL.Path, "/admin/api/") || !strings.HasSuffix(r.URL.Path, "/graphql.json") {
			http.Error(w, "bad path "+r.URL.Path, 404)
			return
		}
		var req gqlRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		op := opName(req.Query)
		m.mu.Lock()
		m.requests = append(m.requests, req)
		m.counts[op]++
		n := m.counts[op]
		h := m.handlers[op]
		m.mu.Unlock()
		if h == nil {
			t.Errorf("unexpected operation %q", op)
			http.Error(w, "unexpected op "+op, 500)
			return
		}
		status, body := h(req.Variables, n)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)

	c, err := New("test.myshopify.com", "shpat_test", WithHTTPClient(srv.Client()), WithRetries(3))
	if err != nil {
		t.Fatal(err)
	}
	// Point the client at the test server: swap the transport's target.
	c.http.Transport = rewriteTransport{base: srv.Client().Transport, target: strings.TrimPrefix(srv.URL, "http://")}
	return m, c
}

// rewriteTransport sends every request to the test server regardless of host.
type rewriteTransport struct {
	base   http.RoundTripper
	target string
}

func (r rewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req.URL.Scheme = "http"
	req.URL.Host = r.target
	return r.base.RoundTrip(req)
}

func opName(query string) string {
	q := strings.TrimSpace(query)
	q = strings.TrimPrefix(q, "query")
	q = strings.TrimPrefix(q, "mutation")
	q = strings.TrimSpace(q)
	end := strings.IndexAny(q, "({ ")
	if end < 0 {
		return q
	}
	return q[:end]
}

func (m *mockShop) on(op string, h func(vars map[string]any, n int) (int, string)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.handlers[op] = h
}

func ok(data string) (int, string) {
	return 200, `{"data":` + data + `,"extensions":{"cost":{"requestedQueryCost":10,"actualQueryCost":5,"throttleStatus":{"maximumAvailable":1000,"currentlyAvailable":990,"restoreRate":100}}}}`
}

// ---- transport ---------------------------------------------------------------

func TestRejectsBadConstructorArgs(t *testing.T) {
	if _, err := New("", "x"); err == nil {
		t.Error("empty shop accepted")
	}
	if _, err := New("shop.myshopify.com", ""); err == nil {
		t.Error("empty token accepted")
	}
	c, err := New("https://shop.myshopify.com/", "shpat_x", WithAPIVersion("2025-10"))
	if err != nil || c.Endpoint() != "https://shop.myshopify.com/admin/api/2025-10/graphql.json" {
		t.Errorf("endpoint: %v %v", c.Endpoint(), err)
	}
}

func TestRetriesThrottledThenSucceeds(t *testing.T) {
	m, c := newMockShop(t)
	m.on("Locations", func(_ map[string]any, n int) (int, string) {
		if n == 1 {
			return 200, `{"errors":[{"message":"Throttled","extensions":{"code":"THROTTLED"}}],"extensions":{"cost":{"requestedQueryCost":10,"actualQueryCost":0,"throttleStatus":{"maximumAvailable":1000,"currentlyAvailable":5,"restoreRate":1000}}}}`
		}
		return ok(`{"locations":{"nodes":[{"id":"gid://shopify/Location/1","name":"Main","isActive":true,"fulfillsOnlineOrders":true}]}}`)
	})
	locs, err := c.Locations(context.Background())
	if err != nil || len(locs) != 1 || locs[0].Name != "Main" {
		t.Fatalf("locs=%v err=%v", locs, err)
	}
	if m.counts["Locations"] != 2 {
		t.Fatalf("want 2 attempts got %d", m.counts["Locations"])
	}
}

func TestRetries5xxButNot4xx(t *testing.T) {
	m, c := newMockShop(t)
	m.on("Locations", func(_ map[string]any, n int) (int, string) {
		if n < 3 {
			return 502, "bad gateway"
		}
		return ok(`{"locations":{"nodes":[]}}`)
	})
	if _, err := c.Locations(context.Background()); err != nil {
		t.Fatal(err)
	}
	if m.counts["Locations"] != 3 {
		t.Fatalf("want 3 attempts got %d", m.counts["Locations"])
	}

	m.on("Locations", func(_ map[string]any, n int) (int, string) { return 403, `{"errors":"forbidden"}` })
	m.counts["Locations"] = 0
	_, err := c.Locations(context.Background())
	var he *HTTPError
	if !errors.As(err, &he) || he.Status != 403 || m.counts["Locations"] != 1 {
		t.Fatalf("want single 403 HTTPError, got %v after %d attempts", err, m.counts["Locations"])
	}
}

func TestGraphQLErrorsSurface(t *testing.T) {
	m, c := newMockShop(t)
	m.on("Locations", func(_ map[string]any, n int) (int, string) {
		return 200, `{"errors":[{"message":"Access denied for locations field.","extensions":{"code":"ACCESS_DENIED","requiredAccess":"read_locations"}}]}`
	})
	_, err := c.Locations(context.Background())
	var ge GraphQLErrors
	if !errors.As(err, &ge) || ge[0].Code() != "ACCESS_DENIED" {
		t.Fatalf("want ACCESS_DENIED GraphQLErrors, got %v", err)
	}
	if m.counts["Locations"] != 1 {
		t.Fatal("ACCESS_DENIED must not be retried")
	}
}

func TestBudgetWaitUsesObservedRestoreRate(t *testing.T) {
	m, c := newMockShop(t)
	// Response says the bucket is nearly empty and refills at 1000/s.
	m.on("Locations", func(_ map[string]any, n int) (int, string) {
		return 200, `{"data":{"locations":{"nodes":[]}},"extensions":{"cost":{"requestedQueryCost":1,"actualQueryCost":1,"throttleStatus":{"maximumAvailable":1000,"currentlyAvailable":0,"restoreRate":1000}}}}`
	})
	if _, err := c.Locations(context.Background()); err != nil {
		t.Fatal(err)
	}
	c.mu.Lock()
	avail, rate := c.available, c.restore
	c.mu.Unlock()
	if avail != 0 || rate != 1000 {
		t.Fatalf("bucket not observed: avail=%v rate=%v", avail, rate)
	}
	// Next call needs a small budget; with 1000/s restore the wait is a few ms, not a timeout.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	start := time.Now()
	if _, err := c.Locations(ctx); err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > time.Second {
		t.Fatalf("waited too long for budget: %v", time.Since(start))
	}
}

// ---- operations --------------------------------------------------------------

const variantJSON = `{"id":"gid://shopify/ProductVariant/%d","title":"%s","sku":"%s","barcode":"%s","price":"4.99",
 "product":{"id":"gid://shopify/Product/%d","title":"%s","status":"ACTIVE","handle":"h","vendor":"Brand X","tags":["snack"]},
 "inventoryItem":{"id":"gid://shopify/InventoryItem/%d","inventoryLevels":{"nodes":[
   {"location":{"id":"gid://shopify/Location/1"},"quantities":[{"name":"available","quantity":%d}]},
   {"location":{"id":"gid://shopify/Location/2"},"quantities":[{"name":"available","quantity":%d}]}]}}}`

func variant(n int, title, sku, barcode string, q1, q2 int) string {
	return fmt.Sprintf(variantJSON, n, title, sku, barcode, n, "Product "+title, n, q1, q2)
}

func TestCatalogPaginates(t *testing.T) {
	m, c := newMockShop(t)
	m.on("Catalog", func(vars map[string]any, n int) (int, string) {
		switch vars["cursor"] {
		case nil:
			return ok(`{"productVariants":{"pageInfo":{"hasNextPage":true,"endCursor":"c1"},"nodes":[` + variant(1, "16 oz", "PB-16", "012345678905", 40, 10) + `]}}`)
		case "c1":
			return ok(`{"productVariants":{"pageInfo":{"hasNextPage":false,"endCursor":"c2"},"nodes":[` + variant(2, "32 oz", "PB-32", "012345678912", 5, 0) + `]}}`)
		}
		return 500, "bad cursor"
	})
	vs, err := c.Catalog(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(vs) != 2 || m.counts["Catalog"] != 2 {
		t.Fatalf("variants=%d pages=%d", len(vs), m.counts["Catalog"])
	}
	v := vs[0]
	if v.Barcode != "012345678905" || v.ProductID != "gid://shopify/Product/1" || v.InventoryItemID != "gid://shopify/InventoryItem/1" {
		t.Errorf("mapping: %+v", v)
	}
	if v.Available() != 50 || len(v.Inventory) != 2 || v.Inventory[1].LocationID != "gid://shopify/Location/2" {
		t.Errorf("inventory: %+v", v.Inventory)
	}
	if v.Vendor != "Brand X" || v.Tags[0] != "snack" || v.ProductStatus != StatusActive {
		t.Errorf("product fields: %+v", v)
	}
}

func TestVariantsByBarcodeKeepsExactMatchesOnly(t *testing.T) {
	m, c := newMockShop(t)
	m.on("VariantsByBarcode", func(vars map[string]any, n int) (int, string) {
		q := vars["q"].(string)
		if !strings.Contains(q, "barcode:012345678905 OR barcode:999") {
			t.Errorf("query: %s", q)
		}
		// Server returns a prefix hit too; client must drop it.
		return ok(`{"productVariants":{"nodes":[` + variant(1, "a", "A", "012345678905", 1, 0) + `,` + variant(3, "b", "B", "0123456789050", 1, 0) + `]}}`)
	})
	vs, err := c.VariantsByBarcode(context.Background(), []string{"012345678905", " 999 ", ""})
	if err != nil || len(vs) != 1 || vs[0].Barcode != "012345678905" {
		t.Fatalf("vs=%v err=%v", vs, err)
	}
}

func TestOrdersSinceQueryAndMapping(t *testing.T) {
	m, c := newMockShop(t)
	since := time.Date(2026, 9, 5, 0, 0, 0, 0, time.UTC)
	m.on("OrdersSince", func(vars map[string]any, n int) (int, string) {
		if vars["q"] != "created_at:>='2026-09-05T00:00:00Z'" {
			t.Errorf("q: %v", vars["q"])
		}
		return ok(`{"orders":{"pageInfo":{"hasNextPage":false,"endCursor":""},"nodes":[
		  {"id":"gid://shopify/Order/10","name":"#1001","createdAt":"2026-09-11T10:00:00Z","email":"a@b.c","customer":{"id":"gid://shopify/Customer/7"},
		   "displayFulfillmentStatus":"UNFULFILLED","displayFinancialStatus":"PAID",
		   "lineItems":{"nodes":[{"id":"gid://shopify/LineItem/100","sku":"PB-16","title":"PB 16 oz","quantity":2,"variant":{"id":"gid://shopify/ProductVariant/1"}},
		                         {"id":"gid://shopify/LineItem/101","sku":"X","title":"deleted variant","quantity":1,"variant":null}]}}]}}`)
	})
	os, err := c.OrdersSince(context.Background(), since)
	if err != nil || len(os) != 1 {
		t.Fatalf("orders=%v err=%v", os, err)
	}
	o := os[0]
	if o.CustomerID != "gid://shopify/Customer/7" || o.FulfillmentStatus != "UNFULFILLED" || len(o.LineItems) != 2 {
		t.Errorf("order: %+v", o)
	}
	if !o.Contains("gid://shopify/ProductVariant/1") || o.Contains("gid://shopify/ProductVariant/2") {
		t.Error("Contains")
	}
	if o.LineItems[1].VariantID != "" {
		t.Error("null variant should map to empty id")
	}
}

func TestMutationsSendInputsAndSurfaceUserErrors(t *testing.T) {
	m, c := newMockShop(t)
	ctx := context.Background()

	m.on("SetAvailable", func(vars map[string]any, n int) (int, string) {
		in := vars["input"].(map[string]any)
		q := in["quantities"].([]any)[0].(map[string]any)
		if in["name"] != "available" || in["reason"] != "quality_control" || q["quantity"].(float64) != 0 || in["ignoreCompareQuantity"] != true {
			t.Errorf("input: %v", in)
		}
		return ok(`{"inventorySetQuantities":{"inventoryAdjustmentGroup":{"id":"g1"},"userErrors":[]}}`)
	})
	if err := c.SetAvailable(ctx, "gid://shopify/InventoryItem/1", "gid://shopify/Location/1", 0, "quality_control"); err != nil {
		t.Fatal(err)
	}

	m.on("MoveAvailable", func(vars map[string]any, n int) (int, string) {
		in := vars["input"].(map[string]any)
		ch := in["changes"].([]any)
		from, to := ch[0].(map[string]any), ch[1].(map[string]any)
		if in["name"] != "available" || from["delta"].(float64) != -12 || from["locationId"] != "L1" || to["delta"].(float64) != 12 || to["locationId"] != "L2" || !strings.HasPrefix(in["referenceDocumentUri"].(string), "soteria://containment/move/") {
			t.Errorf("input: %v", in)
		}
		if n == 1 {
			// Quarantine has never stocked this item: the client must activate it and retry.
			return ok(`{"inventoryAdjustQuantities":{"inventoryAdjustmentGroup":null,"userErrors":[{"field":["input","changes","1"],"message":"not stocked","code":"ITEM_NOT_STOCKED_AT_LOCATION"}]}}`)
		}
		return ok(`{"inventoryAdjustQuantities":{"inventoryAdjustmentGroup":null,"userErrors":[{"field":["input","changes","0","delta"],"message":"Only 10 available","code":"INVALID_QUANTITY_TOO_LOW"}]}}`)
	})
	activated := 0
	m.on("Activate", func(vars map[string]any, n int) (int, string) {
		activated++
		if vars["inventoryItemId"] != "I1" || vars["locationId"] != "L2" {
			t.Errorf("activate vars: %v", vars)
		}
		return ok(`{"inventoryActivate":{"inventoryLevel":{"id":"lvl1"},"userErrors":[]}}`)
	})
	err := c.MoveAvailable(ctx, "I1", "L1", "L2", 12, "")
	var ue *UserErrors
	if !errors.As(err, &ue) || ue.Mutation != "inventoryAdjustQuantities" || ue.Errors[0].Code != "INVALID_QUANTITY_TOO_LOW" {
		t.Fatalf("want UserErrors, got %v", err)
	}
	if activated != 1 {
		t.Errorf("activated %d times, want 1", activated)
	}
	if err := c.MoveAvailable(ctx, "I1", "L1", "L2", 0, ""); err == nil {
		t.Error("zero move accepted")
	}

	m.on("SetStatus", func(vars map[string]any, n int) (int, string) {
		p := vars["product"].(map[string]any)
		if p["id"] != "gid://shopify/Product/1" || p["status"] != "DRAFT" {
			t.Errorf("product: %v", p)
		}
		return ok(`{"productUpdate":{"product":{"id":"gid://shopify/Product/1","status":"DRAFT"},"userErrors":[]}}`)
	})
	if err := c.SetProductStatus(ctx, "gid://shopify/Product/1", StatusDraft); err != nil {
		t.Fatal(err)
	}
	if err := c.SetProductStatus(ctx, "gid://shopify/Product/1", "hidden"); err == nil {
		t.Error("invalid status accepted")
	}

	m.on("AddTags", func(vars map[string]any, n int) (int, string) {
		if tags := vars["tags"].([]any); len(tags) != 2 || tags[0] != "RECALL_HAZARD" {
			t.Errorf("tags: %v", vars["tags"])
		}
		return ok(`{"tagsAdd":{"node":{"id":"gid://shopify/Product/1"},"userErrors":[]}}`)
	})
	if err := c.AddTags(ctx, "gid://shopify/Product/1", []string{"RECALL_HAZARD", "RECALL_LOT:2026-X89"}); err != nil {
		t.Fatal(err)
	}

	m.on("SetMetafields", func(vars map[string]any, n int) (int, string) {
		mf := vars["metafields"].([]any)[0].(map[string]any)
		if mf["namespace"] != "soteria" || mf["key"] != "badge" || mf["type"] != "single_line_text_field" {
			t.Errorf("metafield: %v", mf)
		}
		return ok(`{"metafieldsSet":{"metafields":[{"id":"gid://shopify/Metafield/1"}],"userErrors":[]}}`)
	})
	if err := c.SetMetafields(ctx, []Metafield{{OwnerID: "gid://shopify/Product/1", Namespace: "soteria", Key: "badge", Value: "Verified Safe Lot"}}); err != nil {
		t.Fatal(err)
	}
}

func TestReplaceLineItemRunsTheEditSequence(t *testing.T) {
	m, c := newMockShop(t)
	var seq []string
	m.on("Begin", func(vars map[string]any, n int) (int, string) {
		seq = append(seq, "begin")
		return ok(`{"orderEditBegin":{"calculatedOrder":{"id":"gid://shopify/CalculatedOrder/9","lineItems":{"nodes":[
		  {"id":"gid://shopify/CalculatedLineItem/100","quantity":3,"sku":"PB-16","variant":{"id":"gid://shopify/ProductVariant/1"}}]}},"userErrors":[]}}`)
	})
	m.on("SetQty", func(vars map[string]any, n int) (int, string) {
		seq = append(seq, "setqty")
		if vars["id"] != "gid://shopify/CalculatedOrder/9" || vars["lineItemId"] != "gid://shopify/CalculatedLineItem/100" || vars["quantity"].(float64) != 1 {
			t.Errorf("setqty vars: %v", vars)
		}
		return ok(`{"orderEditSetQuantity":{"calculatedLineItem":{"id":"x","quantity":1},"userErrors":[]}}`)
	})
	m.on("AddVariant", func(vars map[string]any, n int) (int, string) {
		seq = append(seq, "add")
		if vars["variantId"] != "gid://shopify/ProductVariant/2" || vars["quantity"].(float64) != 2 {
			t.Errorf("add vars: %v", vars)
		}
		return ok(`{"orderEditAddVariant":{"calculatedLineItem":{"id":"y"},"userErrors":[]}}`)
	})
	m.on("Commit", func(vars map[string]any, n int) (int, string) {
		seq = append(seq, "commit")
		if vars["notify"] != true {
			t.Errorf("commit vars: %v", vars)
		}
		return ok(`{"orderEditCommit":{"order":{"id":"gid://shopify/Order/10"},"userErrors":[]}}`)
	})
	err := c.ReplaceLineItem(context.Background(), "gid://shopify/Order/10", "gid://shopify/LineItem/100", "gid://shopify/ProductVariant/2", 2, true)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(seq, ",") != "begin,setqty,add,commit" {
		t.Fatalf("sequence: %v", seq)
	}

	// Asking for more than the line has must fail before any edit mutation.
	seq = nil
	err = c.ReplaceLineItem(context.Background(), "gid://shopify/Order/10", "gid://shopify/LineItem/100", "gid://shopify/ProductVariant/2", 5, false)
	if err == nil || strings.Join(seq, ",") != "begin" {
		t.Fatalf("over-replace: err=%v seq=%v", err, seq)
	}
}

func TestTokenNeverAppearsInErrors(t *testing.T) {
	_, c := newMockShop(t)
	c.token = "shpat_secret_value" // the mock only accepts shpat_test → 401
	_, err := c.Locations(context.Background())
	if err == nil || strings.Contains(err.Error(), "shpat_secret_value") {
		t.Fatalf("token leaked or no error: %v", err)
	}
}
