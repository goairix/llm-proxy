package migration

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/goairix/llm-proxy/internal/infrastructure/config"
	"github.com/goairix/llm-proxy/internal/infrastructure/persistence/database"
	"github.com/goairix/llm-proxy/internal/infrastructure/persistence/entity"
)

func TestInitialMigrationID(t *testing.T) {
	migrations := allMigrations()
	if len(migrations) != 1 || migrations[0].ID != InitialMigrationID {
		t.Fatalf("migrations = %+v", migrations)
	}
	if InitialMigrationID != "2026082501_initial_control_plane" {
		t.Fatalf("InitialMigrationID = %q", InitialMigrationID)
	}
}

func TestPostgresInitialMigration(t *testing.T) {
	dsn := os.Getenv("LLM_PROXY_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("未设置 LLM_PROXY_TEST_POSTGRES_DSN，跳过 PostgreSQL 迁移集成测试")
	}

	db, err := database.Open(context.Background(), config.DatabaseConfig{
		Driver:             "postgres",
		DSN:                dsn,
		MaxIdleConnections: 1,
		MaxOpenConnections: 2,
		ConnectionLifetime: time.Minute,
		ConnectTimeout:     5 * time.Second,
	})
	if err != nil {
		t.Fatalf("open test postgres: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })

	_ = Down(db)
	if err := Up(db); err != nil {
		t.Fatalf("Up() error = %v", err)
	}

	for _, table := range businessTables {
		if !db.Migrator().HasTable(table) {
			t.Errorf("table %q was not created", table)
		}
	}
	var revision entity.ConfigRevision
	if err := db.First(&revision).Error; err != nil {
		t.Fatalf("read base revision: %v", err)
	}
	if revision.ID.Version() != uuid.Version(7) || revision.Revision != 0 {
		t.Fatalf("base revision = %+v; want UUIDv7 revision 0", revision)
	}
	assertModelColumnType(t, db, &entity.Organization{}, "id", "uuid")
	assertModelColumnType(t, db, &entity.VirtualKey{}, "hash", "bytea")
	assertModelColumnType(t, db, &entity.Deployment{}, "capabilities", "text")

	if err := Down(db); err != nil {
		t.Fatalf("Down() error = %v", err)
	}
	for _, table := range businessTables {
		if db.Migrator().HasTable(table) {
			t.Errorf("table %q remains after Down", table)
		}
	}
}

func assertModelColumnType(t *testing.T, db *gorm.DB, model any, name, want string) {
	t.Helper()
	columns, err := db.Migrator().ColumnTypes(model)
	if err != nil {
		t.Fatalf("read column types: %v", err)
	}
	for _, column := range columns {
		if column.Name() == name {
			if got := strings.ToLower(column.DatabaseTypeName()); got != want {
				t.Fatalf("column %s type = %q; want %q", name, got, want)
			}
			return
		}
	}
	t.Fatalf("column %q not found", name)
}
