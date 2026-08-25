package transactions

import (
	"context"
	"errors"
	"fmt"

	"gorm.io/gorm"

	sharederrors "github.com/goairix/llm-proxy/internal/domain/shared/errors"
	sharedport "github.com/goairix/llm-proxy/internal/domain/shared/port"
	"github.com/goairix/llm-proxy/internal/infrastructure/persistence/database"
)

// DBProvider supplies the current GORM connection or a stable availability error.
type DBProvider interface {
	DB(context.Context) (*gorm.DB, error)
}

// DBManager is the internal persistence contract used by repository adapters.
type DBManager interface {
	sharedport.TransactionManager
	DB(context.Context) (*gorm.DB, error)
}

// Manager propagates a GORM transaction through context without leaking it into application code.
type Manager struct {
	provider DBProvider
}

// NewManager creates a transaction manager backed by a database runtime.
func NewManager(provider DBProvider) *Manager {
	return &Manager{provider: provider}
}

// Transaction starts one transaction or reuses the transaction already in context.
func (m *Manager) Transaction(ctx context.Context, fn func(context.Context) error) error {
	if _, ok := transactionFromContext(ctx); ok {
		return fn(ctx)
	}
	db, err := m.provider.DB(ctx)
	if err != nil {
		if errors.Is(err, database.ErrUnavailable) {
			return fmt.Errorf("%w: database unavailable: %w", sharederrors.ErrDependencyUnavailable, err)
		}
		return err
	}
	tx := db.WithContext(ctx).Begin()
	if tx.Error != nil {
		return transactionBoundaryError(tx.Error)
	}
	completed := false
	defer func() {
		if !completed {
			_ = tx.Rollback().Error
		}
	}()
	if err := fn(context.WithValue(ctx, transactionContextKey{}, tx)); err != nil {
		return err
	}
	commitErr := tx.Commit().Error
	completed = true
	return transactionBoundaryError(commitErr)
}

func transactionBoundaryError(err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%w: database transaction boundary failed: %w", sharederrors.ErrDependencyUnavailable, err)
}

// DB returns the transaction in context, falling back to the runtime connection.
func (m *Manager) DB(ctx context.Context) (*gorm.DB, error) {
	if tx, ok := transactionFromContext(ctx); ok {
		return tx, nil
	}
	return m.provider.DB(ctx)
}

type transactionContextKey struct{}

func transactionFromContext(ctx context.Context) (*gorm.DB, bool) {
	tx, ok := ctx.Value(transactionContextKey{}).(*gorm.DB)
	return tx, ok && tx != nil
}

var _ sharedport.TransactionManager = (*Manager)(nil)
