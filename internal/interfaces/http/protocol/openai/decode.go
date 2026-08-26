package openai

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	gatewayservice "github.com/goairix/llm-proxy/internal/application/gateway/service"
	inference "github.com/goairix/llm-proxy/internal/domain/inference/model"
)

type DecodedRequest struct {
	Request      inference.Request
	IncludeUsage bool
}

func Decode(reader io.Reader) (inference.Request, error) {
	decoded, err := DecodeWithOptions(reader)
	return decoded.Request, err
}

func DecodeWithOptions(reader io.Reader) (DecodedRequest, error) {
	if reader == nil {
		return DecodedRequest{}, invalidRequest("body", "请求正文不能为空", nil)
	}
	body, err := io.ReadAll(io.LimitReader(reader, maxRequestBodyBytes+1))
	if err != nil {
		return DecodedRequest{}, invalidRequest("body", "读取请求正文失败", err)
	}
	if len(body) > maxRequestBodyBytes {
		return DecodedRequest{}, invalidRequest("body", "请求正文超过 4 MiB 限制", nil)
	}
	var source requestDTO
	if err := json.Unmarshal(body, &source); err != nil {
		return DecodedRequest{}, invalidRequest("body", "请求正文不是有效 JSON", err)
	}
	request, err := mapRequest(source)
	if err != nil {
		return DecodedRequest{}, err
	}
	if err := request.Validate(); err != nil {
		return DecodedRequest{}, invalidRequest("", "请求参数无效", err)
	}
	return DecodedRequest{
		Request: request, IncludeUsage: source.StreamOptions != nil && source.StreamOptions.IncludeUsage,
	}, nil
}

func mapRequest(source requestDTO) (inference.Request, error) {
	request := inference.Request{Model: source.Model, Stream: source.Stream}
	for index, sourceMessage := range source.Messages {
		message, err := mapMessage(sourceMessage)
		if err != nil {
			return inference.Request{}, invalidRequest(fmt.Sprintf("messages[%d]", index), "消息格式无效", err)
		}
		request.Messages = append(request.Messages, message)
	}
	for index, sourceTool := range source.Tools {
		tool, err := mapTool(sourceTool)
		if err != nil {
			return inference.Request{}, invalidRequest(fmt.Sprintf("tools[%d]", index), "工具定义无效", err)
		}
		request.Tools = append(request.Tools, tool)
	}
	choice, err := mapToolChoice(source.ToolChoice)
	if err != nil {
		return inference.Request{}, invalidRequest("tool_choice", "工具选择无效", err)
	}
	request.ToolChoice = choice
	structured, err := mapResponseFormat(source.ResponseFormat)
	if err != nil {
		return inference.Request{}, invalidRequest("response_format", "响应格式无效", err)
	}
	request.StructuredOutput = structured
	if source.Temperature != nil {
		request.Temperature = inference.Some(*source.Temperature)
	}
	if source.TopP != nil {
		request.TopP = inference.Some(*source.TopP)
	}
	if source.MaxTokens != nil && source.MaxCompletionTokens != nil {
		return inference.Request{}, invalidRequest("max_tokens", "max_tokens 与 max_completion_tokens 不能同时设置", nil)
	}
	if source.MaxTokens != nil {
		request.MaxTokens = inference.Some(*source.MaxTokens)
	} else if source.MaxCompletionTokens != nil {
		request.MaxTokens = inference.Some(*source.MaxCompletionTokens)
	}
	stop, err := mapStop(source.Stop)
	if err != nil {
		return inference.Request{}, invalidRequest("stop", "停止序列无效", err)
	}
	request.Stop = stop
	return request, nil
}

func mapMessage(source messageDTO) (inference.Message, error) {
	if source.Role == "tool" {
		text, err := toolResultText(source.Content)
		if err != nil {
			return inference.Message{}, err
		}
		return inference.Message{Role: inference.RoleUser, Content: []inference.ContentBlock{{
			Type:       inference.ContentToolResult,
			ToolResult: &inference.ToolResultContent{ToolCallID: source.ToolCallID, Text: &text},
		}}}, nil
	}
	role, err := mapRole(source.Role)
	if err != nil {
		return inference.Message{}, err
	}
	content, err := mapMessageContent(source.Content)
	if err != nil {
		return inference.Message{}, err
	}
	if len(source.ToolCalls) > 0 && role != inference.RoleAssistant {
		return inference.Message{}, fmt.Errorf("tool_calls require assistant role")
	}
	for _, call := range source.ToolCalls {
		if call.Type != "function" {
			return inference.Message{}, fmt.Errorf("unsupported tool call type %q", call.Type)
		}
		content = append(content, inference.ContentBlock{Type: inference.ContentToolCall, ToolCall: &inference.ToolCallContent{
			ID: call.ID, Name: call.Function.Name, Arguments: json.RawMessage(call.Function.Arguments),
		}})
	}
	return inference.Message{Role: role, Content: content}, nil
}

func mapRole(role string) (inference.Role, error) {
	switch role {
	case "system":
		return inference.RoleSystem, nil
	case "developer":
		return inference.RoleDeveloper, nil
	case "user":
		return inference.RoleUser, nil
	case "assistant":
		return inference.RoleAssistant, nil
	default:
		return "", fmt.Errorf("unsupported role %q", role)
	}
}

func mapMessageContent(raw json.RawMessage) ([]inference.ContentBlock, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil, nil
	}
	var text string
	if err := json.Unmarshal(trimmed, &text); err == nil {
		return []inference.ContentBlock{{Type: inference.ContentText, Text: &inference.TextContent{Text: text}}}, nil
	}
	var parts []contentPartDTO
	if err := json.Unmarshal(trimmed, &parts); err != nil {
		return nil, fmt.Errorf("content must be a string or content-part array")
	}
	content := make([]inference.ContentBlock, 0, len(parts))
	for index, part := range parts {
		switch part.Type {
		case "text":
			content = append(content, inference.ContentBlock{Type: inference.ContentText, Text: &inference.TextContent{Text: part.Text}})
		case "image_url":
			if part.ImageURL == nil {
				return nil, fmt.Errorf("content part %d image_url is required", index)
			}
			source, err := mapImageSource(*part.ImageURL)
			if err != nil {
				return nil, fmt.Errorf("content part %d: %w", index, err)
			}
			content = append(content, inference.ContentBlock{Type: inference.ContentImage, Image: &inference.ImageContent{Source: source}})
		default:
			return nil, fmt.Errorf("unsupported content part type %q", part.Type)
		}
	}
	return content, nil
}

func mapImageSource(image imageURLDTO) (inference.ImageSource, error) {
	if strings.HasPrefix(image.URL, "data:") {
		header, data, found := strings.Cut(strings.TrimPrefix(image.URL, "data:"), ",")
		mediaType, encoding, foundHeader := strings.Cut(header, ";")
		if !found || !foundHeader || encoding != "base64" {
			return inference.ImageSource{}, fmt.Errorf("image data URL must be base64 encoded")
		}
		return inference.ImageSource{Type: inference.ImageBase64, MediaType: mediaType, Data: data, Detail: image.Detail}, nil
	}
	return inference.ImageSource{Type: inference.ImageURL, Data: image.URL, Detail: image.Detail}, nil
}

func toolResultText(raw json.RawMessage) (string, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return "", fmt.Errorf("tool result content is required")
	}
	content, err := mapMessageContent(raw)
	if err != nil {
		return "", err
	}
	parts := make([]string, 0, len(content))
	for _, block := range content {
		if block.Text == nil {
			return "", fmt.Errorf("tool result only supports text content")
		}
		parts = append(parts, block.Text.Text)
	}
	return strings.Join(parts, "\n"), nil
}

func mapTool(source toolDTO) (inference.Tool, error) {
	if source.Type != "function" {
		return inference.Tool{}, fmt.Errorf("unsupported tool type %q", source.Type)
	}
	parameters := source.Function.Parameters
	if len(bytes.TrimSpace(parameters)) == 0 || bytes.Equal(bytes.TrimSpace(parameters), []byte("null")) {
		parameters = json.RawMessage(`{}`)
	}
	return inference.Tool{
		Name: source.Function.Name, Description: source.Function.Description,
		InputSchema: parameters, Strict: source.Function.Strict,
	}, nil
}

func mapToolChoice(raw json.RawMessage) (*inference.ToolChoice, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil, nil
	}
	var mode string
	if json.Unmarshal(trimmed, &mode) == nil {
		switch mode {
		case "auto":
			return &inference.ToolChoice{Mode: inference.ToolChoiceAuto}, nil
		case "none":
			return &inference.ToolChoice{Mode: inference.ToolChoiceNone}, nil
		case "required":
			return &inference.ToolChoice{Mode: inference.ToolChoiceRequired}, nil
		default:
			return nil, fmt.Errorf("unsupported tool choice %q", mode)
		}
	}
	var choice toolChoiceDTO
	if err := json.Unmarshal(trimmed, &choice); err != nil || choice.Type != "function" {
		return nil, fmt.Errorf("specific tool choice must use function type")
	}
	return &inference.ToolChoice{Mode: inference.ToolChoiceSpecific, Name: choice.Function.Name}, nil
}

func mapResponseFormat(source *responseFormatDTO) (*inference.StructuredOutput, error) {
	if source == nil || source.Type == "text" || source.Type == "" {
		return nil, nil
	}
	switch source.Type {
	case "json_object":
		return &inference.StructuredOutput{Type: inference.StructuredJSONObject}, nil
	case "json_schema":
		if source.JSONSchema == nil {
			return nil, fmt.Errorf("json_schema definition is required")
		}
		if strings.TrimSpace(source.JSONSchema.Name) == "" {
			return nil, fmt.Errorf("json_schema name is required")
		}
		return &inference.StructuredOutput{
			Type: inference.StructuredJSONSchema, Name: source.JSONSchema.Name,
			Description: source.JSONSchema.Description, Strict: source.JSONSchema.Strict,
			Schema: source.JSONSchema.Schema,
		}, nil
	default:
		return nil, fmt.Errorf("unsupported response format %q", source.Type)
	}
}

func mapStop(raw json.RawMessage) (inference.Optional[[]string], error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return inference.Optional[[]string]{}, nil
	}
	var single string
	if json.Unmarshal(trimmed, &single) == nil {
		return inference.Some([]string{single}), nil
	}
	var multiple []string
	if err := json.Unmarshal(trimmed, &multiple); err != nil {
		return inference.Optional[[]string]{}, fmt.Errorf("stop must be a string or string array")
	}
	return inference.Some(multiple), nil
}

func invalidRequest(param, safeMessage string, cause error) error {
	return gatewayservice.NewError(gatewayservice.InvalidRequest, safeMessage, param, cause)
}
