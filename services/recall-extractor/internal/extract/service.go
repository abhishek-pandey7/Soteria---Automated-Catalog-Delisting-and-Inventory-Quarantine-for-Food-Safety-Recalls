package extract

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"soteria/libs/core/bus"
	"soteria/libs/core/events"
	feedevent "soteria/libs/feedkit/event"
	"soteria/libs/feedkit/source"
)

// TypeExtracted is the produced event (contracts/events/recall.extracted.v1.json)
// on exchange ingestion.x.
const (
	TypeExtracted = "ingestion.recall.extracted.v1"
	Producer      = "recall-extractor"
)

// Extracted is the payload of recall.extracted.v1: the validated extraction
// plus a pointer back to the raw notice event.
type Extracted struct {
	Extraction
	RawEventID string `json:"raw_event_id"`
	SourceURL  string `json:"source_url,omitempty"`
	Title      string `json:"title"`
	// Text is the assembled notice text the model saw, so consumers and
	// auditors can check the extraction against exactly what was read.
	Text string `json:"text"`
}

// Publisher emits extracted events.
type Publisher interface {
	PublishExtracted(ctx context.Context, env events.Envelope) error
}

// Service is the bus handler.
type Service struct {
	Extractor Extractor
	Out       Publisher
	Log       *slog.Logger
	stats     stats
}

type stats struct {
	mu                                   sync.Mutex
	Received, Extracted, Failed, Skipped int64
	LastError                            string
	LastAt                               time.Time
	Latency                              time.Duration
}

// Stats is a snapshot for /healthz.
type Stats struct {
	Received, Extracted, Failed, Skipped int64
	LastError                            string
	LastAt                               time.Time
	LastLatencyMS                        int64
}

// New wires the service.
func New(ex Extractor, out Publisher, log *slog.Logger) *Service {
	if log == nil {
		log = slog.Default()
	}
	return &Service{Extractor: ex, Out: out, Log: log}
}

// Stats returns counters.
func (s *Service) Stats() Stats {
	s.stats.mu.Lock()
	defer s.stats.mu.Unlock()
	return Stats{s.stats.Received, s.stats.Extracted, s.stats.Failed, s.stats.Skipped, s.stats.LastError, s.stats.LastAt, s.stats.Latency.Milliseconds()}
}

// Subscription is the queue binding: every raw recall from any feed.
func Subscription() bus.Subscription {
	return bus.Subscription{Queue: "extractor.recall-raw", Exchange: events.ExchangeIngestion, BindingKeys: []string{"ingestion.recall.raw.received.*"}}
}

// Handle extracts one raw recall notice. Model/transport failures return an
// error so the bus retries; a notice with no usable text is acked and skipped.
// ExtractTimeout bounds one model call from the bus handler. Generous on
// purpose: a per-minute rate limit is waited out here rather than failed,
// which holds the message (the queue is the buffer) instead of burning one
// of its five deliveries. Must stay under the broker's consumer_timeout.
const ExtractTimeout = 5 * time.Minute

func (s *Service) Handle(ctx context.Context, env events.Envelope) error {
	s.bump(func(st *stats) { st.Received++ })
	var raw feedevent.Event
	if err := json.Unmarshal(env.Payload, &raw); err != nil || raw.SourceID == "" {
		s.Log.Warn("not a recall.raw.received event; skipped", "event_id", env.EventID, "err", err)
		s.bump(func(st *stats) { st.Skipped++ })
		return nil
	}
	doc := DocumentFor(raw)
	text := doc.Text()
	if len(strings.TrimSpace(text)) < 20 {
		s.Log.Warn("notice has no usable text; skipped", "source", raw.Source, "source_id", raw.SourceID)
		s.bump(func(st *stats) { st.Skipped++ })
		return nil
	}

	// The bus hands every handler a 30s deadline. A long enforcement report
	// through the model can legitimately take longer, and a timeout here is
	// redelivered up to five times and then dead-lettered: wasted model calls
	// and a lost notice. Extraction gets its own budget; the broker's
	// consumer_timeout (30 min) is the real ceiling.
	ectx, cancel := context.WithTimeout(context.WithoutCancel(ctx), ExtractTimeout)
	defer cancel()
	started := time.Now()
	ext, err := s.Extractor.Extract(ectx, text)
	if err != nil {
		s.bump(func(st *stats) { st.Failed++; st.LastError = err.Error(); st.LastAt = time.Now() })
		return fmt.Errorf("extract %s/%s: %w", raw.Source, raw.SourceID, err)
	}
	ext.Source, ext.SourceID = raw.Source, raw.SourceID
	ext.ExtractedAt = time.Now().UTC()
	if ext.Classification == "" {
		ext.Classification = strOr(raw.Normalized.Classification)
	}
	Validate(&ext, text)
	latency := time.Since(started)

	out := Extracted{Extraction: ext, RawEventID: env.EventID, SourceURL: strOr(raw.SourceURL), Title: raw.Normalized.Title, Text: text}
	body, err := json.Marshal(out)
	if err != nil {
		return err
	}
	pub := events.Envelope{
		EventID: events.NewID(), EventType: TypeExtracted, EventVersion: 1, OccurredAt: time.Now().UTC(),
		Producer: Producer, CorrelationID: raw.Source + ":" + raw.SourceID, CausationID: env.EventID, Payload: body,
	}
	if err := s.Out.PublishExtracted(ctx, pub); err != nil {
		s.bump(func(st *stats) { st.Failed++; st.LastError = err.Error(); st.LastAt = time.Now() })
		return fmt.Errorf("publish extracted: %w", err)
	}
	s.bump(func(st *stats) { st.Extracted++; st.LastAt = time.Now(); st.Latency = latency })
	s.Log.Info("extracted", "source", raw.Source, "source_id", raw.SourceID, "products", len(ext.Products),
		"upcs", len(ext.AllUPCs()), "lots", len(ext.AllLots()), "hazard", ext.Hazard.Type, "agent", ext.Hazard.Agent,
		"confidence", ext.Confidence, "corrections", len(ext.Corrections), "model", ext.Model, "latency_ms", latency.Milliseconds())
	return nil
}

func (s *Service) bump(f func(*stats)) {
	s.stats.mu.Lock()
	defer s.stats.mu.Unlock()
	f(&s.stats)
}

func strOr(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// DocumentFor assembles the model input from a normalized notice and its raw record.
func DocumentFor(raw feedevent.Event) Document {
	n := raw.Normalized
	d := Document{
		Source:         raw.Source,
		Firm:           strOr(n.Firm),
		Title:          n.Title,
		Product:        strOr(n.ProductDescription),
		Codes:          strOr(n.CodeInfo),
		Reason:         strOr(n.Reason),
		Distribution:   strOr(n.Distribution),
		Classification: strOr(n.Classification),
	}
	// Source-specific extras from the untouched record.
	var rec map[string]any
	_ = json.Unmarshal(raw.Raw, &rec)
	get := func(k string) string {
		if v, ok := rec[k].(string); ok {
			return strings.TrimSpace(v)
		}
		return ""
	}
	switch raw.Source {
	case source.FDAEnforcement:
		if d.Product == d.Title || strings.HasSuffix(d.Title, "...") {
			d.Title = "" // title is just a truncated product description
		}
		d.Extra = joinNonEmpty(" | ", labelled("recall initiated", get("recall_initiation_date")), labelled("status", get("status")), labelled("city/state", joinNonEmpty(", ", get("city"), get("state"))))
	case source.FDAPress:
		// With the press-release page fetched, product_description is the
		// full announcement and raw.summary carries the structured header;
		// otherwise it is the 300-character RSS blurb.
		if sum, ok := rec["summary"].(map[string]any); ok {
			gs := func(k string) string { v, _ := sum[k].(string); return strings.TrimSpace(v) }
			d.Extra = joinNonEmpty(" | ", labelled("company", gs("company")), labelled("brand", gs("brand")), labelled("product", gs("product")), labelled("reason", gs("reason")))
		}
	case source.USDAFSIS:
		d.Extra = joinNonEmpty(" | ", labelled("establishment", get("field_establishment")), labelled("recall type", get("field_recall_type")), labelled("processing", get("field_processing")))
	case source.EURASFF:
		d.Extra = joinNonEmpty(" | ", labelled("notifying country", get("notifyingCountry")), labelled("origin", get("origin")), labelled("product category", get("productCategory")), labelled("risk decision", get("riskDecision")))
	}
	return d
}

func labelled(k, v string) string {
	if v == "" {
		return ""
	}
	return k + ": " + v
}

func joinNonEmpty(sep string, parts ...string) string {
	var out []string
	for _, p := range parts {
		if strings.TrimSpace(p) != "" {
			out = append(out, strings.TrimSpace(p))
		}
	}
	return strings.Join(out, sep)
}

// ---- publishers ----------------------------------------------------------------------

// MemOut collects extracted events (tests, in-process bus).
type MemOut struct {
	mu     sync.Mutex
	Events []events.Envelope
}

func (m *MemOut) PublishExtracted(_ context.Context, env events.Envelope) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Events = append(m.Events, env)
	return nil
}
