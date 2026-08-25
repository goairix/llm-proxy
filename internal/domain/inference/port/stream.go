package port

import (
	"context"

	inference "github.com/goairix/llm-proxy/internal/domain/inference/model"
)

// Stream provides a cancellable, backpressured inference event source.
// Recv has one serial consumer; Close is idempotent and may be called concurrently.
type Stream interface {
	Recv(context.Context) (inference.Event, error)
	Close() error
}
