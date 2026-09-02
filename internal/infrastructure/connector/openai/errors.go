package openai

import (
	"context"
	"errors"
	"fmt"
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

func classifyHTTPStatus(status int) error {
	cause := fmt.Errorf("upstream returned HTTP status %d", status)
	switch status {
	case http.StatusUnauthorized, http.StatusForbidden:
		return connectorError(gatewayport.UpstreamAuthentication, cause)
	case http.StatusTooManyRequests:
		return connectorError(gatewayport.UpstreamRateLimited, cause)
	case http.StatusRequestTimeout, http.StatusGatewayTimeout:
		return connectorError(gatewayport.UpstreamTimeout, cause)
	case http.StatusBadRequest, http.StatusNotFound, http.StatusConflict, http.StatusUnprocessableEntity:
		return connectorError(gatewayport.UpstreamRequestRejected, cause)
	default:
		if status >= 400 && status < 500 {
			return connectorError(gatewayport.UpstreamRequestRejected, cause)
		}
		return connectorError(gatewayport.UpstreamUnavailable, cause)
	}
}

func classifyRequestError(err error) error {
	if err == nil {
		return nil
	}
	var connectorErr *gatewayport.ConnectorError
	if errors.As(err, &connectorErr) {
		return err
	}
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return connectorError(gatewayport.UpstreamTimeout, err)
	}
	return connectorError(gatewayport.UpstreamUnavailable, err)
}

func classifyStreamReadError(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	var connectorErr *gatewayport.ConnectorError
	if errors.As(err, &connectorErr) {
		return err
	}
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return connectorError(gatewayport.UpstreamTimeout, err)
	}
	return invalidResponseError(err)
}
