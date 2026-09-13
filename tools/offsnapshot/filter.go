package main

import (
	"fmt"
	"sort"
	"strings"

	"soteria/libs/core/matching"
)

// Reason is why a candidate did not make the snapshot. Every rejection is
// counted and reported: a filter that silently drops most of what it fetched is
// indistinguishable from one that is broken.
type Reason string

const (
	ReasonNoBarcode      Reason = "no barcode"
	ReasonBadCheckDigit  Reason = "barcode fails its check digit"
	ReasonNoName         Reason = "no product_name"
	ReasonNoAllergenTags Reason = "allergens_tags and traces_tags both empty"
	ReasonNoIngredients  Reason = "no ingredients_text"
	ReasonNotComplete    Reason = "coverage below COMPLETE"
	ReasonDuplicate      Reason = "duplicate barcode"
)

// Tally counts rejections by reason, in a stable order for reporting.
type Tally struct {
	counts map[Reason]int
	order  []Reason
}

func newTally() *Tally { return &Tally{counts: map[Reason]int{}} }

func (t *Tally) add(r Reason) {
	if _, seen := t.counts[r]; !seen {
		t.order = append(t.order, r)
	}
	t.counts[r]++
}

func (t *Tally) total() int {
	n := 0
	for _, c := range t.counts {
		n += c
	}
	return n
}

// lines renders the tally most-frequent first, ties broken by first appearance.
func (t *Tally) lines() []string {
	rs := append([]Reason(nil), t.order...)
	sort.SliceStable(rs, func(i, j int) bool { return t.counts[rs[i]] > t.counts[rs[j]] })
	out := make([]string, 0, len(rs))
	for _, r := range rs {
		out = append(out, fmt.Sprintf("%6d  %s", t.counts[r], r))
	}
	return out
}

// coverage is a transliteration of the rule in
// apps/storefront/src/api/openFoodFacts.ts, deliberately kept identical:
//
//   - a NON-EMPTY allergens_tags or traces_tags is the only evidence that anyone
//     looked, and the only thing that earns COMPLETE;
//   - ingredients_text alone means the product has some contributed data but
//     nobody derived allergens from it: PARTIAL;
//   - nothing at all: ABSENT.
//
// An empty allergens_tags is never COMPLETE. OFF sends the same empty array for
// "checked, none" and "nobody filled this in", so reading it as a verified
// absence would tell an allergic customer a product is clear when nobody ever
// checked.
func coverage(allergens, traces []string, ingredientsText string) string {
	if len(allergens) > 0 || len(traces) > 0 {
		return CoverageComplete
	}
	if strings.TrimSpace(ingredientsText) != "" {
		return CoveragePartial
	}
	return CoverageAbsent
}

// pickIngredients prefers OFF's English text when it has one. The storefront
// shows this to a shopper, and an ingredients list they cannot read is not much
// better than none.
func pickIngredients(english, native string) string {
	if s := strings.TrimSpace(english); s != "" {
		return s
	}
	return strings.TrimSpace(native)
}

// accept converts one OFF product into a snapshot entry, or explains why not.
//
// The order of the checks is the order of cheapness, so the tally reads as a
// funnel rather than as an arbitrary set of counts.
func accept(p offProduct, seen map[string]bool, fetchedAt string) (ProductAllergens, Reason, bool) {
	code := strings.TrimSpace(p.Code)
	if code == "" {
		return ProductAllergens{}, ReasonNoBarcode, false
	}

	// The same validator resolution-service and seedshop use, so a product in
	// this snapshot is one a recall could actually match.
	gtin := matching.NormalizeGTIN(code)
	if gtin == "" {
		return ProductAllergens{}, ReasonBadCheckDigit, false
	}
	if seen[gtin] {
		return ProductAllergens{}, ReasonDuplicate, false
	}

	name := strings.TrimSpace(p.ProductName)
	if name == "" {
		return ProductAllergens{}, ReasonNoName, false
	}

	allergens := clean(p.AllergensTags)
	traces := clean(p.TracesTags)
	if len(allergens) == 0 && len(traces) == 0 {
		return ProductAllergens{}, ReasonNoAllergenTags, false
	}

	ingredients := pickIngredients(p.IngredientsTextEN, p.IngredientsText)
	if ingredients == "" {
		return ProductAllergens{}, ReasonNoIngredients, false
	}

	cov := coverage(allergens, traces, ingredients)
	if cov != CoverageComplete {
		// Unreachable given the checks above; kept so the invariant is enforced
		// rather than assumed, because it is the one that matters.
		return ProductAllergens{}, ReasonNotComplete, false
	}

	return ProductAllergens{
		GTIN:            gtin,
		ProductName:     name,
		Brand:           strings.TrimSpace(p.Brands),
		Source:          SourceOFF,
		FetchedAt:       fetchedAt,
		Coverage:        cov,
		Allergens:       allergens,
		Traces:          traces,
		IngredientsText: ingredients,
	}, "", true
}

// clean drops blank tags and preserves OFF's own order and prefixes ("en:milk"),
// because the storefront stores tags verbatim and normalises at the point of use.
func clean(tags []string) []string {
	out := make([]string, 0, len(tags))
	for _, t := range tags {
		if s := strings.TrimSpace(t); s != "" {
			out = append(out, s)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
