package guardrail

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/knadh/koanf/v2"
	"github.com/rs/zerolog"

	"github.com/ezequielranieri/agent-gateway/internal/config"
	"github.com/ezequielranieri/agent-gateway/internal/domain"
)

// LocalGuardrail implements the Guardrail interface with local regex/wordlist rules
type LocalGuardrail struct {
	cfg         config.GuardrailsConfig
	rules       []GuardrailRule
	compiled    []CompiledRule
	piiRegexes  map[string]*regexp.Regexp
	injectionPatterns []string
	logger      zerolog.Logger
	enabled     bool
}

// GuardrailRule represents a configured guardrail rule (legacy custom rules)
type GuardrailRule struct {
	Name     string   `koanf:"name"`
	Type     string   `koanf:"type"`     // regex, wordlist, pii, injection
	Pattern  string   `koanf:"pattern"`
	Words    []string `koanf:"words"`
	Severity string   `koanf:"severity"` // info, warn, critical
	Phase    string   `koanf:"phase"`    // input, output, both
}

// CompiledRule is a pre-compiled rule for efficient matching
type CompiledRule struct {
	Name     string
	Type     string
	Regex    *regexp.Regexp
	Words    []string
	Severity string
	Phase    string
}

// NewLocalGuardrail creates a new LocalGuardrail from config
func NewLocalGuardrail(cfg config.GuardrailsConfig, logger zerolog.Logger) *LocalGuardrail {
	lg := &LocalGuardrail{
		cfg:    cfg,
		logger: logger.With().Str("component", "local_guardrail").Logger(),
		enabled: cfg.Enabled,
		piiRegexes: map[string]*regexp.Regexp{
			"email":       regexp.MustCompile(`[a-zA-Z0-9._%+-]+@[a-zA-Z0-9.-]+\.[a-zA-Z]{2,}`),
			"credit_card": regexp.MustCompile(`\b(?:\d[ -]*?){13,16}\b`),
			"ssn":         regexp.MustCompile(`\b\d{3}-\d{2}-\d{4}\b`),
		},
		injectionPatterns: []string{
			"ignore previous",
			"system prompt",
			"you are now",
			"DAN",
			"jailbreak",
			"roleplay",
			"forget everything",
			"new instructions",
			"ignore all",
			"disregard",
			"override",
			"pretend to be",
			"act as",
			"you must",
			"you will",
			"do anything now",
		},
	}

	// Merge config injection patterns with built-in ones
	if len(cfg.InjectionPatterns) > 0 {
		lg.injectionPatterns = append(lg.injectionPatterns, cfg.InjectionPatterns...)
	}

	lg.compileRules()
	return lg
}

// NewLocalGuardrailFromKoanf creates a LocalGuardrail from koanf config
func NewLocalGuardrailFromKoanf(k *koanf.Koanf, logger zerolog.Logger) (*LocalGuardrail, error) {
	var cfg config.GuardrailsConfig
	if err := k.Unmarshal("guardrails", &cfg); err != nil {
		return nil, fmt.Errorf("failed to unmarshal guardrails config: %w", err)
	}
	return NewLocalGuardrail(cfg, logger), nil
}

func (g *LocalGuardrail) compileRules() {
	g.compiled = make([]CompiledRule, 0, len(g.cfg.Rules))

	for _, rule := range g.cfg.Rules {
		cr := CompiledRule{
			Name:     rule.Name,
			Type:     rule.Type,
			Words:    rule.Words,
			Severity: rule.Severity,
			Phase:    rule.Phase,
		}

		switch rule.Type {
		case "regex":
			if rule.Pattern != "" {
				re, err := regexp.Compile(rule.Pattern)
				if err != nil {
					g.logger.Warn().Str("rule", rule.Name).Err(err).Msg("Failed to compile regex pattern")
				} else {
					cr.Regex = re
				}
			}
		case "wordlist":
			// Words are used directly for substring matching
		case "pii":
			// Use built-in PII regexes
		case "injection":
			// Use built-in injection patterns
		}

		g.compiled = append(g.compiled, cr)
	}
}

// CheckInput validates input before sending to model
// Returns first violation found (fail-closed for input)
func (g *LocalGuardrail) CheckInput(ctx context.Context, tenantID domain.UUID, input string) (*domain.GuardrailViolation, error) {
	if !g.enabled {
		return nil, nil
	}

	// Check length limit
	maxInput := g.cfg.LengthLimits.MaxInputChars
	if maxInput <= 0 {
		maxInput = 100000
	}
	if violation := g.checkLength(input, "input", maxInput); violation != nil {
		violation.TenantID = tenantID
		return violation, nil
	}

	// Check custom rules first
	if violation := g.checkRules(input, "input"); violation != nil {
		violation.TenantID = tenantID
		return violation, nil
	}

	// Check built-in PII patterns (critical for input)
	if g.cfg.PIIPatterns.Email || g.cfg.PIIPatterns.CreditCard || g.cfg.PIIPatterns.SSN {
		if violation := g.checkPII(input, "input"); violation != nil {
			violation.TenantID = tenantID
			return violation, nil
		}
	}

	// Check built-in injection patterns (critical for input)
	if violation := g.checkInjection(input, "input"); violation != nil {
		violation.TenantID = tenantID
		return violation, nil
	}

	return nil, nil
}

// CheckOutput validates output from model
// Returns violation (critical=reject, warn=sanitize)
func (g *LocalGuardrail) CheckOutput(ctx context.Context, tenantID domain.UUID, output string) (*domain.GuardrailViolation, error) {
	if !g.enabled {
		return nil, nil
	}

	// Check length limit
	maxOutput := g.cfg.LengthLimits.MaxOutputChars
	if maxOutput <= 0 {
		maxOutput = 200000
	}
	if violation := g.checkLength(output, "output", maxOutput); violation != nil {
		violation.TenantID = tenantID
		return violation, nil
	}

	// Check custom rules
	if violation := g.checkRules(output, "output"); violation != nil {
		violation.TenantID = tenantID
		return violation, nil
	}

	// Check PII in output (warn severity - sanitize)
	if g.cfg.PIIPatterns.Email || g.cfg.PIIPatterns.CreditCard || g.cfg.PIIPatterns.SSN {
		if violation := g.checkPII(output, "output"); violation != nil {
			violation.TenantID = tenantID
			return violation, nil
		}
	}

	return nil, nil
}

// checkLength validates text length
func (g *LocalGuardrail) checkLength(text, phase string, maxLen int) *domain.GuardrailViolation {
	if len(text) > maxLen {
		return &domain.GuardrailViolation{
			ID:        domain.NewUUID(),
			TenantID:  domain.NewUUID(), // Will be set by caller
			Phase:     domain.GuardrailPhase(phase),
			Rule:      "length_limit",
			Severity:  "critical",
			Message:   fmt.Sprintf("%s exceeds maximum length of %d characters", phase, maxLen),
			Context:   fmt.Sprintf(`{"length":%d,"max":%d}`, len(text), maxLen),
			CreatedAt: domain.Now(),
		}
	}
	return nil
}

// checkRules checks custom configured rules
func (g *LocalGuardrail) checkRules(text, phase string) *domain.GuardrailViolation {
	for _, rule := range g.compiled {
		// Skip if rule doesn't apply to this phase
		if rule.Phase != "both" && rule.Phase != phase {
			continue
		}

		var matched bool
		var matchContext string

		switch rule.Type {
		case "regex":
			if rule.Regex != nil {
				matches := rule.Regex.FindStringSubmatch(text)
				if len(matches) > 0 {
					matched = true
					matchContext = fmt.Sprintf(`{"matched":"%s"}`, strings.ReplaceAll(matches[0], `"`, `\"`))
				}
			}
		case "wordlist":
			for _, word := range rule.Words {
				if strings.Contains(strings.ToLower(text), strings.ToLower(word)) {
					matched = true
					matchContext = fmt.Sprintf(`{"matched_word":"%s"}`, word)
					break
				}
			}
		}

		if matched {
			return &domain.GuardrailViolation{
				ID:        domain.NewUUID(),
				TenantID:  domain.NewUUID(), // Will be set by caller
				Phase:     domain.GuardrailPhase(phase),
				Rule:      rule.Name,
				Severity:  rule.Severity,
				Message:   fmt.Sprintf("Guardrail rule '%s' triggered", rule.Name),
				Context:   matchContext,
				CreatedAt: domain.Now(),
			}
		}
	}
	return nil
}

// checkPII checks for PII patterns
func (g *LocalGuardrail) checkPII(text, phase string) *domain.GuardrailViolation {
	// For input: critical, for output: warn
	severity := "critical"
	if phase == "output" {
		severity = "warn"
	}

	// Check each enabled PII pattern
	if g.cfg.PIIPatterns.Email {
		if matches := g.piiRegexes["email"].FindStringSubmatch(text); len(matches) > 0 {
			return &domain.GuardrailViolation{
				ID:        domain.NewUUID(),
				TenantID:  domain.NewUUID(),
				Phase:     domain.GuardrailPhase(phase),
				Rule:      "pii.email",
				Severity:  severity,
				Message:   "PII detected: email",
				Context:   fmt.Sprintf(`{"matched":"%s"}`, strings.ReplaceAll(matches[0], `"`, `\"`)),
				CreatedAt: domain.Now(),
			}
		}
	}

	if g.cfg.PIIPatterns.CreditCard {
		if matches := g.piiRegexes["credit_card"].FindStringSubmatch(text); len(matches) > 0 {
			return &domain.GuardrailViolation{
				ID:        domain.NewUUID(),
				TenantID:  domain.NewUUID(),
				Phase:     domain.GuardrailPhase(phase),
				Rule:      "pii.credit_card",
				Severity:  severity,
				Message:   "PII detected: credit card",
				Context:   fmt.Sprintf(`{"matched":"%s"}`, strings.ReplaceAll(matches[0], `"`, `\"`)),
				CreatedAt: domain.Now(),
			}
		}
	}

	if g.cfg.PIIPatterns.SSN {
		if matches := g.piiRegexes["ssn"].FindStringSubmatch(text); len(matches) > 0 {
			return &domain.GuardrailViolation{
				ID:        domain.NewUUID(),
				TenantID:  domain.NewUUID(),
				Phase:     domain.GuardrailPhase(phase),
				Rule:      "pii.ssn",
				Severity:  severity,
				Message:   "PII detected: SSN",
				Context:   fmt.Sprintf(`{"matched":"%s"}`, strings.ReplaceAll(matches[0], `"`, `\"`)),
				CreatedAt: domain.Now(),
			}
		}
	}

	return nil
}

// checkInjection checks for prompt injection patterns
func (g *LocalGuardrail) checkInjection(text, phase string) *domain.GuardrailViolation {
	// Injection patterns only apply to input
	if phase != "input" {
		return nil
	}

	lowerText := strings.ToLower(text)
	for _, pattern := range g.injectionPatterns {
		if strings.Contains(lowerText, strings.ToLower(pattern)) {
			ruleName := "injection." + strings.ReplaceAll(strings.ReplaceAll(pattern, " ", "_"), "-", "_")
			return &domain.GuardrailViolation{
				ID:        domain.NewUUID(),
				TenantID:  domain.NewUUID(),
				Phase:     domain.GuardrailPhase(phase),
				Rule:      ruleName,
				Severity:  "critical",
				Message:   fmt.Sprintf("Prompt injection detected: %s", pattern),
				Context:   fmt.Sprintf(`{"pattern":"%s"}`, pattern),
				CreatedAt: domain.Now(),
			}
		}
	}
	return nil
}

// SanitizeOutput removes or masks sensitive data from output
func (g *LocalGuardrail) SanitizeOutput(output string) string {
	if !g.cfg.PIIPatterns.Email && !g.cfg.PIIPatterns.CreditCard && !g.cfg.PIIPatterns.SSN {
		return output
	}

	// Mask emails
	if g.cfg.PIIPatterns.Email {
		output = g.piiRegexes["email"].ReplaceAllStringFunc(output, func(match string) string {
			parts := strings.Split(match, "@")
			if len(parts) == 2 {
				return parts[0][:min(3, len(parts[0]))] + "***" + "@" + parts[1]
			}
			return "***@***"
		})
	}

	// Mask credit cards
	if g.cfg.PIIPatterns.CreditCard {
		output = g.piiRegexes["credit_card"].ReplaceAllString(output, "XXXX-XXXX-XXXX-XXXX")
	}

	// Mask SSN
	if g.cfg.PIIPatterns.SSN {
		output = g.piiRegexes["ssn"].ReplaceAllString(output, "XXX-XX-XXXX")
	}

	return output
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// MarshalJSON implements custom JSON marshaling
func (g *LocalGuardrail) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Enabled      bool   `json:"enabled"`
		RulesCount   int    `json:"rules_count"`
		PIIPatterns  int    `json:"pii_patterns_enabled"`
	}{
		Enabled:     g.enabled,
		RulesCount:  len(g.compiled),
		PIIPatterns: len(g.piiRegexes),
	})
}