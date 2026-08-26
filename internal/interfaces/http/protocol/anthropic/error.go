package anthropic

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	gatewayservice "github.com/goairix/llm-proxy/internal/application/gateway/service"
)

type errorEnvelope struct {
	Type      string    `json:"type"`
	Error     errorBody `json:"error"`
	RequestID string    `json:"request_id,omitempty"`
}

type errorBody struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

func EncodeError(writer io.Writer, err error, requestID string) (int, error) {
	status, body := mapError(err)
	return status, json.NewEncoder(writer).Encode(errorEnvelope{Type: "error", Error: body, RequestID: requestID})
}

func mapError(err error) (int, errorBody) {
	code := gatewayservice.InternalError
	message := "内部服务错误"
	var gatewayError *gatewayservice.GatewayError
	if errors.As(err, &gatewayError) && gatewayError != nil {
		code = gatewayError.Code
		message = gatewayError.Error()
	}
	status := http.StatusInternalServerError
	errorType := "api_error"
	switch code {
	case gatewayservice.InvalidRequest, gatewayservice.CapabilityUnsupported:
		status, errorType = http.StatusBadRequest, "invalid_request_error"
	case gatewayservice.AuthenticationFailed:
		status, errorType = http.StatusUnauthorized, "authentication_error"
	case gatewayservice.PermissionDenied:
		status, errorType = http.StatusForbidden, "permission_error"
	case gatewayservice.ResourceNotFound:
		status, errorType = http.StatusNotFound, "not_found_error"
	case gatewayservice.Conflict:
		status, errorType = http.StatusConflict, "invalid_request_error"
	case gatewayservice.GatewayNotReady:
		status, errorType = http.StatusServiceUnavailable, "overloaded_error"
	case gatewayservice.ConnectorFailed:
		status, errorType = http.StatusBadGateway, "api_error"
	}
	return status, errorBody{Type: errorType, Message: message}
}

func writeStreamGatewayError(writer FlushWriter, err error) error {
	_, body := mapError(err)
	return writeSSEEvent(writer, "error", errorEnvelope{Type: "error", Error: body})
}

func writeStreamError(writer FlushWriter, message, errorType string) error {
	return writeSSEEvent(writer, "error", errorEnvelope{Type: "error", Error: errorBody{Type: errorType, Message: message}})
}
