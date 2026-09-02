package responses

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/google/uuid"

	gatewayservice "github.com/goairix/llm-proxy/internal/application/gateway/service"
	inference "github.com/goairix/llm-proxy/internal/domain/inference/model"
	inferenceport "github.com/goairix/llm-proxy/internal/domain/inference/port"
)

func EncodeResponse(writer io.Writer, response inference.Response) error {
	return encodeResponse(writer, response, uuid.NewV7)
}
func encodeResponse(writer io.Writer, response inference.Response, idGenerator func() (uuid.UUID, error)) error {
	if writer == nil || idGenerator == nil {
		return fmt.Errorf("Responses writer and id generator are required")
	}
	if err := response.Validate(); err != nil {
		return err
	}
	output, err := buildOutput(response.Content, idGenerator)
	if err != nil {
		return err
	}
	return json.NewEncoder(writer).Encode(responseObject(response.ID, response.Model, response.CreatedAt.Unix(), response.StopReason, output, response.Usage))
}

func buildOutput(content []inference.ContentBlock, idGenerator func() (uuid.UUID, error)) ([]any, error) {
	var output []any
	var parts []any
	flush := func() error {
		if len(parts) == 0 {
			return nil
		}
		id, err := nextID(idGenerator)
		if err != nil {
			return err
		}
		output = append(output, map[string]any{"id": localID("msg", id), "type": "message", "status": "completed", "role": "assistant", "content": parts})
		parts = nil
		return nil
	}
	for _, block := range content {
		switch block.Type {
		case inference.ContentText:
			parts = append(parts, map[string]any{"type": "output_text", "text": block.Text.Text, "annotations": []any{}})
		case inference.ContentRefusal:
			parts = append(parts, map[string]any{"type": "refusal", "refusal": block.Refusal.Text})
		case inference.ContentToolCall:
			if err := flush(); err != nil {
				return nil, err
			}
			id, err := nextID(idGenerator)
			if err != nil {
				return nil, err
			}
			output = append(output, map[string]any{"id": localID("fc", id), "type": "function_call", "status": "completed", "call_id": block.ToolCall.ID, "name": block.ToolCall.Name, "arguments": string(block.ToolCall.Arguments)})
		}
	}
	if err := flush(); err != nil {
		return nil, err
	}
	return output, nil
}

func responseObject(id uuid.UUID, model string, created int64, reason inference.StopReason, output []any, usage inference.Usage) map[string]any {
	status := "completed"
	var incomplete any
	if reason == inference.StopMaxTokens {
		status = "incomplete"
		incomplete = map[string]any{"reason": "max_output_tokens"}
	} else if reason == inference.StopContentFilter {
		status = "incomplete"
		incomplete = map[string]any{"reason": "content_filter"}
	}
	if output == nil {
		output = []any{}
	}
	return map[string]any{"id": localID("resp", id), "object": "response", "created_at": created, "status": status, "incomplete_details": incomplete, "model": model, "output": output, "usage": usageObject(usage)}
}
func usageObject(usage inference.Usage) map[string]any {
	return map[string]any{"input_tokens": usage.InputTokens, "output_tokens": usage.OutputTokens, "total_tokens": usage.TotalTokens(), "input_tokens_details": map[string]any{"cached_tokens": usage.CacheReadInputTokens}, "output_tokens_details": map[string]any{"reasoning_tokens": int64(0)}}
}
func localID(prefix string, id uuid.UUID) string {
	return prefix + "_" + strings.ReplaceAll(id.String(), "-", "")
}

func nextID(generator func() (uuid.UUID, error)) (uuid.UUID, error) {
	id, err := generator()
	if err != nil {
		return uuid.Nil, err
	}
	if id == uuid.Nil || id.Version() != uuid.Version(7) {
		return uuid.Nil, fmt.Errorf("Responses item id must be UUIDv7")
	}
	return id, nil
}

type streamEncoder struct {
	writer     FlushWriter
	ids        func() (uuid.UUID, error)
	sequence   int64
	responseID uuid.UUID
	model      string
	created    int64
	output     []any
	blocks     map[int]*encodedBlock
	usage      inference.Usage
}
type encodedBlock struct {
	kind                        inference.ContentType
	outputIndex, contentIndex   int
	itemID, callID, name, value string
	item                        map[string]any
	part                        map[string]any
}

func EncodeStream(ctx context.Context, writer FlushWriter, stream inferenceport.Stream) error {
	return encodeStream(ctx, writer, stream, uuid.NewV7)
}
func encodeStream(ctx context.Context, writer FlushWriter, stream inferenceport.Stream, ids func() (uuid.UUID, error)) error {
	if writer == nil || stream == nil || ids == nil {
		return fmt.Errorf("Responses stream dependencies are required")
	}
	state := &streamEncoder{writer: writer, ids: ids, blocks: make(map[int]*encodedBlock)}
	validator := inference.NewSequenceValidator()
	for {
		event, err := stream.Recv(ctx)
		if err != nil {
			if errors.Is(err, io.EOF) {
				return validator.ValidateEOF()
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return state.fail("供应商流式响应失败")
		}
		if err := validator.Push(event); err != nil {
			return err
		}
		switch event.Type {
		case inference.EventResponseStart:
			state.responseID = event.ResponseStart.ID
			state.model = event.ResponseStart.Model
			state.created = event.ResponseStart.CreatedAt.Unix()
			if err := state.write("response.created", map[string]any{"response": map[string]any{
				"id": localID("resp", state.responseID), "object": "response", "created_at": state.created,
				"status": "in_progress", "incomplete_details": nil, "model": state.model, "output": []any{}, "usage": nil,
			}}); err != nil {
				return err
			}
		case inference.EventContentBlockStart:
			if err := state.startContent(event); err != nil {
				return err
			}
		case inference.EventToolCallStart:
			if err := state.startTool(event); err != nil {
				return err
			}
		case inference.EventTextDelta:
			if err := state.delta(event.TextDelta.Index, "response.output_text.delta", event.TextDelta.Text); err != nil {
				return err
			}
		case inference.EventRefusalDelta:
			if err := state.delta(event.RefusalDelta.Index, "response.refusal.delta", event.RefusalDelta.Text); err != nil {
				return err
			}
		case inference.EventToolArgumentsDelta:
			if err := state.delta(event.ToolArgumentsDelta.Index, "response.function_call_arguments.delta", event.ToolArgumentsDelta.Delta); err != nil {
				return err
			}
		case inference.EventContentBlockStop:
			if err := state.stop(event.ContentBlockStop.Index); err != nil {
				return err
			}
		case inference.EventUsageUpdate:
			state.usage = event.UsageUpdate.Usage
		case inference.EventResponseFinish:
			name := "response.completed"
			if event.ResponseFinish.StopReason == inference.StopMaxTokens || event.ResponseFinish.StopReason == inference.StopContentFilter {
				name = "response.incomplete"
			}
			return state.write(name, map[string]any{"response": responseObject(state.responseID, state.model, state.created, event.ResponseFinish.StopReason, state.output, state.usage)})
		case inference.EventStreamError:
			return state.fail(event.StreamError.Message)
		}
	}
}
func (s *streamEncoder) startContent(event inference.Event) error {
	id, err := nextID(s.ids)
	if err != nil {
		return err
	}
	block := &encodedBlock{kind: event.ContentBlockStart.Type, outputIndex: len(s.output), contentIndex: 0, itemID: localID("msg", id)}
	partType := "output_text"
	block.value = ""
	block.part = map[string]any{"type": partType, "text": "", "annotations": []any{}}
	if block.kind == inference.ContentRefusal {
		partType = "refusal"
		block.part = map[string]any{"type": partType, "refusal": ""}
	}
	block.item = map[string]any{"id": block.itemID, "type": "message", "status": "in_progress", "role": "assistant", "content": []any{block.part}}
	s.blocks[event.ContentBlockStart.Index] = block
	s.output = append(s.output, block.item)
	if err := s.write("response.output_item.added", map[string]any{"output_index": block.outputIndex, "item": block.item}); err != nil {
		return err
	}
	return s.write("response.content_part.added", map[string]any{"item_id": block.itemID, "output_index": block.outputIndex, "content_index": 0, "part": block.part})
}
func (s *streamEncoder) startTool(event inference.Event) error {
	id, err := nextID(s.ids)
	if err != nil {
		return err
	}
	block := &encodedBlock{kind: inference.ContentToolCall, outputIndex: len(s.output), itemID: localID("fc", id), callID: event.ToolCallStart.ID, name: event.ToolCallStart.Name}
	block.item = map[string]any{"id": block.itemID, "type": "function_call", "status": "in_progress", "call_id": block.callID, "name": block.name, "arguments": ""}
	s.blocks[event.ToolCallStart.Index] = block
	s.output = append(s.output, block.item)
	return s.write("response.output_item.added", map[string]any{"output_index": block.outputIndex, "item": block.item})
}
func (s *streamEncoder) delta(index int, name, value string) error {
	block := s.blocks[index]
	if block == nil {
		return fmt.Errorf("unknown content block %d", index)
	}
	block.value += value
	if block.kind == inference.ContentText {
		block.part["text"] = block.value
	} else if block.kind == inference.ContentRefusal {
		block.part["refusal"] = block.value
	} else {
		block.item["arguments"] = block.value
	}
	payload := map[string]any{"output_index": block.outputIndex, "delta": value}
	if block.kind != inference.ContentToolCall {
		payload["item_id"] = block.itemID
		payload["content_index"] = block.contentIndex
	}
	return s.write(name, payload)
}
func (s *streamEncoder) stop(index int) error {
	block := s.blocks[index]
	if block == nil {
		return fmt.Errorf("unknown content block %d", index)
	}
	block.item["status"] = "completed"
	if block.kind == inference.ContentToolCall {
		if err := s.write("response.function_call_arguments.done", map[string]any{"output_index": block.outputIndex, "item_id": block.itemID, "arguments": block.value}); err != nil {
			return err
		}
	} else {
		name := "response.output_text.done"
		payload := map[string]any{"item_id": block.itemID, "output_index": block.outputIndex, "content_index": 0, "text": block.value}
		if block.kind == inference.ContentRefusal {
			name = "response.refusal.done"
			delete(payload, "text")
			payload["refusal"] = block.value
		}
		if err := s.write(name, payload); err != nil {
			return err
		}
		if err := s.write("response.content_part.done", map[string]any{"item_id": block.itemID, "output_index": block.outputIndex, "content_index": 0, "part": block.part}); err != nil {
			return err
		}
	}
	if err := s.write("response.output_item.done", map[string]any{"output_index": block.outputIndex, "item": block.item}); err != nil {
		return err
	}
	delete(s.blocks, index)
	return nil
}
func (s *streamEncoder) write(name string, payload map[string]any) error {
	payload["type"] = name
	payload["sequence_number"] = s.sequence
	s.sequence++
	encoded, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	if _, err = s.writer.Write([]byte("event: " + name + "\ndata: " + string(encoded) + "\n\n")); err != nil {
		return err
	}
	s.writer.Flush()
	return nil
}
func (s *streamEncoder) fail(message string) error {
	if s.responseID == uuid.Nil {
		id, err := nextID(s.ids)
		if err != nil {
			return err
		}
		s.responseID = id
	}
	return s.write("response.failed", map[string]any{"response": map[string]any{"id": localID("resp", s.responseID), "object": "response", "created_at": s.created, "status": "failed", "model": s.model, "output": s.output, "error": map[string]any{"code": string(gatewayservice.ConnectorFailed), "message": message}}})
}
