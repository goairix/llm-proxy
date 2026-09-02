package anthropic

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	gatewayport "github.com/goairix/llm-proxy/internal/application/gateway/port"
	inference "github.com/goairix/llm-proxy/internal/domain/inference/model"
)

func TestDecodeResponseMapsTextToolUsageAndLogicalIdentity(t *testing.T) {
	fixture, err := os.ReadFile("testdata/message_text_tool.json")
	if err != nil {
		t.Fatal(err)
	}
	invocation := fullInvocation(t)
	response, err := decodeResponse(bytes.NewReader(fixture), invocation, fixedIDGenerator, fixedClock)
	if err != nil {
		t.Fatal(err)
	}
	if response.ID != fixedResponseID || response.Model != invocation.Request.Model ||
		response.CreatedAt != fixedNow.UTC() || response.StopReason != inference.StopToolUse {
		t.Fatalf("response=%+v", response)
	}
	if len(response.Content) != 2 || response.Content[0].Text.Text != "hello" ||
		response.Content[1].ToolCall.ID != "call_one" || response.Content[1].ToolCall.Name != "weather" {
		t.Fatalf("content=%+v", response.Content)
	}
	var arguments map[string]string
	if err := json.Unmarshal(response.Content[1].ToolCall.Arguments, &arguments); err != nil || arguments["city"] != "Shanghai" {
		t.Fatalf("arguments=%s err=%v", response.Content[1].ToolCall.Arguments, err)
	}
	if response.Usage.InputTokens != 11 || response.Usage.OutputTokens != 7 ||
		response.Usage.CacheReadInputTokens != 3 || response.Usage.CacheWriteInputTokens != 2 {
		t.Fatalf("usage=%+v", response.Usage)
	}
}

func TestDecodeResponseAcceptsPresentZeroUsage(t *testing.T) {
	body := `{"id":"msg","type":"message","role":"assistant","model":"claude","content":[{"type":"text","text":"hello"}],"stop_reason":"end_turn","usage":{"input_tokens":0,"output_tokens":0}}`
	response, err := decodeResponse(strings.NewReader(body), fullInvocation(t), fixedIDGenerator, fixedClock)
	if err != nil {
		t.Fatal(err)
	}
	if response.Usage.InputTokens != 0 || response.Usage.OutputTokens != 0 {
		t.Fatalf("usage=%+v", response.Usage)
	}
}

func TestMapStopReason(t *testing.T) {
	for _, test := range []struct {
		value string
		want  inference.StopReason
		valid bool
	}{
		{value: "end_turn", want: inference.StopEndTurn, valid: true},
		{value: "max_tokens", want: inference.StopMaxTokens, valid: true},
		{value: "stop_sequence", want: inference.StopSequence, valid: true},
		{value: "tool_use", want: inference.StopToolUse, valid: true},
		{value: "refusal", want: inference.StopContentFilter, valid: true},
		{value: "model_context_window_exceeded", want: inference.StopMaxTokens, valid: true},
		{value: "pause_turn"},
		{value: "future_reason"},
	} {
		t.Run(test.value, func(t *testing.T) {
			got, err := mapStopReason(test.value)
			if test.valid {
				if err != nil || got != test.want {
					t.Fatalf("reason=%q err=%v", got, err)
				}
				return
			}
			assertConnectorErrorKind(t, err, gatewayport.UpstreamInvalidResponse, "")
		})
	}
}

func TestDecodeResponseMapsRefusalWithoutLeakingCategory(t *testing.T) {
	fixture, err := os.ReadFile("testdata/message_refusal.json")
	if err != nil {
		t.Fatal(err)
	}
	response, err := decodeResponse(bytes.NewReader(fixture), fullInvocation(t), fixedIDGenerator, fixedClock)
	if err != nil {
		t.Fatal(err)
	}
	if response.StopReason != inference.StopContentFilter || len(response.Content) != 1 ||
		response.Content[0].Refusal.Text != "无法处理该请求" {
		t.Fatalf("response=%+v", response)
	}
	encoded, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "private-category") {
		t.Fatalf("category leaked: %s", encoded)
	}
}

func TestDecodeResponseRejectsUnknownContentWithoutLeakingPayload(t *testing.T) {
	for _, content := range []string{
		`{"type":"thinking","thinking":"private-thinking","signature":"private-signature"}`,
		`{"type":"redacted_thinking","data":"private-redacted"}`,
		`{"type":"future_block","payload":"private-payload"}`,
	} {
		body := `{"id":"msg_private","type":"message","role":"assistant","model":"private-upstream-model","content":[` + content + `],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`
		_, err := decodeResponse(strings.NewReader(body), fullInvocation(t), fixedIDGenerator, fixedClock)
		assertConnectorErrorKind(t, err, gatewayport.UpstreamInvalidResponse, "")
		visible := err.Error()
		if cause := errors.Unwrap(err); cause != nil {
			visible += " " + fmt.Sprint(cause)
		}
		for _, secret := range []string{"private-thinking", "private-signature", "private-redacted", "private-payload", "private-upstream-model", "msg_private"} {
			if strings.Contains(visible, secret) {
				t.Fatalf("error leaked %q: %s", secret, visible)
			}
		}
	}
}

func TestDecodeResponseRejectsInvalidEnvelopeUsageAndTrailingJSON(t *testing.T) {
	for _, body := range []string{
		`{"id":"msg","type":"future","role":"assistant","model":"claude","content":[],"stop_reason":"max_tokens","usage":{"input_tokens":1,"output_tokens":1}}`,
		`{"id":"msg","type":"message","role":"user","model":"claude","content":[],"stop_reason":"max_tokens","usage":{"input_tokens":1,"output_tokens":1}}`,
		`{"id":"","type":"message","role":"assistant","model":"claude","content":[],"stop_reason":"max_tokens","usage":{"input_tokens":1,"output_tokens":1}}`,
		`{"id":"msg","type":"message","role":"assistant","model":"","content":[],"stop_reason":"max_tokens","usage":{"input_tokens":1,"output_tokens":1}}`,
		`{"id":"msg","type":"message","role":"assistant","model":"claude","content":[],"stop_reason":"max_tokens"}`,
		`{"id":"msg","type":"message","role":"assistant","model":"claude","content":[],"stop_reason":"max_tokens","usage":{"output_tokens":1}}`,
		`{"id":"msg","type":"message","role":"assistant","model":"claude","content":[],"stop_reason":"max_tokens","usage":{"input_tokens":1}}`,
		`{"id":"msg","type":"message","role":"assistant","model":"claude","content":[],"stop_reason":"max_tokens","usage":{"input_tokens":-1,"output_tokens":1}}`,
		`{"id":"msg","type":"message","role":"assistant","model":"claude","content":[],"stop_reason":"max_tokens","usage":{"input_tokens":1,"output_tokens":1}}{}`,
	} {
		_, err := decodeResponse(strings.NewReader(body), fullInvocation(t), fixedIDGenerator, fixedClock)
		assertConnectorErrorKind(t, err, gatewayport.UpstreamInvalidResponse, "")
	}
}
