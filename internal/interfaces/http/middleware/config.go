package middleware

// RateLimitRule defines a rate limiting rule for the HTTP adapter.
type RateLimitRule struct {
	RequestsPerSecond float64
	Burst             int
}

// RateLimitConfig contains only the settings required by the rate limiter.
type RateLimitConfig struct {
	Enabled   bool
	Default   RateLimitRule
	Whitelist []string
	Overrides map[string]RateLimitRule
}
