package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// offBase is the public Open Food Facts API. No key, no account.
const offBase = "https://world.openfoodfacts.org/api/v2/search"

// userAgent follows OFF's request that clients identify themselves, and matches
// the shape tools/seedshop already uses for openFDA.
const userAgent = "soteria-offsnapshot/1.0 (+https://soteria.dev)"

// offFields is the projection requested from OFF. ingredients_text_en is asked
// for alongside ingredients_text so English can be preferred when it exists.
const offFields = "code,product_name,brands,allergens_tags,traces_tags,ingredients_text,ingredients_text_en"

// offProduct is the subset of an OFF product this tool reads.
type offProduct struct {
	Code              string   `json:"code"`
	ProductName       string   `json:"product_name"`
	Brands            string   `json:"brands"`
	AllergensTags     []string `json:"allergens_tags"`
	TracesTags        []string `json:"traces_tags"`
	IngredientsText   string   `json:"ingredients_text"`
	IngredientsTextEN string   `json:"ingredients_text_en"`
}

type offSearchResponse struct {
	Count     int          `json:"count"`
	Page      int          `json:"page"`
	PageSize  int          `json:"page_size"`
	PageCount int          `json:"page_count"`
	Products  []offProduct `json:"products"`
}

// retryable marks a failure that says "ask again" rather than "there is nothing
// here": a timeout, a 429, a 5xx.
type retryable struct{ err error }

func (r retryable) Error() string { return r.err.Error() }
func (r retryable) Unwrap() error { return r.err }

func isRetryable(err error) bool {
	var r retryable
	return errors.As(err, &r)
}

type fetcher struct {
	http        *http.Client
	base        string
	productBase string
	delay       time.Duration
	retries     int
	verbose     bool
}

// backoffFor is the pause before one more attempt.
func backoffFor(attempt int) time.Duration {
	return time.Duration(attempt) * 2 * time.Second
}

// sleep waits, but gives up the moment the run is cancelled.
func (f *fetcher) sleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(d):
		return nil
	}
}

// pageWithRetry re-asks on a transient failure, backing off each time.
func (f *fetcher) pageWithRetry(ctx context.Context, category string, page, pageSize int) (offSearchResponse, error) {
	var lastErr error
	for attempt := 0; attempt <= f.retries; attempt++ {
		if attempt > 0 {
			backoff := backoffFor(attempt)
			if f.verbose {
				fmt.Printf("  retry  %s page %d in %s (%v)\n", category, page, backoff, lastErr)
			}
			if err := f.sleep(ctx, backoff); err != nil {
				return offSearchResponse{}, err
			}
		}
		res, err := f.page(ctx, category, page, pageSize)
		if err == nil {
			return res, nil
		}
		lastErr = err
		if !isRetryable(err) {
			return offSearchResponse{}, err
		}
	}
	return offSearchResponse{}, lastErr
}

func newFetcher(timeout, delay time.Duration, retries int, verbose bool) *fetcher {
	return &fetcher{
		http:        &http.Client{Timeout: timeout},
		base:        offBase,
		productBase: offProductBase,
		delay:       delay,
		retries:     retries,
		verbose:     verbose,
	}
}

// page fetches one page of one category.
//
// A category that yields nothing is reported rather than skipped silently: OFF's
// category tags drift, and a misspelled one is otherwise indistinguishable from
// a category with no usable products.
func (f *fetcher) page(ctx context.Context, category string, page, pageSize int) (offSearchResponse, error) {
	q := url.Values{}
	q.Set("categories_tags_en", category)
	q.Set("fields", offFields)
	q.Set("page_size", strconv.Itoa(pageSize))
	q.Set("page", strconv.Itoa(page))
	// Ask OFF for entries that at least claim to have the data we need. This is
	// a hint, not a guarantee — every product is still filtered locally.
	q.Set("states_tags", "en:ingredients-completed")

	endpoint := f.base + "?" + q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return offSearchResponse{}, err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "application/json")

	resp, err := f.http.Do(req)
	if err != nil {
		return offSearchResponse{}, retryable{fmt.Errorf("offsnapshot: %s page %d: %w", category, page, err)}
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		err := fmt.Errorf("offsnapshot: %s page %d: HTTP %d", category, page, resp.StatusCode)
		// OFF rate-limits and sheds load freely. A 429 or 5xx says "ask again",
		// not "this category is empty" — treating the two alike is how a run
		// ends up taking every product from whichever shelf answered first.
		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
			return offSearchResponse{}, retryable{err}
		}
		return offSearchResponse{}, err
	}

	var out offSearchResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return offSearchResponse{}, fmt.Errorf("offsnapshot: %s page %d: decode: %w", category, page, err)
	}
	return out, nil
}

// collect walks the categories in turn, taking pages until the target is met or
// the categories run out.
//
// Categories are visited round-robin by page rather than draining one before
// starting the next, so a snapshot of 50 is spread across the shelf instead of
// being 50 biscuits.
func (f *fetcher) collect(
	ctx context.Context,
	categories []string,
	target, pageSize, maxPages int,
	fetchedAt string,
) ([]ProductAllergens, int, *Tally, map[string]int, map[string]bool, map[string]bool, error) {
	var kept []ProductAllergens
	seen := map[string]bool{}
	tally := newTally()
	perCategory := map[string]int{}
	fetched := 0

	exhausted := map[string]bool{}
	failed := map[string]bool{}
	queried := map[string]bool{}

	// Per-pass quota, so one well-stocked category cannot fill the whole target
	// before the others are asked even once. Categories that run dry are made up
	// for by later passes over the ones that have not.
	quota := (target + len(categories) - 1) / len(categories)
	if quota < 1 {
		quota = 1
	}

	for page := 1; page <= maxPages && len(kept) < target; page++ {
		progress := false

		for _, category := range categories {
			if len(kept) >= target {
				break
			}
			if exhausted[category] {
				continue
			}
			queried[category] = true

			res, err := f.pageWithRetry(ctx, category, page, pageSize)
			if err != nil {
				// One bad category must not lose the whole run. Once the
				// retries are spent, stop asking this one and keep the others —
				// but record that OFF would not answer, which is a different
				// fact from "this shelf has nothing usable".
				fmt.Fprintf(os.Stderr, "  warning: giving up on %v\n", err)
				exhausted[category] = true
				failed[category] = true
				continue
			}
			if len(res.Products) == 0 {
				exhausted[category] = true
				continue
			}
			progress = true
			fetched += len(res.Products)

			taken := 0
			for _, p := range res.Products {
				if len(kept) >= target || taken >= quota {
					break
				}
				entry, reason, ok := accept(p, seen, fetchedAt)
				if !ok {
					tally.add(reason)
					if f.verbose {
						fmt.Printf("  reject %-14s %s\n", p.Code, reason)
					}
					continue
				}
				seen[entry.GTIN] = true
				kept = append(kept, entry)
				perCategory[category]++
				taken++
				if f.verbose {
					fmt.Printf("  keep   %-14s %s\n", entry.GTIN, truncate(entry.ProductName, 48))
				}
			}

			if f.delay > 0 {
				select {
				case <-ctx.Done():
					return kept, fetched, tally, perCategory, failed, queried, ctx.Err()
				case <-time.After(f.delay):
				}
			}
		}

		if !progress {
			break
		}
	}

	return kept, fetched, tally, perCategory, failed, queried, nil
}

func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}
