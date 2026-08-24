package tokenusage

import (
	"bytes"
	"encoding/json"
	"math"
)

type usageEnvelope struct {
	Usage json.RawMessage `json:"usage"`
}

type openAIResponsesUsage struct {
	InputTokens  *int64 `json:"input_tokens"`
	OutputTokens *int64 `json:"output_tokens"`
	InputDetails struct {
		Cached     *int64 `json:"cached_tokens"`
		CacheWrite *int64 `json:"cache_write_tokens"`
	} `json:"input_tokens_details"`
	OutputDetails struct {
		Reasoning *int64 `json:"reasoning_tokens"`
	} `json:"output_tokens_details"`
}

type openAICompletionsUsage struct {
	PromptTokens     *int64 `json:"prompt_tokens"`
	CompletionTokens *int64 `json:"completion_tokens"`
	PromptDetails    struct {
		Cached     *int64 `json:"cached_tokens"`
		CacheWrite *int64 `json:"cache_write_tokens"`
	} `json:"prompt_tokens_details"`
	CompletionDetails struct {
		Reasoning *int64 `json:"reasoning_tokens"`
	} `json:"completion_tokens_details"`
}

type anthropicUsage struct {
	InputTokens        *int64 `json:"input_tokens"`
	OutputTokens       *int64 `json:"output_tokens"`
	CacheCreationInput *int64 `json:"cache_creation_input_tokens"`
	CacheReadInput     *int64 `json:"cache_read_input_tokens"`
	OutputDetails      struct {
		Thinking *int64 `json:"thinking_tokens"`
	} `json:"output_tokens_details"`
}

func parseJSON(provider, path string, data []byte) Result {
	var envelope usageEnvelope
	if err := json.Unmarshal(data, &envelope); err != nil || len(envelope.Usage) == 0 || bytes.Equal(bytes.TrimSpace(envelope.Usage), []byte("null")) {
		return Result{}
	}

	switch provider {
	case "openai":
		if path == "/openai/v1/responses" || path == "/openai/v1/responses/compact" {
			return parseOpenAIResponses(envelope.Usage)
		}
		return parseOpenAICompletions(envelope.Usage)
	case "anthropic":
		return parseAnthropic(envelope.Usage)
	default:
		return Result{}
	}
}

func parseOpenAIResponses(data []byte) Result {
	var raw openAIResponsesUsage
	if err := json.Unmarshal(data, &raw); err != nil || !requiredNonNegative(raw.InputTokens, raw.OutputTokens) {
		return Result{}
	}
	usage := Usage{
		Input:      *raw.InputTokens,
		Output:     *raw.OutputTokens,
		CacheRead:  optionalValue(raw.InputDetails.Cached),
		CacheWrite: optionalValue(raw.InputDetails.CacheWrite),
		Reasoning:  optionalValue(raw.OutputDetails.Reasoning),
	}
	if !validUsage(usage) {
		return Result{}
	}
	return Result{Usage: usage, Present: true}
}

func parseOpenAICompletions(data []byte) Result {
	var raw openAICompletionsUsage
	if err := json.Unmarshal(data, &raw); err != nil || !requiredNonNegative(raw.PromptTokens, raw.CompletionTokens) {
		return Result{}
	}
	usage := Usage{
		Input:      *raw.PromptTokens,
		Output:     *raw.CompletionTokens,
		CacheRead:  optionalValue(raw.PromptDetails.Cached),
		CacheWrite: optionalValue(raw.PromptDetails.CacheWrite),
		Reasoning:  optionalValue(raw.CompletionDetails.Reasoning),
	}
	if !validUsage(usage) {
		return Result{}
	}
	return Result{Usage: usage, Present: true}
}

func parseAnthropic(data []byte) Result {
	var raw anthropicUsage
	if err := json.Unmarshal(data, &raw); err != nil || !requiredNonNegative(raw.InputTokens, raw.OutputTokens) {
		return Result{}
	}
	cacheWrite := optionalValue(raw.CacheCreationInput)
	cacheRead := optionalValue(raw.CacheReadInput)
	if cacheWrite < 0 || cacheRead < 0 || *raw.InputTokens > math.MaxInt64-cacheWrite || *raw.InputTokens+cacheWrite > math.MaxInt64-cacheRead {
		return Result{}
	}
	usage := Usage{
		Input:      *raw.InputTokens + cacheWrite + cacheRead,
		Output:     *raw.OutputTokens,
		CacheRead:  cacheRead,
		CacheWrite: cacheWrite,
		Reasoning:  optionalValue(raw.OutputDetails.Thinking),
	}
	if !validUsage(usage) {
		return Result{}
	}
	return Result{Usage: usage, Present: true}
}

func requiredNonNegative(values ...*int64) bool {
	for _, value := range values {
		if value == nil || *value < 0 {
			return false
		}
	}
	return true
}

func optionalValue(value *int64) int64 {
	if value == nil {
		return 0
	}
	return *value
}

func validUsage(usage Usage) bool {
	return usage.Input >= 0 && usage.Output >= 0 && usage.CacheRead >= 0 && usage.CacheWrite >= 0 && usage.Reasoning >= 0
}
