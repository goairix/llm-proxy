package openai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"

	gatewayport "github.com/goairix/llm-proxy/internal/application/gateway/port"
	inference "github.com/goairix/llm-proxy/internal/domain/inference/model"
	inferenceport "github.com/goairix/llm-proxy/internal/domain/inference/port"
)

type chatStream struct {
	recvMu        sync.Mutex
	reader        *sseReader
	start         inference.Event
	queue         []inference.Event
	started       bool
	terminal      bool
	finished      bool
	closed        atomic.Bool
	closeOnce     sync.Once
	closeErr      error
	nextBlock     int
	textIndex     *int
	refusalIndex  *int
	tools         map[int]*chatStreamTool
	active        map[int]struct{}
	pendingReason *inference.StopReason
	usage         *inference.Usage
	usageSent     bool
}

type chatStreamTool struct {
	blockIndex int
	id         string
	name       string
	started    bool
}

func newChatStream(reader *sseReader, invocation gatewayport.Invocation, idGenerator func() (uuid.UUID, error), clock func() time.Time) (inferenceport.Stream, error) {
	if reader == nil || idGenerator == nil || clock == nil {
		return nil, invalidChatResponse(fmt.Errorf("chat stream dependencies are required"))
	}
	id, err := idGenerator()
	if err != nil {
		_ = reader.Close()
		return nil, invalidChatResponse(fmt.Errorf("generate response id: %w", err))
	}
	start := inference.NewResponseStartAt(id, invocation.Request.Model, clock().UTC())
	if err := start.Validate(); err != nil {
		_ = reader.Close()
		return nil, invalidChatResponse(err)
	}
	return &chatStream{reader: reader, start: start, tools: make(map[int]*chatStreamTool), active: make(map[int]struct{})}, nil
}

func (s *chatStream) Recv(ctx context.Context) (inference.Event, error) {
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
		event, err := s.reader.Next(ctx)
		if err != nil {
			if errors.Is(err, io.EOF) {
				return inference.Event{}, invalidChatResponse(fmt.Errorf("stream ended before [DONE]"))
			}
			return inference.Event{}, classifyStreamReadError(ctx, err)
		}
		if string(event.Data) == "[DONE]" {
			if !s.finished {
				if err := s.finish(); err != nil {
					return inference.Event{}, err
				}
			}
			s.terminal = true
			break
		}
		if len(event.Data) == 0 {
			continue
		}
		if err := s.consumeChunk(event.Data); err != nil {
			return inference.Event{}, err
		}
	}
	if len(s.queue) == 0 {
		return inference.Event{}, io.EOF
	}
	event := s.queue[0]
	s.queue = s.queue[1:]
	return event, nil
}

func (s *chatStream) consumeChunk(data []byte) error {
	var chunk chatResponsePayload
	if err := json.Unmarshal(data, &chunk); err != nil {
		return invalidChatResponse(err)
	}
	if !s.started {
		s.started = true
		s.queue = append(s.queue, s.start)
	}
	if len(chunk.Choices) > 1 || len(chunk.Choices) == 1 && chunk.Choices[0].Index != 0 {
		return invalidChatResponse(fmt.Errorf("stream requires at most choice index 0"))
	}
	if len(chunk.Choices) == 1 {
		choice := chunk.Choices[0]
		if choice.Delta.Role != "" && choice.Delta.Role != "assistant" {
			return invalidChatResponse(fmt.Errorf("delta role must be assistant"))
		}
		if choice.FinishReason != nil {
			reason, err := decodeChatFinishReason(choice.FinishReason)
			if err != nil {
				return err
			}
			s.pendingReason = &reason
		}
		if choice.Delta.Content != nil && *choice.Delta.Content != "" {
			index := s.ensureContentBlock(&s.textIndex, inference.ContentText)
			s.queue = append(s.queue, inference.NewTextDelta(index, *choice.Delta.Content))
		}
		if choice.Delta.Refusal != nil && *choice.Delta.Refusal != "" {
			index := s.ensureContentBlock(&s.refusalIndex, inference.ContentRefusal)
			s.queue = append(s.queue, inference.NewRefusalDelta(index, *choice.Delta.Refusal))
		}
		for _, call := range choice.Delta.ToolCalls {
			if call.Index == nil {
				return invalidChatResponse(fmt.Errorf("tool call delta index is required"))
			}
			state, exists := s.tools[*call.Index]
			if !exists {
				state = &chatStreamTool{blockIndex: s.allocateBlock()}
				s.tools[*call.Index] = state
			}
			if call.Type != "" && call.Type != "function" {
				return invalidChatResponse(fmt.Errorf("unsupported tool call type %q", call.Type))
			}
			if call.ID != "" {
				state.id = call.ID
			}
			if call.Function.Name != "" {
				state.name = call.Function.Name
			}
			if !state.started {
				if state.id == "" || state.name == "" {
					return invalidChatResponse(fmt.Errorf("tool call delta must provide id and name before arguments"))
				}
				state.started = true
				s.active[state.blockIndex] = struct{}{}
				s.queue = append(s.queue, inference.NewToolCallStart(state.blockIndex, state.id, state.name))
			}
			if call.Function.Arguments != "" {
				s.queue = append(s.queue, inference.NewToolArgumentsDelta(state.blockIndex, call.Function.Arguments))
			}
		}
	}
	if chunk.Usage != nil {
		usage, err := decodeChatUsage(*chunk.Usage)
		if err != nil {
			return err
		}
		s.usage = &usage
	}
	if s.pendingReason != nil && s.usage != nil {
		return s.finish()
	}
	return nil
}

func (s *chatStream) ensureContentBlock(target **int, contentType inference.ContentType) int {
	if *target != nil {
		return **target
	}
	index := s.allocateBlock()
	*target = &index
	s.active[index] = struct{}{}
	s.queue = append(s.queue, inference.NewContentBlockStart(index, contentType))
	return index
}

func (s *chatStream) allocateBlock() int { index := s.nextBlock; s.nextBlock++; return index }

func (s *chatStream) finish() error {
	if s.finished {
		return nil
	}
	if s.pendingReason == nil {
		return invalidChatResponse(fmt.Errorf("[DONE] appeared before finish_reason"))
	}
	indices := make([]int, 0, len(s.active))
	for index := range s.active {
		indices = append(indices, index)
	}
	sort.Ints(indices)
	for _, index := range indices {
		s.queue = append(s.queue, inference.NewContentBlockStop(index))
		delete(s.active, index)
	}
	if s.usage != nil && !s.usageSent {
		s.queue = append(s.queue, inference.NewUsageUpdate(*s.usage))
		s.usageSent = true
	}
	s.queue = append(s.queue, inference.NewResponseFinish(*s.pendingReason))
	s.pendingReason = nil
	s.finished = true
	return nil
}

func (s *chatStream) Close() error {
	if s == nil {
		return nil
	}
	s.closeOnce.Do(func() {
		s.closed.Store(true)
		s.closeErr = s.reader.Close()
	})
	return s.closeErr
}

var _ inferenceport.Stream = (*chatStream)(nil)
