package model

import (
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

type StopReason string

const (
	StopEndTurn       StopReason = "end_turn"
	StopMaxTokens     StopReason = "max_tokens"
	StopSequence      StopReason = "stop_sequence"
	StopToolUse       StopReason = "tool_use"
	StopContentFilter StopReason = "content_filter"
)

func (r StopReason) Validate() error {
	switch r {
	case StopEndTurn, StopMaxTokens, StopSequence, StopToolUse, StopContentFilter:
		return nil
	default:
		return fmt.Errorf("unsupported stop reason %q", r)
	}
}

type Usage struct {
	InputTokens           int64
	OutputTokens          int64
	CacheReadInputTokens  int64
	CacheWriteInputTokens int64
}

func (u Usage) TotalTokens() int64 { return u.InputTokens + u.OutputTokens }

func (u Usage) Validate() error {
	if u.InputTokens < 0 || u.OutputTokens < 0 || u.CacheReadInputTokens < 0 || u.CacheWriteInputTokens < 0 {
		return fmt.Errorf("usage token counts must not be negative")
	}
	return nil
}

type Response struct {
	ID         uuid.UUID
	Model      string
	Content    []ContentBlock
	StopReason StopReason
	Usage      Usage
	CreatedAt  time.Time
}

func (r Response) Validate() error {
	if r.ID == uuid.Nil || r.ID.Version() != uuid.Version(7) {
		return fmt.Errorf("response id must be UUIDv7")
	}
	if strings.TrimSpace(r.Model) == "" {
		return fmt.Errorf("response model is required")
	}
	if len(r.Content) == 0 && r.StopReason != StopMaxTokens && r.StopReason != StopContentFilter {
		return fmt.Errorf("response content is required")
	}
	for index := range r.Content {
		block := r.Content[index]
		if err := block.Validate(); err != nil {
			return fmt.Errorf("response content block %d: %w", index, err)
		}
		if block.Type != ContentText && block.Type != ContentToolCall && block.Type != ContentRefusal {
			return fmt.Errorf("response content block %d has unsupported type %q", index, block.Type)
		}
	}
	if err := r.StopReason.Validate(); err != nil {
		return err
	}
	if err := r.Usage.Validate(); err != nil {
		return err
	}
	if r.CreatedAt.IsZero() {
		return fmt.Errorf("response created time is required")
	}
	return nil
}
