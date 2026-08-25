package tenancy

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	sharederrors "github.com/goairix/llm-proxy/internal/domain/shared/errors"
	sharedmodel "github.com/goairix/llm-proxy/internal/domain/shared/model"
	tenantmodel "github.com/goairix/llm-proxy/internal/domain/tenancy/model"
	"github.com/goairix/llm-proxy/internal/infrastructure/config"
	"github.com/goairix/llm-proxy/internal/infrastructure/persistence/database"
	"github.com/goairix/llm-proxy/internal/infrastructure/persistence/migration"
	"github.com/goairix/llm-proxy/internal/infrastructure/persistence/transactions"
)

func TestVirtualKeyMapperRoundTrip(t *testing.T) {
	expires := time.Now().UTC().Add(time.Hour)
	key, err := tenantmodel.NewVirtualKey(uuid.Must(uuid.NewV7()), "ci", [32]byte{1, 2, 3}, "llmp_v1_abc", "1234", &expires)
	if err != nil {
		t.Fatal(err)
	}
	key.Status = sharedmodel.StatusDisabled

	got, err := virtualKeyToDomain(virtualKeyToEntity(key))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, key) {
		t.Fatalf("round trip = %+v; want %+v", got, key)
	}
}

func TestVirtualKeyMapperRejectsCorruptHash(t *testing.T) {
	record := virtualKeyToEntity(&tenantmodel.VirtualKey{})
	record.Hash = []byte{1, 2, 3}
	if _, err := virtualKeyToDomain(record); !errors.Is(err, sharederrors.ErrInvalid) {
		t.Fatalf("error = %v; want ErrInvalid", err)
	}
}

func TestTenancyRepositoriesWithPostgres(t *testing.T) {
	db := openRepositoryTestDB(t)
	manager := transactions.NewManager(staticDBProvider{db: db})
	organizations := NewOrganizationRepository(manager)
	projects := NewProjectRepository(manager)
	virtualKeys := NewVirtualKeyRepository(manager)

	organization, _ := tenantmodel.NewOrganization("Acme")
	project, _ := tenantmodel.NewProject(organization.ID, "Production")
	if err := organizations.Save(context.Background(), organization); err != nil {
		t.Fatal(err)
	}
	if err := projects.Save(context.Background(), project); err != nil {
		t.Fatal(err)
	}
	got, err := projects.FindByID(context.Background(), project.ID)
	if err != nil || got == nil || got.OrganizationID != organization.ID {
		t.Fatalf("FindByID() = %+v, %v", got, err)
	}

	hash := [32]byte{1}
	first, _ := tenantmodel.NewVirtualKey(project.ID, "first", hash, "llmp_v1_abc", "1234", nil)
	second, _ := tenantmodel.NewVirtualKey(project.ID, "second", hash, "llmp_v1_def", "5678", nil)
	if err := virtualKeys.Save(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	if err := virtualKeys.Save(context.Background(), second); !errors.Is(err, sharederrors.ErrConflict) {
		t.Fatalf("duplicate hash error = %v; want ErrConflict", err)
	}

	rolledBack, _ := tenantmodel.NewOrganization("Rollback")
	wantRollback := errors.New("force rollback")
	err = manager.Transaction(context.Background(), func(ctx context.Context) error {
		if err := organizations.Save(ctx, rolledBack); err != nil {
			return err
		}
		return wantRollback
	})
	if !errors.Is(err, wantRollback) {
		t.Fatalf("transaction error = %v", err)
	}
	if got, err := organizations.FindByID(context.Background(), rolledBack.ID); err != nil || got != nil {
		t.Fatalf("rolled back organization = %+v, %v", got, err)
	}
}

type staticDBProvider struct{ db *gorm.DB }

func (p staticDBProvider) DB(context.Context) (*gorm.DB, error) { return p.db, nil }

func openRepositoryTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := os.Getenv("LLM_PROXY_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("未设置 LLM_PROXY_TEST_POSTGRES_DSN，跳过 PostgreSQL Repository 集成测试")
	}
	parsed, err := url.Parse(dsn)
	if err != nil || parsed.Scheme == "" {
		t.Fatalf("测试 DSN 必须使用 postgres:// URL 格式")
	}
	schema := "llm_proxy_tenancy_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	base := openDatabase(t, dsn)
	if err := base.Exec(fmt.Sprintf(`CREATE SCHEMA "%s"`, schema)).Error; err != nil {
		t.Fatalf("create test schema: %v", err)
	}
	query := parsed.Query()
	query.Set("search_path", schema)
	parsed.RawQuery = query.Encode()
	db := openDatabase(t, parsed.String())
	if err := migration.Up(db); err != nil {
		t.Fatalf("migrate test schema: %v", err)
	}
	t.Cleanup(func() {
		sqlDB, _ := db.DB()
		if sqlDB != nil {
			_ = sqlDB.Close()
		}
		_ = base.Exec(fmt.Sprintf(`DROP SCHEMA "%s" CASCADE`, schema)).Error
		baseSQL, _ := base.DB()
		if baseSQL != nil {
			_ = baseSQL.Close()
		}
	})
	return db
}

func openDatabase(t *testing.T, dsn string) *gorm.DB {
	t.Helper()
	db, err := database.Open(context.Background(), config.DatabaseConfig{
		Driver:             "postgres",
		DSN:                dsn,
		MaxIdleConnections: 1,
		MaxOpenConnections: 8,
		ConnectionLifetime: time.Minute,
		ConnectTimeout:     5 * time.Second,
	})
	if err != nil {
		t.Fatalf("open test postgres: %v", err)
	}
	return db
}
