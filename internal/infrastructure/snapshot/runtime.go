package snapshot

import (
	"context"
	"sync"

	gatewaysnapshot "github.com/goairix/llm-proxy/internal/application/gateway/snapshot"
)

type databaseRuntime interface {
	Run(context.Context)
	Close() error
}

// Runtime owns database reconnect and snapshot refresh workers without owning HTTP shutdown.
type Runtime struct {
	database  databaseRuntime
	refresher *Refresher
	store     *gatewaysnapshot.Store

	startOnce sync.Once
	stateMu   sync.RWMutex
	started   bool
	cancel    context.CancelFunc
	wait      sync.WaitGroup
	done      chan struct{}
}

func NewRuntime(database databaseRuntime, refresher *Refresher, store *gatewaysnapshot.Store) *Runtime {
	if store == nil {
		store = gatewaysnapshot.NewStore()
	}
	return &Runtime{database: database, refresher: refresher, store: store, done: make(chan struct{})}
}

func (r *Runtime) Start(parent context.Context) {
	if r == nil {
		return
	}
	r.startOnce.Do(func() {
		runContext, cancel := context.WithCancel(parent)
		r.stateMu.Lock()
		r.started = true
		r.cancel = cancel
		r.stateMu.Unlock()
		if r.database != nil {
			r.wait.Add(1)
			go func() {
				defer r.wait.Done()
				r.database.Run(runContext)
			}()
		}
		if r.refresher != nil {
			r.wait.Add(1)
			go func() {
				defer r.wait.Done()
				r.refresher.Run(runContext)
			}()
		}
		go func() {
			r.wait.Wait()
			close(r.done)
		}()
	})
}

// NotifyRefresh requests a non-blocking local refresh after a control-plane commit.
func (r *Runtime) NotifyRefresh() {
	if r != nil && r.refresher != nil {
		r.refresher.NotifyRefresh()
	}
}

// Stop cancels and joins runtime workers but intentionally leaves the database open.
func (r *Runtime) Stop(ctx context.Context) error {
	if r == nil {
		return nil
	}
	r.stateMu.RLock()
	started, cancel := r.started, r.cancel
	r.stateMu.RUnlock()
	if !started {
		return nil
	}
	cancel()
	select {
	case <-r.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// CloseDatabase releases the connection pool after HTTP shutdown is complete.
func (r *Runtime) CloseDatabase() error {
	if r == nil || r.database == nil {
		return nil
	}
	return r.database.Close()
}

func (r *Runtime) Store() *gatewaysnapshot.Store {
	if r == nil {
		return nil
	}
	return r.store
}
