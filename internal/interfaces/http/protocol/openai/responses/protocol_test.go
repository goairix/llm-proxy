package responses

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	gatewayservice "github.com/goairix/llm-proxy/internal/application/gateway/service"
	inference "github.com/goairix/llm-proxy/internal/domain/inference/model"
)

func TestDecodeResponsesStringInput(t *testing.T) {
	decoded, err := Decode(strings.NewReader(`{"model":"assistant","input":"hello","stream":true}`))
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Request.Model != "assistant" || !decoded.Request.Stream || decoded.Request.Messages[0].Content[0].Text.Text != "hello" {
		t.Fatalf("decoded=%+v", decoded)
	}
}

func TestDecodeResponsesItemsToolsAndExplicitZero(t *testing.T) {
	body := `{"model":"assistant","instructions":"follow policy","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"look"},{"type":"input_image","image_url":"https://example.invalid/a.png","detail":"high"}]},{"type":"function_call","call_id":"call_one","name":"weather","arguments":"{\"city\":\"Shanghai\"}"},{"type":"function_call_output","call_id":"call_one","output":"sunny"}],"tools":[{"type":"function","name":"weather","description":"weather","parameters":{"type":"object"},"strict":true}],"tool_choice":{"type":"function","name":"weather"},"text":{"format":{"type":"json_schema","name":"answer","schema":{"type":"object"},"strict":true}},"temperature":0,"top_p":0.8,"max_output_tokens":12,"store":false}`
	decoded, err := Decode(strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request := decoded.Request
	if len(request.Messages) != 4 || request.Messages[0].Role != inference.RoleDeveloper || request.Messages[2].Content[0].ToolCall.ID != "call_one" || request.Messages[3].Content[0].ToolResult.ToolCallID != "call_one" {
		t.Fatalf("messages=%+v", request.Messages)
	}
	if !request.Temperature.Set || request.Temperature.Value != 0 || request.MaxTokens.Value != 12 || request.ToolChoice.Name != "weather" || request.StructuredOutput.Name != "answer" {
		t.Fatalf("request=%+v", request)
	}
	if err := request.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestDecodeResponsesRejectsUnsupportedAndOversizedInput(t *testing.T) {
	tests := []struct{ name, body, param string }{
		{name: "unknown", body: `{"model":"m","input":"x","mystery":true}`, param: "body"},
		{name: "built in tool", body: `{"model":"m","input":"x","tools":[{"type":"web_search_preview"}]}`, param: "tools[0].type"},
		{name: "file", body: `{"model":"m","input":[{"type":"message","role":"user","content":[{"type":"input_file","file_id":"x"}]}]}`, param: "input[0].content[0].type"},
		{name: "previous", body: `{"model":"m","input":"x","previous_response_id":"resp_x"}`, param: "previous_response_id"},
		{name: "conversation", body: `{"model":"m","input":"x","conversation":"c"}`, param: "conversation"},
		{name: "background", body: `{"model":"m","input":"x","background":false}`, param: "background"},
		{name: "store", body: `{"model":"m","input":"x","store":true}`, param: "store"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := Decode(strings.NewReader(test.body))
			var gatewayErr *gatewayservice.GatewayError
			if !errors.As(err, &gatewayErr) || gatewayErr.Code != gatewayservice.InvalidRequest || gatewayErr.Param != test.param {
				t.Fatalf("error=%v", err)
			}
		})
	}
	_, err := Decode(strings.NewReader(`{"model":"` + strings.Repeat("x", maxRequestBodyBytes) + `","input":"x"}`))
	if err == nil {
		t.Fatal("oversized body accepted")
	}
}

func TestEncodeResponseUsesResponsesShapeAndLocalItemIDs(t *testing.T) {
	response := responseFixture()
	ids := fixedIDs("0198e3ce-8d5a-7000-8000-000000000011", "0198e3ce-8d5a-7000-8000-000000000012")
	var output bytes.Buffer
	if err := encodeResponse(&output, response, ids); err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(output.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload["id"] != "resp_0198e3ce8d5a70008000000000000010" || payload["object"] != "response" || payload["status"] != "completed" || payload["model"] != "assistant" {
		t.Fatalf("payload=%#v", payload)
	}
	items := payload["output"].([]any)
	if len(items) != 2 || items[0].(map[string]any)["id"] != "msg_0198e3ce8d5a70008000000000000011" || items[1].(map[string]any)["id"] != "fc_0198e3ce8d5a70008000000000000012" || items[1].(map[string]any)["call_id"] != "call_one" {
		t.Fatalf("items=%#v", items)
	}
	usage := payload["usage"].(map[string]any)
	if usage["total_tokens"] != float64(18) {
		t.Fatalf("usage=%#v", usage)
	}
}

func TestEncodeStreamWritesNamedSSEAndFlushes(t *testing.T) {
	response := responseFixture()
	stream := &sliceStream{events: []inference.Event{
		inference.NewResponseStartAt(response.ID, response.Model, response.CreatedAt),
		inference.NewContentBlockStart(0, inference.ContentText), inference.NewTextDelta(0, "hello"), inference.NewContentBlockStop(0),
		inference.NewToolCallStart(1, "call_one", "weather"), inference.NewToolArgumentsDelta(1, `{"city":"Shanghai"}`), inference.NewContentBlockStop(1),
		inference.NewUsageUpdate(response.Usage), inference.NewResponseFinish(inference.StopToolUse),
	}}
	writer := &flushBuffer{}
	if err := encodeStream(context.Background(), writer, stream, fixedIDs("0198e3ce-8d5a-7000-8000-000000000011", "0198e3ce-8d5a-7000-8000-000000000012")); err != nil {
		t.Fatal(err)
	}
	value := writer.String()
	for _, name := range []string{"response.created", "response.output_item.added", "response.content_part.added", "response.output_text.delta", "response.function_call_arguments.delta", "response.completed"} {
		if !strings.Contains(value, "event: "+name+"\n") {
			t.Fatalf("missing %s in %s", name, value)
		}
	}
	if writer.flushes < 10 || !strings.Contains(value, `"sequence_number":0`) || !strings.Contains(value, `"sequence_number":11`) {
		t.Fatalf("flushes=%d body=%s", writer.flushes, value)
	}
}

func TestEncodeStreamMapsSafeFailure(t *testing.T) {
	stream := &sliceStream{events: []inference.Event{inference.NewResponseStartAt(uuid.Must(uuid.NewV7()), "assistant", time.Now().UTC()), inference.NewStreamError("安全错误")}}
	writer := &flushBuffer{}
	if err := encodeStream(context.Background(), writer, stream, uuid.NewV7); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(writer.String(), "event: response.failed") || !strings.Contains(writer.String(), "安全错误") {
		t.Fatalf("body=%s", writer.String())
	}
}

func responseFixture() inference.Response {
	return inference.Response{ID: uuid.MustParse("0198e3ce-8d5a-7000-8000-000000000010"), Model: "assistant", CreatedAt: time.Unix(100, 0).UTC(), StopReason: inference.StopToolUse, Usage: inference.Usage{InputTokens: 11, OutputTokens: 7, CacheReadInputTokens: 3}, Content: []inference.ContentBlock{
		{Type: inference.ContentText, Text: &inference.TextContent{Text: "hello"}},
		{Type: inference.ContentRefusal, Refusal: &inference.RefusalContent{Text: "cannot comply"}},
		{Type: inference.ContentToolCall, ToolCall: &inference.ToolCallContent{ID: "call_one", Name: "weather", Arguments: json.RawMessage(`{"city":"Shanghai"}`)}},
	}}
}

func fixedIDs(values ...string) func() (uuid.UUID, error) {
	index := 0
	return func() (uuid.UUID, error) {
		if index >= len(values) {
			return uuid.Nil, errors.New("exhausted")
		}
		id := uuid.MustParse(values[index])
		index++
		return id, nil
	}
}

type sliceStream struct {
	events []inference.Event
	index  int
}

func (s *sliceStream) Recv(context.Context) (inference.Event, error) {
	if s.index >= len(s.events) {
		return inference.Event{}, io.EOF
	}
	event := s.events[s.index]
	s.index++
	return event, nil
}
func (*sliceStream) Close() error { return nil }

type flushBuffer struct {
	bytes.Buffer
	flushes int
}

func (w *flushBuffer) Flush() { w.flushes++ }
