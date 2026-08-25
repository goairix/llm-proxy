package port

import (
	"context"

	gatewaysnapshot "github.com/goairix/llm-proxy/internal/application/gateway/snapshot"
	inference "github.com/goairix/llm-proxy/internal/domain/inference/model"
	inferenceport "github.com/goairix/llm-proxy/internal/domain/inference/port"
)

type Invocation struct {
	Request    inference.Request
	Access     gatewaysnapshot.AccessContext
	Deployment gatewaysnapshot.Deployment
	Revision   int64
}

type Connector interface {
	Complete(context.Context, Invocation) (inference.Response, error)
	Stream(context.Context, Invocation) (inferenceport.Stream, error)
}

type ConnectorRegistry interface {
	Find(connectorType string) (Connector, bool)
}
