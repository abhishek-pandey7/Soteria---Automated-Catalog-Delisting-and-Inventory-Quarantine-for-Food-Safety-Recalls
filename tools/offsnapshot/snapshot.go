// Command offsnapshot builds a committed snapshot of real Open Food Facts
// products, so the storefront can show real allergen data without calling OFF on
// every page load.
//
// Only products whose allergen data is actually usable are kept. A product with
// an empty allergens_tags is rejected rather than stored: OFF returns the same
// empty array for "a contributor checked and there are none" and for "nobody has
// filled this in", and treating the second as the first is exactly the mistake
// that makes the rescue flow offer an unchecked substitute as a safe one.
//
// Usage:
//
//	go run . -count 50
//	go run . -count 50 -out ../../apps/storefront/src/api/allergens.snapshot.json
//	go run . -categories biscuits,soups -verbose
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Coverage mirrors apps/storefront/src/api/allergens.ts.
const (
	CoverageComplete = "COMPLETE"
	CoveragePartial  = "PARTIAL"
	CoverageAbsent   = "ABSENT"
)

// SourceOFF is the only value the storefront's ProductAllergens.source accepts.
const SourceOFF = "OPEN_FOOD_FACTS"

// ProductAllergens is the storefront's type, field for field, so the snapshot
// deserialises into it with no new type on that side:
//
//	apps/storefront/src/api/allergens.ts
//
// `allergens` carries no omitempty because the TypeScript field is required and
// must be an array, never absent.
type ProductAllergens struct {
	GTIN            string   `json:"gtin"`
	ProductName     string   `json:"product_name,omitempty"`
	Brand           string   `json:"brand,omitempty"`
	Source          string   `json:"source"`
	FetchedAt       string   `json:"fetched_at"`
	Coverage        string   `json:"coverage"`
	Allergens       []string `json:"allergens"`
	Traces          []string `json:"traces,omitempty"`
	IngredientsText string   `json:"ingredients_text,omitempty"`
}

// write emits the snapshot as a JSON array, newest run overwriting the last.
func write(path string, products []ProductAllergens) error {
	if dir := filepath.Dir(path); dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("create %s: %w", dir, err)
		}
	}
	body, err := json.MarshalIndent(products, "", "  ")
	if err != nil {
		return err
	}
	body = append(body, '\n')
	if err := os.WriteFile(path, body, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

func now() string { return time.Now().UTC().Format(time.RFC3339) }
