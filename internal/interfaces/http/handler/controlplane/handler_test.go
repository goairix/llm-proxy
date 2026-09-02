package controlplane

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/goairix/llm-proxy/internal/application/controlplane/dto"
	controlerrors "github.com/goairix/llm-proxy/internal/application/controlplane/errors"
	catalogmodel "github.com/goairix/llm-proxy/internal/domain/catalog/model"
	tenantmodel "github.com/goairix/llm-proxy/internal/domain/tenancy/model"
	"github.com/goairix/llm-proxy/internal/interfaces/http/middleware"
)

func TestCreateOrganizationReturnsResourceAndRevision(t *testing.T) {
	organization, _ := tenantmodel.NewOrganization("Acme")
	tenancy := &stubTenancy{
		createOrganization: func(_ context.Context, command dto.CreateOrganization) (dto.OrganizationResult, error) {
			if command.Name != "Acme" {
				t.Fatalf("command = %+v", command)
			}
			return dto.OrganizationResult{Organization: *organization, Revision: 7}, nil
		},
	}
	handler := middleware.RequestID(New(Dependencies{Tenancy: tenancy, Catalog: &stubCatalog{}}))
	req := httptest.NewRequest(http.MethodPost, "/v1/organizations", strings.NewReader(`{"name":"Acme"}`))
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusCreated {
		t.Fatalf("status = %d body=%s", recorder.Code, recorder.Body.String())
	}
	var response struct {
		Data struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"data"`
		Revision int64 `json:"config_revision"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Data.ID != organization.ID.String() || response.Data.Name != "Acme" || response.Revision != 7 {
		t.Fatalf("response = %+v", response)
	}
}

func TestVirtualKeySecretAppearsOnlyInCreateResponse(t *testing.T) {
	projectID := uuid.Must(uuid.NewV7())
	keyID := uuid.Must(uuid.NewV7())
	tenancy := &stubTenancy{
		createVirtualKey: func(context.Context, dto.CreateVirtualKey) (dto.CreateVirtualKeyResult, error) {
			return dto.CreateVirtualKeyResult{ID: keyID, Secret: "llmp_v1_one-time-secret", Prefix: "llmp_v1_one", LastFour: "cret", Revision: 2}, nil
		},
		getVirtualKey: func(context.Context, uuid.UUID) (dto.VirtualKeyView, error) {
			return dto.VirtualKeyView{ID: keyID, ProjectID: projectID, Name: "ci", Prefix: "llmp_v1_one", LastFour: "cret"}, nil
		},
	}
	handler := middleware.RequestID(New(Dependencies{Tenancy: tenancy, Catalog: &stubCatalog{}}))

	createReq := httptest.NewRequest(http.MethodPost, "/v1/projects/"+projectID.String()+"/virtual-keys", strings.NewReader(`{"name":"ci"}`))
	createRecorder := httptest.NewRecorder()
	handler.ServeHTTP(createRecorder, createReq)
	if createRecorder.Code != http.StatusCreated || !strings.Contains(createRecorder.Body.String(), "llmp_v1_one-time-secret") {
		t.Fatalf("create response = %d %s", createRecorder.Code, createRecorder.Body.String())
	}

	getReq := httptest.NewRequest(http.MethodGet, "/v1/virtual-keys/"+keyID.String(), nil)
	getRecorder := httptest.NewRecorder()
	handler.ServeHTTP(getRecorder, getReq)
	if getRecorder.Code != http.StatusOK {
		t.Fatalf("get response = %d %s", getRecorder.Code, getRecorder.Body.String())
	}
	for _, forbidden := range []string{"one-time-secret", `"secret"`, `"hash"`} {
		if strings.Contains(strings.ToLower(getRecorder.Body.String()), forbidden) {
			t.Fatalf("get response leaked %q: %s", forbidden, getRecorder.Body.String())
		}
	}
}

func TestProviderCredentialReadNeverReturnsEnvelope(t *testing.T) {
	credentialID := uuid.Must(uuid.NewV7())
	providerID := uuid.Must(uuid.NewV7())
	catalog := &stubCatalog{
		getProviderCredential: func(context.Context, uuid.UUID) (dto.ProviderCredentialResult, error) {
			return dto.ProviderCredentialResult{
				ID: credentialID, ProviderID: providerID,
				Scope: catalogmodel.Scope{Kind: catalogmodel.ScopePlatform},
			}, nil
		},
	}
	handler := middleware.RequestID(New(Dependencies{Tenancy: &stubTenancy{}, Catalog: catalog}))
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/v1/provider-credentials/"+credentialID.String(), nil))

	if recorder.Code != http.StatusOK {
		t.Fatalf("response = %d %s", recorder.Code, recorder.Body.String())
	}
	for _, forbidden := range []string{"ciphertext", "wrapped_data_key", "wrapped_key_nonce", "payload_nonce", "key_version", "credential"} {
		if strings.Contains(strings.ToLower(recorder.Body.String()), forbidden) {
			t.Fatalf("response leaked %q: %s", forbidden, recorder.Body.String())
		}
	}
}

func TestProviderHTTPContractOwnsBaseURL(t *testing.T) {
	provider, _ := catalogmodel.NewProvider("OpenAI", catalogmodel.ConnectorOpenAI, "https://api.openai.com")
	catalog := &stubCatalog{
		createProvider: func(_ context.Context, command dto.CreateProvider) (dto.ProviderResult, error) {
			if command.Name != "OpenAI" || command.ConnectorType != catalogmodel.ConnectorOpenAI || command.BaseURL != "https://api.openai.com/" {
				t.Fatalf("command=%+v", command)
			}
			return dto.ProviderResult{Provider: *provider, Revision: 4}, nil
		},
	}
	handler := middleware.RequestID(New(Dependencies{Tenancy: &stubTenancy{}, Catalog: catalog}))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/v1/providers", strings.NewReader(`{"name":"OpenAI","connector_type":"openai","base_url":"https://api.openai.com/"}`)))

	if recorder.Code != http.StatusCreated || !strings.Contains(recorder.Body.String(), `"base_url":"https://api.openai.com"`) {
		t.Fatalf("response=%d %s", recorder.Code, recorder.Body.String())
	}
}

func TestAnthropicProviderHTTPContract(t *testing.T) {
	provider, _ := catalogmodel.NewProvider("Anthropic", catalogmodel.ConnectorAnthropic, "https://api.anthropic.com")
	catalog := &stubCatalog{
		createProvider: func(_ context.Context, command dto.CreateProvider) (dto.ProviderResult, error) {
			if command.ConnectorType != catalogmodel.ConnectorAnthropic || command.BaseURL != "https://api.anthropic.com/" {
				t.Fatalf("command=%+v", command)
			}
			return dto.ProviderResult{Provider: *provider, Revision: 5}, nil
		},
	}
	handler := middleware.RequestID(New(Dependencies{Tenancy: &stubTenancy{}, Catalog: catalog}))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/v1/providers", strings.NewReader(`{"name":"Anthropic","connector_type":"anthropic","base_url":"https://api.anthropic.com/"}`)))

	response := recorder.Body.String()
	if recorder.Code != http.StatusCreated || !strings.Contains(response, `"connector_type":"anthropic"`) ||
		!strings.Contains(response, `"base_url":"https://api.anthropic.com"`) {
		t.Fatalf("response=%d %s", recorder.Code, response)
	}
}

func TestDeploymentHTTPContractRejectsLegacyBindingFields(t *testing.T) {
	providerID := uuid.Must(uuid.NewV7())
	for name, field := range map[string]string{
		"credential": `,"credential_id":"` + uuid.Must(uuid.NewV7()).String() + `"`,
		"connector":  `,"connector_type":"openai"`,
	} {
		t.Run(name, func(t *testing.T) {
			handler := middleware.RequestID(New(Dependencies{Tenancy: &stubTenancy{}, Catalog: &stubCatalog{}}))
			body := `{"provider_id":"` + providerID.String() + `","name":"primary","upstream_model":"gpt-5","upstream_protocol":"responses","scope":{"kind":"platform"},"capabilities":{"text":true}` + field + `}`
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/v1/deployments", strings.NewReader(body)))
			if recorder.Code != http.StatusBadRequest {
				t.Fatalf("response=%d %s", recorder.Code, recorder.Body.String())
			}
		})
	}
}

func TestDeploymentHTTPContractUsesUpstreamProtocol(t *testing.T) {
	providerID := uuid.Must(uuid.NewV7())
	deployment, _ := catalogmodel.NewDeployment(
		providerID, "primary", "gpt-5", catalogmodel.UpstreamResponses,
		catalogmodel.Scope{Kind: catalogmodel.ScopePlatform}, catalogmodel.CapabilitySet{Text: true},
	)
	catalog := &stubCatalog{
		createDeployment: func(_ context.Context, command dto.CreateDeployment) (dto.DeploymentResult, error) {
			if command.ProviderID != providerID || command.UpstreamProtocol != catalogmodel.UpstreamResponses {
				t.Fatalf("command=%+v", command)
			}
			return dto.DeploymentResult{Deployment: *deployment, Revision: 6}, nil
		},
	}
	handler := middleware.RequestID(New(Dependencies{Tenancy: &stubTenancy{}, Catalog: catalog}))
	body := `{"provider_id":"` + providerID.String() + `","name":"primary","upstream_model":"gpt-5","upstream_protocol":"responses","scope":{"kind":"platform"},"capabilities":{"text":true}}`
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/v1/deployments", strings.NewReader(body)))

	response := recorder.Body.String()
	if recorder.Code != http.StatusCreated || !strings.Contains(response, `"upstream_protocol":"responses"`) || strings.Contains(response, "credential_id") || strings.Contains(response, "connector_type") {
		t.Fatalf("response=%d %s", recorder.Code, response)
	}
}

func TestAnthropicDeploymentHTTPContract(t *testing.T) {
	providerID := uuid.Must(uuid.NewV7())
	deployment, _ := catalogmodel.NewDeployment(
		providerID, "claude", "claude-sonnet", catalogmodel.UpstreamAnthropicMessages,
		catalogmodel.Scope{Kind: catalogmodel.ScopePlatform}, catalogmodel.CapabilitySet{Text: true},
	)
	catalog := &stubCatalog{
		createDeployment: func(_ context.Context, command dto.CreateDeployment) (dto.DeploymentResult, error) {
			if command.ProviderID != providerID || command.UpstreamProtocol != catalogmodel.UpstreamAnthropicMessages {
				t.Fatalf("command=%+v", command)
			}
			return dto.DeploymentResult{Deployment: *deployment, Revision: 7}, nil
		},
	}
	handler := middleware.RequestID(New(Dependencies{Tenancy: &stubTenancy{}, Catalog: catalog}))
	body := `{"provider_id":"` + providerID.String() + `","name":"claude","upstream_model":"claude-sonnet","upstream_protocol":"anthropic_messages","scope":{"kind":"platform"},"capabilities":{"text":true}}`
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/v1/deployments", strings.NewReader(body)))

	if response := recorder.Body.String(); recorder.Code != http.StatusCreated || !strings.Contains(response, `"upstream_protocol":"anthropic_messages"`) {
		t.Fatalf("response=%d %s", recorder.Code, response)
	}
}

func TestHandlerRejectsInvalidUUID(t *testing.T) {
	handler := middleware.RequestID(New(Dependencies{Tenancy: &stubTenancy{}, Catalog: &stubCatalog{}}))
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/v1/projects/not-a-uuid", nil))

	if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), "invalid_request") {
		t.Fatalf("response = %d %s", recorder.Code, recorder.Body.String())
	}
}

func TestHandlerMapsDependencyUnavailableWithoutLeakingCause(t *testing.T) {
	cause := errors.New("dial postgres://admin:secret@private.internal/database")
	tenancy := &stubTenancy{
		listOrganizations: func(context.Context, dto.Pagination) ([]tenantmodel.Organization, error) {
			return nil, controlerrors.New(controlerrors.DependencyUnavailable, "依赖服务暂不可用", "", cause)
		},
	}
	handler := middleware.RequestID(New(Dependencies{Tenancy: tenancy, Catalog: &stubCatalog{}}))
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/v1/organizations", nil))

	if recorder.Code != http.StatusServiceUnavailable || !strings.Contains(recorder.Body.String(), "dependency_unavailable") {
		t.Fatalf("response = %d %s", recorder.Code, recorder.Body.String())
	}
	if strings.Contains(recorder.Body.String(), "private.internal") || strings.Contains(recorder.Body.String(), "secret") {
		t.Fatalf("response leaked cause: %s", recorder.Body.String())
	}
}

func TestHandlerLimitsJSONBodyAndRejectsUnknownFields(t *testing.T) {
	handler := middleware.RequestID(New(Dependencies{Tenancy: &stubTenancy{}, Catalog: &stubCatalog{}}))

	tooLarge := httptest.NewRecorder()
	handler.ServeHTTP(tooLarge, httptest.NewRequest(
		http.MethodPost,
		"/v1/organizations",
		strings.NewReader(`{"name":"`+strings.Repeat("a", maxRequestBodyBytes)+`"}`),
	))
	if tooLarge.Code != http.StatusBadRequest || !strings.Contains(tooLarge.Body.String(), "invalid_request") {
		t.Fatalf("large body response = %d %s", tooLarge.Code, tooLarge.Body.String())
	}

	unknownField := httptest.NewRecorder()
	handler.ServeHTTP(unknownField, httptest.NewRequest(
		http.MethodPost,
		"/v1/organizations",
		strings.NewReader(`{"name":"Acme","unexpected":true}`),
	))
	if unknownField.Code != http.StatusBadRequest || !strings.Contains(unknownField.Body.String(), "invalid_request") {
		t.Fatalf("unknown field response = %d %s", unknownField.Code, unknownField.Body.String())
	}
}

func TestAllResourcePathsRejectDelete(t *testing.T) {
	handler := middleware.RequestID(New(Dependencies{Tenancy: &stubTenancy{}, Catalog: &stubCatalog{}}))
	id := uuid.Must(uuid.NewV7()).String()
	paths := []string{
		"/v1/organizations",
		"/v1/organizations/" + id,
		"/v1/organizations/" + id + "/projects",
		"/v1/projects/" + id,
		"/v1/projects/" + id + "/virtual-keys",
		"/v1/virtual-keys/" + id,
		"/v1/providers",
		"/v1/providers/" + id,
		"/v1/provider-credentials",
		"/v1/provider-credentials/" + id,
		"/v1/deployments",
		"/v1/deployments/" + id,
		"/v1/model-aliases",
		"/v1/model-aliases/" + id,
		"/v1/model-aliases/" + id + "/route-targets",
		"/v1/route-targets/" + id,
	}
	for _, path := range paths {
		t.Run(path, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodDelete, path, nil))
			if recorder.Code != http.StatusMethodNotAllowed || !strings.Contains(recorder.Body.String(), "method_not_allowed") {
				t.Fatalf("response = %d %s", recorder.Code, recorder.Body.String())
			}
		})
	}
}

type stubTenancy struct {
	TenancyService
	createOrganization func(context.Context, dto.CreateOrganization) (dto.OrganizationResult, error)
	listOrganizations  func(context.Context, dto.Pagination) ([]tenantmodel.Organization, error)
	createVirtualKey   func(context.Context, dto.CreateVirtualKey) (dto.CreateVirtualKeyResult, error)
	getVirtualKey      func(context.Context, uuid.UUID) (dto.VirtualKeyView, error)
}

func (s *stubTenancy) CreateOrganization(ctx context.Context, command dto.CreateOrganization) (dto.OrganizationResult, error) {
	return s.createOrganization(ctx, command)
}

func (s *stubTenancy) ListOrganizations(ctx context.Context, pagination dto.Pagination) ([]tenantmodel.Organization, error) {
	return s.listOrganizations(ctx, pagination)
}

func (s *stubTenancy) CreateVirtualKey(ctx context.Context, command dto.CreateVirtualKey) (dto.CreateVirtualKeyResult, error) {
	return s.createVirtualKey(ctx, command)
}

func (s *stubTenancy) GetVirtualKey(ctx context.Context, id uuid.UUID) (dto.VirtualKeyView, error) {
	return s.getVirtualKey(ctx, id)
}

type stubCatalog struct {
	CatalogService
	createProvider        func(context.Context, dto.CreateProvider) (dto.ProviderResult, error)
	createDeployment      func(context.Context, dto.CreateDeployment) (dto.DeploymentResult, error)
	getProviderCredential func(context.Context, uuid.UUID) (dto.ProviderCredentialResult, error)
}

func (s *stubCatalog) CreateProvider(ctx context.Context, command dto.CreateProvider) (dto.ProviderResult, error) {
	return s.createProvider(ctx, command)
}

func (s *stubCatalog) CreateDeployment(ctx context.Context, command dto.CreateDeployment) (dto.DeploymentResult, error) {
	return s.createDeployment(ctx, command)
}

func (s *stubCatalog) GetProviderCredential(ctx context.Context, id uuid.UUID) (dto.ProviderCredentialResult, error) {
	return s.getProviderCredential(ctx, id)
}
