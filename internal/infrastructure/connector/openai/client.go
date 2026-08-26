package openai

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"

	gatewaysnapshot "github.com/goairix/llm-proxy/internal/application/gateway/snapshot"
)

const (
	maxSuccessBodyBytes = 16 << 20
	maxErrorBodyBytes   = 64 << 10
)

var errBodyTooLarge = errors.New("upstream response body too large")

func newUpstreamRequest(ctx context.Context, provider gatewaysnapshot.Provider, endpoint string, body, apiKey []byte, stream bool) (*http.Request, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if provider.BaseURL == "" || len(apiKey) == 0 {
		return nil, fmt.Errorf("provider base URL and API key are required")
	}
	if endpoint != "responses" && endpoint != "chat/completions" {
		return nil, fmt.Errorf("unsupported OpenAI endpoint %q", endpoint)
	}
	joined, err := url.JoinPath(provider.BaseURL, "v1", endpoint)
	if err != nil {
		return nil, fmt.Errorf("join provider URL: %w", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, joined, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create upstream request: %w", err)
	}
	request.Host = request.URL.Host
	request.Header.Set("Content-Type", "application/json")
	if stream {
		request.Header.Set("Accept", "text/event-stream")
	} else {
		request.Header.Set("Accept", "application/json")
	}
	request.Header.Set("Authorization", "Bearer "+string(apiKey))
	return request, nil
}

func cloneClientWithoutRedirects(source *http.Client) *http.Client {
	clone := *source
	clone.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &clone
}

func readLimited(body io.ReadCloser, limit int64) ([]byte, error) {
	if body == nil {
		return nil, fmt.Errorf("upstream response body is nil")
	}
	defer body.Close()
	if limit < 0 {
		return nil, fmt.Errorf("response body limit must be non-negative")
	}
	payload, err := io.ReadAll(io.LimitReader(body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(payload)) > limit {
		return nil, errBodyTooLarge
	}
	return payload, nil
}
