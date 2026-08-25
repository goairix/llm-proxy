package port

import (
	"context"

	"github.com/goairix/llm-proxy/internal/application/gateway/snapshot"
)

// RuntimeConfigReader reads one versioned cross-aggregate runtime configuration.
type RuntimeConfigReader interface {
	CurrentRevision(context.Context) (int64, error)
	Load(context.Context) (snapshot.SourceConfig, error)
}
