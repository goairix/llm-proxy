package snapshot

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/zap"

	gatewaysnapshot "github.com/goairix/llm-proxy/internal/application/gateway/snapshot"
	catalogmodel "github.com/goairix/llm-proxy/internal/domain/catalog/model"
	tenancymodel "github.com/goairix/llm-proxy/internal/domain/tenancy/model"
)

func TestRefresherRunLoadsImmediatelyAndWakePublishesNewRevision(t *testing.T) {
	reader := newFakeReader(t, 1)
	compiler := newFakeCompiler()
	store := gatewaysnapshot.NewStore()
	refresher := NewRefresher(reader, compiler, store, Options{
		PollInterval: time.Hour, LoadTimeout: time.Second, RetryBackoff: time.Second,
	}, zap.NewNop())
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		refresher.Run(ctx)
	}()

	waitForSnapshotRevision(t, store, 1)
	reader.set(t, 2)
	refresher.NotifyRefresh()
	waitForSnapshotRevision(t, store, 2)

	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("refresher did not stop after context cancellation")
	}
}

func TestRefresherSkipsUnchangedRevision(t *testing.T) {
	reader := newFakeReader(t, 1)
	compiler := newFakeCompiler()
	store := gatewaysnapshot.NewStore()
	refresher := NewRefresher(reader, compiler, store, Options{LoadTimeout: time.Second}, zap.NewNop())

	if err := refresher.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := refresher.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := compiler.calls.Load(); got != 1 {
		t.Fatalf("compile calls = %d, want 1", got)
	}
	if got := reader.loadCalls.Load(); got != 1 {
		t.Fatalf("load calls = %d, want 1", got)
	}
}

func TestRefresherKeepsLastKnownGoodUntilLaterSuccess(t *testing.T) {
	reader := newFakeReader(t, 1)
	compiler := newFakeCompiler()
	store := gatewaysnapshot.NewStore()
	refresher := NewRefresher(reader, compiler, store, Options{LoadTimeout: time.Second}, zap.NewNop())
	if err := refresher.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}

	reader.set(t, 2)
	reader.setLoadError(errors.New("database offline"))
	if err := refresher.Refresh(context.Background()); err == nil {
		t.Fatal("Refresh() succeeded while load failed")
	}
	waitForSnapshotRevision(t, store, 1)

	reader.setLoadError(nil)
	compiler.setError(errors.New("invalid routing graph"))
	if err := refresher.Refresh(context.Background()); err == nil {
		t.Fatal("Refresh() succeeded while compile failed")
	}
	waitForSnapshotRevision(t, store, 1)

	compiler.setError(nil)
	if err := refresher.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	waitForSnapshotRevision(t, store, 2)
}

func TestRefresherNotifyRefreshNeverBlocksWhenWakeAlreadyPending(t *testing.T) {
	refresher := NewRefresher(newFakeReader(t, 1), newFakeCompiler(), gatewaysnapshot.NewStore(), Options{}, zap.NewNop())
	done := make(chan struct{})
	go func() {
		for range 10_000 {
			refresher.NotifyRefresh()
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("NotifyRefresh blocked on a full wake channel")
	}
}

func TestRefresherBoundsLoadWithTimeout(t *testing.T) {
	reader := newFakeReader(t, 1)
	reader.blockLoad.Store(true)
	refresher := NewRefresher(reader, newFakeCompiler(), gatewaysnapshot.NewStore(), Options{LoadTimeout: 20 * time.Millisecond}, zap.NewNop())

	started := time.Now()
	err := refresher.Refresh(context.Background())
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Refresh() error = %v, want context deadline exceeded", err)
	}
	if time.Since(started) > time.Second {
		t.Fatal("Refresh did not honor load timeout")
	}
}

type fakeReader struct {
	mu           sync.RWMutex
	revision     int64
	source       gatewaysnapshot.SourceConfig
	currentErr   error
	loadErr      error
	currentCalls atomic.Int64
	loadCalls    atomic.Int64
	blockLoad    atomic.Bool
}

func newFakeReader(t *testing.T, revision int64) *fakeReader {
	t.Helper()
	return &fakeReader{revision: revision, source: sourceForRevision(t, revision)}
}

func (r *fakeReader) CurrentRevision(context.Context) (int64, error) {
	r.currentCalls.Add(1)
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.revision, r.currentErr
}

func (r *fakeReader) Load(ctx context.Context) (gatewaysnapshot.SourceConfig, error) {
	r.loadCalls.Add(1)
	if r.blockLoad.Load() {
		<-ctx.Done()
		return gatewaysnapshot.SourceConfig{}, ctx.Err()
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.source, r.loadErr
}

func (r *fakeReader) set(t *testing.T, revision int64) {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	r.revision = revision
	r.source = sourceForRevision(t, revision)
}

func (r *fakeReader) setLoadError(err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.loadErr = err
}

type fakeCompiler struct {
	delegate *gatewaysnapshot.Compiler
	calls    atomic.Int64
	mu       sync.RWMutex
	err      error
}

func newFakeCompiler() *fakeCompiler {
	return &fakeCompiler{delegate: gatewaysnapshot.NewCompiler()}
}

func (c *fakeCompiler) Compile(source gatewaysnapshot.SourceConfig, now time.Time) (*gatewaysnapshot.RuntimeSnapshot, error) {
	c.calls.Add(1)
	c.mu.RLock()
	err := c.err
	c.mu.RUnlock()
	if err != nil {
		return nil, err
	}
	return c.delegate.Compile(source, now)
}

func (c *fakeCompiler) setError(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.err = err
}

func sourceForRevision(t *testing.T, revision int64) gatewaysnapshot.SourceConfig {
	t.Helper()
	organization, err := tenancymodel.NewOrganization("Acme")
	if err != nil {
		t.Fatal(err)
	}
	project, _ := tenancymodel.NewProject(organization.ID, "Production")
	key, _ := tenancymodel.NewVirtualKey(project.ID, "ci", [32]byte{1, byte(revision + 1)}, "llmp_v1_test", "test", nil)
	provider, _ := catalogmodel.NewProvider("Fake", "fake")
	deployment, _ := catalogmodel.NewDeployment(
		provider.ID, nil, "Fake", "fake-model", "fake", catalogmodel.Scope{Kind: catalogmodel.ScopePlatform},
		catalogmodel.CapabilitySet{Text: true, Streaming: true},
	)
	alias, _ := catalogmodel.NewModelAlias(project.ID, "assistant")
	target, _ := catalogmodel.NewRouteTarget(alias.ID, deployment.ID, 0, 100)
	return gatewaysnapshot.SourceConfig{
		Revision: revision, Organizations: []tenancymodel.Organization{*organization}, Projects: []tenancymodel.Project{*project},
		VirtualKeys: []tenancymodel.VirtualKey{*key}, Providers: []catalogmodel.Provider{*provider},
		Deployments: []catalogmodel.Deployment{*deployment}, ModelAliases: []catalogmodel.ModelAlias{*alias},
		RouteTargets: []catalogmodel.RouteTarget{*target},
	}
}

func waitForSnapshotRevision(t *testing.T, store *gatewaysnapshot.Store, revision int64) {
	t.Helper()
	deadline := time.NewTimer(time.Second)
	ticker := time.NewTicker(time.Millisecond)
	defer deadline.Stop()
	defer ticker.Stop()
	for {
		if current, ok := store.Current(); ok && current.Revision() == revision {
			return
		}
		select {
		case <-deadline.C:
			current, _ := store.Current()
			t.Fatalf("snapshot = %v, want revision %d", current, revision)
		case <-ticker.C:
		}
	}
}
