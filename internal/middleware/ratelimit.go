package middleware

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync"

	"golang.org/x/time/rate"

	"github.com/goairix/llm-proxy/internal/config"
)

// RateLimiter enforces per-API-key rate limiting using token bucket algorithm.
type RateLimiter struct {
	cfg      config.RateLimitConfig
	limiters sync.Map // map[string]*rate.Limiter
	whiteset map[string]struct{}
}

// NewRateLimiter creates a RateLimiter from the given config.
// The whitelist is converted to a set for O(1) lookup.
func NewRateLimiter(cfg config.RateLimitConfig) *RateLimiter {
	whiteset := make(map[string]struct{}, len(cfg.Whitelist))
	for _, key := range cfg.Whitelist {
		whiteset[key] = struct{}{}
	}
	return &RateLimiter{
		cfg:      cfg,
		whiteset: whiteset,
	}
}

// getLimiter returns the rate.Limiter for the given API key,
// creating one if it doesn't exist. Uses override config if available,
// otherwise uses default.
func (rl *RateLimiter) getLimiter(apiKey string) *rate.Limiter {
	if v, ok := rl.limiters.Load(apiKey); ok {
		return v.(*rate.Limiter)
	}

	rule := rl.cfg.Default
	if override, ok := rl.cfg.Overrides[apiKey]; ok {
		rule = override
	}

	limiter := rate.NewLimiter(rate.Limit(rule.RequestsPerSecond), rule.Burst)
	actual, _ := rl.limiters.LoadOrStore(apiKey, limiter)
	return actual.(*rate.Limiter)
}

// extractAPIKey extracts the API key from request headers.
// provider is either "openai" or "anthropic".
// OpenAI: Authorization: Bearer sk-xxx
// Anthropic: x-api-key: sk-xxx OR Authorization: Bearer sk-xxx
func extractAPIKey(r *http.Request, provider string) string {
	if provider == "anthropic" {
		// Anthropic prefers x-api-key header.
		if key := r.Header.Get("x-api-key"); key != "" {
			return key
		}
	}

	// Both providers support Authorization: Bearer <key>.
	auth := r.Header.Get("Authorization")
	if key, ok := strings.CutPrefix(auth, "Bearer "); ok {
		return key
	}

	return ""
}

// Handler returns an HTTP middleware that enforces rate limits.
// provider is "openai" or "anthropic" — used for key extraction.
// If rate limiting is disabled, the next handler is called directly.
// Returns 429 JSON: {"error": "rate limit exceeded"} when limit exceeded.
func (rl *RateLimiter) Handler(provider string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !rl.cfg.Enabled {
			next.ServeHTTP(w, r)
			return
		}

		apiKey := extractAPIKey(r, provider)

		// Whitelisted keys bypass rate limiting entirely.
		if _, ok := rl.whiteset[apiKey]; ok {
			next.ServeHTTP(w, r)
			return
		}

		limiter := rl.getLimiter(apiKey)
		if !limiter.Allow() {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "rate limit exceeded"})
			return
		}

		next.ServeHTTP(w, r)
	})
}
