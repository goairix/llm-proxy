package model

import (
	"fmt"
	"strings"

	"github.com/google/uuid"

	sharederrors "github.com/goairix/llm-proxy/internal/domain/shared/errors"
	sharedmodel "github.com/goairix/llm-proxy/internal/domain/shared/model"
)

// Deployment describes a callable upstream model and its declared capabilities.
type Deployment struct {
	sharedmodel.Entity
	ProviderID       uuid.UUID
	CredentialID     *uuid.UUID
	Name             string
	UpstreamModel    string
	ConnectorType    string
	UpstreamProtocol UpstreamProtocol
	Scope            Scope
	Capabilities     CapabilitySet
	Status           sharedmodel.Status
}

// NewDeployment creates an active deployment. Fake deployments may omit a credential.
func NewDeployment(providerID uuid.UUID, credentialID *uuid.UUID, name, upstreamModel, connectorType string, scope Scope, capabilities CapabilitySet) (*Deployment, error) {
	protocol := UpstreamProtocol("")
	if strings.TrimSpace(connectorType) == ConnectorFake {
		protocol = UpstreamFake
	}
	return newDeployment(providerID, credentialID, name, upstreamModel, connectorType, protocol, scope, capabilities)
}

// NewDeploymentWithProtocol creates an active deployment using provider-owned transport configuration.
func NewDeploymentWithProtocol(providerID uuid.UUID, name, upstreamModel string, protocol UpstreamProtocol, scope Scope, capabilities CapabilitySet) (*Deployment, error) {
	return newDeployment(providerID, nil, name, upstreamModel, "", protocol, scope, capabilities)
}

func newDeployment(providerID uuid.UUID, credentialID *uuid.UUID, name, upstreamModel, connectorType string, protocol UpstreamProtocol, scope Scope, capabilities CapabilitySet) (*Deployment, error) {
	entity, err := sharedmodel.NewEntity()
	if err != nil {
		return nil, err
	}
	deployment := &Deployment{
		Entity:           entity,
		ProviderID:       providerID,
		Name:             strings.TrimSpace(name),
		UpstreamModel:    strings.TrimSpace(upstreamModel),
		ConnectorType:    strings.TrimSpace(connectorType),
		UpstreamProtocol: protocol,
		Scope:            scope,
		Capabilities:     capabilities,
		Status:           sharedmodel.StatusActive,
	}
	if credentialID != nil {
		id := *credentialID
		deployment.CredentialID = &id
	}
	if err := deployment.Validate(); err != nil {
		return nil, err
	}
	return deployment, nil
}

// Validate checks deployment references and declared behavior.
func (d Deployment) Validate() error {
	if d.ID == uuid.Nil {
		return fmt.Errorf("%w: deployment id is required", sharederrors.ErrInvalid)
	}
	if d.ProviderID == uuid.Nil {
		return fmt.Errorf("%w: provider id is required", sharederrors.ErrInvalid)
	}
	if d.CredentialID != nil && *d.CredentialID == uuid.Nil {
		return fmt.Errorf("%w: credential id cannot be nil", sharederrors.ErrInvalid)
	}
	if strings.TrimSpace(d.Name) == "" || strings.TrimSpace(d.UpstreamModel) == "" {
		return fmt.Errorf("%w: deployment name and upstream model are required", sharederrors.ErrInvalid)
	}
	legacyBinding := strings.TrimSpace(d.ConnectorType) != "" || d.CredentialID != nil
	if legacyBinding {
		if strings.TrimSpace(d.ConnectorType) == "" {
			return fmt.Errorf("%w: deployment connector type is required", sharederrors.ErrInvalid)
		}
		if d.UpstreamProtocol != "" && !(d.ConnectorType == ConnectorFake && d.UpstreamProtocol == UpstreamFake) {
			return fmt.Errorf("%w: legacy deployment protocol conflicts with connector type", sharederrors.ErrInvalid)
		}
	} else {
		if d.CredentialID != nil || strings.TrimSpace(d.ConnectorType) != "" {
			return fmt.Errorf("%w: deployment cannot own provider transport configuration", sharederrors.ErrInvalid)
		}
		if err := d.UpstreamProtocol.Validate(); err != nil {
			return fmt.Errorf("deployment upstream protocol: %w", err)
		}
	}
	if err := d.Scope.Validate(); err != nil {
		return fmt.Errorf("deployment scope: %w", err)
	}
	if err := d.Capabilities.Validate(); err != nil {
		return fmt.Errorf("deployment capabilities: %w", err)
	}
	if err := d.Status.Validate(); err != nil {
		return fmt.Errorf("deployment status: %w", err)
	}
	return nil
}
