package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	gatewayport "github.com/goairix/llm-proxy/internal/application/gateway/port"
	snapshot "github.com/goairix/llm-proxy/internal/application/gateway/snapshot"
	catalogmodel "github.com/goairix/llm-proxy/internal/domain/catalog/model"
	inference "github.com/goairix/llm-proxy/internal/domain/inference/model"
)

func TestEncodeChatRequestMapsUnifiedContract(t *testing.T) {
	invocation := fullInvocation(t)
	invocation.Request.Messages = []inference.Message{
		{Role: inference.RoleSystem, Content: []inference.ContentBlock{textBlock("system")}},
		{Role: inference.RoleDeveloper, Content: []inference.ContentBlock{textBlock("developer")}},
		{Role: inference.RoleUser, Content: []inference.ContentBlock{
			textBlock("look"),
			{Type: inference.ContentImage, Image: &inference.ImageContent{Source: inference.ImageSource{Type: inference.ImageURL, Data: "https://example.invalid/image.png", Detail: "high"}}},
			{Type: inference.ContentImage, Image: &inference.ImageContent{Source: inference.ImageSource{Type: inference.ImageBase64, MediaType: "image/png", Data: "aGVsbG8=", Detail: "low"}}},
		}},
		{Role: inference.RoleAssistant, Content: []inference.ContentBlock{
			textBlock("calling"),
			{Type: inference.ContentToolCall, ToolCall: &inference.ToolCallContent{ID: "call_one", Name: "weather", Arguments: json.RawMessage(`{"city":"Shanghai"}`)}},
			{Type: inference.ContentToolCall, ToolCall: &inference.ToolCallContent{ID: "call_two", Name: "clock", Arguments: json.RawMessage(`{"zone":"Asia/Shanghai"}`)}},
		}},
		{Role: inference.RoleUser, Content: []inference.ContentBlock{{Type: inference.ContentToolResult, ToolResult: &inference.ToolResultContent{ToolCallID: "call_one", JSON: json.RawMessage(`{"temperature":28}`)}}}},
		{Role: inference.RoleUser, Content: []inference.ContentBlock{{Type: inference.ContentToolResult, ToolResult: &inference.ToolResultContent{ToolCallID: "call_two", Text: stringPointer("12:00")}}}},
	}
	invocation.Request.Tools = []inference.Tool{{Name: "weather", Description: "get weather", InputSchema: json.RawMessage(`{"type":"object"}`), Strict: true}, {Name: "clock", InputSchema: json.RawMessage(`{"type":"object"}`)}}
	invocation.Request.ToolChoice = &inference.ToolChoice{Mode: inference.ToolChoiceSpecific, Name: "weather"}
	invocation.Request.StructuredOutput = &inference.StructuredOutput{Type: inference.StructuredJSONSchema, Name: "answer", Description: "answer shape", Strict: true, Schema: json.RawMessage(`{"type":"object"}`)}
	invocation.Request.Temperature = inference.Some(0.0)
	invocation.Request.TopP = inference.Some(0.75)
	invocation.Request.MaxTokens = inference.Some[int64](12)
	invocation.Request.Stop = inference.Some([]string{"END"})
	invocation.Request.Stream = true
	if err := invocation.Request.Validate(); err != nil {
		t.Fatal(err)
	}

	body, err := encodeChatRequest(invocation)
	if err != nil {
		t.Fatal(err)
	}
	payload := decodeObject(t, body)
	if payload["model"] != "gpt-5" || payload["stream"] != true || payload["temperature"] != json.Number("0") {
		t.Fatalf("unexpected base fields: %#v", payload)
	}
	assertJSONNumber(t, payload, "max_completion_tokens", 12)
	assertAbsent(t, payload, "max_tokens")
	if payload["stream_options"].(map[string]any)["include_usage"] != true {
		t.Fatalf("stream_options=%#v", payload["stream_options"])
	}
	messages := payload["messages"].([]any)
	if len(messages) != 6 {
		t.Fatalf("messages=%d %#v", len(messages), messages)
	}
	if messages[0].(map[string]any)["role"] != "system" || messages[1].(map[string]any)["role"] != "developer" {
		t.Fatalf("roles=%#v", messages[:2])
	}
	parts := messages[2].(map[string]any)["content"].([]any)
	if len(parts) != 3 || parts[1].(map[string]any)["image_url"].(map[string]any)["url"] != "https://example.invalid/image.png" || parts[2].(map[string]any)["image_url"].(map[string]any)["url"] != "data:image/png;base64,aGVsbG8=" {
		t.Fatalf("content=%#v", parts)
	}
	assistant := messages[3].(map[string]any)
	if len(assistant["tool_calls"].([]any)) != 2 {
		t.Fatalf("assistant=%#v", assistant)
	}
	if messages[4].(map[string]any)["role"] != "tool" || messages[4].(map[string]any)["content"] != `{"temperature":28}` || messages[5].(map[string]any)["content"] != "12:00" {
		t.Fatalf("tool results=%#v", messages[4:6])
	}
	choice := payload["tool_choice"].(map[string]any)
	if choice["type"] != "function" || choice["function"].(map[string]any)["name"] != "weather" {
		t.Fatalf("tool_choice=%#v", choice)
	}
	format := payload["response_format"].(map[string]any)
	if format["type"] != "json_schema" || format["json_schema"].(map[string]any)["name"] != "answer" {
		t.Fatalf("response_format=%#v", format)
	}
}

func TestEncodeChatToolChoiceModes(t *testing.T) {
	for _, test := range []struct {
		name   string
		want   any
		choice *inference.ToolChoice
	}{
		{name: "omitted", want: nil},
		{name: "auto", want: "auto", choice: &inference.ToolChoice{Mode: inference.ToolChoiceAuto}},
		{name: "none", want: "none", choice: &inference.ToolChoice{Mode: inference.ToolChoiceNone}},
		{name: "required", want: "required", choice: &inference.ToolChoice{Mode: inference.ToolChoiceRequired}},
	} {
		t.Run(test.name, func(t *testing.T) {
			invocation := fullInvocation(t)
			invocation.Request.Tools = []inference.Tool{{Name: "tool", InputSchema: json.RawMessage(`{}`)}}
			invocation.Request.ToolChoice = test.choice
			body, err := encodeChatRequest(invocation)
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

func TestEncodeChatTokenLimitByConnectorType(t *testing.T) {
	invocation := fullInvocation(t)
	invocation.Request.MaxTokens = inference.Some[int64](12)
	invocation.Provider.ConnectorType = catalogmodel.ConnectorOpenAI
	officialBody, err := encodeChatRequest(invocation)
	if err != nil {
		t.Fatal(err)
	}
	official := decodeObject(t, officialBody)
	invocation.Provider.ConnectorType = catalogmodel.ConnectorOpenAICompatible
	compatibleBody, err := encodeChatRequest(invocation)
	if err != nil {
		t.Fatal(err)
	}
	compatible := decodeObject(t, compatibleBody)
	assertJSONNumber(t, official, "max_completion_tokens", 12)
	assertAbsent(t, official, "max_tokens")
	assertJSONNumber(t, compatible, "max_tokens", 12)
	assertAbsent(t, compatible, "max_completion_tokens")
}

func TestEncodeChatRejectsZeroMaxTokens(t *testing.T) {
	invocation := fullInvocation(t)
	invocation.Request.MaxTokens = inference.Some[int64](0)
	_, err := encodeChatRequest(invocation)
	assertConnectorErrorKind(t, err, gatewayport.ParameterUnsupported, "max_tokens")
}

func TestDecodeChatResponseMapsLocalIdentityContentAndUsage(t *testing.T) {
	invocation := fullInvocation(t)
	payload, err := os.Open("testdata/chat_text_response.json")
	if err != nil {
		t.Fatal(err)
	}
	defer payload.Close()
	wantID := uuid.MustParse("0198e3ce-8d5a-7000-8000-000000000001")
	wantTime := time.Date(2026, 8, 26, 10, 0, 0, 0, time.UTC)
	response, err := decodeChatResponse(payload, invocation, func() (uuid.UUID, error) { return wantID, nil }, func() time.Time { return wantTime })
	if err != nil {
		t.Fatal(err)
	}
	if response.ID != wantID || response.Model != "assistant" || !response.CreatedAt.Equal(wantTime) || response.StopReason != inference.StopToolUse {
		t.Fatalf("response=%+v", response)
	}
	if len(response.Content) != 4 || response.Content[0].Text.Text != "hello" || response.Content[1].Refusal.Text != "cannot comply" || response.Content[2].ToolCall.ID != "call_one" || response.Content[3].ToolCall.Name != "clock" {
		t.Fatalf("content=%+v", response.Content)
	}
	if response.Usage != (inference.Usage{InputTokens: 11, OutputTokens: 7, CacheReadInputTokens: 3}) {
		t.Fatalf("usage=%+v", response.Usage)
	}
	if err := response.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestDecodeChatResponseRejectsInvalidChoiceFinishAndUsage(t *testing.T) {
	invocation := fullInvocation(t)
	for _, body := range []string{
		`{"choices":[]}`,
		`{"choices":[{"index":1,"message":{"role":"assistant","content":"x"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`,
		`{"choices":[{"index":0,"message":{"role":"assistant","content":"x"},"finish_reason":"unknown"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`,
		`{"choices":[{"index":0,"message":{"role":"assistant","content":"x"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":3}}`,
	} {
		_, err := decodeChatResponse(strings.NewReader(body), invocation, uuid.NewV7, time.Now)
		assertConnectorErrorKind(t, err, gatewayport.UpstreamInvalidResponse, "")
	}
}

func TestChatStreamMapsToolArgumentDeltasAndUsage(t *testing.T) {
	invocation := fullInvocation(t)
	payload, err := os.Open("testdata/chat_tool_stream.sse")
	if err != nil {
		t.Fatal(err)
	}
	reader := newSSEReader(payload, time.Second, 2<<20)
	wantID := uuid.MustParse("0198e3ce-8d5a-7000-8000-000000000002")
	stream, err := newChatStream(reader, invocation, func() (uuid.UUID, error) { return wantID, nil }, func() time.Time { return time.Date(2026, 8, 26, 10, 0, 0, 0, time.UTC) })
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	events := collectEvents(t, stream)
	if err := inference.ValidateEventSequence(events); err != nil {
		t.Fatal(err)
	}
	if events[0].Type != inference.EventResponseStart || events[0].ResponseStart.ID != wantID || events[0].ResponseStart.Model != "assistant" {
		t.Fatalf("start=%+v", events[0])
	}
	arguments := map[int]string{}
	toolStarts := 0
	for _, event := range events {
		if event.Type == inference.EventToolCallStart {
			toolStarts++
		}
		if event.Type == inference.EventToolArgumentsDelta {
			arguments[event.ToolArgumentsDelta.Index] += event.ToolArgumentsDelta.Delta
		}
	}
	if toolStarts != 2 || arguments[1] != `{"city":"Shanghai"}` || arguments[2] != `{"zone":"Asia/Shanghai"}` {
		t.Fatalf("tool_starts=%d arguments=%#v", toolStarts, arguments)
	}
	if got := events[len(events)-2].UsageUpdate.Usage; got != (inference.Usage{InputTokens: 11, OutputTokens: 7, CacheReadInputTokens: 3}) {
		t.Fatalf("usage=%+v", got)
	}
	if events[len(events)-1].ResponseFinish.StopReason != inference.StopToolUse {
		t.Fatalf("finish=%+v", events[len(events)-1])
	}
}

func TestChatStreamReturnsFirstEventBeforeUpstreamFinishes(t *testing.T) {
	pipeReader, pipeWriter := io.Pipe()
	release := make(chan struct{})
	go func() {
		_, _ = io.WriteString(pipeWriter, "data: {\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"hello\"},\"finish_reason\":null}]}\n\n")
		<-release
		_, _ = io.WriteString(pipeWriter, "data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":1,\"completion_tokens\":1,\"total_tokens\":2}}\n\ndata: [DONE]\n\n")
		_ = pipeWriter.Close()
	}()
	stream, err := newChatStream(newSSEReader(pipeReader, time.Second, 2<<20), fullInvocation(t), uuid.NewV7, time.Now)
	if err != nil {
		close(release)
		t.Fatal(err)
	}
	defer stream.Close()
	result := make(chan inference.Event, 1)
	go func() { event, _ := stream.Recv(context.Background()); result <- event }()
	select {
	case event := <-result:
		if event.Type != inference.EventResponseStart {
			t.Fatalf("event=%+v", event)
		}
	case <-time.After(250 * time.Millisecond):
		t.Fatal("first event was buffered until upstream completion")
	}
	close(release)
}

func fullInvocation(t *testing.T) gatewayport.Invocation {
	t.Helper()
	providerID := uuid.Must(uuid.NewV7())
	return gatewayport.Invocation{
		Request:    inference.Request{Model: "assistant", Messages: []inference.Message{{Role: inference.RoleUser, Content: []inference.ContentBlock{textBlock("hello")}}}},
		Provider:   snapshot.Provider{ID: providerID, ConnectorType: catalogmodel.ConnectorOpenAI, BaseURL: "https://api.openai.com"},
		Deployment: snapshot.Deployment{ID: uuid.Must(uuid.NewV7()), ProviderID: providerID, UpstreamModel: "gpt-5", UpstreamProtocol: catalogmodel.UpstreamChatCompletions, Capabilities: catalogmodel.CapabilitySet{Text: true, Streaming: true}},
		Revision:   1,
	}
}

func textBlock(value string) inference.ContentBlock {
	return inference.ContentBlock{Type: inference.ContentText, Text: &inference.TextContent{Text: value}}
}
func stringPointer(value string) *string { return &value }

func decodeObject(t *testing.T, payload []byte) map[string]any {
	t.Helper()
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.UseNumber()
	var result map[string]any
	if err := decoder.Decode(&result); err != nil {
		t.Fatal(err)
	}
	return result
}

func assertAbsent(t *testing.T, value map[string]any, key string) {
	t.Helper()
	if _, exists := value[key]; exists {
		t.Fatalf("field %s unexpectedly exists", key)
	}
}

func assertJSONNumber(t *testing.T, value map[string]any, key string, want int64) {
	t.Helper()
	got, ok := value[key].(json.Number)
	if !ok || got.String() != strconv.FormatInt(want, 10) {
		t.Fatalf("%s=%v", key, value[key])
	}
}

func assertConnectorErrorKind(t *testing.T, err error, kind gatewayport.ConnectorErrorKind, param string) {
	t.Helper()
	var connectorErr *gatewayport.ConnectorError
	if !errors.As(err, &connectorErr) || connectorErr.Kind != kind || connectorErr.Param != param {
		t.Fatalf("error=%v kind=%q param=%q", err, kind, param)
	}
}

func collectEvents(t *testing.T, stream interface {
	Recv(context.Context) (inference.Event, error)
}) []inference.Event {
	t.Helper()
	var events []inference.Event
	for {
		event, err := stream.Recv(context.Background())
		if errors.Is(err, io.EOF) {
			return events
		}
		if err != nil {
			t.Fatalf("receive event: %v (cause: %v)", err, errors.Unwrap(err))
		}
		events = append(events, event)
	}
}
