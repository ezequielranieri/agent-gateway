package model

// ChatRequest represents a chat completion request.
// Fields align with OpenAI-compatible API for broad compatibility.
type ChatRequest struct {
	// Model is the model identifier (required).
	Model string `json:"model"`

	// Messages is the conversation history.
	Messages []Message `json:"messages"`

	// Temperature controls randomness (0.0 to 2.0).
	Temperature *float64 `json:"temperature,omitempty"`

	// MaxTokens limits the response length.
	MaxTokens *int `json:"max_tokens,omitempty"`

	// TopP controls nucleus sampling.
	TopP *float64 `json:"top_p,omitempty"`

	// Tools available for the model to call.
	Tools []Tool `json:"tools,omitempty"`

	// ToolChoice controls tool calling behavior ("auto", "none", or specific tool).
	ToolChoice any `json:"tool_choice,omitempty"`

	// Stream enables SSE streaming response.
	Stream bool `json:"stream,omitempty"`

	// Stop sequences where the model should stop generating.
	Stop []string `json:"stop,omitempty"`

	// PresencePenalty penalizes new tokens based on presence.
	PresencePenalty *float64 `json:"presence_penalty,omitempty"`

	// FrequencyPenalty penalizes new tokens based on frequency.
	FrequencyPenalty *float64 `json:"frequency_penalty,omitempty"`

	// User identifier for abuse detection.
	User string `json:"user,omitempty"`
}

// Message represents a single message in the conversation.
type Message struct {
	// Role is the message author role (system, user, assistant, tool).
	Role string `json:"role"`

	// Content is the message content (text).
	Content string `json:"content,omitempty"`

	// ToolCalls are the tool calls made by the assistant.
	ToolCalls []ToolCall `json:"tool_calls,omitempty"`

	// ToolCallID is the ID of the tool call this message responds to.
	ToolCallID string `json:"tool_call_id,omitempty"`

	// Name is the name of the function/tool (for tool messages).
	Name string `json:"name,omitempty"`
}

// ToolCall represents a function call made by the model.
type ToolCall struct {
	// ID is the unique identifier for this tool call.
	ID string `json:"id"`

	// Type is the tool type (always "function" for now).
	Type string `json:"type"`

	// Function contains the function name and arguments.
	Function FunctionCall `json:"function"`
}

// FunctionCall represents a function invocation.
type FunctionCall struct {
	// Name is the function name.
	Name string `json:"name"`

	// Arguments is the JSON-encoded arguments.
	Arguments string `json:"arguments"`
}

// Tool represents a function/tool definition available to the model.
type Tool struct {
	// Type is the tool type (always "function" for now).
	Type string `json:"type"`

	// Function is the function definition.
	Function FunctionDef `json:"function"`
}

// FunctionDef defines a callable function.
type FunctionDef struct {
	// Name is the function name.
	Name string `json:"name"`

	// Description describes what the function does.
	Description string `json:"description,omitempty"`

	// Parameters is the JSON schema for function parameters.
	Parameters map[string]any `json:"parameters,omitempty"`
}

// Usage represents token usage for a completion.
type Usage struct {
	// PromptTokens is the number of tokens in the prompt.
	PromptTokens int `json:"prompt_tokens"`

	// CompletionTokens is the number of tokens in the completion.
	CompletionTokens int `json:"completion_tokens"`

	// TotalTokens is the sum of prompt and completion tokens.
	TotalTokens int `json:"total_tokens"`
}

// Choice represents a single completion choice.
type Choice struct {
	// Index is the choice index.
	Index int `json:"index"`

	// Message is the generated message.
	Message Message `json:"message"`

	// FinishReason indicates why the generation stopped.
	FinishReason string `json:"finish_reason"`
}

// ChatResponse represents the raw provider response.
// This is typically used internally by adapters before mapping to Completion.
type ChatResponse struct {
	// ID is the response identifier.
	ID string `json:"id"`

	// Object is the object type (e.g., "chat.completion").
	Object string `json:"object"`

	// Created is the Unix timestamp of creation.
	Created int64 `json:"created"`

	// Model is the model used.
	Model string `json:"model"`

	// Choices are the generated completions.
	Choices []Choice `json:"choices"`

	// Usage is the token usage.
	Usage Usage `json:"usage"`

	// SystemFingerprint is the backend fingerprint (OpenAI).
	SystemFingerprint string `json:"system_fingerprint,omitempty"`
}

// Completion is the normalized completion result returned by ModelProvider.Complete.
// It includes the response, usage, and provider metadata for routing/cost tracking.
type Completion struct {
	// Response is the chat response.
	Response ChatResponse

	// Provider is the provider name (e.g., "openai").
	Provider string

	// Model is the actual model used (may differ from request if fallback occurred).
	Model string

	// LatencyMs is the request latency in milliseconds.
	LatencyMs int64
}