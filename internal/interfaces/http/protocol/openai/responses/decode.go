package responses

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	gatewayservice "github.com/goairix/llm-proxy/internal/application/gateway/service"
	inference "github.com/goairix/llm-proxy/internal/domain/inference/model"
)

type DecodedRequest struct{ Request inference.Request }

func Decode(reader io.Reader) (DecodedRequest, error) {
	if reader == nil {
		return DecodedRequest{}, invalid("body", fmt.Errorf("body is required"))
	}
	body, err := io.ReadAll(io.LimitReader(reader, maxRequestBodyBytes+1))
	if err != nil || len(body) > maxRequestBodyBytes {
		return DecodedRequest{}, invalid("body", err)
	}
	var source requestDTO
	if err := decodeStrict(body, &source); err != nil {
		return DecodedRequest{}, invalid("body", err)
	}
	for param, raw := range map[string]json.RawMessage{"previous_response_id": source.PreviousResponseID, "conversation": source.Conversation, "background": source.Background} {
		if len(raw) > 0 {
			return DecodedRequest{}, invalid(param, fmt.Errorf("field is not supported"))
		}
	}
	if source.Store != nil && *source.Store {
		return DecodedRequest{}, invalid("store", fmt.Errorf("stored responses are not supported"))
	}
	request := inference.Request{Model: source.Model, Stream: source.Stream}
	if source.Instructions != "" {
		request.Messages = append(request.Messages, inference.Message{Role: inference.RoleDeveloper, Content: []inference.ContentBlock{textBlock(source.Instructions)}})
	}
	input, err := decodeInput(source.Input)
	if err != nil {
		return DecodedRequest{}, err
	}
	request.Messages = append(request.Messages, input...)
	for index, raw := range source.Tools {
		var tool toolDTO
		if err := decodeStrict(raw, &tool); err != nil {
			return DecodedRequest{}, invalid(fmt.Sprintf("tools[%d]", index), err)
		}
		if tool.Type != "function" {
			return DecodedRequest{}, invalid(fmt.Sprintf("tools[%d].type", index), fmt.Errorf("only function tools are supported"))
		}
		request.Tools = append(request.Tools, inference.Tool{Name: tool.Name, Description: tool.Description, InputSchema: tool.Parameters, Strict: tool.Strict})
	}
	request.ToolChoice, err = decodeToolChoice(source.ToolChoice)
	if err != nil {
		return DecodedRequest{}, err
	}
	request.StructuredOutput, err = decodeText(source.Text)
	if err != nil {
		return DecodedRequest{}, err
	}
	if source.Temperature != nil {
		request.Temperature = inference.Some(*source.Temperature)
	}
	if source.TopP != nil {
		request.TopP = inference.Some(*source.TopP)
	}
	if source.MaxOutputTokens != nil {
		if *source.MaxOutputTokens <= 0 {
			return DecodedRequest{}, invalid("max_output_tokens", fmt.Errorf("must be positive"))
		}
		request.MaxTokens = inference.Some(*source.MaxOutputTokens)
	}
	if err := request.Validate(); err != nil {
		return DecodedRequest{}, invalid("", err)
	}
	return DecodedRequest{Request: request}, nil
}

func decodeInput(raw json.RawMessage) ([]inference.Message, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil, invalid("input", fmt.Errorf("input is required"))
	}
	var text string
	if json.Unmarshal(trimmed, &text) == nil {
		if strings.TrimSpace(text) == "" {
			return nil, invalid("input", fmt.Errorf("input must not be empty"))
		}
		return []inference.Message{{Role: inference.RoleUser, Content: []inference.ContentBlock{textBlock(text)}}}, nil
	}
	var items []json.RawMessage
	if err := json.Unmarshal(trimmed, &items); err != nil || len(items) == 0 {
		return nil, invalid("input", fmt.Errorf("input must be a string or item array"))
	}
	var messages []inference.Message
	for index, item := range items {
		var header inputHeader
		if err := json.Unmarshal(item, &header); err != nil {
			return nil, invalid(fmt.Sprintf("input[%d]", index), err)
		}
		switch header.Type {
		case "", "message":
			message, err := decodeInputMessage(item, index)
			if err != nil {
				return nil, err
			}
			messages = append(messages, message)
		case "function_call":
			var call functionCallDTO
			if err := decodeStrict(item, &call); err != nil {
				return nil, invalid(fmt.Sprintf("input[%d]", index), err)
			}
			messages = append(messages, inference.Message{Role: inference.RoleAssistant, Content: []inference.ContentBlock{{Type: inference.ContentToolCall, ToolCall: &inference.ToolCallContent{ID: call.CallID, Name: call.Name, Arguments: json.RawMessage(call.Arguments)}}}})
		case "function_call_output":
			var output functionOutputDTO
			if err := decodeStrict(item, &output); err != nil {
				return nil, invalid(fmt.Sprintf("input[%d]", index), err)
			}
			result := &inference.ToolResultContent{ToolCallID: output.CallID}
			var value string
			if json.Unmarshal(output.Output, &value) == nil {
				result.Text = &value
			} else {
				result.JSON = append(json.RawMessage(nil), output.Output...)
			}
			messages = append(messages, inference.Message{Role: inference.RoleUser, Content: []inference.ContentBlock{{Type: inference.ContentToolResult, ToolResult: result}}})
		default:
			return nil, invalid(fmt.Sprintf("input[%d].type", index), fmt.Errorf("unsupported input item type %q", header.Type))
		}
	}
	return messages, nil
}

func decodeInputMessage(raw json.RawMessage, index int) (inference.Message, error) {
	var source inputMessageDTO
	if err := decodeStrict(raw, &source); err != nil {
		return inference.Message{}, invalid(fmt.Sprintf("input[%d]", index), err)
	}
	role, err := decodeRole(source.Role)
	if err != nil {
		return inference.Message{}, invalid(fmt.Sprintf("input[%d].role", index), err)
	}
	var text string
	if json.Unmarshal(source.Content, &text) == nil {
		return inference.Message{Role: role, Content: []inference.ContentBlock{textBlock(text)}}, nil
	}
	var parts []json.RawMessage
	if err := json.Unmarshal(source.Content, &parts); err != nil {
		return inference.Message{}, invalid(fmt.Sprintf("input[%d].content", index), err)
	}
	message := inference.Message{Role: role}
	for partIndex, rawPart := range parts {
		var header inputHeader
		_ = json.Unmarshal(rawPart, &header)
		param := fmt.Sprintf("input[%d].content[%d]", index, partIndex)
		if header.Type != "input_text" && header.Type != "output_text" && header.Type != "input_image" {
			return inference.Message{}, invalid(param+".type", fmt.Errorf("unsupported content type %q", header.Type))
		}
		var part inputContentDTO
		if err := decodeStrict(rawPart, &part); err != nil {
			return inference.Message{}, invalid(param, err)
		}
		if part.Type == "input_image" {
			source, err := decodeImage(part.ImageURL, part.Detail)
			if err != nil {
				return inference.Message{}, invalid(param+".image_url", err)
			}
			message.Content = append(message.Content, inference.ContentBlock{Type: inference.ContentImage, Image: &inference.ImageContent{Source: source}})
		} else {
			if role == inference.RoleAssistant && part.Type != "output_text" || role != inference.RoleAssistant && part.Type == "output_text" {
				return inference.Message{}, invalid(param+".type", fmt.Errorf("content type does not match message role"))
			}
			message.Content = append(message.Content, textBlock(part.Text))
		}
	}
	return message, nil
}

func decodeImage(value, detail string) (inference.ImageSource, error) {
	if strings.HasPrefix(value, "data:") {
		header, data, ok := strings.Cut(strings.TrimPrefix(value, "data:"), ",")
		media, encoding, ok2 := strings.Cut(header, ";")
		if !ok || !ok2 || encoding != "base64" {
			return inference.ImageSource{}, fmt.Errorf("invalid data URL")
		}
		return inference.ImageSource{Type: inference.ImageBase64, MediaType: media, Data: data, Detail: detail}, nil
	}
	return inference.ImageSource{Type: inference.ImageURL, Data: value, Detail: detail}, nil
}

func decodeRole(value string) (inference.Role, error) {
	switch value {
	case "system":
		return inference.RoleSystem, nil
	case "developer":
		return inference.RoleDeveloper, nil
	case "user":
		return inference.RoleUser, nil
	case "assistant":
		return inference.RoleAssistant, nil
	default:
		return "", fmt.Errorf("unsupported role %q", value)
	}
}
func decodeToolChoice(raw json.RawMessage) (*inference.ToolChoice, error) {
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
		}
		return nil, invalid("tool_choice", fmt.Errorf("unsupported mode"))
	}
	var choice toolChoiceDTO
	if err := decodeStrict(trimmed, &choice); err != nil || choice.Type != "function" {
		return nil, invalid("tool_choice", err)
	}
	return &inference.ToolChoice{Mode: inference.ToolChoiceSpecific, Name: choice.Name}, nil
}
func decodeText(source *textDTO) (*inference.StructuredOutput, error) {
	if source == nil || source.Format.Type == "" || source.Format.Type == "text" {
		return nil, nil
	}
	switch source.Format.Type {
	case "json_object":
		return &inference.StructuredOutput{Type: inference.StructuredJSONObject}, nil
	case "json_schema":
		if strings.TrimSpace(source.Format.Name) == "" {
			return nil, invalid("text.format.name", fmt.Errorf("name is required"))
		}
		return &inference.StructuredOutput{Type: inference.StructuredJSONSchema, Name: source.Format.Name, Description: source.Format.Description, Strict: source.Format.Strict, Schema: source.Format.Schema}, nil
	default:
		return nil, invalid("text.format.type", fmt.Errorf("unsupported format"))
	}
}
func textBlock(value string) inference.ContentBlock {
	return inference.ContentBlock{Type: inference.ContentText, Text: &inference.TextContent{Text: value}}
}
func decodeStrict(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return fmt.Errorf("multiple JSON values")
	}
	return nil
}
func invalid(param string, cause error) error {
	return gatewayservice.NewError(gatewayservice.InvalidRequest, "请求参数无效", param, cause)
}
