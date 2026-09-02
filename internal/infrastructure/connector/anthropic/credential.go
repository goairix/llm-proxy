package anthropic

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/google/uuid"

	gatewayport "github.com/goairix/llm-proxy/internal/application/gateway/port"
	gatewaysnapshot "github.com/goairix/llm-proxy/internal/application/gateway/snapshot"
	catalogmodel "github.com/goairix/llm-proxy/internal/domain/catalog/model"
)

type CredentialOpener interface {
	Open(context.Context, uuid.UUID, uuid.UUID, catalogmodel.Scope, catalogmodel.SealedCredential) ([]byte, error)
}

func openAPIKey(ctx context.Context, opener CredentialOpener, envelope gatewaysnapshot.CredentialEnvelope) ([]byte, error) {
	if opener == nil {
		return nil, connectorError(gatewayport.CredentialUnavailable, fmt.Errorf("credential opener is nil"))
	}
	plaintext, err := opener.Open(ctx, envelope.CredentialID, envelope.ProviderID, envelope.Scope, envelope.Sealed)
	if err != nil {
		clear(plaintext)
		return nil, connectorError(gatewayport.CredentialUnavailable, err)
	}
	defer clear(plaintext)

	var value struct {
		APIKey string `json:"api_key"`
	}
	decoder := json.NewDecoder(bytes.NewReader(plaintext))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&value); err != nil {
		return nil, connectorError(gatewayport.CredentialUnavailable, err)
	}
	if strings.TrimSpace(value.APIKey) == "" {
		return nil, connectorError(gatewayport.CredentialUnavailable, fmt.Errorf("credential API key is empty"))
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return nil, connectorError(gatewayport.CredentialUnavailable, fmt.Errorf("credential must contain one JSON value"))
	}
	key := append([]byte(nil), value.APIKey...)
	value.APIKey = ""
	return key, nil
}
