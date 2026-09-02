package fake

import (
	"context"
	"io"
	"sync"
	"unicode/utf8"

	inference "github.com/goairix/llm-proxy/internal/domain/inference/model"
	inferenceport "github.com/goairix/llm-proxy/internal/domain/inference/port"
)

type eventStream struct {
	mu           sync.Mutex
	events       []inference.Event
	index        int
	delivered    int
	errorAfter   int
	errorMessage string
	errorSent    bool
	closed       bool
}

func (s *eventStream) Recv(ctx context.Context) (inference.Event, error) {
	if err := ctx.Err(); err != nil {
		return inference.Event{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return inference.Event{}, err
	}
	if s.closed || s.errorSent {
		return inference.Event{}, io.EOF
	}
	if s.index >= len(s.events) {
		return inference.Event{}, io.EOF
	}
	if s.errorAfter > 0 && s.delivered >= s.errorAfter {
		s.errorSent = true
		return inference.NewStreamError(s.errorMessage), nil
	}
	event := s.events[s.index]
	s.index++
	s.delivered++
	return event, nil
}

func (s *eventStream) Close() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	return nil
}

func responseEvents(response inference.Response, chunkSize int) []inference.Event {
	events := []inference.Event{inference.NewResponseStartAt(response.ID, response.Model, response.CreatedAt)}
	for index, block := range response.Content {
		switch block.Type {
		case inference.ContentText:
			events = append(events, inference.NewContentBlockStart(index, inference.ContentText))
			for _, chunk := range chunkText(block.Text.Text, chunkSize) {
				events = append(events, inference.NewTextDelta(index, chunk))
			}
		case inference.ContentRefusal:
			events = append(events, inference.NewContentBlockStart(index, inference.ContentRefusal))
			for _, chunk := range chunkText(block.Refusal.Text, chunkSize) {
				events = append(events, inference.NewRefusalDelta(index, chunk))
			}
		case inference.ContentToolCall:
			events = append(events, inference.NewToolCallStart(index, block.ToolCall.ID, block.ToolCall.Name))
			for _, chunk := range chunkText(string(block.ToolCall.Arguments), chunkSize) {
				events = append(events, inference.NewToolArgumentsDelta(index, chunk))
			}
		}
		events = append(events, inference.NewContentBlockStop(index))
	}
	events = append(events, inference.NewUsageUpdate(response.Usage), inference.NewResponseFinish(response.StopReason))
	return events
}

func chunkText(value string, size int) []string {
	if value == "" {
		return nil
	}
	if size <= 0 {
		size = utf8.RuneCountInString(value)
	}
	runes := []rune(value)
	chunks := make([]string, 0, (len(runes)+size-1)/size)
	for start := 0; start < len(runes); start += size {
		end := min(start+size, len(runes))
		chunks = append(chunks, string(runes[start:end]))
	}
	return chunks
}

var _ inferenceport.Stream = (*eventStream)(nil)
