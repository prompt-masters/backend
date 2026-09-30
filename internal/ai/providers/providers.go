// Package providers names every built-in adapter. It is the only package
// outside the adapters themselves that knows they exist.
package providers

import (
	"github.com/prompt-masters/backend/internal/ai"
	"github.com/prompt-masters/backend/internal/ai/providers/anthropic"
	"github.com/prompt-masters/backend/internal/ai/providers/openai"
)

// Registry returns a registry of every built-in provider, keyed by the name
// used in configuration.
func Registry() *ai.Registry {
	return ai.NewRegistry(map[string]ai.Factory{
		anthropic.Name: anthropic.New,
		openai.Name:    openai.New,
	})
}
