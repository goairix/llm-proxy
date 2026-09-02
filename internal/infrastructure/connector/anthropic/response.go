package anthropic

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

type messageResponse struct {
	ID          string               `json:"id"`
	Type        string               `json:"type"`
	Role        string               `json:"role"`
	Model       string               `json:"model"`
	Content     []responseContent    `json:"content"`
	StopReason  string               `json:"stop_reason"`
	StopDetails *responseStopDetails `json:"stop_details"`
	Usage       *responseUsage       `json:"usage"`
}

type responseContent struct {
	Type  string          `json:"type"`
	Text  string          `json:"text"`
	ID    string          `json:"id"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`
}

type responseStopDetails struct {
	Explanation string `json:"explanation"`
	Category    string `json:"category"`
}

type responseUsage struct {
	InputTokens              *int64 `json:"input_tokens"`
	OutputTokens             *int64 `json:"output_tokens"`
	CacheReadInputTokens     int64  `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int64  `json:"cache_creation_input_tokens"`
}

func decodeResponse(
	reader io.Reader,
	invocation gatewayport.Invocation,
	idGenerator func() (uuid.UUID, error),
	clock func() time.Time,
) (inference.Response, error) {
	var source messageResponse
	decoder := json.NewDecoder(reader)
	if err := decoder.Decode(&source); err != nil {
		return inference.Response{}, invalidResponseError(err)
	}
	if err := ensureResponseEOF(decoder); err != nil {
		return inference.Response{}, invalidResponseError(err)
	}
	if source.Type != "message" || source.Role != "assistant" {
		return inference.Response{}, invalidResponseError(fmt.Errorf("Anthropic response envelope is invalid"))
	}
	if strings.TrimSpace(source.ID) == "" || strings.TrimSpace(source.Model) == "" {
		return inference.Response{}, invalidResponseError(fmt.Errorf("Anthropic response identity is missing"))
	}
	if source.Usage == nil {
		return inference.Response{}, invalidResponseError(fmt.Errorf("Anthropic response usage is required"))
	}
	if source.Usage.InputTokens == nil || source.Usage.OutputTokens == nil {
		return inference.Response{}, invalidResponseError(fmt.Errorf("Anthropic response usage is incomplete"))
	}

	content, err := decodeResponseContent(source.Content)
	if err != nil {
		return inference.Response{}, err
	}
	stopReason, err := mapStopReason(source.StopReason)
	if err != nil {
		return inference.Response{}, err
	}
	if stopReason == inference.StopContentFilter {
		content = nil
		if source.StopDetails != nil {
			if explanation := strings.TrimSpace(source.StopDetails.Explanation); explanation != "" {
				content = append(content, inference.ContentBlock{
					Type:    inference.ContentRefusal,
					Refusal: &inference.RefusalContent{Text: explanation},
				})
			}
		}
	}
	usage := inference.Usage{
		InputTokens:           *source.Usage.InputTokens,
		OutputTokens:          *source.Usage.OutputTokens,
		CacheReadInputTokens:  source.Usage.CacheReadInputTokens,
		CacheWriteInputTokens: source.Usage.CacheCreationInputTokens,
	}
	if err := usage.Validate(); err != nil {
		return inference.Response{}, invalidResponseError(err)
	}
	id, err := idGenerator()
	if err != nil {
		return inference.Response{}, invalidResponseError(fmt.Errorf("generate response id: %w", err))
	}
	response := inference.Response{
		ID: id, Model: invocation.Request.Model, Content: content,
		StopReason: stopReason, Usage: usage, CreatedAt: clock().UTC(),
	}
	if err := response.Validate(); err != nil {
		return inference.Response{}, invalidResponseError(err)
	}
	return response, nil
}

func decodeResponseContent(source []responseContent) ([]inference.ContentBlock, error) {
	content := make([]inference.ContentBlock, 0, len(source))
	for index, block := range source {
		switch block.Type {
		case "text":
			content = append(content, inference.ContentBlock{
				Type: inference.ContentText, Text: &inference.TextContent{Text: block.Text},
			})
		case "tool_use":
			content = append(content, inference.ContentBlock{
				Type:     inference.ContentToolCall,
				ToolCall: &inference.ToolCallContent{ID: block.ID, Name: block.Name, Arguments: block.Input},
			})
		default:
			return nil, invalidResponseError(fmt.Errorf("unsupported Anthropic content type %q at index %d", block.Type, index))
		}
	}
	return content, nil
}

func mapStopReason(value string) (inference.StopReason, error) {
	switch value {
	case "end_turn":
		return inference.StopEndTurn, nil
	case "max_tokens", "model_context_window_exceeded":
		return inference.StopMaxTokens, nil
	case "stop_sequence":
		return inference.StopSequence, nil
	case "tool_use":
		return inference.StopToolUse, nil
	case "refusal":
		return inference.StopContentFilter, nil
	case "pause_turn":
		return "", invalidResponseError(fmt.Errorf("unsupported Anthropic stop reason %q", value))
	default:
		return "", invalidResponseError(fmt.Errorf("unknown Anthropic stop reason %q", value))
	}
}

func ensureResponseEOF(decoder *json.Decoder) error {
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			return fmt.Errorf("response contains multiple JSON values")
		}
		return err
	}
	return nil
}
