package runtime

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	sharedmodel "github.com/goairix/llm-proxy/internal/domain/shared/model"
	"github.com/goairix/llm-proxy/internal/infrastructure/config"
	"github.com/goairix/llm-proxy/internal/infrastructure/persistence/database"
	"github.com/goairix/llm-proxy/internal/infrastructure/persistence/entity"
	"github.com/goairix/llm-proxy/internal/infrastructure/persistence/migration"
	"github.com/goairix/llm-proxy/internal/infrastructure/persistence/transactions"
)

func TestReaderPropagatesUnavailableDatabase(t *testing.T) {
	reader := NewReader(unavailableManager{})
	if _, err := reader.CurrentRevision(context.Background()); !errors.Is(err, database.ErrUnavailable) {
		t.Fatalf("CurrentRevision() error = %v, want database.ErrUnavailable", err)
	}
	if _, err := reader.Load(context.Background()); !errors.Is(err, database.ErrUnavailable) {
		t.Fatalf("Load() error = %v, want database.ErrUnavailable", err)
	}
}

func TestMapRecordsPreservesDisabledResourcesAndSealedCredential(t *testing.T) {
	records := completeRecords(t)
	records.organizations[0].Status = string(sharedmodel.StatusDisabled)

	config, err := mapRecords(records)
	if err != nil {
		t.Fatal(err)
	}
	if config.Revision != 12 || len(config.Organizations) != 1 || config.Organizations[0].Status != sharedmodel.StatusDisabled {
		t.Fatalf("source config = %+v", config)
	}
	want := records.credentials[0]
	got := config.Credentials[0].Sealed
	if got.KeyVersion != want.KeyVersion || string(got.WrappedDataKey) != string(want.WrappedDataKey) || string(got.Ciphertext) != string(want.Ciphertext) {
		t.Fatalf("sealed credential changed: %+v", got)
	}
	want.Ciphertext[0] = 99
	if got.Ciphertext[0] == 99 {
		t.Fatal("mapped source aliases entity credential bytes")
	}
}

func TestReaderLoadsCompleteConfigFromPostgres(t *testing.T) {
	db, _ := openRuntimeReaderTestDB(t)
	records := completeRecords(t)
	for _, value := range []any{
		&records.organizations, &records.projects, &records.virtualKeys, &records.providers,
		&records.credentials, &records.deployments, &records.modelAliases, &records.routeTargets,
	} {
		if err := db.Create(value).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Model(&entity.ConfigRevision{}).Where("revision = ?", 0).Updates(map[string]any{"revision": records.revision, "updated_at": time.Now().UTC()}).Error; err != nil {
		t.Fatal(err)
	}

	reader := NewReader(transactions.NewManager(postgresTestProvider{db: db}))
	current, err := reader.CurrentRevision(context.Background())
	if err != nil || current != records.revision {
		t.Fatalf("CurrentRevision() = %d, %v", current, err)
	}
	got, err := reader.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got.Revision != records.revision || len(got.Organizations) != 1 || len(got.Projects) != 1 || len(got.VirtualKeys) != 1 ||
		len(got.Providers) != 1 || len(got.Credentials) != 1 || len(got.Deployments) != 1 || len(got.ModelAliases) != 1 || len(got.RouteTargets) != 1 {
		t.Fatalf("incomplete source config: %+v", got)
	}
	if got.RouteTargets[0].DeploymentID != got.Deployments[0].ID || got.Deployments[0].UpstreamProtocol != "responses" {
		t.Fatalf("associations changed: %+v", got)
	}
}

func TestReaderLoadKeepsOneRepeatableReadViewDuringConcurrentCommit(t *testing.T) {
	readerDB, schemaDSN := openRuntimeReaderTestDB(t)
	writerDB := openRuntimeReaderDatabase(t, schemaDSN)
	t.Cleanup(func() {
		if sqlDB, err := writerDB.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})
	now := time.Now().UTC()
	first := entity.Organization{
		BaseEntity: entity.BaseEntity{ID: uuid.Must(uuid.NewV7()), CreatedAt: now, UpdatedAt: now},
		Name:       "First", Status: string(sharedmodel.StatusActive),
	}
	firstProvider := entity.Provider{
		BaseEntity: entity.BaseEntity{ID: uuid.Must(uuid.NewV7()), CreatedAt: now, UpdatedAt: now},
		Name:       "First Fake", ConnectorType: "fake", BaseURL: "", Status: string(sharedmodel.StatusActive),
	}
	firstCredential := entity.ProviderCredential{
		BaseEntity: entity.BaseEntity{ID: uuid.Must(uuid.NewV7()), CreatedAt: now, UpdatedAt: now},
		ProviderID: firstProvider.ID, ScopeKind: "platform", KeyVersion: "v1",
		WrappedKeyNonce: []byte{1}, WrappedDataKey: []byte{2}, PayloadNonce: []byte{3}, Ciphertext: []byte{4},
		Status: string(sharedmodel.StatusActive),
	}
	firstDeployment := entity.Deployment{
		BaseEntity: entity.BaseEntity{ID: uuid.Must(uuid.NewV7()), CreatedAt: now, UpdatedAt: now},
		ProviderID: firstProvider.ID, Name: "First", UpstreamModel: "fake-model", UpstreamProtocol: "fake",
		ScopeKind: "platform", Capabilities: `{"text":true}`, Status: string(sharedmodel.StatusActive),
	}
	for _, value := range []any{&first, &firstProvider, &firstCredential, &firstDeployment} {
		if err := readerDB.Create(value).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := readerDB.Model(&entity.ConfigRevision{}).Where("revision = ?", 0).Updates(map[string]any{"revision": 1, "updated_at": now}).Error; err != nil {
		t.Fatal(err)
	}

	revisionRead := make(chan struct{})
	writerDone := make(chan struct{})
	var intercepted atomic.Bool
	var writerErr error
	callbackName := "runtime_reader_repeatable_read_test"
	if err := readerDB.Callback().Query().After("gorm:query").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement.Table == "config_revisions" && intercepted.CompareAndSwap(false, true) {
			close(revisionRead)
			<-writerDone
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = readerDB.Callback().Query().Remove(callbackName) })
	go func() {
		defer close(writerDone)
		select {
		case <-revisionRead:
		case <-time.After(5 * time.Second):
			writerErr = errors.New("reader did not expose the revision query callback")
			return
		}
		second := entity.Organization{
			BaseEntity: entity.BaseEntity{ID: uuid.Must(uuid.NewV7()), CreatedAt: now, UpdatedAt: now},
			Name:       "Second", Status: string(sharedmodel.StatusActive),
		}
		secondProvider := entity.Provider{
			BaseEntity: entity.BaseEntity{ID: uuid.Must(uuid.NewV7()), CreatedAt: now, UpdatedAt: now},
			Name:       "Second OpenAI", ConnectorType: "openai", BaseURL: "https://api.openai.com", Status: string(sharedmodel.StatusActive),
		}
		secondCredential := entity.ProviderCredential{
			BaseEntity: entity.BaseEntity{ID: uuid.Must(uuid.NewV7()), CreatedAt: now, UpdatedAt: now},
			ProviderID: secondProvider.ID, ScopeKind: "platform", KeyVersion: "v1",
			WrappedKeyNonce: []byte{5}, WrappedDataKey: []byte{6}, PayloadNonce: []byte{7}, Ciphertext: []byte{8},
			Status: string(sharedmodel.StatusActive),
		}
		secondDeployment := entity.Deployment{
			BaseEntity: entity.BaseEntity{ID: uuid.Must(uuid.NewV7()), CreatedAt: now, UpdatedAt: now},
			ProviderID: secondProvider.ID, Name: "Second", UpstreamModel: "gpt-test", UpstreamProtocol: "responses",
			ScopeKind: "platform", Capabilities: `{"text":true}`, Status: string(sharedmodel.StatusActive),
		}
		writerErr = writerDB.Transaction(func(tx *gorm.DB) error {
			for _, value := range []any{&second, &secondProvider, &secondCredential, &secondDeployment} {
				if err := tx.Create(value).Error; err != nil {
					return err
				}
			}
			return tx.Model(&entity.ConfigRevision{}).Where("revision = ?", 1).Updates(map[string]any{"revision": 2, "updated_at": now}).Error
		})
	}()

	reader := NewReader(transactions.NewManager(postgresTestProvider{db: readerDB}))
	got, err := reader.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	<-writerDone
	if writerErr != nil {
		t.Fatal(writerErr)
	}
	if got.Revision != 1 || len(got.Organizations) != 1 || got.Organizations[0].ID != first.ID ||
		len(got.Providers) != 1 || got.Providers[0].ID != firstProvider.ID ||
		len(got.Credentials) != 1 || got.Credentials[0].ID != firstCredential.ID ||
		len(got.Deployments) != 1 || got.Deployments[0].ID != firstDeployment.ID {
		t.Fatalf("mixed runtime view: revision=%d organizations=%+v providers=%+v credentials=%+v deployments=%+v", got.Revision, got.Organizations, got.Providers, got.Credentials, got.Deployments)
	}
	current, err := reader.CurrentRevision(context.Background())
	if err != nil || current != 2 {
		t.Fatalf("CurrentRevision() = %d, %v; want committed revision 2", current, err)
	}
}

func completeRecords(t *testing.T) persistedConfig {
	t.Helper()
	now := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	newBase := func() entity.BaseEntity {
		return entity.BaseEntity{ID: uuid.Must(uuid.NewV7()), CreatedAt: now, UpdatedAt: now}
	}
	organization := entity.Organization{BaseEntity: newBase(), Name: "Acme", Status: string(sharedmodel.StatusActive)}
	project := entity.Project{BaseEntity: newBase(), OrganizationID: organization.ID, Name: "Production", Status: string(sharedmodel.StatusActive)}
	virtualKey := entity.VirtualKey{
		BaseEntity: newBase(), ProjectID: project.ID, Name: "ci", Hash: append([]byte(nil), makeHash()...),
		Prefix: "llmp_v1_test", LastFour: "test", Status: string(sharedmodel.StatusActive),
	}
	provider := entity.Provider{BaseEntity: newBase(), Name: "OpenAI", ConnectorType: "openai", BaseURL: "https://api.openai.com", Status: string(sharedmodel.StatusActive)}
	credential := entity.ProviderCredential{
		BaseEntity: newBase(), ProviderID: provider.ID, ScopeKind: "platform", KeyVersion: "v1",
		WrappedKeyNonce: []byte{1}, WrappedDataKey: []byte{2}, PayloadNonce: []byte{3}, Ciphertext: []byte{4, 5},
		Status: string(sharedmodel.StatusActive),
	}
	deployment := entity.Deployment{
		BaseEntity: newBase(), ProviderID: provider.ID, Name: "primary",
		UpstreamModel: "gpt-test", UpstreamProtocol: "responses", ScopeKind: "platform",
		Capabilities: `{"text":true,"streaming":true}`, Status: string(sharedmodel.StatusActive),
	}
	alias := entity.ModelAlias{BaseEntity: newBase(), ProjectID: project.ID, Name: "assistant", Status: string(sharedmodel.StatusActive)}
	target := entity.RouteTarget{
		BaseEntity: newBase(), ModelAliasID: alias.ID, DeploymentID: deployment.ID,
		Priority: 0, Weight: 100, Status: string(sharedmodel.StatusActive),
	}
	return persistedConfig{
		revision: 12, organizations: []entity.Organization{organization}, projects: []entity.Project{project},
		virtualKeys: []entity.VirtualKey{virtualKey}, providers: []entity.Provider{provider},
		credentials: []entity.ProviderCredential{credential}, deployments: []entity.Deployment{deployment},
		modelAliases: []entity.ModelAlias{alias}, routeTargets: []entity.RouteTarget{target},
	}
}

func makeHash() []byte {
	hash := make([]byte, 32)
	for index := range hash {
		hash[index] = byte(index + 1)
	}
	return hash
}

type unavailableManager struct{}

func (unavailableManager) Transaction(context.Context, func(context.Context) error) error {
	return database.ErrUnavailable
}
func (unavailableManager) ReadOnlySnapshot(context.Context, func(context.Context) error) error {
	return database.ErrUnavailable
}
func (unavailableManager) DB(context.Context) (*gorm.DB, error) { return nil, database.ErrUnavailable }

type postgresTestProvider struct{ db *gorm.DB }

func (p postgresTestProvider) DB(context.Context) (*gorm.DB, error) { return p.db, nil }

func openRuntimeReaderTestDB(t *testing.T) (*gorm.DB, string) {
	t.Helper()
	dsn := os.Getenv("LLM_PROXY_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("未设置 LLM_PROXY_TEST_POSTGRES_DSN，跳过 Runtime Reader PostgreSQL 集成测试")
	}
	parsed, err := url.Parse(dsn)
	if err != nil || parsed.Scheme == "" {
		t.Fatal("测试 DSN 必须使用 postgres:// URL 格式")
	}
	base := openRuntimeReaderDatabase(t, dsn)
	schema := "llm_proxy_runtime_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if err := base.Exec(fmt.Sprintf(`CREATE SCHEMA "%s"`, schema)).Error; err != nil {
		t.Fatal(err)
	}
	query := parsed.Query()
	query.Set("search_path", schema)
	parsed.RawQuery = query.Encode()
	db := openRuntimeReaderDatabase(t, parsed.String())
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
	return db, parsed.String()
}

func openRuntimeReaderDatabase(t *testing.T, dsn string) *gorm.DB {
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
