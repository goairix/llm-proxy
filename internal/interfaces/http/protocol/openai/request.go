package openai

import "encoding/json"

const maxRequestBodyBytes = 4 << 20

type requestDTO struct {
	Model               string             `json:"model"`
	Messages            []messageDTO       `json:"messages"`
	Tools               []toolDTO          `json:"tools"`
	ToolChoice          json.RawMessage    `json:"tool_choice"`
	ResponseFormat      *responseFormatDTO `json:"response_format"`
	Temperature         *float64           `json:"temperature"`
	TopP                *float64           `json:"top_p"`
	MaxTokens           *int64             `json:"max_tokens"`
	MaxCompletionTokens *int64             `json:"max_completion_tokens"`
	Stop                json.RawMessage    `json:"stop"`
	Stream              bool               `json:"stream"`
	StreamOptions       *streamOptionsDTO  `json:"stream_options"`
}

type messageDTO struct {
	Role       string          `json:"role"`
	Content    json.RawMessage `json:"content"`
	ToolCallID string          `json:"tool_call_id"`
	ToolCalls  []toolCallDTO   `json:"tool_calls"`
}

type contentPartDTO struct {
	Type     string       `json:"type"`
	Text     string       `json:"text"`
	ImageURL *imageURLDTO `json:"image_url"`
}

type imageURLDTO struct {
	URL    string `json:"url"`
	Detail string `json:"detail"`
}

type toolDTO struct {
	Type     string          `json:"type"`
	Function functionToolDTO `json:"function"`
}

type functionToolDTO struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
	Strict      bool            `json:"strict"`
}

type toolCallDTO struct {
	ID       string          `json:"id"`
	Type     string          `json:"type"`
	Function functionCallDTO `json:"function"`
}

type functionCallDTO struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type toolChoiceDTO struct {
	Type     string `json:"type"`
	Function struct {
		Name string `json:"name"`
	} `json:"function"`
}

type responseFormatDTO struct {
	Type       string         `json:"type"`
	JSONSchema *jsonSchemaDTO `json:"json_schema"`
}

type jsonSchemaDTO struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Strict      bool            `json:"strict"`
	Schema      json.RawMessage `json:"schema"`
}

type streamOptionsDTO struct {
	IncludeUsage bool `json:"include_usage"`
}
