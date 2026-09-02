package openai

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/google/uuid"

	gatewayport "github.com/goairix/llm-proxy/internal/application/gateway/port"
	inference "github.com/goairix/llm-proxy/internal/domain/inference/model"
)

type chatResponsePayload struct {
	Choices []chatChoice `json:"choices"`
	Usage   *chatUsage   `json:"usage"`
}

type chatChoice struct {
	Index        int                 `json:"index"`
	Message      chatResponseMessage `json:"message"`
	Delta        chatResponseDelta   `json:"delta"`
	FinishReason *string             `json:"finish_reason"`
}

type chatResponseMessage struct {
	Role      string         `json:"role"`
	Content   *string        `json:"content"`
	Refusal   *string        `json:"refusal"`
	ToolCalls []chatToolCall `json:"tool_calls"`
}

type chatResponseDelta struct {
	Role      string         `json:"role"`
	Content   *string        `json:"content"`
	Refusal   *string        `json:"refusal"`
	ToolCalls []chatToolCall `json:"tool_calls"`
}

type chatUsage struct {
	PromptTokens        int64                  `json:"prompt_tokens"`
	CompletionTokens    int64                  `json:"completion_tokens"`
	TotalTokens         int64                  `json:"total_tokens"`
	PromptTokensDetails chatPromptTokenDetails `json:"prompt_tokens_details"`
}

type chatPromptTokenDetails struct {
	CachedTokens int64 `json:"cached_tokens"`
}

func decodeChatResponse(reader io.Reader, invocation gatewayport.Invocation, idGenerator func() (uuid.UUID, error), clock func() time.Time) (inference.Response, error) {
	var payload chatResponsePayload
	decoder := json.NewDecoder(reader)
	if err := decoder.Decode(&payload); err != nil {
		return inference.Response{}, invalidChatResponse(err)
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return inference.Response{}, invalidChatResponse(err)
	}
	if len(payload.Choices) != 1 || payload.Choices[0].Index != 0 || payload.Usage == nil {
		return inference.Response{}, invalidChatResponse(fmt.Errorf("Chat Completions requires choice index 0 and usage"))
	}
	choice := payload.Choices[0]
	reason, err := decodeChatFinishReason(choice.FinishReason)
	if err != nil {
		return inference.Response{}, err
	}
	content, err := decodeChatMessage(choice.Message)
	if err != nil {
		return inference.Response{}, err
	}
	usage, err := decodeChatUsage(*payload.Usage)
	if err != nil {
		return inference.Response{}, err
	}
	id, err := idGenerator()
	if err != nil {
		return inference.Response{}, invalidChatResponse(fmt.Errorf("generate response id: %w", err))
	}
	response := inference.Response{ID: id, Model: invocation.Request.Model, Content: content, StopReason: reason, Usage: usage, CreatedAt: clock().UTC()}
	if err := response.Validate(); err != nil {
		return inference.Response{}, invalidChatResponse(err)
	}
	return response, nil
}

func decodeChatMessage(message chatResponseMessage) ([]inference.ContentBlock, error) {
	if message.Role != "assistant" {
		return nil, invalidChatResponse(fmt.Errorf("choice message role must be assistant"))
	}
	content := make([]inference.ContentBlock, 0, 2+len(message.ToolCalls))
	if message.Content != nil && *message.Content != "" {
		content = append(content, inference.ContentBlock{Type: inference.ContentText, Text: &inference.TextContent{Text: *message.Content}})
	}
	if message.Refusal != nil && strings.TrimSpace(*message.Refusal) != "" {
		content = append(content, inference.ContentBlock{Type: inference.ContentRefusal, Refusal: &inference.RefusalContent{Text: *message.Refusal}})
	}
	for _, call := range message.ToolCalls {
		if call.Type != "function" {
			return nil, invalidChatResponse(fmt.Errorf("unsupported tool call type %q", call.Type))
		}
		content = append(content, inference.ContentBlock{Type: inference.ContentToolCall, ToolCall: &inference.ToolCallContent{
			ID: call.ID, Name: call.Function.Name, Arguments: json.RawMessage(call.Function.Arguments),
		}})
	}
	return content, nil
}

func decodeChatFinishReason(value *string) (inference.StopReason, error) {
	if value == nil {
		return "", invalidChatResponse(fmt.Errorf("finish_reason is required"))
	}
	switch *value {
	case "stop":
		return inference.StopEndTurn, nil
	case "length":
		return inference.StopMaxTokens, nil
	case "tool_calls":
		return inference.StopToolUse, nil
	case "content_filter":
		return inference.StopContentFilter, nil
	default:
		return "", invalidChatResponse(fmt.Errorf("unsupported finish_reason %q", *value))
	}
}

func decodeChatUsage(value chatUsage) (inference.Usage, error) {
	usage := inference.Usage{InputTokens: value.PromptTokens, OutputTokens: value.CompletionTokens, CacheReadInputTokens: value.PromptTokensDetails.CachedTokens}
	if err := usage.Validate(); err != nil || value.TotalTokens < 0 || value.TotalTokens != usage.TotalTokens() {
		if err == nil {
			err = fmt.Errorf("usage total_tokens does not equal prompt_tokens plus completion_tokens")
		}
		return inference.Usage{}, invalidChatResponse(err)
	}
	return usage, nil
}

func invalidChatResponse(cause error) error {
	return connectorError(gatewayport.UpstreamInvalidResponse, cause)
}

func ensureJSONEOF(decoder *json.Decoder) error {
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			return fmt.Errorf("response contains multiple JSON values")
		}
		return err
	}
	return nil
}
