package extract

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// Extractor turns notice text into a raw (unvalidated) Extraction.
type Extractor interface {
	Name() string
	Extract(ctx context.Context, text string) (Extraction, error)
}

// ---- Groq (OpenAI-compatible chat completions) --------------------------------

// Groq calls the Groq free tier. Default model openai/gpt-oss-120b (Groq retired llama-3.3-70b in 2026).
type Groq struct {
	APIKey  string
	Model   string
	BaseURL string
	HTTP    *http.Client
	// MaxRetries for 429/5xx (default 4); honours Retry-After.
	MaxRetries int
	// Fallback is tried when Model is rate-limited for longer than
	// FallbackAfter (a daily-quota 429 comes with Retry-After in the tens of
	// minutes; a per-minute one in seconds). Each Groq model has its own
	// quota, so a second model keeps notices flowing. "" disables it.
	Fallback      string
	FallbackAfter time.Duration
	// Log, when set, records rate-limit waits and fallbacks.
	Log *slog.Logger
}

// NewGroq returns a client; model "" → openai/gpt-oss-120b, fallback
// openai/gpt-oss-20b (same prompt format, separate free-tier quota).
func NewGroq(apiKey, model string) *Groq {
	if model == "" {
		model = "openai/gpt-oss-120b"
	}
	return &Groq{APIKey: apiKey, Model: model, BaseURL: "https://api.groq.com/openai/v1", HTTP: &http.Client{Timeout: 60 * time.Second}, MaxRetries: 4,
		Fallback: "openai/gpt-oss-20b", FallbackAfter: 30 * time.Second}
}

func (g *Groq) Name() string { return "groq/" + g.Model }

type chatRequest struct {
	Model          string        `json:"model"`
	Messages       []chatMessage `json:"messages"`
	Temperature    float64       `json:"temperature"`
	MaxTokens      int           `json:"max_tokens,omitempty"`
	ResponseFormat *struct {
		Type string `json:"type"`
	} `json:"response_format,omitempty"`
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
	} `json:"error"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
}

// Extract runs one JSON-mode completion and decodes the model's JSON.
func (g *Groq) Extract(ctx context.Context, text string) (Extraction, error) {
	if g.APIKey == "" {
		return Extraction{}, errors.New("groq: GROQ_API_KEY not set")
	}
	model := g.Model
	req := chatRequest{
		Model:       model,
		Temperature: 0,
		MaxTokens:   8192,
		Messages: []chatMessage{
			{Role: "system", Content: SystemPrompt},
			{Role: "user", Content: UserPrompt(text)},
		},
	}
	req.ResponseFormat = &struct {
		Type string `json:"type"`
	}{Type: "json_object"}
	body, _ := json.Marshal(req)

	var last error
	for attempt := 0; attempt <= g.MaxRetries; attempt++ {
		if attempt > 0 {
			wait := backoff(attempt, last)
			if g.Fallback != "" && model != g.Fallback && wait > g.FallbackAfter {
				// Quota, not a blip: switch models for this notice instead of
				// holding the queue for the rest of the window.
				g.logf("groq: %s rate-limited for %s; falling back to %s", model, wait.Round(time.Second), g.Fallback)
				model, req.Model = g.Fallback, g.Fallback
				body, _ = json.Marshal(req)
				wait = time.Second
			}
			if dl, ok := ctx.Deadline(); ok && time.Until(dl) < wait {
				// Sleeping into the deadline just turns a clear 429 into
				// "context deadline exceeded"; say what actually happened.
				return Extraction{}, fmt.Errorf("groq: rate-limited for %s, longer than the %s left: %w", wait.Round(time.Second), time.Until(dl).Round(time.Second), last)
			}
			if wait > 5*time.Second {
				g.logf("groq: waiting %s before retry %d (%v)", wait.Round(time.Second), attempt, last)
			}
			if err := sleep(ctx, wait); err != nil {
				return Extraction{}, fmt.Errorf("%w (giving up after %d attempts; last: %v)", err, attempt, last)
			}
		}
		hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, g.BaseURL+"/chat/completions", bytes.NewReader(body))
		if err != nil {
			return Extraction{}, err
		}
		hreq.Header.Set("Content-Type", "application/json")
		hreq.Header.Set("Authorization", "Bearer "+g.APIKey)
		resp, err := g.HTTP.Do(hreq)
		if err != nil {
			last = err
			continue
		}
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
		resp.Body.Close()
		if resp.StatusCode == 429 || resp.StatusCode >= 500 {
			last = &rateLimited{status: resp.StatusCode, retryAfter: resp.Header.Get("Retry-After"), body: string(data)}
			continue
		}
		if resp.StatusCode/100 != 2 {
			return Extraction{}, fmt.Errorf("groq: HTTP %d: %.300s", resp.StatusCode, data)
		}
		var out chatResponse
		if err := json.Unmarshal(data, &out); err != nil {
			return Extraction{}, fmt.Errorf("groq: decode: %w", err)
		}
		if out.Error != nil {
			return Extraction{}, fmt.Errorf("groq: %s: %s", out.Error.Type, out.Error.Message)
		}
		if len(out.Choices) == 0 {
			return Extraction{}, errors.New("groq: no choices")
		}
		ext, err := ParseModelJSON(out.Choices[0].Message.Content)
		if err != nil {
			return Extraction{}, err
		}
		ext.Model = "groq/" + model
		return ext, nil
	}
	return Extraction{}, fmt.Errorf("groq: giving up after %d attempts: %w", g.MaxRetries+1, last)
}

func (g *Groq) logf(format string, args ...any) {
	if g.Log != nil {
		g.Log.Warn(fmt.Sprintf(format, args...))
	}
}

type rateLimited struct {
	status     int
	retryAfter string
	body       string
}

func (r *rateLimited) Error() string { return fmt.Sprintf("groq: HTTP %d: %.200s", r.status, r.body) }

func backoff(attempt int, last error) time.Duration {
	var rl *rateLimited
	if errors.As(last, &rl) && rl.retryAfter != "" {
		if secs, err := strconv.ParseFloat(rl.retryAfter, 64); err == nil {
			return time.Duration(secs*float64(time.Second)) + 200*time.Millisecond
		}
	}
	d := time.Second
	for i := 1; i < attempt; i++ {
		d *= 2
	}
	if d > 20*time.Second {
		d = 20 * time.Second
	}
	return d
}

func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// ParseModelJSON decodes the model's answer, tolerating a ```json fence.
func ParseModelJSON(content string) (Extraction, error) {
	s := strings.TrimSpace(content)
	if strings.HasPrefix(s, "```") {
		s = strings.TrimPrefix(strings.TrimPrefix(s, "```json"), "```")
		s = strings.TrimSuffix(strings.TrimSpace(s), "```")
	}
	if i := strings.Index(s, "{"); i > 0 {
		s = s[i:]
	}
	var ext Extraction
	dec := json.NewDecoder(strings.NewReader(s))
	if err := dec.Decode(&ext); err != nil {
		return Extraction{}, fmt.Errorf("model returned invalid JSON: %w (%.200s)", err, s)
	}
	return ext, nil
}

// ---- Cached ---------------------------------------------------------------------

// Cached memoizes an Extractor's raw output in SQLite keyed by
// sha256(model + text). Eval runs, redeliveries and restarts then cost
// nothing, and CI can run against a committed cache with no API key.
type Cached struct {
	Inner Extractor
	db    *sql.DB
	// ReadOnly makes a cache miss an error instead of a model call (CI mode).
	ReadOnly bool
}

// OpenCache opens (creating) the cache database.
func OpenCache(path string, inner Extractor) (*Cached, error) {
	if path != ":memory:" {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return nil, err
		}
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS cache (key TEXT PRIMARY KEY, model TEXT NOT NULL, text TEXT NOT NULL, response TEXT NOT NULL, created_at TEXT NOT NULL)`); err != nil {
		db.Close()
		return nil, err
	}
	return &Cached{Inner: inner, db: db}, nil
}

// Close closes the cache.
func (c *Cached) Close() error { return c.db.Close() }

func (c *Cached) Name() string { return c.Inner.Name() }

// Key is the cache key for text under the inner model.
func (c *Cached) Key(text string) string {
	h := sha256.Sum256([]byte(c.Inner.Name() + "\x00" + text))
	return hex.EncodeToString(h[:])
}

func (c *Cached) Extract(ctx context.Context, text string) (Extraction, error) {
	key := c.Key(text)
	var resp string
	err := c.db.QueryRowContext(ctx, `SELECT response FROM cache WHERE key = ?`, key).Scan(&resp)
	if err == nil {
		var ext Extraction
		if jerr := json.Unmarshal([]byte(resp), &ext); jerr == nil {
			return ext, nil
		}
	}
	if c.ReadOnly {
		return Extraction{}, fmt.Errorf("extract cache miss for %s in read-only mode (set GROQ_API_KEY to record)", key[:12])
	}
	ext, err := c.Inner.Extract(ctx, text)
	if err != nil {
		return Extraction{}, err
	}
	raw, _ := json.Marshal(ext)
	_, _ = c.db.ExecContext(ctx, `INSERT OR REPLACE INTO cache (key, model, text, response, created_at) VALUES (?, ?, ?, ?, ?)`,
		key, c.Inner.Name(), text, string(raw), time.Now().UTC().Format(time.RFC3339))
	return ext, nil
}

// Put stores a response (used to seed a cache from recorded JSON files).
func (c *Cached) Put(text string, ext Extraction) error {
	raw, _ := json.Marshal(ext)
	_, err := c.db.Exec(`INSERT OR REPLACE INTO cache (key, model, text, response, created_at) VALUES (?, ?, ?, ?, ?)`,
		c.Key(text), c.Inner.Name(), text, string(raw), time.Now().UTC().Format(time.RFC3339))
	return err
}

// ---- Fake ---------------------------------------------------------------------------

// Fake returns canned extractions keyed by a substring of the text; tests use it.
type Fake struct {
	ModelName string
	Answers   map[string]Extraction // substring → extraction
	Err       error
	Calls     int
}

func (f *Fake) Name() string {
	if f.ModelName == "" {
		return "fake"
	}
	return f.ModelName
}

func (f *Fake) Extract(_ context.Context, text string) (Extraction, error) {
	f.Calls++
	if f.Err != nil {
		return Extraction{}, f.Err
	}
	for sub, ext := range f.Answers {
		if strings.Contains(text, sub) {
			ext.Model = f.Name()
			return ext, nil
		}
	}
	return Extraction{}, fmt.Errorf("fake: no canned answer for text %.60q", text)
}
