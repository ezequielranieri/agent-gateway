package external

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ezequielranieri/agent-gateway/internal/domain/guardrail"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOpenAIClassifier_ClassifyInput(t *testing.T) {
	// Create test server that mimics OpenAI Moderation API
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/v1/moderations", r.URL.Path)
		assert.Equal(t, "POST", r.Method)

		var req map[string]interface{}
		err := json.NewDecoder(r.Body).Decode(&req)
require.NoError(t, err)
		assert.Equal(t, "test input", req["input"])

	resp := OpenAIModerationResponse{
		ID:    "test-id",
		Model: "omni-moderation-latest",
		Results: []struct {
			Flagged        bool                   `json:"flagged"`
			Categories     map[string]bool        `json:"categories"`
			CategoryScores map[string]float64     `json:"category_scores"`
		}{
			{
				Flagged: true,
				Categories: map[string]bool{
					"sexual": true,
					"hate":    false,
				},
				CategoryScores: map[string]float64{
					"sexual": 0.9,
					"hate":   0.1,
				},
			},
},
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
	}));
	defer server.Close()

	// Create classifier with test server
	config := OpenAIConfig{
		APIKey:    "test-key",
		Endpoint:  server.URL + "/v1/moderations",
		Model:     "omni-moderation-latest",
		Timeout:   5 * time.Second,
		Thresholds: map[string]float64{
			"sexual": 0.7,
			"hate":   0.8,
		},
	}

	classifier, err := NewOpenAIClassifier(config, zerolog.New(zerolog.NewTestWriter(t)))
	require.NoError(t, err)
	defer classifier.Close()

	// Test classification
	ctx := context.Background()
	result, err := classifier.ClassifyInput(ctx, "test input")

	require.NoError(t, err)
	assert.True(t, result.Violated)
	assert.Equal(t, "openai", result.Provider)
	assert.Equal(t, "omni-moderation-latest", result.Model)
	assert.Len(t, result.Categories, 2)

	// Check sexual category
	sexualCat := findCategory(result.Categories, "sexual")
	require.NotNil(t, sexualCat)
	assert.True(t, sexualCat.Detected)
	assert.GreaterOrEqual(t, sexualCat.Confidence, 0.7)
	assert.Equal(t, 0.7, sexualCat.Threshold)

	// Check hate category
	hateCat := findCategory(result.Categories, "hate")
	require.NotNil(t, hateCat)
	assert.False(t, hateCat.Detected)
}

func TestAnthropicClassifier_ClassifyInput(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/v1/messages", r.URL.Path)
		assert.Equal(t, "POST", r.Method)

		var req AnthropicRequest
		err := json.NewDecoder(r.Body).Decode(&req)
		require.NoError(t, err)
		assert.Contains(t, req.Messages[0].Content, "test input")

		// Return classifier response with violations
		response := `{
			"violations": [
				{"category": "sexual", "detected": true, "confidence": 0.9},
				{"category": "hate", "detected": false, "confidence": 0.1}
			]
		}`

resp := AnthropicResponse{
		Content: []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}{{Type: "text", Text: response}},
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
	}));
	defer server.Close()

	config := AnthropicConfig{
		APIKey:    "test-key",
		Endpoint:  server.URL + "/v1/messages",
		Model:     "claude-3-5-haiku-20241022",
		Timeout:   10 * time.Second,
		Thresholds: map[string]float64{
			"sexual": 0.7,
			"hate":   0.8,
		},
	}

	classifier, err := NewAnthropicClassifier(config, zerolog.New(zerolog.NewTestWriter(t)))
	require.NoError(t, err)
	defer classifier.Close()

	ctx := context.Background()
	result, err := classifier.ClassifyInput(ctx, "test input")

	require.NoError(t, err)
	assert.Equal(t, "anthropic", result.Provider)
	assert.True(t, result.Violated)
	assert.Len(t, result.Categories, 2)
}

func TestLlamaGuardClassifier_ClassifyInput(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/chat", r.URL.Path)
		assert.Equal(t, "POST", r.Method)

		var req OllamaChatRequest
		err := json.NewDecoder(r.Body).Decode(&req)
		require.NoError(t, err)
		assert.Contains(t, req.Messages[0].Content, "test input")

		// Return unsafe response
		resp := OllamaChatResponse{
			Model:     "llama-guard-3",
			CreatedAt: time.Now().Format(time.RFC3339),
			Message:   OllamaMessage{Role: "assistant", Content: "unsafe\nS1, H1"},
			Done:      true,
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}));
	defer server.Close()

	config := LlamaGuardConfig{
		Endpoint:    server.URL,
		Model:       "llama-guard-3",
		Timeout:     15 * time.Second,
		Thresholds: map[string]float64{
			"S1": 0.5,
			"H1": 0.5,
		},
	}

	classifier, err := NewLlamaGuardClassifier(config, zerolog.New(zerolog.NewTestWriter(t)))
	require.NoError(t, err)
	defer classifier.Close()

	ctx := context.Background()
	result, err := classifier.ClassifyInput(ctx, "test input")

	require.NoError(t, err)
	assert.Equal(t, "llamaguard", result.Provider)
	assert.True(t, result.Violated)
	assert.Len(t, result.Categories, 2)
}

func TestLocalClassifier_ClassifyInput(t *testing.T) {
	config := LocalClassifierConfig{
		Enabled:   true,
		Patterns:  []string{`(?i)password\s*[:=]\s*\S+`, `(?i)secret\s*[:=]\s*\S+`},
		Threshold: 0.7,
	}

	classifier, err := NewLocalClassifier(config, zerolog.New(zerolog.NewTestWriter(t)))
	require.NoError(t, err)

	ctx := context.Background()

	// Test with sensitive content
	result, err := classifier.ClassifyInput(ctx, "password = secret123")
	require.NoError(t, err)
	assert.True(t, result.Violated)
	assert.Equal(t, "local", result.Provider)

	// Test without sensitive content
	result2, err := classifier.ClassifyInput(ctx, "hello world")
	require.NoError(t, err)
	assert.False(t, result2.Violated)
}

func TestGuardrailHTTPClient_Retry(t *testing.T) {
	attemptCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attemptCount++
		if attemptCount < 2 {
			// Fail first attempt
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		// Succeed on second attempt
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	}));
	defer server.Close()

	client := NewGuardrailHTTPClient(HTTPClientConfig{
		Timeout:        5 * time.Second,
		Retry:          RetryConfig{MaxAttempts: 1, Backoff: 10 * time.Millisecond},
		CircuitBreaker: CircuitBreakerConfig{FailureThreshold: 5, Window: 30 * time.Second, ResetTimeout: 60 * time.Second},
	}, zerolog.New(zerolog.NewTestWriter(t)))

	ctx := context.Background()
	req, _ := http.NewRequestWithContext(context.Background(), "GET", server.URL, nil)

	resp, err := client.Do(ctx, req)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, 2, attemptCount) // Original + 1 retry
}

func TestCircuitBreaker(t *testing.T) {
	cb := NewCircuitBreaker(CircuitBreakerConfig{
		FailureThreshold: 3,
		Window:           30 * time.Second,
		ResetTimeout:     100 * time.Millisecond,
	}, zerolog.New(zerolog.NewTestWriter(t)))

	// First 3 failures should open the circuit
	for i := 0; i < 3; i++ {
		err := cb.Execute(context.Background(), func() error {
			return assert.AnError
		})
		assert.Error(t, err)
	}

	// Circuit should now be open
	assert.Equal(t, CircuitBreakerOpen, cb.State())

	// Requests should be rejected
	err := cb.Execute(context.Background(), func() error {
		return nil
	})
	assert.Error(t, err)
	assert.ErrorIs(t, err, ErrCircuitBreakerOpen)

	// Wait for reset timeout
	time.Sleep(150 * time.Millisecond)

	// Trigger state transition by attempting a request
	err = cb.Execute(context.Background(), func() error {
		return nil
	})
	assert.NoError(t, err)

	// Should be half-open now
	assert.Equal(t, CircuitBreakerHalfOpen, cb.State())

	// One more successful request should close the circuit
	err = cb.Execute(context.Background(), func() error {
		return nil
	})
	assert.NoError(t, err)

	assert.Equal(t, CircuitBreakerClosed, cb.State())
}

func findCategory(categories []guardrail.CategoryResult, name string) *guardrail.CategoryResult {
	for i := range categories {
		if categories[i].Category == name {
			return &categories[i]
		}
	}
	return nil
}