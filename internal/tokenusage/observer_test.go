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

type assertError string

func (e assertError) Error() string { return string(e) }
