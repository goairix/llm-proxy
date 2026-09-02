package anthropic

import (
	"bytes"
	"encoding/json"
	"fmt"

	gatewayport "github.com/goairix/llm-proxy/internal/application/gateway/port"
	catalogmodel "github.com/goairix/llm-proxy/internal/domain/catalog/model"
	inference "github.com/goairix/llm-proxy/internal/domain/inference/model"
)

type messageRequest struct {
	Model         string               `json:"model"`
	MaxTokens     int64                `json:"max_tokens"`
	System        []requestContent     `json:"system,omitempty"`
	Messages      []requestMessage     `json:"messages"`
	Tools         []requestTool        `json:"tools,omitempty"`
	ToolChoice    *requestToolChoice   `json:"tool_choice,omitempty"`
	OutputConfig  *requestOutputConfig `json:"output_config,omitempty"`
	Temperature   *float64             `json:"temperature,omitempty"`
	TopP          *float64             `json:"top_p,omitempty"`
	StopSequences *[]string            `json:"stop_sequences,omitempty"`
	Stream        bool                 `json:"stream"`
}

type requestMessage struct {
	Role    string           `json:"role"`
	Content []requestContent `json:"content"`
}

type requestContent struct {
	Type      string              `json:"type"`
	Text      string              `json:"text,omitempty"`
	Source    *requestImageSource `json:"source,omitempty"`
	ID        string              `json:"id,omitempty"`
	Name      string              `json:"name,omitempty"`
	Input     json.RawMessage     `json:"input,omitempty"`
	ToolUseID string              `json:"tool_use_id,omitempty"`
	Content   any                 `json:"content,omitempty"`
	IsError   bool                `json:"is_error,omitempty"`
}

type requestImageSource struct {
	Type      string `json:"type"`
	MediaType string `json:"media_type,omitempty"`
	Data      string `json:"data,omitempty"`
	URL       string `json:"url,omitempty"`
}

type requestTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"input_schema"`
	Strict      bool            `json:"strict,omitempty"`
}

type requestToolChoice struct {
	Type string `json:"type"`
	Name string `json:"name,omitempty"`
}

type requestOutputConfig struct {
	Format requestOutputFormat `json:"format"`
}

type requestOutputFormat struct {
	Type   string          `json:"type"`
	Schema json.RawMessage `json:"schema"`
}

func encodeRequest(invocation gatewayport.Invocation) ([]byte, error) {
	if err := invocation.Request.Validate(); err != nil {
		return nil, fmt.Errorf("validate Anthropic request: %w", err)
	}
	if invocation.Deployment.UpstreamProtocol != catalogmodel.UpstreamAnthropicMessages {
		return nil, fmt.Errorf("deployment does not match Anthropic Messages provider")
	}
	if !invocation.Request.MaxTokens.Set || invocation.Request.MaxTokens.Value <= 0 {
		return nil, parameterUnsupported("max_tokens", fmt.Errorf("Anthropic Messages requires max_tokens greater than zero"))
	}
	if invocation.Request.Temperature.Set && invocation.Request.Temperature.Value > 1 {
		return nil, parameterUnsupported("temperature", fmt.Errorf("Anthropic temperature must not exceed one"))
	}

	request := messageRequest{
		Model:     invocation.Deployment.UpstreamModel,
		MaxTokens: invocation.Request.MaxTokens.Value,
		Stream:    invocation.Request.Stream,
	}
	seenConversation := false
	for _, message := range invocation.Request.Messages {
		if message.Role == inference.RoleSystem || message.Role == inference.RoleDeveloper {
			if seenConversation {
				return nil, parameterUnsupported("messages", fmt.Errorf("system and developer messages must precede conversation messages"))
			}
			for _, block := range message.Content {
				request.System = append(request.System, requestContent{Type: "text", Text: block.Text.Text})
			}
			continue
		}
		seenConversation = true
		mapped, err := encodeMessage(message)
		if err != nil {
			return nil, err
		}
		request.Messages = append(request.Messages, mapped)
	}
	if len(request.Messages) == 0 {
		return nil, parameterUnsupported("messages", fmt.Errorf("Anthropic Messages requires at least one conversation message"))
	}

	for _, tool := range invocation.Request.Tools {
		request.Tools = append(request.Tools, requestTool{
			Name: tool.Name, Description: tool.Description, InputSchema: tool.InputSchema, Strict: tool.Strict,
		})
	}
	request.ToolChoice = encodeToolChoice(invocation.Request.ToolChoice)
	outputConfig, err := encodeOutputConfig(invocation.Request.StructuredOutput)
	if err != nil {
		return nil, err
	}
	request.OutputConfig = outputConfig
	if invocation.Request.Temperature.Set {
		request.Temperature = &invocation.Request.Temperature.Value
	}
	if invocation.Request.TopP.Set {
		request.TopP = &invocation.Request.TopP.Value
	}
	if invocation.Request.Stop.Set {
		request.StopSequences = &invocation.Request.Stop.Value
	}

	payload, err := json.Marshal(request)
	if err != nil {
		return nil, fmt.Errorf("encode Anthropic Messages request: %w", err)
	}
	return payload, nil
}

func encodeMessage(message inference.Message) (requestMessage, error) {
	result := requestMessage{Role: string(message.Role)}
	seenRegular := false
	for _, block := range message.Content {
		var mapped requestContent
		switch block.Type {
		case inference.ContentText:
			seenRegular = true
			mapped = requestContent{Type: "text", Text: block.Text.Text}
		case inference.ContentImage:
			seenRegular = true
			source := requestImageSource{Type: string(block.Image.Source.Type)}
			if block.Image.Source.Type == inference.ImageBase64 {
				source.MediaType = block.Image.Source.MediaType
				source.Data = block.Image.Source.Data
			} else {
				source.URL = block.Image.Source.Data
			}
			mapped = requestContent{Type: "image", Source: &source}
		case inference.ContentToolCall:
			seenRegular = true
			mapped = requestContent{
				Type: "tool_use", ID: block.ToolCall.ID, Name: block.ToolCall.Name,
				Input: block.ToolCall.Arguments,
			}
		case inference.ContentToolResult:
			if seenRegular {
				return requestMessage{}, parameterUnsupported("messages", fmt.Errorf("tool results must precede other user content"))
			}
			content, err := encodeToolResult(block.ToolResult)
			if err != nil {
				return requestMessage{}, err
			}
			mapped = requestContent{
				Type: "tool_result", ToolUseID: block.ToolResult.ToolCallID,
				Content: content, IsError: block.ToolResult.IsError,
			}
		default:
			return requestMessage{}, parameterUnsupported("messages", fmt.Errorf("Anthropic Messages does not support content type %q in a request", block.Type))
		}
		result.Content = append(result.Content, mapped)
	}
	return result, nil
}

func encodeToolResult(result *inference.ToolResultContent) (string, error) {
	if result.Text != nil {
		return *result.Text, nil
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, result.JSON); err != nil {
		return "", fmt.Errorf("encode Anthropic tool result: %w", err)
	}
	return compact.String(), nil
}

func encodeToolChoice(choice *inference.ToolChoice) *requestToolChoice {
	if choice == nil {
		return nil
	}
	switch choice.Mode {
	case inference.ToolChoiceRequired:
		return &requestToolChoice{Type: "any"}
	case inference.ToolChoiceSpecific:
		return &requestToolChoice{Type: "tool", Name: choice.Name}
	default:
		return &requestToolChoice{Type: string(choice.Mode)}
	}
}

func encodeOutputConfig(output *inference.StructuredOutput) (*requestOutputConfig, error) {
	if output == nil {
		return nil, nil
	}
	if output.Type == inference.StructuredJSONObject {
		return nil, parameterUnsupported("response_format", fmt.Errorf("Anthropic requires a JSON schema"))
	}
	return &requestOutputConfig{Format: requestOutputFormat{Type: "json_schema", Schema: output.Schema}}, nil
}
