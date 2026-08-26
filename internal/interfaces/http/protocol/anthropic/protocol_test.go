package anthropic

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	gatewayservice "github.com/goairix/llm-proxy/internal/application/gateway/service"
	inference "github.com/goairix/llm-proxy/internal/domain/inference/model"
)

func TestDecodeMapsSystemRequiredZeroAndExplicitEmptyStop(t *testing.T) {
	body, err := os.Open("testdata/text_request.json")
	if err != nil {
		t.Fatal(err)
	}
	defer body.Close()
	request, err := Decode(body)
	if err != nil {
		t.Fatal(err)
	}
	if len(request.Messages) != 2 || request.Messages[0].Role != inference.RoleSystem || request.Messages[1].Role != inference.RoleUser {
		t.Fatalf("messages=%+v", request.Messages)
	}
	if !request.MaxTokens.Set || request.MaxTokens.Value != 0 || !request.Temperature.Set || request.Temperature.Value != 0 || !request.Stop.Set || len(request.Stop.Value) != 0 {
		t.Fatalf("request=%+v", request)
	}
}

func TestDecodeMapsToolsResultsChoiceAndStructuredOutput(t *testing.T) {
	body, err := os.Open("testdata/tool_request.json")
	if err != nil {
		t.Fatal(err)
	}
	defer body.Close()
	request, err := Decode(body)
	if err != nil {
		t.Fatal(err)
	}
	if request.Messages[1].Content[0].ToolCall == nil || request.Messages[2].Content[0].ToolResult == nil {
		t.Fatalf("messages=%+v", request.Messages)
	}
	if len(request.Tools) != 1 || !request.Tools[0].Strict || request.ToolChoice == nil || request.ToolChoice.Mode != inference.ToolChoiceSpecific || request.ToolChoice.Name != "weather" {
		t.Fatalf("tools=%+v choice=%+v", request.Tools, request.ToolChoice)
	}
	if request.StructuredOutput == nil || request.StructuredOutput.Type != inference.StructuredJSONSchema || string(request.StructuredOutput.Schema) != `{"type": "object"}` {
		t.Fatalf("structured=%+v", request.StructuredOutput)
	}
}

func TestDecodeMapsTextBase64AndURLImages(t *testing.T) {
	body := `{"model":"assistant","max_tokens":1,"messages":[{"role":"user","content":[
		{"type":"text","text":"look"},
		{"type":"image","source":{"type":"base64","media_type":"image/png","data":"aW1hZ2U="}},
		{"type":"image","source":{"type":"url","url":"https://example.com/image.png"}}
	]}]}`
	request, err := Decode(strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	content := request.Messages[0].Content
	if len(content) != 3 || content[1].Image.Source.Type != inference.ImageBase64 || content[2].Image.Source.Type != inference.ImageURL {
		t.Fatalf("content=%+v", content)
	}
}

func TestDecodeMapsSystemTextBlocksAndAllToolChoices(t *testing.T) {
	systemBody := `{"model":"assistant","max_tokens":1,"system":[{"type":"text","text":"first"},{"type":"text","text":"second"}],"messages":[{"role":"user","content":"hi"}]}`
	request, err := Decode(strings.NewReader(systemBody))
	if err != nil {
		t.Fatal(err)
	}
	if len(request.Messages[0].Content) != 2 || request.Messages[0].Content[1].Text.Text != "second" {
		t.Fatalf("system=%+v", request.Messages[0])
	}

	tests := []struct {
		choice string
		mode   inference.ToolChoiceMode
		name   string
	}{
		{`{"type":"auto"}`, inference.ToolChoiceAuto, ""},
		{`{"type":"none"}`, inference.ToolChoiceNone, ""},
		{`{"type":"any"}`, inference.ToolChoiceRequired, ""},
		{`{"type":"tool","name":"weather"}`, inference.ToolChoiceSpecific, "weather"},
	}
	for _, test := range tests {
		body := `{"model":"assistant","max_tokens":1,"messages":[{"role":"user","content":"hi"}],"tools":[{"name":"weather","input_schema":{"type":"object"}}],"tool_choice":` + test.choice + `}`
		request, err := Decode(strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		if request.ToolChoice == nil || request.ToolChoice.Mode != test.mode || request.ToolChoice.Name != test.name {
			t.Fatalf("choice=%s mapped=%+v", test.choice, request.ToolChoice)
		}
	}
}

func TestDecodeRejectsMissingMaxTokensAndInvalidContentButIgnoresLegacyOutputFormat(t *testing.T) {
	tests := []string{
		`{"model":"assistant","messages":[{"role":"user","content":"hi"}]}`,
		`{"model":"assistant","max_tokens":1,"system":"system without messages"}`,
		`{"model":"assistant","max_tokens":1,"messages":[{"role":"user","content":[{"type":"document"}]}]}`,
		`{"model":"assistant","max_tokens":1,"temperature":1.1,"messages":[{"role":"user","content":"hi"}]}`,
		`{"model":"assistant","max_tokens":1,"messages":[{"role":"user","content":"call"},{"role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"tool","input":{}}]},{"role":"user","content":[{"type":"text","text":"must follow results"},{"type":"tool_result","tool_use_id":"toolu_1","content":"ok"}]}]}`,
		`{"model":"assistant","max_tokens":1,"messages":[{"role":"user","content":"hi"}],"output_format":{"type":"json_schema","schema":{"type":"object"}}}`,
	}
	for index, body := range tests {
		_, err := Decode(strings.NewReader(body))
		if index == len(tests)-1 {
			if err != nil {
				t.Fatal(err)
			}
			continue
		}
		if err == nil {
			t.Fatalf("Decode(%s) succeeded", body)
		}
	}
}

func TestEncodeResponseMapsContentStopAndUsage(t *testing.T) {
	response := validResponse()
	response.Content = append(response.Content, inference.ContentBlock{Type: inference.ContentToolCall, ToolCall: &inference.ToolCallContent{
		ID: "toolu_1", Name: "weather", Arguments: json.RawMessage(`{"city":"Shanghai"}`),
	}})
	response.StopReason = inference.StopToolUse
	response.Usage = inference.Usage{InputTokens: 10, OutputTokens: 4, CacheReadInputTokens: 3, CacheWriteInputTokens: 2}
	var output bytes.Buffer
	if err := EncodeResponse(&output, response); err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		ID         string `json:"id"`
		Type       string `json:"type"`
		Role       string `json:"role"`
		Model      string `json:"model"`
		StopReason string `json:"stop_reason"`
		Content    []struct {
			Type  string         `json:"type"`
			Text  string         `json:"text"`
			ID    string         `json:"id"`
			Name  string         `json:"name"`
			Input map[string]any `json:"input"`
		} `json:"content"`
		Usage usageDTO `json:"usage"`
	}
	if err := json.Unmarshal(output.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.ID != "msg_"+response.ID.String() || decoded.Type != "message" || decoded.Role != "assistant" || decoded.Model != "assistant" || decoded.StopReason != "tool_use" {
		t.Fatalf("response=%+v", decoded)
	}
	if len(decoded.Content) != 2 || decoded.Content[0].Text != "hello" || decoded.Content[1].ID != "toolu_1" || decoded.Content[1].Input["city"] != "Shanghai" {
		t.Fatalf("content=%+v", decoded.Content)
	}
	if decoded.Usage.InputTokens != 10 || decoded.Usage.OutputTokens != 4 || decoded.Usage.CacheReadInputTokens != 3 || decoded.Usage.CacheCreationInputTokens != 2 {
		t.Fatalf("usage=%+v", decoded.Usage)
	}
}

func TestEncodeStreamMatchesGoldenAndFlushesEveryEvent(t *testing.T) {
	response := validResponse()
	events := []inference.Event{
		inference.NewResponseStartAt(response.ID, response.Model, response.CreatedAt),
		inference.NewContentBlockStart(0, inference.ContentText),
		inference.NewTextDelta(0, "hello"),
		inference.NewContentBlockStop(0),
		inference.NewUsageUpdate(inference.Usage{InputTokens: 1, OutputTokens: 0}),
		inference.NewUsageUpdate(inference.Usage{InputTokens: 2, OutputTokens: 1}),
		inference.NewResponseFinish(inference.StopEndTurn),
	}
	writer := &flushBuffer{}
	if err := EncodeStream(context.Background(), writer, &sliceStream{events: events}); err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile("testdata/stream.golden")
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSuffix(writer.String(), "\n") != string(want) {
		t.Fatalf("stream mismatch\nwant:\n%s\ngot:\n%s", want, writer.String())
	}
	if writer.flushes != 6 {
		t.Fatalf("flushes=%d", writer.flushes)
	}
}

func TestEncodeStreamMapsToolArgumentsAndSafeError(t *testing.T) {
	response := validResponse()
	writer := &flushBuffer{}
	events := []inference.Event{
		inference.NewResponseStartAt(response.ID, response.Model, response.CreatedAt),
		inference.NewToolCallStart(0, "toolu_1", "weather"),
		inference.NewToolArgumentsDelta(0, `{"city":`),
		inference.NewToolArgumentsDelta(0, `"Shanghai"}`),
		inference.NewContentBlockStop(0),
		inference.NewStreamError("供应商流失败"),
	}
	if err := EncodeStream(context.Background(), writer, &sliceStream{events: events}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(writer.String(), `"type":"input_json_delta","partial_json":"{\"city\":"`) ||
		!strings.Contains(writer.String(), `event: error`) || !strings.Contains(writer.String(), `供应商流失败`) ||
		strings.Contains(writer.String(), `message_stop`) {
		t.Fatalf("stream=%s", writer.String())
	}
}

func TestEncodeErrorMapsStatusRequestIDAndHidesCause(t *testing.T) {
	tests := []struct {
		code   gatewayservice.ErrorCode
		status int
		type_  string
	}{
		{gatewayservice.InvalidRequest, 400, "invalid_request_error"},
		{gatewayservice.CapabilityUnsupported, 400, "invalid_request_error"},
		{gatewayservice.AuthenticationFailed, 401, "authentication_error"},
		{gatewayservice.PermissionDenied, 403, "permission_error"},
		{gatewayservice.ResourceNotFound, 404, "not_found_error"},
		{gatewayservice.Conflict, 409, "invalid_request_error"},
		{gatewayservice.ConnectorFailed, 502, "api_error"},
		{gatewayservice.GatewayNotReady, 503, "overloaded_error"},
		{gatewayservice.InternalError, 500, "api_error"},
	}
	for _, test := range tests {
		var output bytes.Buffer
		status, err := EncodeError(&output, gatewayservice.NewError(test.code, "safe", "", errors.New("secret credential")), "req_1")
		if err != nil {
			t.Fatal(err)
		}
		if status != test.status || !strings.Contains(output.String(), `"type":"`+test.type_+`"`) || !strings.Contains(output.String(), `"request_id":"req_1"`) || strings.Contains(output.String(), "secret") {
			t.Fatalf("code=%s status=%d body=%s", test.code, status, output.String())
		}
	}
}

type flushBuffer struct {
	bytes.Buffer
	flushes int
}

func (w *flushBuffer) Flush() { w.flushes++ }

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
func (s *sliceStream) Close() error { return nil }

func validResponse() inference.Response {
	return inference.Response{
		ID: uuid.MustParse("0198dd23-236d-7b6c-8c10-fcd0fe1a3c11"), Model: "assistant",
		CreatedAt: time.Unix(1_777_777_777, 0).UTC(), StopReason: inference.StopEndTurn,
		Content: []inference.ContentBlock{{Type: inference.ContentText, Text: &inference.TextContent{Text: "hello"}}},
	}
}
