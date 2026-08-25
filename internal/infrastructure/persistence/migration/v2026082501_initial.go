package migration

import (
	"fmt"
	"time"

	"github.com/go-gormigrate/gormigrate/v2"
	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/goairix/llm-proxy/internal/infrastructure/persistence/entity"
)

const InitialMigrationID = "2026082501_initial_control_plane"

func allMigrations() []*gormigrate.Migration {
	return []*gormigrate.Migration{
		{
			ID:       InitialMigrationID,
			Migrate:  migrateInitialControlPlane,
			Rollback: rollbackInitialControlPlane,
		},
	}
}

func migrateInitialControlPlane(db *gorm.DB) error {
	models := []any{
		&entity.Organization{},
		&entity.Project{},
		&entity.VirtualKey{},
		&entity.Provider{},
		&entity.ProviderCredential{},
		&entity.Deployment{},
		&entity.ModelAlias{},
		&entity.RouteTarget{},
		&entity.ConfigRevision{},
	}
	for _, record := range models {
		if err := db.Migrator().CreateTable(record); err != nil {
			return fmt.Errorf("create control plane table: %w", err)
		}
	}

	id, err := uuid.NewV7()
	if err != nil {
		return fmt.Errorf("generate base revision uuidv7: %w", err)
	}
	now := time.Now().UTC()
	base := entity.ConfigRevision{
		BaseEntity: entity.BaseEntity{ID: id, CreatedAt: now, UpdatedAt: now},
		Revision:   0,
	}
	if err := db.Create(&base).Error; err != nil {
		return fmt.Errorf("create base config revision: %w", err)
	}
	return nil
}

func rollbackInitialControlPlane(db *gorm.DB) error {
	models := []any{
		&entity.ConfigRevision{},
		&entity.RouteTarget{},
		&entity.ModelAlias{},
		&entity.Deployment{},
		&entity.ProviderCredential{},
		&entity.Provider{},
		&entity.VirtualKey{},
		&entity.Project{},
		&entity.Organization{},
	}
	for _, record := range models {
		if err := db.Migrator().DropTable(record); err != nil {
			return fmt.Errorf("drop control plane table: %w", err)
		}
	}
	return nil
}
