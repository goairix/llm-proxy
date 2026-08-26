package openai

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	gatewayport "github.com/goairix/llm-proxy/internal/application/gateway/port"
	catalogmodel "github.com/goairix/llm-proxy/internal/domain/catalog/model"
	inference "github.com/goairix/llm-proxy/internal/domain/inference/model"
)

func TestEncodeResponsesMapsUnifiedContractAndForcesStatelessCalls(t *testing.T) {
	invocation := fullInvocation(t)
	invocation.Deployment.UpstreamProtocol = catalogmodel.UpstreamResponses
	invocation.Request.Messages = []inference.Message{
		{Role: inference.RoleSystem, Content: []inference.ContentBlock{textBlock("system")}},
		{Role: inference.RoleDeveloper, Content: []inference.ContentBlock{textBlock("developer")}},
		{Role: inference.RoleUser, Content: []inference.ContentBlock{
			textBlock("look"),
			{Type: inference.ContentImage, Image: &inference.ImageContent{Source: inference.ImageSource{Type: inference.ImageURL, Data: "https://example.invalid/image.png", Detail: "high"}}},
			{Type: inference.ContentImage, Image: &inference.ImageContent{Source: inference.ImageSource{Type: inference.ImageBase64, MediaType: "image/png", Data: "aGVsbG8="}}},
		}},
		{Role: inference.RoleAssistant, Content: []inference.ContentBlock{
			textBlock("calling"),
			{Type: inference.ContentToolCall, ToolCall: &inference.ToolCallContent{ID: "call_one", Name: "weather", Arguments: json.RawMessage(`{"city":"Shanghai"}`)}},
		}},
		{Role: inference.RoleUser, Content: []inference.ContentBlock{{Type: inference.ContentToolResult, ToolResult: &inference.ToolResultContent{ToolCallID: "call_one", JSON: json.RawMessage(`{"temperature":28}`)}}}},
	}
	invocation.Request.Tools = []inference.Tool{{Name: "weather", Description: "get weather", InputSchema: json.RawMessage(`{"type":"object"}`), Strict: true}}
	invocation.Request.ToolChoice = &inference.ToolChoice{Mode: inference.ToolChoiceSpecific, Name: "weather"}
	invocation.Request.StructuredOutput = &inference.StructuredOutput{Type: inference.StructuredJSONSchema, Name: "answer", Description: "answer shape", Strict: true, Schema: json.RawMessage(`{"type":"object"}`)}
	invocation.Request.Temperature = inference.Some(0.0)
	invocation.Request.TopP = inference.Some(0.8)
	invocation.Request.MaxTokens = inference.Some[int64](12)
	invocation.Request.Stream = true
	if err := invocation.Request.Validate(); err != nil {
		t.Fatal(err)
	}

	body, err := encodeResponsesRequest(invocation)
	if err != nil {
		t.Fatal(err)
	}
	payload := decodeObject(t, body)
	if payload["store"] != false || payload["model"] != "gpt-5" || payload["stream"] != true || payload["instructions"] != "system\ndeveloper" {
		t.Fatalf("payload=%#v", payload)
	}
	assertJSONNumber(t, payload, "max_output_tokens", 12)
	if payload["temperature"] != json.Number("0") || payload["top_p"] != json.Number("0.8") {
		t.Fatalf("sampling=%#v", payload)
	}
	input := payload["input"].([]any)
	if len(input) != 4 {
		t.Fatalf("input=%d %#v", len(input), input)
	}
	userContent := input[0].(map[string]any)["content"].([]any)
	if len(userContent) != 3 || userContent[1].(map[string]any)["image_url"] != "https://example.invalid/image.png" || userContent[2].(map[string]any)["image_url"] != "data:image/png;base64,aGVsbG8=" {
		t.Fatalf("user content=%#v", userContent)
	}
	if input[2].(map[string]any)["type"] != "function_call" || input[3].(map[string]any)["type"] != "function_call_output" {
		t.Fatalf("tool input=%#v", input[2:4])
	}
	if input[3].(map[string]any)["output"] != `{"temperature":28}` {
		t.Fatalf("output=%#v", input[3])
	}
	tool := payload["tools"].([]any)[0].(map[string]any)
	if tool["type"] != "function" || tool["name"] != "weather" || tool["strict"] != true {
		t.Fatalf("tool=%#v", tool)
	}
	choice := payload["tool_choice"].(map[string]any)
	if choice["type"] != "function" || choice["name"] != "weather" {
		t.Fatalf("choice=%#v", choice)
	}
	format := payload["text"].(map[string]any)["format"].(map[string]any)
	if format["type"] != "json_schema" || format["name"] != "answer" {
		t.Fatalf("format=%#v", format)
	}
}

func TestEncodeResponsesToolChoiceModes(t *testing.T) {
	for _, test := range []struct {
		name   string
		choice *inference.ToolChoice
		want   any
	}{
		{name: "omitted"},
		{name: "auto", choice: &inference.ToolChoice{Mode: inference.ToolChoiceAuto}, want: "auto"},
		{name: "none", choice: &inference.ToolChoice{Mode: inference.ToolChoiceNone}, want: "none"},
		{name: "required", choice: &inference.ToolChoice{Mode: inference.ToolChoiceRequired}, want: "required"},
	} {
		t.Run(test.name, func(t *testing.T) {
			invocation := fullInvocation(t)
			invocation.Deployment.UpstreamProtocol = catalogmodel.UpstreamResponses
			invocation.Request.Tools = []inference.Tool{{Name: "tool", InputSchema: json.RawMessage(`{}`)}}
			invocation.Request.ToolChoice = test.choice
			body, err := encodeResponsesRequest(invocation)
			if err != nil {
				t.Fatal(err)
			}
			payload := decodeObject(t, body)
			value, exists := payload["tool_choice"]
			if test.want == nil && exists {
				t.Fatalf("tool_choice=%#v", value)
			}
			if test.want != nil && value != test.want {
				t.Fatalf("tool_choice=%#v", value)
			}
		})
	}
}

func TestResponsesRejectsStopAndZeroMaxTokens(t *testing.T) {
	invocation := fullInvocation(t)
	invocation.Deployment.UpstreamProtocol = catalogmodel.UpstreamResponses
	invocation.Request.Stop = inference.Some([]string{"END"})
	_, err := encodeResponsesRequest(invocation)
	assertConnectorErrorKind(t, err, gatewayport.ParameterUnsupported, "stop")
	invocation.Request.Stop = inference.Optional[[]string]{}
	invocation.Request.MaxTokens = inference.Some[int64](0)
	_, err = encodeResponsesRequest(invocation)
	assertConnectorErrorKind(t, err, gatewayport.ParameterUnsupported, "max_tokens")
}

func TestDecodeResponsesResponseMapsVisibleOutputAndIgnoresReasoningMetadata(t *testing.T) {
	invocation := fullInvocation(t)
	invocation.Deployment.UpstreamProtocol = catalogmodel.UpstreamResponses
	payload, err := os.Open("testdata/responses_text_response.json")
	if err != nil {
		t.Fatal(err)
	}
	defer payload.Close()
	wantID := uuid.MustParse("0198e3ce-8d5a-7000-8000-000000000003")
	wantTime := time.Date(2026, 8, 26, 11, 0, 0, 0, time.UTC)
	response, err := decodeResponsesResponse(payload, invocation, func() (uuid.UUID, error) { return wantID, nil }, func() time.Time { return wantTime })
	if err != nil {
		t.Fatal(err)
	}
	if response.ID != wantID || response.Model != "assistant" || response.StopReason != inference.StopToolUse || !response.CreatedAt.Equal(wantTime) {
		t.Fatalf("response=%+v", response)
	}
	if len(response.Content) != 3 || response.Content[0].Text.Text != "hello" || response.Content[1].Refusal.Text != "cannot comply" || response.Content[2].ToolCall.ID != "call_one" {
		t.Fatalf("content=%+v", response.Content)
	}
	if response.Usage != (inference.Usage{InputTokens: 20, OutputTokens: 8, CacheReadInputTokens: 4}) {
		t.Fatalf("usage=%+v", response.Usage)
	}
	if err := response.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestDecodeResponsesResponseMapsIncompleteReasons(t *testing.T) {
	invocation := fullInvocation(t)
	invocation.Deployment.UpstreamProtocol = catalogmodel.UpstreamResponses
	for reason, want := range map[string]inference.StopReason{"max_output_tokens": inference.StopMaxTokens, "content_filter": inference.StopContentFilter} {
		body := `{"status":"incomplete","incomplete_details":{"reason":"` + reason + `"},"output":[],"usage":{"input_tokens":1,"output_tokens":0,"total_tokens":1}}`
		response, err := decodeResponsesResponse(strings.NewReader(body), invocation, uuid.NewV7, time.Now)
		if err != nil {
			t.Fatal(err)
		}
		if response.StopReason != want {
			t.Fatalf("reason=%s got=%s", reason, response.StopReason)
		}
	}
}

func TestDecodeResponsesResponseFailsClosedOnUnknownVisibleOutput(t *testing.T) {
	invocation := fullInvocation(t)
	invocation.Deployment.UpstreamProtocol = catalogmodel.UpstreamResponses
	body := `{"status":"completed","output":[{"type":"computer_call"}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`
	_, err := decodeResponsesResponse(strings.NewReader(body), invocation, uuid.NewV7, time.Now)
	assertConnectorErrorKind(t, err, gatewayport.UpstreamInvalidResponse, "")
}

func TestResponsesStreamMapsNamedEventsSequenceAndUsage(t *testing.T) {
	invocation := fullInvocation(t)
	invocation.Deployment.UpstreamProtocol = catalogmodel.UpstreamResponses
	payload, err := os.Open("testdata/responses_tool_stream.sse")
	if err != nil {
		t.Fatal(err)
	}
	stream, err := newResponsesStream(newSSEReader(payload, time.Second, 2<<20), invocation, func() (uuid.UUID, error) {
		return uuid.MustParse("0198e3ce-8d5a-7000-8000-000000000004"), nil
	}, func() time.Time { return time.Date(2026, 8, 26, 11, 0, 0, 0, time.UTC) })
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	events := collectEvents(t, stream)
	if err := inference.ValidateEventSequence(events); err != nil {
		t.Fatal(err)
	}
	if events[0].Type != inference.EventResponseStart || events[0].ResponseStart.Model != "assistant" {
		t.Fatalf("start=%+v", events[0])
	}
	arguments := ""
	for _, event := range events {
		if event.Type == inference.EventToolArgumentsDelta {
			arguments += event.ToolArgumentsDelta.Delta
		}
	}
	if arguments != `{"city":"Shanghai"}` {
		t.Fatalf("arguments=%q", arguments)
	}
	if got := events[len(events)-2].UsageUpdate.Usage; got != (inference.Usage{InputTokens: 20, OutputTokens: 8, CacheReadInputTokens: 4}) {
		t.Fatalf("usage=%+v", got)
	}
	if events[len(events)-1].ResponseFinish.StopReason != inference.StopToolUse {
		t.Fatalf("finish=%+v", events[len(events)-1])
	}
}

func TestResponsesStreamRejectsNonMonotonicSequence(t *testing.T) {
	body := "event: response.created\ndata: {\"type\":\"response.created\",\"sequence_number\":2,\"response\":{}}\n\n" +
		"event: response.completed\ndata: {\"type\":\"response.completed\",\"sequence_number\":2,\"response\":{\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}}\n\n"
	invocation := fullInvocation(t)
	invocation.Deployment.UpstreamProtocol = catalogmodel.UpstreamResponses
	stream, err := newResponsesStream(newSSEReader(io.NopCloser(strings.NewReader(body)), time.Second, 2<<20), invocation, uuid.NewV7, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	if _, err := stream.Recv(context.Background()); err != nil {
		t.Fatal(err)
	}
	_, err = stream.Recv(context.Background())
	assertConnectorErrorKind(t, err, gatewayport.UpstreamInvalidResponse, "")
}

func TestResponsesStreamMapsFailedToSafeTerminalEvent(t *testing.T) {
	body := "event: response.created\ndata: {\"type\":\"response.created\",\"sequence_number\":0,\"response\":{}}\n\n" +
		"event: response.failed\ndata: {\"type\":\"response.failed\",\"sequence_number\":1,\"response\":{\"error\":{\"message\":\"private upstream detail\"}}}\n\n"
	invocation := fullInvocation(t)
	invocation.Deployment.UpstreamProtocol = catalogmodel.UpstreamResponses
	stream, err := newResponsesStream(newSSEReader(io.NopCloser(strings.NewReader(body)), time.Second, 2<<20), invocation, uuid.NewV7, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	first, err := stream.Recv(context.Background())
	if err != nil || first.Type != inference.EventResponseStart {
		t.Fatalf("first=%+v error=%v", first, err)
	}
	failed, err := stream.Recv(context.Background())
	if err != nil || failed.Type != inference.EventStreamError || strings.Contains(failed.StreamError.Message, "private") {
		t.Fatalf("failed=%+v error=%v", failed, err)
	}
	if _, err := stream.Recv(context.Background()); !errors.Is(err, io.EOF) {
		t.Fatalf("EOF=%v", err)
	}
}
