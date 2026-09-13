package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

// defaultCategories mirror the recalled products in tools/seedshop's seed, so a
// substitute the rescue flow offers is plausibly the same kind of thing as the
// item it replaces: noodles for noodles, ice cream for ice cream.
var defaultCategories = []string{
	"biscuits",
	"soups",
	"ice-creams",
	"noodles",
	"canned-fish",
	"prepared-meats",
}

func main() {
	var (
		out        = flag.String("out", "allergens.snapshot.json", "file to write")
		count      = flag.Int("count", 50, "how many products to keep")
		categories = flag.String("categories", strings.Join(defaultCategories, ","), "comma-separated OFF category tags")
		pageSize   = flag.Int("page-size", 100, "products requested per OFF page")
		maxPages   = flag.Int("max-pages", 12, "pages to walk per category before giving up")
		timeout    = flag.Duration("timeout", 30*time.Second, "per-request timeout")
		delay      = flag.Duration("delay", 700*time.Millisecond, "pause between OFF requests")
		retries    = flag.Int("retries", 3, "retries per page on a 429 or 5xx from OFF")
		seedPath   = flag.String("seed", "", "a tools/seedshop seed file; every barcode in it is added to the snapshot whatever OFF knows")
		verbose    = flag.Bool("verbose", false, "print every keep and reject")
	)
	flag.Parse()

	cats := splitCategories(*categories)
	if len(cats) == 0 {
		fmt.Fprintln(os.Stderr, "offsnapshot: no categories given")
		os.Exit(2)
	}
	if *count <= 0 {
		fmt.Fprintln(os.Stderr, "offsnapshot: -count must be positive")
		os.Exit(2)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	fmt.Printf("Open Food Facts snapshot\n")
	fmt.Printf("  categories : %s\n", strings.Join(cats, ", "))
	fmt.Printf("  target     : %d products\n\n", *count)

	f := newFetcher(*timeout, *delay, *retries, *verbose)
	fetchedAt := now()

	kept, fetched, tally, perCategory, failed, queried, err := f.collect(ctx, cats, *count, *pageSize, *maxPages, fetchedAt)
	if err != nil && len(kept) == 0 {
		fmt.Fprintf(os.Stderr, "offsnapshot: %v\n", err)
		os.Exit(1)
	}

	report(cats, kept, fetched, tally, perCategory, failed, queried)

	if *seedPath != "" {
		seed, err := loadSeed(*seedPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "offsnapshot: %v\n", err)
			os.Exit(1)
		}
		// The clean catalogue's GTINs, so a recalled barcode already in the pool
		// is not stored twice.
		seen := make(map[string]bool, len(kept))
		for _, p := range kept {
			seen[p.GTIN] = true
		}

		recalled, rows := f.collectRecalled(ctx, seed.Products, seen, fetchedAt)
		kept = append(kept, recalled...)
		reportRecalled(*seedPath, seed, rows)
	}

	if len(kept) == 0 {
		fmt.Fprintln(os.Stderr, "\noffsnapshot: nothing usable was found; not writing a file")
		os.Exit(1)
	}
	if err := write(*out, kept); err != nil {
		fmt.Fprintf(os.Stderr, "offsnapshot: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("\nwrote %d products to %s\n", len(kept), *out)

	if len(kept) < *count {
		// Not an error: a short snapshot is still usable, and pretending
		// otherwise would hide which categories are thin.
		fmt.Printf("note: asked for %d, kept %d — raise -max-pages or widen -categories\n",
			*count, len(kept))
	}
}

// report prints the funnel: what was fetched, what survived, and what each
// rejection reason cost. A filter this strict is only trustworthy if it says how
// much it threw away.
func report(categories []string, kept []ProductAllergens, fetched int, tally *Tally, perCategory map[string]int, failed, queried map[string]bool) {
	fmt.Printf("\ncandidates fetched : %d\n", fetched)
	fmt.Printf("kept               : %d\n", len(kept))
	fmt.Printf("rejected           : %d\n", tally.total())

	if lines := tally.lines(); len(lines) > 0 {
		fmt.Printf("\nrejected by reason\n")
		for _, l := range lines {
			fmt.Println(l)
		}
	}

	fmt.Printf("\nkept by category\n")
	for _, c := range categories {
		n := perCategory[c]
		switch {
		case failed[c]:
			// "OFF would not answer" and "this shelf has nothing usable" are
			// different facts, and only one of them is about the data.
			fmt.Printf("%6d  %s   (OFF did not answer — the snapshot is thin here, not the shelf)\n", n, c)
		case !queried[c]:
			// The target was met before this shelf was reached. Saying nothing
			// would imply OFF has nothing here, which is not what was learned.
			fmt.Printf("%6d  %s   (not reached — target met first)\n", n, c)
		case n == 0:
			// An OFF category tag that has drifted looks exactly like a category
			// with nothing usable in it.
			fmt.Printf("%6d  %s   (nothing kept — check the tag exists on OFF)\n", n, c)
		default:
			fmt.Printf("%6d  %s\n", n, c)
		}
	}

	withTraces := 0
	for _, p := range kept {
		if len(p.Traces) > 0 {
			withTraces++
		}
	}
	fmt.Printf("\nevery kept product is COVERAGE=%s; %d of %d also carry traces\n",
		CoverageComplete, withTraces, len(kept))
}

func splitCategories(raw string) []string {
	var out []string
	seen := map[string]bool{}
	for _, part := range strings.Split(raw, ",") {
		c := strings.TrimSpace(part)
		if c == "" || seen[c] {
			continue
		}
		seen[c] = true
		out = append(out, c)
	}
	return out
}

// reportRecalled prints what the seed contributed.
//
// The coverage breakdown is the point: some recalled products have no allergen
// data at all, and that has to be visible here rather than discovered later as a
// blank panel in the storefront. Both titles are shown because OFF is keyed on
// barcode alone, so a lookup can answer confidently about a different product.
func reportRecalled(path string, seed seedFile, rows []recalledRow) {
	fmt.Printf("\nrecalled products from %s\n", path)
	if seed.GeneratedAt != "" {
		fmt.Printf("  seed generated %s from %s\n", seed.GeneratedAt, seed.Source)
	}

	byCoverage := map[string]int{}
	added, duplicates, notFound, failures := 0, 0, 0, 0

	fmt.Printf("\n%-14s %-9s %-34s %s\n", "barcode", "coverage", "seed title", "open food facts")
	for _, r := range rows {
		switch {
		case r.Duplicate:
			duplicates++
			fmt.Printf("%-14s %-9s %-34s %s\n", r.Barcode, "-", truncate(r.SeedTitle, 34),
				"already in the clean set; not duplicated")
			continue
		case r.Err != nil:
			failures++
		case !r.Found:
			notFound++
		}

		added++
		byCoverage[r.Coverage]++

		note := r.OFFName
		switch {
		case r.Err != nil:
			note = "lookup failed: " + r.Err.Error()
		case !r.Found:
			note = "not in Open Food Facts"
		case note == "":
			note = "(found, but no product name)"
		}
		fmt.Printf("%-14s %-9s %-34s %s\n", r.Barcode, r.Coverage, truncate(r.SeedTitle, 34), truncate(note, 44))
	}

	fmt.Printf("\nadded %d recalled product(s): %d COMPLETE, %d PARTIAL, %d ABSENT\n",
		added, byCoverage[CoverageComplete], byCoverage[CoveragePartial], byCoverage[CoverageAbsent])
	if duplicates > 0 {
		fmt.Printf("%d already present in the clean set\n", duplicates)
	}
	if notFound > 0 {
		fmt.Printf("%d not in Open Food Facts at all — kept with ABSENT coverage, so the storefront\n"+
			"  can tell \"recalled, and we have no allergen data\" from \"not recalled\"\n", notFound)
	}
	if failures > 0 {
		fmt.Printf("%d lookup(s) failed; those are recorded ABSENT but were never actually answered\n", failures)
	}
	fmt.Printf("compare the two title columns: OFF is keyed on barcode alone, so a confident\n" +
		"  answer about the wrong product looks exactly like a right one\n")
}
