package catalog

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	catalogmodel "github.com/goairix/llm-proxy/internal/domain/catalog/model"
	sharederrors "github.com/goairix/llm-proxy/internal/domain/shared/errors"
	tenantmodel "github.com/goairix/llm-proxy/internal/domain/tenancy/model"
	"github.com/goairix/llm-proxy/internal/infrastructure/config"
	"github.com/goairix/llm-proxy/internal/infrastructure/persistence/database"
	"github.com/goairix/llm-proxy/internal/infrastructure/persistence/migration"
	tenantrepo "github.com/goairix/llm-proxy/internal/infrastructure/persistence/repository/tenancy"
	"github.com/goairix/llm-proxy/internal/infrastructure/persistence/transactions"
)

func TestCredentialMapperPreservesEncryptedBytes(t *testing.T) {
	sealed := catalogmodel.SealedCredential{
		KeyVersion:      "v1",
		WrappedKeyNonce: []byte{1, 2},
		WrappedDataKey:  []byte{3, 4},
		PayloadNonce:    []byte{5, 6},
		Ciphertext:      []byte{7, 8, 9},
	}
	credential, err := catalogmodel.NewProviderCredential(uuid.Must(uuid.NewV7()), catalogmodel.Scope{Kind: catalogmodel.ScopePlatform}, sealed)
	if err != nil {
		t.Fatal(err)
	}
	got, err := credentialToDomain(credentialToEntity(credential))
	if err != nil {
		t.Fatal(err)
	}
	if string(got.Sealed.WrappedKeyNonce) != string(sealed.WrappedKeyNonce) ||
		string(got.Sealed.WrappedDataKey) != string(sealed.WrappedDataKey) ||
		string(got.Sealed.PayloadNonce) != string(sealed.PayloadNonce) ||
		string(got.Sealed.Ciphertext) != string(sealed.Ciphertext) {
		t.Fatalf("sealed credential changed: %+v", got.Sealed)
	}
}

func TestDeploymentMapperPreservesCapabilities(t *testing.T) {
	capabilities := catalogmodel.CapabilitySet{Text: true, Tools: true, Streaming: true}
	deployment, err := catalogmodel.NewDeployment(uuid.Must(uuid.NewV7()), nil, "fake", "fake-model", "fake", catalogmodel.Scope{Kind: catalogmodel.ScopePlatform}, capabilities)
	if err != nil {
		t.Fatal(err)
	}
	record, err := deploymentToEntity(deployment)
	if err != nil {
		t.Fatal(err)
	}
	got, err := deploymentToDomain(record)
	if err != nil {
		t.Fatal(err)
	}
	if got.Capabilities != capabilities {
		t.Fatalf("capabilities = %+v; want %+v", got.Capabilities, capabilities)
	}
}

func TestCatalogRepositoriesWithPostgres(t *testing.T) {
	db := openCatalogRepositoryTestDB(t)
	manager := transactions.NewManager(catalogStaticDBProvider{db: db})
	organizations := tenantrepo.NewOrganizationRepository(manager)
	projects := tenantrepo.NewProjectRepository(manager)
	providers := NewProviderRepository(manager)
	credentials := NewProviderCredentialRepository(manager)
	aliases := NewModelAliasRepository(manager)
	targets := NewRouteTargetRepository(manager)
	deployments := NewDeploymentRepository(manager)
	revisions := NewConfigRevisionRepository(manager)

	organization, _ := tenantmodel.NewOrganization("Acme")
	project, _ := tenantmodel.NewProject(organization.ID, "Production")
	provider, _ := catalogmodel.NewProvider("Fake", "fake")
	deployment, _ := catalogmodel.NewDeployment(provider.ID, nil, "fake", "fake-model", "fake", catalogmodel.Scope{Kind: catalogmodel.ScopePlatform}, catalogmodel.CapabilitySet{Text: true})
	credential, _ := catalogmodel.NewProviderCredential(provider.ID, catalogmodel.Scope{Kind: catalogmodel.ScopePlatform}, catalogmodel.SealedCredential{
		KeyVersion: "v1", WrappedKeyNonce: []byte{1}, WrappedDataKey: []byte{2}, PayloadNonce: []byte{3}, Ciphertext: []byte{4},
	})
	credentialID := credential.ID
	credentialDeployment, _ := catalogmodel.NewDeployment(provider.ID, &credentialID, "credential", "credential-model", "fake", catalogmodel.Scope{Kind: catalogmodel.ScopePlatform}, catalogmodel.CapabilitySet{Text: true})
	alias, _ := catalogmodel.NewModelAlias(project.ID, "assistant")
	target, _ := catalogmodel.NewRouteTarget(alias.ID, deployment.ID, 0, 100)
	ctx := context.Background()
	for _, save := range []func() error{
		func() error { return organizations.Save(ctx, organization) },
		func() error { return projects.Save(ctx, project) },
		func() error { return providers.Save(ctx, provider) },
		func() error { return credentials.Save(ctx, credential) },
		func() error { return deployments.Save(ctx, deployment) },
		func() error { return deployments.Save(ctx, credentialDeployment) },
		func() error { return aliases.Save(ctx, alias) },
		func() error { return targets.Save(ctx, target) },
	} {
		if err := save(); err != nil {
			t.Fatal(err)
		}
	}
	if got, err := targets.ListByModelAlias(ctx, alias.ID); err != nil || len(got) != 1 || got[0].DeploymentID != deployment.ID {
		t.Fatalf("targets = %+v, %v", got, err)
	}
	if got, err := deployments.HasActiveByProvider(ctx, provider.ID); err != nil || !got {
		t.Fatalf("active deployment by provider = %v, %v", got, err)
	}
	if got, err := deployments.HasActiveByCredential(ctx, credential.ID); err != nil || !got {
		t.Fatalf("active deployment by credential = %v, %v", got, err)
	}
	if got, err := targets.HasActiveByDeployment(ctx, deployment.ID); err != nil || !got {
		t.Fatalf("active target by deployment = %v, %v", got, err)
	}
	duplicate, _ := catalogmodel.NewModelAlias(project.ID, "assistant")
	if err := aliases.Save(ctx, duplicate); !errors.Is(err, sharederrors.ErrConflict) {
		t.Fatalf("duplicate alias error = %v; want ErrConflict", err)
	}

	start, err := revisions.Current(ctx)
	if err != nil {
		t.Fatal(err)
	}
	const workers = 8
	var wg sync.WaitGroup
	errs := make(chan error, workers)
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- manager.Transaction(ctx, func(txCtx context.Context) error {
				_, err := revisions.Next(txCtx)
				return err
			})
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	current, err := revisions.Current(ctx)
	if err != nil || current != start+workers {
		t.Fatalf("revision = %d, %v; want %d", current, err, start+workers)
	}
}

type catalogStaticDBProvider struct{ db *gorm.DB }

func (p catalogStaticDBProvider) DB(context.Context) (*gorm.DB, error) { return p.db, nil }

func openCatalogRepositoryTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := os.Getenv("LLM_PROXY_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("未设置 LLM_PROXY_TEST_POSTGRES_DSN，跳过 PostgreSQL Repository 集成测试")
	}
	parsed, err := url.Parse(dsn)
	if err != nil || parsed.Scheme == "" {
		t.Fatalf("测试 DSN 必须使用 postgres:// URL 格式")
	}
	schema := "llm_proxy_catalog_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	base := openCatalogDatabase(t, dsn)
	if err := base.Exec(fmt.Sprintf(`CREATE SCHEMA "%s"`, schema)).Error; err != nil {
		t.Fatalf("create test schema: %v", err)
	}
	query := parsed.Query()
	query.Set("search_path", schema)
	parsed.RawQuery = query.Encode()
	db := openCatalogDatabase(t, parsed.String())
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

func openCatalogDatabase(t *testing.T, dsn string) *gorm.DB {
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
