package runtime

// TokenUsage is normalized token usage for one completed transparent proxy request.
type TokenUsage struct {
	Input      int64
	Output     int64
	CacheRead  int64
	CacheWrite int64
	Reasoning  int64
}

// UsageResult reports whether a valid usage object was present.
type UsageResult struct {
	Usage   TokenUsage
	Present bool
}

// UsageObserver incrementally observes an eligible response and returns one result.
type UsageObserver interface {
	Observe(contentType string, chunk []byte)
	Finish(status int, writeErr error) UsageResult
}

// UsageObserverFactory creates an observer for an eligible provider endpoint.
type UsageObserverFactory func(provider, method, path string) UsageObserver
