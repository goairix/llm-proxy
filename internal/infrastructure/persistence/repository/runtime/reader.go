// Package runtime implements the cross-aggregate gateway configuration reader.
package runtime

import (
	"context"
	"fmt"

	gatewayport "github.com/goairix/llm-proxy/internal/application/gateway/port"
	gatewaysnapshot "github.com/goairix/llm-proxy/internal/application/gateway/snapshot"
	"github.com/goairix/llm-proxy/internal/infrastructure/persistence/entity"
	catalogrepository "github.com/goairix/llm-proxy/internal/infrastructure/persistence/repository/catalog"
	tenancyrepository "github.com/goairix/llm-proxy/internal/infrastructure/persistence/repository/tenancy"
	"github.com/goairix/llm-proxy/internal/infrastructure/persistence/transactions"
)

// Reader loads one complete runtime configuration through the persistence abstraction.
type Reader struct {
	manager transactions.DBManager
}

func NewReader(manager transactions.DBManager) *Reader {
	return &Reader{manager: manager}
}

func (r *Reader) CurrentRevision(ctx context.Context) (int64, error) {
	db, err := r.manager.DB(ctx)
	if err != nil {
		return 0, err
	}
	var record entity.ConfigRevision
	if err := db.WithContext(ctx).Select("revision").Order("revision DESC").First(&record).Error; err != nil {
		return 0, fmt.Errorf("read current config revision: %w", err)
	}
	return record.Revision, nil
}

func (r *Reader) Load(ctx context.Context) (gatewaysnapshot.SourceConfig, error) {
	var records persistedConfig
	err := r.manager.ReadOnlySnapshot(ctx, func(snapshotContext context.Context) error {
		db, err := r.manager.DB(snapshotContext)
		if err != nil {
			return err
		}
		query := db.WithContext(snapshotContext)
		var revision entity.ConfigRevision
		if err := query.Select("revision").Order("revision DESC").First(&revision).Error; err != nil {
			return fmt.Errorf("read snapshot config revision: %w", err)
		}
		records.revision = revision.Revision
		loads := []struct {
			name   string
			target any
		}{
			{name: "organizations", target: &records.organizations},
			{name: "projects", target: &records.projects},
			{name: "virtual keys", target: &records.virtualKeys},
			{name: "providers", target: &records.providers},
			{name: "provider credentials", target: &records.credentials},
			{name: "deployments", target: &records.deployments},
			{name: "model aliases", target: &records.modelAliases},
			{name: "route targets", target: &records.routeTargets},
		}
		for _, load := range loads {
			if err := query.Order("id ASC").Find(load.target).Error; err != nil {
				return fmt.Errorf("read runtime %s: %w", load.name, err)
			}
		}
		return nil
	})
	if err != nil {
		return gatewaysnapshot.SourceConfig{}, err
	}
	return mapRecords(records)
}

type persistedConfig struct {
	revision      int64
	organizations []entity.Organization
	projects      []entity.Project
	virtualKeys   []entity.VirtualKey
	providers     []entity.Provider
	credentials   []entity.ProviderCredential
	deployments   []entity.Deployment
	modelAliases  []entity.ModelAlias
	routeTargets  []entity.RouteTarget
}

func mapRecords(records persistedConfig) (gatewaysnapshot.SourceConfig, error) {
	result := gatewaysnapshot.SourceConfig{Revision: records.revision}
	for index := range records.organizations {
		value, err := tenancyrepository.OrganizationFromEntity(&records.organizations[index])
		if err != nil {
			return gatewaysnapshot.SourceConfig{}, err
		}
		result.Organizations = append(result.Organizations, *value)
	}
	for index := range records.projects {
		value, err := tenancyrepository.ProjectFromEntity(&records.projects[index])
		if err != nil {
			return gatewaysnapshot.SourceConfig{}, err
		}
		result.Projects = append(result.Projects, *value)
	}
	for index := range records.virtualKeys {
		value, err := tenancyrepository.VirtualKeyFromEntity(&records.virtualKeys[index])
		if err != nil {
			return gatewaysnapshot.SourceConfig{}, err
		}
		result.VirtualKeys = append(result.VirtualKeys, *value)
	}
	for index := range records.providers {
		value, err := catalogrepository.ProviderFromEntity(&records.providers[index])
		if err != nil {
			return gatewaysnapshot.SourceConfig{}, err
		}
		result.Providers = append(result.Providers, *value)
	}
	for index := range records.credentials {
		value, err := catalogrepository.ProviderCredentialFromEntity(&records.credentials[index])
		if err != nil {
			return gatewaysnapshot.SourceConfig{}, err
		}
		result.Credentials = append(result.Credentials, *value)
	}
	for index := range records.deployments {
		value, err := catalogrepository.DeploymentFromEntity(&records.deployments[index])
		if err != nil {
			return gatewaysnapshot.SourceConfig{}, err
		}
		result.Deployments = append(result.Deployments, *value)
	}
	for index := range records.modelAliases {
		value, err := catalogrepository.ModelAliasFromEntity(&records.modelAliases[index])
		if err != nil {
			return gatewaysnapshot.SourceConfig{}, err
		}
		result.ModelAliases = append(result.ModelAliases, *value)
	}
	for index := range records.routeTargets {
		value, err := catalogrepository.RouteTargetFromEntity(&records.routeTargets[index])
		if err != nil {
			return gatewaysnapshot.SourceConfig{}, err
		}
		result.RouteTargets = append(result.RouteTargets, *value)
	}
	return result, nil
}

var _ gatewayport.RuntimeConfigReader = (*Reader)(nil)
