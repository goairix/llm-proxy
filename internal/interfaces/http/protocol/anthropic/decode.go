package anthropic

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	gatewayservice "github.com/goairix/llm-proxy/internal/application/gateway/service"
	inference "github.com/goairix/llm-proxy/internal/domain/inference/model"
)

func Decode(reader io.Reader) (inference.Request, error) {
	if reader == nil {
		return inference.Request{}, invalidRequest("body", "请求正文不能为空", nil)
	}
	body, err := io.ReadAll(io.LimitReader(reader, maxRequestBodyBytes+1))
	if err != nil {
		return inference.Request{}, invalidRequest("body", "读取请求正文失败", err)
	}
	if len(body) > maxRequestBodyBytes {
		return inference.Request{}, invalidRequest("body", "请求正文超过 4 MiB 限制", nil)
	}
	var source requestDTO
	if err := json.Unmarshal(body, &source); err != nil {
		return inference.Request{}, invalidRequest("body", "请求正文不是有效 JSON", err)
	}
	request, err := mapRequest(source)
	if err != nil {
		return inference.Request{}, err
	}
	if err := request.Validate(); err != nil {
		return inference.Request{}, invalidRequest("", "请求参数无效", err)
	}
	return request, nil
}

func mapRequest(source requestDTO) (inference.Request, error) {
	if source.MaxTokens == nil {
		return inference.Request{}, invalidRequest("max_tokens", "max_tokens 为必填字段", nil)
	}
	if len(source.Messages) == 0 {
		return inference.Request{}, invalidRequest("messages", "messages 为必填字段", nil)
	}
	if source.Temperature != nil && (*source.Temperature < 0 || *source.Temperature > 1) {
		return inference.Request{}, invalidRequest("temperature", "temperature 必须在 0 到 1 之间", nil)
	}
	request := inference.Request{
		Model: source.Model, MaxTokens: inference.Some(*source.MaxTokens), Stream: source.Stream,
	}
	system, err := mapSystem(source.System)
	if err != nil {
		return inference.Request{}, invalidRequest("system", "system 格式无效", err)
	}
	if system != nil {
		request.Messages = append(request.Messages, *system)
	}
	for index, sourceMessage := range source.Messages {
		message, err := mapMessage(sourceMessage)
		if err != nil {
			return inference.Request{}, invalidRequest(fmt.Sprintf("messages[%d]", index), "消息格式无效", err)
		}
		request.Messages = append(request.Messages, message)
	}
	for index, sourceTool := range source.Tools {
		if len(bytes.TrimSpace(sourceTool.InputSchema)) == 0 || bytes.Equal(bytes.TrimSpace(sourceTool.InputSchema), []byte("null")) {
			return inference.Request{}, invalidRequest(fmt.Sprintf("tools[%d].input_schema", index), "工具输入 Schema 为必填字段", nil)
		}
		request.Tools = append(request.Tools, inference.Tool{
			Name: sourceTool.Name, Description: sourceTool.Description,
			InputSchema: sourceTool.InputSchema, Strict: sourceTool.Strict,
		})
	}
	choice, err := mapToolChoice(source.ToolChoice)
	if err != nil {
		return inference.Request{}, invalidRequest("tool_choice", "工具选择无效", err)
	}
	request.ToolChoice = choice
	structured, err := mapOutputConfig(source.OutputConfig)
	if err != nil {
		return inference.Request{}, invalidRequest("output_config.format", "结构化输出格式无效", err)
	}
	request.StructuredOutput = structured
	if source.Temperature != nil {
		request.Temperature = inference.Some(*source.Temperature)
	}
	if source.TopP != nil {
		request.TopP = inference.Some(*source.TopP)
	}
	if source.StopSequences != nil {
		request.Stop = inference.Some(*source.StopSequences)
	}
	return request, nil
}

func mapSystem(raw json.RawMessage) (*inference.Message, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil, nil
	}
	var text string
	if json.Unmarshal(trimmed, &text) == nil {
		return &inference.Message{Role: inference.RoleSystem, Content: []inference.ContentBlock{{
			Type: inference.ContentText, Text: &inference.TextContent{Text: text},
		}}}, nil
	}
	var blocks []contentBlockDTO
	if err := json.Unmarshal(trimmed, &blocks); err != nil || len(blocks) == 0 {
		return nil, fmt.Errorf("system must be a string or non-empty text block array")
	}
	content := make([]inference.ContentBlock, 0, len(blocks))
	for _, block := range blocks {
		if block.Type != "text" {
			return nil, fmt.Errorf("system only supports text blocks")
		}
		content = append(content, inference.ContentBlock{Type: inference.ContentText, Text: &inference.TextContent{Text: block.Text}})
	}
	return &inference.Message{Role: inference.RoleSystem, Content: content}, nil
}

func mapMessage(source messageDTO) (inference.Message, error) {
	role := inference.Role(source.Role)
	if role != inference.RoleUser && role != inference.RoleAssistant {
		return inference.Message{}, fmt.Errorf("unsupported role %q", source.Role)
	}
	trimmed := bytes.TrimSpace(source.Content)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return inference.Message{}, fmt.Errorf("content is required")
	}
	var text string
	if json.Unmarshal(trimmed, &text) == nil {
		return inference.Message{Role: role, Content: []inference.ContentBlock{{
			Type: inference.ContentText, Text: &inference.TextContent{Text: text},
		}}}, nil
	}
	var blocks []contentBlockDTO
	if err := json.Unmarshal(trimmed, &blocks); err != nil || len(blocks) == 0 {
		return inference.Message{}, fmt.Errorf("content must be a string or non-empty block array")
	}
	content := make([]inference.ContentBlock, 0, len(blocks))
	seenNonToolResult := false
	for index, block := range blocks {
		if role == inference.RoleUser {
			if block.Type == "tool_result" && seenNonToolResult {
				return inference.Message{}, fmt.Errorf("tool_result blocks must appear before other user content")
			}
			if block.Type != "tool_result" {
				seenNonToolResult = true
			}
		}
		mapped, err := mapContentBlock(block)
		if err != nil {
			return inference.Message{}, fmt.Errorf("content block %d: %w", index, err)
		}
		content = append(content, mapped)
	}
	return inference.Message{Role: role, Content: content}, nil
}

func mapContentBlock(block contentBlockDTO) (inference.ContentBlock, error) {
	switch block.Type {
	case "text":
		return inference.ContentBlock{Type: inference.ContentText, Text: &inference.TextContent{Text: block.Text}}, nil
	case "image":
		if block.Source == nil {
			return inference.ContentBlock{}, fmt.Errorf("image source is required")
		}
		source, err := mapImageSource(*block.Source)
		if err != nil {
			return inference.ContentBlock{}, err
		}
		return inference.ContentBlock{Type: inference.ContentImage, Image: &inference.ImageContent{Source: source}}, nil
	case "tool_use":
		return inference.ContentBlock{Type: inference.ContentToolCall, ToolCall: &inference.ToolCallContent{
			ID: block.ID, Name: block.Name, Arguments: block.Input,
		}}, nil
	case "tool_result":
		result, err := mapToolResult(block)
		if err != nil {
			return inference.ContentBlock{}, err
		}
		return inference.ContentBlock{Type: inference.ContentToolResult, ToolResult: result}, nil
	default:
		return inference.ContentBlock{}, fmt.Errorf("unsupported content block type %q", block.Type)
	}
}

func mapImageSource(source imageSourceDTO) (inference.ImageSource, error) {
	switch source.Type {
	case "base64":
		return inference.ImageSource{Type: inference.ImageBase64, MediaType: source.MediaType, Data: source.Data}, nil
	case "url":
		return inference.ImageSource{Type: inference.ImageURL, Data: source.URL}, nil
	default:
		return inference.ImageSource{}, fmt.Errorf("unsupported image source type %q", source.Type)
	}
}

func mapToolResult(block contentBlockDTO) (*inference.ToolResultContent, error) {
	trimmed := bytes.TrimSpace(block.Content)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil, fmt.Errorf("tool_result content is required")
	}
	var text string
	if json.Unmarshal(trimmed, &text) == nil {
		return &inference.ToolResultContent{ToolCallID: block.ToolUseID, Text: &text, IsError: block.IsError}, nil
	}
	var blocks []contentBlockDTO
	if err := json.Unmarshal(trimmed, &blocks); err != nil || len(blocks) == 0 {
		return nil, fmt.Errorf("tool_result content must be text or a non-empty text block array")
	}
	parts := make([]string, 0, len(blocks))
	for _, content := range blocks {
		if content.Type != "text" {
			return nil, fmt.Errorf("tool_result only supports text blocks in Phase 1B")
		}
		parts = append(parts, content.Text)
	}
	text = strings.Join(parts, "\n")
	return &inference.ToolResultContent{ToolCallID: block.ToolUseID, Text: &text, IsError: block.IsError}, nil
}

func mapToolChoice(source *toolChoiceDTO) (*inference.ToolChoice, error) {
	if source == nil {
		return nil, nil
	}
	switch source.Type {
	case "auto":
		return &inference.ToolChoice{Mode: inference.ToolChoiceAuto}, nil
	case "none":
		return &inference.ToolChoice{Mode: inference.ToolChoiceNone}, nil
	case "any":
		return &inference.ToolChoice{Mode: inference.ToolChoiceRequired}, nil
	case "tool":
		return &inference.ToolChoice{Mode: inference.ToolChoiceSpecific, Name: source.Name}, nil
	default:
		return nil, fmt.Errorf("unsupported tool choice type %q", source.Type)
	}
}

func mapOutputConfig(source *outputConfigDTO) (*inference.StructuredOutput, error) {
	if source == nil || source.Format == nil {
		return nil, nil
	}
	if source.Format.Type != "json_schema" {
		return nil, fmt.Errorf("unsupported output format %q", source.Format.Type)
	}
	return &inference.StructuredOutput{Type: inference.StructuredJSONSchema, Schema: source.Format.Schema}, nil
}

func invalidRequest(param, safeMessage string, cause error) error {
	return gatewayservice.NewError(gatewayservice.InvalidRequest, safeMessage, param, cause)
}
