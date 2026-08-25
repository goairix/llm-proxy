package tokenusage

import (
	"bytes"
	"encoding/json"
)

type sseDecoder struct {
	provider     string
	path         string
	line         []byte
	data         []byte
	hasData      bool
	discardLine  bool
	discardEvent bool
	latest       Result
	anthropic    anthropicPartialUsage
}

type anthropicPartialUsage struct {
	input      *int64
	output     *int64
	cacheRead  *int64
	cacheWrite *int64
	reasoning  *int64
}

func (d *sseDecoder) Observe(chunk []byte) {
	for _, b := range chunk {
		if b == '\n' {
			d.finishLine()
			d.line = d.line[:0]
			d.discardLine = false
			continue
		}
		if d.discardLine {
			continue
		}
		if len(d.line) == maxCaptureBytes {
			d.line = nil
			d.discardLine = true
			d.discardEvent = true
			continue
		}
		d.line = append(d.line, b)
	}
}

func (d *sseDecoder) Finish() Result {
	if d.discardLine {
		d.discardEvent = true
	} else if len(d.line) > 0 {
		d.finishLine()
	}
	if d.hasData || d.discardEvent {
		d.finishEvent()
	}
	return d.latest
}

func (d *sseDecoder) finishLine() {
	if d.discardLine {
		return
	}
	line := bytes.TrimSuffix(d.line, []byte{'\r'})
	if len(line) == 0 {
		d.finishEvent()
		return
	}
	if d.discardEvent {
		return
	}

	field, value, found := bytes.Cut(line, []byte{':'})
	if !found {
		value = nil
	}
	if !bytes.Equal(field, []byte("data")) {
		return
	}
	value = bytes.TrimPrefix(value, []byte{' '})
	additional := len(value)
	if d.hasData {
		additional++
	}
	if additional > maxCaptureBytes-len(d.data) {
		d.data = nil
		d.hasData = false
		d.discardEvent = true
		return
	}
	if d.hasData {
		d.data = append(d.data, '\n')
	}
	d.data = append(d.data, value...)
	d.hasData = true
}

func (d *sseDecoder) finishEvent() {
	if !d.discardEvent && d.hasData {
		d.parseEvent(d.data)
	}
	d.data = d.data[:0]
	d.hasData = false
	d.discardEvent = false
}

func (d *sseDecoder) parseEvent(data []byte) {
	data = bytes.TrimSpace(data)
	if len(data) == 0 || bytes.Equal(data, []byte("[DONE]")) {
		return
	}

	var event struct {
		Type     string          `json:"type"`
		Usage    json.RawMessage `json:"usage"`
		Response struct {
			Usage json.RawMessage `json:"usage"`
		} `json:"response"`
		Message struct {
			Usage json.RawMessage `json:"usage"`
		} `json:"message"`
	}
	if err := json.Unmarshal(data, &event); err != nil {
		return
	}

	if d.provider == "anthropic" {
		switch event.Type {
		case "message_start":
			d.mergeAnthropic(event.Message.Usage, true)
		case "message_delta":
			d.mergeAnthropic(event.Usage, false)
		}
		return
	}
	if d.path == "/openai/v1/responses" || d.path == "/openai/v1/responses/compact" {
		if event.Type == "response.completed" {
			if result := parseOpenAIResponses(event.Response.Usage); result.Present {
				d.latest = result
			}
		}
		return
	}
	if len(event.Usage) > 0 {
		if result := parseOpenAICompletions(event.Usage); result.Present {
			d.latest = result
		}
	}
}

func (d *sseDecoder) mergeAnthropic(data json.RawMessage, replace bool) {
	if len(data) == 0 {
		return
	}
	var raw anthropicUsage
	if err := json.Unmarshal(data, &raw); err != nil {
		return
	}

	next := d.anthropic
	if replace {
		next = anthropicPartialUsage{}
	}
	mergeInt64Pointer(&next.input, raw.InputTokens)
	mergeInt64Pointer(&next.output, raw.OutputTokens)
	mergeInt64Pointer(&next.cacheWrite, raw.CacheCreationInput)
	mergeInt64Pointer(&next.cacheRead, raw.CacheReadInput)
	mergeInt64Pointer(&next.reasoning, raw.OutputDetails.Thinking)

	normalizedRaw := anthropicUsage{
		InputTokens:        next.input,
		OutputTokens:       next.output,
		CacheCreationInput: next.cacheWrite,
		CacheReadInput:     next.cacheRead,
	}
	normalizedRaw.OutputDetails.Thinking = next.reasoning
	normalized := normalizeAnthropic(normalizedRaw)
	if !normalized.Present {
		return
	}
	d.anthropic = next
	d.latest = normalized
}

func mergeInt64Pointer(dst **int64, src *int64) {
	if src != nil {
		*dst = src
	}
}
