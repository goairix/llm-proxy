package snapshot

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/zap"
	"gorm.io/gorm"

	gatewaysnapshot "github.com/goairix/llm-proxy/internal/application/gateway/snapshot"
)

func TestRuntimeStartsStopsWorkersAndClosesDatabaseSeparately(t *testing.T) {
	database := &fakeDatabaseRuntime{
		started: make(chan struct{}), stopped: make(chan struct{}), closeErr: errors.New("close failed"),
	}
	reader := newFakeReader(t, 1)
	store := gatewaysnapshot.NewStore()
	refresher := NewRefresher(reader, newFakeCompiler(), store, Options{PollInterval: time.Hour, LoadTimeout: time.Second}, zap.NewNop())
	runtime := NewRuntime(database, refresher, store)

	runtime.Start(context.Background())
	runtime.Start(context.Background())
	select {
	case <-database.started:
	case <-time.After(time.Second):
		t.Fatal("database worker did not start")
	}
	waitForSnapshotRevision(t, store, 1)
	if database.runCalls.Load() != 1 {
		t.Fatalf("database run calls = %d, want 1", database.runCalls.Load())
	}

	stopContext, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := runtime.Stop(stopContext); err != nil {
		t.Fatal(err)
	}
	select {
	case <-database.stopped:
	default:
		t.Fatal("database worker was not joined")
	}
	if database.closeCalls.Load() != 0 {
		t.Fatal("Stop closed the database before HTTP shutdown")
	}
	if err := runtime.CloseDatabase(); !errors.Is(err, database.closeErr) {
		t.Fatalf("CloseDatabase() error = %v", err)
	}
}

func TestRuntimeStopHonorsCallerDeadline(t *testing.T) {
	database := &fakeDatabaseRuntime{started: make(chan struct{}), stopped: make(chan struct{}), ignoreCancellation: true}
	runtime := NewRuntime(database, nil, gatewaysnapshot.NewStore())
	runtime.Start(context.Background())
	<-database.started

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := runtime.Stop(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Stop() error = %v, want context deadline exceeded", err)
	}
	close(database.release)
	joinContext, joinCancel := context.WithTimeout(context.Background(), time.Second)
	defer joinCancel()
	if err := runtime.Stop(joinContext); err != nil {
		t.Fatalf("second Stop() error = %v", err)
	}
}

type fakeDatabaseRuntime struct {
	started            chan struct{}
	stopped            chan struct{}
	release            chan struct{}
	ignoreCancellation bool
	closeErr           error
	runCalls           atomic.Int64
	closeCalls         atomic.Int64
}

func (d *fakeDatabaseRuntime) Run(ctx context.Context) {
	d.runCalls.Add(1)
	if d.release == nil {
		d.release = make(chan struct{})
	}
	close(d.started)
	if d.ignoreCancellation {
		<-d.release
	} else {
		<-ctx.Done()
	}
	close(d.stopped)
}

func (d *fakeDatabaseRuntime) Close() error {
	d.closeCalls.Add(1)
	return d.closeErr
}

func (d *fakeDatabaseRuntime) DB(context.Context) (*gorm.DB, error) { return nil, nil }
