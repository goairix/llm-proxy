package service

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/google/uuid"

	"github.com/goairix/llm-proxy/internal/application/controlplane/dto"
	controlerrors "github.com/goairix/llm-proxy/internal/application/controlplane/errors"
	controlport "github.com/goairix/llm-proxy/internal/application/controlplane/port"
	catalogmodel "github.com/goairix/llm-proxy/internal/domain/catalog/model"
	sharedmodel "github.com/goairix/llm-proxy/internal/domain/shared/model"
	tenantmodel "github.com/goairix/llm-proxy/internal/domain/tenancy/model"
)

func TestCreateProjectValidatesOrganizationInsideTransaction(t *testing.T) {
	organization, _ := tenantmodel.NewOrganization("Acme")
	organizations := &organizationRepo{items: map[uuid.UUID]*tenantmodel.Organization{organization.ID: organization}}
	transactions := &testTransactions{}
	organizations.requireTransaction = transactions
	service := NewTenancyService(organizations, &projectRepo{items: map[uuid.UUID]*tenantmodel.Project{}}, &virtualKeyRepo{}, &revisionRepo{}, transactions, staticKeyGenerator{}, nil)

	result, err := service.CreateProject(context.Background(), dto.CreateProject{OrganizationID: organization.ID, Name: "Production"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Project.OrganizationID != organization.ID || result.Project.ID.Version() != 7 {
		t.Fatalf("project = %+v", result.Project)
	}
	if organizations.findOutsideTransaction {
		t.Fatal("organization was read outside transaction")
	}
}

func TestCreateVirtualKeyReturnsPlaintextOnceAndPersistsOnlyHash(t *testing.T) {
	organization, _ := tenantmodel.NewOrganization("Acme")
	project, _ := tenantmodel.NewProject(organization.ID, "Production")
	keys := &virtualKeyRepo{}
	revisions := &revisionRepo{}
	service := NewTenancyService(
		&organizationRepo{items: map[uuid.UUID]*tenantmodel.Organization{organization.ID: organization}},
		&projectRepo{items: map[uuid.UUID]*tenantmodel.Project{project.ID: project}},
		keys,
		revisions,
		&testTransactions{},
		staticKeyGenerator{},
		nil,
	)

	result, err := service.CreateVirtualKey(context.Background(), dto.CreateVirtualKey{ProjectID: project.ID, Name: "ci"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Secret != "llmp_v1_test-secret-body" {
		t.Fatalf("Secret = %q", result.Secret)
	}
	if keys.saved == nil || keys.saved.Hash != ([32]byte{1, 2, 3}) || keys.saved.Prefix != "llmp_v1_test" || keys.saved.LastFour != "body" {
		t.Fatalf("saved virtual key = %+v", keys.saved)
	}
	if result.Revision != 1 || revisions.value != 1 {
		t.Fatalf("revision = %d, stored = %d", result.Revision, revisions.value)
	}
}

func TestCreateProviderCredentialPersistsOnlySealedCredential(t *testing.T) {
	provider, _ := catalogmodel.NewProvider("OpenAI", catalogmodel.ConnectorOpenAI, "https://api.openai.com")
	credentials := &credentialRepo{}
	cipher := &recordingCipher{sealed: catalogmodel.SealedCredential{
		KeyVersion: "v1", WrappedKeyNonce: []byte{1}, WrappedDataKey: []byte{2}, PayloadNonce: []byte{3}, Ciphertext: []byte{4},
	}}
	service := NewCatalogService(
		&providerRepo{items: map[uuid.UUID]*catalogmodel.Provider{provider.ID: provider}}, credentials,
		&deploymentRepo{}, &aliasRepo{}, &targetRepo{}, &organizationRepo{}, &projectRepo{},
		&revisionRepo{}, &testTransactions{}, cipher, nil,
	)
	plaintext := []byte(`{"api_key":"provider-secret"}`)
	result, err := service.CreateProviderCredential(context.Background(), dto.CreateProviderCredential{
		ProviderID: provider.ID,
		Scope:      catalogmodel.Scope{Kind: catalogmodel.ScopePlatform},
		Credential: plaintext,
	})
	if err != nil {
		t.Fatal(err)
	}
	if credentials.saved == nil || credentials.saved.Sealed.KeyVersion != "v1" || string(credentials.saved.Sealed.Ciphertext) != string([]byte{4}) {
		t.Fatalf("saved credential = %+v", credentials.saved)
	}
	if result.ProviderID != provider.ID {
		t.Fatalf("result = %+v", result)
	}
	if string(cipher.plaintext) != string(plaintext) {
		t.Fatal("cipher did not receive credential plaintext")
	}
}

func TestCreateProviderNormalizesBaseURL(t *testing.T) {
	providers := &providerRepo{}
	service := NewCatalogService(
		providers, &credentialRepo{}, &deploymentRepo{}, &aliasRepo{}, &targetRepo{},
		&organizationRepo{}, &projectRepo{}, &revisionRepo{}, &testTransactions{}, &recordingCipher{}, nil,
	)
	result, err := service.CreateProvider(context.Background(), dto.CreateProvider{
		Name: "OpenAI", ConnectorType: catalogmodel.ConnectorOpenAI, BaseURL: "https://api.openai.com/",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Provider.BaseURL != "https://api.openai.com" || providers.items[result.Provider.ID].BaseURL != "https://api.openai.com" {
		t.Fatalf("provider=%+v", result.Provider)
	}
}

func TestCreateProjectScopedProviderCredential(t *testing.T) {
	organization, _ := tenantmodel.NewOrganization("Acme")
	project, _ := tenantmodel.NewProject(organization.ID, "Production")
	provider, _ := catalogmodel.NewProvider("OpenAI", catalogmodel.ConnectorOpenAI, "https://api.openai.com")
	credentials := &credentialRepo{}
	cipher := &recordingCipher{sealed: catalogmodel.SealedCredential{
		KeyVersion: "v1", WrappedKeyNonce: []byte{1}, WrappedDataKey: []byte{2}, PayloadNonce: []byte{3}, Ciphertext: []byte{4},
	}}
	service := NewCatalogService(
		&providerRepo{items: map[uuid.UUID]*catalogmodel.Provider{provider.ID: provider}}, credentials,
		&deploymentRepo{}, &aliasRepo{}, &targetRepo{},
		&organizationRepo{items: map[uuid.UUID]*tenantmodel.Organization{organization.ID: organization}},
		&projectRepo{items: map[uuid.UUID]*tenantmodel.Project{project.ID: project}},
		&revisionRepo{}, &testTransactions{}, cipher, nil,
	)
	result, err := service.CreateProviderCredential(context.Background(), dto.CreateProviderCredential{
		ProviderID: provider.ID, Scope: catalogmodel.Scope{Kind: catalogmodel.ScopeProject, ProjectID: project.ID},
		Credential: []byte(`{"api_key":"project-secret"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Scope.Kind != catalogmodel.ScopeProject || result.Scope.ProjectID != project.ID || cipher.calls != 1 {
		t.Fatalf("result=%+v cipher.calls=%d", result, cipher.calls)
	}
}

func TestProviderCredentialRejectsInvalidPayloadBeforeSealing(t *testing.T) {
	provider, _ := catalogmodel.NewProvider("OpenAI", catalogmodel.ConnectorOpenAI, "https://api.openai.com")
	for _, payload := range [][]byte{[]byte(`{}`), []byte(`{"api_key":""}`), []byte(`{"api_key":"x","extra":true}`), []byte(`{"api_key":"x"}{}`)} {
		t.Run(string(payload), func(t *testing.T) {
			cipher := &recordingCipher{}
			service := NewCatalogService(
				&providerRepo{items: map[uuid.UUID]*catalogmodel.Provider{provider.ID: provider}}, &credentialRepo{},
				&deploymentRepo{}, &aliasRepo{}, &targetRepo{}, &organizationRepo{}, &projectRepo{},
				&revisionRepo{}, &testTransactions{}, cipher, nil,
			)
			_, err := service.CreateProviderCredential(context.Background(), dto.CreateProviderCredential{
				ProviderID: provider.ID, Scope: catalogmodel.Scope{Kind: catalogmodel.ScopePlatform}, Credential: payload,
			})
			assertApplicationErrorCode(t, err, controlerrors.InvalidRequest)
			if cipher.calls != 0 {
				t.Fatal("invalid payload reached cipher")
			}
		})
	}
}

func TestCanDisableLastProviderCredential(t *testing.T) {
	provider, _ := catalogmodel.NewProvider("OpenAI", catalogmodel.ConnectorOpenAI, "https://api.openai.com")
	credential, _ := catalogmodel.NewProviderCredential(provider.ID, catalogmodel.Scope{Kind: catalogmodel.ScopePlatform}, catalogmodel.SealedCredential{
		KeyVersion: "v1", WrappedKeyNonce: []byte{1}, WrappedDataKey: []byte{2}, PayloadNonce: []byte{3}, Ciphertext: []byte{4},
	})
	notifier := &recordingRefreshNotifier{}
	service := NewCatalogService(
		&providerRepo{items: map[uuid.UUID]*catalogmodel.Provider{provider.ID: provider}},
		&credentialRepo{items: map[uuid.UUID]*catalogmodel.ProviderCredential{credential.ID: credential}},
		&deploymentRepo{}, &aliasRepo{}, &targetRepo{}, &organizationRepo{}, &projectRepo{},
		&revisionRepo{}, &testTransactions{}, &recordingCipher{}, notifier,
	)
	status := sharedmodel.StatusDisabled
	result, err := service.UpdateProviderCredential(context.Background(), dto.UpdateProviderCredential{ID: credential.ID, Status: &status})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != status || result.Revision == 0 || notifier.calls != 1 {
		t.Fatalf("result=%+v notify=%d", result, notifier.calls)
	}
}

func TestCreateDeploymentUsesProviderProtocolWithoutFixedCredential(t *testing.T) {
	provider, _ := catalogmodel.NewProvider("OpenAI", catalogmodel.ConnectorOpenAI, "https://api.openai.com")
	service := NewCatalogService(
		&providerRepo{items: map[uuid.UUID]*catalogmodel.Provider{provider.ID: provider}}, &credentialRepo{},
		&deploymentRepo{}, &aliasRepo{}, &targetRepo{}, &organizationRepo{}, &projectRepo{},
		&revisionRepo{}, &testTransactions{}, &recordingCipher{}, nil,
	)
	result, err := service.CreateDeployment(context.Background(), dto.CreateDeployment{
		ProviderID: provider.ID, Name: "responses", UpstreamModel: "gpt-5", UpstreamProtocol: catalogmodel.UpstreamResponses,
		Scope: catalogmodel.Scope{Kind: catalogmodel.ScopePlatform}, Capabilities: catalogmodel.CapabilitySet{Text: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Deployment.UpstreamProtocol != catalogmodel.UpstreamResponses {
		t.Fatalf("deployment=%+v", result.Deployment)
	}
}

func TestCreateAnthropicDeploymentUsesProviderProtocol(t *testing.T) {
	provider, _ := catalogmodel.NewProvider("Anthropic", catalogmodel.ConnectorAnthropic, "https://api.anthropic.com")
	service := NewCatalogService(
		&providerRepo{items: map[uuid.UUID]*catalogmodel.Provider{provider.ID: provider}}, &credentialRepo{},
		&deploymentRepo{}, &aliasRepo{}, &targetRepo{}, &organizationRepo{}, &projectRepo{},
		&revisionRepo{}, &testTransactions{}, &recordingCipher{}, nil,
	)
	result, err := service.CreateDeployment(context.Background(), dto.CreateDeployment{
		ProviderID: provider.ID, Name: "claude", UpstreamModel: "claude-sonnet",
		UpstreamProtocol: catalogmodel.UpstreamAnthropicMessages,
		Scope:            catalogmodel.Scope{Kind: catalogmodel.ScopePlatform},
		Capabilities:     catalogmodel.CapabilitySet{Text: true, Streaming: true},
	})
	if err != nil || result.Deployment.UpstreamProtocol != catalogmodel.UpstreamAnthropicMessages {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestCreateProviderCredentialRejectsFakeProvider(t *testing.T) {
	provider, _ := catalogmodel.NewProvider("Fake", catalogmodel.ConnectorFake, "")
	credentials := &credentialRepo{}
	cipher := &recordingCipher{}
	service := NewCatalogService(
		&providerRepo{items: map[uuid.UUID]*catalogmodel.Provider{provider.ID: provider}}, credentials,
		&deploymentRepo{}, &aliasRepo{}, &targetRepo{}, &organizationRepo{}, &projectRepo{},
		&revisionRepo{}, &testTransactions{}, cipher, nil,
	)
	_, err := service.CreateProviderCredential(context.Background(), dto.CreateProviderCredential{
		ProviderID: provider.ID,
		Scope:      catalogmodel.Scope{Kind: catalogmodel.ScopePlatform},
		Credential: []byte(`{"api_key":"not-used"}`),
	})
	var applicationError *controlerrors.Error
	if !errors.As(err, &applicationError) || applicationError.Code != controlerrors.InvalidRequest {
		t.Fatalf("error = %v; want InvalidRequest", err)
	}
	if credentials.saved != nil || cipher.calls != 0 {
		t.Fatal("fake provider credential reached cipher or repository")
	}
}

func TestCreateDeploymentAliasAndRouteTargetValidateLogicalRelations(t *testing.T) {
	organization, _ := tenantmodel.NewOrganization("Acme")
	project, _ := tenantmodel.NewProject(organization.ID, "Production")
	provider, _ := catalogmodel.NewProvider("Fake", catalogmodel.ConnectorFake, "")
	deployments := &deploymentRepo{items: map[uuid.UUID]*catalogmodel.Deployment{}}
	aliases := &aliasRepo{items: map[uuid.UUID]*catalogmodel.ModelAlias{}}
	targets := &targetRepo{items: map[uuid.UUID]*catalogmodel.RouteTarget{}}
	revisions := &revisionRepo{}
	service := NewCatalogService(
		&providerRepo{items: map[uuid.UUID]*catalogmodel.Provider{provider.ID: provider}}, &credentialRepo{},
		deployments, aliases, targets,
		&organizationRepo{items: map[uuid.UUID]*tenantmodel.Organization{organization.ID: organization}},
		&projectRepo{items: map[uuid.UUID]*tenantmodel.Project{project.ID: project}},
		revisions, &testTransactions{}, &recordingCipher{}, nil,
	)

	deploymentResult, err := service.CreateDeployment(context.Background(), dto.CreateDeployment{
		ProviderID: provider.ID, Name: "fake", UpstreamModel: "fake-model", UpstreamProtocol: catalogmodel.UpstreamFake,
		Scope: catalogmodel.Scope{Kind: catalogmodel.ScopePlatform}, Capabilities: catalogmodel.CapabilitySet{Text: true, Streaming: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	aliasResult, err := service.CreateModelAlias(context.Background(), dto.CreateModelAlias{ProjectID: project.ID, Name: "assistant"})
	if err != nil {
		t.Fatal(err)
	}
	if aliasResult.ModelAlias.Status != sharedmodel.StatusDisabled {
		t.Fatalf("new alias status = %s; want disabled", aliasResult.ModelAlias.Status)
	}
	targetResult, err := service.CreateRouteTarget(context.Background(), dto.CreateRouteTarget{
		ModelAliasID: aliasResult.ModelAlias.ID, DeploymentID: deploymentResult.Deployment.ID, Priority: 0, Weight: 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	if targetResult.RouteTarget.ID.Version() != 7 || revisions.value != 3 {
		t.Fatalf("target = %+v, revision = %d", targetResult.RouteTarget, revisions.value)
	}
	active := sharedmodel.StatusActive
	aliasResult, err = service.UpdateModelAlias(context.Background(), dto.UpdateModelAlias{ID: aliasResult.ModelAlias.ID, Status: &active})
	if err != nil {
		t.Fatal(err)
	}
	if aliasResult.ModelAlias.Status != sharedmodel.StatusActive || revisions.value != 4 {
		t.Fatalf("activated alias = %+v, revision = %d", aliasResult.ModelAlias, revisions.value)
	}

	_, err = service.CreateRouteTarget(context.Background(), dto.CreateRouteTarget{
		ModelAliasID: aliasResult.ModelAlias.ID, DeploymentID: deploymentResult.Deployment.ID, Priority: 1, Weight: 50,
	})
	var applicationError *controlerrors.Error
	if !errors.As(err, &applicationError) || applicationError.Code != controlerrors.Conflict {
		t.Fatalf("second target error = %v; want Conflict", err)
	}
}

func TestCreateOrganizationRollsBackWhenRevisionFails(t *testing.T) {
	organizations := &organizationRepo{items: map[uuid.UUID]*tenantmodel.Organization{}}
	revisions := &revisionRepo{nextErr: errors.New("revision unavailable")}
	transactions := &rollbackTransactions{organizations: organizations, revisions: revisions}
	service := NewTenancyService(organizations, &projectRepo{}, &virtualKeyRepo{}, revisions, transactions, staticKeyGenerator{}, nil)

	_, err := service.CreateOrganization(context.Background(), dto.CreateOrganization{Name: "Acme"})
	if err == nil {
		t.Fatal("expected create organization error")
	}
	if len(organizations.items) != 0 || revisions.value != 0 {
		t.Fatalf("transaction was not rolled back: organizations=%d revision=%d", len(organizations.items), revisions.value)
	}
}

func TestMutationNotifiesRefreshOnlyAfterCommit(t *testing.T) {
	organization, _ := tenantmodel.NewOrganization("Acme")
	transactions := &testTransactions{}
	notifier := &recordingRefreshNotifier{transactions: transactions}
	revisions := &revisionRepo{}
	service := NewTenancyService(
		&organizationRepo{items: map[uuid.UUID]*tenantmodel.Organization{organization.ID: organization}},
		&projectRepo{items: map[uuid.UUID]*tenantmodel.Project{}}, &virtualKeyRepo{}, revisions,
		transactions, staticKeyGenerator{}, notifier,
	)

	if _, err := service.CreateProject(context.Background(), dto.CreateProject{OrganizationID: organization.ID, Name: "Production"}); err != nil {
		t.Fatal(err)
	}
	if notifier.calls != 1 || notifier.calledInsideTransaction || revisions.locks != 1 {
		t.Fatalf("notifier calls = %d, called inside transaction = %v, revision locks = %d", notifier.calls, notifier.calledInsideTransaction, revisions.locks)
	}
}

func TestFailedMutationDoesNotNotifyRefresh(t *testing.T) {
	revisions := &revisionRepo{nextErr: errors.New("revision unavailable")}
	notifier := &recordingRefreshNotifier{}
	service := NewTenancyService(
		&organizationRepo{}, &projectRepo{}, &virtualKeyRepo{}, revisions,
		&testTransactions{}, staticKeyGenerator{}, notifier,
	)

	_, _ = service.CreateOrganization(context.Background(), dto.CreateOrganization{Name: "Acme"})
	if notifier.calls != 0 {
		t.Fatalf("notifier calls = %d, want 0", notifier.calls)
	}
}

func TestUpdateProjectRejectsMissingOrganization(t *testing.T) {
	project, _ := tenantmodel.NewProject(uuid.Must(uuid.NewV7()), "Production")
	projects := &projectRepo{items: map[uuid.UUID]*tenantmodel.Project{project.ID: project}}
	service := NewTenancyService(&organizationRepo{}, projects, &virtualKeyRepo{}, &revisionRepo{}, &testTransactions{}, staticKeyGenerator{}, nil)
	name := "Renamed"

	_, err := service.UpdateProject(context.Background(), dto.UpdateProject{ID: project.ID, Name: &name})
	assertApplicationErrorCode(t, err, controlerrors.NotFound)
}

func TestUpdateVirtualKeyRejectsMissingOrganization(t *testing.T) {
	project, _ := tenantmodel.NewProject(uuid.Must(uuid.NewV7()), "Production")
	key, _ := tenantmodel.NewVirtualKey(project.ID, "ci", [32]byte{1}, "llmp_v1_test", "body", nil)
	keys := &virtualKeyRepo{items: map[uuid.UUID]*tenantmodel.VirtualKey{key.ID: key}}
	service := NewTenancyService(
		&organizationRepo{},
		&projectRepo{items: map[uuid.UUID]*tenantmodel.Project{project.ID: project}},
		keys,
		&revisionRepo{},
		&testTransactions{},
		staticKeyGenerator{},
		nil,
	)
	name := "renamed"

	_, err := service.UpdateVirtualKey(context.Background(), dto.UpdateVirtualKey{ID: key.ID, Name: &name})
	assertApplicationErrorCode(t, err, controlerrors.NotFound)
}

func TestUpdateDeploymentRejectsMissingProvider(t *testing.T) {
	providerID := uuid.Must(uuid.NewV7())
	deployment, _ := catalogmodel.NewDeployment(
		providerID, "fake", "fake-model", catalogmodel.UpstreamFake,
		catalogmodel.Scope{Kind: catalogmodel.ScopePlatform},
		catalogmodel.CapabilitySet{Text: true},
	)
	service := NewCatalogService(
		&providerRepo{}, &credentialRepo{},
		&deploymentRepo{items: map[uuid.UUID]*catalogmodel.Deployment{deployment.ID: deployment}},
		&aliasRepo{}, &targetRepo{}, &organizationRepo{}, &projectRepo{},
		&revisionRepo{}, &testTransactions{}, &recordingCipher{}, nil,
	)
	name := "renamed"

	_, err := service.UpdateDeployment(context.Background(), dto.UpdateDeployment{ID: deployment.ID, Name: &name})
	assertApplicationErrorCode(t, err, controlerrors.NotFound)
}

func TestUpdateDeploymentRejectsUnsupportedProviderProtocol(t *testing.T) {
	provider, _ := catalogmodel.NewProvider("OpenAI", catalogmodel.ConnectorOpenAI, "https://api.openai.com")
	deployment, _ := catalogmodel.NewDeployment(
		provider.ID, "openai", "gpt-test", catalogmodel.UpstreamResponses,
		catalogmodel.Scope{Kind: catalogmodel.ScopePlatform},
		catalogmodel.CapabilitySet{Text: true},
	)
	service := NewCatalogService(
		&providerRepo{items: map[uuid.UUID]*catalogmodel.Provider{provider.ID: provider}},
		&credentialRepo{},
		&deploymentRepo{items: map[uuid.UUID]*catalogmodel.Deployment{deployment.ID: deployment}},
		&aliasRepo{}, &targetRepo{}, &organizationRepo{}, &projectRepo{},
		&revisionRepo{}, &testTransactions{}, &recordingCipher{}, nil,
	)
	protocol := catalogmodel.UpstreamFake

	_, err := service.UpdateDeployment(context.Background(), dto.UpdateDeployment{ID: deployment.ID, UpstreamProtocol: &protocol})
	assertApplicationErrorCode(t, err, controlerrors.InvalidRequest)
}

func TestUpdateModelAliasRejectsMissingOrganization(t *testing.T) {
	project, _ := tenantmodel.NewProject(uuid.Must(uuid.NewV7()), "Production")
	alias, _ := catalogmodel.NewModelAlias(project.ID, "assistant")
	service := NewCatalogService(
		&providerRepo{}, &credentialRepo{}, &deploymentRepo{},
		&aliasRepo{items: map[uuid.UUID]*catalogmodel.ModelAlias{alias.ID: alias}},
		&targetRepo{}, &organizationRepo{},
		&projectRepo{items: map[uuid.UUID]*tenantmodel.Project{project.ID: project}},
		&revisionRepo{}, &testTransactions{}, &recordingCipher{}, nil,
	)
	name := "renamed"

	_, err := service.UpdateModelAlias(context.Background(), dto.UpdateModelAlias{ID: alias.ID, Name: &name})
	assertApplicationErrorCode(t, err, controlerrors.NotFound)
}

func TestUpdateRouteTargetRevalidatesLogicalRelations(t *testing.T) {
	organization, _ := tenantmodel.NewOrganization("Acme")
	project, _ := tenantmodel.NewProject(organization.ID, "Production")
	alias, _ := catalogmodel.NewModelAlias(project.ID, "assistant")
	provider, _ := catalogmodel.NewProvider("Fake", catalogmodel.ConnectorFake, "")
	deployment, _ := catalogmodel.NewDeployment(
		provider.ID, "fake", "fake-model", catalogmodel.UpstreamFake,
		catalogmodel.Scope{Kind: catalogmodel.ScopeProject, ProjectID: uuid.Must(uuid.NewV7())},
		catalogmodel.CapabilitySet{Text: true},
	)
	target, _ := catalogmodel.NewRouteTarget(alias.ID, deployment.ID, 0, 100)
	service := NewCatalogService(
		&providerRepo{items: map[uuid.UUID]*catalogmodel.Provider{provider.ID: provider}},
		&credentialRepo{},
		&deploymentRepo{items: map[uuid.UUID]*catalogmodel.Deployment{deployment.ID: deployment}},
		&aliasRepo{items: map[uuid.UUID]*catalogmodel.ModelAlias{alias.ID: alias}},
		&targetRepo{items: map[uuid.UUID]*catalogmodel.RouteTarget{target.ID: target}},
		&organizationRepo{items: map[uuid.UUID]*tenantmodel.Organization{organization.ID: organization}},
		&projectRepo{items: map[uuid.UUID]*tenantmodel.Project{project.ID: project}},
		&revisionRepo{}, &testTransactions{}, &recordingCipher{}, nil,
	)
	weight := 50

	_, err := service.UpdateRouteTarget(context.Background(), dto.UpdateRouteTarget{ID: target.ID, Weight: &weight})
	assertApplicationErrorCode(t, err, controlerrors.PermissionDenied)
}

func TestCannotDisableProviderReferencedByActiveDeployment(t *testing.T) {
	provider, _ := catalogmodel.NewProvider("Fake", catalogmodel.ConnectorFake, "")
	deployment, _ := catalogmodel.NewDeployment(
		provider.ID, "fake", "fake-model", catalogmodel.UpstreamFake, catalogmodel.Scope{Kind: catalogmodel.ScopePlatform},
		catalogmodel.CapabilitySet{Text: true},
	)
	service := NewCatalogService(
		&providerRepo{items: map[uuid.UUID]*catalogmodel.Provider{provider.ID: provider}}, &credentialRepo{},
		&deploymentRepo{items: map[uuid.UUID]*catalogmodel.Deployment{deployment.ID: deployment}},
		&aliasRepo{}, &targetRepo{}, &organizationRepo{}, &projectRepo{},
		&revisionRepo{}, &testTransactions{}, &recordingCipher{}, nil,
	)
	status := sharedmodel.StatusDisabled

	_, err := service.UpdateProvider(context.Background(), dto.UpdateProvider{ID: provider.ID, Status: &status})
	assertApplicationErrorCode(t, err, controlerrors.Conflict)
}

func TestCannotDisableDeploymentReferencedByActiveTarget(t *testing.T) {
	provider, _ := catalogmodel.NewProvider("Fake", catalogmodel.ConnectorFake, "")
	deployment, _ := catalogmodel.NewDeployment(
		provider.ID, "fake", "fake-model", catalogmodel.UpstreamFake, catalogmodel.Scope{Kind: catalogmodel.ScopePlatform},
		catalogmodel.CapabilitySet{Text: true},
	)
	target, _ := catalogmodel.NewRouteTarget(uuid.Must(uuid.NewV7()), deployment.ID, 0, 100)
	service := NewCatalogService(
		&providerRepo{items: map[uuid.UUID]*catalogmodel.Provider{provider.ID: provider}}, &credentialRepo{},
		&deploymentRepo{items: map[uuid.UUID]*catalogmodel.Deployment{deployment.ID: deployment}},
		&aliasRepo{}, &targetRepo{items: map[uuid.UUID]*catalogmodel.RouteTarget{target.ID: target}},
		&organizationRepo{}, &projectRepo{}, &revisionRepo{}, &testTransactions{}, &recordingCipher{}, nil,
	)
	status := sharedmodel.StatusDisabled

	_, err := service.UpdateDeployment(context.Background(), dto.UpdateDeployment{ID: deployment.ID, Status: &status})
	assertApplicationErrorCode(t, err, controlerrors.Conflict)
}

func TestCannotDisableActiveTargetBeforeAlias(t *testing.T) {
	organization, _ := tenantmodel.NewOrganization("Acme")
	project, _ := tenantmodel.NewProject(organization.ID, "Production")
	alias, _ := catalogmodel.NewModelAlias(project.ID, "assistant")
	alias.Status = sharedmodel.StatusActive
	provider, _ := catalogmodel.NewProvider("Fake", catalogmodel.ConnectorFake, "")
	deployment, _ := catalogmodel.NewDeployment(
		provider.ID, "fake", "fake-model", catalogmodel.UpstreamFake, catalogmodel.Scope{Kind: catalogmodel.ScopePlatform},
		catalogmodel.CapabilitySet{Text: true},
	)
	target, _ := catalogmodel.NewRouteTarget(alias.ID, deployment.ID, 0, 100)
	service := NewCatalogService(
		&providerRepo{items: map[uuid.UUID]*catalogmodel.Provider{provider.ID: provider}}, &credentialRepo{},
		&deploymentRepo{items: map[uuid.UUID]*catalogmodel.Deployment{deployment.ID: deployment}},
		&aliasRepo{items: map[uuid.UUID]*catalogmodel.ModelAlias{alias.ID: alias}},
		&targetRepo{items: map[uuid.UUID]*catalogmodel.RouteTarget{target.ID: target}},
		&organizationRepo{items: map[uuid.UUID]*tenantmodel.Organization{organization.ID: organization}},
		&projectRepo{items: map[uuid.UUID]*tenantmodel.Project{project.ID: project}},
		&revisionRepo{}, &testTransactions{}, &recordingCipher{}, nil,
	)
	status := sharedmodel.StatusDisabled

	_, err := service.UpdateRouteTarget(context.Background(), dto.UpdateRouteTarget{ID: target.ID, Status: &status})
	assertApplicationErrorCode(t, err, controlerrors.Conflict)
}

func TestCannotEnableAliasWithoutOneUsableTarget(t *testing.T) {
	organization, _ := tenantmodel.NewOrganization("Acme")
	project, _ := tenantmodel.NewProject(organization.ID, "Production")
	alias, _ := catalogmodel.NewModelAlias(project.ID, "assistant")
	alias.Status = sharedmodel.StatusDisabled
	service := NewCatalogService(
		&providerRepo{}, &credentialRepo{}, &deploymentRepo{},
		&aliasRepo{items: map[uuid.UUID]*catalogmodel.ModelAlias{alias.ID: alias}}, &targetRepo{},
		&organizationRepo{items: map[uuid.UUID]*tenantmodel.Organization{organization.ID: organization}},
		&projectRepo{items: map[uuid.UUID]*tenantmodel.Project{project.ID: project}},
		&revisionRepo{}, &testTransactions{}, &recordingCipher{}, nil,
	)
	status := sharedmodel.StatusActive

	_, err := service.UpdateModelAlias(context.Background(), dto.UpdateModelAlias{ID: alias.ID, Status: &status})
	assertApplicationErrorCode(t, err, controlerrors.Conflict)
}

func TestDisabledAliasCanPrepareTargetBeforeReactivation(t *testing.T) {
	organization, _ := tenantmodel.NewOrganization("Acme")
	project, _ := tenantmodel.NewProject(organization.ID, "Production")
	alias, _ := catalogmodel.NewModelAlias(project.ID, "assistant")
	alias.Status = sharedmodel.StatusDisabled
	provider, _ := catalogmodel.NewProvider("Fake", catalogmodel.ConnectorFake, "")
	deployment, _ := catalogmodel.NewDeployment(
		provider.ID, "fake", "fake-model", catalogmodel.UpstreamFake, catalogmodel.Scope{Kind: catalogmodel.ScopePlatform},
		catalogmodel.CapabilitySet{Text: true},
	)
	target, _ := catalogmodel.NewRouteTarget(alias.ID, deployment.ID, 0, 100)
	target.Status = sharedmodel.StatusDisabled
	revisions := &revisionRepo{}
	service := NewCatalogService(
		&providerRepo{items: map[uuid.UUID]*catalogmodel.Provider{provider.ID: provider}}, &credentialRepo{},
		&deploymentRepo{items: map[uuid.UUID]*catalogmodel.Deployment{deployment.ID: deployment}},
		&aliasRepo{items: map[uuid.UUID]*catalogmodel.ModelAlias{alias.ID: alias}},
		&targetRepo{items: map[uuid.UUID]*catalogmodel.RouteTarget{target.ID: target}},
		&organizationRepo{items: map[uuid.UUID]*tenantmodel.Organization{organization.ID: organization}},
		&projectRepo{items: map[uuid.UUID]*tenantmodel.Project{project.ID: project}},
		revisions, &testTransactions{}, &recordingCipher{}, nil,
	)
	active := sharedmodel.StatusActive

	targetResult, err := service.UpdateRouteTarget(context.Background(), dto.UpdateRouteTarget{ID: target.ID, Status: &active})
	if err != nil {
		t.Fatalf("prepare target under disabled alias: %v", err)
	}
	if targetResult.RouteTarget.Status != sharedmodel.StatusActive || targetResult.Revision != 1 {
		t.Fatalf("prepared target = %+v, revision = %d", targetResult.RouteTarget, targetResult.Revision)
	}
	aliasResult, err := service.UpdateModelAlias(context.Background(), dto.UpdateModelAlias{ID: alias.ID, Status: &active})
	if err != nil {
		t.Fatalf("reactivate alias after target preparation: %v", err)
	}
	if aliasResult.ModelAlias.Status != sharedmodel.StatusActive || aliasResult.Revision != 2 {
		t.Fatalf("reactivated alias = %+v, revision = %d", aliasResult.ModelAlias, aliasResult.Revision)
	}
}

func TestDisabledAliasCanCreateTargetBeforeActivation(t *testing.T) {
	organization, _ := tenantmodel.NewOrganization("Acme")
	project, _ := tenantmodel.NewProject(organization.ID, "Production")
	alias, _ := catalogmodel.NewModelAlias(project.ID, "assistant")
	alias.Status = sharedmodel.StatusDisabled
	provider, _ := catalogmodel.NewProvider("Fake", catalogmodel.ConnectorFake, "")
	deployment, _ := catalogmodel.NewDeployment(
		provider.ID, "fake", "fake-model", catalogmodel.UpstreamFake, catalogmodel.Scope{Kind: catalogmodel.ScopePlatform},
		catalogmodel.CapabilitySet{Text: true},
	)
	service := NewCatalogService(
		&providerRepo{items: map[uuid.UUID]*catalogmodel.Provider{provider.ID: provider}}, &credentialRepo{},
		&deploymentRepo{items: map[uuid.UUID]*catalogmodel.Deployment{deployment.ID: deployment}},
		&aliasRepo{items: map[uuid.UUID]*catalogmodel.ModelAlias{alias.ID: alias}}, &targetRepo{},
		&organizationRepo{items: map[uuid.UUID]*tenantmodel.Organization{organization.ID: organization}},
		&projectRepo{items: map[uuid.UUID]*tenantmodel.Project{project.ID: project}},
		&revisionRepo{}, &testTransactions{}, &recordingCipher{}, nil,
	)

	result, err := service.CreateRouteTarget(context.Background(), dto.CreateRouteTarget{
		ModelAliasID: alias.ID, DeploymentID: deployment.ID, Priority: 0, Weight: 100,
	})
	if err != nil {
		t.Fatalf("create target under disabled alias: %v", err)
	}
	if result.RouteTarget.Status != sharedmodel.StatusActive {
		t.Fatalf("created target status = %s", result.RouteTarget.Status)
	}
}

func assertApplicationErrorCode(t *testing.T, err error, want controlerrors.Code) {
	t.Helper()
	var applicationError *controlerrors.Error
	if !errors.As(err, &applicationError) || applicationError.Code != want {
		t.Fatalf("error = %v; want code %s", err, want)
	}
}

type transactionMarker struct{}

type recordingRefreshNotifier struct {
	transactions            *testTransactions
	calls                   int
	calledInsideTransaction bool
}

func (n *recordingRefreshNotifier) NotifyRefresh() {
	n.calls++
	if n.transactions != nil && n.transactions.inside {
		n.calledInsideTransaction = true
	}
}

type testTransactions struct {
	inside bool
}

func (m *testTransactions) Transaction(ctx context.Context, fn func(context.Context) error) error {
	m.inside = true
	defer func() { m.inside = false }()
	return fn(context.WithValue(ctx, transactionMarker{}, true))
}

func (m *testTransactions) ReadOnlySnapshot(ctx context.Context, fn func(context.Context) error) error {
	return m.Transaction(ctx, fn)
}

type rollbackTransactions struct {
	organizations *organizationRepo
	revisions     *revisionRepo
}

func (m *rollbackTransactions) Transaction(ctx context.Context, fn func(context.Context) error) error {
	organizations := make(map[uuid.UUID]*tenantmodel.Organization, len(m.organizations.items))
	for id, organization := range m.organizations.items {
		copy := *organization
		organizations[id] = &copy
	}
	revision := m.revisions.value
	if err := fn(ctx); err != nil {
		m.organizations.items = organizations
		m.revisions.value = revision
		return err
	}
	return nil
}

func (m *rollbackTransactions) ReadOnlySnapshot(ctx context.Context, fn func(context.Context) error) error {
	return m.Transaction(ctx, fn)
}

type organizationRepo struct {
	items                  map[uuid.UUID]*tenantmodel.Organization
	requireTransaction     *testTransactions
	findOutsideTransaction bool
}

func (r *organizationRepo) Save(_ context.Context, value *tenantmodel.Organization) error {
	if r.items == nil {
		r.items = map[uuid.UUID]*tenantmodel.Organization{}
	}
	r.items[value.ID] = value
	return nil
}
func (r *organizationRepo) FindByID(ctx context.Context, id uuid.UUID) (*tenantmodel.Organization, error) {
	if r.requireTransaction != nil {
		if inside, _ := ctx.Value(transactionMarker{}).(bool); !inside {
			r.findOutsideTransaction = true
		}
	}
	return r.items[id], nil
}
func (r *organizationRepo) List(context.Context, int, int) ([]tenantmodel.Organization, error) {
	return nil, nil
}

type projectRepo struct {
	items map[uuid.UUID]*tenantmodel.Project
}

func (r *projectRepo) Save(_ context.Context, value *tenantmodel.Project) error {
	if r.items == nil {
		r.items = map[uuid.UUID]*tenantmodel.Project{}
	}
	r.items[value.ID] = value
	return nil
}
func (r *projectRepo) FindByID(_ context.Context, id uuid.UUID) (*tenantmodel.Project, error) {
	return r.items[id], nil
}
func (r *projectRepo) ListByOrganization(context.Context, uuid.UUID, int, int) ([]tenantmodel.Project, error) {
	return nil, nil
}

type virtualKeyRepo struct {
	items map[uuid.UUID]*tenantmodel.VirtualKey
	saved *tenantmodel.VirtualKey
}

func (r *virtualKeyRepo) Save(_ context.Context, value *tenantmodel.VirtualKey) error {
	if r.items == nil {
		r.items = map[uuid.UUID]*tenantmodel.VirtualKey{}
	}
	r.items[value.ID] = value
	r.saved = value
	return nil
}
func (r *virtualKeyRepo) FindByID(_ context.Context, id uuid.UUID) (*tenantmodel.VirtualKey, error) {
	return r.items[id], nil
}
func (r *virtualKeyRepo) ListByProject(context.Context, uuid.UUID, int, int) ([]tenantmodel.VirtualKey, error) {
	return nil, nil
}

type revisionRepo struct {
	value   int64
	nextErr error
	locks   int
}

func (r *revisionRepo) Lock(context.Context) error             { r.locks++; return nil }
func (r *revisionRepo) Current(context.Context) (int64, error) { return r.value, nil }
func (r *revisionRepo) Next(context.Context) (int64, error) {
	r.value++
	if r.nextErr != nil {
		return 0, fmt.Errorf("next revision: %w", r.nextErr)
	}
	return r.value, nil
}

type staticKeyGenerator struct{}

func (staticKeyGenerator) Generate() (controlport.GeneratedVirtualKey, error) {
	return controlport.GeneratedVirtualKey{Plaintext: "llmp_v1_test-secret-body", Hash: [32]byte{1, 2, 3}, Prefix: "llmp_v1_test", LastFour: "body"}, nil
}

type providerRepo struct {
	items map[uuid.UUID]*catalogmodel.Provider
}

func (r *providerRepo) Save(_ context.Context, value *catalogmodel.Provider) error {
	if r.items == nil {
		r.items = map[uuid.UUID]*catalogmodel.Provider{}
	}
	r.items[value.ID] = value
	return nil
}
func (r *providerRepo) FindByID(_ context.Context, id uuid.UUID) (*catalogmodel.Provider, error) {
	return r.items[id], nil
}
func (r *providerRepo) List(context.Context, int, int) ([]catalogmodel.Provider, error) {
	return nil, nil
}

type credentialRepo struct {
	items map[uuid.UUID]*catalogmodel.ProviderCredential
	saved *catalogmodel.ProviderCredential
}

func (r *credentialRepo) Save(_ context.Context, value *catalogmodel.ProviderCredential) error {
	if r.items == nil {
		r.items = map[uuid.UUID]*catalogmodel.ProviderCredential{}
	}
	r.items[value.ID] = value
	r.saved = value
	return nil
}
func (r *credentialRepo) FindByID(_ context.Context, id uuid.UUID) (*catalogmodel.ProviderCredential, error) {
	return r.items[id], nil
}
func (r *credentialRepo) List(context.Context, int, int) ([]catalogmodel.ProviderCredential, error) {
	return nil, nil
}
func (r *credentialRepo) ListByProvider(context.Context, uuid.UUID, int, int) ([]catalogmodel.ProviderCredential, error) {
	return nil, nil
}

type deploymentRepo struct {
	items map[uuid.UUID]*catalogmodel.Deployment
}

func (r *deploymentRepo) Save(_ context.Context, value *catalogmodel.Deployment) error {
	if r.items == nil {
		r.items = map[uuid.UUID]*catalogmodel.Deployment{}
	}
	r.items[value.ID] = value
	return nil
}
func (r *deploymentRepo) FindByID(_ context.Context, id uuid.UUID) (*catalogmodel.Deployment, error) {
	return r.items[id], nil
}
func (*deploymentRepo) List(context.Context, int, int) ([]catalogmodel.Deployment, error) {
	return nil, nil
}
func (*deploymentRepo) ListByProvider(context.Context, uuid.UUID, int, int) ([]catalogmodel.Deployment, error) {
	return nil, nil
}
func (r *deploymentRepo) HasActiveByProvider(_ context.Context, providerID uuid.UUID) (bool, error) {
	for _, deployment := range r.items {
		if deployment.ProviderID == providerID && deployment.Status == sharedmodel.StatusActive {
			return true, nil
		}
	}
	return false, nil
}

type aliasRepo struct {
	items map[uuid.UUID]*catalogmodel.ModelAlias
}

func (r *aliasRepo) Save(_ context.Context, value *catalogmodel.ModelAlias) error {
	if r.items == nil {
		r.items = map[uuid.UUID]*catalogmodel.ModelAlias{}
	}
	r.items[value.ID] = value
	return nil
}
func (r *aliasRepo) FindByID(_ context.Context, id uuid.UUID) (*catalogmodel.ModelAlias, error) {
	return r.items[id], nil
}
func (r *aliasRepo) FindByProjectAndName(_ context.Context, projectID uuid.UUID, name string) (*catalogmodel.ModelAlias, error) {
	for _, alias := range r.items {
		if alias.ProjectID == projectID && alias.Name == name {
			return alias, nil
		}
	}
	return nil, nil
}
func (*aliasRepo) List(context.Context, int, int) ([]catalogmodel.ModelAlias, error) { return nil, nil }
func (*aliasRepo) ListByProject(context.Context, uuid.UUID, int, int) ([]catalogmodel.ModelAlias, error) {
	return nil, nil
}

type targetRepo struct {
	items map[uuid.UUID]*catalogmodel.RouteTarget
}

func (r *targetRepo) Save(_ context.Context, value *catalogmodel.RouteTarget) error {
	if r.items == nil {
		r.items = map[uuid.UUID]*catalogmodel.RouteTarget{}
	}
	r.items[value.ID] = value
	return nil
}
func (r *targetRepo) FindByID(_ context.Context, id uuid.UUID) (*catalogmodel.RouteTarget, error) {
	return r.items[id], nil
}
func (r *targetRepo) ListByModelAlias(_ context.Context, aliasID uuid.UUID) ([]catalogmodel.RouteTarget, error) {
	var result []catalogmodel.RouteTarget
	for _, target := range r.items {
		if target.ModelAliasID == aliasID {
			result = append(result, *target)
		}
	}
	return result, nil
}
func (r *targetRepo) HasActiveByDeployment(_ context.Context, deploymentID uuid.UUID) (bool, error) {
	for _, target := range r.items {
		if target.DeploymentID == deploymentID && target.Status == sharedmodel.StatusActive {
			return true, nil
		}
	}
	return false, nil
}

type recordingCipher struct {
	sealed    catalogmodel.SealedCredential
	plaintext []byte
	calls     int
}

func (c *recordingCipher) Seal(_ context.Context, _, _ uuid.UUID, _ catalogmodel.Scope, plaintext []byte) (catalogmodel.SealedCredential, error) {
	c.calls++
	c.plaintext = append([]byte(nil), plaintext...)
	return c.sealed, nil
}
func (*recordingCipher) Open(context.Context, uuid.UUID, uuid.UUID, catalogmodel.Scope, catalogmodel.SealedCredential) ([]byte, error) {
	return nil, nil
}
