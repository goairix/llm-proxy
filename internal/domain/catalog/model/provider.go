package model

import (
	"fmt"
	"net/url"
	"strings"

	sharederrors "github.com/goairix/llm-proxy/internal/domain/shared/errors"
	sharedmodel "github.com/goairix/llm-proxy/internal/domain/shared/model"
)

const (
	ConnectorFake             = "fake"
	ConnectorOpenAI           = "openai"
	ConnectorOpenAICompatible = "openai_compatible"
)

// Provider describes a non-sensitive model-vendor integration.
type Provider struct {
	sharedmodel.Entity
	Name          string
	ConnectorType string
	BaseURL       string
	Status        sharedmodel.Status
}

// NewProvider creates an active provider definition.
func NewProvider(name, connectorType string) (*Provider, error) {
	return newProvider(name, connectorType, "", true)
}

// NewProviderWithBaseURL creates an active provider with explicit transport configuration.
func NewProviderWithBaseURL(name, connectorType, baseURL string) (*Provider, error) {
	return newProvider(name, connectorType, baseURL, false)
}

func newProvider(name, connectorType, baseURL string, legacy bool) (*Provider, error) {
	entity, err := sharedmodel.NewEntity()
	if err != nil {
		return nil, err
	}
	normalizedBaseURL, err := normalizeBaseURL(connectorType, baseURL, legacy)
	if err != nil {
		return nil, err
	}
	provider := &Provider{
		Entity:        entity,
		Name:          strings.TrimSpace(name),
		ConnectorType: strings.TrimSpace(connectorType),
		BaseURL:       normalizedBaseURL,
		Status:        sharedmodel.StatusActive,
	}
	if err := provider.Validate(); err != nil {
		return nil, err
	}
	return provider, nil
}

// Validate checks provider invariants.
func (p Provider) Validate() error {
	if p.ID == [16]byte{} {
		return fmt.Errorf("%w: provider id is required", sharederrors.ErrInvalid)
	}
	if strings.TrimSpace(p.Name) == "" {
		return fmt.Errorf("%w: provider name is required", sharederrors.ErrInvalid)
	}
	if strings.TrimSpace(p.ConnectorType) == "" {
		return fmt.Errorf("%w: provider connector type is required", sharederrors.ErrInvalid)
	}
	switch p.ConnectorType {
	case ConnectorFake:
		if strings.TrimSpace(p.BaseURL) != "" {
			return fmt.Errorf("%w: fake provider base URL must be empty", sharederrors.ErrInvalid)
		}
	case ConnectorOpenAI, ConnectorOpenAICompatible:
		// Empty BaseURL remains valid only for legacy records until the persistence
		// migration is complete. NewProviderWithBaseURL rejects it before creation.
		if strings.TrimSpace(p.BaseURL) != "" {
			normalized, err := normalizeBaseURL(p.ConnectorType, p.BaseURL, false)
			if err != nil {
				return err
			}
			if normalized != p.BaseURL {
				return fmt.Errorf("%w: provider base URL must be normalized", sharederrors.ErrInvalid)
			}
		}
	default:
		return fmt.Errorf("%w: unsupported provider connector type %q", sharederrors.ErrInvalid, p.ConnectorType)
	}
	if err := p.Status.Validate(); err != nil {
		return fmt.Errorf("provider status: %w", err)
	}
	return nil
}

// Supports reports whether the provider connector can invoke the protocol.
func (p Provider) Supports(protocol UpstreamProtocol) bool {
	switch p.ConnectorType {
	case ConnectorFake:
		return protocol == UpstreamFake
	case ConnectorOpenAI, ConnectorOpenAICompatible:
		return protocol == UpstreamResponses || protocol == UpstreamChatCompletions
	default:
		return false
	}
}

func normalizeBaseURL(connectorType, value string, legacy bool) (string, error) {
	connectorType = strings.TrimSpace(connectorType)
	value = strings.TrimSpace(value)
	if connectorType == ConnectorFake {
		if value != "" {
			return "", fmt.Errorf("%w: fake provider base URL must be empty", sharederrors.ErrInvalid)
		}
		return "", nil
	}
	if value == "" && legacy {
		return "", nil
	}
	if value == "" {
		return "", fmt.Errorf("%w: provider base URL is required", sharederrors.ErrInvalid)
	}
	parsed, err := url.Parse(value)
	if err != nil {
		return "", fmt.Errorf("%w: invalid provider base URL: %v", sharederrors.ErrInvalid, err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" || parsed.Host == "" || parsed.Opaque != "" {
		return "", fmt.Errorf("%w: provider base URL must be an absolute HTTP URL", sharederrors.ErrInvalid)
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.RawFragment != "" {
		return "", fmt.Errorf("%w: provider base URL cannot contain user info, query, or fragment", sharederrors.ErrInvalid)
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/")
	parsed.RawPath = strings.TrimRight(parsed.RawPath, "/")
	return parsed.String(), nil
}
