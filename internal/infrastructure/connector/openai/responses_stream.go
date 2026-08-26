package openai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"

	gatewayport "github.com/goairix/llm-proxy/internal/application/gateway/port"
	inference "github.com/goairix/llm-proxy/internal/domain/inference/model"
	inferenceport "github.com/goairix/llm-proxy/internal/domain/inference/port"
)

type responsesStreamEvent struct {
	Type           string                 `json:"type"`
	SequenceNumber int64                  `json:"sequence_number"`
	OutputIndex    int                    `json:"output_index"`
	ContentIndex   int                    `json:"content_index"`
	Delta          string                 `json:"delta"`
	Arguments      string                 `json:"arguments"`
	Item           responsesOutputItem    `json:"item"`
	Part           responsesOutputContent `json:"part"`
	Response       responsesPayload       `json:"response"`
}

type responsesStream struct {
	recvMu       sync.Mutex
	reader       *sseReader
	invocation   gatewayport.Invocation
	start        inference.Event
	queue        []inference.Event
	sequenceSeen bool
	lastSequence int64
	started      bool
	terminal     bool
	nextBlock    int
	outputTypes  map[int]string
	outputBlocks map[int][]int
	content      map[[2]int]responsesContentState
	tools        map[int]*responsesToolState
	active       map[int]struct{}
	closed       atomic.Bool
	closeOnce    sync.Once
	closeErr     error
}

type responsesContentState struct {
	blockIndex int
	typeName   string
}

type responsesToolState struct {
	blockIndex int
	arguments  string
}

func newResponsesStream(reader *sseReader, invocation gatewayport.Invocation, idGenerator func() (uuid.UUID, error), clock func() time.Time) (inferenceport.Stream, error) {
	if reader == nil || idGenerator == nil || clock == nil {
		return nil, invalidResponsesResponse(fmt.Errorf("Responses stream dependencies are required"))
	}
	id, err := idGenerator()
	if err != nil {
		_ = reader.Close()
		return nil, invalidResponsesResponse(fmt.Errorf("generate response id: %w", err))
	}
	start := inference.NewResponseStartAt(id, invocation.Request.Model, clock().UTC())
	if err := start.Validate(); err != nil {
		_ = reader.Close()
		return nil, invalidResponsesResponse(err)
	}
	return &responsesStream{
		reader: reader, invocation: invocation, start: start, outputTypes: make(map[int]string),
		outputBlocks: make(map[int][]int), content: make(map[[2]int]responsesContentState),
		tools: make(map[int]*responsesToolState), active: make(map[int]struct{}),
	}, nil
}

func (s *responsesStream) Recv(ctx context.Context) (inference.Event, error) {
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
				return inference.Event{}, invalidResponsesResponse(fmt.Errorf("Responses stream ended before a terminal event"))
			}
			return inference.Event{}, err
		}
		if len(event.Data) == 0 {
			continue
		}
		if err := s.consumeEvent(event); err != nil {
			return inference.Event{}, err
		}
	}
	event := s.queue[0]
	s.queue = s.queue[1:]
	return event, nil
}

func (s *responsesStream) consumeEvent(source sseEvent) error {
	var event responsesStreamEvent
	if err := json.Unmarshal(source.Data, &event); err != nil {
		return invalidResponsesResponse(err)
	}
	if event.Type == "" || source.Event != "" && source.Event != event.Type {
		return invalidResponsesResponse(fmt.Errorf("SSE event name does not match payload type"))
	}
	if event.SequenceNumber < 0 {
		return invalidResponsesResponse(fmt.Errorf("sequence_number must not be negative"))
	}
	if s.sequenceSeen && event.SequenceNumber <= s.lastSequence {
		return invalidResponsesResponse(fmt.Errorf("sequence_number must increase"))
	}
	s.sequenceSeen, s.lastSequence = true, event.SequenceNumber
	if s.terminal {
		return invalidResponsesResponse(fmt.Errorf("event appeared after terminal event"))
	}

	switch event.Type {
	case "response.created":
		if s.started {
			return invalidResponsesResponse(fmt.Errorf("response was already created"))
		}
		s.started = true
		s.queue = append(s.queue, s.start)
	case "response.in_progress":
		if err := s.requireStarted(); err != nil {
			return err
		}
	case "response.output_item.added":
		if err := s.requireStarted(); err != nil {
			return err
		}
		if event.OutputIndex < 0 {
			return invalidResponsesResponse(fmt.Errorf("output index must not be negative"))
		}
		if _, exists := s.outputTypes[event.OutputIndex]; exists {
			return invalidResponsesResponse(fmt.Errorf("output index %d is duplicated", event.OutputIndex))
		}
		s.outputTypes[event.OutputIndex] = event.Item.Type
		switch event.Item.Type {
		case "message":
			if event.Item.Role != "assistant" {
				return invalidResponsesResponse(fmt.Errorf("output message role must be assistant"))
			}
		case "function_call":
			index := s.allocateBlock(event.OutputIndex)
			s.tools[event.OutputIndex] = &responsesToolState{blockIndex: index}
			s.active[index] = struct{}{}
			s.queue = append(s.queue, inference.NewToolCallStart(index, event.Item.CallID, event.Item.Name))
			if event.Item.Arguments != "" {
				s.tools[event.OutputIndex].arguments = event.Item.Arguments
				s.queue = append(s.queue, inference.NewToolArgumentsDelta(index, event.Item.Arguments))
			}
		case "reasoning":
		default:
			return invalidResponsesResponse(fmt.Errorf("unsupported output item type %q", event.Item.Type))
		}
	case "response.content_part.added":
		if event.OutputIndex < 0 || event.ContentIndex < 0 {
			return invalidResponsesResponse(fmt.Errorf("output and content indexes must not be negative"))
		}
		if s.outputTypes[event.OutputIndex] != "message" {
			return invalidResponsesResponse(fmt.Errorf("content part does not belong to a message"))
		}
		key := [2]int{event.OutputIndex, event.ContentIndex}
		if _, exists := s.content[key]; exists {
			return invalidResponsesResponse(fmt.Errorf("content part is duplicated"))
		}
		var contentType inference.ContentType
		switch event.Part.Type {
		case "output_text":
			contentType = inference.ContentText
		case "refusal":
			contentType = inference.ContentRefusal
		default:
			return invalidResponsesResponse(fmt.Errorf("unsupported content part type %q", event.Part.Type))
		}
		index := s.allocateBlock(event.OutputIndex)
		s.content[key] = responsesContentState{blockIndex: index, typeName: event.Part.Type}
		s.active[index] = struct{}{}
		s.queue = append(s.queue, inference.NewContentBlockStart(index, contentType))
		initial := event.Part.Text
		if event.Part.Type == "refusal" {
			initial = event.Part.Refusal
		}
		if initial != "" {
			s.appendContentDelta(index, event.Part.Type, initial)
		}
	case "response.output_text.delta", "response.refusal.delta":
		key := [2]int{event.OutputIndex, event.ContentIndex}
		state, exists := s.content[key]
		want := "output_text"
		if event.Type == "response.refusal.delta" {
			want = "refusal"
		}
		if !exists || state.typeName != want || event.Delta == "" {
			return invalidResponsesResponse(fmt.Errorf("delta does not match an active content part"))
		}
		s.appendContentDelta(state.blockIndex, want, event.Delta)
	case "response.function_call_arguments.delta":
		state := s.tools[event.OutputIndex]
		if state == nil || event.Delta == "" {
			return invalidResponsesResponse(fmt.Errorf("function arguments delta does not match an active call"))
		}
		state.arguments += event.Delta
		s.queue = append(s.queue, inference.NewToolArgumentsDelta(state.blockIndex, event.Delta))
	case "response.function_call_arguments.done":
		state := s.tools[event.OutputIndex]
		if state == nil || event.Arguments != state.arguments {
			return invalidResponsesResponse(fmt.Errorf("function arguments done does not match accumulated deltas"))
		}
	case "response.output_item.done":
		if err := s.finishOutputItem(event); err != nil {
			return err
		}
	case "response.content_part.done":
		key := [2]int{event.OutputIndex, event.ContentIndex}
		state, exists := s.content[key]
		if !exists {
			return invalidResponsesResponse(fmt.Errorf("content part done is unknown"))
		}
		s.closeBlock(state.blockIndex)
		delete(s.content, key)
	case "response.output_text.done", "response.refusal.done", "response.output_text.annotation.added",
		"response.reasoning_summary_part.added", "response.reasoning_summary_part.done",
		"response.reasoning_summary_text.delta", "response.reasoning_summary_text.done":
		// These events repeat final text or carry provider-only reasoning/annotation metadata.
	case "response.completed", "response.incomplete":
		if err := s.complete(event.Response); err != nil {
			return err
		}
	case "response.failed":
		s.queue = append(s.queue, inference.NewStreamError("供应商流式响应失败"))
		s.terminal = true
	default:
		return invalidResponsesResponse(fmt.Errorf("unsupported Responses stream event %q", event.Type))
	}
	return nil
}

func (s *responsesStream) finishOutputItem(event responsesStreamEvent) error {
	typeName, exists := s.outputTypes[event.OutputIndex]
	if !exists || event.Item.Type != "" && event.Item.Type != typeName {
		return invalidResponsesResponse(fmt.Errorf("output item done does not match an active item"))
	}
	switch typeName {
	case "message":
		for _, index := range s.outputBlocks[event.OutputIndex] {
			s.closeBlock(index)
		}
		for key := range s.content {
			if key[0] == event.OutputIndex {
				delete(s.content, key)
			}
		}
	case "function_call":
		state := s.tools[event.OutputIndex]
		if state == nil {
			return invalidResponsesResponse(fmt.Errorf("function call state is missing"))
		}
		if event.Item.Arguments != "" && state.arguments == "" {
			state.arguments = event.Item.Arguments
			s.queue = append(s.queue, inference.NewToolArgumentsDelta(state.blockIndex, event.Item.Arguments))
		} else if event.Item.Arguments != "" && event.Item.Arguments != state.arguments {
			return invalidResponsesResponse(fmt.Errorf("function call final arguments differ from deltas"))
		}
		s.closeBlock(state.blockIndex)
		delete(s.tools, event.OutputIndex)
	case "reasoning":
	}
	delete(s.outputTypes, event.OutputIndex)
	return nil
}

func (s *responsesStream) complete(payload responsesPayload) error {
	if err := s.requireStarted(); err != nil {
		return err
	}
	if len(s.active) != 0 || len(s.outputTypes) != 0 {
		return invalidResponsesResponse(fmt.Errorf("response completed with active output items"))
	}
	_, hasTool, err := decodeResponsesOutput(payload.Output)
	if err != nil {
		return err
	}
	reason, err := decodeResponsesStopReason(payload, hasTool)
	if err != nil {
		return err
	}
	if payload.Usage == nil {
		return invalidResponsesResponse(fmt.Errorf("completed response usage is required"))
	}
	usage, err := decodeResponsesUsage(*payload.Usage)
	if err != nil {
		return err
	}
	s.queue = append(s.queue, inference.NewUsageUpdate(usage), inference.NewResponseFinish(reason))
	s.terminal = true
	return nil
}

func (s *responsesStream) appendContentDelta(index int, typeName, delta string) {
	if typeName == "refusal" {
		s.queue = append(s.queue, inference.NewRefusalDelta(index, delta))
		return
	}
	s.queue = append(s.queue, inference.NewTextDelta(index, delta))
}

func (s *responsesStream) allocateBlock(outputIndex int) int {
	index := s.nextBlock
	s.nextBlock++
	s.outputBlocks[outputIndex] = append(s.outputBlocks[outputIndex], index)
	return index
}

func (s *responsesStream) closeBlock(index int) {
	if _, active := s.active[index]; !active {
		return
	}
	delete(s.active, index)
	s.queue = append(s.queue, inference.NewContentBlockStop(index))
}

func (s *responsesStream) requireStarted() error {
	if !s.started {
		return invalidResponsesResponse(fmt.Errorf("event appeared before response.created"))
	}
	return nil
}

func (s *responsesStream) Close() error {
	if s == nil {
		return nil
	}
	s.closeOnce.Do(func() { s.closed.Store(true); s.closeErr = s.reader.Close() })
	return s.closeErr
}

var _ inferenceport.Stream = (*responsesStream)(nil)
