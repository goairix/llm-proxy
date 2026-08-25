package port

import "context"

// TransactionManager executes an application operation atomically.
type TransactionManager interface {
	Transaction(ctx context.Context, fn func(context.Context) error) error
}
