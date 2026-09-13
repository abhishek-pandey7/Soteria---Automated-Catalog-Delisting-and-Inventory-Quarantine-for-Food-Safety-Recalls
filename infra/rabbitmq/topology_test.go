// Package rabbitmq holds the broker's declarative topology and the test that
// keeps it honest.
//
// Three things describe the same topology and can drift apart:
//
//  1. contracts/rabbitmq-topology.md, which the team reviews;
//  2. definitions.json, which the broker imports at boot;
//  3. the queues and bindings each service declares for itself on startup.
//
// Drift is silent and expensive: a queue bound in one place and not the other
// means events vanish with nothing in any log to say so. These tests compare all
// three.
package rabbitmq

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// deliveryLimit must equal bus.MaxRetries. The broker and the services declare
// these queues independently and RabbitMQ rejects a second declaration whose
// arguments differ, so a mismatch here stops a service from starting at all.
const deliveryLimit = 5

type definitions struct {
	Exchanges []struct {
		Name    string `json:"name"`
		Type    string `json:"type"`
		Durable bool   `json:"durable"`
	} `json:"exchanges"`
	Queues []struct {
		Name      string         `json:"name"`
		Durable   bool           `json:"durable"`
		Arguments map[string]any `json:"arguments"`
	} `json:"queues"`
	Bindings []struct {
		Source      string `json:"source"`
		Destination string `json:"destination"`
		RoutingKey  string `json:"routing_key"`
	} `json:"bindings"`
}

func load(t *testing.T) definitions {
	t.Helper()
	raw, err := os.ReadFile("definitions.json")
	if err != nil {
		t.Fatalf("read definitions: %v", err)
	}
	var d definitions
	if err := json.Unmarshal(raw, &d); err != nil {
		t.Fatalf("parse definitions: %v", err)
	}
	return d
}

func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "contracts")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not find the repo root")
		}
		dir = parent
	}
}

// TestEveryExchangeInTheRegistryIsDeclared: the contracts doc is the registry of
// record, so anything named there must exist in the broker.
func TestEveryExchangeInTheRegistryIsDeclared(t *testing.T) {
	doc, err := os.ReadFile(filepath.Join(repoRoot(t), "contracts", "rabbitmq-topology.md"))
	if err != nil {
		t.Fatalf("read topology registry: %v", err)
	}

	declared := map[string]bool{}
	for _, x := range load(t).Exchanges {
		declared[x.Name] = true
	}

	// Exchange names in the registry appear as `name.x` in backticks.
	re := regexp.MustCompile("`([a-z]+\\.x)`")
	seen := map[string]bool{}
	for _, m := range re.FindAllStringSubmatch(string(doc), -1) {
		name := m[1]
		if seen[name] {
			continue
		}
		seen[name] = true
		if !declared[name] {
			t.Errorf("%s is in the registry but the broker never declares it: events published there would be dropped", name)
		}
	}
	if len(seen) == 0 {
		t.Fatal("found no exchanges in the registry; the parser or the doc changed")
	}
}

// TestEveryQueueHasADeadLetterPair. Without one, a handler that keeps failing
// either spins forever or drops the message, and the audit trail's completeness
// claim rests on neither happening.
func TestEveryQueueHasADeadLetterPair(t *testing.T) {
	d := load(t)
	queues := map[string]bool{}
	for _, q := range d.Queues {
		queues[q.Name] = true
	}

	for _, q := range d.Queues {
		if strings.HasSuffix(q.Name, ".dlq") {
			continue
		}
		if !q.Durable {
			t.Errorf("queue %s is not durable: a broker restart would lose it", q.Name)
		}
		dlx, ok := q.Arguments["x-dead-letter-exchange"].(string)
		if !ok || dlx == "" {
			t.Errorf("queue %s has no dead-letter exchange", q.Name)
			continue
		}
		// The delivery budget is enforced by the broker, not by the consumer: a
		// consumer counting attempts in memory forgets them on restart, and a
		// message no handler can process would then retry forever. Quorum queues
		// count deliveries and dead-letter at the limit.
		if qt, _ := q.Arguments["x-queue-type"].(string); qt != "quorum" {
			t.Errorf("queue %s is %q, not quorum: nothing would enforce the delivery limit", q.Name, qt)
		}
		if limit, ok := q.Arguments["x-delivery-limit"].(float64); !ok || int(limit) != deliveryLimit {
			t.Errorf("queue %s has delivery limit %v, want %d (bus.MaxRetries)", q.Name, q.Arguments["x-delivery-limit"], deliveryLimit)
		}
		if !queues[q.Name+".dlq"] {
			t.Errorf("queue %s dead-letters to %s but there is no %s.dlq to catch it", q.Name, dlx, q.Name)
		}
	}
}

// TestDeadLetterQueuesAreBound: a DLQ that exists but is not bound to its
// exchange silently discards everything sent to it, which is the worst of both.
func TestDeadLetterQueuesAreBound(t *testing.T) {
	d := load(t)
	bound := map[string]bool{}
	for _, b := range d.Bindings {
		if strings.HasSuffix(b.Destination, ".dlq") {
			if b.RoutingKey != "#" {
				t.Errorf("%s is bound with %q; a DLQ must catch everything with #", b.Destination, b.RoutingKey)
			}
			bound[b.Destination] = true
		}
	}
	for _, q := range d.Queues {
		if strings.HasSuffix(q.Name, ".dlq") && !bound[q.Name] {
			t.Errorf("%s is declared but never bound: dead letters sent there would be lost", q.Name)
		}
	}
}

// TestConsumerQueuesMatchTheServices pins the bindings each service declares on
// boot. When a service changes its subscription, this test fails until the
// broker definitions are updated too, which is the drift we are guarding against.
func TestConsumerQueuesMatchTheServices(t *testing.T) {
	want := map[string][]string{
		"resolution.recall-raw":     {"ingestion.recall.raw.received.*", "ingestion.catalog.sku.vanished.v1"},
		"containment.lot-resolved":  {"resolution.lot.resolved.v1"},
		"rescue.containment-taken":  {"containment.action.taken.v1"},
		"rescue.confirmations":      {"rescue.order.confirmed.v1"},
		"notification.outbound":     {"containment.action.taken.v1", "evasion.flagged.v1", "rescue.order.proposed.v1"},
		"extractor.recall-raw":      {"ingestion.recall.raw.received.*"},
		"evasion.containment-taken": {"containment.action.taken.v1"},
		"ops.review":                {"audit.dossier.generated.v1", "containment.action.proposed.v1"},
		"storefront.projection":     {"containment.action.taken.v1", "resolution.lot.resolved.v1"},
		// The audit ledger binds to everything on every incident-bearing
		// exchange: a dossier assembled from a subset would be evidence of nothing.
		"audit.ledger": {"#", "#", "#", "#", "#"},
	}

	got := map[string][]string{}
	for _, b := range load(t).Bindings {
		if strings.HasSuffix(b.Destination, ".dlq") {
			continue
		}
		got[b.Destination] = append(got[b.Destination], b.RoutingKey)
	}

	for queue, keys := range want {
		actual := got[queue]
		sort.Strings(actual)
		sort.Strings(keys)
		if strings.Join(actual, ",") != strings.Join(keys, ",") {
			t.Errorf("%s binds %v, services expect %v", queue, actual, keys)
		}
	}
	for queue := range got {
		if _, expected := want[queue]; !expected {
			t.Errorf("%s is bound in the broker but no service claims it", queue)
		}
	}
}

// TestAuditLedgerSeesEveryIncidentExchange. The dossier's claim is completeness;
// if a new exchange carries incident events and audit is not bound to it, that
// claim quietly becomes false.
func TestAuditLedgerSeesEveryIncidentExchange(t *testing.T) {
	required := []string{"resolution.x", "containment.x", "rescue.x", "evasion.x", "notification.x"}

	sources := map[string]bool{}
	for _, b := range load(t).Bindings {
		if b.Destination == "audit.ledger" {
			sources[b.Source] = true
		}
	}
	for _, x := range required {
		if !sources[x] {
			t.Errorf("audit.ledger is not bound to %s: those events would be missing from every dossier", x)
		}
	}
}
