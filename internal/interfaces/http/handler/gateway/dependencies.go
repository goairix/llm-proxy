package gateway

import (
	"context"
	"net/http"

	gatewayservice "github.com/goairix/llm-proxy/internal/application/gateway/service"
	inference "github.com/goairix/llm-proxy/internal/domain/inference/model"
	inferenceport "github.com/goairix/llm-proxy/internal/domain/inference/port"
)

const maxRequestBodyBytes = 4 << 20

type Gateway interface {
	Complete(context.Context, string, inference.Request) (inference.Response, error)
	Stream(context.Context, string, inference.Request) (inferenceport.Stream, error)
}

type openAIHandler struct{ gateway Gateway }
type responsesHandler struct{ gateway Gateway }
type anthropicHandler struct{ gateway Gateway }

func NewOpenAI(gateway Gateway) http.Handler    { return &openAIHandler{gateway: gateway} }
func NewResponses(gateway Gateway) http.Handler { return &responsesHandler{gateway: gateway} }
func NewAnthropic(gateway Gateway) http.Handler { return &anthropicHandler{gateway: gateway} }

func unavailableGatewayError() error {
	return gatewayservice.NewError(gatewayservice.GatewayNotReady, "网关尚未就绪", "", nil)
}
