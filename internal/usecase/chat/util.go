package chat

import (
	"github.com/ezequielranieri/agent-gateway/internal/domain/model"
)

// estimatePromptTokens roughly estimates prompt tokens
func estimatePromptTokens(messages []model.Message) int {
	totalChars := 0
	for _, msg := range messages {
		totalChars += len(msg.Content)
		for _, tc := range msg.ToolCalls {
			totalChars += len(tc.Function.Name) + len(tc.Function.Arguments)
		}
	}
	// Rough estimate: ~4 chars per token
	tokens := totalChars / 4
	if tokens == 0 {
		tokens = 100
	}
	return tokens
}