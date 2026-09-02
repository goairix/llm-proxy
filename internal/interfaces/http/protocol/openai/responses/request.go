package responses

import "encoding/json"

const maxRequestBodyBytes = 4 << 20

type requestDTO struct {
	Model              string            `json:"model"`
	Input              json.RawMessage   `json:"input"`
	Instructions       string            `json:"instructions"`
	MaxOutputTokens    *int64            `json:"max_output_tokens"`
	Temperature        *float64          `json:"temperature"`
	TopP               *float64          `json:"top_p"`
	Tools              []json.RawMessage `json:"tools"`
	ToolChoice         json.RawMessage   `json:"tool_choice"`
	Text               *textDTO          `json:"text"`
	Stream             bool              `json:"stream"`
	Store              *bool             `json:"store"`
	PreviousResponseID json.RawMessage   `json:"previous_response_id"`
	Conversation       json.RawMessage   `json:"conversation"`
	Background         json.RawMessage   `json:"background"`
}

type inputHeader struct {
	Type string `json:"type"`
}
type inputMessageDTO struct {
	Type    string          `json:"type"`
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}
type inputContentDTO struct {
	Type     string `json:"type"`
	Text     string `json:"text"`
	ImageURL string `json:"image_url"`
	Detail   string `json:"detail"`
}
type functionCallDTO struct {
	Type      string `json:"type"`
	CallID    string `json:"call_id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}
type functionOutputDTO struct {
	Type   string          `json:"type"`
	CallID string          `json:"call_id"`
	Output json.RawMessage `json:"output"`
}
type toolDTO struct {
	Type        string          `json:"type"`
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
	Strict      bool            `json:"strict"`
}
type toolChoiceDTO struct {
	Type string `json:"type"`
	Name string `json:"name"`
}
type textDTO struct {
	Format textFormatDTO `json:"format"`
}
type textFormatDTO struct {
	Type        string          `json:"type"`
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Strict      bool            `json:"strict"`
	Schema      json.RawMessage `json:"schema"`
}
