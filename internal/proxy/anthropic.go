package proxy

import (
	"net/http"
)

// NewAnthropicProxy creates a reverse proxy for the Anthropic API.
// It strips the "/anthropic" prefix from the request path and forwards to baseURL.
// Example: /anthropic/v1/messages → https://api.anthropic.com/v1/messages
func NewAnthropicProxy(baseURL string) (http.Handler, error) {
	return newReverseProxy(options{
		BaseURL:     baseURL,
		StripPrefix: "/anthropic",
	})
}
