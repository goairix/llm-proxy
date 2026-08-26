package controlplane

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"

	"github.com/google/uuid"

	"github.com/goairix/llm-proxy/internal/application/controlplane/dto"
	controlerrors "github.com/goairix/llm-proxy/internal/application/controlplane/errors"
	catalogmodel "github.com/goairix/llm-proxy/internal/domain/catalog/model"
	tenantmodel "github.com/goairix/llm-proxy/internal/domain/tenancy/model"
	httpresponse "github.com/goairix/llm-proxy/internal/interfaces/http/response"
)

const maxRequestBodyBytes = 1 << 20

// ResourcePatterns are the exact control-plane resource shapes mounted by the root router.
var ResourcePatterns = []string{
	"/v1/organizations",
	"/v1/organizations/{organization_id}",
	"/v1/organizations/{organization_id}/projects",
	"/v1/projects/{project_id}",
	"/v1/projects/{project_id}/virtual-keys",
	"/v1/virtual-keys/{virtual_key_id}",
	"/v1/providers",
	"/v1/providers/{provider_id}",
	"/v1/provider-credentials",
	"/v1/provider-credentials/{credential_id}",
	"/v1/deployments",
	"/v1/deployments/{deployment_id}",
	"/v1/model-aliases",
	"/v1/model-aliases/{model_alias_id}",
	"/v1/model-aliases/{model_alias_id}/route-targets",
	"/v1/route-targets/{route_target_id}",
}

type TenancyService interface {
	CreateOrganization(context.Context, dto.CreateOrganization) (dto.OrganizationResult, error)
	GetOrganization(context.Context, uuid.UUID) (*tenantmodel.Organization, error)
	ListOrganizations(context.Context, dto.Pagination) ([]tenantmodel.Organization, error)
	UpdateOrganization(context.Context, dto.UpdateOrganization) (dto.OrganizationResult, error)
	CreateProject(context.Context, dto.CreateProject) (dto.ProjectResult, error)
	GetProject(context.Context, uuid.UUID) (*tenantmodel.Project, error)
	ListProjects(context.Context, uuid.UUID, dto.Pagination) ([]tenantmodel.Project, error)
	UpdateProject(context.Context, dto.UpdateProject) (dto.ProjectResult, error)
	CreateVirtualKey(context.Context, dto.CreateVirtualKey) (dto.CreateVirtualKeyResult, error)
	GetVirtualKey(context.Context, uuid.UUID) (dto.VirtualKeyView, error)
	ListVirtualKeys(context.Context, uuid.UUID, dto.Pagination) ([]dto.VirtualKeyView, error)
	UpdateVirtualKey(context.Context, dto.UpdateVirtualKey) (dto.VirtualKeyResult, error)
}

type CatalogService interface {
	CreateProvider(context.Context, dto.CreateProvider) (dto.ProviderResult, error)
	GetProvider(context.Context, uuid.UUID) (*catalogmodel.Provider, error)
	ListProviders(context.Context, dto.Pagination) ([]catalogmodel.Provider, error)
	UpdateProvider(context.Context, dto.UpdateProvider) (dto.ProviderResult, error)
	CreateProviderCredential(context.Context, dto.CreateProviderCredential) (dto.ProviderCredentialResult, error)
	GetProviderCredential(context.Context, uuid.UUID) (dto.ProviderCredentialResult, error)
	ListProviderCredentials(context.Context, dto.Pagination) ([]dto.ProviderCredentialResult, error)
	UpdateProviderCredential(context.Context, dto.UpdateProviderCredential) (dto.ProviderCredentialResult, error)
	CreateDeployment(context.Context, dto.CreateDeployment) (dto.DeploymentResult, error)
	GetDeployment(context.Context, uuid.UUID) (*catalogmodel.Deployment, error)
	ListDeployments(context.Context, dto.Pagination) ([]catalogmodel.Deployment, error)
	UpdateDeployment(context.Context, dto.UpdateDeployment) (dto.DeploymentResult, error)
	CreateModelAlias(context.Context, dto.CreateModelAlias) (dto.ModelAliasResult, error)
	GetModelAlias(context.Context, uuid.UUID) (*catalogmodel.ModelAlias, error)
	ListModelAliases(context.Context, dto.Pagination) ([]catalogmodel.ModelAlias, error)
	UpdateModelAlias(context.Context, dto.UpdateModelAlias) (dto.ModelAliasResult, error)
	CreateRouteTarget(context.Context, dto.CreateRouteTarget) (dto.RouteTargetResult, error)
	GetRouteTarget(context.Context, uuid.UUID) (*catalogmodel.RouteTarget, error)
	ListRouteTargets(context.Context, uuid.UUID) ([]catalogmodel.RouteTarget, error)
	UpdateRouteTarget(context.Context, dto.UpdateRouteTarget) (dto.RouteTargetResult, error)
}

type Dependencies struct {
	Tenancy TenancyService
	Catalog CatalogService
}

type Handler struct {
	dependencies Dependencies
	mux          *http.ServeMux
}

func New(dependencies Dependencies) *Handler {
	handler := &Handler{dependencies: dependencies, mux: http.NewServeMux()}
	handler.registerTenancyRoutes()
	handler.registerCatalogRoutes()
	return handler
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.mux.ServeHTTP(w, r)
}

func decodeJSON(w http.ResponseWriter, r *http.Request, destination any) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodyBytes)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("request body must contain one JSON value")
		}
		return err
	}
	return nil
}

func pathUUID(r *http.Request, name string) (uuid.UUID, error) {
	return uuid.Parse(r.PathValue(name))
}

func pagination(r *http.Request) (dto.Pagination, error) {
	result := dto.Pagination{Limit: 50}
	var err error
	if value := r.URL.Query().Get("limit"); value != "" {
		result.Limit, err = strconv.Atoi(value)
		if err != nil || result.Limit < 1 || result.Limit > 100 {
			return dto.Pagination{}, errors.New("limit must be between 1 and 100")
		}
	}
	if value := r.URL.Query().Get("offset"); value != "" {
		result.Offset, err = strconv.Atoi(value)
		if err != nil || result.Offset < 0 {
			return dto.Pagination{}, errors.New("offset must be non-negative")
		}
	}
	return result, nil
}

func writeInputError(w http.ResponseWriter, r *http.Request, message string) {
	httpresponse.Error(w, http.StatusBadRequest, string(controlerrors.InvalidRequest), message, httpresponse.RequestID(r.Context()))
}

func writeApplicationError(w http.ResponseWriter, r *http.Request, err error) {
	applicationError := &controlerrors.Error{Code: controlerrors.Internal, SafeMessage: "服务内部错误"}
	var candidate *controlerrors.Error
	if errors.As(err, &candidate) {
		applicationError = candidate
	}
	status := http.StatusInternalServerError
	switch applicationError.Code {
	case controlerrors.InvalidRequest:
		status = http.StatusBadRequest
	case controlerrors.AuthenticationFailed:
		status = http.StatusUnauthorized
	case controlerrors.PermissionDenied:
		status = http.StatusForbidden
	case controlerrors.NotFound:
		status = http.StatusNotFound
	case controlerrors.Conflict:
		status = http.StatusConflict
	case controlerrors.DependencyUnavailable:
		status = http.StatusServiceUnavailable
	}
	httpresponse.Error(w, status, string(applicationError.Code), applicationError.SafeMessage, httpresponse.RequestID(r.Context()))
}

func registerMethodFallback(mux *http.ServeMux, pattern, allow string) {
	mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Allow", allow)
		httpresponse.Error(w, http.StatusMethodNotAllowed, "method_not_allowed", "请求方法不支持", httpresponse.RequestID(r.Context()))
	})
}

type dataEnvelope[T any] struct {
	Data T `json:"data"`
}

type writeEnvelope[T any] struct {
	Data           T     `json:"data"`
	ConfigRevision int64 `json:"config_revision"`
}
