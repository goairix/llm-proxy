package model

import (
	"testing"

	"github.com/google/uuid"
)

func TestEventSequenceAcceptsTextAndToolCalls(t *testing.T) {
	events := []Event{
		NewResponseStart(uuid.Must(uuid.NewV7()), "fake-model"),
		NewContentBlockStart(0, ContentText),
		NewTextDelta(0, "hello"),
		NewContentBlockStop(0),
		NewToolCallStart(1, "call_1", "weather"),
		NewToolArgumentsDelta(1, `{"city":`),
		NewToolArgumentsDelta(1, `"Shanghai"}`),
		NewContentBlockStop(1),
		NewUsageUpdate(Usage{InputTokens: 10, OutputTokens: 5}),
		NewResponseFinish(StopToolUse),
	}
	if err := ValidateEventSequence(events); err != nil {
		t.Fatal(err)
	}
}

func TestEventSequenceRejectsInvalidOrdering(t *testing.T) {
	id := uuid.Must(uuid.NewV7())
	tests := []struct {
		name   string
		events []Event
	}{
		{name: "delta before response start", events: []Event{NewTextDelta(0, "hello")}},
		{name: "delta before block start", events: []Event{NewResponseStart(id, "fake"), NewTextDelta(0, "hello")}},
		{name: "duplicate stop", events: []Event{
			NewResponseStart(id, "fake"), NewContentBlockStart(0, ContentText), NewContentBlockStop(0), NewContentBlockStop(0),
		}},
		{name: "finish with open block", events: []Event{
			NewResponseStart(id, "fake"), NewContentBlockStart(0, ContentText), NewResponseFinish(StopEndTurn),
		}},
		{name: "event after finish", events: []Event{
			NewResponseStart(id, "fake"), NewUsageUpdate(Usage{}), NewResponseFinish(StopEndTurn), NewUsageUpdate(Usage{}),
		}},
		{name: "decreasing usage", events: []Event{
			NewResponseStart(id, "fake"), NewUsageUpdate(Usage{InputTokens: 2}), NewUsageUpdate(Usage{InputTokens: 1}), NewResponseFinish(StopEndTurn),
		}},
		{name: "invalid completed tool arguments", events: []Event{
			NewResponseStart(id, "fake"), NewToolCallStart(0, "call_1", "weather"), NewToolArgumentsDelta(0, `{"city":`),
			NewContentBlockStop(0), NewResponseFinish(StopToolUse),
		}},
		{name: "duplicate tool call id", events: []Event{
			NewResponseStart(id, "fake"), NewToolCallStart(0, "call_1", "weather"), NewToolArgumentsDelta(0, `{}`), NewContentBlockStop(0),
			NewToolCallStart(1, "call_1", "weather"), NewToolArgumentsDelta(1, `{}`), NewContentBlockStop(1), NewResponseFinish(StopToolUse),
		}},
		{name: "missing terminal event", events: []Event{NewResponseStart(id, "fake")}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := ValidateEventSequence(test.events); err == nil {
				t.Fatalf("ValidateEventSequence() = nil for %+v", test.events)
			}
		})
	}
}

func TestSequenceValidatorAcceptsTerminalStreamError(t *testing.T) {
	validator := NewSequenceValidator()
	if err := validator.Push(NewStreamError("connector stream failed")); err != nil {
		t.Fatal(err)
	}
	if !validator.Finished() {
		t.Fatal("stream error did not terminate sequence")
	}
	if err := validator.ValidateEOF(); err != nil {
		t.Fatal(err)
	}
}

func TestEventValidationRejectsMultiplePayloads(t *testing.T) {
	event := NewTextDelta(0, "hello")
	event.UsageUpdate = &UsageUpdateEvent{Usage: Usage{InputTokens: 1}}
	if err := event.Validate(); err == nil {
		t.Fatal("event with multiple payloads was accepted")
	}
}
