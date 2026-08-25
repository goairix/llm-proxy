package openai

type chatCompletionResponse struct {
	ID      string             `json:"id"`
	Object  string             `json:"object"`
	Created int64              `json:"created"`
	Model   string             `json:"model"`
	Choices []completionChoice `json:"choices"`
	Usage   usageDTO           `json:"usage"`
}

type completionChoice struct {
	Index        int                 `json:"index"`
	Message      assistantMessageDTO `json:"message"`
	FinishReason string              `json:"finish_reason"`
}

type assistantMessageDTO struct {
	Role      string             `json:"role"`
	Content   *string            `json:"content"`
	ToolCalls []responseToolCall `json:"tool_calls,omitempty"`
}

type responseToolCall struct {
	ID       string               `json:"id"`
	Type     string               `json:"type"`
	Function responseFunctionCall `json:"function"`
}

type responseFunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type usageDTO struct {
	PromptTokens        int64                  `json:"prompt_tokens"`
	CompletionTokens    int64                  `json:"completion_tokens"`
	TotalTokens         int64                  `json:"total_tokens"`
	PromptTokensDetails *promptTokenDetailsDTO `json:"prompt_tokens_details,omitempty"`
}

type promptTokenDetailsDTO struct {
	CachedTokens int64 `json:"cached_tokens"`
}

type chatCompletionChunk struct {
	ID      string        `json:"id"`
	Object  string        `json:"object"`
	Created int64         `json:"created"`
	Model   string        `json:"model"`
	Choices []chunkChoice `json:"choices"`
	Usage   any           `json:"usage,omitempty"`
}

type chunkChoice struct {
	Index        int        `json:"index"`
	Delta        chunkDelta `json:"delta"`
	FinishReason *string    `json:"finish_reason"`
}

type chunkDelta struct {
	Role      string          `json:"role,omitempty"`
	Content   string          `json:"content,omitempty"`
	ToolCalls []toolCallDelta `json:"tool_calls,omitempty"`
}

type toolCallDelta struct {
	Index    int                       `json:"index"`
	ID       string                    `json:"id,omitempty"`
	Type     string                    `json:"type,omitempty"`
	Function responseFunctionCallDelta `json:"function"`
}

type responseFunctionCallDelta struct {
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments"`
}
