// Package snapshot manages immutable gateway snapshot publication and refresh lifecycle.
package snapshot

import (
	"context"
	"fmt"
	"sync"
	"time"

	"go.uber.org/zap"

	gatewayport "github.com/goairix/llm-proxy/internal/application/gateway/port"
	gatewaysnapshot "github.com/goairix/llm-proxy/internal/application/gateway/snapshot"
)

type Options struct {
	PollInterval time.Duration
	LoadTimeout  time.Duration
	RetryBackoff time.Duration
}

type Refresher struct {
	revisions gatewayport.RevisionReader
	loader    gatewayport.ConfigLoader
	compiler  gatewayport.SnapshotCompiler
	store     *gatewaysnapshot.Store
	options   Options
	logger    *zap.Logger
	wake      chan struct{}
	refreshMu sync.Mutex
	logMu     sync.Mutex
	lastError time.Time
}

func NewRefresher(
	reader gatewayport.RuntimeConfigReader,
	compiler gatewayport.SnapshotCompiler,
	store *gatewaysnapshot.Store,
	options Options,
	logger *zap.Logger,
) *Refresher {
	if options.PollInterval <= 0 {
		options.PollInterval = 5 * time.Second
	}
	if options.LoadTimeout <= 0 {
		options.LoadTimeout = 3 * time.Second
	}
	if options.RetryBackoff <= 0 {
		options.RetryBackoff = 5 * time.Second
	}
	if store == nil {
		store = gatewaysnapshot.NewStore()
	}
	if logger == nil {
		logger = zap.NewNop()
	}
	return &Refresher{
		revisions: reader, loader: reader, compiler: compiler, store: store,
		options: options, logger: logger, wake: make(chan struct{}, 1),
	}
}

// NotifyRefresh coalesces any number of pending wake requests without blocking callers.
func (r *Refresher) NotifyRefresh() {
	if r == nil {
		return
	}
	select {
	case r.wake <- struct{}{}:
	default:
	}
}

// Run loads immediately and then refreshes on polling ticks or local wake notifications.
func (r *Refresher) Run(ctx context.Context) {
	if r == nil {
		return
	}
	r.refreshAndLog(ctx)
	ticker := time.NewTicker(r.options.PollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.refreshAndLog(ctx)
		case <-r.wake:
			r.refreshAndLog(ctx)
		}
	}
}

// Refresh checks the committed revision and atomically publishes a newer valid snapshot.
func (r *Refresher) Refresh(ctx context.Context) error {
	if r == nil || r.revisions == nil || r.loader == nil || r.compiler == nil || r.store == nil {
		return fmt.Errorf("snapshot refresher dependencies are incomplete")
	}
	r.refreshMu.Lock()
	defer r.refreshMu.Unlock()

	checkContext, cancelCheck := context.WithTimeout(ctx, r.options.LoadTimeout)
	revision, err := r.revisions.CurrentRevision(checkContext)
	cancelCheck()
	if err != nil {
		return fmt.Errorf("read runtime revision: %w", err)
	}
	current, published := r.store.Current()
	if published && revision <= current.Revision() {
		return nil
	}

	loadContext, cancelLoad := context.WithTimeout(ctx, r.options.LoadTimeout)
	source, err := r.loader.Load(loadContext)
	cancelLoad()
	if err != nil {
		return fmt.Errorf("load runtime configuration: %w", err)
	}
	if source.Revision < revision {
		return fmt.Errorf("loaded runtime revision %d is older than observed revision %d", source.Revision, revision)
	}
	if published && source.Revision <= current.Revision() {
		return nil
	}
	next, err := r.compiler.Compile(source, time.Now().UTC())
	if err != nil {
		return fmt.Errorf("compile runtime revision %d: %w", source.Revision, err)
	}
	if next == nil || next.Revision() != source.Revision {
		return fmt.Errorf("compiler returned inconsistent runtime revision")
	}
	r.store.Publish(next)
	return nil
}

func (r *Refresher) refreshAndLog(ctx context.Context) {
	err := r.Refresh(ctx)
	if err == nil {
		r.logMu.Lock()
		r.lastError = time.Time{}
		r.logMu.Unlock()
		return
	}
	if ctx.Err() != nil {
		return
	}
	now := time.Now()
	r.logMu.Lock()
	defer r.logMu.Unlock()
	if !r.lastError.IsZero() && now.Sub(r.lastError) < r.options.RetryBackoff {
		return
	}
	r.lastError = now
	r.logger.Warn("运行时配置刷新失败，继续使用最后可用快照", zap.Error(err))
}

var _ gatewayport.RefreshNotifier = (*Refresher)(nil)
