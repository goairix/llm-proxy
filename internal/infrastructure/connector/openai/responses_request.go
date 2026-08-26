package openai

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	gatewayport "github.com/goairix/llm-proxy/internal/application/gateway/port"
	catalogmodel "github.com/goairix/llm-proxy/internal/domain/catalog/model"
	inference "github.com/goairix/llm-proxy/internal/domain/inference/model"
)

type responsesRequest struct {
	Model           string          `json:"model"`
	Instructions    string          `json:"instructions,omitempty"`
	Input           []any           `json:"input"`
	Tools           []responsesTool `json:"tools,omitempty"`
	ToolChoice      any             `json:"tool_choice,omitempty"`
	Text            *responsesText  `json:"text,omitempty"`
	Temperature     *float64        `json:"temperature,omitempty"`
	TopP            *float64        `json:"top_p,omitempty"`
	MaxOutputTokens *int64          `json:"max_output_tokens,omitempty"`
	Stream          bool            `json:"stream"`
	Store           bool            `json:"store"`
}

type responsesInputMessage struct {
	Type    string                  `json:"type"`
	Role    string                  `json:"role"`
	Content []responsesInputContent `json:"content"`
}

type responsesInputContent struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	ImageURL string `json:"image_url,omitempty"`
	Detail   string `json:"detail,omitempty"`
}

type responsesFunctionCall struct {
	Type      string `json:"type"`
	CallID    string `json:"call_id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type responsesFunctionOutput struct {
	Type   string `json:"type"`
	CallID string `json:"call_id"`
	Output string `json:"output"`
}

type responsesTool struct {
	Type        string          `json:"type"`
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters"`
	Strict      bool            `json:"strict,omitempty"`
}

type responsesText struct {
	Format responsesTextFormat `json:"format"`
}

type responsesTextFormat struct {
	Type        string          `json:"type"`
	Name        string          `json:"name,omitempty"`
	Description string          `json:"description,omitempty"`
	Strict      bool            `json:"strict,omitempty"`
	Schema      json.RawMessage `json:"schema,omitempty"`
}

func encodeResponsesRequest(invocation gatewayport.Invocation) ([]byte, error) {
	if err := invocation.Request.Validate(); err != nil {
		return nil, fmt.Errorf("validate Responses request: %w", err)
	}
	if err := validateProvider(invocation.Provider.ConnectorType, invocation.Provider); err != nil {
		return nil, err
	}
	if invocation.Deployment.ProviderID != invocation.Provider.ID || invocation.Deployment.UpstreamProtocol != catalogmodel.UpstreamResponses {
		return nil, fmt.Errorf("deployment does not match Responses provider")
	}
	if invocation.Request.Stop.Set {
		return nil, parameterUnsupported("stop", fmt.Errorf("Responses does not support stop sequences"))
	}
	if invocation.Request.MaxTokens.Set && invocation.Request.MaxTokens.Value == 0 {
		return nil, parameterUnsupported("max_tokens", fmt.Errorf("Responses requires a positive output token limit"))
	}

	request := responsesRequest{Model: invocation.Deployment.UpstreamModel, Stream: invocation.Request.Stream, Store: false}
	var instructions []string
	for _, message := range invocation.Request.Messages {
		if message.Role == inference.RoleSystem || message.Role == inference.RoleDeveloper {
			for _, block := range message.Content {
				instructions = append(instructions, block.Text.Text)
			}
			continue
		}
		items, err := encodeResponsesInput(message)
		if err != nil {
			return nil, err
		}
		request.Input = append(request.Input, items...)
	}
	request.Instructions = strings.Join(instructions, "\n")
	for _, tool := range invocation.Request.Tools {
		request.Tools = append(request.Tools, responsesTool{Type: "function", Name: tool.Name, Description: tool.Description, Parameters: tool.InputSchema, Strict: tool.Strict})
	}
	request.ToolChoice = encodeResponsesToolChoice(invocation.Request.ToolChoice)
	request.Text = encodeResponsesText(invocation.Request.StructuredOutput)
	if invocation.Request.Temperature.Set {
		request.Temperature = &invocation.Request.Temperature.Value
	}
	if invocation.Request.TopP.Set {
		request.TopP = &invocation.Request.TopP.Value
	}
	if invocation.Request.MaxTokens.Set {
		request.MaxOutputTokens = &invocation.Request.MaxTokens.Value
	}
	payload, err := json.Marshal(request)
	if err != nil {
		return nil, fmt.Errorf("encode Responses request: %w", err)
	}
	return payload, nil
}

func encodeResponsesInput(message inference.Message) ([]any, error) {
	items := make([]any, 0, len(message.Content))
	content := make([]responsesInputContent, 0, len(message.Content))
	flushContent := func() {
		if len(content) == 0 {
			return
		}
		items = append(items, responsesInputMessage{Type: "message", Role: string(message.Role), Content: content})
		content = nil
	}
	for _, block := range message.Content {
		switch block.Type {
		case inference.ContentText:
			contentType := "input_text"
			if message.Role == inference.RoleAssistant {
				contentType = "output_text"
			}
			content = append(content, responsesInputContent{Type: contentType, Text: block.Text.Text})
		case inference.ContentImage:
			url := block.Image.Source.Data
			if block.Image.Source.Type == inference.ImageBase64 {
				url = "data:" + block.Image.Source.MediaType + ";base64," + block.Image.Source.Data
			}
			content = append(content, responsesInputContent{Type: "input_image", ImageURL: url, Detail: block.Image.Source.Detail})
		case inference.ContentToolCall:
			flushContent()
			items = append(items, responsesFunctionCall{Type: "function_call", CallID: block.ToolCall.ID, Name: block.ToolCall.Name, Arguments: string(block.ToolCall.Arguments)})
		case inference.ContentToolResult:
			flushContent()
			output, err := encodeResponsesToolOutput(block.ToolResult)
			if err != nil {
				return nil, err
			}
			items = append(items, responsesFunctionOutput{Type: "function_call_output", CallID: block.ToolResult.ToolCallID, Output: output})
		default:
			return nil, parameterUnsupported("messages", fmt.Errorf("Responses input does not support content type %q", block.Type))
		}
	}
	flushContent()
	return items, nil
}

func encodeResponsesToolOutput(result *inference.ToolResultContent) (string, error) {
	if result.Text != nil {
		return *result.Text, nil
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, result.JSON); err != nil {
		return "", fmt.Errorf("encode function output: %w", err)
	}
	return compact.String(), nil
}

func encodeResponsesToolChoice(choice *inference.ToolChoice) any {
	if choice == nil {
		return nil
	}
	if choice.Mode != inference.ToolChoiceSpecific {
		return string(choice.Mode)
	}
	return map[string]string{"type": "function", "name": choice.Name}
}

func encodeResponsesText(output *inference.StructuredOutput) *responsesText {
	if output == nil {
		return nil
	}
	format := responsesTextFormat{Type: string(output.Type)}
	if output.Type == inference.StructuredJSONSchema {
		format.Name, format.Description, format.Strict, format.Schema = output.Name, output.Description, output.Strict, output.Schema
	}
	return &responsesText{Format: format}
}
