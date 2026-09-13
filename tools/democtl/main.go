// Command democtl makes the recall demo repeatable against the live store and
// the real broker.
//
//	democtl status                      what is held, tagged or drafted right now
//	democtl reset [--restart]           release every hold, clear tags and badges,
//	                                    optionally restart the stateful services
//	democtl inject <case-id|file.json>  publish one real recall.raw.received.v1
//	democtl order <barcode> [--qty N]   place a paid, unfulfilled test order so
//	                                    order-rescue has something to rescue
//
// Env (a .env in the working directory or any parent is loaded): SHOPIFY_SHOP,
// SHOPIFY_ACCESS_TOKEN, SHOPIFY_API_VERSION, QUARANTINE_LOCATION (Quarantine),
// RABBITMQ_URL (amqp://soteria:soteria-local@localhost:5672/), CASES
// (services/recall-extractor/eval/cases.json), COMPOSE_FILE (infra/docker-compose.yml).
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/joho/godotenv"
	amqp "github.com/rabbitmq/amqp091-go"

	core "soteria/libs/core/shopify"
	"soteria/libs/shopify"
	"soteria/libs/shopify/hold"
)

func main() {
	loadDotEnv()
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	var err error
	switch os.Args[1] {
	case "status":
		err = status(ctx)
	case "reset":
		err = reset(ctx, os.Args[2:])
	case "inject":
		err = inject(ctx, os.Args[2:])
	case "order":
		err = order(ctx, os.Args[2:])
	default:
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: democtl <status | reset [--restart] | inject <case-id|file> | order <barcode> [--qty N] [--email E]>")
}

// ---- store ------------------------------------------------------------------

func client() (*shopify.Client, error) {
	shop, token := os.Getenv("SHOPIFY_SHOP"), os.Getenv("SHOPIFY_ACCESS_TOKEN")
	if shop == "" || token == "" {
		return nil, errors.New("SHOPIFY_SHOP and SHOPIFY_ACCESS_TOKEN must be set")
	}
	var opts []shopify.Option
	if v := os.Getenv("SHOPIFY_API_VERSION"); v != "" {
		opts = append(opts, shopify.WithAPIVersion(v))
	}
	return shopify.New(shop, token, opts...)
}

// touched reports whether a variant carries any trace of a containment action.
func touched(v shopify.Variant) (heldLots []string, tags []string, drafted bool) {
	for _, l := range v.Lots {
		if l.Held {
			heldLots = append(heldLots, l.Code)
		}
	}
	for _, t := range v.Tags {
		if t == hold.TagHazard || strings.HasPrefix(t, hold.TagLotPrefix) {
			tags = append(tags, t)
		}
	}
	return heldLots, tags, v.ProductStatus == shopify.StatusDraft
}

func status(ctx context.Context) error {
	c, err := client()
	if err != nil {
		return err
	}
	variants, err := c.Catalog(ctx)
	if err != nil {
		return err
	}
	clean := 0
	for _, v := range variants {
		held, tags, drafted := touched(v)
		if len(held) == 0 && len(tags) == 0 && !drafted {
			clean++
			continue
		}
		fmt.Printf("%-14s %-8s %s\n    held lots %v  tags %v\n", v.Barcode, v.ProductStatus, v.ProductTitle, held, tags)
	}
	fmt.Printf("%d variants clean, %d touched by containment\n", clean, len(variants)-clean)
	return nil
}

func reset(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("reset", flag.ExitOnError)
	restart := fs.Bool("restart", false, "also restart the in-memory domain services via docker compose")
	_ = fs.Parse(args)

	c, err := client()
	if err != nil {
		return err
	}
	selling, quarantine, err := hold.ResolveLocations(ctx, c, envOr("QUARANTINE_LOCATION", "Quarantine"))
	if err != nil {
		return err
	}
	adapter := hold.New(c, selling, quarantine)
	variants, err := c.Catalog(ctx)
	if err != nil {
		return err
	}
	released := 0
	for _, v := range variants {
		held, tags, drafted := touched(v)
		if len(held) == 0 && len(tags) == 0 && !drafted {
			continue
		}
		if len(held) > 0 {
			// The same adapter containment used, in reverse: units back to the
			// selling location, Held cleared, lot tags removed, badge/status
			// restored once nothing is held.
			resp, err := adapter.ReleaseLots(ctx, core.HoldRequest{GTIN: v.Barcode, SKU: v.SKU, LotCodes: held, Reason: "demo reset"})
			if err != nil {
				return fmt.Errorf("release %s: %w", v.Barcode, err)
			}
			fmt.Printf("released %-14s %v (%d units back on sale)\n", v.Barcode, held, resp.UnitsLeftSellable)
		}
		// Anything the release did not cover: tags left by a whole-SKU hold on
		// a variant with no ledger, a DRAFT from an earlier run.
		if len(tags) > 0 {
			if err := c.RemoveTags(ctx, v.ProductID, tags); err != nil {
				return err
			}
		}
		if err := c.SetMetafields(ctx, []shopify.Metafield{{OwnerID: v.ProductID, Namespace: shopify.MetafieldNamespace, Key: shopify.BadgeKey, Type: "single_line_text_field", Value: ""}}); err != nil {
			return err
		}
		if drafted {
			if err := c.SetProductStatus(ctx, v.ProductID, shopify.StatusActive); err != nil {
				return err
			}
		}
		released++
	}
	fmt.Printf("store clean: %d product(s) restored\n", released)

	if *restart {
		// resolution, containment, rescue and audit keep incident state in
		// memory; a restart is the reset. Notification and evasion keep ledgers
		// on disk and are left alone: they are history, not state.
		compose := envOr("COMPOSE_FILE", filepath.Join(repoRoot(), "infra", "docker-compose.yml"))
		cmd := exec.CommandContext(ctx, "docker", "compose", "-f", compose, "restart",
			"resolution-service", "containment-service", "order-rescue-service", "audit-proof-service")
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("restart services: %w", err)
		}
	}
	return nil
}

// ---- broker -----------------------------------------------------------------

func inject(ctx context.Context, args []string) error {
	if len(args) < 1 {
		return errors.New("inject needs a case id (e.g. H-1258-2026) or a JSON file")
	}
	raw, err := loadNotice(args[0])
	if err != nil {
		return err
	}
	// A fresh identity every time: consumers dedupe on event_id, and the
	// incident id derives from source + source_id, so a re-injected notice is a
	// new delivery of the same incident, which is exactly what a re-run is.
	eventID := uuid.NewString()
	raw["event_id"] = eventID
	raw["occurred_at"] = time.Now().UTC().Format(time.RFC3339Nano)
	body, _ := json.Marshal(raw)

	url := envOr("RABBITMQ_URL", "amqp://soteria:soteria-local@localhost:5672/")
	conn, err := amqp.Dial(url)
	if err != nil {
		return fmt.Errorf("dial broker: %w", err)
	}
	defer conn.Close()
	ch, err := conn.Channel()
	if err != nil {
		return err
	}
	defer ch.Close()
	if err := ch.Confirm(false); err != nil {
		return err
	}
	confirms := ch.NotifyPublish(make(chan amqp.Confirmation, 1))
	const key = "ingestion.recall.raw.received.v1"
	if err := ch.PublishWithContext(ctx, "ingestion.x", key, true, false, amqp.Publishing{
		ContentType: "application/json", DeliveryMode: amqp.Persistent, MessageId: eventID,
		Type: "recall.raw.received.v1", Timestamp: time.Now(), Body: body,
	}); err != nil {
		return err
	}
	select {
	case c := <-confirms:
		if !c.Ack {
			return errors.New("broker nacked the publish")
		}
	case <-ctx.Done():
		return ctx.Err()
	}
	title, _ := raw["normalized"].(map[string]any)["title"].(string)
	fmt.Printf("published %s  %s/%s\n  %s\n", eventID, raw["source"], raw["source_id"], title)
	fmt.Println("follow it: curl localhost:8082/v1/containment/actions ; curl localhost:8085/v1/dossiers")
	return nil
}

// loadNotice returns the flat recall.raw.received.v1 for a case id from the
// extractor eval set, or the contents of a JSON file.
func loadNotice(ref string) (map[string]any, error) {
	if strings.HasSuffix(ref, ".json") {
		b, err := os.ReadFile(ref)
		if err != nil {
			return nil, err
		}
		var raw map[string]any
		if err := json.Unmarshal(b, &raw); err != nil {
			return nil, err
		}
		if inner, ok := raw["raw"].(map[string]any); ok && raw["source_id"] == nil {
			raw = inner // an eval case rather than a bare event
		}
		return raw, nil
	}
	path := envOr("CASES", filepath.Join(repoRoot(), "services", "recall-extractor", "eval", "cases.json"))
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read cases: %w", err)
	}
	var cases []struct {
		ID  string         `json:"id"`
		Raw map[string]any `json:"raw"`
	}
	if err := json.Unmarshal(b, &cases); err != nil {
		return nil, err
	}
	var ids []string
	for _, c := range cases {
		if c.ID == ref {
			return c.Raw, nil
		}
		ids = append(ids, c.ID)
	}
	return nil, fmt.Errorf("no case %q; have %s", ref, strings.Join(ids, ", "))
}

// ---- orders -----------------------------------------------------------------

func order(ctx context.Context, args []string) error {
	if len(args) < 1 {
		return errors.New("order needs a barcode")
	}
	fs := flag.NewFlagSet("order", flag.ExitOnError)
	qty := fs.Int("qty", 2, "units on the line")
	email := fs.String("email", "customer@example.com", "customer email (the rescue offer is addressed here)")
	real := fs.Bool("real", false, "create a real (non-test) order")
	_ = fs.Parse(args[1:])

	c, err := client()
	if err != nil {
		return err
	}
	variants, err := c.VariantsByBarcode(ctx, barcodeForms(args[0]))
	if err != nil {
		return err
	}
	if len(variants) != 1 {
		return fmt.Errorf("barcode %s matches %d variants", args[0], len(variants))
	}
	v := variants[0]
	o, err := c.CreateOrder(ctx, *email, []shopify.OrderLine{{VariantID: v.ID, Quantity: *qty}}, !*real)
	if err != nil {
		return err
	}
	fmt.Printf("order %s (%s) for %s: %d x %s [%s]\n", o.Name, o.ID, o.Email, *qty, v.ProductTitle, o.FulfillmentStatus)
	fmt.Println("  in the rescue scan for the next 30 days; a hold on this product now proposes a rescue")
	return nil
}

func barcodeForms(b string) []string {
	b = strings.TrimSpace(b)
	trimmed := strings.TrimLeft(b, "0")
	forms := []string{b, trimmed}
	for _, n := range []int{8, 12, 13, 14} {
		if len(trimmed) <= n {
			forms = append(forms, strings.Repeat("0", n-len(trimmed))+trimmed)
		}
	}
	return forms
}

// ---- helpers ----------------------------------------------------------------

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// loadDotEnv loads the nearest .env walking up from the working directory.
func loadDotEnv() {
	dir, _ := os.Getwd()
	for {
		if p := filepath.Join(dir, ".env"); fileExists(p) {
			_ = godotenv.Load(p)
			return
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return
		}
		dir = parent
	}
}

// repoRoot is the nearest ancestor with a go.work.
func repoRoot() string {
	dir, _ := os.Getwd()
	for {
		if fileExists(filepath.Join(dir, "go.work")) {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "."
		}
		dir = parent
	}
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}
