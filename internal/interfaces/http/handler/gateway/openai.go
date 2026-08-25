package gateway

import (
	"bytes"
	"net/http"

	gatewayservice "github.com/goairix/llm-proxy/internal/application/gateway/service"
	openai "github.com/goairix/llm-proxy/internal/interfaces/http/protocol/openai"
)

func (h *openAIHandler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		writer.Header().Set("Allow", http.MethodPost)
		writeOpenAIError(writer, gatewayservice.NewError(gatewayservice.InvalidRequest, "请求方法不支持", "", nil), http.StatusMethodNotAllowed)
		return
	}
	if !validJSONContentType(request) {
		writeOpenAIError(writer, gatewayservice.NewError(gatewayservice.InvalidRequest, "Content-Type 必须是 application/json", "", nil), http.StatusUnsupportedMediaType)
		return
	}
	virtualKey := bearerToken(request.Header.Get("Authorization"))
	sanitized := sanitizedRequest(writer, request)
	decoded, err := openai.DecodeWithOptions(sanitized.Body)
	if err != nil {
		writeOpenAIError(writer, err, 0)
		return
	}
	if decoded.Request.Stream {
		h.stream(writer, sanitized, virtualKey, decoded)
		return
	}
	if h == nil || h.gateway == nil {
		writeOpenAIError(writer, unavailableGatewayError(), 0)
		return
	}
	response, err := h.gateway.Complete(sanitized.Context(), virtualKey, decoded.Request)
	if err != nil {
		writeOpenAIError(writer, err, 0)
		return
	}
	var body bytes.Buffer
	if err := openai.EncodeResponse(&body, response); err != nil {
		writeOpenAIError(writer, gatewayservice.NewError(gatewayservice.InternalError, "内部服务错误", "", err), 0)
		return
	}
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(http.StatusOK)
	_, _ = writer.Write(body.Bytes())
}

func (h *openAIHandler) stream(writer http.ResponseWriter, request *http.Request, virtualKey string, decoded openai.DecodedRequest) {
	if h == nil || h.gateway == nil {
		writeOpenAIError(writer, unavailableGatewayError(), 0)
		return
	}
	stream, err := h.gateway.Stream(request.Context(), virtualKey, decoded.Request)
	if err != nil {
		writeOpenAIError(writer, err, 0)
		return
	}
	defer stream.Close()
	if _, ok := prepareSSE(writer); !ok {
		writeOpenAIError(writer, gatewayservice.NewError(gatewayservice.InternalError, "服务端不支持流式响应", "", nil), 0)
		return
	}
	_ = openai.EncodeStream(request.Context(), writer.(openai.FlushWriter), stream, decoded.IncludeUsage)
}

func writeOpenAIError(writer http.ResponseWriter, err error, statusOverride int) {
	var body bytes.Buffer
	status, encodeErr := openai.EncodeError(&body, err)
	if encodeErr != nil {
		status = http.StatusInternalServerError
		body.Reset()
		body.WriteString(`{"error":{"message":"内部服务错误","type":"server_error","param":null,"code":"internal_error"}}`)
	}
	if statusOverride != 0 {
		status = statusOverride
	}
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_, _ = writer.Write(body.Bytes())
}
