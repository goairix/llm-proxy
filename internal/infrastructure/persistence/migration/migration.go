// Package migration owns fixed, ordered database schema migrations.
package migration

import (
	"errors"

	"github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

const migrationTable = "schema_migrations"

var businessTables = []string{
	"organizations",
	"projects",
	"virtual_keys",
	"providers",
	"provider_credentials",
	"deployments",
	"model_aliases",
	"route_targets",
	"config_revisions",
}

func newMigrator(db *gorm.DB) *gormigrate.Gormigrate {
	return gormigrate.New(db, &gormigrate.Options{
		TableName:                 migrationTable,
		IDColumnName:              "id",
		IDColumnSize:              255,
		UseTransaction:            true,
		ValidateUnknownMigrations: true,
	}, allMigrations())
}

// Up applies every pending migration.
func Up(db *gorm.DB) error {
	return newMigrator(db).Migrate()
}

// Down rolls back the latest applied migration.
func Down(db *gorm.DB) error {
	err := newMigrator(db).RollbackLast()
	if errors.Is(err, gormigrate.ErrNoRunMigration) {
		return nil
	}
	return err
}

// Status returns the most recently applied migration ID, or "none".
func Status(db *gorm.DB) (string, error) {
	if !db.Migrator().HasTable(migrationTable) {
		return "none", nil
	}
	var record struct {
		ID string
	}
	err := db.Table(migrationTable).Order("id DESC").First(&record).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return "none", nil
	}
	if err != nil {
		return "", err
	}
	return record.ID, nil
}
