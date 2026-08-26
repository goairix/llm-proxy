package gateway

import (
	"bytes"
	"net/http"
	"strings"

	gatewayservice "github.com/goairix/llm-proxy/internal/application/gateway/service"
	inference "github.com/goairix/llm-proxy/internal/domain/inference/model"
	anthropic "github.com/goairix/llm-proxy/internal/interfaces/http/protocol/anthropic"
	httpresponse "github.com/goairix/llm-proxy/internal/interfaces/http/response"
)

func (h *anthropicHandler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		writer.Header().Set("Allow", http.MethodPost)
		writeAnthropicError(writer, request, gatewayservice.NewError(gatewayservice.InvalidRequest, "请求方法不支持", "", nil), http.StatusMethodNotAllowed)
		return
	}
	if !validJSONContentType(request) {
		writeAnthropicError(writer, request, gatewayservice.NewError(gatewayservice.InvalidRequest, "Content-Type 必须是 application/json", "", nil), http.StatusUnsupportedMediaType)
		return
	}
	virtualKey := strings.TrimSpace(request.Header.Get("x-api-key"))
	if virtualKey == "" {
		virtualKey = bearerToken(request.Header.Get("Authorization"))
	}
	sanitized := sanitizedRequest(writer, request)
	decoded, err := anthropic.Decode(sanitized.Body)
	if err != nil {
		writeAnthropicError(writer, sanitized, err, 0)
		return
	}
	if decoded.Stream {
		h.stream(writer, sanitized, virtualKey, decoded)
		return
	}
	if h == nil || h.gateway == nil {
		writeAnthropicError(writer, sanitized, unavailableGatewayError(), 0)
		return
	}
	response, err := h.gateway.Complete(sanitized.Context(), virtualKey, decoded)
	if err != nil {
		writeAnthropicError(writer, sanitized, err, 0)
		return
	}
	var body bytes.Buffer
	if err := anthropic.EncodeResponse(&body, response); err != nil {
		writeAnthropicError(writer, sanitized, gatewayservice.NewError(gatewayservice.InternalError, "内部服务错误", "", err), 0)
		return
	}
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(http.StatusOK)
	_, _ = writer.Write(body.Bytes())
}

func (h *anthropicHandler) stream(writer http.ResponseWriter, request *http.Request, virtualKey string, decoded inference.Request) {
	if h == nil || h.gateway == nil {
		writeAnthropicError(writer, request, unavailableGatewayError(), 0)
		return
	}
	stream, err := h.gateway.Stream(request.Context(), virtualKey, decoded)
	if err != nil {
		writeAnthropicError(writer, request, err, 0)
		return
	}
	defer stream.Close()
	if _, ok := prepareSSE(writer); !ok {
		writeAnthropicError(writer, request, gatewayservice.NewError(gatewayservice.InternalError, "服务端不支持流式响应", "", nil), 0)
		return
	}
	_ = anthropic.EncodeStream(request.Context(), writer.(anthropic.FlushWriter), stream)
}

func writeAnthropicError(writer http.ResponseWriter, request *http.Request, err error, statusOverride int) {
	var body bytes.Buffer
	requestID := ""
	if request != nil {
		requestID = httpresponse.RequestID(request.Context())
	}
	status, encodeErr := anthropic.EncodeError(&body, err, requestID)
	if encodeErr != nil {
		status = http.StatusInternalServerError
		body.Reset()
		body.WriteString(`{"type":"error","error":{"type":"api_error","message":"内部服务错误"}}`)
	}
	if statusOverride != 0 {
		status = statusOverride
	}
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_, _ = writer.Write(body.Bytes())
}
