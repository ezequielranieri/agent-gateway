package guardrail

import (
	"time"
)

// ToolCall represents a single tool invocation request
type ToolCall struct {
	ID        string         `json:"id"`
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments"`
}

// ToolResult represents the result of a tool execution
type ToolResult struct {
	CallID   string         `json:"call_id"`
	Output   map[string]any `json:"output,omitempty"`
	Error    string         `json:"error,omitempty"`
	Duration time.Duration  `json:"duration_ms"`
}

// Tool represents a function/tool definition available to the model
type Tool struct {
	Type     string      `json:"type"`
	Function FunctionDef `json:"function"`
}

type FunctionDef struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Parameters  map[string]any `json:"parameters,omitempty"`
}

type ToolChoice struct {
	Type     string `json:"type"`
	Name     string `json:"name,omitempty"`
	Function string `json:"function,omitempty"`
}

type ChatRequest struct {
	Model            string      `json:"model"`
	Messages         []Message   `json:"messages"`
	Temperature      *float64    `json:"temperature,omitempty"`
	MaxTokens        *int        `json:"max_tokens,omitempty"`
	TopP             *float64    `json:"top_p,omitempty"`
	Tools            []Tool      `json:"tools,omitempty"`
	ToolChoice       interface{} `json:"tool_choice,omitempty"`
	Stream           bool        `json:"stream,omitempty"`
	Stop             []string    `json:"stop,omitempty"`
	PresencePenalty  *float64    `json:"presence_penalty,omitempty"`
	FrequencyPenalty *float64    `json:"frequency_penalty,omitempty"`
	User             string      `json:"user,omitempty"`
}

type Message struct {
	Role       string     `json:"role"`
	Content    string     `json:"content"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
	Name       string     `json:"name,omitempty"`
}

type FunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type ChatResponse struct {
	ID                string         `json:"id"`
	Object            string         `json:"object"`
	Created           int64          `json:"created"`
	Model             string         `json:"model"`
	Choices           []Choice       `json:"choices"`
	Usage             Usage          `json:"usage"`
	SystemFingerprint string         `json:"system_fingerprint,omitempty"`
}

type Choice struct {
	Index        int         `json:"index"`
	Message      Message     `json:"message"`
	FinishReason string      `json:"finish_reason"`
}

type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

type FallbackResult struct {
	Completion       Completion
	Provider         string
	Model            string
	Attempt          int
	LatencyMs        int64
	Retried          bool
	FallbackReason   string
}

type Completion struct {
	Response ChatResponse
	Provider string
	Model    string
	LatencyMs int64
}