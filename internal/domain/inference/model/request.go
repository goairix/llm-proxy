package model

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
)

type Role string

const (
	RoleSystem    Role = "system"
	RoleDeveloper Role = "developer"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
)

func (r Role) Validate() error {
	switch r {
	case RoleSystem, RoleDeveloper, RoleUser, RoleAssistant:
		return nil
	default:
		return fmt.Errorf("unsupported message role %q", r)
	}
}

type Message struct {
	Role    Role
	Content []ContentBlock
}

func (m Message) Validate() error {
	if err := m.Role.Validate(); err != nil {
		return err
	}
	if len(m.Content) == 0 {
		return fmt.Errorf("message content is required")
	}
	for index := range m.Content {
		block := m.Content[index]
		if err := block.Validate(); err != nil {
			return fmt.Errorf("content block %d: %w", index, err)
		}
		if err := validateRoleContent(m.Role, block.Type); err != nil {
			return fmt.Errorf("content block %d: %w", index, err)
		}
	}
	return nil
}

func validateRoleContent(role Role, contentType ContentType) error {
	switch role {
	case RoleSystem, RoleDeveloper:
		if contentType != ContentText {
			return fmt.Errorf("role %q only supports text content", role)
		}
	case RoleUser:
		if contentType == ContentToolCall {
			return fmt.Errorf("user role does not support tool call content")
		}
	case RoleAssistant:
		if contentType == ContentImage || contentType == ContentToolResult {
			return fmt.Errorf("assistant role does not support %q content", contentType)
		}
	}
	return nil
}

type Tool struct {
	Name        string
	Description string
	InputSchema json.RawMessage
	Strict      bool
}

type ToolChoiceMode string

const (
	ToolChoiceAuto     ToolChoiceMode = "auto"
	ToolChoiceNone     ToolChoiceMode = "none"
	ToolChoiceRequired ToolChoiceMode = "required"
	ToolChoiceSpecific ToolChoiceMode = "specific"
)

type ToolChoice struct {
	Mode ToolChoiceMode
	Name string
}

type StructuredOutputType string

const (
	StructuredJSONObject StructuredOutputType = "json_object"
	StructuredJSONSchema StructuredOutputType = "json_schema"
)

type StructuredOutput struct {
	Type        StructuredOutputType
	Name        string
	Description string
	Strict      bool
	Schema      json.RawMessage
}

type Requirements struct {
	Text             bool
	ImageInput       bool
	Tools            bool
	StructuredOutput bool
	Streaming        bool
}

type Request struct {
	Model            string
	Messages         []Message
	Tools            []Tool
	ToolChoice       *ToolChoice
	StructuredOutput *StructuredOutput
	Temperature      Optional[float64]
	TopP             Optional[float64]
	MaxTokens        Optional[int64]
	Stop             Optional[[]string]
	Stream           bool
}

func (r Request) Validate() error {
	if strings.TrimSpace(r.Model) == "" {
		return fmt.Errorf("model is required")
	}
	if len(r.Messages) == 0 {
		return fmt.Errorf("messages are required")
	}
	for index := range r.Messages {
		if err := r.Messages[index].Validate(); err != nil {
			return fmt.Errorf("message %d: %w", index, err)
		}
	}
	if err := validateToolGraph(r.Messages); err != nil {
		return err
	}
	toolNames := make(map[string]struct{}, len(r.Tools))
	for index := range r.Tools {
		tool := r.Tools[index]
		name := strings.TrimSpace(tool.Name)
		if name == "" {
			return fmt.Errorf("tool %d name is required", index)
		}
		if _, duplicate := toolNames[name]; duplicate {
			return fmt.Errorf("tool name %q is duplicated", name)
		}
		if err := validateJSONObject(tool.InputSchema, fmt.Sprintf("tool %d input schema", index)); err != nil {
			return err
		}
		toolNames[name] = struct{}{}
	}
	if err := validateToolChoice(r.ToolChoice, toolNames); err != nil {
		return err
	}
	if err := validateStructuredOutput(r.StructuredOutput); err != nil {
		return err
	}
	if r.Temperature.Set && (!finite(r.Temperature.Value) || r.Temperature.Value < 0 || r.Temperature.Value > 2) {
		return fmt.Errorf("temperature must be between 0 and 2")
	}
	if r.TopP.Set && (!finite(r.TopP.Value) || r.TopP.Value < 0 || r.TopP.Value > 1) {
		return fmt.Errorf("top_p must be between 0 and 1")
	}
	if r.MaxTokens.Set && r.MaxTokens.Value < 0 {
		return fmt.Errorf("max_tokens must not be negative")
	}
	if r.Stop.Set {
		for index, stop := range r.Stop.Value {
			if stop == "" {
				return fmt.Errorf("stop sequence %d must not be empty", index)
			}
		}
	}
	return nil
}

func validateToolGraph(messages []Message) error {
	seen := make(map[string]struct{})
	pending := make(map[string]struct{})
	for messageIndex, message := range messages {
		if len(pending) > 0 {
			if message.Role != RoleUser {
				return fmt.Errorf("message %d must provide pending tool results", messageIndex)
			}
			hasResult := false
			for _, block := range message.Content {
				if block.Type == ContentToolResult {
					hasResult = true
					break
				}
			}
			if !hasResult {
				return fmt.Errorf("message %d must provide pending tool results", messageIndex)
			}
		}
		for blockIndex, block := range message.Content {
			switch block.Type {
			case ContentToolCall:
				id := block.ToolCall.ID
				if _, duplicate := seen[id]; duplicate {
					return fmt.Errorf("message %d content block %d duplicates tool call id %q", messageIndex, blockIndex, id)
				}
				seen[id] = struct{}{}
				pending[id] = struct{}{}
			case ContentToolResult:
				id := block.ToolResult.ToolCallID
				if _, exists := pending[id]; !exists {
					return fmt.Errorf("message %d content block %d references unknown or completed tool call %q", messageIndex, blockIndex, id)
				}
				delete(pending, id)
			}
		}
	}
	if len(pending) > 0 {
		return fmt.Errorf("request contains unresolved tool calls")
	}
	return nil
}

func (r Request) RequiredCapabilities() Requirements {
	requirements := Requirements{
		Text:             true,
		Tools:            len(r.Tools) > 0,
		StructuredOutput: r.StructuredOutput != nil,
		Streaming:        r.Stream,
	}
	for _, message := range r.Messages {
		for _, block := range message.Content {
			switch block.Type {
			case ContentImage:
				requirements.ImageInput = true
			case ContentToolCall, ContentToolResult:
				requirements.Tools = true
			}
		}
	}
	return requirements
}

func validateToolChoice(choice *ToolChoice, tools map[string]struct{}) error {
	if choice == nil {
		return nil
	}
	switch choice.Mode {
	case ToolChoiceAuto, ToolChoiceNone:
		if strings.TrimSpace(choice.Name) != "" {
			return fmt.Errorf("tool choice %q must not name a tool", choice.Mode)
		}
	case ToolChoiceRequired:
		if len(tools) == 0 {
			return fmt.Errorf("required tool choice needs at least one tool")
		}
		if strings.TrimSpace(choice.Name) != "" {
			return fmt.Errorf("required tool choice must not name a tool")
		}
	case ToolChoiceSpecific:
		name := strings.TrimSpace(choice.Name)
		if name == "" {
			return fmt.Errorf("specific tool choice name is required")
		}
		if _, exists := tools[name]; !exists {
			return fmt.Errorf("specific tool choice %q is not defined", name)
		}
	default:
		return fmt.Errorf("unsupported tool choice mode %q", choice.Mode)
	}
	return nil
}

func validateStructuredOutput(output *StructuredOutput) error {
	if output == nil {
		return nil
	}
	switch output.Type {
	case StructuredJSONObject:
		if len(output.Schema) != 0 {
			return fmt.Errorf("json_object structured output must not include a schema")
		}
	case StructuredJSONSchema:
		if err := validateJSONObject(output.Schema, "structured output schema"); err != nil {
			return err
		}
	default:
		return fmt.Errorf("unsupported structured output type %q", output.Type)
	}
	return nil
}

func finite(value float64) bool { return !math.IsNaN(value) && !math.IsInf(value, 0) }
