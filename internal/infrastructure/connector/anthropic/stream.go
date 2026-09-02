package anthropic

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"

	gatewayport "github.com/goairix/llm-proxy/internal/application/gateway/port"
	inference "github.com/goairix/llm-proxy/internal/domain/inference/model"
	inferenceport "github.com/goairix/llm-proxy/internal/domain/inference/port"
)

type messageStreamEvent struct {
	Type         string             `json:"type"`
	Message      streamMessage      `json:"message"`
	Index        int                `json:"index"`
	ContentBlock streamContentBlock `json:"content_block"`
	Delta        streamDelta        `json:"delta"`
	Usage        streamUsage        `json:"usage"`
}

type streamMessage struct {
	ID         string            `json:"id"`
	Type       string            `json:"type"`
	Role       string            `json:"role"`
	Model      string            `json:"model"`
	Content    []json.RawMessage `json:"content"`
	StopReason *string           `json:"stop_reason"`
	Usage      streamUsage       `json:"usage"`
}

type streamContentBlock struct {
	Type  string          `json:"type"`
	Text  string          `json:"text"`
	ID    string          `json:"id"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`
}

type streamDelta struct {
	Type        string             `json:"type"`
	Text        string             `json:"text"`
	PartialJSON string             `json:"partial_json"`
	StopReason  *string            `json:"stop_reason"`
	StopDetails *streamStopDetails `json:"stop_details"`
}

type streamStopDetails struct {
	Type        string `json:"type"`
	Category    string `json:"category"`
	Explanation string `json:"explanation"`
}

type streamUsage struct {
	InputTokens              *int64 `json:"input_tokens"`
	OutputTokens             *int64 `json:"output_tokens"`
	CacheReadInputTokens     *int64 `json:"cache_read_input_tokens"`
	CacheCreationInputTokens *int64 `json:"cache_creation_input_tokens"`
}

type messagesStream struct {
	recvMu        sync.Mutex
	reader        *sseReader
	start         inference.Event
	queue         []inference.Event
	started       bool
	terminal      bool
	closed        atomic.Bool
	closeOnce     sync.Once
	closeErr      error
	active        map[int]*streamBlock
	closedIndexes map[int]struct{}
	nextIndex     int
	stopReason    *inference.StopReason
	usage         inference.Usage
	usageSeen     bool
	refusalQueued bool
}

type streamBlock struct {
	contentType inference.ContentType
	arguments   strings.Builder
}

func newMessagesStream(
	reader *sseReader,
	invocation gatewayport.Invocation,
	idGenerator func() (uuid.UUID, error),
	clock func() time.Time,
) (inferenceport.Stream, error) {
	if reader == nil || idGenerator == nil || clock == nil {
		return nil, invalidResponseError(fmt.Errorf("Anthropic stream dependencies are required"))
	}
	id, err := idGenerator()
	if err != nil {
		_ = reader.Close()
		return nil, invalidResponseError(fmt.Errorf("generate response id: %w", err))
	}
	start := inference.NewResponseStartAt(id, invocation.Request.Model, clock().UTC())
	if err := start.Validate(); err != nil {
		_ = reader.Close()
		return nil, invalidResponseError(err)
	}
	return &messagesStream{
		reader: reader, start: start, active: make(map[int]*streamBlock),
		closedIndexes: make(map[int]struct{}),
	}, nil
}

func (s *messagesStream) Recv(ctx context.Context) (inference.Event, error) {
	if err := ctx.Err(); err != nil {
		return inference.Event{}, err
	}
	s.recvMu.Lock()
	defer s.recvMu.Unlock()
	if err := ctx.Err(); err != nil {
		return inference.Event{}, err
	}
	if s.closed.Load() || s.terminal && len(s.queue) == 0 {
		return inference.Event{}, io.EOF
	}
	for len(s.queue) == 0 {
		source, err := s.reader.Next(ctx)
		if err != nil {
			if s.closed.Load() {
				return inference.Event{}, io.EOF
			}
			if errors.Is(err, io.EOF) {
				return inference.Event{}, invalidResponseError(fmt.Errorf("Anthropic stream ended before message_stop"))
			}
			var connectorErr *gatewayport.ConnectorError
			if errors.As(err, &connectorErr) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return inference.Event{}, err
			}
			return inference.Event{}, invalidResponseError(errors.New("read Anthropic stream"))
		}
		if len(source.Data) == 0 {
			continue
		}
		if err := s.consumeEvent(source); err != nil {
			return inference.Event{}, err
		}
	}
	event := s.queue[0]
	s.queue = s.queue[1:]
	return event, nil
}

func (s *messagesStream) consumeEvent(source sseEvent) error {
	var header struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(source.Data, &header); err != nil {
		return invalidResponseError(err)
	}
	if header.Type == "" || source.Event != "" && source.Event != header.Type {
		return invalidResponseError(fmt.Errorf("SSE event name does not match payload type"))
	}
	if s.terminal {
		return invalidResponseError(fmt.Errorf("event appeared after terminal event"))
	}
	if header.Type == "ping" {
		return nil
	}
	if header.Type == "error" {
		s.queue = append(s.queue, inference.NewStreamError("供应商流式响应失败"))
		s.terminal = true
		return nil
	}

	var event messageStreamEvent
	if err := json.Unmarshal(source.Data, &event); err != nil {
		return invalidResponseError(err)
	}
	switch event.Type {
	case "message_start":
		return s.consumeMessageStart(event.Message)
	case "content_block_start":
		return s.consumeContentBlockStart(event.Index, event.ContentBlock)
	case "content_block_delta":
		return s.consumeContentBlockDelta(event.Index, event.Delta)
	case "content_block_stop":
		return s.consumeContentBlockStop(event.Index)
	case "message_delta":
		return s.consumeMessageDelta(event.Delta, event.Usage)
	case "message_stop":
		return s.consumeMessageStop()
	default:
		return nil
	}
}

func (s *messagesStream) consumeMessageStart(message streamMessage) error {
	if s.started {
		return invalidResponseError(fmt.Errorf("Anthropic message already started"))
	}
	if message.Type != "message" || message.Role != "assistant" || strings.TrimSpace(message.ID) == "" ||
		strings.TrimSpace(message.Model) == "" || len(message.Content) != 0 || message.StopReason != nil {
		return invalidResponseError(fmt.Errorf("Anthropic message_start envelope is invalid"))
	}
	if message.Usage.InputTokens == nil || message.Usage.OutputTokens == nil {
		return invalidResponseError(fmt.Errorf("Anthropic message_start usage is incomplete"))
	}
	usage, err := mergeUsage(inference.Usage{}, message.Usage)
	if err != nil {
		return err
	}
	s.started = true
	s.usage, s.usageSeen = usage, true
	s.queue = append(s.queue, s.start, inference.NewUsageUpdate(usage))
	return nil
}

func (s *messagesStream) consumeContentBlockStart(index int, content streamContentBlock) error {
	if err := s.requireStarted(); err != nil {
		return err
	}
	if s.stopReason != nil || index < 0 {
		return invalidResponseError(fmt.Errorf("content block start is out of sequence"))
	}
	if _, exists := s.active[index]; exists {
		return invalidResponseError(fmt.Errorf("content block index %d is duplicated", index))
	}
	if _, exists := s.closedIndexes[index]; exists {
		return invalidResponseError(fmt.Errorf("content block index %d cannot be reused", index))
	}
	if index >= s.nextIndex {
		s.nextIndex = index + 1
	}
	block := &streamBlock{}
	switch content.Type {
	case "text":
		if content.Text != "" {
			return invalidResponseError(fmt.Errorf("text content block %d must start empty", index))
		}
		block.contentType = inference.ContentText
		s.queue = append(s.queue, inference.NewContentBlockStart(index, inference.ContentText))
	case "tool_use":
		if strings.TrimSpace(content.ID) == "" || strings.TrimSpace(content.Name) == "" || !emptyJSONObject(content.Input) {
			return invalidResponseError(fmt.Errorf("tool content block %d is invalid", index))
		}
		block.contentType = inference.ContentToolCall
		s.queue = append(s.queue, inference.NewToolCallStart(index, content.ID, content.Name))
	default:
		return invalidResponseError(fmt.Errorf("unsupported Anthropic content type %q at index %d", content.Type, index))
	}
	s.active[index] = block
	return nil
}

func (s *messagesStream) consumeContentBlockDelta(index int, delta streamDelta) error {
	if err := s.requireStarted(); err != nil {
		return err
	}
	block := s.active[index]
	if block == nil {
		return invalidResponseError(fmt.Errorf("content block delta index %d is not active", index))
	}
	switch delta.Type {
	case "text_delta":
		if block.contentType != inference.ContentText || delta.Text == "" {
			return invalidResponseError(fmt.Errorf("text delta does not match content block %d", index))
		}
		s.queue = append(s.queue, inference.NewTextDelta(index, delta.Text))
	case "input_json_delta":
		if block.contentType != inference.ContentToolCall {
			return invalidResponseError(fmt.Errorf("tool delta does not match content block %d", index))
		}
		if delta.PartialJSON != "" {
			block.arguments.WriteString(delta.PartialJSON)
			s.queue = append(s.queue, inference.NewToolArgumentsDelta(index, delta.PartialJSON))
		}
	default:
		return invalidResponseError(fmt.Errorf("unsupported Anthropic delta type %q at index %d", delta.Type, index))
	}
	return nil
}

func (s *messagesStream) consumeContentBlockStop(index int) error {
	if err := s.requireStarted(); err != nil {
		return err
	}
	block := s.active[index]
	if block == nil {
		return invalidResponseError(fmt.Errorf("content block stop index %d is not active", index))
	}
	if block.contentType == inference.ContentToolCall {
		if block.arguments.Len() == 0 {
			block.arguments.WriteString("{}")
			s.queue = append(s.queue, inference.NewToolArgumentsDelta(index, "{}"))
		}
		var object map[string]json.RawMessage
		if err := json.Unmarshal([]byte(block.arguments.String()), &object); err != nil || object == nil {
			return invalidResponseError(fmt.Errorf("tool content block %d arguments are invalid", index))
		}
	}
	delete(s.active, index)
	s.closedIndexes[index] = struct{}{}
	s.queue = append(s.queue, inference.NewContentBlockStop(index))
	return nil
}

func (s *messagesStream) consumeMessageDelta(delta streamDelta, usageDelta streamUsage) error {
	if err := s.requireStarted(); err != nil {
		return err
	}
	if len(s.active) != 0 || usageDelta.OutputTokens == nil {
		return invalidResponseError(fmt.Errorf("Anthropic message_delta is incomplete or out of sequence"))
	}
	if delta.StopReason != nil {
		reason, err := mapStopReason(*delta.StopReason)
		if err != nil {
			return err
		}
		if s.stopReason != nil && *s.stopReason != reason {
			return invalidResponseError(fmt.Errorf("Anthropic stop reason changed"))
		}
		s.stopReason = &reason
		if reason == inference.StopContentFilter {
			if delta.StopDetails != nil && delta.StopDetails.Type != "" && delta.StopDetails.Type != "refusal" {
				return invalidResponseError(fmt.Errorf("Anthropic refusal stop details are invalid"))
			}
			if !s.refusalQueued && delta.StopDetails != nil {
				if explanation := strings.TrimSpace(delta.StopDetails.Explanation); explanation != "" {
					index := s.nextIndex
					s.nextIndex++
					s.closedIndexes[index] = struct{}{}
					s.queue = append(s.queue,
						inference.NewContentBlockStart(index, inference.ContentRefusal),
						inference.NewRefusalDelta(index, explanation),
						inference.NewContentBlockStop(index),
					)
					s.refusalQueued = true
				}
			}
		} else if delta.StopDetails != nil {
			return invalidResponseError(fmt.Errorf("Anthropic stop details appeared without refusal"))
		}
	}
	usage, err := mergeUsage(s.usage, usageDelta)
	if err != nil {
		return err
	}
	s.usage, s.usageSeen = usage, true
	s.queue = append(s.queue, inference.NewUsageUpdate(usage))
	return nil
}

func (s *messagesStream) consumeMessageStop() error {
	if err := s.requireStarted(); err != nil {
		return err
	}
	if len(s.active) != 0 || s.stopReason == nil || !s.usageSeen {
		return invalidResponseError(fmt.Errorf("Anthropic message_stop appeared before completion"))
	}
	s.queue = append(s.queue, inference.NewResponseFinish(*s.stopReason))
	s.terminal = true
	return nil
}

func (s *messagesStream) requireStarted() error {
	if !s.started {
		return invalidResponseError(fmt.Errorf("Anthropic event appeared before message_start"))
	}
	return nil
}

func mergeUsage(current inference.Usage, delta streamUsage) (inference.Usage, error) {
	next := current
	if delta.InputTokens != nil {
		next.InputTokens = *delta.InputTokens
	}
	if delta.OutputTokens != nil {
		next.OutputTokens = *delta.OutputTokens
	}
	if delta.CacheReadInputTokens != nil {
		next.CacheReadInputTokens = *delta.CacheReadInputTokens
	}
	if delta.CacheCreationInputTokens != nil {
		next.CacheWriteInputTokens = *delta.CacheCreationInputTokens
	}
	if err := next.Validate(); err != nil {
		return inference.Usage{}, invalidResponseError(err)
	}
	if next.InputTokens < current.InputTokens || next.OutputTokens < current.OutputTokens ||
		next.CacheReadInputTokens < current.CacheReadInputTokens || next.CacheWriteInputTokens < current.CacheWriteInputTokens {
		return inference.Usage{}, invalidResponseError(fmt.Errorf("Anthropic usage decreased"))
	}
	return next, nil
}

func emptyJSONObject(value json.RawMessage) bool {
	var object map[string]json.RawMessage
	return json.Unmarshal(value, &object) == nil && object != nil && len(object) == 0
}

func (s *messagesStream) Close() error {
	if s == nil {
		return nil
	}
	s.closeOnce.Do(func() {
		s.closed.Store(true)
		s.closeErr = s.reader.Close()
	})
	return s.closeErr
}

var _ inferenceport.Stream = (*messagesStream)(nil)
