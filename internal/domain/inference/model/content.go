package model

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
)

type ContentType string

const (
	ContentText       ContentType = "text"
	ContentImage      ContentType = "image"
	ContentToolCall   ContentType = "tool_call"
	ContentToolResult ContentType = "tool_result"
	ContentRefusal    ContentType = "refusal"
)

type TextContent struct {
	Text string
}

type RefusalContent struct {
	Text string
}

type ImageSourceType string

const (
	ImageURL    ImageSourceType = "url"
	ImageBase64 ImageSourceType = "base64"
)

type ImageSource struct {
	Type      ImageSourceType
	MediaType string
	Data      string
	Detail    string
}

type ImageContent struct {
	Source ImageSource
}

type ToolCallContent struct {
	ID        string
	Name      string
	Arguments json.RawMessage
}

// ToolResultContent is the Phase 1B text-or-JSON tool result subset.
type ToolResultContent struct {
	ToolCallID string
	Text       *string
	JSON       json.RawMessage
	IsError    bool
}

type ContentBlock struct {
	Type       ContentType
	Text       *TextContent
	Image      *ImageContent
	ToolCall   *ToolCallContent
	ToolResult *ToolResultContent
	Refusal    *RefusalContent
}

func (b ContentBlock) Validate() error {
	payloads := 0
	for _, present := range []bool{b.Text != nil, b.Image != nil, b.ToolCall != nil, b.ToolResult != nil, b.Refusal != nil} {
		if present {
			payloads++
		}
	}
	if payloads != 1 {
		return fmt.Errorf("content block must contain exactly one payload")
	}

	switch b.Type {
	case ContentText:
		if b.Text == nil {
			return fmt.Errorf("text content payload is required")
		}
		if strings.TrimSpace(b.Text.Text) == "" {
			return fmt.Errorf("text content must not be empty")
		}
	case ContentImage:
		if b.Image == nil {
			return fmt.Errorf("image content payload is required")
		}
		if err := b.Image.Source.Validate(); err != nil {
			return fmt.Errorf("image source: %w", err)
		}
	case ContentToolCall:
		if b.ToolCall == nil {
			return fmt.Errorf("tool call content payload is required")
		}
		if strings.TrimSpace(b.ToolCall.ID) == "" {
			return fmt.Errorf("tool call id is required")
		}
		if strings.TrimSpace(b.ToolCall.Name) == "" {
			return fmt.Errorf("tool call name is required")
		}
		if err := validateJSONObject(b.ToolCall.Arguments, "tool call arguments"); err != nil {
			return err
		}
	case ContentToolResult:
		if b.ToolResult == nil {
			return fmt.Errorf("tool result content payload is required")
		}
		if strings.TrimSpace(b.ToolResult.ToolCallID) == "" {
			return fmt.Errorf("tool result call id is required")
		}
		hasText := b.ToolResult.Text != nil
		hasJSON := len(b.ToolResult.JSON) > 0
		if hasText == hasJSON {
			return fmt.Errorf("tool result must contain exactly one text or JSON payload")
		}
		if hasJSON && !json.Valid(b.ToolResult.JSON) {
			return fmt.Errorf("tool result JSON is invalid")
		}
	case ContentRefusal:
		if b.Refusal == nil {
			return fmt.Errorf("refusal content payload is required")
		}
		if strings.TrimSpace(b.Refusal.Text) == "" {
			return fmt.Errorf("refusal content must not be empty")
		}
	default:
		return fmt.Errorf("unsupported content type %q", b.Type)
	}
	return nil
}

func (s ImageSource) Validate() error {
	if strings.TrimSpace(s.Data) == "" {
		return fmt.Errorf("image data is required")
	}
	if s.Detail != "" && s.Detail != "auto" && s.Detail != "low" && s.Detail != "high" {
		return fmt.Errorf("unsupported image detail %q", s.Detail)
	}
	switch s.Type {
	case ImageURL:
		parsed, err := url.Parse(s.Data)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
			return fmt.Errorf("image URL must use http or https")
		}
	case ImageBase64:
		if strings.TrimSpace(s.MediaType) == "" {
			return fmt.Errorf("base64 image media type is required")
		}
		if _, err := base64.StdEncoding.DecodeString(s.Data); err != nil {
			return fmt.Errorf("base64 image data is invalid")
		}
	default:
		return fmt.Errorf("unsupported image source type %q", s.Type)
	}
	return nil
}

func validateJSONObject(value json.RawMessage, field string) error {
	if len(value) == 0 {
		return fmt.Errorf("%s is required", field)
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(value, &object); err != nil || object == nil {
		return fmt.Errorf("%s must be a JSON object", field)
	}
	return nil
}
