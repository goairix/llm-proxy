package anthropic

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	inference "github.com/goairix/llm-proxy/internal/domain/inference/model"
	inferenceport "github.com/goairix/llm-proxy/internal/domain/inference/port"
)

type FlushWriter interface {
	io.Writer
	Flush()
}

func EncodeResponse(writer io.Writer, response inference.Response) error {
	if writer == nil {
		return fmt.Errorf("Anthropic response writer is nil")
	}
	if err := response.Validate(); err != nil {
		return fmt.Errorf("validate Anthropic response: %w", err)
	}
	content := make([]responseContentBlock, 0, len(response.Content))
	for _, block := range response.Content {
		switch block.Type {
		case inference.ContentText:
			content = append(content, responseContentBlock{Type: "text", Text: block.Text.Text})
		case inference.ContentRefusal:
			content = append(content, responseContentBlock{Type: "text", Text: block.Refusal.Text})
		case inference.ContentToolCall:
			var object map[string]json.RawMessage
			if err := json.Unmarshal(block.ToolCall.Arguments, &object); err != nil || object == nil {
				return fmt.Errorf("Anthropic tool arguments must be a JSON object")
			}
			content = append(content, responseContentBlock{
				Type: "tool_use", ID: block.ToolCall.ID, Name: block.ToolCall.Name, Input: block.ToolCall.Arguments,
			})
		}
	}
	payload := messageResponse{
		ID: "msg_" + response.ID.String(), Type: "message", Role: "assistant", Content: content,
		Model: response.Model, StopReason: anthropicStopReason(response.StopReason), Usage: mapUsage(response.Usage),
	}
	return json.NewEncoder(writer).Encode(payload)
}

func EncodeStream(ctx context.Context, writer FlushWriter, stream inferenceport.Stream) error {
	if writer == nil {
		return fmt.Errorf("Anthropic stream writer is nil")
	}
	if stream == nil {
		return fmt.Errorf("Anthropic stream is nil")
	}
	validator := inference.NewSequenceValidator()
	usage := inference.Usage{}
	for {
		event, err := stream.Recv(ctx)
		if err != nil {
			if errors.Is(err, io.EOF) {
				return validator.ValidateEOF()
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return writeStreamGatewayError(writer, err)
		}
		if err := validator.Push(event); err != nil {
			return fmt.Errorf("validate Anthropic stream event: %w", err)
		}
		switch event.Type {
		case inference.EventResponseStart:
			payload := messageStartData{Type: "message_start", Message: messageStartMessage{
				ID: "msg_" + event.ResponseStart.ID.String(), Type: "message", Role: "assistant",
				Content: []responseContentBlock{}, Model: event.ResponseStart.Model, Usage: usageDTO{},
			}}
			if err := writeSSEEvent(writer, "message_start", payload); err != nil {
				return err
			}
		case inference.EventContentBlockStart:
			if err := writeSSEEvent(writer, "content_block_start", contentBlockStartData{
				Type: "content_block_start", Index: event.ContentBlockStart.Index,
				ContentBlock: textBlockStart{Type: "text", Text: ""},
			}); err != nil {
				return err
			}
		case inference.EventToolCallStart:
			if err := writeSSEEvent(writer, "content_block_start", contentBlockStartData{
				Type: "content_block_start", Index: event.ToolCallStart.Index,
				ContentBlock: toolBlockStart{
					Type: "tool_use", ID: event.ToolCallStart.ID, Name: event.ToolCallStart.Name, Input: json.RawMessage(`{}`),
				},
			}); err != nil {
				return err
			}
		case inference.EventTextDelta:
			if err := writeSSEEvent(writer, "content_block_delta", contentBlockDeltaData{
				Type: "content_block_delta", Index: event.TextDelta.Index,
				Delta: textDelta{Type: "text_delta", Text: event.TextDelta.Text},
			}); err != nil {
				return err
			}
		case inference.EventRefusalDelta:
			if err := writeSSEEvent(writer, "content_block_delta", contentBlockDeltaData{
				Type: "content_block_delta", Index: event.RefusalDelta.Index,
				Delta: textDelta{Type: "text_delta", Text: event.RefusalDelta.Text},
			}); err != nil {
				return err
			}
		case inference.EventToolArgumentsDelta:
			if err := writeSSEEvent(writer, "content_block_delta", contentBlockDeltaData{
				Type: "content_block_delta", Index: event.ToolArgumentsDelta.Index,
				Delta: inputJSONDelta{Type: "input_json_delta", PartialJSON: event.ToolArgumentsDelta.Delta},
			}); err != nil {
				return err
			}
		case inference.EventContentBlockStop:
			if err := writeSSEEvent(writer, "content_block_stop", contentBlockStopData{
				Type: "content_block_stop", Index: event.ContentBlockStop.Index,
			}); err != nil {
				return err
			}
		case inference.EventUsageUpdate:
			usage = event.UsageUpdate.Usage
		case inference.EventResponseFinish:
			if err := writeSSEEvent(writer, "message_delta", messageDeltaData{
				Type: "message_delta", Delta: messageDeltaPayload{StopReason: anthropicStopReason(event.ResponseFinish.StopReason)},
				Usage: mapUsage(usage),
			}); err != nil {
				return err
			}
			return writeSSEEvent(writer, "message_stop", messageStopData{Type: "message_stop"})
		case inference.EventStreamError:
			return writeStreamError(writer, event.StreamError.Message, "api_error")
		}
	}
}

func writeSSEEvent(writer FlushWriter, eventName string, payload any) error {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("encode Anthropic stream event: %w", err)
	}
	if _, err := fmt.Fprintf(writer, "event: %s\ndata: %s\n\n", eventName, encoded); err != nil {
		return err
	}
	writer.Flush()
	return nil
}

func anthropicStopReason(reason inference.StopReason) string {
	switch reason {
	case inference.StopMaxTokens:
		return "max_tokens"
	case inference.StopSequence:
		return "stop_sequence"
	case inference.StopToolUse:
		return "tool_use"
	case inference.StopContentFilter:
		return "refusal"
	default:
		return "end_turn"
	}
}

func mapUsage(usage inference.Usage) usageDTO {
	return usageDTO{
		InputTokens: usage.InputTokens, OutputTokens: usage.OutputTokens,
		CacheReadInputTokens: usage.CacheReadInputTokens, CacheCreationInputTokens: usage.CacheWriteInputTokens,
	}
}
