package model

import (
	"errors"
	"reflect"
	"testing"

	"github.com/google/uuid"

	sharederrors "github.com/goairix/llm-proxy/internal/domain/shared/errors"
	sharedmodel "github.com/goairix/llm-proxy/internal/domain/shared/model"
)

func TestScopeValidation(t *testing.T) {
	organizationID := uuid.Must(uuid.NewV7())
	projectID := uuid.Must(uuid.NewV7())
	tests := []struct {
		name  string
		scope Scope
		valid bool
	}{
		{name: "platform", scope: Scope{Kind: ScopePlatform}, valid: true},
		{name: "organization", scope: Scope{Kind: ScopeOrganization, OrganizationID: organizationID}, valid: true},
		{name: "project", scope: Scope{Kind: ScopeProject, ProjectID: projectID}, valid: true},
		{name: "platform with organization", scope: Scope{Kind: ScopePlatform, OrganizationID: organizationID}},
		{name: "platform with project", scope: Scope{Kind: ScopePlatform, ProjectID: projectID}},
		{name: "organization without id", scope: Scope{Kind: ScopeOrganization}},
		{name: "organization with project", scope: Scope{Kind: ScopeOrganization, OrganizationID: organizationID, ProjectID: projectID}},
		{name: "project without id", scope: Scope{Kind: ScopeProject}},
		{name: "project with organization", scope: Scope{Kind: ScopeProject, OrganizationID: organizationID, ProjectID: projectID}},
		{name: "unknown", scope: Scope{Kind: "unknown"}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := test.scope.Validate()
			if test.valid && err != nil {
				t.Fatalf("Validate() error = %v", err)
			}
			if !test.valid && !errors.Is(err, sharederrors.ErrInvalid) {
				t.Fatalf("Validate() error = %v; want ErrInvalid", err)
			}
		})
	}
}

func TestCatalogConstructorsUseUUIDv7(t *testing.T) {
	provider, err := NewProvider("Fake", ConnectorFake, "")
	if err != nil {
		t.Fatal(err)
	}
	sealed := SealedCredential{
		KeyVersion:      "v1",
		WrappedKeyNonce: []byte{1},
		WrappedDataKey:  []byte{2},
		PayloadNonce:    []byte{3},
		Ciphertext:      []byte{4},
	}
	credential, err := NewProviderCredential(provider.ID, Scope{Kind: ScopePlatform}, sealed)
	if err != nil {
		t.Fatal(err)
	}
	deployment, err := NewDeployment(provider.ID, "fake", "fake-model", UpstreamFake, Scope{Kind: ScopePlatform}, CapabilitySet{Text: true})
	if err != nil {
		t.Fatal(err)
	}
	alias, err := NewModelAlias(uuid.Must(uuid.NewV7()), "assistant")
	if err != nil {
		t.Fatal(err)
	}
	target, err := NewRouteTarget(alias.ID, uuid.Must(uuid.NewV7()), 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	revision, err := NewConfigRevision(0)
	if err != nil {
		t.Fatal(err)
	}

	for name, id := range map[string]uuid.UUID{
		"provider":   provider.ID,
		"credential": credential.ID,
		"deployment": deployment.ID,
		"alias":      alias.ID,
		"target":     target.ID,
		"revision":   revision.ID,
	} {
		if id.Version() != 7 {
			t.Fatalf("%s UUID version = %d; want 7", name, id.Version())
		}
	}
}

func TestProviderNormalizesBaseURLAndSupportsProtocols(t *testing.T) {
	provider, err := NewProvider("OpenAI", ConnectorOpenAI, "https://api.openai.com/")
	if err != nil {
		t.Fatal(err)
	}
	if provider.BaseURL != "https://api.openai.com" {
		t.Fatalf("BaseURL = %q; want normalized URL", provider.BaseURL)
	}
	if !provider.Supports(UpstreamResponses) || !provider.Supports(UpstreamChatCompletions) || provider.Supports(UpstreamFake) {
		t.Fatalf("unexpected protocol support for %+v", provider)
	}
}

func TestProviderRejectsUnsafeBaseURL(t *testing.T) {
	for _, value := range []string{
		"",
		"api.openai.com",
		"ftp://api.openai.com",
		"https://u:p@api.openai.com",
		"https://api.openai.com?q=1",
		"https://api.openai.com#x",
	} {
		t.Run(value, func(t *testing.T) {
			if _, err := NewProvider("OpenAI", ConnectorOpenAI, value); err == nil {
				t.Fatalf("BaseURL %q accepted", value)
			}
		})
	}
}

func TestFakeProviderRejectsBaseURL(t *testing.T) {
	if _, err := NewProvider("Fake", ConnectorFake, "https://example.invalid"); err == nil {
		t.Fatal("Fake provider accepted BaseURL")
	}
}

func TestDeploymentOwnsProtocolButNotProviderTransportConfiguration(t *testing.T) {
	providerID := uuid.Must(uuid.NewV7())
	deployment, err := NewDeployment(
		providerID,
		"gpt-5",
		"gpt-5",
		UpstreamResponses,
		Scope{Kind: ScopePlatform},
		CapabilitySet{Text: true, Streaming: true},
	)
	if err != nil {
		t.Fatal(err)
	}
	if deployment.ProviderID != providerID || deployment.UpstreamProtocol != UpstreamResponses {
		t.Fatalf("deployment = %+v", deployment)
	}
}

func TestDeploymentDoesNotDuplicateProviderOrCredentialConfiguration(t *testing.T) {
	typeOfDeployment := reflect.TypeOf(Deployment{})
	for _, field := range []string{"CredentialID", "ConnectorType"} {
		if _, found := typeOfDeployment.FieldByName(field); found {
			t.Fatalf("legacy field %s remains", field)
		}
	}
}

func TestNewModelAliasStartsDisabledUntilRouteIsPrepared(t *testing.T) {
	alias, err := NewModelAlias(uuid.Must(uuid.NewV7()), "assistant")
	if err != nil {
		t.Fatal(err)
	}
	if alias.Status != sharedmodel.StatusDisabled {
		t.Fatalf("new model alias status = %s; want disabled", alias.Status)
	}
}

func TestCatalogConstructorsRejectInvalidInput(t *testing.T) {
	id := uuid.Must(uuid.NewV7())
	validCapabilities := CapabilitySet{Text: true, Streaming: true}
	tests := []struct {
		name string
		new  func() error
	}{
		{name: "provider without connector", new: func() error { _, err := NewProvider("Fake", " ", ""); return err }},
		{name: "credential without sealed payload", new: func() error {
			_, err := NewProviderCredential(id, Scope{Kind: ScopePlatform}, SealedCredential{})
			return err
		}},
		{name: "deployment without capabilities", new: func() error {
			_, err := NewDeployment(id, "fake", "fake-model", UpstreamFake, Scope{Kind: ScopePlatform}, CapabilitySet{})
			return err
		}},
		{name: "deployment nil provider", new: func() error {
			_, err := NewDeployment(uuid.Nil, "fake", "fake-model", UpstreamFake, Scope{Kind: ScopePlatform}, validCapabilities)
			return err
		}},
		{name: "alias nil project", new: func() error { _, err := NewModelAlias(uuid.Nil, "assistant"); return err }},
		{name: "route target nonpositive weight", new: func() error { _, err := NewRouteTarget(id, id, 0, 0); return err }},
		{name: "negative revision", new: func() error { _, err := NewConfigRevision(-1); return err }},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := test.new(); !errors.Is(err, sharederrors.ErrInvalid) {
				t.Fatalf("error = %v; want ErrInvalid", err)
			}
		})
	}
}
