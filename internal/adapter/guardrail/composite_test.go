package guardrail

import (
	"context"
	"testing"
	"time"

	domainguardrail "github.com/ezequielranieri/agent-gateway/internal/domain/guardrail"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockClassifier implements ExternalClassifier for testing
type mockClassifier struct {
	name          string
	shouldFail    bool
	latency       time.Duration
	result        domainguardrail.ClassificationResult
	failBehavior  string
}

func newMockClassifier(name string, opts ...func(*mockClassifier)) *mockClassifier {
	m := &mockClassifier{
		name: name,
		result: domainguardrail.ClassificationResult{
			Provider:  name,
			Violated:  false,
			Categories: []domainguardrail.CategoryResult{},
		},
	}
	for _, opt := range opts {
		opt(m)
	}
	return m
}

func withViolation(cat string, detected bool, conf float64) func(*mockClassifier) {
	return func(m *mockClassifier) {
		m.result.Violated = true
		m.result.Categories = []domainguardrail.CategoryResult{
			{Category: cat, Detected: true, Confidence: 0.9, Threshold: 0.7},
		}
	}
}

func withLatency(d time.Duration) func(*mockClassifier) {
	return func(m *mockClassifier) {
		m.latency = d
	}
}

func withFailure() func(*mockClassifier) {
	return func(m *mockClassifier) {
		m.shouldFail = true
	}
}

func (m *mockClassifier) Name() string {
	return m.name
}

func (m *mockClassifier) ClassifyInput(ctx context.Context, text string) (domainguardrail.ClassificationResult, error) {
	if m.shouldFail {
		return domainguardrail.ClassificationResult{}, assert.AnError
	}
	if m.latency > 0 {
		select {
		case <-ctx.Done():
			return domainguardrail.ClassificationResult{}, ctx.Err()
		case <-time.After(m.latency):
		}
	}
	return m.result, nil
}

func (m *mockClassifier) ClassifyOutput(ctx context.Context, text string) (domainguardrail.ClassificationResult, error) {
	return m.ClassifyInput(ctx, text)
}

func (m *mockClassifier) HealthCheck(ctx context.Context) error {
	return nil
}

func (m *mockClassifier) Close() error {
	return nil
}

func TestCompositeGuardrail_LocalOnly(t *testing.T) {
	logger := zerolog.New(zerolog.NewTestWriter(t)).Level(zerolog.DebugLevel)

	local := newMockClassifier("local", withViolation("pii", true, 0.9))

	composite := NewCompositeGuardrail(local, nil, domainguardrail.CompositeConfig{
		Mode:               "sequential",
		FailBehavior:       "fallback_local",
		MergeLogic:         "any_violation",
		SendContentExternal: false,
	}, logger)

	ctx := context.Background()
	result, err := composite.CheckInput(ctx, "tenant-1", "test input")

	require.NoError(t, err)
	assert.True(t, result.Violated)
	assert.Equal(t, "composite", result.Provider)
	assert.Len(t, result.Categories, 1)
}

func TestCompositeGuardrail_ExternalOnly(t *testing.T) {
	logger := zerolog.New(zerolog.NewTestWriter(t)).Level(zerolog.DebugLevel)

	ext := newMockClassifier("openai", withViolation("sexual", true, 0.9))

	composite := NewCompositeGuardrail(nil, ext, domainguardrail.CompositeConfig{
		Mode:               "sequential",
		FailBehavior:       "fallback_local",
		MergeLogic:         "any_violation",
		SendContentExternal: true,
	}, logger)

	ctx := context.Background()
	result, err := composite.CheckInput(ctx, "tenant-1", "test input")

	require.NoError(t, err)
	assert.True(t, result.Violated)
	assert.Len(t, result.Categories, 1)
}

func TestCompositeGuardrail_BothClassifiersSequential(t *testing.T) {
	logger := zerolog.New(zerolog.NewTestWriter(t)).Level(zerolog.DebugLevel)

	local := newMockClassifier("local", withViolation("pii", true, 0.8))
	ext := newMockClassifier("openai", withViolation("sexual", true, 0.9))

	composite := NewCompositeGuardrail(local, ext, domainguardrail.CompositeConfig{
		Mode:               "sequential",
		FailBehavior:       "fallback_local",
		MergeLogic:         "any_violation",
		SendContentExternal: true,
	}, logger)

	ctx := context.Background()
	result, err := composite.CheckInput(ctx, "tenant-1", "test input")

	require.NoError(t, err)
	assert.True(t, result.Violated)
	assert.Len(t, result.Categories, 2) // Both violations should be present
}

func TestCompositeGuardrail_MergeLogicAnyViolation(t *testing.T) {
	logger := zerolog.New(zerolog.NewTestWriter(t)).Level(zerolog.DebugLevel)

	local := newMockClassifier("local") // No violation
	ext := newMockClassifier("openai", withViolation("sexual", true, 0.9))

	composite := NewCompositeGuardrail(local, ext, domainguardrail.CompositeConfig{
		Mode:               "sequential",
		FailBehavior:       "fallback_local",
		MergeLogic:         "any_violation",
		SendContentExternal: true,
	}, logger)

	ctx := context.Background()
	result, err := composite.CheckInput(ctx, "tenant-1", "test input")

	require.NoError(t, err)
	assert.True(t, result.Violated)
	assert.Len(t, result.Categories, 1)
}

func TestCompositeGuardrail_MergeLogicAllViolation(t *testing.T) {
	logger := zerolog.New(zerolog.NewTestWriter(t)).Level(zerolog.DebugLevel)

	local := newMockClassifier("local", withViolation("pii", true, 0.8))
	ext := newMockClassifier("openai") // No violation

	composite := NewCompositeGuardrail(local, ext, domainguardrail.CompositeConfig{
		Mode:               "sequential",
		FailBehavior:       "fallback_local",
		MergeLogic:         "all_violation",
		SendContentExternal: true,
	}, logger)

	ctx := context.Background()
	result, err := composite.CheckInput(ctx, "tenant-1", "test input")

	require.NoError(t, err)
	// all_violation requires ALL classifiers to detect violation
	assert.False(t, result.Violated)
}

func TestCompositeGuardrail_FailBehaviorFallbackLocal(t *testing.T) {
	logger := zerolog.New(zerolog.NewTestWriter(t)).Level(zerolog.DebugLevel)

	local := newMockClassifier("local", withViolation("pii", true, 0.9))
	ext := newMockClassifier("openai", withFailure())

	composite := NewCompositeGuardrail(local, ext, domainguardrail.CompositeConfig{
		Mode:               "sequential",
		FailBehavior:       "fallback_local",
		MergeLogic:         "any_violation",
		SendContentExternal: true,
	}, logger)

	ctx := context.Background()
	result, err := composite.CheckInput(ctx, "tenant-1", "test input")

	require.NoError(t, err)
	assert.True(t, result.Violated) // Should use local result
}

func TestCompositeGuardrail_FailBehaviorFailOpen(t *testing.T) {
	logger := zerolog.New(zerolog.NewTestWriter(t)).Level(zerolog.DebugLevel)

	ext := newMockClassifier("openai", withFailure())

	composite := NewCompositeGuardrail(nil, ext, domainguardrail.CompositeConfig{
		Mode:               "sequential",
		FailBehavior:       "fail_open",
		MergeLogic:         "any_violation",
		SendContentExternal: true,
	}, logger)

	ctx := context.Background()
	result, err := composite.CheckInput(ctx, "tenant-1", "test input")

	require.NoError(t, err)
	assert.False(t, result.Violated) // Should allow (fail open)
}

func TestCompositeGuardrail_FailBehaviorFailClosed(t *testing.T) {
	logger := zerolog.New(zerolog.NewTestWriter(t)).Level(zerolog.DebugLevel)

	ext := newMockClassifier("openai", withFailure())

	composite := NewCompositeGuardrail(nil, ext, domainguardrail.CompositeConfig{
		Mode:               "sequential",
		FailBehavior:       "fail_closed",
		MergeLogic:         "any_violation",
		SendContentExternal: true,
	}, logger)

	ctx := context.Background()
	_, err := composite.CheckInput(ctx, "tenant-1", "test input")

	require.Error(t, err)
	assert.ErrorIs(t, err, domainguardrail.ErrExternalClassifierUnavailable)
}

func TestCompositeGuardrail_ParallelMode(t *testing.T) {
	logger := zerolog.New(zerolog.NewTestWriter(t)).Level(zerolog.DebugLevel)

	local := newMockClassifier("local", withViolation("pii", true, 0.8))
	ext := newMockClassifier("openai", withViolation("sexual", true, 0.9), withLatency(50*time.Millisecond))

	composite := NewCompositeGuardrail(local, ext, domainguardrail.CompositeConfig{
		Mode:               "parallel",
		FailBehavior:       "fallback_local",
		MergeLogic:         "any_violation",
		ParallelBudgetMs:   500,
		SendContentExternal: true,
	}, logger)

	ctx := context.Background()
	result, err := composite.CheckInput(ctx, "tenant-1", "test input")

	require.NoError(t, err)
	assert.True(t, result.Violated)
	assert.Len(t, result.Categories, 2)
}

func TestCompositeGuardrail_ParallelBudgetExceeded(t *testing.T) {
	logger := zerolog.New(zerolog.NewTestWriter(t)).Level(zerolog.DebugLevel)

	local := newMockClassifier("local", withViolation("pii", true, 0.8))
	ext := newMockClassifier("openai", withViolation("sexual", true, 0.9), withLatency(2*time.Second))

	composite := NewCompositeGuardrail(local, ext, domainguardrail.CompositeConfig{
		Mode:               "parallel",
		FailBehavior:       "fallback_local",
		MergeLogic:         "any_violation",
		ParallelBudgetMs:   100, // Very short budget
		SendContentExternal: true,
	}, logger)

	ctx := context.Background()
	result, err := composite.CheckInput(ctx, "tenant-1", "test input")

	require.NoError(t, err)
	// Should fall back to local when parallel times out
	assert.True(t, result.Violated)
}

func TestCompositeGuardrail_MergeLogicWeighted(t *testing.T) {
	logger := zerolog.New(zerolog.NewTestWriter(t)).Level(zerolog.DebugLevel)

	local := newMockClassifier("local", withViolation("sexual", true, 0.6)) // Lower confidence
	ext := newMockClassifier("openai", withViolation("sexual", true, 0.9)) // Higher confidence

	composite := NewCompositeGuardrail(local, ext, domainguardrail.CompositeConfig{
		Mode:               "sequential",
		FailBehavior:       "fallback_local",
		MergeLogic:         "weighted",
		SendContentExternal: true,
	}, logger)

	ctx := context.Background()
	result, err := composite.CheckInput(ctx, "tenant-1", "test input")

	require.NoError(t, err)
	assert.True(t, result.Violated)

	// Find sexual category - should have higher confidence from external
	for _, cat := range result.Categories {
		if cat.Category == "sexual" {
			assert.Equal(t, 0.9, cat.Confidence) // Should keep higher confidence
		}
	}
}

func TestCompositeGuardrail_NoClassifiers(t *testing.T) {
	logger := zerolog.New(zerolog.NewTestWriter(t)).Level(zerolog.DebugLevel)

	composite := NewCompositeGuardrail(nil, nil, domainguardrail.CompositeConfig{
		SendContentExternal: false,
	}, logger)

	ctx := context.Background()
	_, err := composite.CheckInput(ctx, "tenant-1", "test input")

	require.Error(t, err)
	assert.ErrorIs(t, err, ErrNoClassifiersConfigured)
}