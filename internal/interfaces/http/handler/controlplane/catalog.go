package controlplane

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/google/uuid"

	"github.com/goairix/llm-proxy/internal/application/controlplane/dto"
	catalogmodel "github.com/goairix/llm-proxy/internal/domain/catalog/model"
	sharedmodel "github.com/goairix/llm-proxy/internal/domain/shared/model"
	httpresponse "github.com/goairix/llm-proxy/internal/interfaces/http/response"
)

func (h *Handler) registerCatalogRoutes() {
	h.mux.HandleFunc("POST /v1/providers", h.createProvider)
	h.mux.HandleFunc("GET /v1/providers", h.listProviders)
	h.mux.HandleFunc("GET /v1/providers/{provider_id}", h.getProvider)
	h.mux.HandleFunc("PATCH /v1/providers/{provider_id}", h.updateProvider)
	h.mux.HandleFunc("POST /v1/provider-credentials", h.createProviderCredential)
	h.mux.HandleFunc("GET /v1/provider-credentials", h.listProviderCredentials)
	h.mux.HandleFunc("GET /v1/provider-credentials/{credential_id}", h.getProviderCredential)
	h.mux.HandleFunc("PATCH /v1/provider-credentials/{credential_id}", h.updateProviderCredential)
	h.mux.HandleFunc("POST /v1/deployments", h.createDeployment)
	h.mux.HandleFunc("GET /v1/deployments", h.listDeployments)
	h.mux.HandleFunc("GET /v1/deployments/{deployment_id}", h.getDeployment)
	h.mux.HandleFunc("PATCH /v1/deployments/{deployment_id}", h.updateDeployment)
	h.mux.HandleFunc("POST /v1/model-aliases", h.createModelAlias)
	h.mux.HandleFunc("GET /v1/model-aliases", h.listModelAliases)
	h.mux.HandleFunc("GET /v1/model-aliases/{model_alias_id}", h.getModelAlias)
	h.mux.HandleFunc("PATCH /v1/model-aliases/{model_alias_id}", h.updateModelAlias)
	h.mux.HandleFunc("POST /v1/model-aliases/{model_alias_id}/route-targets", h.createRouteTarget)
	h.mux.HandleFunc("GET /v1/model-aliases/{model_alias_id}/route-targets", h.listRouteTargets)
	h.mux.HandleFunc("GET /v1/route-targets/{route_target_id}", h.getRouteTarget)
	h.mux.HandleFunc("PATCH /v1/route-targets/{route_target_id}", h.updateRouteTarget)

	registerMethodFallback(h.mux, "/v1/providers", "GET, POST")
	registerMethodFallback(h.mux, "/v1/providers/{provider_id}", "GET, PATCH")
	registerMethodFallback(h.mux, "/v1/provider-credentials", "GET, POST")
	registerMethodFallback(h.mux, "/v1/provider-credentials/{credential_id}", "GET, PATCH")
	registerMethodFallback(h.mux, "/v1/deployments", "GET, POST")
	registerMethodFallback(h.mux, "/v1/deployments/{deployment_id}", "GET, PATCH")
	registerMethodFallback(h.mux, "/v1/model-aliases", "GET, POST")
	registerMethodFallback(h.mux, "/v1/model-aliases/{model_alias_id}", "GET, PATCH")
	registerMethodFallback(h.mux, "/v1/model-aliases/{model_alias_id}/route-targets", "GET, POST")
	registerMethodFallback(h.mux, "/v1/route-targets/{route_target_id}", "GET, PATCH")
}

type scopePayload struct {
	Kind           catalogmodel.ScopeKind `json:"kind"`
	OrganizationID uuid.UUID              `json:"organization_id"`
	ProjectID      uuid.UUID              `json:"project_id"`
}

func (p scopePayload) domain() catalogmodel.Scope {
	return catalogmodel.Scope{Kind: p.Kind, OrganizationID: p.OrganizationID, ProjectID: p.ProjectID}
}

type credentialPayload map[string]json.RawMessage

func credentialBytes(value credentialPayload) ([]byte, error) {
	if value == nil {
		return nil, errors.New("credential is required")
	}
	return json.Marshal(value)
}

func (h *Handler) createProvider(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name          string `json:"name"`
		ConnectorType string `json:"connector_type"`
		BaseURL       string `json:"base_url"`
	}
	if err := decodeJSON(w, r, &body); err != nil {
		writeInputError(w, r, "请求体不是有效的 JSON")
		return
	}
	result, err := h.dependencies.Catalog.CreateProvider(r.Context(), dto.CreateProvider{Name: body.Name, ConnectorType: body.ConnectorType, BaseURL: body.BaseURL})
	if err != nil {
		writeApplicationError(w, r, err)
		return
	}
	httpresponse.JSON(w, http.StatusCreated, writeEnvelope[providerView]{Data: viewProvider(result.Provider), ConfigRevision: result.Revision})
}

func (h *Handler) listProviders(w http.ResponseWriter, r *http.Request) {
	page, err := pagination(r)
	if err != nil {
		writeInputError(w, r, "分页参数不合法")
		return
	}
	values, err := h.dependencies.Catalog.ListProviders(r.Context(), page)
	if err != nil {
		writeApplicationError(w, r, err)
		return
	}
	views := make([]providerView, 0, len(values))
	for _, value := range values {
		views = append(views, viewProvider(value))
	}
	httpresponse.JSON(w, http.StatusOK, dataEnvelope[[]providerView]{Data: views})
}

func (h *Handler) getProvider(w http.ResponseWriter, r *http.Request) {
	id, err := pathUUID(r, "provider_id")
	if err != nil {
		writeInputError(w, r, "provider_id 不是有效的 UUID")
		return
	}
	value, err := h.dependencies.Catalog.GetProvider(r.Context(), id)
	if err != nil {
		writeApplicationError(w, r, err)
		return
	}
	httpresponse.JSON(w, http.StatusOK, dataEnvelope[providerView]{Data: viewProvider(*value)})
}

func (h *Handler) updateProvider(w http.ResponseWriter, r *http.Request) {
	id, err := pathUUID(r, "provider_id")
	if err != nil {
		writeInputError(w, r, "provider_id 不是有效的 UUID")
		return
	}
	var body struct {
		Name    *string             `json:"name"`
		BaseURL *string             `json:"base_url"`
		Status  *sharedmodel.Status `json:"status"`
	}
	if err := decodeJSON(w, r, &body); err != nil {
		writeInputError(w, r, "请求体不是有效的 JSON")
		return
	}
	result, err := h.dependencies.Catalog.UpdateProvider(r.Context(), dto.UpdateProvider{ID: id, Name: body.Name, BaseURL: body.BaseURL, Status: body.Status})
	if err != nil {
		writeApplicationError(w, r, err)
		return
	}
	httpresponse.JSON(w, http.StatusOK, writeEnvelope[providerView]{Data: viewProvider(result.Provider), ConfigRevision: result.Revision})
}

func (h *Handler) createProviderCredential(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ProviderID uuid.UUID         `json:"provider_id"`
		Scope      scopePayload      `json:"scope"`
		Credential credentialPayload `json:"credential"`
	}
	if err := decodeJSON(w, r, &body); err != nil {
		writeInputError(w, r, "请求体不是有效的 JSON")
		return
	}
	credential, err := credentialBytes(body.Credential)
	if err != nil {
		writeInputError(w, r, "credential 必须是 JSON 对象")
		return
	}
	result, err := h.dependencies.Catalog.CreateProviderCredential(r.Context(), dto.CreateProviderCredential{ProviderID: body.ProviderID, Scope: body.Scope.domain(), Credential: credential})
	if err != nil {
		writeApplicationError(w, r, err)
		return
	}
	httpresponse.JSON(w, http.StatusCreated, writeEnvelope[providerCredentialView]{Data: viewProviderCredential(result), ConfigRevision: result.Revision})
}

func (h *Handler) listProviderCredentials(w http.ResponseWriter, r *http.Request) {
	page, err := pagination(r)
	if err != nil {
		writeInputError(w, r, "分页参数不合法")
		return
	}
	values, err := h.dependencies.Catalog.ListProviderCredentials(r.Context(), page)
	if err != nil {
		writeApplicationError(w, r, err)
		return
	}
	views := make([]providerCredentialView, 0, len(values))
	for _, value := range values {
		views = append(views, viewProviderCredential(value))
	}
	httpresponse.JSON(w, http.StatusOK, dataEnvelope[[]providerCredentialView]{Data: views})
}

func (h *Handler) getProviderCredential(w http.ResponseWriter, r *http.Request) {
	id, err := pathUUID(r, "credential_id")
	if err != nil {
		writeInputError(w, r, "credential_id 不是有效的 UUID")
		return
	}
	value, err := h.dependencies.Catalog.GetProviderCredential(r.Context(), id)
	if err != nil {
		writeApplicationError(w, r, err)
		return
	}
	httpresponse.JSON(w, http.StatusOK, dataEnvelope[providerCredentialView]{Data: viewProviderCredential(value)})
}

func (h *Handler) updateProviderCredential(w http.ResponseWriter, r *http.Request) {
	id, err := pathUUID(r, "credential_id")
	if err != nil {
		writeInputError(w, r, "credential_id 不是有效的 UUID")
		return
	}
	var body struct {
		Status     *sharedmodel.Status `json:"status"`
		Credential credentialPayload   `json:"credential"`
	}
	if err := decodeJSON(w, r, &body); err != nil {
		writeInputError(w, r, "请求体不是有效的 JSON")
		return
	}
	var credential []byte
	if body.Credential != nil {
		credential, err = credentialBytes(body.Credential)
		if err != nil {
			writeInputError(w, r, "credential 必须是 JSON 对象")
			return
		}
	}
	result, err := h.dependencies.Catalog.UpdateProviderCredential(r.Context(), dto.UpdateProviderCredential{ID: id, Status: body.Status, Credential: credential})
	if err != nil {
		writeApplicationError(w, r, err)
		return
	}
	httpresponse.JSON(w, http.StatusOK, writeEnvelope[providerCredentialView]{Data: viewProviderCredential(result), ConfigRevision: result.Revision})
}

func (h *Handler) createDeployment(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ProviderID       uuid.UUID                     `json:"provider_id"`
		Name             string                        `json:"name"`
		UpstreamModel    string                        `json:"upstream_model"`
		UpstreamProtocol catalogmodel.UpstreamProtocol `json:"upstream_protocol"`
		Scope            scopePayload                  `json:"scope"`
		Capabilities     catalogmodel.CapabilitySet    `json:"capabilities"`
	}
	if err := decodeJSON(w, r, &body); err != nil {
		writeInputError(w, r, "请求体不是有效的 JSON")
		return
	}
	result, err := h.dependencies.Catalog.CreateDeployment(r.Context(), dto.CreateDeployment{
		ProviderID: body.ProviderID, Name: body.Name, UpstreamModel: body.UpstreamModel,
		UpstreamProtocol: body.UpstreamProtocol, Scope: body.Scope.domain(), Capabilities: body.Capabilities,
	})
	if err != nil {
		writeApplicationError(w, r, err)
		return
	}
	httpresponse.JSON(w, http.StatusCreated, writeEnvelope[deploymentView]{Data: viewDeployment(result.Deployment), ConfigRevision: result.Revision})
}

func (h *Handler) listDeployments(w http.ResponseWriter, r *http.Request) {
	page, err := pagination(r)
	if err != nil {
		writeInputError(w, r, "分页参数不合法")
		return
	}
	values, err := h.dependencies.Catalog.ListDeployments(r.Context(), page)
	if err != nil {
		writeApplicationError(w, r, err)
		return
	}
	views := make([]deploymentView, 0, len(values))
	for _, value := range values {
		views = append(views, viewDeployment(value))
	}
	httpresponse.JSON(w, http.StatusOK, dataEnvelope[[]deploymentView]{Data: views})
}

func (h *Handler) getDeployment(w http.ResponseWriter, r *http.Request) {
	id, err := pathUUID(r, "deployment_id")
	if err != nil {
		writeInputError(w, r, "deployment_id 不是有效的 UUID")
		return
	}
	value, err := h.dependencies.Catalog.GetDeployment(r.Context(), id)
	if err != nil {
		writeApplicationError(w, r, err)
		return
	}
	httpresponse.JSON(w, http.StatusOK, dataEnvelope[deploymentView]{Data: viewDeployment(*value)})
}

func (h *Handler) updateDeployment(w http.ResponseWriter, r *http.Request) {
	id, err := pathUUID(r, "deployment_id")
	if err != nil {
		writeInputError(w, r, "deployment_id 不是有效的 UUID")
		return
	}
	var body struct {
		Name             *string                        `json:"name"`
		UpstreamModel    *string                        `json:"upstream_model"`
		UpstreamProtocol *catalogmodel.UpstreamProtocol `json:"upstream_protocol"`
		Capabilities     *catalogmodel.CapabilitySet    `json:"capabilities"`
		Status           *sharedmodel.Status            `json:"status"`
	}
	if err := decodeJSON(w, r, &body); err != nil {
		writeInputError(w, r, "请求体不是有效的 JSON")
		return
	}
	result, err := h.dependencies.Catalog.UpdateDeployment(r.Context(), dto.UpdateDeployment{
		ID: id, Name: body.Name, UpstreamModel: body.UpstreamModel, UpstreamProtocol: body.UpstreamProtocol,
		Capabilities: body.Capabilities, Status: body.Status,
	})
	if err != nil {
		writeApplicationError(w, r, err)
		return
	}
	httpresponse.JSON(w, http.StatusOK, writeEnvelope[deploymentView]{Data: viewDeployment(result.Deployment), ConfigRevision: result.Revision})
}

func (h *Handler) createModelAlias(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ProjectID uuid.UUID `json:"project_id"`
		Name      string    `json:"name"`
	}
	if err := decodeJSON(w, r, &body); err != nil {
		writeInputError(w, r, "请求体不是有效的 JSON")
		return
	}
	result, err := h.dependencies.Catalog.CreateModelAlias(r.Context(), dto.CreateModelAlias{ProjectID: body.ProjectID, Name: body.Name})
	if err != nil {
		writeApplicationError(w, r, err)
		return
	}
	httpresponse.JSON(w, http.StatusCreated, writeEnvelope[modelAliasView]{Data: viewModelAlias(result.ModelAlias), ConfigRevision: result.Revision})
}

func (h *Handler) listModelAliases(w http.ResponseWriter, r *http.Request) {
	page, err := pagination(r)
	if err != nil {
		writeInputError(w, r, "分页参数不合法")
		return
	}
	values, err := h.dependencies.Catalog.ListModelAliases(r.Context(), page)
	if err != nil {
		writeApplicationError(w, r, err)
		return
	}
	views := make([]modelAliasView, 0, len(values))
	for _, value := range values {
		views = append(views, viewModelAlias(value))
	}
	httpresponse.JSON(w, http.StatusOK, dataEnvelope[[]modelAliasView]{Data: views})
}

func (h *Handler) getModelAlias(w http.ResponseWriter, r *http.Request) {
	id, err := pathUUID(r, "model_alias_id")
	if err != nil {
		writeInputError(w, r, "model_alias_id 不是有效的 UUID")
		return
	}
	value, err := h.dependencies.Catalog.GetModelAlias(r.Context(), id)
	if err != nil {
		writeApplicationError(w, r, err)
		return
	}
	httpresponse.JSON(w, http.StatusOK, dataEnvelope[modelAliasView]{Data: viewModelAlias(*value)})
}

func (h *Handler) updateModelAlias(w http.ResponseWriter, r *http.Request) {
	id, err := pathUUID(r, "model_alias_id")
	if err != nil {
		writeInputError(w, r, "model_alias_id 不是有效的 UUID")
		return
	}
	var body struct {
		Name   *string             `json:"name"`
		Status *sharedmodel.Status `json:"status"`
	}
	if err := decodeJSON(w, r, &body); err != nil {
		writeInputError(w, r, "请求体不是有效的 JSON")
		return
	}
	result, err := h.dependencies.Catalog.UpdateModelAlias(r.Context(), dto.UpdateModelAlias{ID: id, Name: body.Name, Status: body.Status})
	if err != nil {
		writeApplicationError(w, r, err)
		return
	}
	httpresponse.JSON(w, http.StatusOK, writeEnvelope[modelAliasView]{Data: viewModelAlias(result.ModelAlias), ConfigRevision: result.Revision})
}

func (h *Handler) createRouteTarget(w http.ResponseWriter, r *http.Request) {
	modelAliasID, err := pathUUID(r, "model_alias_id")
	if err != nil {
		writeInputError(w, r, "model_alias_id 不是有效的 UUID")
		return
	}
	var body struct {
		DeploymentID uuid.UUID `json:"deployment_id"`
		Priority     int       `json:"priority"`
		Weight       int       `json:"weight"`
	}
	if err := decodeJSON(w, r, &body); err != nil {
		writeInputError(w, r, "请求体不是有效的 JSON")
		return
	}
	result, err := h.dependencies.Catalog.CreateRouteTarget(r.Context(), dto.CreateRouteTarget{ModelAliasID: modelAliasID, DeploymentID: body.DeploymentID, Priority: body.Priority, Weight: body.Weight})
	if err != nil {
		writeApplicationError(w, r, err)
		return
	}
	httpresponse.JSON(w, http.StatusCreated, writeEnvelope[routeTargetView]{Data: viewRouteTarget(result.RouteTarget), ConfigRevision: result.Revision})
}

func (h *Handler) listRouteTargets(w http.ResponseWriter, r *http.Request) {
	modelAliasID, err := pathUUID(r, "model_alias_id")
	if err != nil {
		writeInputError(w, r, "model_alias_id 不是有效的 UUID")
		return
	}
	values, err := h.dependencies.Catalog.ListRouteTargets(r.Context(), modelAliasID)
	if err != nil {
		writeApplicationError(w, r, err)
		return
	}
	views := make([]routeTargetView, 0, len(values))
	for _, value := range values {
		views = append(views, viewRouteTarget(value))
	}
	httpresponse.JSON(w, http.StatusOK, dataEnvelope[[]routeTargetView]{Data: views})
}

func (h *Handler) getRouteTarget(w http.ResponseWriter, r *http.Request) {
	id, err := pathUUID(r, "route_target_id")
	if err != nil {
		writeInputError(w, r, "route_target_id 不是有效的 UUID")
		return
	}
	value, err := h.dependencies.Catalog.GetRouteTarget(r.Context(), id)
	if err != nil {
		writeApplicationError(w, r, err)
		return
	}
	httpresponse.JSON(w, http.StatusOK, dataEnvelope[routeTargetView]{Data: viewRouteTarget(*value)})
}

func (h *Handler) updateRouteTarget(w http.ResponseWriter, r *http.Request) {
	id, err := pathUUID(r, "route_target_id")
	if err != nil {
		writeInputError(w, r, "route_target_id 不是有效的 UUID")
		return
	}
	var body struct {
		Priority *int                `json:"priority"`
		Weight   *int                `json:"weight"`
		Status   *sharedmodel.Status `json:"status"`
	}
	if err := decodeJSON(w, r, &body); err != nil {
		writeInputError(w, r, "请求体不是有效的 JSON")
		return
	}
	result, err := h.dependencies.Catalog.UpdateRouteTarget(r.Context(), dto.UpdateRouteTarget{ID: id, Priority: body.Priority, Weight: body.Weight, Status: body.Status})
	if err != nil {
		writeApplicationError(w, r, err)
		return
	}
	httpresponse.JSON(w, http.StatusOK, writeEnvelope[routeTargetView]{Data: viewRouteTarget(result.RouteTarget), ConfigRevision: result.Revision})
}
