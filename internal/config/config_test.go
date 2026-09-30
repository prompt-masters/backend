package config

import (
	"strings"
	"testing"
	"time"
)

const testAnthropicKey = "sk-ant-config-test-secret"

func TestLoadAIDefaults(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", testAnthropicKey)
	cfg, err := loadAI()
	if err != nil {
		t.Fatalf("loadAI() error = %v", err)
	}
	if cfg.Executor.Provider != "anthropic" || cfg.Judge.Model != "claude-opus-5" || cfg.Judge.Timeout != 90*time.Second {
		t.Errorf("roles = %+v / %+v", cfg.Executor, cfg.Judge)
	}
	if cfg.Executor.Temperature != nil || cfg.Judge.Temperature != nil {
		t.Error("temperature defaulted; current Claude models reject it")
	}
	if got := cfg.ExecutorModels["claude-haiku-4-5"]; got.Provider != "anthropic" || got.Model != "claude-haiku-4-5" {
		t.Errorf("executor model mapping = %+v", got)
	}
	if cfg.Providers["anthropic"].APIKey.Reveal() != testAnthropicKey || cfg.JudgeParseRetries != 1 {
		t.Errorf("providers = %v, parse retries = %d", cfg.Providers, cfg.JudgeParseRetries)
	}
}

func TestLoadAISwapsProvidersThroughEnv(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", testAnthropicKey)
	t.Setenv("OPENAI_API_KEY", "sk-openai-config-test")
	t.Setenv("AI_EXECUTOR_PROVIDER", "openai")
	t.Setenv("AI_EXECUTOR_MODEL", "gpt-test")
	t.Setenv("AI_EXECUTOR_TEMPERATURE", "0.7")
	t.Setenv("AI_EXECUTOR_MODELS", "claude-opus-5=openai/gpt-big, claude-sonnet-5=anthropic/claude-sonnet-5,claude-haiku-4-5=openai/gpt-small")
	t.Setenv("AI_RATE_LIMITS", "openai=5:10,anthropic=2.5:3")
	t.Setenv("AI_PRICING", "gpt-big=1.25:10")
	t.Setenv("AI_JUDGE_PARSE_RETRIES", "0")

	cfg, err := loadAI()
	if err != nil {
		t.Fatalf("loadAI() error = %v", err)
	}
	if cfg.Executor.Provider != "openai" || cfg.Executor.Model != "gpt-test" || *cfg.Executor.Temperature != 0.7 {
		t.Errorf("executor = %+v", cfg.Executor)
	}
	if cfg.ExecutorModels["claude-opus-5"].Model != "gpt-big" || cfg.RateLimits["anthropic"].RPS != 2.5 || cfg.Pricing["gpt-big"].OutputPerMTok != 10 {
		t.Errorf("parsed = %+v / %+v / %+v", cfg.ExecutorModels, cfg.RateLimits, cfg.Pricing)
	}
	if cfg.JudgeParseRetries != 0 {
		t.Errorf("parse retries = %d, want 0", cfg.JudgeParseRetries)
	}
}

func TestLoadAIFailsFast(t *testing.T) {
	t.Run("missing key", func(t *testing.T) {
		t.Setenv("ANTHROPIC_API_KEY", "")
		t.Setenv("OPENAI_API_KEY", "sk-openai-must-not-print")
		t.Setenv("AI_JUDGE_PROVIDER", "openai")
		_, err := loadAI()
		if err == nil || !strings.Contains(err.Error(), "ANTHROPIC_API_KEY") {
			t.Fatalf("loadAI() = %v, want the missing key named", err)
		}
		if strings.Contains(err.Error(), "sk-openai-must-not-print") {
			t.Error("the error prints a key")
		}
	})

	for name, env := range map[string][2]string{
		"bad mapping":         {"AI_EXECUTOR_MODELS", "claude-opus-5=anthropic"},
		"unmapped game model": {"AI_EXECUTOR_MODELS", "claude-opus-5=anthropic/claude-opus-5"},
		"bad rate limit":      {"AI_RATE_LIMITS", "anthropic=fast"},
		"bad price":           {"AI_PRICING", "claude-opus-5=5"},
		"bad temperature":     {"AI_JUDGE_TEMPERATURE", "cold"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("ANTHROPIC_API_KEY", testAnthropicKey)
			t.Setenv(env[0], env[1])
			if _, err := loadAI(); err == nil {
				t.Errorf("loadAI() with %s=%q succeeded", env[0], env[1])
			}
		})
	}
}
