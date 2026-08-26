package transactions

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"sync/atomic"
	"testing"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"

	sharederrors "github.com/goairix/llm-proxy/internal/domain/shared/errors"
	"github.com/goairix/llm-proxy/internal/infrastructure/persistence/database"
)

func TestManagerTransactionSharesAndReusesTransaction(t *testing.T) {
	state := &transactionState{}
	base := newTransactionTestGORM(t, state)
	manager := NewManager(staticProvider{db: base})

	err := manager.Transaction(context.Background(), func(ctx context.Context) error {
		outer, err := manager.DB(ctx)
		if err != nil {
			return err
		}
		if outer == base {
			t.Fatal("transaction DB equals base DB")
		}
		return manager.Transaction(ctx, func(nested context.Context) error {
			inner, err := manager.DB(nested)
			if err != nil {
				return err
			}
			if inner != outer {
				t.Fatal("nested transaction did not reuse outer DB")
			}
			return nil
		})
	})
	if err != nil {
		t.Fatalf("Transaction() error = %v", err)
	}
	if state.begins.Load() != 1 || state.commits.Load() != 1 || state.rollbacks.Load() != 0 {
		t.Fatalf("state = begins:%d commits:%d rollbacks:%d", state.begins.Load(), state.commits.Load(), state.rollbacks.Load())
	}
}

func TestManagerTransactionRollsBackCallbackError(t *testing.T) {
	state := &transactionState{}
	manager := NewManager(staticProvider{db: newTransactionTestGORM(t, state)})
	want := errors.New("write failed")

	err := manager.Transaction(context.Background(), func(context.Context) error { return want })
	if !errors.Is(err, want) {
		t.Fatalf("Transaction() error = %v; want callback error", err)
	}
	if state.commits.Load() != 0 || state.rollbacks.Load() != 1 {
		t.Fatalf("commits = %d, rollbacks = %d; want 0, 1", state.commits.Load(), state.rollbacks.Load())
	}
}

func TestManagerPropagatesUnavailableProvider(t *testing.T) {
	want := errors.New("unavailable")
	manager := NewManager(staticProvider{err: want})
	if err := manager.Transaction(context.Background(), func(context.Context) error { return nil }); !errors.Is(err, want) {
		t.Fatalf("Transaction() error = %v; want unavailable", err)
	}
	if _, err := manager.DB(context.Background()); !errors.Is(err, want) {
		t.Fatalf("DB() error = %v; want unavailable", err)
	}
}

func TestManagerMapsDatabaseUnavailableAtTransactionBoundary(t *testing.T) {
	manager := NewManager(staticProvider{err: database.ErrUnavailable})

	err := manager.Transaction(context.Background(), func(context.Context) error { return nil })

	if !errors.Is(err, sharederrors.ErrDependencyUnavailable) {
		t.Fatalf("Transaction() error = %v; want ErrDependencyUnavailable", err)
	}
}

func TestManagerMapsBeginFailureAfterDatabaseWasPublished(t *testing.T) {
	sqlDB := sql.OpenDB(failingBeginConnector{})
	db, err := gorm.Open(postgres.New(postgres.Config{Conn: sqlDB}), &gorm.Config{DisableAutomaticPing: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	manager := NewManager(staticProvider{db: db})

	err = manager.Transaction(context.Background(), func(context.Context) error { return nil })

	if !errors.Is(err, sharederrors.ErrDependencyUnavailable) {
		t.Fatalf("Transaction() error = %v; want ErrDependencyUnavailable", err)
	}
}

func TestManagerMapsCommitFailureAfterDatabaseWasPublished(t *testing.T) {
	sqlDB := sql.OpenDB(failingCommitConnector{})
	db, err := gorm.Open(postgres.New(postgres.Config{Conn: sqlDB}), &gorm.Config{DisableAutomaticPing: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	manager := NewManager(staticProvider{db: db})

	err = manager.Transaction(context.Background(), func(context.Context) error { return nil })

	if !errors.Is(err, sharederrors.ErrDependencyUnavailable) {
		t.Fatalf("Transaction() error = %v; want ErrDependencyUnavailable", err)
	}
}

func TestReadOnlySnapshotReusesTransactionContextAndOptions(t *testing.T) {
	state := &transactionState{}
	manager := NewManager(staticProvider{db: newTransactionTestGORM(t, state)})

	err := manager.ReadOnlySnapshot(context.Background(), func(ctx context.Context) error {
		first, err := manager.DB(ctx)
		if err != nil {
			return err
		}
		second, err := manager.DB(ctx)
		if err != nil {
			return err
		}
		if first != second {
			t.Fatal("transaction handle was not reused")
		}
		return manager.ReadOnlySnapshot(ctx, func(nested context.Context) error {
			nestedDB, err := manager.DB(nested)
			if err != nil {
				return err
			}
			if nestedDB != first {
				t.Fatal("nested snapshot did not reuse transaction")
			}
			return nil
		})
	})
	if err != nil {
		t.Fatal(err)
	}
	if state.begins.Load() != 1 || state.commits.Load() != 1 || !state.readOnly.Load() || state.isolation.Load() != int32(driver.IsolationLevel(sql.LevelRepeatableRead)) {
		t.Fatalf("state = begins:%d commits:%d read_only:%v isolation:%d", state.begins.Load(), state.commits.Load(), state.readOnly.Load(), state.isolation.Load())
	}
}

func TestReadOnlySnapshotPropagatesDatabaseUnavailable(t *testing.T) {
	manager := NewManager(staticProvider{err: database.ErrUnavailable})

	err := manager.ReadOnlySnapshot(context.Background(), func(context.Context) error { return nil })

	if !errors.Is(err, database.ErrUnavailable) {
		t.Fatalf("ReadOnlySnapshot() error = %v; want database.ErrUnavailable", err)
	}
}

type staticProvider struct {
	db  *gorm.DB
	err error
}

func (p staticProvider) DB(context.Context) (*gorm.DB, error) { return p.db, p.err }

func newTransactionTestGORM(t *testing.T, state *transactionState) *gorm.DB {
	t.Helper()
	sqlDB := sql.OpenDB(transactionConnector{state: state})
	db, err := gorm.Open(postgres.New(postgres.Config{Conn: sqlDB}), &gorm.Config{DisableAutomaticPing: true})
	if err != nil {
		t.Fatalf("open test gorm: %v", err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	return db
}

type transactionState struct {
	begins    atomic.Int32
	commits   atomic.Int32
	rollbacks atomic.Int32
	readOnly  atomic.Bool
	isolation atomic.Int32
}

type transactionConnector struct{ state *transactionState }

func (c transactionConnector) Connect(context.Context) (driver.Conn, error) {
	return &transactionConn{state: c.state}, nil
}
func (c transactionConnector) Driver() driver.Driver { return transactionDriver{state: c.state} }

type transactionDriver struct{ state *transactionState }

func (d transactionDriver) Open(string) (driver.Conn, error) {
	return &transactionConn{state: d.state}, nil
}

type transactionConn struct{ state *transactionState }

func (c *transactionConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("not implemented")
}
func (c *transactionConn) Close() error { return nil }
func (c *transactionConn) Begin() (driver.Tx, error) {
	c.state.begins.Add(1)
	return &transactionTx{state: c.state}, nil
}

func (c *transactionConn) BeginTx(_ context.Context, options driver.TxOptions) (driver.Tx, error) {
	c.state.readOnly.Store(options.ReadOnly)
	c.state.isolation.Store(int32(options.Isolation))
	return c.Begin()
}

type transactionTx struct{ state *transactionState }

func (t *transactionTx) Commit() error {
	t.state.commits.Add(1)
	return nil
}
func (t *transactionTx) Rollback() error {
	t.state.rollbacks.Add(1)
	return nil
}

type failingBeginConnector struct{}

func (failingBeginConnector) Connect(context.Context) (driver.Conn, error) {
	return failingBeginConn{}, nil
}
func (failingBeginConnector) Driver() driver.Driver { return failingBeginDriver{} }

type failingBeginDriver struct{}

func (failingBeginDriver) Open(string) (driver.Conn, error) { return failingBeginConn{}, nil }

type failingBeginConn struct{}

func (failingBeginConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("not implemented")
}
func (failingBeginConn) Close() error              { return nil }
func (failingBeginConn) Begin() (driver.Tx, error) { return nil, driver.ErrBadConn }

type failingCommitConnector struct{}

func (failingCommitConnector) Connect(context.Context) (driver.Conn, error) {
	return failingCommitConn{}, nil
}
func (failingCommitConnector) Driver() driver.Driver { return failingCommitDriver{} }

type failingCommitDriver struct{}

func (failingCommitDriver) Open(string) (driver.Conn, error) { return failingCommitConn{}, nil }

type failingCommitConn struct{}

func (failingCommitConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("not implemented")
}
func (failingCommitConn) Close() error { return nil }
func (failingCommitConn) Begin() (driver.Tx, error) {
	return failingCommitTx{}, nil
}

type failingCommitTx struct{}

func (failingCommitTx) Commit() error   { return driver.ErrBadConn }
func (failingCommitTx) Rollback() error { return nil }
