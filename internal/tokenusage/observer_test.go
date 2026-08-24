package tokenusage

import (
	"reflect"
	"strings"
	"testing"
)

func TestNewObserverEndpoints(t *testing.T) {
	tests := []struct {
		name, provider, method, path string
		wantEligible                 bool
	}{
		{name: "responses", provider: "openai", method: "POST", path: "/openai/v1/responses", wantEligible: true},
		{name: "responses compact", provider: "openai", method: "POST", path: "/openai/v1/responses/compact", wantEligible: true},
		{name: "chat completions", provider: "openai", method: "POST", path: "/openai/v1/chat/completions", wantEligible: true},
		{name: "legacy completions", provider: "openai", method: "POST", path: "/openai/v1/completions", wantEligible: true},
		{name: "messages", provider: "anthropic", method: "POST", path: "/anthropic/v1/messages", wantEligible: true},
		{name: "method mismatch", provider: "openai", method: "GET", path: "/openai/v1/responses"},
		{name: "retrieve response", provider: "openai", method: "GET", path: "/openai/v1/responses/resp_1"},
		{name: "input tokens", provider: "openai", method: "POST", path: "/openai/v1/responses/input_tokens"},
		{name: "provider mismatch", provider: "anthropic", method: "POST", path: "/openai/v1/responses"},
		{name: "unknown", provider: "openai", method: "POST", path: "/openai/v1/embeddings"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := NewObserver(tc.provider, tc.method, tc.path)
			if (got != nil) != tc.wantEligible {
				t.Fatalf("NewObserver() eligible = %v, want %v", got != nil, tc.wantEligible)
			}
		})
	}
}

func TestObserverJSON(t *testing.T) {
	tests := []struct {
		name, provider, path, body string
		want                       Usage
	}{
		{
			name:     "OpenAI Responses",
			provider: "openai",
			path:     "/openai/v1/responses",
			body:     `{"usage":{"input_tokens":120,"output_tokens":30,"input_tokens_details":{"cached_tokens":40,"cache_write_tokens":10},"output_tokens_details":{"reasoning_tokens":12}}}`,
			want:     Usage{Input: 120, Output: 30, CacheRead: 40, CacheWrite: 10, Reasoning: 12},
		},
		{
			name:     "OpenAI Responses compact",
			provider: "openai",
			path:     "/openai/v1/responses/compact",
			body:     `{"usage":{"input_tokens":7,"output_tokens":2}}`,
			want:     Usage{Input: 7, Output: 2},
		},
		{
			name:     "OpenAI Chat Completions",
			provider: "openai",
			path:     "/openai/v1/chat/completions",
			body:     `{"usage":{"prompt_tokens":80,"completion_tokens":20,"prompt_tokens_details":{"cached_tokens":25,"cache_write_tokens":5},"completion_tokens_details":{"reasoning_tokens":7}}}`,
			want:     Usage{Input: 80, Output: 20, CacheRead: 25, CacheWrite: 5, Reasoning: 7},
		},
		{
			name:     "OpenAI legacy Completions",
			provider: "openai",
			path:     "/openai/v1/completions",
			body:     `{"usage":{"prompt_tokens":14,"completion_tokens":6}}`,
			want:     Usage{Input: 14, Output: 6},
		},
		{
			name:     "Anthropic Messages",
			provider: "anthropic",
			path:     "/anthropic/v1/messages",
			body:     `{"usage":{"input_tokens":50,"cache_creation_input_tokens":20,"cache_read_input_tokens":30,"output_tokens":15,"output_tokens_details":{"thinking_tokens":6}}}`,
			want:     Usage{Input: 100, Output: 15, CacheRead: 30, CacheWrite: 20, Reasoning: 6},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			observer := NewObserver(tc.provider, "POST", tc.path)
			if observer == nil {
				t.Fatal("NewObserver() = nil")
			}
			observer.Observe("application/json; charset=utf-8", []byte(tc.body))
			got := observer.Finish(200, nil)
			if !got.Present || got.Usage != tc.want {
				t.Fatalf("Finish() = %+v, want present usage %+v", got, tc.want)
			}
		})
	}
}

func TestObserverJSONValidationAndLimits(t *testing.T) {
	tests := []struct {
		name, body string
		want       Result
	}{
		{name: "zero usage", body: `{"usage":{"input_tokens":0,"output_tokens":0}}`, want: Result{Present: true}},
		{name: "optional details missing", body: `{"usage":{"input_tokens":2,"output_tokens":1}}`, want: Result{Usage: Usage{Input: 2, Output: 1}, Present: true}},
		{name: "input missing", body: `{"usage":{"output_tokens":1}}`},
		{name: "output missing", body: `{"usage":{"input_tokens":2}}`},
		{name: "negative", body: `{"usage":{"input_tokens":-1,"output_tokens":1}}`},
		{name: "usage null", body: `{"usage":null}`},
		{name: "invalid json", body: `{"usage":`},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			observer := NewObserver("openai", "POST", "/openai/v1/responses")
			observer.Observe("application/json", []byte(tc.body))
			got := observer.Finish(200, nil)
			if got != tc.want {
				t.Fatalf("Finish() = %+v, want %+v", got, tc.want)
			}
			if second := observer.Finish(200, nil); second != got {
				t.Fatalf("second Finish() = %+v, first = %+v", second, got)
			}
		})
	}

	t.Run("non-success status", func(t *testing.T) {
		observer := NewObserver("openai", "POST", "/openai/v1/responses")
		observer.Observe("application/json", []byte(`{"usage":{"input_tokens":2,"output_tokens":1}}`))
		if got := observer.Finish(429, nil); got.Present {
			t.Fatalf("Finish() = %+v, want missing", got)
		}
	})

	t.Run("write error", func(t *testing.T) {
		observer := NewObserver("openai", "POST", "/openai/v1/responses")
		observer.Observe("application/json", []byte(`{"usage":{"input_tokens":2,"output_tokens":1}}`))
		if got := observer.Finish(200, assertError("write failed")); got.Present {
			t.Fatalf("Finish() = %+v, want missing", got)
		}
	})

	t.Run("capture boundary", func(t *testing.T) {
		const envelopeBytes = len(`{"usage":{"input_tokens":2,"output_tokens":1},"padding":""}`)
		body := `{"usage":{"input_tokens":2,"output_tokens":1},"padding":"` + strings.Repeat("x", maxCaptureBytes-envelopeBytes) + `"}`
		if len(body) != maxCaptureBytes {
			t.Fatalf("test body length = %d, want %d", len(body), maxCaptureBytes)
		}

		observer := NewObserver("openai", "POST", "/openai/v1/responses")
		observer.Observe("application/json", []byte(body))
		if got := observer.Finish(200, nil); !got.Present {
			t.Fatalf("exact limit Finish() = %+v, want present", got)
		}

		oversized := NewObserver("openai", "POST", "/openai/v1/responses")
		oversized.Observe("application/json", append([]byte(body), ' '))
		if got := oversized.Finish(200, nil); got.Present {
			t.Fatalf("oversized Finish() = %+v, want missing", got)
		}
	})
}

func TestParseJSONRejectsAnthropicOverflow(t *testing.T) {
	observer := NewObserver("anthropic", "POST", "/anthropic/v1/messages")
	observer.Observe("application/json", []byte(`{"usage":{"input_tokens":9223372036854775807,"cache_creation_input_tokens":1,"output_tokens":1}}`))
	if got := observer.Finish(200, nil); !reflect.DeepEqual(got, Result{}) {
		t.Fatalf("Finish() = %+v, want missing", got)
	}
}

func TestObserverSSE(t *testing.T) {
	tests := []struct {
		name, provider, path, stream string
		want                         Usage
	}{
		{
			name:     "OpenAI Responses",
			provider: "openai",
			path:     "/openai/v1/responses",
			stream: "event: response.completed\n" +
				"data: {\"type\":\"response.completed\",\"response\":{\"usage\":{\"input_tokens\":10,\"output_tokens\":4,\"input_tokens_details\":{\"cached_tokens\":3},\"output_tokens_details\":{\"reasoning_tokens\":2}}}}\n\n",
			want: Usage{Input: 10, Output: 4, CacheRead: 3, Reasoning: 2},
		},
		{
			name:     "OpenAI Chat",
			provider: "openai",
			path:     "/openai/v1/chat/completions",
			stream: "data: {\"object\":\"chat.completion.chunk\",\"usage\":{\"prompt_tokens\":11,\"completion_tokens\":5}}\n\n" +
				"data: [DONE]\n\n",
			want: Usage{Input: 11, Output: 5},
		},
		{
			name:     "Anthropic Messages",
			provider: "anthropic",
			path:     "/anthropic/v1/messages",
			stream: "event: message_start\r\n" +
				"data: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":10,\"cache_creation_input_tokens\":2,\"cache_read_input_tokens\":3,\"output_tokens\":1}}}\r\n\r\n" +
				"event: message_delta\r\n" +
				"data: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":8,\"output_tokens_details\":{\"thinking_tokens\":4}}}\r\n\r\n" +
				"event: message_stop\r\ndata: {\"type\":\"message_stop\"}\r\n\r\n",
			want: Usage{Input: 15, Output: 8, CacheRead: 3, CacheWrite: 2, Reasoning: 4},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			for _, chunkSize := range []int{len(tc.stream), 1} {
				observer := NewObserver(tc.provider, "POST", tc.path)
				for start := 0; start < len(tc.stream); start += chunkSize {
					end := min(start+chunkSize, len(tc.stream))
					observer.Observe("text/event-stream; charset=utf-8", []byte(tc.stream[start:end]))
				}
				got := observer.Finish(200, nil)
				if !got.Present || got.Usage != tc.want {
					t.Fatalf("chunk size %d: Finish() = %+v, want %+v", chunkSize, got, tc.want)
				}
			}
		})
	}
}

func TestObserverSSEMergingAndRecovery(t *testing.T) {
	t.Run("latest chat usage wins without trailing blank line", func(t *testing.T) {
		stream := "data: {\"usage\":{\"prompt_tokens\":1,\"completion_tokens\":2}}\n\n" +
			"data: {\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":20}}"
		observer := NewObserver("openai", "POST", "/openai/v1/chat/completions")
		observer.Observe("text/event-stream", []byte(stream))
		want := Result{Usage: Usage{Input: 10, Output: 20}, Present: true}
		if got := observer.Finish(200, nil); got != want {
			t.Fatalf("Finish() = %+v, want %+v", got, want)
		}
		if got := observer.Finish(200, nil); got != want {
			t.Fatalf("second Finish() = %+v, want %+v", got, want)
		}
	})

	t.Run("latest cumulative anthropic delta wins", func(t *testing.T) {
		stream := "data: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":4,\"output_tokens\":0}}}\n\n" +
			"data: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":3}}\n\n" +
			"data: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":7}}\n\n"
		observer := NewObserver("anthropic", "POST", "/anthropic/v1/messages")
		observer.Observe("text/event-stream", []byte(stream))
		want := Result{Usage: Usage{Input: 4, Output: 7}, Present: true}
		if got := observer.Finish(200, nil); got != want {
			t.Fatalf("Finish() = %+v, want %+v", got, want)
		}
	})

	t.Run("oversized event is discarded and next event parses", func(t *testing.T) {
		stream := "data: " + strings.Repeat("x", maxCaptureBytes+1) + "\n\n" +
			"data: {\"usage\":{\"prompt_tokens\":9,\"completion_tokens\":4}}\n\n"
		observer := NewObserver("openai", "POST", "/openai/v1/chat/completions")
		observer.Observe("text/event-stream", []byte(stream))
		want := Result{Usage: Usage{Input: 9, Output: 4}, Present: true}
		if got := observer.Finish(200, nil); got != want {
			t.Fatalf("Finish() = %+v, want %+v", got, want)
		}
	})

	t.Run("invalid and negative events are ignored", func(t *testing.T) {
		stream := ": ping\n\n" +
			"data: not-json\n\n" +
			"data: {\"usage\":{\"prompt_tokens\":-1,\"completion_tokens\":4}}\n\n" +
			"data: [DONE]\n\n"
		observer := NewObserver("openai", "POST", "/openai/v1/chat/completions")
		observer.Observe("text/event-stream", []byte(stream))
		if got := observer.Finish(200, nil); got.Present {
			t.Fatalf("Finish() = %+v, want missing", got)
		}
	})
}

type assertError string

func (e assertError) Error() string { return string(e) }
