package anthropic

import "encoding/json"

const maxRequestBodyBytes = 4 << 20

type requestDTO struct {
	Model         string           `json:"model"`
	MaxTokens     *int64           `json:"max_tokens"`
	System        json.RawMessage  `json:"system"`
	Messages      []messageDTO     `json:"messages"`
	Tools         []toolDTO        `json:"tools"`
	ToolChoice    *toolChoiceDTO   `json:"tool_choice"`
	OutputConfig  *outputConfigDTO `json:"output_config"`
	Temperature   *float64         `json:"temperature"`
	TopP          *float64         `json:"top_p"`
	StopSequences *[]string        `json:"stop_sequences"`
	Stream        bool             `json:"stream"`
}

type messageDTO struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

type contentBlockDTO struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	Source    *imageSourceDTO `json:"source"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
	Content   json.RawMessage `json:"content"`
	IsError   bool            `json:"is_error"`
}

type imageSourceDTO struct {
	Type      string `json:"type"`
	MediaType string `json:"media_type"`
	Data      string `json:"data"`
	URL       string `json:"url"`
}

type toolDTO struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
	Strict      bool            `json:"strict"`
}

type toolChoiceDTO struct {
	Type string `json:"type"`
	Name string `json:"name"`
}

type outputConfigDTO struct {
	Format *outputFormatDTO `json:"format"`
}

type outputFormatDTO struct {
	Type   string          `json:"type"`
	Schema json.RawMessage `json:"schema"`
}
