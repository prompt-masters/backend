// Package ai is the provider-agnostic AI layer. Business logic depends only
// on the types and interfaces here; provider SDKs and wire formats live in
// the adapter packages under ai/providers, which this package never imports.
package ai

import "context"

// AIClient is one configured provider. Adapters implement it.
type AIClient interface {
	// Generate returns the whole response once the provider has finished.
	Generate(ctx context.Context, req Request) (Response, error)
	// Stream starts a streaming request. Errors before the stream starts
	// (a non-2xx status, an unreachable provider) are returned directly;
	// later ones arrive as the terminal StreamEvent. The channel is always
	// closed, and its producer exits once ctx is done, so a consumer that
	// stops reading early must cancel ctx.
	Stream(ctx context.Context, req Request) (<-chan StreamEvent, error)
}

// Role names the part an AI call plays in the game.
type Role string

const (
	// RoleExecutor runs a player's prompt to produce the output that is
	// judged.
	RoleExecutor Role = "executor"
	// RoleJudge scores submissions against a challenge's rubric.
	RoleJudge Role = "judge"
)

type MessageRole string

const (
	MessageRoleUser      MessageRole = "user"
	MessageRoleAssistant MessageRole = "assistant"
)

type Message struct {
	Role    MessageRole
	Content string
}

// Request is a provider-neutral completion request.
type Request struct {
	// Model is the provider's model ID, resolved from configuration.
	Model    string
	System   string
	Messages []Message
	// MaxTokens caps the response length.
	MaxTokens int
	// Temperature is sent only when set: several current models reject it.
	Temperature *float64
	// Metadata (game_id, round_id, role, ...) is used for logging and
	// tracing only; it is never sent to the provider.
	Metadata map[string]string
}

type Response struct {
	Text  string
	Model string
	Usage Usage
	// FinishReason is the provider's own stop reason, e.g. "end_turn",
	// "max_tokens" or "stop".
	FinishReason      string
	ProviderRequestID string
}

type Usage struct {
	InputTokens  int
	OutputTokens int
	// CostUSD is nil when the model has no configured price.
	CostUSD *float64
}

// StreamEvent is one item of a stream: a text delta, or the terminal event
// (Done, or Err). Nothing follows a terminal event.
type StreamEvent struct {
	Delta string
	// Usage is set on the terminal Done event when the provider reports it.
	Usage *Usage
	// FinishReason is set on the terminal Done event.
	FinishReason string
	Done         bool
	// Err is a *AIError and is terminal.
	Err error
}
