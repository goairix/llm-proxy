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
func (c *transactionConn) BeginTx(context.Context, driver.TxOptions) (driver.Tx, error) {
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
