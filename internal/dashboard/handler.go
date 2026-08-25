package dashboard

import (
	_ "embed"
	"encoding/json"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	appRuntime "github.com/goairix/llm-proxy/internal/application/runtime"
	"github.com/goairix/llm-proxy/internal/infrastructure/config"
)

//go:embed web/index.html
var indexHTML string

// Stats tracks request metrics atomically (reset on restart).
type Stats struct {
	// Request counts
	Total     atomic.Int64
	OpenAI    atomic.Int64
	Anthropic atomic.Int64

	// Error / rate-limit counts
	Errors      atomic.Int64 // upstream 4xx/5xx
	RateLimited atomic.Int64 // 429 from rate limiter

	// Latency accumulation — divide by Total for average
	TotalLatencyMs atomic.Int64

	// Bandwidth bytes
	ReqBytes  atomic.Int64
	RespBytes atomic.Int64

	Tokens TokenCounters
}

// TokenStats holds atomic counters for one provider group.
type TokenStats struct {
	Input      atomic.Int64
	Output     atomic.Int64
	CacheRead  atomic.Int64
	CacheWrite atomic.Int64
	Reasoning  atomic.Int64
	Missing    atomic.Int64
}

// TokenCounters groups total and per-provider token counters.
type TokenCounters struct {
	Total     TokenStats
	OpenAI    TokenStats
	Anthropic TokenStats
}

// AddTokenUsage adds one valid usage result to total and provider counters.
func (s *Stats) AddTokenUsage(provider string, usage appRuntime.TokenUsage) {
	providerStats := s.tokenStats(provider)
	if providerStats == nil {
		return
	}
	addTokenUsage(&s.Tokens.Total, usage)
	addTokenUsage(providerStats, usage)
}

// AddMissingUsage records one successful eligible request without usage data.
func (s *Stats) AddMissingUsage(provider string) {
	providerStats := s.tokenStats(provider)
	if providerStats == nil {
		return
	}
	s.Tokens.Total.Missing.Add(1)
	providerStats.Missing.Add(1)
}

func (s *Stats) tokenStats(provider string) *TokenStats {
	switch provider {
	case "openai":
		return &s.Tokens.OpenAI
	case "anthropic":
		return &s.Tokens.Anthropic
	default:
		return nil
	}
}

func addTokenUsage(stats *TokenStats, usage appRuntime.TokenUsage) {
	stats.Input.Add(usage.Input)
	stats.Output.Add(usage.Output)
	stats.CacheRead.Add(usage.CacheRead)
	stats.CacheWrite.Add(usage.CacheWrite)
	stats.Reasoning.Add(usage.Reasoning)
}

// Handler serves the dashboard HTML page.
type Handler struct {
	stats     *Stats
	startTime time.Time
	cfg       config.RateLimitConfig
	version   string
	baseURL   string
}

// NewHandler creates a new dashboard Handler.
func NewHandler(stats *Stats, cfg config.RateLimitConfig, version, baseURL string) *Handler {
	return &Handler{
		stats:     stats,
		startTime: time.Now(),
		cfg:       cfg,
		version:   version,
		baseURL:   baseURL,
	}
}

// proxyData is the JSON payload injected into the HTML page.
type proxyData struct {
	Uptime    string        `json:"uptime"`
	StartTime string        `json:"start_time"`
	Version   string        `json:"version"`
	BaseURL   string        `json:"base_url"`
	Stats     statsData     `json:"stats"`
	RateLimit rateLimitData `json:"rate_limit"`
	Tokens    tokenData     `json:"tokens"`
}

type statsData struct {
	Total        int64   `json:"total"`
	OpenAI       int64   `json:"openai"`
	Anthropic    int64   `json:"anthropic"`
	Errors       int64   `json:"errors"`
	RateLimited  int64   `json:"rate_limited"`
	SuccessRate  float64 `json:"success_rate"`   // 0–100
	AvgLatencyMs int64   `json:"avg_latency_ms"` // ms
	ReqBytes     int64   `json:"req_bytes"`
	RespBytes    int64   `json:"resp_bytes"`
}

type rateLimitData struct {
	Enabled           bool    `json:"enabled"`
	RequestsPerSecond float64 `json:"requests_per_second"`
	Burst             int     `json:"burst"`
}

type tokenData struct {
	Total     tokenDataItem `json:"total"`
	OpenAI    tokenDataItem `json:"openai"`
	Anthropic tokenDataItem `json:"anthropic"`
}

type tokenDataItem struct {
	TotalTokens          int64 `json:"total_tokens"`
	InputTokens          int64 `json:"input_tokens"`
	OutputTokens         int64 `json:"output_tokens"`
	CacheReadTokens      int64 `json:"cache_read_tokens"`
	CacheWriteTokens     int64 `json:"cache_write_tokens"`
	ReasoningTokens      int64 `json:"reasoning_tokens"`
	MissingUsageRequests int64 `json:"missing_usage_requests"`
}

// ServeHTTP serves the dashboard page.
// It injects a <script> tag with window.__PROXY_DATA__ JSON before </head>.
func (h *Handler) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	total := h.stats.Total.Load()
	errors := h.stats.Errors.Load()
	rateLimited := h.stats.RateLimited.Load()

	var successRate float64 = 100
	var avgLatencyMs int64
	if total > 0 {
		successRate = float64(total-errors-rateLimited) / float64(total) * 100
		// round to 1 decimal
		successRate = float64(int64(successRate*10+0.5)) / 10
		avgLatencyMs = h.stats.TotalLatencyMs.Load() / total
	}

	data := proxyData{
		Uptime:    time.Since(h.startTime).Round(time.Second).String(),
		StartTime: h.startTime.Format("2006-01-02 15:04:05"),
		Version:   h.version,
		BaseURL:   h.baseURL,
		Stats: statsData{
			Total:        total,
			OpenAI:       h.stats.OpenAI.Load(),
			Anthropic:    h.stats.Anthropic.Load(),
			Errors:       errors,
			RateLimited:  rateLimited,
			SuccessRate:  successRate,
			AvgLatencyMs: avgLatencyMs,
			ReqBytes:     h.stats.ReqBytes.Load(),
			RespBytes:    h.stats.RespBytes.Load(),
		},
		RateLimit: rateLimitData{
			Enabled:           h.cfg.Enabled,
			RequestsPerSecond: h.cfg.Default.RequestsPerSecond,
			Burst:             h.cfg.Default.Burst,
		},
		Tokens: tokenData{
			Total:     snapshotTokenStats(&h.stats.Tokens.Total),
			OpenAI:    snapshotTokenStats(&h.stats.Tokens.OpenAI),
			Anthropic: snapshotTokenStats(&h.stats.Tokens.Anthropic),
		},
	}

	jsonBytes, err := json.Marshal(data)
	if err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	scriptTag := "<script>\nwindow.__PROXY_DATA__ = " + string(jsonBytes) + ";\n</script>\n</head>"
	page := strings.Replace(indexHTML, "</head>", scriptTag, 1)

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(page))
}

func snapshotTokenStats(stats *TokenStats) tokenDataItem {
	input := stats.Input.Load()
	output := stats.Output.Load()
	return tokenDataItem{
		TotalTokens:          input + output,
		InputTokens:          input,
		OutputTokens:         output,
		CacheReadTokens:      stats.CacheRead.Load(),
		CacheWriteTokens:     stats.CacheWrite.Load(),
		ReasoningTokens:      stats.Reasoning.Load(),
		MissingUsageRequests: stats.Missing.Load(),
	}
}
