package database

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/zap"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestRuntimeStartsUnavailableAndRecovers(t *testing.T) {
	db := newTestGORM(t)
	var attempts atomic.Int32
	runtime := NewRuntime(func(context.Context) (*gorm.DB, error) {
		if attempts.Add(1) == 1 {
			return nil, errors.New("database offline")
		}
		return db, nil
	}, time.Millisecond, zap.NewNop())

	if runtime.Available() {
		t.Fatal("new runtime is available")
	}
	if _, err := runtime.DB(context.Background()); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("DB() error = %v; want ErrUnavailable", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go runtime.Run(ctx)
	requireEventually(t, time.Second, runtime.Available)

	got, err := runtime.DB(context.Background())
	if err != nil || got != db {
		t.Fatalf("DB() = %p, %v; want %p, nil", got, err, db)
	}
	if attempts.Load() < 2 {
		t.Fatalf("attempts = %d; want at least 2", attempts.Load())
	}
	if err := runtime.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if err := runtime.Close(); err != nil {
		t.Fatalf("second Close() error = %v", err)
	}
}

func TestRuntimeStopsRetryingWhenContextIsCanceled(t *testing.T) {
	var attempts atomic.Int32
	runtime := NewRuntime(func(context.Context) (*gorm.DB, error) {
		attempts.Add(1)
		return nil, errors.New("database offline")
	}, time.Hour, zap.NewNop())
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		runtime.Run(ctx)
	}()
	requireEventually(t, time.Second, func() bool { return attempts.Load() == 1 })
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Run did not stop after context cancellation")
	}
}

func requireEventually(t *testing.T, timeout time.Duration, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("condition was not satisfied before timeout")
}

func newTestGORM(t *testing.T) *gorm.DB {
	t.Helper()
	sqlDB := sql.OpenDB(testConnector{})
	db, err := gorm.Open(postgres.New(postgres.Config{Conn: sqlDB}), &gorm.Config{DisableAutomaticPing: true})
	if err != nil {
		t.Fatalf("open test gorm: %v", err)
	}
	return db
}

type testConnector struct{}

func (testConnector) Connect(context.Context) (driver.Conn, error) { return testConn{}, nil }
func (testConnector) Driver() driver.Driver                        { return testDriver{} }

type testDriver struct{}

func (testDriver) Open(string) (driver.Conn, error) { return testConn{}, nil }

type testConn struct{}

func (testConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("not implemented") }
func (testConn) Close() error                        { return nil }
func (testConn) Begin() (driver.Tx, error)           { return testTx{}, nil }

type testTx struct{}

func (testTx) Commit() error   { return nil }
func (testTx) Rollback() error { return nil }
