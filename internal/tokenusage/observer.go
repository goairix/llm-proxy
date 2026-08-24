package tokenusage

import (
	"strings"

	appRuntime "github.com/goairix/llm-proxy/internal/application/runtime"
)

const maxCaptureBytes = 2 << 20

// Usage is kept as a compatibility alias while token parsing moves behind the application port.
type Usage = appRuntime.TokenUsage

// Result is kept as a compatibility alias while token parsing moves behind the application port.
type Result = appRuntime.UsageResult

// Observer is kept as a compatibility alias while token parsing moves behind the application port.
type Observer = appRuntime.UsageObserver

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
	sse       sseDecoder
}

// NewObserver returns an observer only for token-bearing generation endpoints.
func NewObserver(provider, method, path string) Observer {
	if method != "POST" || !eligibleEndpoint(provider, path) {
		return nil
	}
	return &observer{
		provider: provider,
		path:     path,
		sse:      sseDecoder{provider: provider, path: path},
	}
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
		o.sse.Observe(chunk)
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
	} else if o.mode == responseModeSSE {
		o.result = o.sse.Finish()
	}
	return o.result
}
