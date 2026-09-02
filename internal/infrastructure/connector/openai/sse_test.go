package openai

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	gatewayport "github.com/goairix/llm-proxy/internal/application/gateway/port"
)

func TestSSEReaderHandlesCRLFMultipleDataChunksAndTailEvent(t *testing.T) {
	reader := newSSEReader(io.NopCloser(&chunkReader{chunks: []string{
		"event: response.output_text.delta\r\n", "data: {\"a\":", "1}\r\n",
		"data: second\r\n\r\n", "data: [DONE]",
	}}), time.Second, 2<<20)
	t.Cleanup(func() { _ = reader.Close() })
	event, err := reader.Next(context.Background())
	if err != nil || event.Event != "response.output_text.delta" || string(event.Data) != "{\"a\":1}\nsecond" {
		t.Fatalf("event=%+v error=%v", event, err)
	}
	event, err = reader.Next(context.Background())
	if err != nil || string(event.Data) != "[DONE]" {
		t.Fatalf("tail=%+v error=%v", event, err)
	}
	if _, err := reader.Next(context.Background()); !errors.Is(err, io.EOF) {
		t.Fatalf("EOF error=%v", err)
	}
}

func TestSSEReaderRejectsOversizedEvent(t *testing.T) {
	reader := newSSEReader(io.NopCloser(strings.NewReader("data: "+strings.Repeat("x", 33)+"\n\n")), time.Second, 32)
	defer reader.Close()
	_, err := reader.Next(context.Background())
	var connectorErr *gatewayport.ConnectorError
	if !errors.As(err, &connectorErr) || connectorErr.Kind != gatewayport.UpstreamInvalidResponse {
		t.Fatalf("error=%v", err)
	}
}

func TestSSEReaderEnforcesIdleTimeout(t *testing.T) {
	reader := newSSEReader(newBlockingReadCloser(), 20*time.Millisecond, 1024)
	defer reader.Close()
	started := time.Now()
	_, err := reader.Next(context.Background())
	var connectorErr *gatewayport.ConnectorError
	if !errors.As(err, &connectorErr) || connectorErr.Kind != gatewayport.UpstreamTimeout || time.Since(started) > time.Second {
		t.Fatalf("error=%v elapsed=%s", err, time.Since(started))
	}
}

type chunkReader struct{ chunks []string }

func (r *chunkReader) Read(destination []byte) (int, error) {
	if len(r.chunks) == 0 {
		return 0, io.EOF
	}
	chunk := r.chunks[0]
	r.chunks = r.chunks[1:]
	return copy(destination, chunk), nil
}

type blockingReadCloser struct{ closed chan struct{} }

func newBlockingReadCloser() *blockingReadCloser {
	return &blockingReadCloser{closed: make(chan struct{})}
}

func (r *blockingReadCloser) Read([]byte) (int, error) {
	<-r.closed
	return 0, io.ErrClosedPipe
}

func (r *blockingReadCloser) Close() error {
	select {
	case <-r.closed:
	default:
		close(r.closed)
	}
	return nil
}
