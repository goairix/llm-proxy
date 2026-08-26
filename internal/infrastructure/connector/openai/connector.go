package openai

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/google/uuid"

	gatewaysnapshot "github.com/goairix/llm-proxy/internal/application/gateway/snapshot"
	catalogmodel "github.com/goairix/llm-proxy/internal/domain/catalog/model"
)

type CredentialOpener interface {
	Open(context.Context, uuid.UUID, uuid.UUID, catalogmodel.Scope, catalogmodel.SealedCredential) ([]byte, error)
}

type Options struct {
	ConnectorType     string
	ResponsesClient   *http.Client
	ChatClient        *http.Client
	CredentialOpener  CredentialOpener
	CompleteTimeout   time.Duration
	StreamIdleTimeout time.Duration
	IDGenerator       func() (uuid.UUID, error)
	Clock             func() time.Time
}

type Connector struct{ options Options }

func New(options Options) (*Connector, error) {
	if options.ConnectorType != catalogmodel.ConnectorOpenAI && options.ConnectorType != catalogmodel.ConnectorOpenAICompatible {
		return nil, fmt.Errorf("unsupported OpenAI connector type %q", options.ConnectorType)
	}
	if options.ResponsesClient == nil || options.ChatClient == nil || options.CredentialOpener == nil {
		return nil, fmt.Errorf("OpenAI connector clients and credential opener are required")
	}
	if options.CompleteTimeout <= 0 || options.StreamIdleTimeout <= 0 {
		return nil, fmt.Errorf("OpenAI connector timeouts must be positive")
	}
	if options.IDGenerator == nil {
		options.IDGenerator = uuid.NewV7
	}
	if options.Clock == nil {
		options.Clock = func() time.Time { return time.Now().UTC() }
	}
	options.ResponsesClient = cloneClientWithoutRedirects(options.ResponsesClient)
	options.ChatClient = cloneClientWithoutRedirects(options.ChatClient)
	return &Connector{options: options}, nil
}

func validateProvider(connectorType string, provider gatewaysnapshot.Provider) error {
	if provider.ID == uuid.Nil || provider.ConnectorType != connectorType || provider.BaseURL == "" {
		return fmt.Errorf("provider does not match OpenAI connector")
	}
	return nil
}
