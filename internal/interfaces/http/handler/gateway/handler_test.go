package gateway

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	gatewayservice "github.com/goairix/llm-proxy/internal/application/gateway/service"
	inference "github.com/goairix/llm-proxy/internal/domain/inference/model"
	inferenceport "github.com/goairix/llm-proxy/internal/domain/inference/port"
)

func TestOpenAIHandlerCompletesAndExtractsOnlyBearerKey(t *testing.T) {
	stub := &gatewayStub{response: validResponse()}
	handler := NewOpenAI(stub)
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"assistant","messages":[{"role":"user","content":"hello"}]}`))
	request.Header.Set("Content-Type", "application/json; charset=utf-8")
	request.Header.Set("Authorization", "Bearer vk-openai-secret")
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"object":"chat.completion"`) {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if stub.virtualKey != "vk-openai-secret" || stub.request.Model != "assistant" || stub.completeCalls != 1 || stub.streamCalls != 0 {
		t.Fatalf("stub=%+v", stub)
	}
	if request.Header.Get("Authorization") != "Bearer vk-openai-secret" {
		t.Fatal("handler mutated the original request headers")
	}
}

func TestAnthropicHandlerPrefersAPIKeyAndFallsBackToBearer(t *testing.T) {
	tests := []struct {
		name      string
		xAPIKey   string
		authorize string
		wantKey   string
	}{
		{name: "x api key", xAPIKey: "vk-anthropic", authorize: "Bearer ignored", wantKey: "vk-anthropic"},
		{name: "bearer fallback", authorize: "Bearer vk-bearer", wantKey: "vk-bearer"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			stub := &gatewayStub{response: validResponse()}
			handler := NewAnthropic(stub)
			request := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"assistant","max_tokens":1,"messages":[{"role":"user","content":"hello"}]}`))
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("x-api-key", test.xAPIKey)
			request.Header.Set("Authorization", test.authorize)
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, request)
			if recorder.Code != http.StatusOK || stub.virtualKey != test.wantKey || !strings.Contains(recorder.Body.String(), `"type":"message"`) {
				t.Fatalf("status=%d key=%q body=%s", recorder.Code, stub.virtualKey, recorder.Body.String())
			}
		})
	}
}

func TestGatewayHandlersRejectMethodContentTypeOversizedBodyAndEmptyKey(t *testing.T) {
	t.Run("method", func(t *testing.T) {
		recorder := httptest.NewRecorder()
		NewOpenAI(&gatewayStub{}).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/v1/chat/completions", nil))
		if recorder.Code != http.StatusMethodNotAllowed || recorder.Header().Get("Allow") != http.MethodPost {
			t.Fatalf("status=%d allow=%q", recorder.Code, recorder.Header().Get("Allow"))
		}
	})
	t.Run("content type", func(t *testing.T) {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{}`))
		request.Header.Set("Content-Type", "text/plain")
		NewAnthropic(&gatewayStub{}).ServeHTTP(recorder, request)
		if recorder.Code != http.StatusUnsupportedMediaType {
			t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
		}
	})
	t.Run("oversized", func(t *testing.T) {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"`+strings.Repeat("x", maxRequestBodyBytes)+`"}`))
		request.Header.Set("Content-Type", "application/json")
		NewOpenAI(&gatewayStub{}).ServeHTTP(recorder, request)
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
		}
	})
	t.Run("empty key reaches authentication", func(t *testing.T) {
		stub := &gatewayStub{completeErr: gatewayservice.NewError(gatewayservice.AuthenticationFailed, "Virtual Key 无效", "", nil)}
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"assistant","messages":[{"role":"user","content":"hello"}]}`))
		request.Header.Set("Content-Type", "application/json")
		NewOpenAI(stub).ServeHTTP(recorder, request)
		if recorder.Code != http.StatusUnauthorized || stub.virtualKey != "" {
			t.Fatalf("status=%d key=%q body=%s", recorder.Code, stub.virtualKey, recorder.Body.String())
		}
	})
}

func TestOpenAIStreamFlushesBeforeConnectorFinishes(t *testing.T) {
	release := make(chan struct{})
	stream := newBlockingStream(release)
	server := httptest.NewServer(NewOpenAI(&gatewayStub{stream: stream}))
	defer server.Close()
	request, err := http.NewRequest(http.MethodPost, server.URL+"/v1/chat/completions", strings.NewReader(`{"model":"assistant","messages":[{"role":"user","content":"hello"}],"stream":true}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer vk")
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	first, err := bufio.NewReader(response.Body).ReadString('\n')
	if err != nil || !strings.HasPrefix(first, "data: ") {
		t.Fatalf("first=%q err=%v", first, err)
	}
	close(release)
}

func TestAnthropicClientDisconnectCancelsAndClosesStream(t *testing.T) {
	stream := newCancelAwareStream()
	server := httptest.NewServer(NewAnthropic(&gatewayStub{stream: stream}))
	defer server.Close()
	request, err := http.NewRequest(http.MethodPost, server.URL+"/v1/messages", strings.NewReader(`{"model":"assistant","max_tokens":1,"messages":[{"role":"user","content":"hello"}],"stream":true}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("x-api-key", "vk")
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(response.Body)
	if first, err := reader.ReadString('\n'); err != nil || first != "event: message_start\n" {
		t.Fatalf("first=%q err=%v", first, err)
	}
	_ = response.Body.Close()
	waitSignal(t, stream.canceled, "stream context cancellation")
	waitSignal(t, stream.closed, "stream close")
}

func TestStreamingRequiresUnderlyingFlusherAndClosesCreatedStream(t *testing.T) {
	stream := &closeTrackingStream{}
	stub := &gatewayStub{stream: stream}
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"assistant","messages":[{"role":"user","content":"hello"}],"stream":true}`))
	request.Header.Set("Content-Type", "application/json")
	writer := &noFlushWriter{header: make(http.Header)}

	NewOpenAI(stub).ServeHTTP(writer, request)

	if writer.status != http.StatusInternalServerError || !stream.isClosed() || stub.streamCalls != 1 {
		t.Fatalf("status=%d closed=%v calls=%d body=%s", writer.status, stream.isClosed(), stub.streamCalls, writer.body.String())
	}
}

type gatewayStub struct {
	virtualKey    string
	request       inference.Request
	response      inference.Response
	stream        inferenceport.Stream
	completeErr   error
	streamErr     error
	completeCalls int
	streamCalls   int
}

func (g *gatewayStub) Complete(_ context.Context, key string, request inference.Request) (inference.Response, error) {
	g.virtualKey, g.request = key, request
	g.completeCalls++
	return g.response, g.completeErr
}

func (g *gatewayStub) Stream(_ context.Context, key string, request inference.Request) (inferenceport.Stream, error) {
	g.virtualKey, g.request = key, request
	g.streamCalls++
	return g.stream, g.streamErr
}

type blockingStream struct {
	release <-chan struct{}
	events  []inference.Event
	index   int
	closed  chan struct{}
	once    sync.Once
}

func newBlockingStream(release <-chan struct{}) *blockingStream {
	response := validResponse()
	return &blockingStream{release: release, closed: make(chan struct{}), events: validEvents(response)}
}

func (s *blockingStream) Recv(ctx context.Context) (inference.Event, error) {
	if s.index == 1 {
		select {
		case <-s.release:
		case <-ctx.Done():
			return inference.Event{}, ctx.Err()
		}
	}
	if s.index >= len(s.events) {
		return inference.Event{}, io.EOF
	}
	event := s.events[s.index]
	s.index++
	return event, nil
}
func (s *blockingStream) Close() error { s.once.Do(func() { close(s.closed) }); return nil }

type cancelAwareStream struct {
	started  bool
	canceled chan struct{}
	closed   chan struct{}
	once     sync.Once
}

func newCancelAwareStream() *cancelAwareStream {
	return &cancelAwareStream{canceled: make(chan struct{}), closed: make(chan struct{})}
}
func (s *cancelAwareStream) Recv(ctx context.Context) (inference.Event, error) {
	if !s.started {
		s.started = true
		response := validResponse()
		return inference.NewResponseStartAt(response.ID, response.Model, response.CreatedAt), nil
	}
	<-ctx.Done()
	close(s.canceled)
	return inference.Event{}, ctx.Err()
}
func (s *cancelAwareStream) Close() error { s.once.Do(func() { close(s.closed) }); return nil }

type closeTrackingStream struct {
	mu     sync.Mutex
	closed bool
}

func (s *closeTrackingStream) Recv(context.Context) (inference.Event, error) {
	return inference.Event{}, io.EOF
}
func (s *closeTrackingStream) Close() error   { s.mu.Lock(); s.closed = true; s.mu.Unlock(); return nil }
func (s *closeTrackingStream) isClosed() bool { s.mu.Lock(); defer s.mu.Unlock(); return s.closed }

type noFlushWriter struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func (w *noFlushWriter) Header() http.Header    { return w.header }
func (w *noFlushWriter) WriteHeader(status int) { w.status = status }
func (w *noFlushWriter) Write(value []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.body.Write(value)
}

func waitSignal(t *testing.T, signal <-chan struct{}, name string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(3 * time.Second):
		t.Fatalf("timed out waiting for %s", name)
	}
}

func validResponse() inference.Response {
	return inference.Response{
		ID: uuid.Must(uuid.NewV7()), Model: "assistant", CreatedAt: time.Now().UTC(), StopReason: inference.StopEndTurn,
		Content: []inference.ContentBlock{{Type: inference.ContentText, Text: &inference.TextContent{Text: "hello"}}},
	}
}

func validEvents(response inference.Response) []inference.Event {
	return []inference.Event{
		inference.NewResponseStartAt(response.ID, response.Model, response.CreatedAt),
		inference.NewContentBlockStart(0, inference.ContentText), inference.NewTextDelta(0, "hello"),
		inference.NewContentBlockStop(0), inference.NewUsageUpdate(inference.Usage{}), inference.NewResponseFinish(inference.StopEndTurn),
	}
}

var _ Gateway = (*gatewayStub)(nil)
var _ inferenceport.Stream = (*blockingStream)(nil)
