package ai

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

var (
	ErrNotConfigured  = errors.New("AI provider is not configured")
	ErrRateLimited    = errors.New("AI provider rate limit exceeded")
	ErrUnavailable    = errors.New("AI provider is unavailable")
	ErrInvalidOutput  = errors.New("AI provider returned invalid structured output")
	ErrContextTooLong = errors.New("AI input exceeds the provider context limit")
)

type Request struct {
	RunID            string
	Model            string
	Instructions     string
	Input            json.RawMessage
	OutputSchema     json.RawMessage
	OutputSchemaName string
	MaxOutputTokens  int
}

type Usage struct {
	InputTokens  int `json:"inputTokens"`
	OutputTokens int `json:"outputTokens"`
	TotalTokens  int `json:"totalTokens"`
}

type Result struct {
	Provider   string          `json:"provider"`
	Model      string          `json:"model"`
	ResponseID string          `json:"responseId,omitempty"`
	Output     json.RawMessage `json:"output"`
	Usage      *Usage          `json:"usage,omitempty"`
	Duration   time.Duration   `json:"duration"`
}

type Provider interface {
	Generate(context.Context, Request) (Result, error)
}
