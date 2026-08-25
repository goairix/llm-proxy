package snapshot

import (
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	catalogmodel "github.com/goairix/llm-proxy/internal/domain/catalog/model"
	sharedmodel "github.com/goairix/llm-proxy/internal/domain/shared/model"
	tenantmodel "github.com/goairix/llm-proxy/internal/domain/tenancy/model"
)

var fixedNow = time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)

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
	if !ok || plan.Deployment.ConnectorType != "fake" || !plan.Deployment.Capabilities.Streaming {
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

func TestCompilerRejectsCredentialOutsideDeploymentScope(t *testing.T) {
	config := validSourceConfig(t)
	otherOrganization, _ := tenantmodel.NewOrganization("Other")
	otherProject, _ := tenantmodel.NewProject(otherOrganization.ID, "Other Project")
	config.Organizations = append(config.Organizations, *otherOrganization)
	config.Projects = append(config.Projects, *otherProject)

	provider, _ := catalogmodel.NewProvider("OpenAI", "openai")
	sealed := testSealedCredential()
	credential, _ := catalogmodel.NewProviderCredential(provider.ID, catalogmodel.Scope{Kind: catalogmodel.ScopeProject, ProjectID: otherProject.ID}, sealed)
	credentialID := credential.ID
	deployment, _ := catalogmodel.NewDeployment(
		provider.ID, &credentialID, "OpenAI", "gpt-test", "openai",
		catalogmodel.Scope{Kind: catalogmodel.ScopeProject, ProjectID: config.Projects[0].ID},
		catalogmodel.CapabilitySet{Text: true},
	)
	config.Providers = append(config.Providers, *provider)
	config.Credentials = append(config.Credentials, *credential)
	config.Deployments = append(config.Deployments, *deployment)

	_, err := NewCompiler().Compile(config, fixedNow)
	if err == nil || !strings.Contains(err.Error(), "作用域") {
		t.Fatalf("error = %v; want scope error", err)
	}
}

func TestCompilerCopiesCredentialEnvelopeWithoutPlaintext(t *testing.T) {
	config := validSourceConfig(t)
	provider, _ := catalogmodel.NewProvider("OpenAI", "openai")
	sealed := testSealedCredential()
	credential, _ := catalogmodel.NewProviderCredential(provider.ID, catalogmodel.Scope{Kind: catalogmodel.ScopePlatform}, sealed)
	credentialID := credential.ID
	deployment, _ := catalogmodel.NewDeployment(
		provider.ID, &credentialID, "OpenAI", "gpt-test", "openai",
		catalogmodel.Scope{Kind: catalogmodel.ScopePlatform}, catalogmodel.CapabilitySet{Text: true, Tools: true},
	)
	config.Providers = append(config.Providers, *provider)
	config.Credentials = append(config.Credentials, *credential)
	config.Deployments[0] = *deployment
	config.RouteTargets[0].DeploymentID = deployment.ID

	compiled, err := NewCompiler().Compile(config, fixedNow)
	if err != nil {
		t.Fatal(err)
	}
	plan := compiled.routes[RouteKey{ProjectID: config.Projects[0].ID, Model: "assistant"}]
	if plan.Deployment.Credential == nil || plan.Deployment.Credential.Sealed.KeyVersion != "v1" || !plan.Deployment.Capabilities.Tools {
		t.Fatalf("plan = %+v", plan)
	}
	credential.Sealed.Ciphertext[0] = 99
	if plan.Deployment.Credential.Sealed.Ciphertext[0] == 99 {
		t.Fatal("compiled snapshot aliases source credential bytes")
	}
}

func validSourceConfig(t *testing.T) SourceConfig {
	t.Helper()
	organization, _ := tenantmodel.NewOrganization("Acme")
	project, _ := tenantmodel.NewProject(organization.ID, "Production")
	key, _ := tenantmodel.NewVirtualKey(project.ID, "ci", [32]byte{1, 2, 3}, "llmp_v1_test", "body", nil)
	provider, _ := catalogmodel.NewProvider("Fake", "fake")
	deployment, _ := catalogmodel.NewDeployment(
		provider.ID, nil, "Fake", "fake-model", "fake", catalogmodel.Scope{Kind: catalogmodel.ScopePlatform},
		catalogmodel.CapabilitySet{Text: true, Streaming: true},
	)
	alias, _ := catalogmodel.NewModelAlias(project.ID, "assistant")
	target, _ := catalogmodel.NewRouteTarget(alias.ID, deployment.ID, 0, 100)
	return SourceConfig{
		Revision: 7, Organizations: []tenantmodel.Organization{*organization}, Projects: []tenantmodel.Project{*project},
		VirtualKeys: []tenantmodel.VirtualKey{*key}, Providers: []catalogmodel.Provider{*provider},
		Deployments: []catalogmodel.Deployment{*deployment}, ModelAliases: []catalogmodel.ModelAlias{*alias},
		RouteTargets: []catalogmodel.RouteTarget{*target},
	}
}

func testSealedCredential() catalogmodel.SealedCredential {
	return catalogmodel.SealedCredential{
		KeyVersion: "v1", WrappedKeyNonce: []byte{1}, WrappedDataKey: []byte{2}, PayloadNonce: []byte{3}, Ciphertext: []byte{4, 5},
	}
}
