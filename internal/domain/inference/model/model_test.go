package model

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestOptionalDistinguishesMissingAndExplicitZero(t *testing.T) {
	missing := None[float64]()
	zero := Some(0.0)
	if missing.Set || !zero.Set || zero.Value != 0 {
		t.Fatalf("missing=%+v zero=%+v", missing, zero)
	}
}

func TestRequestRequiredCapabilities(t *testing.T) {
	req := Request{
		Model: "vision-tools",
		Messages: []Message{{Role: RoleUser, Content: []ContentBlock{
			{Type: ContentImage, Image: &ImageContent{Source: ImageSource{Type: ImageURL, Data: "https://example.invalid/a.png"}}},
		}}},
		Tools:            []Tool{{Name: "weather", InputSchema: json.RawMessage(`{"type":"object"}`)}},
		StructuredOutput: &StructuredOutput{Type: StructuredJSONSchema, Name: "answer", Schema: json.RawMessage(`{"type":"object"}`)},
		Stream:           true,
	}

	got := req.RequiredCapabilities()
	if !got.Text || !got.ImageInput || !got.Tools || !got.StructuredOutput || !got.Streaming {
		t.Fatalf("capabilities = %+v", got)
	}
}

func TestContentBlockValidationRequiresMatchingSinglePayload(t *testing.T) {
	text := &TextContent{Text: "hello"}
	image := &ImageContent{Source: ImageSource{Type: ImageURL, Data: "https://example.invalid/a.png"}}
	tests := []struct {
		name  string
		block ContentBlock
	}{
		{name: "missing payload", block: ContentBlock{Type: ContentText}},
		{name: "mismatched payload", block: ContentBlock{Type: ContentText, Image: image}},
		{name: "multiple payloads", block: ContentBlock{Type: ContentText, Text: text, Image: image}},
		{name: "invalid tool arguments", block: ContentBlock{Type: ContentToolCall, ToolCall: &ToolCallContent{ID: "call_1", Name: "weather", Arguments: json.RawMessage(`{"city":`)}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := test.block.Validate(); err == nil {
				t.Fatalf("Validate() = nil for %+v", test.block)
			}
		})
	}
}

func TestRequestValidationPreservesExplicitZeroAndRejectsInvalidInput(t *testing.T) {
	valid := Request{
		Model: "assistant",
		Messages: []Message{{Role: RoleUser, Content: []ContentBlock{
			{Type: ContentText, Text: &TextContent{Text: "hello"}},
		}}},
		Temperature: Some(0.0),
		TopP:        Some(0.0),
		MaxTokens:   Some(int64(0)),
		Stop:        Some([]string{}),
	}
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name   string
		mutate func(*Request)
	}{
		{name: "missing model", mutate: func(request *Request) { request.Model = " " }},
		{name: "missing messages", mutate: func(request *Request) { request.Messages = nil }},
		{name: "invalid role", mutate: func(request *Request) { request.Messages[0].Role = Role("admin") }},
		{name: "temperature above range", mutate: func(request *Request) { request.Temperature = Some(2.1) }},
		{name: "top p above range", mutate: func(request *Request) { request.TopP = Some(1.1) }},
		{name: "negative max tokens", mutate: func(request *Request) { request.MaxTokens = Some(int64(-1)) }},
		{name: "invalid schema", mutate: func(request *Request) {
			request.StructuredOutput = &StructuredOutput{Type: StructuredJSONSchema, Name: "answer", Schema: json.RawMessage(`[]`)}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := valid
			request.Messages = append([]Message(nil), valid.Messages...)
			test.mutate(&request)
			if err := request.Validate(); err == nil {
				t.Fatalf("Validate() = nil for %+v", request)
			}
		})
	}
}

func TestRequestValidatesToolCallAndResultGraph(t *testing.T) {
	valid := Request{
		Model: "assistant",
		Messages: []Message{
			{Role: RoleUser, Content: []ContentBlock{{Type: ContentText, Text: &TextContent{Text: "weather"}}}},
			{Role: RoleAssistant, Content: []ContentBlock{{Type: ContentToolCall, ToolCall: &ToolCallContent{
				ID: "call_1", Name: "weather", Arguments: json.RawMessage(`{"city":"Shanghai"}`),
			}}}},
			{Role: RoleUser, Content: []ContentBlock{{Type: ContentToolResult, ToolResult: &ToolResultContent{
				ToolCallID: "call_1", JSON: json.RawMessage(`{"temperature":30}`),
			}}}},
		},
	}
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name     string
		messages []Message
	}{
		{name: "dangling result", messages: []Message{{Role: RoleUser, Content: []ContentBlock{{
			Type: ContentToolResult, ToolResult: &ToolResultContent{ToolCallID: "missing", JSON: json.RawMessage(`{}`)},
		}}}}},
		{name: "duplicate call id", messages: []Message{
			{Role: RoleAssistant, Content: []ContentBlock{
				{Type: ContentToolCall, ToolCall: &ToolCallContent{ID: "call_1", Name: "one", Arguments: json.RawMessage(`{}`)}},
				{Type: ContentToolCall, ToolCall: &ToolCallContent{ID: "call_1", Name: "two", Arguments: json.RawMessage(`{}`)}},
			}},
		}},
		{name: "unresolved call", messages: []Message{{Role: RoleAssistant, Content: []ContentBlock{{
			Type: ContentToolCall, ToolCall: &ToolCallContent{ID: "call_1", Name: "weather", Arguments: json.RawMessage(`{}`)},
		}}}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := Request{Model: "assistant", Messages: test.messages}
			if err := request.Validate(); err == nil {
				t.Fatalf("Validate() = nil for %+v", request.Messages)
			}
		})
	}
}

func TestResponseValidationAndUsageTotal(t *testing.T) {
	response := Response{
		ID:         uuid.Must(uuid.NewV7()),
		Model:      "assistant",
		Content:    []ContentBlock{{Type: ContentText, Text: &TextContent{Text: "hello"}}},
		StopReason: StopEndTurn,
		Usage: Usage{
			InputTokens: 10, OutputTokens: 5, CacheReadInputTokens: 2, CacheWriteInputTokens: 3,
		},
		CreatedAt: time.Now().UTC(),
	}
	if err := response.Validate(); err != nil {
		t.Fatal(err)
	}
	if response.Usage.TotalTokens() != 15 {
		t.Fatalf("total tokens = %d", response.Usage.TotalTokens())
	}
	empty := response
	empty.Content = nil
	empty.StopReason = StopMaxTokens
	if err := empty.Validate(); err != nil {
		t.Fatalf("zero-output response: %v", err)
	}
	response.Usage.OutputTokens = -1
	if err := response.Validate(); err == nil {
		t.Fatal("negative usage was accepted")
	}
}
