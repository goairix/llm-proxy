package port

import (
	"context"
	"time"

	"github.com/goairix/llm-proxy/internal/application/gateway/snapshot"
)

// RevisionReader reads the cheapest currently committed configuration revision.
type RevisionReader interface {
	CurrentRevision(context.Context) (int64, error)
}

// ConfigLoader reads one consistent cross-aggregate runtime configuration.
type ConfigLoader interface {
	Load(context.Context) (snapshot.SourceConfig, error)
}

// RuntimeConfigReader combines revision polling and consistent configuration loading.
type RuntimeConfigReader interface {
	RevisionReader
	ConfigLoader
}

// SnapshotCompiler validates source configuration and builds an immutable snapshot.
type SnapshotCompiler interface {
	Compile(snapshot.SourceConfig, time.Time) (*snapshot.RuntimeSnapshot, error)
}

// RefreshNotifier requests an asynchronous runtime configuration refresh.
type RefreshNotifier interface {
	NotifyRefresh()
}
