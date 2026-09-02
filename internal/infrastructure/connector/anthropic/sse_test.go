package anthropic

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
		"event: message_start\r\n", "data: {\"type\":", "\"message_start\"}\r\n",
		"data: second\r\n\r\n", ": keep-alive\n", "event: message_stop\ndata: {\"type\":\"message_stop\"}",
	}}), time.Second, 2<<20)
	t.Cleanup(func() { _ = reader.Close() })

	event, err := reader.Next(context.Background())
	if err != nil || event.Event != "message_start" ||
		string(event.Data) != "{\"type\":\"message_start\"}\nsecond" {
		t.Fatalf("event=%+v err=%v", event, err)
	}
	event, err = reader.Next(context.Background())
	if err != nil || event.Event != "message_stop" || string(event.Data) != `{"type":"message_stop"}` {
		t.Fatalf("tail=%+v err=%v", event, err)
	}
	if _, err := reader.Next(context.Background()); !errors.Is(err, io.EOF) {
		t.Fatalf("EOF error=%v", err)
	}
}

func TestSSEReaderAllowsManySmallEventsInOneLargeChunk(t *testing.T) {
	body := strings.Repeat("data: x\n\n", 20)
	reader := newSSEReader(io.NopCloser(strings.NewReader(body)), time.Second, 16)
	defer reader.Close()
	for index := 0; index < 20; index++ {
		event, err := reader.Next(context.Background())
		if err != nil || string(event.Data) != "x" {
			t.Fatalf("event %d=%+v err=%v", index, event, err)
		}
	}
}

func TestSSEReaderRejectsOversizedEvent(t *testing.T) {
	reader := newSSEReader(io.NopCloser(strings.NewReader("data: "+strings.Repeat("x", 33)+"\n\n")), time.Second, 32)
	defer reader.Close()
	_, err := reader.Next(context.Background())
	assertConnectorErrorKind(t, err, gatewayport.UpstreamInvalidResponse, "")
}

func TestSSEReaderEnforcesIdleTimeout(t *testing.T) {
	reader := newSSEReader(newBlockingReadCloser(), 20*time.Millisecond, 1024)
	defer reader.Close()
	started := time.Now()
	_, err := reader.Next(context.Background())
	assertConnectorErrorKind(t, err, gatewayport.UpstreamTimeout, "")
	if time.Since(started) > time.Second {
		t.Fatalf("idle timeout took %s", time.Since(started))
	}
}

func TestSSEReaderHonorsContextCancellation(t *testing.T) {
	reader := newSSEReader(newBlockingReadCloser(), time.Second, 1024)
	defer reader.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := reader.Next(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error=%v", err)
	}
}

func TestSSEReaderCloseUnblocksRead(t *testing.T) {
	reader := newSSEReader(newBlockingReadCloser(), time.Minute, 1024)
	result := make(chan error, 1)
	go func() {
		_, err := reader.Next(context.Background())
		result <- err
	}()
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-result:
		if !errors.Is(err, io.ErrClosedPipe) {
			t.Fatalf("error=%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Close did not unblock Next")
	}
}

type chunkReader struct {
	chunks  []string
	pending []byte
}

func (r *chunkReader) Read(buffer []byte) (int, error) {
	if len(r.pending) == 0 {
		if len(r.chunks) == 0 {
			return 0, io.EOF
		}
		r.pending = []byte(r.chunks[0])
		r.chunks = r.chunks[1:]
	}
	written := copy(buffer, r.pending)
	r.pending = r.pending[written:]
	return written, nil
}

type blockingReadCloser struct {
	closed chan struct{}
}

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
