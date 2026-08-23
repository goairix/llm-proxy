package proxy

import (
	"net/http"
)

// NewOpenAIProxy creates a reverse proxy for the OpenAI API.
// It strips the "/openai" prefix from the request path and forwards to baseURL.
// Example: /openai/v1/chat/completions → https://api.openai.com/v1/chat/completions
func NewOpenAIProxy(baseURL string) (http.Handler, error) {
	return newReverseProxy(options{
		BaseURL:     baseURL,
		StripPrefix: "/openai",
	})
}
