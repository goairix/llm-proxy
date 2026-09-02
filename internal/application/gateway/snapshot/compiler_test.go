package snapshot

import (
	"crypto/sha256"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	catalogmodel "github.com/goairix/llm-proxy/internal/domain/catalog/model"
	sharedmodel "github.com/goairix/llm-proxy/internal/domain/shared/model"
	tenantmodel "github.com/goairix/llm-proxy/internal/domain/tenancy/model"
)

var fixedNow = time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)

const snapshotTestVirtualKey = "llmp_v1_snapshot-provider-test"

func TestCompilerIndexesVirtualKeyAndModelAlias(t *testing.T) {
	config := validSourceConfig(t)

	compiled, err := NewCompiler().Compile(config, fixedNow)
	if err != nil {
		t.Fatal(err)
	}
	access, ok := compiled.virtualKeys[config.VirtualKeys[0].Hash]
	if !ok || access.ProjectID != config.Projects[0].ID || access.OrganizationID != config.Organizations[0].ID {
		t.Fatalf("access = %+v, ok=%v", access, ok)
	}
	plan, ok := compiled.routes[RouteKey{ProjectID: access.ProjectID, Model: "assistant"}]
	provider, providerOK := compiled.providers[plan.Deployment.ProviderID]
	if !ok || !providerOK || provider.ConnectorType != catalogmodel.ConnectorFake || !plan.Deployment.Capabilities.Streaming {
		t.Fatalf("plan = %+v, ok=%v", plan, ok)
	}
	if compiled.Revision() != config.Revision || !compiled.BuiltAt().Equal(fixedNow) {
		t.Fatalf("metadata = revision:%d built_at:%s", compiled.Revision(), compiled.BuiltAt())
	}
}

func TestCompilerExcludesDisabledTenantResources(t *testing.T) {
	tests := []struct {
		name    string
		disable func(*SourceConfig)
	}{
		{name: "organization", disable: func(config *SourceConfig) { config.Organizations[0].Status = sharedmodel.StatusDisabled }},
		{name: "project", disable: func(config *SourceConfig) { config.Projects[0].Status = sharedmodel.StatusDisabled }},
		{name: "virtual key", disable: func(config *SourceConfig) { config.VirtualKeys[0].Status = sharedmodel.StatusDisabled }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			config := validSourceConfig(t)
			test.disable(&config)
			compiled, err := NewCompiler().Compile(config, fixedNow)
			if err != nil {
				t.Fatal(err)
			}
			if len(compiled.virtualKeys) != 0 {
				t.Fatalf("virtual keys = %d", len(compiled.virtualKeys))
			}
			if test.name != "virtual key" && len(compiled.routes) != 0 {
				t.Fatalf("routes = %d", len(compiled.routes))
			}
		})
	}
}

func TestCompilerAcceptsDisabledAliasBeforeTargetPreparation(t *testing.T) {
	config := validSourceConfig(t)
	config.ModelAliases[0].Status = sharedmodel.StatusDisabled
	config.RouteTargets = nil

	compiled, err := NewCompiler().Compile(config, fixedNow)
	if err != nil {
		t.Fatal(err)
	}
	if len(compiled.routes) != 0 {
		t.Fatalf("routes = %d; want none for disabled alias", len(compiled.routes))
	}
}

func TestCompilerRejectsInvalidRoutingGraph(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*SourceConfig)
		want   string
	}{
		{name: "missing deployment", mutate: func(config *SourceConfig) {
			config.RouteTargets[0].DeploymentID = uuid.Must(uuid.NewV7())
		}, want: "route target"},
		{name: "alias without active target", mutate: func(config *SourceConfig) {
			config.RouteTargets[0].Status = sharedmodel.StatusDisabled
		}, want: "没有唯一启用"},
		{name: "multiple active targets", mutate: func(config *SourceConfig) {
			second := config.RouteTargets[0]
			second.Entity, _ = sharedmodel.NewEntity()
			config.RouteTargets = append(config.RouteTargets, second)
		}, want: "没有唯一启用"},
		{name: "multiple active targets including inactive deployment", mutate: func(config *SourceConfig) {
			inactive := config.Deployments[0]
			inactive.Entity, _ = sharedmodel.NewEntity()
			inactive.Status = sharedmodel.StatusDisabled
			config.Deployments = append(config.Deployments, inactive)
			second := config.RouteTargets[0]
			second.Entity, _ = sharedmodel.NewEntity()
			second.DeploymentID = inactive.ID
			config.RouteTargets = append(config.RouteTargets, second)
		}, want: "没有唯一启用"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			config := validSourceConfig(t)
			test.mutate(&config)
			_, err := NewCompiler().Compile(config, fixedNow)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v; want contains %q", err, test.want)
			}
			if strings.Contains(err.Error(), "provider-secret") || strings.Contains(err.Error(), config.VirtualKeys[0].Prefix) {
				t.Fatalf("error leaked protected data: %v", err)
			}
		})
	}
}

func TestCompilerPublishesRouteWithEmptyCredentialPoolAfterRevocation(t *testing.T) {
	source := validOpenAISourceConfig(t)
	source.Credentials[0].Status = sharedmodel.StatusDisabled
	source.Revision++

	compiled, err := NewCompiler().Compile(source, fixedNow)
	if err != nil {
		t.Fatal(err)
	}
	session := Session{snapshot: compiled}
	access, err := session.Authenticate(snapshotTestVirtualKey, fixedNow)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := session.Resolve(access.ProjectID, "assistant")
	if err != nil || plan.Deployment.ProviderID == uuid.Nil {
		t.Fatalf("plan=%+v err=%v", plan, err)
	}
	provider, err := session.Provider(plan.Deployment.ProviderID)
	if err != nil || provider.ConnectorType != catalogmodel.ConnectorOpenAI || provider.BaseURL != "https://api.openai.com" {
		t.Fatalf("provider=%+v err=%v", provider, err)
	}
	if _, err := session.Provider(uuid.Must(uuid.NewV7())); !errors.Is(err, ErrProviderUnavailable) {
		t.Fatalf("missing provider error=%v", err)
	}
	if _, err := session.CredentialPool(access, plan.Deployment.ProviderID); !errors.Is(err, ErrCredentialUnavailable) {
		t.Fatalf("credential pool error=%v", err)
	}
}

func TestCompilerPublishesAnthropicProvider(t *testing.T) {
	source := validOpenAISourceConfig(t)
	source.Providers[0].Name = "Anthropic"
	source.Providers[0].ConnectorType = catalogmodel.ConnectorAnthropic
	source.Providers[0].BaseURL = "https://api.anthropic.com"
	source.Deployments[0].UpstreamProtocol = catalogmodel.UpstreamAnthropicMessages

	compiled, err := NewCompiler().Compile(source, fixedNow)
	if err != nil {
		t.Fatal(err)
	}
	session := Session{snapshot: compiled}
	access, err := session.Authenticate(snapshotTestVirtualKey, fixedNow)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := session.Resolve(access.ProjectID, "assistant")
	if err != nil {
		t.Fatal(err)
	}
	provider, err := session.Provider(plan.Deployment.ProviderID)
	if err != nil || provider.ConnectorType != catalogmodel.ConnectorAnthropic ||
		plan.Deployment.UpstreamProtocol != catalogmodel.UpstreamAnthropicMessages {
		t.Fatalf("provider=%+v plan=%+v err=%v", provider, plan, err)
	}
}

func TestCompilerExcludesCredentialsInDisabledScopes(t *testing.T) {
	source := validOpenAISourceConfig(t)
	source.Credentials[0].Status = sharedmodel.StatusDisabled
	disabledOrganization, _ := tenantmodel.NewOrganization("Disabled")
	disabledOrganization.Status = sharedmodel.StatusDisabled
	disabledProject, _ := tenantmodel.NewProject(source.Organizations[0].ID, "Disabled Project")
	disabledProject.Status = sharedmodel.StatusDisabled
	organizationCredential, _ := catalogmodel.NewProviderCredential(
		source.Providers[0].ID,
		catalogmodel.Scope{Kind: catalogmodel.ScopeOrganization, OrganizationID: disabledOrganization.ID},
		sealedWithCiphertext(40),
	)
	projectCredential, _ := catalogmodel.NewProviderCredential(
		source.Providers[0].ID,
		catalogmodel.Scope{Kind: catalogmodel.ScopeProject, ProjectID: disabledProject.ID},
		sealedWithCiphertext(41),
	)
	source.Organizations = append(source.Organizations, *disabledOrganization)
	source.Projects = append(source.Projects, *disabledProject)
	source.Credentials = append(source.Credentials, *organizationCredential, *projectCredential)

	compiled, err := NewCompiler().Compile(source, fixedNow)
	if err != nil {
		t.Fatal(err)
	}
	session := Session{snapshot: compiled}
	for _, access := range []AccessContext{
		{OrganizationID: disabledOrganization.ID, ProjectID: uuid.Must(uuid.NewV7())},
		{OrganizationID: source.Organizations[0].ID, ProjectID: disabledProject.ID},
	} {
		if _, err := session.CredentialPool(access, source.Providers[0].ID); !errors.Is(err, ErrCredentialUnavailable) {
			t.Fatalf("access=%+v error=%v", access, err)
		}
	}
}

func TestCredentialPoolUsesProjectThenOrganizationThenPlatform(t *testing.T) {
	source := validOpenAISourceConfig(t)
	providerID := source.Providers[0].ID
	organizationID := source.Organizations[0].ID
	projectID := source.Projects[0].ID
	organizationCredential, _ := catalogmodel.NewProviderCredential(providerID, catalogmodel.Scope{Kind: catalogmodel.ScopeOrganization, OrganizationID: organizationID}, sealedWithCiphertext(20))
	projectCredential, _ := catalogmodel.NewProviderCredential(providerID, catalogmodel.Scope{Kind: catalogmodel.ScopeProject, ProjectID: projectID}, sealedWithCiphertext(30))
	source.Credentials = append(source.Credentials, *organizationCredential, *projectCredential)

	compiled, err := NewCompiler().Compile(source, fixedNow)
	if err != nil {
		t.Fatal(err)
	}
	access := AccessContext{OrganizationID: organizationID, ProjectID: projectID}
	pool, err := (Session{snapshot: compiled}).CredentialPool(access, providerID)
	if err != nil || pool.Key.ScopeKind != catalogmodel.ScopeProject || len(pool.Credentials) != 1 || pool.Credentials[0].CredentialID != projectCredential.ID {
		t.Fatalf("project pool=%+v err=%v", pool, err)
	}

	source.Credentials[2].Status = sharedmodel.StatusDisabled
	compiled, err = NewCompiler().Compile(source, fixedNow)
	if err != nil {
		t.Fatal(err)
	}
	pool, err = (Session{snapshot: compiled}).CredentialPool(access, providerID)
	if err != nil || pool.Key.ScopeKind != catalogmodel.ScopeOrganization || pool.Credentials[0].CredentialID != organizationCredential.ID {
		t.Fatalf("organization pool=%+v err=%v", pool, err)
	}

	source.Credentials[1].Status = sharedmodel.StatusDisabled
	compiled, err = NewCompiler().Compile(source, fixedNow)
	if err != nil {
		t.Fatal(err)
	}
	pool, err = (Session{snapshot: compiled}).CredentialPool(access, providerID)
	if err != nil || pool.Key.ScopeKind != catalogmodel.ScopePlatform || pool.Credentials[0].CredentialID != source.Credentials[0].ID {
		t.Fatalf("platform pool=%+v err=%v", pool, err)
	}
}

func TestCredentialSelectorSortsByUUIDAndReturnsDeepCopies(t *testing.T) {
	source := validOpenAISourceConfig(t)
	providerID := source.Providers[0].ID
	first, _ := catalogmodel.NewProviderCredential(providerID, catalogmodel.Scope{Kind: catalogmodel.ScopeProject, ProjectID: source.Projects[0].ID}, sealedWithCiphertext(10))
	second, _ := catalogmodel.NewProviderCredential(providerID, catalogmodel.Scope{Kind: catalogmodel.ScopeProject, ProjectID: source.Projects[0].ID}, sealedWithCiphertext(11))
	if strings.Compare(first.ID.String(), second.ID.String()) > 0 {
		first, second = second, first
	}
	wantFirstCiphertext := first.Sealed.Ciphertext[0]
	source.Credentials = append(source.Credentials, *second, *first)
	compiled, err := NewCompiler().Compile(source, fixedNow)
	if err != nil {
		t.Fatal(err)
	}
	first.Sealed.Ciphertext[0] = 98
	session := Session{snapshot: compiled}
	access := AccessContext{OrganizationID: source.Organizations[0].ID, ProjectID: source.Projects[0].ID}
	selector := NewCredentialSelector()
	selected, err := selector.Select(session, access, providerID)
	if err != nil || selected.CredentialID != first.ID || selected.Sealed.Ciphertext[0] != wantFirstCiphertext {
		t.Fatalf("first selection=%+v err=%v", selected, err)
	}
	selected.Sealed.Ciphertext[0] = 99
	selected, err = selector.Select(session, access, providerID)
	if err != nil || selected.CredentialID != second.ID || selected.Sealed.Ciphertext[0] == 99 {
		t.Fatalf("second selection=%+v err=%v", selected, err)
	}
	pool, err := session.CredentialPool(access, providerID)
	if err != nil || pool.Credentials[0].Sealed.Ciphertext[0] == 99 {
		t.Fatalf("pool aliases selection: %+v err=%v", pool, err)
	}
}

func TestCompilerRejectsCredentialWithMissingProvider(t *testing.T) {
	source := validOpenAISourceConfig(t)
	source.Credentials[0].ProviderID = uuid.Must(uuid.NewV7())
	_, err := NewCompiler().Compile(source, fixedNow)
	if err == nil || !strings.Contains(err.Error(), "Provider 不存在") {
		t.Fatalf("error=%v", err)
	}
}

func validSourceConfig(t *testing.T) SourceConfig {
	t.Helper()
	organization, _ := tenantmodel.NewOrganization("Acme")
	project, _ := tenantmodel.NewProject(organization.ID, "Production")
	key, _ := tenantmodel.NewVirtualKey(project.ID, "ci", [32]byte{1, 2, 3}, "llmp_v1_test", "body", nil)
	provider, _ := catalogmodel.NewProvider("Fake", catalogmodel.ConnectorFake, "")
	deployment, _ := catalogmodel.NewDeployment(
		provider.ID, "Fake", "fake-model", catalogmodel.UpstreamFake, catalogmodel.Scope{Kind: catalogmodel.ScopePlatform},
		catalogmodel.CapabilitySet{Text: true, Streaming: true},
	)
	alias, _ := catalogmodel.NewModelAlias(project.ID, "assistant")
	alias.Status = sharedmodel.StatusActive
	target, _ := catalogmodel.NewRouteTarget(alias.ID, deployment.ID, 0, 100)
	return SourceConfig{
		Revision: 7, Organizations: []tenantmodel.Organization{*organization}, Projects: []tenantmodel.Project{*project},
		VirtualKeys: []tenantmodel.VirtualKey{*key}, Providers: []catalogmodel.Provider{*provider},
		Deployments: []catalogmodel.Deployment{*deployment}, ModelAliases: []catalogmodel.ModelAlias{*alias},
		RouteTargets: []catalogmodel.RouteTarget{*target},
	}
}

func validOpenAISourceConfig(t *testing.T) SourceConfig {
	t.Helper()
	source := validSourceConfig(t)
	provider, err := catalogmodel.NewProvider("OpenAI", catalogmodel.ConnectorOpenAI, "https://api.openai.com")
	if err != nil {
		t.Fatal(err)
	}
	credential, err := catalogmodel.NewProviderCredential(provider.ID, catalogmodel.Scope{Kind: catalogmodel.ScopePlatform}, testSealedCredential())
	if err != nil {
		t.Fatal(err)
	}
	deployment, err := catalogmodel.NewDeployment(
		provider.ID, "GPT-5", "gpt-5", catalogmodel.UpstreamResponses,
		catalogmodel.Scope{Kind: catalogmodel.ScopePlatform}, catalogmodel.CapabilitySet{Text: true, Streaming: true},
	)
	if err != nil {
		t.Fatal(err)
	}
	source.Providers = []catalogmodel.Provider{*provider}
	source.Credentials = []catalogmodel.ProviderCredential{*credential}
	source.Deployments = []catalogmodel.Deployment{*deployment}
	source.RouteTargets[0].DeploymentID = deployment.ID
	source.VirtualKeys[0].Hash = sha256.Sum256([]byte(snapshotTestVirtualKey))
	return source
}

func sealedWithCiphertext(value byte) catalogmodel.SealedCredential {
	sealed := testSealedCredential()
	sealed.Ciphertext = []byte{value}
	return sealed
}

func testSealedCredential() catalogmodel.SealedCredential {
	return catalogmodel.SealedCredential{
		KeyVersion: "v1", WrappedKeyNonce: []byte{1}, WrappedDataKey: []byte{2}, PayloadNonce: []byte{3}, Ciphertext: []byte{4, 5},
	}
}
