package controlplane

import (
	"net/http"
	"time"

	"github.com/goairix/llm-proxy/internal/application/controlplane/dto"
	sharedmodel "github.com/goairix/llm-proxy/internal/domain/shared/model"
	httpresponse "github.com/goairix/llm-proxy/internal/interfaces/http/response"
)

func (h *Handler) registerTenancyRoutes() {
	h.mux.HandleFunc("POST /v1/organizations", h.createOrganization)
	h.mux.HandleFunc("GET /v1/organizations", h.listOrganizations)
	h.mux.HandleFunc("GET /v1/organizations/{organization_id}", h.getOrganization)
	h.mux.HandleFunc("PATCH /v1/organizations/{organization_id}", h.updateOrganization)
	h.mux.HandleFunc("POST /v1/organizations/{organization_id}/projects", h.createProject)
	h.mux.HandleFunc("GET /v1/organizations/{organization_id}/projects", h.listProjects)
	h.mux.HandleFunc("GET /v1/projects/{project_id}", h.getProject)
	h.mux.HandleFunc("PATCH /v1/projects/{project_id}", h.updateProject)
	h.mux.HandleFunc("POST /v1/projects/{project_id}/virtual-keys", h.createVirtualKey)
	h.mux.HandleFunc("GET /v1/projects/{project_id}/virtual-keys", h.listVirtualKeys)
	h.mux.HandleFunc("GET /v1/virtual-keys/{virtual_key_id}", h.getVirtualKey)
	h.mux.HandleFunc("PATCH /v1/virtual-keys/{virtual_key_id}", h.updateVirtualKey)

	registerMethodFallback(h.mux, "/v1/organizations", "GET, POST")
	registerMethodFallback(h.mux, "/v1/organizations/{organization_id}", "GET, PATCH")
	registerMethodFallback(h.mux, "/v1/organizations/{organization_id}/projects", "GET, POST")
	registerMethodFallback(h.mux, "/v1/projects/{project_id}", "GET, PATCH")
	registerMethodFallback(h.mux, "/v1/projects/{project_id}/virtual-keys", "GET, POST")
	registerMethodFallback(h.mux, "/v1/virtual-keys/{virtual_key_id}", "GET, PATCH")
}

func (h *Handler) createOrganization(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name string `json:"name"`
	}
	if err := decodeJSON(w, r, &body); err != nil {
		writeInputError(w, r, "请求体不是有效的 JSON")
		return
	}
	result, err := h.dependencies.Tenancy.CreateOrganization(r.Context(), dto.CreateOrganization{Name: body.Name})
	if err != nil {
		writeApplicationError(w, r, err)
		return
	}
	httpresponse.JSON(w, http.StatusCreated, writeEnvelope[organizationView]{Data: viewOrganization(result.Organization), ConfigRevision: result.Revision})
}

func (h *Handler) listOrganizations(w http.ResponseWriter, r *http.Request) {
	page, err := pagination(r)
	if err != nil {
		writeInputError(w, r, "分页参数不合法")
		return
	}
	values, err := h.dependencies.Tenancy.ListOrganizations(r.Context(), page)
	if err != nil {
		writeApplicationError(w, r, err)
		return
	}
	views := make([]organizationView, 0, len(values))
	for _, value := range values {
		views = append(views, viewOrganization(value))
	}
	httpresponse.JSON(w, http.StatusOK, dataEnvelope[[]organizationView]{Data: views})
}

func (h *Handler) getOrganization(w http.ResponseWriter, r *http.Request) {
	id, err := pathUUID(r, "organization_id")
	if err != nil {
		writeInputError(w, r, "organization_id 不是有效的 UUID")
		return
	}
	value, err := h.dependencies.Tenancy.GetOrganization(r.Context(), id)
	if err != nil {
		writeApplicationError(w, r, err)
		return
	}
	httpresponse.JSON(w, http.StatusOK, dataEnvelope[organizationView]{Data: viewOrganization(*value)})
}

func (h *Handler) updateOrganization(w http.ResponseWriter, r *http.Request) {
	id, err := pathUUID(r, "organization_id")
	if err != nil {
		writeInputError(w, r, "organization_id 不是有效的 UUID")
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
	result, err := h.dependencies.Tenancy.UpdateOrganization(r.Context(), dto.UpdateOrganization{ID: id, Name: body.Name, Status: body.Status})
	if err != nil {
		writeApplicationError(w, r, err)
		return
	}
	httpresponse.JSON(w, http.StatusOK, writeEnvelope[organizationView]{Data: viewOrganization(result.Organization), ConfigRevision: result.Revision})
}

func (h *Handler) createProject(w http.ResponseWriter, r *http.Request) {
	organizationID, err := pathUUID(r, "organization_id")
	if err != nil {
		writeInputError(w, r, "organization_id 不是有效的 UUID")
		return
	}
	var body struct {
		Name string `json:"name"`
	}
	if err := decodeJSON(w, r, &body); err != nil {
		writeInputError(w, r, "请求体不是有效的 JSON")
		return
	}
	result, err := h.dependencies.Tenancy.CreateProject(r.Context(), dto.CreateProject{OrganizationID: organizationID, Name: body.Name})
	if err != nil {
		writeApplicationError(w, r, err)
		return
	}
	httpresponse.JSON(w, http.StatusCreated, writeEnvelope[projectView]{Data: viewProject(result.Project), ConfigRevision: result.Revision})
}

func (h *Handler) listProjects(w http.ResponseWriter, r *http.Request) {
	organizationID, err := pathUUID(r, "organization_id")
	if err != nil {
		writeInputError(w, r, "organization_id 不是有效的 UUID")
		return
	}
	page, err := pagination(r)
	if err != nil {
		writeInputError(w, r, "分页参数不合法")
		return
	}
	values, err := h.dependencies.Tenancy.ListProjects(r.Context(), organizationID, page)
	if err != nil {
		writeApplicationError(w, r, err)
		return
	}
	views := make([]projectView, 0, len(values))
	for _, value := range values {
		views = append(views, viewProject(value))
	}
	httpresponse.JSON(w, http.StatusOK, dataEnvelope[[]projectView]{Data: views})
}

func (h *Handler) getProject(w http.ResponseWriter, r *http.Request) {
	id, err := pathUUID(r, "project_id")
	if err != nil {
		writeInputError(w, r, "project_id 不是有效的 UUID")
		return
	}
	value, err := h.dependencies.Tenancy.GetProject(r.Context(), id)
	if err != nil {
		writeApplicationError(w, r, err)
		return
	}
	httpresponse.JSON(w, http.StatusOK, dataEnvelope[projectView]{Data: viewProject(*value)})
}

func (h *Handler) updateProject(w http.ResponseWriter, r *http.Request) {
	id, err := pathUUID(r, "project_id")
	if err != nil {
		writeInputError(w, r, "project_id 不是有效的 UUID")
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
	result, err := h.dependencies.Tenancy.UpdateProject(r.Context(), dto.UpdateProject{ID: id, Name: body.Name, Status: body.Status})
	if err != nil {
		writeApplicationError(w, r, err)
		return
	}
	httpresponse.JSON(w, http.StatusOK, writeEnvelope[projectView]{Data: viewProject(result.Project), ConfigRevision: result.Revision})
}

func (h *Handler) createVirtualKey(w http.ResponseWriter, r *http.Request) {
	projectID, err := pathUUID(r, "project_id")
	if err != nil {
		writeInputError(w, r, "project_id 不是有效的 UUID")
		return
	}
	var body struct {
		Name      string     `json:"name"`
		ExpiresAt *time.Time `json:"expires_at"`
	}
	if err := decodeJSON(w, r, &body); err != nil {
		writeInputError(w, r, "请求体不是有效的 JSON")
		return
	}
	result, err := h.dependencies.Tenancy.CreateVirtualKey(r.Context(), dto.CreateVirtualKey{ProjectID: projectID, Name: body.Name, ExpiresAt: body.ExpiresAt})
	if err != nil {
		writeApplicationError(w, r, err)
		return
	}
	view := createdVirtualKeyView{ID: result.ID, Secret: result.Secret, Prefix: result.Prefix, LastFour: result.LastFour, ExpiresAt: result.ExpiresAt}
	httpresponse.JSON(w, http.StatusCreated, writeEnvelope[createdVirtualKeyView]{Data: view, ConfigRevision: result.Revision})
}

func (h *Handler) listVirtualKeys(w http.ResponseWriter, r *http.Request) {
	projectID, err := pathUUID(r, "project_id")
	if err != nil {
		writeInputError(w, r, "project_id 不是有效的 UUID")
		return
	}
	page, err := pagination(r)
	if err != nil {
		writeInputError(w, r, "分页参数不合法")
		return
	}
	values, err := h.dependencies.Tenancy.ListVirtualKeys(r.Context(), projectID, page)
	if err != nil {
		writeApplicationError(w, r, err)
		return
	}
	views := make([]virtualKeyView, 0, len(values))
	for _, value := range values {
		views = append(views, viewVirtualKey(value))
	}
	httpresponse.JSON(w, http.StatusOK, dataEnvelope[[]virtualKeyView]{Data: views})
}

func (h *Handler) getVirtualKey(w http.ResponseWriter, r *http.Request) {
	id, err := pathUUID(r, "virtual_key_id")
	if err != nil {
		writeInputError(w, r, "virtual_key_id 不是有效的 UUID")
		return
	}
	value, err := h.dependencies.Tenancy.GetVirtualKey(r.Context(), id)
	if err != nil {
		writeApplicationError(w, r, err)
		return
	}
	httpresponse.JSON(w, http.StatusOK, dataEnvelope[virtualKeyView]{Data: viewVirtualKey(value)})
}

func (h *Handler) updateVirtualKey(w http.ResponseWriter, r *http.Request) {
	id, err := pathUUID(r, "virtual_key_id")
	if err != nil {
		writeInputError(w, r, "virtual_key_id 不是有效的 UUID")
		return
	}
	var body struct {
		Name      *string             `json:"name"`
		Status    *sharedmodel.Status `json:"status"`
		ExpiresAt *time.Time          `json:"expires_at"`
	}
	if err := decodeJSON(w, r, &body); err != nil {
		writeInputError(w, r, "请求体不是有效的 JSON")
		return
	}
	result, err := h.dependencies.Tenancy.UpdateVirtualKey(r.Context(), dto.UpdateVirtualKey{ID: id, Name: body.Name, Status: body.Status, ExpiresAt: body.ExpiresAt})
	if err != nil {
		writeApplicationError(w, r, err)
		return
	}
	httpresponse.JSON(w, http.StatusOK, writeEnvelope[virtualKeyView]{Data: viewVirtualKey(result.VirtualKey), ConfigRevision: result.Revision})
}
