package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// okHandler is a simple handler that always responds 200 OK.
var okHandler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
})

// TestRateLimiterDisabled verifies that a disabled rate limiter allows all requests through.
func TestRateLimiterDisabled(t *testing.T) {
	cfg := RateLimitConfig{
		Enabled: false,
		Default: RateLimitRule{
			RequestsPerSecond: 0.001, // effectively zero — would block everything if enabled
			Burst:             0,
		},
	}
	rl := NewRateLimiter(cfg)
	handler := rl.Handler("openai", okHandler)

	for i := range 10 {
		req := httptest.NewRequest(http.MethodPost, "/openai/v1/chat/completions", nil)
		req.Header.Set("Authorization", "Bearer sk-test")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Errorf("request %d: expected 200, got %d (rate limiter should be disabled)", i+1, rec.Code)
		}
	}
}

// TestRateLimiterWhitelistBypassesBurst verifies that a whitelisted key bypasses
// rate limiting even under burst conditions.
func TestRateLimiterWhitelistBypassesBurst(t *testing.T) {
	const whitelistedKey = "sk-whitelisted"
	cfg := RateLimitConfig{
		Enabled: true,
		Default: RateLimitRule{
			RequestsPerSecond: 1,
			Burst:             1,
		},
		Whitelist: []string{whitelistedKey},
	}
	rl := NewRateLimiter(cfg)
	handler := rl.Handler("openai", okHandler)

	// Fire many requests — all must pass because the key is whitelisted.
	for i := range 20 {
		req := httptest.NewRequest(http.MethodPost, "/openai/v1/chat/completions", nil)
		req.Header.Set("Authorization", "Bearer "+whitelistedKey)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Errorf("whitelisted request %d: expected 200, got %d", i+1, rec.Code)
		}
	}
}

// TestRateLimiterOverrideKeyUsesCustomLimits verifies that a key with an override
// uses the custom rate limit rules.
func TestRateLimiterOverrideKeyUsesCustomLimits(t *testing.T) {
	const overrideKey = "sk-override"
	cfg := RateLimitConfig{
		Enabled: true,
		Default: RateLimitRule{
			RequestsPerSecond: 1000, // very permissive default
			Burst:             1000,
		},
		Overrides: map[string]RateLimitRule{
			overrideKey: {
				RequestsPerSecond: 1,
				Burst:             1, // only 1 allowed per burst
			},
		},
	}
	rl := NewRateLimiter(cfg)
	handler := rl.Handler("openai", okHandler)

	allowed := 0
	limited := 0
	// Fire many requests rapidly; burst=1 means only the first should pass.
	for range 10 {
		req := httptest.NewRequest(http.MethodPost, "/openai/v1/chat/completions", nil)
		req.Header.Set("Authorization", "Bearer "+overrideKey)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code == http.StatusOK {
			allowed++
		} else if rec.Code == http.StatusTooManyRequests {
			limited++
		}
	}

	// Exactly 1 request should pass (burst=1), the rest should be limited.
	if allowed != 1 {
		t.Errorf("override key: expected 1 allowed request, got %d", allowed)
	}
	if limited != 9 {
		t.Errorf("override key: expected 9 limited requests, got %d", limited)
	}
}

// TestRateLimiterDefaultKeyGetRateLimited verifies that a default key is rate limited
// once the burst is exceeded.
func TestRateLimiterDefaultKeyGetRateLimited(t *testing.T) {
	cfg := RateLimitConfig{
		Enabled: true,
		Default: RateLimitRule{
			RequestsPerSecond: 1,
			Burst:             1, // only 1 token in the bucket
		},
	}
	rl := NewRateLimiter(cfg)
	handler := rl.Handler("openai", okHandler)

	allowed := 0
	limited := 0
	// Fire many requests rapidly; only burst=1 should be allowed.
	for range 10 {
		req := httptest.NewRequest(http.MethodPost, "/openai/v1/chat/completions", nil)
		req.Header.Set("Authorization", "Bearer sk-default")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code == http.StatusOK {
			allowed++
		} else if rec.Code == http.StatusTooManyRequests {
			limited++
		}
	}

	// Only the first request (burst=1) should pass.
	if allowed != 1 {
		t.Errorf("default key: expected 1 allowed request, got %d allowed", allowed)
	}
	if limited != 9 {
		t.Errorf("default key: expected 9 rate limited requests, got %d", limited)
	}
}

// TestExtractAPIKeyOpenAIBearer verifies that extractAPIKey extracts from
// Authorization: Bearer for the openai provider.
func TestExtractAPIKeyOpenAIBearer(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/openai/v1/chat/completions", nil)
	req.Header.Set("Authorization", "Bearer sk-openai-key")

	got := extractAPIKey(req, "openai")
	want := "sk-openai-key"
	if got != want {
		t.Errorf("extractAPIKey (openai, Bearer): got %q, want %q", got, want)
	}
}

// TestExtractAPIKeyAnthropicXAPIKey verifies that extractAPIKey extracts from
// x-api-key header for the anthropic provider.
func TestExtractAPIKeyAnthropicXAPIKey(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/anthropic/v1/messages", nil)
	req.Header.Set("x-api-key", "sk-anthropic-key")

	got := extractAPIKey(req, "anthropic")
	want := "sk-anthropic-key"
	if got != want {
		t.Errorf("extractAPIKey (anthropic, x-api-key): got %q, want %q", got, want)
	}
}

// TestExtractAPIKeyAnthropicBearer verifies that extractAPIKey falls back to
// Authorization: Bearer for the anthropic provider when x-api-key is absent.
func TestExtractAPIKeyAnthropicBearer(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/anthropic/v1/messages", nil)
	req.Header.Set("Authorization", "Bearer sk-anthropic-bearer")

	got := extractAPIKey(req, "anthropic")
	want := "sk-anthropic-bearer"
	if got != want {
		t.Errorf("extractAPIKey (anthropic, Bearer fallback): got %q, want %q", got, want)
	}
}
