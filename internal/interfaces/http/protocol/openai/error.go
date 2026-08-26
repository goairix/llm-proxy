package openai

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	gatewayservice "github.com/goairix/llm-proxy/internal/application/gateway/service"
)

type errorEnvelope struct {
	Error errorBody `json:"error"`
}

type errorBody struct {
	Message string  `json:"message"`
	Type    string  `json:"type"`
	Param   *string `json:"param"`
	Code    string  `json:"code"`
}

func EncodeError(writer io.Writer, err error) (int, error) {
	status, body := mapError(err)
	return status, json.NewEncoder(writer).Encode(errorEnvelope{Error: body})
}

func mapError(err error) (int, errorBody) {
	code := gatewayservice.InternalError
	message := "内部服务错误"
	param := ""
	var gatewayError *gatewayservice.GatewayError
	if errors.As(err, &gatewayError) && gatewayError != nil {
		code = gatewayError.Code
		message = gatewayError.Error()
		param = gatewayError.Param
	}
	status := http.StatusInternalServerError
	errorType := "server_error"
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
		status, errorType = http.StatusConflict, "conflict_error"
	case gatewayservice.GatewayNotReady:
		status, errorType = http.StatusServiceUnavailable, "server_error"
	case gatewayservice.ConnectorFailed:
		status, errorType = http.StatusBadGateway, "api_error"
	}
	var encodedParam *string
	if param != "" {
		encodedParam = &param
	}
	return status, errorBody{Message: message, Type: errorType, Param: encodedParam, Code: string(code)}
}

func writeStreamGatewayError(writer FlushWriter, err error) error {
	_, body := mapError(err)
	return writeSSEJSON(writer, errorEnvelope{Error: body})
}

func writeStreamError(writer FlushWriter, message, code, errorType string) error {
	return writeSSEJSON(writer, errorEnvelope{Error: errorBody{
		Message: message, Type: errorType, Code: code,
	}})
}
