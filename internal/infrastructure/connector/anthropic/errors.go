package anthropic

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"

	gatewayport "github.com/goairix/llm-proxy/internal/application/gateway/port"
)

func connectorError(kind gatewayport.ConnectorErrorKind, cause error) *gatewayport.ConnectorError {
	return &gatewayport.ConnectorError{Kind: kind, Cause: cause}
}

func parameterUnsupported(param string, cause error) *gatewayport.ConnectorError {
	return &gatewayport.ConnectorError{Kind: gatewayport.ParameterUnsupported, Param: param, Cause: cause}
}

func invalidResponseError(cause error) *gatewayport.ConnectorError {
	return connectorError(gatewayport.UpstreamInvalidResponse, cause)
}

func classifyHTTPStatus(status int) *gatewayport.ConnectorError {
	kind := gatewayport.UpstreamUnavailable
	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		kind = gatewayport.UpstreamAuthentication
	case status == http.StatusRequestTimeout || status == http.StatusGatewayTimeout:
		kind = gatewayport.UpstreamTimeout
	case status == http.StatusTooManyRequests:
		kind = gatewayport.UpstreamRateLimited
	case status >= 400 && status < 500:
		kind = gatewayport.UpstreamRequestRejected
	}
	return connectorError(kind, fmt.Errorf("Anthropic upstream returned HTTP %d", status))
}

func classifyRequestError(err error) error {
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	var networkError net.Error
	if errors.Is(err, context.DeadlineExceeded) || errors.As(err, &networkError) && networkError.Timeout() {
		return connectorError(gatewayport.UpstreamTimeout, errors.New("Anthropic upstream request timed out"))
	}
	return connectorError(gatewayport.UpstreamUnavailable, errors.New("Anthropic upstream request failed"))
}
