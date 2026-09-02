package migration

import (
	"fmt"

	"github.com/go-gormigrate/gormigrate/v2"
	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/goairix/llm-proxy/internal/infrastructure/persistence/entity"
)

const ProviderConnectorMigrationID = "2026082601_provider_connectors"

func providerConnectorMigration() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: ProviderConnectorMigrationID, Migrate: migrateProviderConnectors, Rollback: rollbackProviderConnectors,
	}
}

func migrateProviderConnectors(db *gorm.DB) error {
	var legacy int64
	if err := db.Table("providers").Where("connector_type <> ?", "fake").Count(&legacy).Error; err != nil {
		return fmt.Errorf("count legacy providers: %w", err)
	}
	if legacy != 0 {
		return fmt.Errorf("检测到 %d 条未支持的非 Fake 旧 Provider，请先清理后再迁移", legacy)
	}
	if !db.Migrator().HasColumn(&entity.Provider{}, "BaseURL") {
		if err := db.Migrator().AddColumn(&entity.Provider{}, "BaseURL"); err != nil {
			return fmt.Errorf("add providers.base_url: %w", err)
		}
	}
	if !db.Migrator().HasColumn(&entity.Deployment{}, "UpstreamProtocol") {
		if err := db.Migrator().AddColumn(&entity.Deployment{}, "UpstreamProtocol"); err != nil {
			return fmt.Errorf("add deployments.upstream_protocol: %w", err)
		}
	}
	if err := db.Table("deployments").Where("upstream_protocol = ''").Update("upstream_protocol", "fake").Error; err != nil {
		return fmt.Errorf("backfill deployment upstream protocol: %w", err)
	}
	if !db.Migrator().HasIndex(&entity.Deployment{}, "idx_deployments_upstream_protocol") {
		if err := db.Migrator().CreateIndex(&entity.Deployment{}, "UpstreamProtocol"); err != nil {
			return fmt.Errorf("create deployment upstream protocol index: %w", err)
		}
	}
	for _, column := range []string{"credential_id", "connector_type"} {
		if db.Migrator().HasColumn("deployments", column) {
			if err := db.Migrator().DropColumn("deployments", column); err != nil {
				return fmt.Errorf("drop deployments.%s: %w", column, err)
			}
		}
	}
	return nil
}

type legacyDeploymentColumns struct {
	ConnectorType string     `gorm:"column:connector_type;type:varchar(64);not null;default:''"`
	CredentialID  *uuid.UUID `gorm:"column:credential_id;type:uuid"`
}

func (legacyDeploymentColumns) TableName() string { return "deployments" }

func rollbackProviderConnectors(db *gorm.DB) error {
	if !db.Migrator().HasColumn("deployments", "connector_type") {
		if err := db.Migrator().AddColumn(&legacyDeploymentColumns{}, "ConnectorType"); err != nil {
			return fmt.Errorf("restore deployments.connector_type: %w", err)
		}
	}
	if err := db.Exec(`UPDATE deployments AS d SET connector_type = p.connector_type FROM providers AS p WHERE p.id = d.provider_id`).Error; err != nil {
		return fmt.Errorf("restore deployment connector type values: %w", err)
	}
	if !db.Migrator().HasColumn("deployments", "credential_id") {
		if err := db.Migrator().AddColumn(&legacyDeploymentColumns{}, "CredentialID"); err != nil {
			return fmt.Errorf("restore deployments.credential_id: %w", err)
		}
	}
	if db.Migrator().HasColumn(&entity.Deployment{}, "UpstreamProtocol") {
		if err := db.Migrator().DropColumn(&entity.Deployment{}, "UpstreamProtocol"); err != nil {
			return fmt.Errorf("drop deployments.upstream_protocol: %w", err)
		}
	}
	if db.Migrator().HasColumn(&entity.Provider{}, "BaseURL") {
		if err := db.Migrator().DropColumn(&entity.Provider{}, "BaseURL"); err != nil {
			return fmt.Errorf("drop providers.base_url: %w", err)
		}
	}
	return nil
}
