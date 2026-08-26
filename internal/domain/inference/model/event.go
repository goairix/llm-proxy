package model

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

type EventType string

const (
	EventResponseStart      EventType = "response_start"
	EventContentBlockStart  EventType = "content_block_start"
	EventTextDelta          EventType = "text_delta"
	EventToolCallStart      EventType = "tool_call_start"
	EventToolArgumentsDelta EventType = "tool_arguments_delta"
	EventContentBlockStop   EventType = "content_block_stop"
	EventUsageUpdate        EventType = "usage_update"
	EventResponseFinish     EventType = "response_finish"
	EventStreamError        EventType = "stream_error"
)

type ResponseStartEvent struct {
	ID        uuid.UUID
	Model     string
	CreatedAt time.Time
}

type ContentBlockStartEvent struct {
	Index int
	Type  ContentType
}

type TextDeltaEvent struct {
	Index int
	Text  string
}

type ToolCallStartEvent struct {
	Index int
	ID    string
	Name  string
}

type ToolArgumentsDeltaEvent struct {
	Index int
	Delta string
}

type ContentBlockStopEvent struct {
	Index int
}

type UsageUpdateEvent struct {
	Usage Usage
}

type ResponseFinishEvent struct {
	StopReason StopReason
}

type StreamErrorEvent struct {
	Message string
}

type Event struct {
	Type               EventType
	ResponseStart      *ResponseStartEvent
	ContentBlockStart  *ContentBlockStartEvent
	TextDelta          *TextDeltaEvent
	ToolCallStart      *ToolCallStartEvent
	ToolArgumentsDelta *ToolArgumentsDeltaEvent
	ContentBlockStop   *ContentBlockStopEvent
	UsageUpdate        *UsageUpdateEvent
	ResponseFinish     *ResponseFinishEvent
	StreamError        *StreamErrorEvent
}

func NewResponseStart(id uuid.UUID, model string) Event {
	return Event{Type: EventResponseStart, ResponseStart: &ResponseStartEvent{ID: id, Model: model}}
}

func NewResponseStartAt(id uuid.UUID, model string, createdAt time.Time) Event {
	return Event{Type: EventResponseStart, ResponseStart: &ResponseStartEvent{ID: id, Model: model, CreatedAt: createdAt}}
}

func NewContentBlockStart(index int, contentType ContentType) Event {
	return Event{Type: EventContentBlockStart, ContentBlockStart: &ContentBlockStartEvent{Index: index, Type: contentType}}
}

func NewTextDelta(index int, delta string) Event {
	return Event{Type: EventTextDelta, TextDelta: &TextDeltaEvent{Index: index, Text: delta}}
}

func NewToolCallStart(index int, id, name string) Event {
	return Event{Type: EventToolCallStart, ToolCallStart: &ToolCallStartEvent{Index: index, ID: id, Name: name}}
}

func NewToolArgumentsDelta(index int, delta string) Event {
	return Event{Type: EventToolArgumentsDelta, ToolArgumentsDelta: &ToolArgumentsDeltaEvent{Index: index, Delta: delta}}
}

func NewContentBlockStop(index int) Event {
	return Event{Type: EventContentBlockStop, ContentBlockStop: &ContentBlockStopEvent{Index: index}}
}

func NewUsageUpdate(usage Usage) Event {
	return Event{Type: EventUsageUpdate, UsageUpdate: &UsageUpdateEvent{Usage: usage}}
}

func NewResponseFinish(reason StopReason) Event {
	return Event{Type: EventResponseFinish, ResponseFinish: &ResponseFinishEvent{StopReason: reason}}
}

func NewStreamError(message string) Event {
	return Event{Type: EventStreamError, StreamError: &StreamErrorEvent{Message: message}}
}

func (e Event) Validate() error {
	payloads := 0
	for _, present := range []bool{
		e.ResponseStart != nil, e.ContentBlockStart != nil, e.TextDelta != nil, e.ToolCallStart != nil,
		e.ToolArgumentsDelta != nil, e.ContentBlockStop != nil, e.UsageUpdate != nil,
		e.ResponseFinish != nil, e.StreamError != nil,
	} {
		if present {
			payloads++
		}
	}
	if payloads != 1 {
		return fmt.Errorf("event must contain exactly one payload")
	}

	switch e.Type {
	case EventResponseStart:
		if e.ResponseStart == nil || e.ResponseStart.ID == uuid.Nil || e.ResponseStart.ID.Version() != uuid.Version(7) || strings.TrimSpace(e.ResponseStart.Model) == "" {
			return fmt.Errorf("response start requires UUIDv7 id and model")
		}
	case EventContentBlockStart:
		if e.ContentBlockStart == nil || e.ContentBlockStart.Index < 0 || e.ContentBlockStart.Type != ContentText {
			return fmt.Errorf("content block start requires a non-negative text block index")
		}
	case EventTextDelta:
		if e.TextDelta == nil || e.TextDelta.Index < 0 || e.TextDelta.Text == "" {
			return fmt.Errorf("text delta requires a non-negative index and text")
		}
	case EventToolCallStart:
		if e.ToolCallStart == nil || e.ToolCallStart.Index < 0 || strings.TrimSpace(e.ToolCallStart.ID) == "" || strings.TrimSpace(e.ToolCallStart.Name) == "" {
			return fmt.Errorf("tool call start requires index, id, and name")
		}
	case EventToolArgumentsDelta:
		if e.ToolArgumentsDelta == nil || e.ToolArgumentsDelta.Index < 0 || e.ToolArgumentsDelta.Delta == "" {
			return fmt.Errorf("tool arguments delta requires index and data")
		}
	case EventContentBlockStop:
		if e.ContentBlockStop == nil || e.ContentBlockStop.Index < 0 {
			return fmt.Errorf("content block stop requires a non-negative index")
		}
	case EventUsageUpdate:
		if e.UsageUpdate == nil {
			return fmt.Errorf("usage update payload is required")
		}
		if err := e.UsageUpdate.Usage.Validate(); err != nil {
			return err
		}
	case EventResponseFinish:
		if e.ResponseFinish == nil {
			return fmt.Errorf("response finish payload is required")
		}
		if err := e.ResponseFinish.StopReason.Validate(); err != nil {
			return err
		}
	case EventStreamError:
		if e.StreamError == nil || strings.TrimSpace(e.StreamError.Message) == "" {
			return fmt.Errorf("stream error requires a safe message")
		}
	default:
		return fmt.Errorf("unsupported event type %q", e.Type)
	}
	return nil
}

type SequenceValidator struct {
	started   bool
	terminal  bool
	active    map[int]ContentType
	closed    map[int]struct{}
	toolArgs  map[int]*strings.Builder
	toolIDs   map[string]struct{}
	usage     Usage
	usageSeen bool
}

func NewSequenceValidator() *SequenceValidator {
	return &SequenceValidator{
		active: make(map[int]ContentType), closed: make(map[int]struct{}),
		toolArgs: make(map[int]*strings.Builder), toolIDs: make(map[string]struct{}),
	}
}

func (v *SequenceValidator) Push(event Event) error {
	if v == nil {
		return fmt.Errorf("sequence validator is nil")
	}
	if v.active == nil {
		v.active = make(map[int]ContentType)
	}
	if v.closed == nil {
		v.closed = make(map[int]struct{})
	}
	if v.toolArgs == nil {
		v.toolArgs = make(map[int]*strings.Builder)
	}
	if v.toolIDs == nil {
		v.toolIDs = make(map[string]struct{})
	}
	if v.terminal {
		return fmt.Errorf("event %q appeared after terminal event", event.Type)
	}
	if err := event.Validate(); err != nil {
		return err
	}

	switch event.Type {
	case EventResponseStart:
		if v.started {
			return fmt.Errorf("response already started")
		}
		v.started = true
	case EventContentBlockStart:
		if err := v.startBlock(event.ContentBlockStart.Index, ContentText); err != nil {
			return err
		}
	case EventToolCallStart:
		if _, duplicate := v.toolIDs[event.ToolCallStart.ID]; duplicate {
			return fmt.Errorf("tool call id %q is duplicated", event.ToolCallStart.ID)
		}
		if err := v.startBlock(event.ToolCallStart.Index, ContentToolCall); err != nil {
			return err
		}
		v.toolIDs[event.ToolCallStart.ID] = struct{}{}
		v.toolArgs[event.ToolCallStart.Index] = &strings.Builder{}
	case EventTextDelta:
		if err := v.requireBlock(event.TextDelta.Index, ContentText); err != nil {
			return err
		}
	case EventToolArgumentsDelta:
		if err := v.requireBlock(event.ToolArgumentsDelta.Index, ContentToolCall); err != nil {
			return err
		}
		v.toolArgs[event.ToolArgumentsDelta.Index].WriteString(event.ToolArgumentsDelta.Delta)
	case EventContentBlockStop:
		index := event.ContentBlockStop.Index
		contentType, exists := v.active[index]
		if !exists {
			return fmt.Errorf("content block %d is not active", index)
		}
		if contentType == ContentToolCall {
			if err := validateJSONObject(json.RawMessage(v.toolArgs[index].String()), "tool call arguments"); err != nil {
				return fmt.Errorf("content block %d: %w", index, err)
			}
			delete(v.toolArgs, index)
		}
		delete(v.active, index)
		v.closed[index] = struct{}{}
	case EventUsageUpdate:
		if !v.started {
			return fmt.Errorf("usage update appeared before response start")
		}
		next := event.UsageUpdate.Usage
		if v.usageSeen && usageDecreased(v.usage, next) {
			return fmt.Errorf("usage update must be cumulative")
		}
		v.usage, v.usageSeen = next, true
	case EventResponseFinish:
		if !v.started {
			return fmt.Errorf("response finish appeared before response start")
		}
		if len(v.active) != 0 {
			return fmt.Errorf("response finished with active content blocks")
		}
		v.terminal = true
	case EventStreamError:
		v.terminal = true
	}
	return nil
}

func (v *SequenceValidator) startBlock(index int, contentType ContentType) error {
	if !v.started {
		return fmt.Errorf("content block %d started before response", index)
	}
	if _, active := v.active[index]; active {
		return fmt.Errorf("content block %d already started", index)
	}
	if _, closed := v.closed[index]; closed {
		return fmt.Errorf("content block %d cannot be reused", index)
	}
	v.active[index] = contentType
	return nil
}

func (v *SequenceValidator) requireBlock(index int, contentType ContentType) error {
	actual, exists := v.active[index]
	if !exists || actual != contentType {
		return fmt.Errorf("content block %d is not an active %s block", index, contentType)
	}
	return nil
}

func (v *SequenceValidator) Finished() bool { return v != nil && v.terminal }

func (v *SequenceValidator) ValidateEOF() error {
	if v == nil || !v.terminal {
		return fmt.Errorf("event stream ended before a terminal event")
	}
	return nil
}

func ValidateEventSequence(events []Event) error {
	validator := NewSequenceValidator()
	for index, event := range events {
		if err := validator.Push(event); err != nil {
			return fmt.Errorf("event %d: %w", index, err)
		}
	}
	return validator.ValidateEOF()
}

func usageDecreased(previous, next Usage) bool {
	return next.InputTokens < previous.InputTokens || next.OutputTokens < previous.OutputTokens ||
		next.CacheReadInputTokens < previous.CacheReadInputTokens || next.CacheWriteInputTokens < previous.CacheWriteInputTokens
}
