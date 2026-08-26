package openai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	inference "github.com/goairix/llm-proxy/internal/domain/inference/model"
	inferenceport "github.com/goairix/llm-proxy/internal/domain/inference/port"
)

type FlushWriter interface {
	io.Writer
	Flush()
}

func EncodeResponse(writer io.Writer, response inference.Response) error {
	if writer == nil {
		return fmt.Errorf("OpenAI response writer is nil")
	}
	if err := response.Validate(); err != nil {
		return fmt.Errorf("validate OpenAI response: %w", err)
	}
	var text strings.Builder
	message := assistantMessageDTO{Role: "assistant"}
	for _, block := range response.Content {
		switch block.Type {
		case inference.ContentText:
			text.WriteString(block.Text.Text)
		case inference.ContentToolCall:
			message.ToolCalls = append(message.ToolCalls, responseToolCall{
				ID: block.ToolCall.ID, Type: "function",
				Function: responseFunctionCall{Name: block.ToolCall.Name, Arguments: string(block.ToolCall.Arguments)},
			})
		}
	}
	if text.Len() > 0 {
		content := text.String()
		message.Content = &content
	}
	payload := chatCompletionResponse{
		ID: "chatcmpl-" + response.ID.String(), Object: "chat.completion",
		Created: response.CreatedAt.Unix(), Model: response.Model,
		Choices: []completionChoice{{Index: 0, Message: message, FinishReason: openAIFinishReason(response.StopReason)}},
		Usage:   mapUsage(response.Usage),
	}
	return json.NewEncoder(writer).Encode(payload)
}

func EncodeStream(ctx context.Context, writer FlushWriter, stream inferenceport.Stream, includeUsage bool) error {
	if writer == nil {
		return fmt.Errorf("OpenAI stream writer is nil")
	}
	if stream == nil {
		return fmt.Errorf("OpenAI stream is nil")
	}
	validator := inference.NewSequenceValidator()
	state := streamState{toolIndexes: make(map[int]int), includeUsage: includeUsage}
	var pendingUsage *usageDTO
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
			return fmt.Errorf("validate OpenAI stream event: %w", err)
		}
		switch event.Type {
		case inference.EventResponseStart:
			state.id = "chatcmpl-" + event.ResponseStart.ID.String()
			state.model = event.ResponseStart.Model
			state.created = event.ResponseStart.CreatedAt.Unix()
			if err := state.writeChunk(writer, chunkDelta{Role: "assistant"}, nil); err != nil {
				return err
			}
		case inference.EventTextDelta:
			if err := state.writeChunk(writer, chunkDelta{Content: event.TextDelta.Text}, nil); err != nil {
				return err
			}
		case inference.EventToolCallStart:
			toolIndex := len(state.toolIndexes)
			state.toolIndexes[event.ToolCallStart.Index] = toolIndex
			delta := toolCallDelta{
				Index: toolIndex, ID: event.ToolCallStart.ID, Type: "function",
				Function: responseFunctionCallDelta{Name: event.ToolCallStart.Name, Arguments: ""},
			}
			if err := state.writeChunk(writer, chunkDelta{ToolCalls: []toolCallDelta{delta}}, nil); err != nil {
				return err
			}
		case inference.EventToolArgumentsDelta:
			toolIndex, exists := state.toolIndexes[event.ToolArgumentsDelta.Index]
			if !exists {
				return fmt.Errorf("tool arguments reference unknown block %d", event.ToolArgumentsDelta.Index)
			}
			delta := toolCallDelta{Index: toolIndex, Function: responseFunctionCallDelta{Arguments: event.ToolArgumentsDelta.Delta}}
			if err := state.writeChunk(writer, chunkDelta{ToolCalls: []toolCallDelta{delta}}, nil); err != nil {
				return err
			}
		case inference.EventUsageUpdate:
			if includeUsage {
				usage := mapUsage(event.UsageUpdate.Usage)
				pendingUsage = &usage
			}
		case inference.EventResponseFinish:
			finish := openAIFinishReason(event.ResponseFinish.StopReason)
			if err := state.writeChunk(writer, chunkDelta{}, &finish); err != nil {
				return err
			}
			if pendingUsage != nil {
				if err := state.writeUsage(writer, *pendingUsage); err != nil {
					return err
				}
			}
			return writeSSEFrame(writer, []byte("[DONE]"))
		case inference.EventStreamError:
			return writeStreamError(writer, event.StreamError.Message, "connector_failed", "api_error")
		}
	}
}

type streamState struct {
	id           string
	model        string
	created      int64
	toolIndexes  map[int]int
	includeUsage bool
}

func (s streamState) writeChunk(writer FlushWriter, delta chunkDelta, finish *string) error {
	var usage any
	if s.includeUsage {
		usage = (*usageDTO)(nil)
	}
	payload := chatCompletionChunk{
		ID: s.id, Object: "chat.completion.chunk", Created: s.created, Model: s.model,
		Choices: []chunkChoice{{Index: 0, Delta: delta, FinishReason: finish}}, Usage: usage,
	}
	return writeSSEJSON(writer, payload)
}

func (s streamState) writeUsage(writer FlushWriter, usage usageDTO) error {
	return writeSSEJSON(writer, chatCompletionChunk{
		ID: s.id, Object: "chat.completion.chunk", Created: s.created, Model: s.model,
		Choices: []chunkChoice{}, Usage: usage,
	})
}

func writeSSEJSON(writer FlushWriter, payload any) error {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("encode OpenAI stream frame: %w", err)
	}
	return writeSSEFrame(writer, encoded)
}

func writeSSEFrame(writer FlushWriter, data []byte) error {
	if _, err := writer.Write(append(append([]byte("data: "), data...), '\n', '\n')); err != nil {
		return err
	}
	writer.Flush()
	return nil
}

func openAIFinishReason(reason inference.StopReason) string {
	switch reason {
	case inference.StopMaxTokens:
		return "length"
	case inference.StopToolUse:
		return "tool_calls"
	case inference.StopContentFilter:
		return "content_filter"
	default:
		return "stop"
	}
}

func mapUsage(usage inference.Usage) usageDTO {
	mapped := usageDTO{
		PromptTokens: usage.InputTokens, CompletionTokens: usage.OutputTokens, TotalTokens: usage.TotalTokens(),
	}
	if usage.CacheReadInputTokens > 0 {
		mapped.PromptTokensDetails = &promptTokenDetailsDTO{CachedTokens: usage.CacheReadInputTokens}
	}
	return mapped
}
