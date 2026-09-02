package anthropic

import (
	"encoding/json"
	"strings"
	"testing"

	gatewayport "github.com/goairix/llm-proxy/internal/application/gateway/port"
	catalogmodel "github.com/goairix/llm-proxy/internal/domain/catalog/model"
	inference "github.com/goairix/llm-proxy/internal/domain/inference/model"
)

func TestEncodeRequestMapsUnifiedRequestToMessages(t *testing.T) {
	invocation := fullInvocation(t)
	invocation.Request = inference.Request{
		Model: "assistant",
		Messages: []inference.Message{
			{Role: inference.RoleSystem, Content: []inference.ContentBlock{textBlock("system")}},
			{Role: inference.RoleDeveloper, Content: []inference.ContentBlock{textBlock("developer")}},
			{Role: inference.RoleUser, Content: []inference.ContentBlock{
				{Type: inference.ContentImage, Image: &inference.ImageContent{Source: inference.ImageSource{Type: inference.ImageURL, Data: "https://images.example/cat.png", Detail: "high"}}},
				{Type: inference.ContentImage, Image: &inference.ImageContent{Source: inference.ImageSource{Type: inference.ImageBase64, MediaType: "image/png", Data: "aGVsbG8=", Detail: "low"}}},
			}},
			{Role: inference.RoleAssistant, Content: []inference.ContentBlock{{
				Type: inference.ContentToolCall,
				ToolCall: &inference.ToolCallContent{
					ID: "call_one", Name: "weather", Arguments: json.RawMessage(`{"city":"Shanghai"}`),
				},
			}}},
			{Role: inference.RoleUser, Content: []inference.ContentBlock{
				{Type: inference.ContentToolResult, ToolResult: &inference.ToolResultContent{
					ToolCallID: "call_one", JSON: json.RawMessage(`{ "temperature": 21 }`),
				}},
				textBlock("continue"),
			}},
		},
		Tools: []inference.Tool{{
			Name: "weather", Description: "Get weather", Strict: true,
			InputSchema: json.RawMessage(`{"type":"object","properties":{"city":{"type":"string"}},"required":["city"]}`),
		}},
		ToolChoice: &inference.ToolChoice{Mode: inference.ToolChoiceSpecific, Name: "weather"},
		StructuredOutput: &inference.StructuredOutput{
			Type: inference.StructuredJSONSchema, Name: "weather_output", Description: "Weather result", Strict: true,
			Schema: json.RawMessage(`{"type":"object","properties":{"summary":{"type":"string"}},"required":["summary"]}`),
		},
		Temperature: inference.Some(0.5),
		TopP:        inference.Some(0.8),
		MaxTokens:   inference.Some[int64](128),
		Stop:        inference.Some([]string{"STOP"}),
		Stream:      true,
	}

	payload, err := encodeRequest(invocation)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(payload), "detail") {
		t.Fatalf("request leaked unsupported image detail: %s", payload)
	}
	var got messageRequest
	if err := json.Unmarshal(payload, &got); err != nil {
		t.Fatal(err)
	}
	if got.Model != "claude-upstream" || got.MaxTokens != 128 || !got.Stream || len(got.System) != 2 || len(got.Messages) != 3 {
		t.Fatalf("request=%+v", got)
	}
	if got.System[0].Text != "system" || got.System[1].Text != "developer" {
		t.Fatalf("system=%+v", got.System)
	}
	images := got.Messages[0].Content
	if len(images) != 2 || images[0].Source == nil || images[0].Source.Type != "url" || images[0].Source.URL != "https://images.example/cat.png" ||
		images[1].Source == nil || images[1].Source.Type != "base64" || images[1].Source.MediaType != "image/png" || images[1].Source.Data != "aGVsbG8=" {
		t.Fatalf("images=%+v", images)
	}
	if got.Messages[1].Content[0].Type != "tool_use" || string(got.Messages[1].Content[0].Input) != `{"city":"Shanghai"}` {
		t.Fatalf("tool call=%+v", got.Messages[1])
	}
	if got.Messages[2].Content[0].Type != "tool_result" || got.Messages[2].Content[0].Content != `{"temperature":21}` ||
		got.Messages[2].Content[1].Text != "continue" {
		t.Fatalf("tool result=%+v", got.Messages[2])
	}
	if len(got.Tools) != 1 || !got.Tools[0].Strict || got.ToolChoice == nil ||
		got.ToolChoice.Type != "tool" || got.ToolChoice.Name != "weather" {
		t.Fatalf("tools=%+v choice=%+v", got.Tools, got.ToolChoice)
	}
	if got.OutputConfig == nil || got.OutputConfig.Format.Type != "json_schema" ||
		string(got.OutputConfig.Format.Schema) != string(invocation.Request.StructuredOutput.Schema) {
		t.Fatalf("output_config=%+v", got.OutputConfig)
	}
	if got.Temperature == nil || *got.Temperature != 0.5 || got.TopP == nil || *got.TopP != 0.8 ||
		got.StopSequences == nil || len(*got.StopSequences) != 1 || (*got.StopSequences)[0] != "STOP" {
		t.Fatalf("generation parameters=%+v", got)
	}
}

func TestEncodeRequestMapsToolChoiceModes(t *testing.T) {
	for _, test := range []struct {
		mode     inference.ToolChoiceMode
		name     string
		wantType string
		wantName string
	}{
		{mode: inference.ToolChoiceAuto, wantType: "auto"},
		{mode: inference.ToolChoiceNone, wantType: "none"},
		{mode: inference.ToolChoiceRequired, wantType: "any"},
		{mode: inference.ToolChoiceSpecific, name: "weather", wantType: "tool", wantName: "weather"},
	} {
		t.Run(string(test.mode), func(t *testing.T) {
			invocation := fullInvocation(t)
			invocation.Request.Tools = []inference.Tool{{Name: "weather", InputSchema: json.RawMessage(`{"type":"object"}`)}}
			invocation.Request.ToolChoice = &inference.ToolChoice{Mode: test.mode, Name: test.name}
			payload, err := encodeRequest(invocation)
			if err != nil {
				t.Fatal(err)
			}
			var got messageRequest
			if err := json.Unmarshal(payload, &got); err != nil {
				t.Fatal(err)
			}
			if got.ToolChoice == nil || got.ToolChoice.Type != test.wantType || got.ToolChoice.Name != test.wantName {
				t.Fatalf("choice=%+v", got.ToolChoice)
			}
		})
	}
}

func TestEncodeRequestRejectsUnsupportedShapesBeforeNetwork(t *testing.T) {
	tests := []struct {
		name, param string
		mutate      func(*gatewayport.Invocation)
	}{
		{name: "missing max", param: "max_tokens", mutate: func(v *gatewayport.Invocation) {
			v.Request.MaxTokens = inference.Optional[int64]{}
		}},
		{name: "zero max", param: "max_tokens", mutate: func(v *gatewayport.Invocation) {
			v.Request.MaxTokens = inference.Some[int64](0)
		}},
		{name: "temperature above one", param: "temperature", mutate: func(v *gatewayport.Invocation) {
			v.Request.Temperature = inference.Some(1.1)
		}},
		{name: "late developer", param: "messages", mutate: func(v *gatewayport.Invocation) {
			v.Request.Messages = append(v.Request.Messages, inference.Message{
				Role: inference.RoleDeveloper, Content: []inference.ContentBlock{textBlock("late")},
			})
		}},
		{name: "late tool result", param: "messages", mutate: func(v *gatewayport.Invocation) {
			result := "sunny"
			v.Request.Messages = []inference.Message{
				{Role: inference.RoleAssistant, Content: []inference.ContentBlock{{
					Type:     inference.ContentToolCall,
					ToolCall: &inference.ToolCallContent{ID: "call_one", Name: "weather", Arguments: json.RawMessage(`{}`)},
				}}},
				{Role: inference.RoleUser, Content: []inference.ContentBlock{
					textBlock("content before tool result"),
					{Type: inference.ContentToolResult, ToolResult: &inference.ToolResultContent{ToolCallID: "call_one", Text: &result}},
				}},
			}
		}},
		{name: "json object", param: "response_format", mutate: func(v *gatewayport.Invocation) {
			v.Request.StructuredOutput = &inference.StructuredOutput{Type: inference.StructuredJSONObject}
		}},
		{name: "refusal input", param: "messages", mutate: func(v *gatewayport.Invocation) {
			v.Request.Messages[0].Content = []inference.ContentBlock{{
				Type: inference.ContentRefusal, Refusal: &inference.RefusalContent{Text: "no"},
			}}
		}},
		{name: "only system messages", param: "messages", mutate: func(v *gatewayport.Invocation) {
			v.Request.Messages = []inference.Message{{Role: inference.RoleSystem, Content: []inference.ContentBlock{textBlock("system")}}}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			invocation := fullInvocation(t)
			test.mutate(&invocation)
			_, err := encodeRequest(invocation)
			assertConnectorErrorKind(t, err, gatewayport.ParameterUnsupported, test.param)
		})
	}
}

func TestEncodeRequestRejectsWrongDeploymentProtocol(t *testing.T) {
	invocation := fullInvocation(t)
	invocation.Deployment.UpstreamProtocol = catalogmodel.UpstreamResponses
	if _, err := encodeRequest(invocation); err == nil {
		t.Fatal("wrong deployment protocol was accepted")
	}
}
