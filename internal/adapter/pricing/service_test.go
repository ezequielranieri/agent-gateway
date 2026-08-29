package pricing

import (
	"context"
	"testing"

	"github.com/ezequielranieri/agent-gateway/internal/domain/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewService(t *testing.T) {
	cfg := DefaultPricingConfig()
	s, err := NewService(cfg)

	require.NoError(t, err)
	assert.NotNil(t, s)
	assert.Equal(t, "2024-01", s.CurrentVersion())
}

func TestNewService_DefaultVersionValidation(t *testing.T) {
	cfg := model.PricingConfig{
		DefaultVersion: "nonexistent",
		Tables: []model.PriceTable{
			{Version: "2024-01", Provider: "openai", Prices: []model.ModelPrice{{Model: "gpt-4", InputPricePer1k: 0.03, OutputPricePer1k: 0.06}}},
		},
	}

	_, err := NewService(cfg)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "default pricing version not found")
}

func TestService_GetCost_Success(t *testing.T) {
	s := NewTestService()

	cost, version, err := s.GetCost(context.Background(), "gpt-4", 1000, 500)

	require.NoError(t, err)
	assert.Equal(t, "2024-01", version)
	// 1000 input tokens * 0.03/1000 + 500 output tokens * 0.06/1000 = 0.03 + 0.03 = 0.06
	assert.InDelta(t, 0.06, cost, 0.0001)
}

func TestService_GetCost_AnthropicModel(t *testing.T) {
	s := NewTestService()

	cost, version, err := s.GetCost(context.Background(), "claude-3-opus-20240229", 1000, 500)

	require.NoError(t, err)
	assert.Equal(t, "2024-01", version)
	// 1000 * 0.015/1000 + 500 * 0.075/1000 = 0.015 + 0.0375 = 0.0525
	assert.InDelta(t, 0.0525, cost, 0.0001)
}

func TestService_GetCost_OllamaModel(t *testing.T) {
	s := NewTestService()

	cost, version, err := s.GetCost(context.Background(), "llama3", 10000, 5000)

	require.NoError(t, err)
	assert.Equal(t, "2024-01", version)
	// Ollama models are free
	assert.Equal(t, 0.0, cost)
}

func TestService_GetCost_ModelNotFound(t *testing.T) {
	s := NewTestService()

	_, _, err := s.GetCost(context.Background(), "nonexistent-model", 100, 100)

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "model not found in pricing table")
}

func TestService_GetModelPrice_Success(t *testing.T) {
	s := NewTestService()

	inputPrice, outputPrice, version, err := s.GetModelPrice(context.Background(), "gpt-3.5-turbo")

	require.NoError(t, err)
	assert.Equal(t, "2024-01", version)
	assert.Equal(t, 0.0005, inputPrice)
	assert.Equal(t, 0.0015, outputPrice)
}

func TestService_GetModelPrice_ModelNotFound(t *testing.T) {
	s := NewTestService()

	_, _, _, err := s.GetModelPrice(context.Background(), "nonexistent-model")

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "model not found in pricing table")
}

func TestService_ListVersions(t *testing.T) {
	s := NewTestService()

	versions, err := s.ListVersions(context.Background())

	require.NoError(t, err)
	assert.Contains(t, versions, "2024-01")
}

func TestService_SetVersion_Success(t *testing.T) {
	s := NewTestService()

	err := s.SetVersion("2024-01")

	assert.NoError(t, err)
	assert.Equal(t, "2024-01", s.CurrentVersion())
}

func TestService_SetVersion_NotFound(t *testing.T) {
	s := NewTestService()

	err := s.SetVersion("nonexistent")

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "pricing version not found")
}

func TestService_CurrentVersion(t *testing.T) {
	s := NewTestService()

	assert.Equal(t, "2024-01", s.CurrentVersion())
}

func TestService_GetTable(t *testing.T) {
	s := NewTestService()

	table, err := s.GetTable("2024-01", "openai")

	require.NoError(t, err)
	assert.Equal(t, "2024-01", table.Version)
	assert.Equal(t, "openai", table.Provider)
	assert.Len(t, table.Prices, 3)
}

func TestService_GetTable_NotFound(t *testing.T) {
	s := NewTestService()

	_, err := s.GetTable("2024-01", "nonexistent")

	assert.Error(t, err)
}

func TestService_LoadTableFromJSON(t *testing.T) {
	s := NewTestService()

	newTableJSON := `{
		"version": "2024-06",
		"provider": "openai",
		"effective_date": "2024-06-01",
		"description": "Updated pricing",
		"prices": [
			{"model": "gpt-4o", "input_price_per_1k": 0.005, "output_price_per_1k": 0.015}
		]
	}`

	err := s.LoadTableFromJSON([]byte(newTableJSON))

	require.NoError(t, err)

	// Verify new version is available
	versions, _ := s.ListVersions(context.Background())
	assert.Contains(t, versions, "2024-06")

	// Verify new model pricing
	table, err := s.GetTable("2024-06", "openai")
	require.NoError(t, err)
	assert.Len(t, table.Prices, 1)
	assert.Equal(t, "gpt-4o", table.Prices[0].Model)
	assert.Equal(t, 0.005, table.Prices[0].InputPricePer1k)
}

func TestService_GetCostForModel_Convenience(t *testing.T) {
	s := NewTestService()

	cost, version, err := s.GetCostForModel("gpt-4", 1000, 500)

	require.NoError(t, err)
	assert.Equal(t, "2024-01", version)
	assert.InDelta(t, 0.06, cost, 0.0001)
}

func TestService_GetModelPriceSync_Convenience(t *testing.T) {
	s := NewTestService()

	inputPrice, outputPrice, version, err := s.GetModelPriceSync("gpt-4")

	require.NoError(t, err)
	assert.Equal(t, "2024-01", version)
	assert.Equal(t, 0.03, inputPrice)
	assert.Equal(t, 0.06, outputPrice)
}

func TestService_WithOptions(t *testing.T) {
	s := NewTestService(
		WithDefaultVersion("2024-01"),
		WithTable(&PriceTable{
			Version:     "2024-06",
			Provider:    "openai",
			EffectiveDate: "2024-06-01",
			Prices: []ModelPriceEntry{
				{Model: "gpt-4o", InputPricePer1k: 0.005, OutputPricePer1k: 0.015},
			},
		}),
	)

	assert.Equal(t, "2024-01", s.CurrentVersion())

	versions, _ := s.ListVersions(context.Background())
	assert.Contains(t, versions, "2024-06")

	table, _ := s.GetTable("2024-06", "openai")
	assert.Len(t, table.Prices, 1)
	assert.Equal(t, "gpt-4o", table.Prices[0].Model)
}

func TestDefaultPricingConfig(t *testing.T) {
	cfg := DefaultPricingConfig()

	assert.Equal(t, "2024-01", cfg.DefaultVersion)
	assert.False(t, cfg.AutoUpdate)
	assert.Len(t, cfg.Tables, 3)

	// Check OpenAI table
	openaiTable := cfg.Tables[0]
	assert.Equal(t, "openai", openaiTable.Provider)
	assert.Len(t, openaiTable.Prices, 3)

	// Check Anthropic table
	anthropicTable := cfg.Tables[1]
	assert.Equal(t, "anthropic", anthropicTable.Provider)
	assert.Len(t, anthropicTable.Prices, 3)

	// Check Ollama table
	ollamaTable := cfg.Tables[2]
	assert.Equal(t, "ollama", ollamaTable.Provider)
	assert.Len(t, ollamaTable.Prices, 3)
}

func TestPricingCalculations(t *testing.T) {
	s := NewTestService()

	testCases := []struct {
		name           string
		model          string
		promptTokens   int
		completionTokens int
		expectedCost   float64
	}{
		{"gpt-4 small", "gpt-4", 100, 50, 0.006},       // 100*0.03/1000 + 50*0.06/1000 = 0.003 + 0.003 = 0.006
		{"gpt-4 large", "gpt-4", 10000, 5000, 0.6},    // 10000*0.03/1000 + 5000*0.06/1000 = 0.3 + 0.3 = 0.6
		{"gpt-4-turbo", "gpt-4-turbo", 1000, 1000, 0.04}, // 1000*0.01/1000 + 1000*0.03/1000 = 0.01 + 0.03 = 0.04
		{"gpt-3.5-turbo", "gpt-3.5-turbo", 10000, 5000, 0.0125}, // 10000*0.0005/1000 + 5000*0.0015/1000 = 0.005 + 0.0075 = 0.0125
		{"claude-opus", "claude-3-opus-20240229", 1000, 500, 0.0525},
		{"claude-sonnet", "claude-3-sonnet-20240229", 1000, 500, 0.0105},
		{"claude-haiku", "claude-3-haiku-20240307", 1000, 500, 0.000875},
		{"ollama free", "llama3", 100000, 50000, 0.0},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			cost, _, err := s.GetCost(context.Background(), tc.model, tc.promptTokens, tc.completionTokens)
			require.NoError(t, err)
			assert.InDelta(t, tc.expectedCost, cost, 0.0001, "model: %s", tc.model)
		})
	}
}