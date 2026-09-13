// recall-extractor is the agentic step between ingestion and resolution: it
// reads every raw recall notice, has an LLM extract products, identifiers,
// hazard and distribution as JSON, validates that output against the notice
// text (no invented UPCs or lots, check-digit-valid GTINs, regex safety net),
// and publishes recall.extracted.v1 for the resolution-service.
//
// Environment:
//
//	GROQ_API_KEY        Groq free tier (console.groq.com). Without it the
//	                    service only serves cache hits and --replay of recordings.
//	GROQ_MODEL          default openai/gpt-oss-120b
//	GROQ_FALLBACK_MODEL default openai/gpt-oss-20b; used when GROQ_MODEL is
//	                    rate-limited for more than 30s (daily quota). "" disables.
//	RABBITMQ_URL        amqp URL; empty runs on the in-process bus (use --replay/--text)
//	CACHE_PATH          SQLite response cache (default data/extract-cache.db)
//	PORT                HTTP port (default 8086): /healthz /metrics POST /v1/extract
//	LOG_LEVEL
//
// Flags:
//
//	--replay <file>   JSON array of recall.raw.received.v1 events (e.g. the output
//	                  of `ingestion-fda --dry-run`), extracted and printed
//	--text <string>   extract an ad-hoc notice text and print the result
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/joho/godotenv"

	"soteria/libs/core/bus"
	"soteria/libs/core/events"
	feedevent "soteria/libs/feedkit/event"
	"soteria/libs/feedkit/publish"
	"soteria/services/recall-extractor/internal/extract"
)

func main() {
	if err := run(); err != nil && !errors.Is(err, context.Canceled) {
		fmt.Fprintln(os.Stderr, "fatal:", err)
		os.Exit(1)
	}
}

func run() error {
	_ = godotenv.Load()
	_ = godotenv.Load(filepath.Join("..", "..", ".env"))
	replay := flag.String("replay", "", "JSON file (array or JSON-lines) of recall.raw.received.v1 events to extract and print")
	text := flag.String("text", "", "extract this notice text and print the result")
	flag.Parse()

	log := newLogger(env("LOG_LEVEL", "info"))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	groq := extract.NewGroq(os.Getenv("GROQ_API_KEY"), os.Getenv("GROQ_MODEL"))
	groq.Log = log
	if v, ok := os.LookupEnv("GROQ_FALLBACK_MODEL"); ok {
		groq.Fallback = v // "" disables the fallback
	}
	var inner extract.Extractor = groq
	cache, err := extract.OpenCache(env("CACHE_PATH", filepath.Join("data", "extract-cache.db")), inner)
	if err != nil {
		return err
	}
	defer cache.Close()
	if os.Getenv("GROQ_API_KEY") == "" {
		cache.ReadOnly = true
		log.Warn("GROQ_API_KEY not set: serving cached extractions only; new notices will fail until a key is configured")
	}

	if *text != "" {
		ext, err := cache.Extract(ctx, *text)
		if err != nil {
			return err
		}
		ext.ExtractedAt = time.Now().UTC()
		extract.Validate(&ext, *text)
		return printJSON(ext)
	}

	var (
		consumer bus.Consumer
		out      extract.Publisher
		broker   func() bool
	)
	if url := os.Getenv("RABBITMQ_URL"); url != "" && *replay == "" {
		amqpBus, err := bus.DialAMQP(url, log)
		if err != nil {
			return err
		}
		defer amqpBus.Close()
		consumer = amqpBus
		pub, err := publish.NewAMQP(url, events.ExchangeIngestion, log)
		if err != nil {
			return err
		}
		defer pub.Close()
		out, broker = &amqpOut{pub}, pub.Connected
	} else {
		consumer = bus.NewInMem(log)
		out = &printOut{}
		if *replay == "" {
			log.Warn("RABBITMQ_URL not set: running on the in-process bus; use --replay or --text")
		}
	}

	svc := extract.New(cache, out, log)
	if *replay != "" {
		return doReplay(ctx, *replay, svc, log)
	}
	if err := consumer.Subscribe(extract.Subscription(), svc.Handle); err != nil {
		return err
	}

	srv := &http.Server{Addr: ":" + env("PORT", "8086"), Handler: handler(svc, cache, broker), ReadHeaderTimeout: 5 * time.Second}
	go func() {
		log.Info("http listening", "addr", srv.Addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("http server failed", "err", err)
		}
	}()
	err = consumer.Start(ctx)
	if err == nil {
		<-ctx.Done()
	}
	sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(sctx)
	return err
}

// doReplay feeds raw events through the handler and prints each extraction.
func doReplay(ctx context.Context, path string, svc *extract.Service, log *slog.Logger) error {
	raws, err := readRawEvents(path)
	if err != nil {
		return err
	}
	var failed int
	for _, raw := range raws {
		body, _ := json.Marshal(raw)
		env := events.Envelope{EventID: events.NewID(), EventType: "ingestion.recall.raw.received.v1", EventVersion: 1, OccurredAt: time.Now().UTC(), Producer: raw.Producer, Payload: body}
		if err := svc.Handle(ctx, env); err != nil {
			failed++
			log.Error("replay item failed", "source_id", raw.SourceID, "err", err)
		}
	}
	st := svc.Stats()
	log.Info("replay done", "received", st.Received, "extracted", st.Extracted, "failed", failed, "skipped", st.Skipped)
	if failed > 0 {
		return fmt.Errorf("%d of %d notices failed", failed, len(raws))
	}
	return nil
}

func readRawEvents(path string) ([]feedevent.Event, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(f)
	if err != nil {
		return nil, err
	}
	var arr []feedevent.Event
	if json.Unmarshal(data, &arr) == nil && len(arr) > 0 {
		return arr, nil
	}
	sc := bufio.NewScanner(strings.NewReader(string(data)))
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var ev feedevent.Event
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		arr = append(arr, ev)
	}
	return arr, nil
}

// ---- http ------------------------------------------------------------------------

func handler(svc *extract.Service, ex extract.Extractor, broker func() bool) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		st := svc.Stats()
		ok := broker == nil || broker()
		w.Header().Set("Content-Type", "application/json")
		if !ok {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"healthy": ok, "broker_connected": ok, "model": ex.Name(), "received": st.Received, "extracted": st.Extracted,
			"failed": st.Failed, "skipped": st.Skipped, "last_error": st.LastError, "last_latency_ms": st.LastLatencyMS})
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok\n")) })
	mux.HandleFunc("GET /metrics", func(w http.ResponseWriter, _ *http.Request) {
		st := svc.Stats()
		up := 1
		if broker != nil && !broker() {
			up = 0
		}
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		fmt.Fprintf(w, "# TYPE soteria_extractor_up gauge\nsoteria_extractor_up %d\n", up)
		fmt.Fprintf(w, "# TYPE soteria_extractor_received_total counter\nsoteria_extractor_received_total %d\n", st.Received)
		fmt.Fprintf(w, "# TYPE soteria_extractor_extracted_total counter\nsoteria_extractor_extracted_total %d\n", st.Extracted)
		fmt.Fprintf(w, "# TYPE soteria_extractor_failed_total counter\nsoteria_extractor_failed_total %d\n", st.Failed)
		fmt.Fprintf(w, "# TYPE soteria_extractor_last_latency_ms gauge\nsoteria_extractor_last_latency_ms %d\n", st.LastLatencyMS)
	})
	// Ad-hoc extraction for the ops console: {"text": "..."} → validated extraction.
	mux.HandleFunc("POST /v1/extract", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Text string `json:"text"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&in); err != nil || strings.TrimSpace(in.Text) == "" {
			http.Error(w, `{"error":"body must be {\"text\": \"...\"}"}`, http.StatusBadRequest)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
		defer cancel()
		ext, err := ex.Extract(ctx, in.Text)
		if err != nil {
			http.Error(w, fmt.Sprintf(`{"error":%q}`, err.Error()), http.StatusBadGateway)
			return
		}
		ext.ExtractedAt = time.Now().UTC()
		extract.Validate(&ext, in.Text)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(ext)
	})
	return mux
}

// ---- publishers ------------------------------------------------------------------

type amqpOut struct{ pub *publish.AMQP }

func (o *amqpOut) PublishExtracted(ctx context.Context, env events.Envelope) error {
	body, err := json.Marshal(env)
	if err != nil {
		return err
	}
	return o.pub.Publish(ctx, publish.Message{RoutingKey: env.EventType, MessageID: env.EventID, Type: env.EventType, AppID: env.Producer, Timestamp: env.OccurredAt, Body: body})
}

type printOut struct{}

func (printOut) PublishExtracted(_ context.Context, env events.Envelope) error {
	var p extract.Extracted
	_ = json.Unmarshal(env.Payload, &p)
	p.Text = "" // keep the printout readable
	b, _ := json.Marshal(p)
	fmt.Println(string(b))
	return nil
}

func printJSON(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func newLogger(level string) *slog.Logger {
	var lv slog.Level
	if err := lv.UnmarshalText([]byte(level)); err != nil {
		lv = slog.LevelInfo
	}
	return slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: lv})).With("service", extract.Producer)
}
