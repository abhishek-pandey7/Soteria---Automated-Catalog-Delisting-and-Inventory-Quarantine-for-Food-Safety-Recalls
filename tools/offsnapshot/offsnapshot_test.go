package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestEmptyAllergensIsNotComplete pins the rule the whole tool exists to hold.
//
// OFF sends the same empty array for "a contributor checked and there are none"
// and for "nobody has filled this in". Reading the second as the first would tell
// an allergic customer a product is clear when nobody ever looked.
func TestEmptyAllergensIsNotComplete(t *testing.T) {
	if got := coverage(nil, nil, "wheat flour, sugar"); got != CoveragePartial {
		t.Errorf("empty tags with ingredients: got %s, want %s", got, CoveragePartial)
	}
	if got := coverage([]string{}, []string{}, "wheat flour, sugar"); got != CoveragePartial {
		t.Errorf("empty slices with ingredients: got %s, want %s", got, CoveragePartial)
	}
	if got := coverage(nil, nil, "   "); got != CoverageAbsent {
		t.Errorf("nothing at all: got %s, want %s", got, CoverageAbsent)
	}
	if got := coverage(nil, nil, ""); got != CoverageAbsent {
		t.Errorf("no data: got %s, want %s", got, CoverageAbsent)
	}
}

// TestTracesAloneEarnComplete: traces are evidence that someone looked, which is
// what COMPLETE means here, matching openFoodFacts.ts.
func TestTracesAloneEarnComplete(t *testing.T) {
	if got := coverage(nil, []string{"en:nuts"}, "oats"); got != CoverageComplete {
		t.Errorf("traces only: got %s, want %s", got, CoverageComplete)
	}
	if got := coverage([]string{"en:milk"}, nil, "milk, sugar"); got != CoverageComplete {
		t.Errorf("allergens only: got %s, want %s", got, CoverageComplete)
	}
}

func TestAcceptRejectsUnusableProducts(t *testing.T) {
	base := offProduct{
		Code:            "3017620422003", // valid EAN-13 check digit
		ProductName:     "Nutella",
		Brands:          "Ferrero",
		AllergensTags:   []string{"en:milk", "en:nuts"},
		IngredientsText: "sugar, palm oil, hazelnuts",
	}

	cases := []struct {
		name   string
		mutate func(*offProduct)
		want   Reason
		wantOK bool
	}{
		{"complete product is kept", func(*offProduct) {}, "", true},
		{"no barcode", func(p *offProduct) { p.Code = "" }, ReasonNoBarcode, false},
		{"bad check digit", func(p *offProduct) { p.Code = "3017620422004" }, ReasonBadCheckDigit, false},
		{"not a gtin width", func(p *offProduct) { p.Code = "12345" }, ReasonBadCheckDigit, false},
		{"no name", func(p *offProduct) { p.ProductName = "  " }, ReasonNoName, false},
		{
			"no allergen or trace tags",
			func(p *offProduct) { p.AllergensTags = nil; p.TracesTags = nil },
			ReasonNoAllergenTags, false,
		},
		{
			"blank-only allergen tags",
			func(p *offProduct) { p.AllergensTags = []string{"", "  "}; p.TracesTags = nil },
			ReasonNoAllergenTags, false,
		},
		{
			"no ingredients text",
			func(p *offProduct) { p.IngredientsText = ""; p.IngredientsTextEN = "" },
			ReasonNoIngredients, false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := base
			tc.mutate(&p)
			got, reason, ok := accept(p, map[string]bool{}, "2026-09-13T00:00:00Z")
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v (reason %q)", ok, tc.wantOK, reason)
			}
			if !ok && reason != tc.want {
				t.Fatalf("reason = %q, want %q", reason, tc.want)
			}
			if ok && got.Coverage != CoverageComplete {
				t.Fatalf("a kept product must be %s, got %s", CoverageComplete, got.Coverage)
			}
		})
	}
}

func TestAcceptNormalisesBarcodeAndRejectsDuplicates(t *testing.T) {
	p := offProduct{
		Code:            "0072250007399", // 13 digits
		ProductName:     "Bread",
		AllergensTags:   []string{"en:gluten"},
		IngredientsText: "wheat flour",
	}
	seen := map[string]bool{}

	got, _, ok := accept(p, seen, "2026-09-13T00:00:00Z")
	if !ok {
		t.Fatal("expected the product to be kept")
	}
	if len(got.GTIN) != 14 {
		t.Errorf("gtin = %q, want a 14-digit normalised form", got.GTIN)
	}
	seen[got.GTIN] = true

	// The same product under a different printed width is the same product.
	p.Code = "072250007399" // 12 digits, same GTIN
	if _, reason, ok := accept(p, seen, "2026-09-13T00:00:00Z"); ok || reason != ReasonDuplicate {
		t.Errorf("re-adding the same GTIN: ok=%v reason=%q, want a duplicate rejection", ok, reason)
	}
}

func TestEnglishIngredientsArePreferred(t *testing.T) {
	if got := pickIngredients("wheat flour, sugar", "farine de blé, sucre"); got != "wheat flour, sugar" {
		t.Errorf("got %q, want the English text", got)
	}
	if got := pickIngredients("   ", "farine de blé"); got != "farine de blé" {
		t.Errorf("blank English should fall back, got %q", got)
	}
	if got := pickIngredients("", ""); got != "" {
		t.Errorf("got %q, want empty", got)
	}
}

// TestSnapshotShapeMatchesStorefront guards the field names the storefront's
// ProductAllergens declares. A rename here is a silent break there, because the
// storefront parses this file as that type without validating it.
func TestSnapshotShapeMatchesStorefront(t *testing.T) {
	body, err := json.Marshal(ProductAllergens{
		GTIN: "00030176204220", Source: SourceOFF, FetchedAt: "2026-09-13T00:00:00Z",
		Coverage: CoverageComplete, Allergens: []string{"en:milk"},
	})
	if err != nil {
		t.Fatal(err)
	}

	var doc map[string]any
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"gtin", "source", "fetched_at", "coverage", "allergens"} {
		if _, ok := doc[key]; !ok {
			t.Errorf("required field %q missing from the snapshot shape", key)
		}
	}
	// Optional in TypeScript, so they must be omitted rather than null.
	for _, key := range []string{"product_name", "brand", "traces", "ingredients_text"} {
		if _, ok := doc[key]; ok {
			t.Errorf("field %q should be omitted when empty, not serialised", key)
		}
	}
	if doc["source"] != SourceOFF {
		t.Errorf("source = %v, want %q", doc["source"], SourceOFF)
	}
}

func TestTallyCountsAndOrders(t *testing.T) {
	tally := newTally()
	tally.add(ReasonNoName)
	tally.add(ReasonNoAllergenTags)
	tally.add(ReasonNoAllergenTags)

	if tally.total() != 3 {
		t.Errorf("total = %d, want 3", tally.total())
	}
	lines := tally.lines()
	if len(lines) != 2 {
		t.Fatalf("lines = %d, want 2", len(lines))
	}
	if want := string(ReasonNoAllergenTags); lines[0][len(lines[0])-len(want):] != want {
		t.Errorf("most frequent reason should sort first, got %q", lines[0])
	}
}

func TestSplitCategoriesDedupesAndTrims(t *testing.T) {
	got := splitCategories(" biscuits , soups,biscuits, ,noodles ")
	want := []string{"biscuits", "soups", "noodles"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

// stubOFF serves the product endpoint from a fixture map, so the recalled path
// is tested without touching the network.
func stubOFF(t *testing.T, byBarcode map[string]string) *fetcher {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		code := strings.TrimPrefix(r.URL.Path, "/")
		body, ok := byBarcode[code]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)

	f := newFetcher(5*time.Second, 0, 0, false)
	f.productBase = srv.URL + "/"
	f.http = srv.Client()
	return f
}

// TestRecalledProductsAreKeptWhateverTheirCoverage is the rule that separates the
// two halves of the snapshot.
//
// The clean catalogue is filtered to COMPLETE because it is a substitute pool. A
// recalled product is being withdrawn, not offered, so it must appear whatever
// OFF knows — otherwise "recalled, and we have no allergen data" is
// indistinguishable from "not recalled at all".
func TestRecalledProductsAreKeptWhateverTheirCoverage(t *testing.T) {
	f := stubOFF(t, map[string]string{
		// Allergens recorded: COMPLETE.
		"850073087008": `{"status":1,"product":{"product_name":"Brown Butter Chocolate Chunk",
			"brands":"Bakr","allergens_tags":["en:gluten","en:milk"],"ingredients_text":"flour, butter"}}`,
		// Ingredients but no allergen tags: PARTIAL.
		"042272005833": `{"status":1,"product":{"product_name":"Lentil Soup","ingredients_text":"lentils, water"}}`,
		// Found, but OFF holds nothing usable: ABSENT.
		"00175029": `{"status":1,"product":{"product_name":"Imitation Crab Meat Sticks","brands":"Island Pacific"}}`,
		// status 0 is "OFF does not have this": ABSENT.
		"858792003323": `{"status":0}`,
	})

	seed := []seedProduct{
		{Title: "Bakr Brown Butter", Barcode: "850073087008"},
		{Title: "Amy's Lentil", Barcode: "042272005833"},
		{Title: "Island Pacific Crab", Barcode: "00175029"},
		{Title: "Mellish Island Super Skin", Barcode: "858792003323"},
	}

	kept, rows := f.collectRecalled(context.Background(), seed, map[string]bool{}, "2026-09-13T00:00:00Z")
	if len(kept) != 4 {
		t.Fatalf("kept %d, want all 4 recalled products regardless of coverage", len(kept))
	}
	if len(rows) != 4 {
		t.Fatalf("rows %d, want 4", len(rows))
	}

	want := map[string]string{
		"00850073087008": CoverageComplete,
		"00042272005833": CoveragePartial,
		"00000000175029": CoverageAbsent,
		"00858792003323": CoverageAbsent,
	}
	for _, p := range kept {
		if got := want[p.GTIN]; got != p.Coverage {
			t.Errorf("%s: coverage %s, want %s", p.GTIN, p.Coverage, got)
		}
		if p.Source != SourceOFF {
			t.Errorf("%s: source %q, want %q", p.GTIN, p.Source, SourceOFF)
		}
		if p.Allergens == nil {
			t.Errorf("%s: allergens must be an array, not null", p.GTIN)
		}
	}
}

// TestRecalledNotFoundCarriesNoBorrowedData: when OFF has nothing, the entry is a
// bare GTIN. The seed's own title is not copied in, because the record claims
// source OPEN_FOOD_FACTS and every field in it must actually come from there.
func TestRecalledNotFoundCarriesNoBorrowedData(t *testing.T) {
	f := stubOFF(t, map[string]string{"858792003323": `{"status":0}`})

	kept, _ := f.collectRecalled(context.Background(),
		[]seedProduct{{Title: "Mellish Island Super Skin", Barcode: "858792003323"}},
		map[string]bool{}, "2026-09-13T00:00:00Z")

	if len(kept) != 1 {
		t.Fatalf("kept %d, want 1", len(kept))
	}
	got := kept[0]
	if got.ProductName != "" || got.Brand != "" || got.IngredientsText != "" {
		t.Errorf("entry borrowed data OFF did not supply: %+v", got)
	}
	if got.Coverage != CoverageAbsent {
		t.Errorf("coverage = %s, want %s", got.Coverage, CoverageAbsent)
	}
}

// TestRecalledSkipsBarcodesAlreadyInTheCleanSet: dedup is on the normalised GTIN,
// so the same product under a different printed width is not stored twice.
func TestRecalledSkipsBarcodesAlreadyInTheCleanSet(t *testing.T) {
	f := stubOFF(t, map[string]string{
		"0072250007399": `{"status":1,"product":{"product_name":"Bread","allergens_tags":["en:gluten"],"ingredients_text":"wheat flour"}}`,
	})

	// Already present from the clean pass, under the 14-digit form.
	seen := map[string]bool{"00072250007399": true}

	kept, rows := f.collectRecalled(context.Background(),
		[]seedProduct{{Title: "Bread", Barcode: "0072250007399"}}, seen, "2026-09-13T00:00:00Z")

	if len(kept) != 0 {
		t.Fatalf("kept %d, want 0: the product is already in the clean set", len(kept))
	}
	if len(rows) != 1 || !rows[0].Duplicate {
		t.Fatalf("expected one row marked duplicate, got %+v", rows)
	}
}

func TestLoadSeedReadsBarcodesAndRejectsEmpty(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "seed.json")
	if err := os.WriteFile(good, []byte(`{"generated_at":"2026-09-13T00:00:00Z","source":"openFDA",
		"products":[{"title":"A","barcode":"850073087008"},{"title":"B","barcode":"  "}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := loadSeed(good)
	if err != nil {
		t.Fatalf("loadSeed: %v", err)
	}
	if len(s.Products) != 1 || s.Products[0].Barcode != "850073087008" {
		t.Errorf("products = %+v, want only the one with a barcode", s.Products)
	}

	empty := filepath.Join(dir, "empty.json")
	if err := os.WriteFile(empty, []byte(`{"products":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := loadSeed(empty); err == nil {
		t.Error("a seed with no usable barcodes should be an error, not a silent no-op")
	}
}
