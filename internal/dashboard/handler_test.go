package dashboard

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/goairix/llm-proxy/internal/config"
)

// newTestHandler creates a Handler with a zeroed Stats and a predictable config.
func newTestHandler(version string) (*Handler, *Stats) {
	stats := &Stats{}
	cfg := config.RateLimitConfig{
		Enabled: true,
		Default: config.RateLimitRule{
			RequestsPerSecond: 10,
			Burst:             20,
		},
	}
	h := NewHandler(stats, cfg, version, "http://localhost:8080")
	return h, stats
}

func TestNewHandler_ServeHTTP(t *testing.T) {
	h, _ := newTestHandler("1.0.0")

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	resp := rec.Result()
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected status 200, got %d", resp.StatusCode)
	}

	ct := resp.Header.Get("Content-Type")
	if !strings.HasPrefix(ct, "text/html") {
		t.Errorf("expected Content-Type text/html, got %q", ct)
	}

	body := rec.Body.String()
	if !strings.Contains(body, "LLM 代理控制台") {
		t.Errorf("response body does not contain 'LLM 代理控制台'")
	}
}

func TestHandler_StatsInjected(t *testing.T) {
	h, stats := newTestHandler("2.3.4")

	stats.Total.Add(100)
	stats.OpenAI.Add(60)
	stats.Anthropic.Add(40)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	body := rec.Body.String()

	// Locate the injected JSON by finding the script tag.
	const prefix = "window.__PROXY_DATA__ = "
	idx := strings.Index(body, prefix)
	if idx == -1 {
		t.Fatal("window.__PROXY_DATA__ not found in response body")
	}

	// Extract the JSON object: from after the prefix up to the semicolon on the same segment.
	jsonStart := idx + len(prefix)
	// Find the closing semicolon after the JSON object.
	jsonEnd := strings.Index(body[jsonStart:], ";")
	if jsonEnd == -1 {
		t.Fatal("could not find closing semicolon after __PROXY_DATA__")
	}
	rawJSON := body[jsonStart : jsonStart+jsonEnd]

	var pd proxyData
	if err := json.Unmarshal([]byte(rawJSON), &pd); err != nil {
		t.Fatalf("failed to unmarshal injected JSON: %v\nraw: %s", err, rawJSON)
	}

	if pd.Version != "2.3.4" {
		t.Errorf("expected version '2.3.4', got %q", pd.Version)
	}
	if pd.Stats.Total != 100 {
		t.Errorf("expected total 100, got %d", pd.Stats.Total)
	}
	if pd.Stats.OpenAI != 60 {
		t.Errorf("expected openai 60, got %d", pd.Stats.OpenAI)
	}
	if pd.Stats.Anthropic != 40 {
		t.Errorf("expected anthropic 40, got %d", pd.Stats.Anthropic)
	}
	if pd.RateLimit.RequestsPerSecond != 10 {
		t.Errorf("expected requests_per_second 10, got %v", pd.RateLimit.RequestsPerSecond)
	}
	if pd.RateLimit.Burst != 20 {
		t.Errorf("expected burst 20, got %d", pd.RateLimit.Burst)
	}
}

func TestHandler_UptimeIncreasing(t *testing.T) {
	h, _ := newTestHandler("1.0.0")

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	body := rec.Body.String()

	const prefix = "window.__PROXY_DATA__ = "
	idx := strings.Index(body, prefix)
	if idx == -1 {
		t.Fatal("window.__PROXY_DATA__ not found in response body")
	}

	jsonStart := idx + len(prefix)
	jsonEnd := strings.Index(body[jsonStart:], ";")
	if jsonEnd == -1 {
		t.Fatal("could not find closing semicolon after __PROXY_DATA__")
	}
	rawJSON := body[jsonStart : jsonStart+jsonEnd]

	var pd proxyData
	if err := json.Unmarshal([]byte(rawJSON), &pd); err != nil {
		t.Fatalf("failed to unmarshal injected JSON: %v", err)
	}

	if pd.Uptime == "" {
		t.Error("expected non-empty uptime field")
	}
}
