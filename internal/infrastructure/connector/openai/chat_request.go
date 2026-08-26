package openai

import (
	"bytes"
	"encoding/json"
	"fmt"

	gatewayport "github.com/goairix/llm-proxy/internal/application/gateway/port"
	catalogmodel "github.com/goairix/llm-proxy/internal/domain/catalog/model"
	inference "github.com/goairix/llm-proxy/internal/domain/inference/model"
)

type chatRequest struct {
	Model               string               `json:"model"`
	Messages            []chatRequestMessage `json:"messages"`
	Tools               []chatTool           `json:"tools,omitempty"`
	ToolChoice          any                  `json:"tool_choice,omitempty"`
	ResponseFormat      *chatResponseFormat  `json:"response_format,omitempty"`
	Temperature         *float64             `json:"temperature,omitempty"`
	TopP                *float64             `json:"top_p,omitempty"`
	MaxTokens           *int64               `json:"max_tokens,omitempty"`
	MaxCompletionTokens *int64               `json:"max_completion_tokens,omitempty"`
	Stop                *[]string            `json:"stop,omitempty"`
	Stream              bool                 `json:"stream"`
	StreamOptions       *chatStreamOptions   `json:"stream_options,omitempty"`
}

type chatRequestMessage struct {
	Role       string         `json:"role"`
	Content    any            `json:"content,omitempty"`
	ToolCallID string         `json:"tool_call_id,omitempty"`
	ToolCalls  []chatToolCall `json:"tool_calls,omitempty"`
}

type chatContentPart struct {
	Type     string        `json:"type"`
	Text     string        `json:"text,omitempty"`
	ImageURL *chatImageURL `json:"image_url,omitempty"`
}

type chatImageURL struct {
	URL    string `json:"url"`
	Detail string `json:"detail,omitempty"`
}

type chatTool struct {
	Type     string           `json:"type"`
	Function chatToolFunction `json:"function"`
}

type chatToolFunction struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters"`
	Strict      bool            `json:"strict,omitempty"`
}

type chatToolCall struct {
	Index    *int             `json:"index,omitempty"`
	ID       string           `json:"id,omitempty"`
	Type     string           `json:"type,omitempty"`
	Function chatFunctionCall `json:"function"`
}

type chatFunctionCall struct {
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
}

type chatResponseFormat struct {
	Type       string          `json:"type"`
	JSONSchema *chatJSONSchema `json:"json_schema,omitempty"`
}

type chatJSONSchema struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Strict      bool            `json:"strict,omitempty"`
	Schema      json.RawMessage `json:"schema"`
}

type chatStreamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

func encodeChatRequest(invocation gatewayport.Invocation) ([]byte, error) {
	if err := invocation.Request.Validate(); err != nil {
		return nil, fmt.Errorf("validate chat request: %w", err)
	}
	if err := validateProvider(invocation.Provider.ConnectorType, invocation.Provider); err != nil {
		return nil, err
	}
	if invocation.Deployment.ProviderID != invocation.Provider.ID || invocation.Deployment.UpstreamProtocol != catalogmodel.UpstreamChatCompletions {
		return nil, fmt.Errorf("deployment does not match Chat Completions provider")
	}
	if invocation.Request.MaxTokens.Set && invocation.Request.MaxTokens.Value == 0 {
		return nil, parameterUnsupported("max_tokens", fmt.Errorf("Chat Completions requires a positive output token limit"))
	}

	request := chatRequest{Model: invocation.Deployment.UpstreamModel, Stream: invocation.Request.Stream}
	for _, message := range invocation.Request.Messages {
		mapped, err := encodeChatMessages(message)
		if err != nil {
			return nil, err
		}
		request.Messages = append(request.Messages, mapped...)
	}
	for _, tool := range invocation.Request.Tools {
		request.Tools = append(request.Tools, chatTool{Type: "function", Function: chatToolFunction{
			Name: tool.Name, Description: tool.Description, Parameters: tool.InputSchema, Strict: tool.Strict,
		}})
	}
	request.ToolChoice = encodeChatToolChoice(invocation.Request.ToolChoice)
	request.ResponseFormat = encodeChatResponseFormat(invocation.Request.StructuredOutput)
	if invocation.Request.Temperature.Set {
		request.Temperature = &invocation.Request.Temperature.Value
	}
	if invocation.Request.TopP.Set {
		request.TopP = &invocation.Request.TopP.Value
	}
	if invocation.Request.MaxTokens.Set {
		if invocation.Provider.ConnectorType == catalogmodel.ConnectorOpenAI {
			request.MaxCompletionTokens = &invocation.Request.MaxTokens.Value
		} else {
			request.MaxTokens = &invocation.Request.MaxTokens.Value
		}
	}
	if invocation.Request.Stop.Set {
		request.Stop = &invocation.Request.Stop.Value
	}
	if invocation.Request.Stream {
		request.StreamOptions = &chatStreamOptions{IncludeUsage: true}
	}
	payload, err := json.Marshal(request)
	if err != nil {
		return nil, fmt.Errorf("encode Chat Completions request: %w", err)
	}
	return payload, nil
}

func encodeChatMessages(message inference.Message) ([]chatRequestMessage, error) {
	var regular chatRequestMessage
	regular.Role = string(message.Role)
	parts := make([]chatContentPart, 0, len(message.Content))
	results := make([]chatRequestMessage, 0)
	for _, block := range message.Content {
		switch block.Type {
		case inference.ContentText:
			parts = append(parts, chatContentPart{Type: "text", Text: block.Text.Text})
		case inference.ContentImage:
			url := block.Image.Source.Data
			if block.Image.Source.Type == inference.ImageBase64 {
				url = "data:" + block.Image.Source.MediaType + ";base64," + block.Image.Source.Data
			}
			parts = append(parts, chatContentPart{Type: "image_url", ImageURL: &chatImageURL{URL: url, Detail: block.Image.Source.Detail}})
		case inference.ContentToolCall:
			regular.ToolCalls = append(regular.ToolCalls, chatToolCall{ID: block.ToolCall.ID, Type: "function", Function: chatFunctionCall{Name: block.ToolCall.Name, Arguments: string(block.ToolCall.Arguments)}})
		case inference.ContentToolResult:
			content, err := encodeChatToolResult(block.ToolResult)
			if err != nil {
				return nil, err
			}
			results = append(results, chatRequestMessage{Role: "tool", ToolCallID: block.ToolResult.ToolCallID, Content: content})
		}
	}
	if len(results) > 0 {
		return results, nil
	}
	if len(parts) == 1 && parts[0].Type == "text" {
		regular.Content = parts[0].Text
	} else if len(parts) > 0 {
		regular.Content = parts
	}
	return []chatRequestMessage{regular}, nil
}

func encodeChatToolResult(result *inference.ToolResultContent) (string, error) {
	if result.Text != nil {
		return *result.Text, nil
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, result.JSON); err != nil {
		return "", fmt.Errorf("encode tool result: %w", err)
	}
	return compact.String(), nil
}

func encodeChatToolChoice(choice *inference.ToolChoice) any {
	if choice == nil {
		return nil
	}
	if choice.Mode != inference.ToolChoiceSpecific {
		return string(choice.Mode)
	}
	return map[string]any{"type": "function", "function": map[string]string{"name": choice.Name}}
}

func encodeChatResponseFormat(output *inference.StructuredOutput) *chatResponseFormat {
	if output == nil {
		return nil
	}
	if output.Type == inference.StructuredJSONObject {
		return &chatResponseFormat{Type: "json_object"}
	}
	return &chatResponseFormat{Type: "json_schema", JSONSchema: &chatJSONSchema{
		Name: output.Name, Description: output.Description, Strict: output.Strict, Schema: output.Schema,
	}}
}
