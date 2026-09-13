package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// seedFile is the part of a tools/seedshop seed this tool reads.
//
// Declared locally rather than imported: seedshop is a separate module, and the
// only coupling worth having between the two is the barcode. A struct copy is
// cheaper than a dependency that would drag in the Shopify client.
type seedFile struct {
	GeneratedAt string        `json:"generated_at"`
	Source      string        `json:"source"`
	Products    []seedProduct `json:"products"`
}

type seedProduct struct {
	Title   string `json:"title"`
	Barcode string `json:"barcode"`
}

func loadSeed(path string) (seedFile, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return seedFile{}, fmt.Errorf("offsnapshot: read seed %s: %w", path, err)
	}
	var s seedFile
	if err := json.Unmarshal(body, &s); err != nil {
		return seedFile{}, fmt.Errorf("offsnapshot: parse seed %s: %w", path, err)
	}

	var usable []seedProduct
	for _, p := range s.Products {
		if strings.TrimSpace(p.Barcode) != "" {
			usable = append(usable, p)
		}
	}
	s.Products = usable

	if len(s.Products) == 0 {
		return seedFile{}, fmt.Errorf("offsnapshot: seed %s has no products with a barcode", path)
	}
	return s, nil
}
