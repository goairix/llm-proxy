package anthropic

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	gatewayport "github.com/goairix/llm-proxy/internal/application/gateway/port"
	gatewaysnapshot "github.com/goairix/llm-proxy/internal/application/gateway/snapshot"
)

const (
	apiVersion          = "2023-06-01"
	maxSuccessBodyBytes = 16 << 20
	maxErrorBodyBytes   = 64 << 10
	maxSSEEventBytes    = 2 << 20
)

var errBodyTooLarge = errors.New("upstream response body too large")

func newUpstreamRequest(
	ctx context.Context,
	provider gatewaysnapshot.Provider,
	body, apiKey []byte,
	stream bool,
) (*http.Request, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(provider.BaseURL) == "" {
		return nil, fmt.Errorf("Anthropic provider base URL is required")
	}
	if len(apiKey) == 0 {
		return nil, connectorError(gatewayport.CredentialUnavailable, fmt.Errorf("Anthropic API key is empty"))
	}
	joined, err := url.JoinPath(provider.BaseURL, "v1", "messages")
	if err != nil {
		return nil, fmt.Errorf("join provider URL: %w", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, joined, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create Anthropic request: %w", err)
	}
	request.Host = request.URL.Host
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("x-api-key", string(apiKey))
	request.Header.Set("anthropic-version", apiVersion)
	if stream {
		request.Header.Set("Accept", "text/event-stream")
	} else {
		request.Header.Set("Accept", "application/json")
	}
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
