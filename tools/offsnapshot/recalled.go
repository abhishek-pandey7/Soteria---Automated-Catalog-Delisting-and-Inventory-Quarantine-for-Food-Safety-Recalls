package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"soteria/libs/core/matching"
)

// offProductBase is the single-product endpoint. The clean catalogue is built
// from /search; a recalled barcode is looked up directly because we want that
// exact product, not whatever a category happens to contain.
const offProductBase = "https://world.openfoodfacts.org/api/v2/product/"

type offProductResponse struct {
	Status  int        `json:"status"`
	Product offProduct `json:"product"`
}

// recalledRow is one line of the recalled-products report.
//
// It carries both names on purpose. OFF is keyed on barcode alone, and a barcode
// is occasionally reused or mis-entered, so the store's title beside OFF's is the
// only cheap way to see that a lookup answered about a different product.
type recalledRow struct {
	Barcode   string
	SeedTitle string
	OFFName   string
	Coverage  string
	Found     bool
	Duplicate bool
	Err       error
}

// product fetches one barcode. A status of 0 is "OFF does not have this", which
// is an answer rather than a failure.
func (f *fetcher) product(ctx context.Context, barcode string) (offProduct, bool, error) {
	endpoint := f.productBase + url.PathEscape(barcode) + "?fields=" + url.QueryEscape(offFields)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return offProduct{}, false, err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "application/json")

	resp, err := f.http.Do(req)
	if err != nil {
		return offProduct{}, false, retryable{fmt.Errorf("offsnapshot: product %s: %w", barcode, err)}
	}
	defer resp.Body.Close()

	// OFF answers an unknown barcode with 404 on this endpoint. That is "not
	// found", not an outage, so it must not be retried.
	if resp.StatusCode == http.StatusNotFound {
		return offProduct{}, false, nil
	}
	if resp.StatusCode != http.StatusOK {
		err := fmt.Errorf("offsnapshot: product %s: HTTP %d", barcode, resp.StatusCode)
		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
			return offProduct{}, false, retryable{err}
		}
		return offProduct{}, false, err
	}

	var out offProductResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return offProduct{}, false, fmt.Errorf("offsnapshot: product %s: decode: %w", barcode, err)
	}
	if out.Status != 1 {
		return offProduct{}, false, nil
	}
	return out.Product, true, nil
}

func (f *fetcher) productWithRetry(ctx context.Context, barcode string) (offProduct, bool, error) {
	var lastErr error
	for attempt := 0; attempt <= f.retries; attempt++ {
		if attempt > 0 {
			if err := f.sleep(ctx, backoffFor(attempt)); err != nil {
				return offProduct{}, false, err
			}
		}
		p, found, err := f.product(ctx, barcode)
		if err == nil {
			return p, found, nil
		}
		lastErr = err
		if !isRetryable(err) {
			return offProduct{}, false, err
		}
	}
	return offProduct{}, false, lastErr
}

// collectRecalled builds a snapshot entry for every barcode in the seed.
//
// These deliberately skip accept()'s rules. The clean catalogue is filtered to
// COMPLETE because it is a pool of substitutes, and offering one whose allergens
// nobody recorded is the failure the whole system exists to avoid. A recalled
// product is the opposite case: it is not being offered, it is being withdrawn,
// and it has to appear whatever OFF knows. Dropping it would make "we have no
// allergen data for this recalled item" indistinguishable from "this item is not
// under recall" — the storefront cannot tell an absent row from an unknown one.
//
// Coverage is still computed by the same rule as the clean path, so an entry
// never claims to know more than it does.
func (f *fetcher) collectRecalled(
	ctx context.Context,
	products []seedProduct,
	seen map[string]bool,
	fetchedAt string,
) ([]ProductAllergens, []recalledRow) {
	var kept []ProductAllergens
	rows := make([]recalledRow, 0, len(products))

	for _, sp := range products {
		barcode := strings.TrimSpace(sp.Barcode)
		row := recalledRow{Barcode: barcode, SeedTitle: sp.Title}

		// The seed's own validation requires a valid GTIN, so a failure here means
		// the seed drifted. Keep the product anyway under its printed barcode: a
		// recalled item missing from the snapshot is worse than an odd key.
		gtin := matching.NormalizeGTIN(barcode)
		if gtin == "" {
			gtin = barcode
			row.Err = fmt.Errorf("barcode is not a valid GTIN; stored as printed")
		}

		if seen[gtin] {
			row.Duplicate = true
			rows = append(rows, row)
			continue
		}

		p, found, err := f.productWithRetry(ctx, barcode)
		if err != nil {
			// A lookup that never answered is not evidence of absence. Record the
			// product with no data and say the request failed, rather than
			// implying OFF was asked and said nothing.
			row.Err = err
			row.Coverage = CoverageAbsent
			rows = append(rows, row)
			seen[gtin] = true
			kept = append(kept, ProductAllergens{
				GTIN: gtin, Source: SourceOFF, FetchedAt: fetchedAt,
				Coverage: CoverageAbsent, Allergens: []string{},
			})
			continue
		}

		row.Found = found

		allergens := clean(p.AllergensTags)
		traces := clean(p.TracesTags)
		ingredients := pickIngredients(p.IngredientsTextEN, p.IngredientsText)
		cov := coverage(allergens, traces, ingredients)

		row.OFFName = strings.TrimSpace(p.ProductName)
		row.Coverage = cov

		entry := ProductAllergens{
			GTIN:      gtin,
			Source:    SourceOFF,
			FetchedAt: fetchedAt,
			Coverage:  cov,
			Allergens: allergens,
			Traces:    traces,
		}
		// Only OFF's own data goes into an OFF-sourced record. When OFF has
		// nothing, the row is a bare GTIN with ABSENT coverage — which is the
		// honest statement, and is exactly what distinguishes it from a product
		// that is not in the file at all.
		if found {
			entry.ProductName = row.OFFName
			entry.Brand = strings.TrimSpace(p.Brands)
			entry.IngredientsText = ingredients
		}
		if entry.Allergens == nil {
			entry.Allergens = []string{}
		}

		seen[gtin] = true
		kept = append(kept, entry)
		rows = append(rows, row)

		if err := f.sleep(ctx, f.delay); err != nil {
			return kept, rows
		}
	}

	return kept, rows
}
