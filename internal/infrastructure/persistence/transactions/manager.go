package transactions

import (
	"context"

	"gorm.io/gorm"

	sharedport "github.com/goairix/llm-proxy/internal/domain/shared/port"
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
		return err
	}
	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return fn(context.WithValue(ctx, transactionContextKey{}, tx))
	})
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
