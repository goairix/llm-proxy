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
	tenantmodel "github.com/goairix/llm-proxy/internal/domain/tenancy/model"
)

func TestCreateProjectValidatesOrganizationInsideTransaction(t *testing.T) {
	organization, _ := tenantmodel.NewOrganization("Acme")
	organizations := &organizationRepo{items: map[uuid.UUID]*tenantmodel.Organization{organization.ID: organization}}
	transactions := &testTransactions{}
	organizations.requireTransaction = transactions
	service := NewTenancyService(organizations, &projectRepo{items: map[uuid.UUID]*tenantmodel.Project{}}, &virtualKeyRepo{}, &revisionRepo{}, transactions, staticKeyGenerator{})

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
	provider, _ := catalogmodel.NewProvider("Fake", "fake")
	credentials := &credentialRepo{}
	cipher := &recordingCipher{sealed: catalogmodel.SealedCredential{
		KeyVersion: "v1", WrappedKeyNonce: []byte{1}, WrappedDataKey: []byte{2}, PayloadNonce: []byte{3}, Ciphertext: []byte{4},
	}}
	service := NewCatalogService(
		&providerRepo{items: map[uuid.UUID]*catalogmodel.Provider{provider.ID: provider}}, credentials,
		&deploymentRepo{}, &aliasRepo{}, &targetRepo{}, &organizationRepo{}, &projectRepo{},
		&revisionRepo{}, &testTransactions{}, cipher,
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
	if result.KeyVersion != "v1" || result.ProviderID != provider.ID {
		t.Fatalf("result = %+v", result)
	}
	if string(cipher.plaintext) != string(plaintext) {
		t.Fatal("cipher did not receive credential plaintext")
	}
}

func TestCreateProviderCredentialRejectsNonPlatformScope(t *testing.T) {
	provider, _ := catalogmodel.NewProvider("Fake", "fake")
	credentials := &credentialRepo{}
	cipher := &recordingCipher{}
	service := NewCatalogService(
		&providerRepo{items: map[uuid.UUID]*catalogmodel.Provider{provider.ID: provider}}, credentials,
		&deploymentRepo{}, &aliasRepo{}, &targetRepo{}, &organizationRepo{}, &projectRepo{},
		&revisionRepo{}, &testTransactions{}, cipher,
	)
	_, err := service.CreateProviderCredential(context.Background(), dto.CreateProviderCredential{
		ProviderID: provider.ID,
		Scope:      catalogmodel.Scope{Kind: catalogmodel.ScopeProject, ProjectID: uuid.Must(uuid.NewV7())},
		Credential: []byte(`{}`),
	})
	var applicationError *controlerrors.Error
	if !errors.As(err, &applicationError) || applicationError.Code != controlerrors.InvalidRequest {
		t.Fatalf("error = %v; want InvalidRequest", err)
	}
	if credentials.saved != nil || cipher.calls != 0 {
		t.Fatal("non-platform credential reached cipher or repository")
	}
}

func TestCreateDeploymentAliasAndRouteTargetValidateLogicalRelations(t *testing.T) {
	organization, _ := tenantmodel.NewOrganization("Acme")
	project, _ := tenantmodel.NewProject(organization.ID, "Production")
	provider, _ := catalogmodel.NewProvider("Fake", "fake")
	deployments := &deploymentRepo{items: map[uuid.UUID]*catalogmodel.Deployment{}}
	aliases := &aliasRepo{items: map[uuid.UUID]*catalogmodel.ModelAlias{}}
	targets := &targetRepo{items: map[uuid.UUID]*catalogmodel.RouteTarget{}}
	revisions := &revisionRepo{}
	service := NewCatalogService(
		&providerRepo{items: map[uuid.UUID]*catalogmodel.Provider{provider.ID: provider}}, &credentialRepo{},
		deployments, aliases, targets,
		&organizationRepo{items: map[uuid.UUID]*tenantmodel.Organization{organization.ID: organization}},
		&projectRepo{items: map[uuid.UUID]*tenantmodel.Project{project.ID: project}},
		revisions, &testTransactions{}, &recordingCipher{},
	)

	deploymentResult, err := service.CreateDeployment(context.Background(), dto.CreateDeployment{
		ProviderID: provider.ID, Name: "fake", UpstreamModel: "fake-model", ConnectorType: "fake",
		Scope: catalogmodel.Scope{Kind: catalogmodel.ScopePlatform}, Capabilities: catalogmodel.CapabilitySet{Text: true, Streaming: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	aliasResult, err := service.CreateModelAlias(context.Background(), dto.CreateModelAlias{ProjectID: project.ID, Name: "assistant"})
	if err != nil {
		t.Fatal(err)
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
	service := NewTenancyService(organizations, &projectRepo{}, &virtualKeyRepo{}, revisions, transactions, staticKeyGenerator{})

	_, err := service.CreateOrganization(context.Background(), dto.CreateOrganization{Name: "Acme"})
	if err == nil {
		t.Fatal("expected create organization error")
	}
	if len(organizations.items) != 0 || revisions.value != 0 {
		t.Fatalf("transaction was not rolled back: organizations=%d revision=%d", len(organizations.items), revisions.value)
	}
}

func TestUpdateProjectRejectsMissingOrganization(t *testing.T) {
	project, _ := tenantmodel.NewProject(uuid.Must(uuid.NewV7()), "Production")
	projects := &projectRepo{items: map[uuid.UUID]*tenantmodel.Project{project.ID: project}}
	service := NewTenancyService(&organizationRepo{}, projects, &virtualKeyRepo{}, &revisionRepo{}, &testTransactions{}, staticKeyGenerator{})
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
	)
	name := "renamed"

	_, err := service.UpdateVirtualKey(context.Background(), dto.UpdateVirtualKey{ID: key.ID, Name: &name})
	assertApplicationErrorCode(t, err, controlerrors.NotFound)
}

func TestUpdateDeploymentRejectsMissingProvider(t *testing.T) {
	providerID := uuid.Must(uuid.NewV7())
	deployment, _ := catalogmodel.NewDeployment(
		providerID, nil, "fake", "fake-model", "fake",
		catalogmodel.Scope{Kind: catalogmodel.ScopePlatform},
		catalogmodel.CapabilitySet{Text: true},
	)
	service := NewCatalogService(
		&providerRepo{}, &credentialRepo{},
		&deploymentRepo{items: map[uuid.UUID]*catalogmodel.Deployment{deployment.ID: deployment}},
		&aliasRepo{}, &targetRepo{}, &organizationRepo{}, &projectRepo{},
		&revisionRepo{}, &testTransactions{}, &recordingCipher{},
	)
	name := "renamed"

	_, err := service.UpdateDeployment(context.Background(), dto.UpdateDeployment{ID: deployment.ID, Name: &name})
	assertApplicationErrorCode(t, err, controlerrors.NotFound)
}

func TestUpdateDeploymentRejectsMissingCredential(t *testing.T) {
	provider, _ := catalogmodel.NewProvider("OpenAI", "openai")
	credentialID := uuid.Must(uuid.NewV7())
	deployment, _ := catalogmodel.NewDeployment(
		provider.ID, &credentialID, "openai", "gpt-test", "openai",
		catalogmodel.Scope{Kind: catalogmodel.ScopePlatform},
		catalogmodel.CapabilitySet{Text: true},
	)
	service := NewCatalogService(
		&providerRepo{items: map[uuid.UUID]*catalogmodel.Provider{provider.ID: provider}},
		&credentialRepo{},
		&deploymentRepo{items: map[uuid.UUID]*catalogmodel.Deployment{deployment.ID: deployment}},
		&aliasRepo{}, &targetRepo{}, &organizationRepo{}, &projectRepo{},
		&revisionRepo{}, &testTransactions{}, &recordingCipher{},
	)
	name := "renamed"

	_, err := service.UpdateDeployment(context.Background(), dto.UpdateDeployment{ID: deployment.ID, Name: &name})
	assertApplicationErrorCode(t, err, controlerrors.NotFound)
}

func TestUpdateModelAliasRejectsMissingOrganization(t *testing.T) {
	project, _ := tenantmodel.NewProject(uuid.Must(uuid.NewV7()), "Production")
	alias, _ := catalogmodel.NewModelAlias(project.ID, "assistant")
	service := NewCatalogService(
		&providerRepo{}, &credentialRepo{}, &deploymentRepo{},
		&aliasRepo{items: map[uuid.UUID]*catalogmodel.ModelAlias{alias.ID: alias}},
		&targetRepo{}, &organizationRepo{},
		&projectRepo{items: map[uuid.UUID]*tenantmodel.Project{project.ID: project}},
		&revisionRepo{}, &testTransactions{}, &recordingCipher{},
	)
	name := "renamed"

	_, err := service.UpdateModelAlias(context.Background(), dto.UpdateModelAlias{ID: alias.ID, Name: &name})
	assertApplicationErrorCode(t, err, controlerrors.NotFound)
}

func TestUpdateRouteTargetRevalidatesLogicalRelations(t *testing.T) {
	organization, _ := tenantmodel.NewOrganization("Acme")
	project, _ := tenantmodel.NewProject(organization.ID, "Production")
	alias, _ := catalogmodel.NewModelAlias(project.ID, "assistant")
	provider, _ := catalogmodel.NewProvider("Fake", "fake")
	deployment, _ := catalogmodel.NewDeployment(
		provider.ID, nil, "fake", "fake-model", "fake",
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
		&revisionRepo{}, &testTransactions{}, &recordingCipher{},
	)
	weight := 50

	_, err := service.UpdateRouteTarget(context.Background(), dto.UpdateRouteTarget{ID: target.ID, Weight: &weight})
	assertApplicationErrorCode(t, err, controlerrors.PermissionDenied)
}

func assertApplicationErrorCode(t *testing.T, err error, want controlerrors.Code) {
	t.Helper()
	var applicationError *controlerrors.Error
	if !errors.As(err, &applicationError) || applicationError.Code != want {
		t.Fatalf("error = %v; want code %s", err, want)
	}
}

type transactionMarker struct{}

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
}

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
	saved *catalogmodel.ProviderCredential
}

func (r *credentialRepo) Save(_ context.Context, value *catalogmodel.ProviderCredential) error {
	r.saved = value
	return nil
}
func (r *credentialRepo) FindByID(context.Context, uuid.UUID) (*catalogmodel.ProviderCredential, error) {
	return nil, nil
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
