package snapshot

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"
	"gorm.io/gorm"

	gatewaysnapshot "github.com/goairix/llm-proxy/internal/application/gateway/snapshot"
	"github.com/goairix/llm-proxy/internal/infrastructure/config"
	"github.com/goairix/llm-proxy/internal/infrastructure/persistence/database"
	"github.com/goairix/llm-proxy/internal/infrastructure/persistence/entity"
	"github.com/goairix/llm-proxy/internal/infrastructure/persistence/migration"
	runtimerepository "github.com/goairix/llm-proxy/internal/infrastructure/persistence/repository/runtime"
	"github.com/goairix/llm-proxy/internal/infrastructure/persistence/transactions"
)

func TestIntegrationKeepsLastSnapshotAcrossPersistenceAvailabilityBoundary(t *testing.T) {
	db := openSnapshotIntegrationDatabase(t)
	seedSnapshotIntegrationConfig(t, db, 1)
	provider := &switchableDatabaseProvider{db: db, unavailableCall: make(chan struct{}, 1)}
	reader := runtimerepository.NewReader(transactions.NewManager(provider))
	store := gatewaysnapshot.NewStore()
	refresher := NewRefresher(reader, gatewaysnapshot.NewCompiler(), store, Options{
		PollInterval: time.Hour, LoadTimeout: time.Second, RetryBackoff: time.Second,
	}, zap.NewNop())
	runContext, cancelRun := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		refresher.Run(runContext)
	}()
	t.Cleanup(func() {
		cancelRun()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Error("snapshot refresher did not stop")
		}
	})

	waitForSnapshotRevision(t, store, 1)
	provider.unavailable.Store(true)
	refresher.NotifyRefresh()
	select {
	case <-provider.unavailableCall:
	case <-time.After(time.Second):
		t.Fatal("refresher did not observe database outage")
	}
	current, ok := store.Current()
	if !ok || current.Revision() != 1 {
		t.Fatalf("snapshot during outage = %v, ok = %v", current, ok)
	}

	if err := db.Model(&entity.ConfigRevision{}).Where("revision = ?", 1).Updates(map[string]any{
		"revision": 2, "updated_at": time.Now().UTC(),
	}).Error; err != nil {
		t.Fatal(err)
	}
	provider.unavailable.Store(false)
	refresher.NotifyRefresh()
	waitForSnapshotRevision(t, store, 2)
}

type switchableDatabaseProvider struct {
	db              *gorm.DB
	unavailable     atomic.Bool
	unavailableCall chan struct{}
}

func (p *switchableDatabaseProvider) DB(ctx context.Context) (*gorm.DB, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if p.unavailable.Load() {
		select {
		case p.unavailableCall <- struct{}{}:
		default:
		}
		return nil, database.ErrUnavailable
	}
	return p.db, nil
}

func seedSnapshotIntegrationConfig(t *testing.T, db *gorm.DB, revision int64) {
	t.Helper()
	now := time.Now().UTC()
	newBase := func() entity.BaseEntity {
		return entity.BaseEntity{ID: uuid.Must(uuid.NewV7()), CreatedAt: now, UpdatedAt: now}
	}
	organization := entity.Organization{BaseEntity: newBase(), Name: "Acme", Status: "active"}
	project := entity.Project{BaseEntity: newBase(), OrganizationID: organization.ID, Name: "Production", Status: "active"}
	hash := make([]byte, 32)
	hash[0] = 1
	virtualKey := entity.VirtualKey{
		BaseEntity: newBase(), ProjectID: project.ID, Name: "ci", Hash: hash,
		Prefix: "llmp_v1_test", LastFour: "test", Status: "active",
	}
	provider := entity.Provider{BaseEntity: newBase(), Name: "Fake", ConnectorType: "fake", Status: "active"}
	deployment := entity.Deployment{
		BaseEntity: newBase(), ProviderID: provider.ID, Name: "fake", UpstreamModel: "fake-model",
		UpstreamProtocol: "fake", ScopeKind: "platform", Capabilities: `{"text":true,"streaming":true}`, Status: "active",
	}
	alias := entity.ModelAlias{BaseEntity: newBase(), ProjectID: project.ID, Name: "assistant", Status: "active"}
	target := entity.RouteTarget{
		BaseEntity: newBase(), ModelAliasID: alias.ID, DeploymentID: deployment.ID,
		Priority: 0, Weight: 100, Status: "active",
	}
	for _, value := range []any{&organization, &project, &virtualKey, &provider, &deployment, &alias, &target} {
		if err := db.Create(value).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Model(&entity.ConfigRevision{}).Where("revision = ?", 0).Updates(map[string]any{
		"revision": revision, "updated_at": now,
	}).Error; err != nil {
		t.Fatal(err)
	}
}

func openSnapshotIntegrationDatabase(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := os.Getenv("LLM_PROXY_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("未设置 LLM_PROXY_TEST_POSTGRES_DSN，跳过 Snapshot PostgreSQL 纵向测试")
	}
	parsed, err := url.Parse(dsn)
	if err != nil || parsed.Scheme == "" {
		t.Fatal("测试 DSN 必须使用 postgres:// URL 格式")
	}
	base := openSnapshotDatabase(t, dsn)
	schema := "llm_proxy_snapshot_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if err := base.Exec(fmt.Sprintf(`CREATE SCHEMA "%s"`, schema)).Error; err != nil {
		t.Fatal(err)
	}
	query := parsed.Query()
	query.Set("search_path", schema)
	parsed.RawQuery = query.Encode()
	db := openSnapshotDatabase(t, parsed.String())
	if err := migration.Up(db); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
		_ = base.Exec(fmt.Sprintf(`DROP SCHEMA "%s" CASCADE`, schema)).Error
		if sqlDB, err := base.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})
	return db
}

func openSnapshotDatabase(t *testing.T, dsn string) *gorm.DB {
	t.Helper()
	db, err := database.Open(context.Background(), config.DatabaseConfig{
		Driver: "postgres", DSN: dsn, MaxIdleConnections: 1, MaxOpenConnections: 4,
		ConnectionLifetime: time.Minute, ConnectTimeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	return db
}
