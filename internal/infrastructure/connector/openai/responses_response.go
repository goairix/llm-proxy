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

type responsesPayload struct {
	Status            string                     `json:"status"`
	IncompleteDetails *responsesIncompleteDetail `json:"incomplete_details"`
	Output            []responsesOutputItem      `json:"output"`
	Usage             *responsesUsage            `json:"usage"`
	Error             *responsesError            `json:"error"`
}

type responsesIncompleteDetail struct {
	Reason string `json:"reason"`
}

type responsesError struct {
	Message string `json:"message"`
}

type responsesOutputItem struct {
	Type      string                   `json:"type"`
	Role      string                   `json:"role"`
	Content   []responsesOutputContent `json:"content"`
	CallID    string                   `json:"call_id"`
	Name      string                   `json:"name"`
	Arguments string                   `json:"arguments"`
}

type responsesOutputContent struct {
	Type    string `json:"type"`
	Text    string `json:"text"`
	Refusal string `json:"refusal"`
}

type responsesUsage struct {
	InputTokens        int64                       `json:"input_tokens"`
	OutputTokens       int64                       `json:"output_tokens"`
	TotalTokens        int64                       `json:"total_tokens"`
	InputTokenDetails  responsesInputTokenDetails  `json:"input_tokens_details"`
	OutputTokenDetails responsesOutputTokenDetails `json:"output_tokens_details"`
}

type responsesInputTokenDetails struct {
	CachedTokens int64 `json:"cached_tokens"`
}

type responsesOutputTokenDetails struct {
	ReasoningTokens int64 `json:"reasoning_tokens"`
}

func decodeResponsesResponse(reader io.Reader, invocation gatewayport.Invocation, idGenerator func() (uuid.UUID, error), clock func() time.Time) (inference.Response, error) {
	var payload responsesPayload
	decoder := json.NewDecoder(reader)
	if err := decoder.Decode(&payload); err != nil {
		return inference.Response{}, invalidResponsesResponse(err)
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return inference.Response{}, invalidResponsesResponse(err)
	}
	content, hasTool, err := decodeResponsesOutput(payload.Output)
	if err != nil {
		return inference.Response{}, err
	}
	reason, err := decodeResponsesStopReason(payload, hasTool)
	if err != nil {
		return inference.Response{}, err
	}
	if payload.Usage == nil {
		return inference.Response{}, invalidResponsesResponse(fmt.Errorf("Responses usage is required"))
	}
	usage, err := decodeResponsesUsage(*payload.Usage)
	if err != nil {
		return inference.Response{}, err
	}
	id, err := idGenerator()
	if err != nil {
		return inference.Response{}, invalidResponsesResponse(fmt.Errorf("generate response id: %w", err))
	}
	response := inference.Response{ID: id, Model: invocation.Request.Model, Content: content, StopReason: reason, Usage: usage, CreatedAt: clock().UTC()}
	if err := response.Validate(); err != nil {
		return inference.Response{}, invalidResponsesResponse(err)
	}
	return response, nil
}

func decodeResponsesOutput(items []responsesOutputItem) ([]inference.ContentBlock, bool, error) {
	var content []inference.ContentBlock
	hasTool := false
	for _, item := range items {
		switch item.Type {
		case "message":
			if item.Role != "assistant" {
				return nil, false, invalidResponsesResponse(fmt.Errorf("output message role must be assistant"))
			}
			for _, part := range item.Content {
				switch part.Type {
				case "output_text":
					if part.Text != "" {
						content = append(content, inference.ContentBlock{Type: inference.ContentText, Text: &inference.TextContent{Text: part.Text}})
					}
				case "refusal":
					if strings.TrimSpace(part.Refusal) != "" {
						content = append(content, inference.ContentBlock{Type: inference.ContentRefusal, Refusal: &inference.RefusalContent{Text: part.Refusal}})
					}
				default:
					return nil, false, invalidResponsesResponse(fmt.Errorf("unsupported output content type %q", part.Type))
				}
			}
		case "function_call":
			hasTool = true
			content = append(content, inference.ContentBlock{Type: inference.ContentToolCall, ToolCall: &inference.ToolCallContent{ID: item.CallID, Name: item.Name, Arguments: json.RawMessage(item.Arguments)}})
		case "reasoning":
			// Reasoning is provider metadata and is intentionally not exposed.
		default:
			return nil, false, invalidResponsesResponse(fmt.Errorf("unsupported output item type %q", item.Type))
		}
	}
	return content, hasTool, nil
}

func decodeResponsesStopReason(payload responsesPayload, hasTool bool) (inference.StopReason, error) {
	switch payload.Status {
	case "completed":
		if hasTool {
			return inference.StopToolUse, nil
		}
		return inference.StopEndTurn, nil
	case "incomplete":
		if payload.IncompleteDetails == nil {
			return "", invalidResponsesResponse(fmt.Errorf("incomplete_details is required"))
		}
		switch payload.IncompleteDetails.Reason {
		case "max_output_tokens":
			return inference.StopMaxTokens, nil
		case "content_filter":
			return inference.StopContentFilter, nil
		default:
			return "", invalidResponsesResponse(fmt.Errorf("unsupported incomplete reason %q", payload.IncompleteDetails.Reason))
		}
	default:
		return "", invalidResponsesResponse(fmt.Errorf("unsupported response status %q", payload.Status))
	}
}

func decodeResponsesUsage(value responsesUsage) (inference.Usage, error) {
	usage := inference.Usage{InputTokens: value.InputTokens, OutputTokens: value.OutputTokens, CacheReadInputTokens: value.InputTokenDetails.CachedTokens}
	if err := usage.Validate(); err != nil || value.OutputTokenDetails.ReasoningTokens < 0 || value.TotalTokens < 0 || value.TotalTokens != usage.TotalTokens() {
		if err == nil {
			err = fmt.Errorf("Responses usage totals or details are invalid")
		}
		return inference.Usage{}, invalidResponsesResponse(err)
	}
	return usage, nil
}

func invalidResponsesResponse(cause error) error {
	return connectorError(gatewayport.UpstreamInvalidResponse, cause)
}
