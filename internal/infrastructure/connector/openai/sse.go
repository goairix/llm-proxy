package openai

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"time"

	gatewayport "github.com/goairix/llm-proxy/internal/application/gateway/port"
)

type sseEvent struct {
	Event string
	Data  []byte
}

type readChunk struct {
	data []byte
	err  error
}

type sseReader struct {
	source    io.ReadCloser
	idle      time.Duration
	maxEvent  int
	chunks    chan readChunk
	done      chan struct{}
	pending   []byte
	terminal  error
	closeOnce sync.Once
	closeErr  error
}

func newSSEReader(source io.ReadCloser, idle time.Duration, maxEvent int) *sseReader {
	reader := &sseReader{source: source, idle: idle, maxEvent: maxEvent, chunks: make(chan readChunk, 1), done: make(chan struct{})}
	go reader.pump()
	return reader
}

func (r *sseReader) pump() {
	buffered := bufio.NewReader(r.source)
	buffer := make([]byte, 32<<10)
	for {
		count, err := buffered.Read(buffer)
		chunk := readChunk{err: err}
		if count > 0 {
			chunk.data = append([]byte(nil), buffer[:count]...)
		}
		select {
		case r.chunks <- chunk:
		case <-r.done:
			return
		}
		if err != nil {
			return
		}
	}
}

func (r *sseReader) Next(ctx context.Context) (sseEvent, error) {
	if r == nil || r.source == nil {
		return sseEvent{}, connectorError(gatewayport.UpstreamInvalidResponse, errors.New("SSE reader is nil"))
	}
	if err := ctx.Err(); err != nil {
		_ = r.Close()
		return sseEvent{}, err
	}
	if r.terminal != nil && len(r.pending) == 0 {
		return sseEvent{}, r.terminal
	}
	var eventName string
	dataLines := make([][]byte, 0, 1)
	eventBytes := 0
	for {
		line, err := r.readLine(ctx)
		if err != nil {
			if errors.Is(err, io.EOF) && (eventName != "" || len(dataLines) > 0) {
				return sseEvent{Event: eventName, Data: bytes.Join(dataLines, []byte("\n"))}, nil
			}
			return sseEvent{}, err
		}
		eventBytes += len(line) + 1
		if eventBytes > r.maxEvent {
			_ = r.Close()
			return sseEvent{}, connectorError(gatewayport.UpstreamInvalidResponse, errBodyTooLarge)
		}
		line = bytes.TrimSuffix(line, []byte("\r"))
		if len(line) == 0 {
			if eventName == "" && len(dataLines) == 0 {
				continue
			}
			return sseEvent{Event: eventName, Data: bytes.Join(dataLines, []byte("\n"))}, nil
		}
		if line[0] == ':' {
			continue
		}
		field, value, found := bytes.Cut(line, []byte(":"))
		if !found {
			field, value = line, nil
		}
		value = bytes.TrimPrefix(value, []byte(" "))
		switch string(field) {
		case "event":
			eventName = string(value)
		case "data":
			dataLines = append(dataLines, append([]byte(nil), value...))
		}
	}
}

func (r *sseReader) readLine(ctx context.Context) ([]byte, error) {
	for {
		if err := ctx.Err(); err != nil {
			_ = r.Close()
			return nil, err
		}
		if index := bytes.IndexByte(r.pending, '\n'); index >= 0 {
			line := append([]byte(nil), r.pending[:index]...)
			r.pending = r.pending[index+1:]
			return line, nil
		}
		if r.terminal != nil {
			if len(r.pending) == 0 {
				return nil, r.terminal
			}
			line := append([]byte(nil), r.pending...)
			r.pending = nil
			return line, nil
		}
		timer := time.NewTimer(r.idle)
		select {
		case <-ctx.Done():
			stopTimer(timer)
			_ = r.Close()
			return nil, ctx.Err()
		case <-timer.C:
			_ = r.Close()
			return nil, connectorError(gatewayport.UpstreamTimeout, context.DeadlineExceeded)
		case chunk := <-r.chunks:
			stopTimer(timer)
			r.pending = append(r.pending, chunk.data...)
			if len(r.pending) > r.maxEvent {
				_ = r.Close()
				return nil, connectorError(gatewayport.UpstreamInvalidResponse, errBodyTooLarge)
			}
			if chunk.err != nil {
				if errors.Is(chunk.err, io.EOF) {
					r.terminal = io.EOF
				} else if strings.Contains(chunk.err.Error(), "closed") {
					r.terminal = io.ErrClosedPipe
				} else {
					r.terminal = chunk.err
				}
			}
		}
	}
}

func (r *sseReader) Close() error {
	if r == nil {
		return nil
	}
	r.closeOnce.Do(func() {
		close(r.done)
		r.closeErr = r.source.Close()
	})
	return r.closeErr
}

func stopTimer(timer *time.Timer) {
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
}
