package dashboard

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/goairix/llm-proxy/internal/config"
	"github.com/goairix/llm-proxy/internal/tokenusage"
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

func TestHandler_TokenStatsInjected(t *testing.T) {
	h, stats := newTestHandler("1.0.0")
	stats.AddTokenUsage("openai", tokenusage.Usage{Input: 100, Output: 20, CacheRead: 30, CacheWrite: 5, Reasoning: 8})
	stats.AddTokenUsage("anthropic", tokenusage.Usage{Input: 50, Output: 10, CacheRead: 15, CacheWrite: 4, Reasoning: 3})
	stats.AddMissingUsage("openai")
	stats.AddTokenUsage("unknown", tokenusage.Usage{Input: 999, Output: 999})
	stats.AddMissingUsage("unknown")

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	pd := extractProxyData(t, rec.Body.String())

	wantTotal := tokenDataItem{
		TotalTokens:          180,
		InputTokens:          150,
		OutputTokens:         30,
		CacheReadTokens:      45,
		CacheWriteTokens:     9,
		ReasoningTokens:      11,
		MissingUsageRequests: 1,
	}
	if pd.Tokens.Total != wantTotal {
		t.Fatalf("total tokens = %+v, want %+v", pd.Tokens.Total, wantTotal)
	}
	wantOpenAI := tokenDataItem{
		TotalTokens:          120,
		InputTokens:          100,
		OutputTokens:         20,
		CacheReadTokens:      30,
		CacheWriteTokens:     5,
		ReasoningTokens:      8,
		MissingUsageRequests: 1,
	}
	if pd.Tokens.OpenAI != wantOpenAI {
		t.Fatalf("OpenAI tokens = %+v, want %+v", pd.Tokens.OpenAI, wantOpenAI)
	}
	wantAnthropic := tokenDataItem{
		TotalTokens:      60,
		InputTokens:      50,
		OutputTokens:     10,
		CacheReadTokens:  15,
		CacheWriteTokens: 4,
		ReasoningTokens:  3,
	}
	if pd.Tokens.Anthropic != wantAnthropic {
		t.Fatalf("Anthropic tokens = %+v, want %+v", pd.Tokens.Anthropic, wantAnthropic)
	}
}

func TestHandler_TokenSection(t *testing.T) {
	h, _ := newTestHandler("1.0.0")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	body := rec.Body.String()
	for _, marker := range []string{
		"Token 用量",
		`id="token-total"`,
		`id="token-input"`,
		`id="token-output"`,
		`id="token-missing"`,
		`id="token-row-total"`,
		`id="token-row-openai"`,
		`id="token-row-anthropic"`,
		"function formatCount(value)",
	} {
		if !strings.Contains(body, marker) {
			t.Errorf("dashboard HTML missing %q", marker)
		}
	}
}

func extractProxyData(t *testing.T, body string) proxyData {
	t.Helper()
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
	var pd proxyData
	if err := json.Unmarshal([]byte(body[jsonStart:jsonStart+jsonEnd]), &pd); err != nil {
		t.Fatalf("failed to unmarshal injected JSON: %v", err)
	}
	return pd
}
