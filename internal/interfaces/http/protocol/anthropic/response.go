package anthropic

import "encoding/json"

type messageResponse struct {
	ID           string                 `json:"id"`
	Type         string                 `json:"type"`
	Role         string                 `json:"role"`
	Content      []responseContentBlock `json:"content"`
	Model        string                 `json:"model"`
	StopReason   string                 `json:"stop_reason"`
	StopSequence *string                `json:"stop_sequence"`
	Usage        usageDTO               `json:"usage"`
}

type responseContentBlock struct {
	Type  string          `json:"type"`
	Text  string          `json:"text,omitempty"`
	ID    string          `json:"id,omitempty"`
	Name  string          `json:"name,omitempty"`
	Input json.RawMessage `json:"input,omitempty"`
}

type usageDTO struct {
	InputTokens              int64 `json:"input_tokens"`
	OutputTokens             int64 `json:"output_tokens"`
	CacheReadInputTokens     int64 `json:"cache_read_input_tokens,omitempty"`
	CacheCreationInputTokens int64 `json:"cache_creation_input_tokens,omitempty"`
}

type messageStartData struct {
	Type    string              `json:"type"`
	Message messageStartMessage `json:"message"`
}

type messageStartMessage struct {
	ID           string                 `json:"id"`
	Type         string                 `json:"type"`
	Role         string                 `json:"role"`
	Content      []responseContentBlock `json:"content"`
	Model        string                 `json:"model"`
	StopReason   *string                `json:"stop_reason"`
	StopSequence *string                `json:"stop_sequence"`
	Usage        usageDTO               `json:"usage"`
}

type contentBlockStartData struct {
	Type         string `json:"type"`
	Index        int    `json:"index"`
	ContentBlock any    `json:"content_block"`
}

type textBlockStart struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type toolBlockStart struct {
	Type  string          `json:"type"`
	ID    string          `json:"id"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`
}

type contentBlockDeltaData struct {
	Type  string `json:"type"`
	Index int    `json:"index"`
	Delta any    `json:"delta"`
}

type textDelta struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type inputJSONDelta struct {
	Type        string `json:"type"`
	PartialJSON string `json:"partial_json"`
}

type contentBlockStopData struct {
	Type  string `json:"type"`
	Index int    `json:"index"`
}

type messageDeltaData struct {
	Type  string              `json:"type"`
	Delta messageDeltaPayload `json:"delta"`
	Usage usageDTO            `json:"usage"`
}

type messageDeltaPayload struct {
	StopReason   string  `json:"stop_reason"`
	StopSequence *string `json:"stop_sequence"`
}

type messageStopData struct {
	Type string `json:"type"`
}
