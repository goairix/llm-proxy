package openai

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

func TestDecodeTextRequestPreservesRolesAndExplicitZero(t *testing.T) {
	body, err := os.Open("testdata/text_request.json")
	if err != nil {
		t.Fatal(err)
	}
	defer body.Close()

	request, err := Decode(body)
	if err != nil {
		t.Fatal(err)
	}
	if request.Model != "assistant" || len(request.Messages) != 3 {
		t.Fatalf("request=%+v", request)
	}
	if request.Messages[0].Role != inference.RoleSystem || request.Messages[1].Role != inference.RoleDeveloper || request.Messages[2].Role != inference.RoleUser {
		t.Fatalf("roles=%+v", request.Messages)
	}
	if !request.Temperature.Set || request.Temperature.Value != 0 || request.TopP.Set || request.MaxTokens.Set {
		t.Fatalf("optionals temperature=%+v topP=%+v maxTokens=%+v", request.Temperature, request.TopP, request.MaxTokens)
	}
}

func TestDecodeMapsContentPartsToolsResultsAndStructuredOutput(t *testing.T) {
	body, err := os.Open("testdata/tool_request.json")
	if err != nil {
		t.Fatal(err)
	}
	defer body.Close()

	request, err := Decode(body)
	if err != nil {
		t.Fatal(err)
	}
	if len(request.Messages) != 3 || request.Messages[1].Content[0].ToolCall == nil || request.Messages[2].Content[0].ToolResult == nil {
		t.Fatalf("messages=%+v", request.Messages)
	}
	if request.Messages[2].Role != inference.RoleUser || request.Messages[2].Content[0].ToolResult.ToolCallID != "call_1" {
		t.Fatalf("tool result=%+v", request.Messages[2])
	}
	if len(request.Tools) != 1 || !request.Tools[0].Strict || request.ToolChoice == nil || request.ToolChoice.Mode != inference.ToolChoiceSpecific {
		t.Fatalf("tools=%+v choice=%+v", request.Tools, request.ToolChoice)
	}
	if request.StructuredOutput == nil || request.StructuredOutput.Type != inference.StructuredJSONSchema || request.StructuredOutput.Name != "answer" {
		t.Fatalf("structured=%+v", request.StructuredOutput)
	}
}

func TestDecodeMapsImageURLStopAndStreamOptions(t *testing.T) {
	body := `{
		"model":"assistant",
		"messages":[{"role":"user","content":[
			{"type":"text","text":"describe"},
			{"type":"image_url","image_url":{"url":"https://example.com/image.png","detail":"low"}}
		]}],
		"stop":["END"],"max_completion_tokens":0,"stream":true,
		"stream_options":{"include_usage":true}
	}`
	decoded, err := DecodeWithOptions(strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request := decoded.Request
	if len(request.Messages[0].Content) != 2 || request.Messages[0].Content[1].Image == nil {
		t.Fatalf("content=%+v", request.Messages[0].Content)
	}
	if !request.Stop.Set || len(request.Stop.Value) != 1 || !request.MaxTokens.Set || request.MaxTokens.Value != 0 || !request.Stream || !decoded.IncludeUsage {
		t.Fatalf("request=%+v decoded=%+v", request, decoded)
	}
}

func TestDecodeRejectsConflictingTokensUnsupportedToolsAndOversizedBody(t *testing.T) {
	tests := []string{
		`{"model":"assistant","messages":[{"role":"user","content":"hi"}],"max_tokens":1,"max_completion_tokens":1}`,
		`{"model":"assistant","messages":[{"role":"user","content":"hi"}],"tools":[{"type":"web_search"}]}`,
		`{"model":"assistant","messages":[{"role":"user","content":[{"type":"input_audio"}]}]}`,
		`{"model":"assistant","messages":[{"role":"user","content":"call"},{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"tool","arguments":"{}"}}]},{"role":"tool","tool_call_id":"call_1","content":null}]}`,
		`{"model":"assistant","messages":[{"role":"user","content":"hi"}],"response_format":{"type":"json_schema","json_schema":{"name":"","schema":{"type":"object"}}}}`,
	}
	for _, body := range tests {
		if _, err := Decode(strings.NewReader(body)); err == nil {
			t.Fatalf("Decode(%s) succeeded", body)
		}
	}
	oversized := strings.NewReader(`{"model":"` + strings.Repeat("x", maxRequestBodyBytes) + `"}`)
	if _, err := Decode(oversized); err == nil {
		t.Fatal("Decode accepted oversized body")
	}
}

func TestEncodeResponseMapsTextToolsFinishReasonAndUsage(t *testing.T) {
	response := validResponse()
	response.Content = append(response.Content, inference.ContentBlock{Type: inference.ContentToolCall, ToolCall: &inference.ToolCallContent{
		ID: "call_1", Name: "weather", Arguments: json.RawMessage(`{"city":"Shanghai"}`),
	}})
	response.StopReason = inference.StopToolUse
	response.Usage = inference.Usage{InputTokens: 10, OutputTokens: 4, CacheReadInputTokens: 3}
	var output bytes.Buffer
	if err := EncodeResponse(&output, response); err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		ID      string `json:"id"`
		Object  string `json:"object"`
		Created int64  `json:"created"`
		Model   string `json:"model"`
		Choices []struct {
			FinishReason string `json:"finish_reason"`
			Message      struct {
				Role      string `json:"role"`
				Content   string `json:"content"`
				ToolCalls []struct {
					Type     string `json:"type"`
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
		Usage struct {
			PromptTokens        int64 `json:"prompt_tokens"`
			CompletionTokens    int64 `json:"completion_tokens"`
			TotalTokens         int64 `json:"total_tokens"`
			PromptTokensDetails struct {
				CachedTokens int64 `json:"cached_tokens"`
			} `json:"prompt_tokens_details"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(output.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.ID != "chatcmpl-"+response.ID.String() || decoded.Object != "chat.completion" || decoded.Created != response.CreatedAt.Unix() || decoded.Model != response.Model {
		t.Fatalf("response=%+v", decoded)
	}
	choice := decoded.Choices[0]
	if choice.FinishReason != "tool_calls" || choice.Message.Role != "assistant" || choice.Message.Content != "hello" || len(choice.Message.ToolCalls) != 1 || choice.Message.ToolCalls[0].Function.Name != "weather" {
		t.Fatalf("choice=%+v", choice)
	}
	if decoded.Usage.PromptTokens != 10 || decoded.Usage.CompletionTokens != 4 || decoded.Usage.TotalTokens != 14 || decoded.Usage.PromptTokensDetails.CachedTokens != 3 {
		t.Fatalf("usage=%+v", decoded.Usage)
	}
}

func TestEncodeResponseAndStreamMapRefusal(t *testing.T) {
	response := validResponse()
	response.Content = []inference.ContentBlock{{Type: inference.ContentRefusal, Refusal: &inference.RefusalContent{Text: "无法协助"}}}
	response.StopReason = inference.StopContentFilter
	var output bytes.Buffer
	if err := EncodeResponse(&output, response); err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		Choices []struct {
			FinishReason string `json:"finish_reason"`
			Message      struct {
				Refusal *string `json:"refusal"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(output.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded.Choices) != 1 || decoded.Choices[0].FinishReason != "content_filter" ||
		decoded.Choices[0].Message.Refusal == nil || *decoded.Choices[0].Message.Refusal != "无法协助" {
		t.Fatalf("response=%s", output.String())
	}

	writer := &flushBuffer{}
	events := []inference.Event{
		inference.NewResponseStartAt(response.ID, response.Model, response.CreatedAt),
		inference.NewContentBlockStart(0, inference.ContentRefusal),
		inference.NewRefusalDelta(0, "无法"),
		inference.NewRefusalDelta(0, "协助"),
		inference.NewContentBlockStop(0),
		inference.NewResponseFinish(inference.StopContentFilter),
	}
	if err := EncodeStream(context.Background(), writer, &sliceStream{events: events}, false); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(writer.String(), `"refusal":"无法"`) || !strings.Contains(writer.String(), `"refusal":"协助"`) ||
		!strings.Contains(writer.String(), `"finish_reason":"content_filter"`) {
		t.Fatalf("stream=%s", writer.String())
	}
}

func TestEncodeStreamMatchesGoldenAndFlushesEveryFrame(t *testing.T) {
	response := validResponse()
	events := []inference.Event{
		inference.NewResponseStartAt(response.ID, response.Model, response.CreatedAt),
		inference.NewContentBlockStart(0, inference.ContentText),
		inference.NewTextDelta(0, "hel"),
		inference.NewTextDelta(0, "lo"),
		inference.NewContentBlockStop(0),
		inference.NewUsageUpdate(inference.Usage{InputTokens: 2, OutputTokens: 1}),
		inference.NewResponseFinish(inference.StopEndTurn),
	}
	writer := &flushBuffer{}
	if err := EncodeStream(context.Background(), writer, &sliceStream{events: events}, true); err != nil {
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

func TestEncodeStreamWritesSafeErrorWithoutDone(t *testing.T) {
	response := validResponse()
	writer := &flushBuffer{}
	stream := &sliceStream{events: []inference.Event{
		inference.NewResponseStartAt(response.ID, response.Model, response.CreatedAt),
		inference.NewStreamError("供应商流失败"),
	}}
	if err := EncodeStream(context.Background(), writer, stream, false); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(writer.String(), `"message":"供应商流失败"`) || strings.Contains(writer.String(), "[DONE]") {
		t.Fatalf("stream=%s", writer.String())
	}
}

func TestEncodeStreamPreservesMultipleToolIndexesAndArgumentDeltas(t *testing.T) {
	response := validResponse()
	events := []inference.Event{
		inference.NewResponseStartAt(response.ID, response.Model, response.CreatedAt),
		inference.NewToolCallStart(0, "call_1", "first"),
		inference.NewToolArgumentsDelta(0, `{}`),
		inference.NewContentBlockStop(0),
		inference.NewToolCallStart(1, "call_2", "second"),
		inference.NewToolArgumentsDelta(1, `{"value":`),
		inference.NewToolArgumentsDelta(1, `1}`),
		inference.NewContentBlockStop(1),
		inference.NewUsageUpdate(inference.Usage{}),
		inference.NewResponseFinish(inference.StopToolUse),
	}
	writer := &flushBuffer{}
	if err := EncodeStream(context.Background(), writer, &sliceStream{events: events}, false); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(writer.String(), `"index":0,"id":"call_1"`) ||
		!strings.Contains(writer.String(), `"index":1,"id":"call_2"`) ||
		!strings.Contains(writer.String(), `"index":1,"function":{"arguments":"{\"value\":"}`) ||
		!strings.Contains(writer.String(), `"finish_reason":"tool_calls"`) {
		t.Fatalf("stream=%s", writer.String())
	}
}

func TestEncodeErrorMapsStatusAndNeverLeaksCause(t *testing.T) {
	secret := errors.New("upstream secret credential")
	var output bytes.Buffer
	status, err := EncodeError(&output, gatewayservice.NewError(gatewayservice.ConnectorFailed, "供应商请求失败", "", secret))
	if err != nil {
		t.Fatal(err)
	}
	if status != 502 || strings.Contains(output.String(), secret.Error()) || !strings.Contains(output.String(), `"code":"connector_failed"`) {
		t.Fatalf("status=%d body=%s", status, output.String())
	}
}

func TestEncodeErrorStatusTable(t *testing.T) {
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
		{gatewayservice.Conflict, 409, "conflict_error"},
		{gatewayservice.ConnectorFailed, 502, "api_error"},
		{gatewayservice.GatewayNotReady, 503, "server_error"},
		{gatewayservice.InternalError, 500, "server_error"},
	}
	for _, test := range tests {
		var output bytes.Buffer
		status, err := EncodeError(&output, gatewayservice.NewError(test.code, "safe", "model", errors.New("secret")))
		if err != nil {
			t.Fatal(err)
		}
		var envelope errorEnvelope
		if err := json.Unmarshal(output.Bytes(), &envelope); err != nil {
			t.Fatal(err)
		}
		if status != test.status || envelope.Error.Type != test.type_ || envelope.Error.Code != string(test.code) || envelope.Error.Param == nil || *envelope.Error.Param != "model" {
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
