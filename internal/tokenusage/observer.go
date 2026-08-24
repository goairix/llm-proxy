package tokenusage

import "strings"

const maxCaptureBytes = 2 << 20

// Usage is the normalized token usage for one completed request.
type Usage struct {
	Input      int64
	Output     int64
	CacheRead  int64
	CacheWrite int64
	Reasoning  int64
}

// Result reports whether a valid usage object was present.
type Result struct {
	Usage   Usage
	Present bool
}

// Observer incrementally observes an eligible response and returns one result.
type Observer interface {
	Observe(contentType string, chunk []byte)
	Finish(status int, writeErr error) Result
}

type responseMode uint8

const (
	responseModeUnknown responseMode = iota
	responseModeJSON
	responseModeSSE
)

type observer struct {
	provider  string
	path      string
	mode      responseMode
	jsonBody  []byte
	oversized bool
	finished  bool
	result    Result
}

// NewObserver returns an observer only for token-bearing generation endpoints.
func NewObserver(provider, method, path string) Observer {
	if method != "POST" || !eligibleEndpoint(provider, path) {
		return nil
	}
	return &observer{provider: provider, path: path}
}

func eligibleEndpoint(provider, path string) bool {
	switch provider {
	case "openai":
		switch path {
		case "/openai/v1/responses",
			"/openai/v1/responses/compact",
			"/openai/v1/chat/completions",
			"/openai/v1/completions":
			return true
		}
	case "anthropic":
		return path == "/anthropic/v1/messages"
	}
	return false
}

func (o *observer) Observe(contentType string, chunk []byte) {
	if o.finished || o.oversized || len(chunk) == 0 {
		return
	}
	if o.mode == responseModeUnknown {
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(contentType)), "text/event-stream") {
			o.mode = responseModeSSE
		} else {
			o.mode = responseModeJSON
		}
	}
	if o.mode != responseModeJSON {
		return
	}
	if len(chunk) > maxCaptureBytes-len(o.jsonBody) {
		o.jsonBody = nil
		o.oversized = true
		return
	}
	o.jsonBody = append(o.jsonBody, chunk...)
}

func (o *observer) Finish(status int, writeErr error) Result {
	if o.finished {
		return o.result
	}
	o.finished = true
	if status < 200 || status >= 300 || writeErr != nil || o.oversized {
		return o.result
	}
	if o.mode == responseModeJSON {
		o.result = parseJSON(o.provider, o.path, o.jsonBody)
	}
	return o.result
}
