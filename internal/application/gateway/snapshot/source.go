package snapshot

import (
	catalogmodel "github.com/goairix/llm-proxy/internal/domain/catalog/model"
	tenancymodel "github.com/goairix/llm-proxy/internal/domain/tenancy/model"
)

// SourceConfig is one consistent cross-aggregate control-plane view.
type SourceConfig struct {
	Revision      int64
	Organizations []tenancymodel.Organization
	Projects      []tenancymodel.Project
	VirtualKeys   []tenancymodel.VirtualKey
	Providers     []catalogmodel.Provider
	Credentials   []catalogmodel.ProviderCredential
	Deployments   []catalogmodel.Deployment
	ModelAliases  []catalogmodel.ModelAlias
	RouteTargets  []catalogmodel.RouteTarget
}
