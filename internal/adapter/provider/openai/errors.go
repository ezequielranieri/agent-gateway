package openai

import (
	"net/http"

	"github.com/ezequielranieri/agent-gateway/internal/domain/model"
)

// MapError maps OpenAI HTTP status codes and error responses to domain sentinel errors
func MapError(statusCode int, errMsg string) error {
	switch statusCode {
	case http.StatusUnauthorized:
		return model.ErrProviderAuthFailed
	case http.StatusTooManyRequests:
		return model.ErrProviderRateLimited
	case http.StatusNotFound:
		if contains(errMsg, "model") {
			return model.ErrProviderInvalidRequest // model not found is an invalid request
		}
		return model.ErrProviderInvalidRequest
	case http.StatusBadRequest:
		if contains(errMsg, "context_length_exceeded") || contains(errMsg, "maximum context length") {
			return model.ErrProviderInvalidRequest
		}
		return model.ErrProviderInvalidRequest
	case http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return model.ErrProviderUnavailable
	default:
		return model.ErrProviderUnavailable
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > len(substr) && findSubstring(s, substr))
}

func findSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}