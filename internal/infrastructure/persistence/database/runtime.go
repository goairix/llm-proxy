package database

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"go.uber.org/zap"
	"gorm.io/gorm"
)

var ErrUnavailable = errors.New("database unavailable")

// Opener opens and verifies one database connection.
type Opener func(context.Context) (*gorm.DB, error)

// Runtime publishes a database connection when it becomes available without blocking startup.
type Runtime struct {
	opener       Opener
	retryBackoff time.Duration
	logger       *zap.Logger
	db           atomic.Pointer[gorm.DB]
	closed       atomic.Bool
	publishMu    sync.Mutex
	closeOnce    sync.Once
	closeErr     error
}

// NewRuntime creates an unavailable runtime. Run performs connection attempts.
func NewRuntime(opener Opener, retryBackoff time.Duration, logger *zap.Logger) *Runtime {
	if retryBackoff <= 0 {
		retryBackoff = time.Second
	}
	if logger == nil {
		logger = zap.NewNop()
	}
	return &Runtime{opener: opener, retryBackoff: retryBackoff, logger: logger}
}

// Run retries until a connection is published, the context is canceled, or Close is called.
func (r *Runtime) Run(ctx context.Context) {
	if r.opener == nil {
		r.logger.Error("数据库连接器未配置")
		return
	}
	for !r.closed.Load() && !r.Available() {
		db, err := r.opener(ctx)
		if err == nil {
			r.publishMu.Lock()
			if r.closed.Load() {
				r.publishMu.Unlock()
				_ = closeGORM(db)
				return
			}
			r.db.Store(db)
			r.publishMu.Unlock()
			r.logger.Info("数据库连接已就绪")
			return
		}
		if ctx.Err() != nil {
			return
		}
		r.logger.Warn("数据库暂不可用，将稍后重试", zap.Error(err), zap.Duration("retry_backoff", r.retryBackoff))
		timer := time.NewTimer(r.retryBackoff)
		select {
		case <-ctx.Done():
			stopTimer(timer)
			return
		case <-timer.C:
		}
	}
}

// DB returns the currently published connection.
func (r *Runtime) DB(ctx context.Context) (*gorm.DB, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	db := r.db.Load()
	if db == nil || r.closed.Load() {
		return nil, ErrUnavailable
	}
	return db, nil
}

// Available reports whether a usable connection is currently published.
func (r *Runtime) Available() bool {
	return !r.closed.Load() && r.db.Load() != nil
}

// Close closes the published SQL pool. It is safe to call repeatedly.
func (r *Runtime) Close() error {
	r.closeOnce.Do(func() {
		r.publishMu.Lock()
		r.closed.Store(true)
		db := r.db.Swap(nil)
		r.publishMu.Unlock()
		if db != nil {
			r.closeErr = closeGORM(db)
		}
	})
	return r.closeErr
}

func closeGORM(db *gorm.DB) error {
	if db == nil {
		return nil
	}
	sqlDB, err := db.DB()
	if err != nil {
		return fmt.Errorf("access database connection pool: %w", err)
	}
	if err := sqlDB.Close(); err != nil {
		return fmt.Errorf("close database connection pool: %w", err)
	}
	return nil
}

func stopTimer(timer *time.Timer) {
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
}
