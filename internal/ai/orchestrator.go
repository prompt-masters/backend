package ai

import (
	"cmp"
	"context"
	"fmt"
	"log"
	"math/rand/v2"
	"strings"
	"time"
)

// Orchestrator is what business logic calls. It picks the provider and model
// for each role from configuration, bounds every call with the role's
// timeout, rate-limits per provider, retries retryable failures with capped
// exponential backoff and full jitter, and logs every attempt.
type Orchestrator struct {
	cfg      Config
	clients  map[string]AIClient
	limiters map[string]*limiter
	logger   *log.Logger
	sleep    func(context.Context, time.Duration) error
	// jitter returns a random duration in [0, d).
	jitter func(d time.Duration) time.Duration
}

type Option func(*Orchestrator)

// WithSleep replaces the ctx-aware sleep used for backoff and rate limiting.
func WithSleep(sleep func(context.Context, time.Duration) error) Option {
	return func(o *Orchestrator) { o.sleep = sleep }
}

// WithJitter replaces the backoff jitter source.
func WithJitter(jitter func(time.Duration) time.Duration) Option {
	return func(o *Orchestrator) { o.jitter = jitter }
}

// NewOrchestrator needs a client for every provider cfg uses; see
// Registry.Clients.
func NewOrchestrator(cfg Config, clients map[string]AIClient, logger *log.Logger, opts ...Option) (*Orchestrator, error) {
	o := &Orchestrator{
		cfg:      cfg,
		clients:  clients,
		limiters: map[string]*limiter{},
		logger:   logger,
		sleep:    sleepCtx,
		jitter: func(d time.Duration) time.Duration {
			if d <= 0 {
				return 0
			}
			return rand.N(d)
		},
	}
	for _, opt := range opts {
		opt(o)
	}
	for _, name := range cfg.ProviderNames() {
		if _, ok := clients[name]; !ok {
			return nil, fmt.Errorf("no AI client for provider %q", name)
		}
		if rl, ok := cfg.RateLimits[name]; ok {
			o.limiters[name] = newLimiter(rl, o.sleep)
		}
	}
	return o, nil
}

type ExecuteInput struct {
	// Model is the game's ai_model setting, mapped through
	// Config.ExecutorModels; "" uses the executor default.
	Model    string
	System   string
	Prompt   string
	Metadata map[string]string
}

type ExecuteOutput struct {
	Text         string
	Provider     string
	Model        string
	Usage        Usage
	FinishReason string
	Attempts     int
}

// Execute runs a prompt with the executor.
func (o *Orchestrator) Execute(ctx context.Context, in ExecuteInput) (ExecuteOutput, error) {
	target, req, err := o.executorRequest(in)
	if err != nil {
		return ExecuteOutput{}, err
	}
	resp, attempts, err := o.generate(ctx, RoleExecutor, target, o.cfg.Executor.Timeout, req)
	if err != nil {
		return ExecuteOutput{Attempts: attempts}, err
	}
	return ExecuteOutput{
		Text:         resp.Text,
		Provider:     target.Provider,
		Model:        resp.Model,
		Usage:        resp.Usage,
		FinishReason: resp.FinishReason,
		Attempts:     attempts,
	}, nil
}

// ExecuteStream runs a prompt with the executor, streaming its output. A
// failure before the first chunk is retried like Execute; once a chunk has
// been sent, a failure ends the stream as its terminal event and nothing is
// replayed. The channel is always closed; a consumer that stops reading must
// cancel ctx.
func (o *Orchestrator) ExecuteStream(ctx context.Context, in ExecuteInput) (<-chan StreamEvent, error) {
	target, req, err := o.executorRequest(in)
	if err != nil {
		return nil, err
	}
	out := make(chan StreamEvent)
	go o.stream(ctx, RoleExecutor, target, o.cfg.Executor.Timeout, req, out)
	return out, nil
}

// ExecutorTarget returns the provider and model Execute uses for a game's
// ai_model setting; "" means the executor default.
func (o *Orchestrator) ExecutorTarget(model string) (Target, error) {
	if model == "" {
		return o.cfg.Executor.Target, nil
	}
	target, ok := o.cfg.ExecutorModels[model]
	if !ok {
		return Target{}, &AIError{Kind: ErrKindInvalidRequest, Provider: o.cfg.Executor.Provider,
			Message: fmt.Sprintf("no executor configured for model %q", model)}
	}
	return target, nil
}

func (o *Orchestrator) executorRequest(in ExecuteInput) (Target, Request, error) {
	target, err := o.ExecutorTarget(in.Model)
	if err != nil {
		return Target{}, Request{}, err
	}
	if strings.TrimSpace(in.Prompt) == "" {
		return Target{}, Request{}, &AIError{Kind: ErrKindInvalidRequest, Provider: target.Provider, Message: "prompt is empty"}
	}
	return target, Request{
		Model:       target.Model,
		System:      in.System,
		Messages:    []Message{{Role: MessageRoleUser, Content: in.Prompt}},
		MaxTokens:   o.cfg.Executor.MaxTokens,
		Temperature: o.cfg.Executor.Temperature,
		Metadata:    in.Metadata,
	}, nil
}

// generate makes one logical call: attempts until success, a failure that is
// not retryable, the attempt limit, or the deadline. timeout bounds all of
// it, so a call never outlives it.
func (o *Orchestrator) generate(ctx context.Context, role Role, target Target, timeout time.Duration, req Request) (Response, int, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	client := o.clients[target.Provider]

	for attempt := 1; ; attempt++ {
		start := time.Now()
		var (
			resp Response
			aerr *AIError
		)
		if err := o.wait(ctx, target.Provider); err != nil {
			aerr = Normalize(ctx, target.Provider, err)
		} else {
			var err error
			resp, err = client.Generate(ctx, req)
			aerr = Normalize(ctx, target.Provider, err)
		}
		o.logAttempt("generate", role, target, req, attempt, time.Since(start), resp.Usage, resp.FinishReason, resp.ProviderRequestID, aerr)
		if aerr == nil {
			o.logContent(role, req, resp.Text)
			return resp, attempt, nil
		}
		delay, ok := o.retryDelay(ctx, aerr, attempt)
		if !ok {
			return Response{}, attempt, aerr
		}
		if err := o.sleep(ctx, delay); err != nil {
			return Response{}, attempt, Normalize(ctx, target.Provider, err)
		}
	}
}

// stream is generate for streams; see ExecuteStream.
func (o *Orchestrator) stream(parent context.Context, role Role, target Target, timeout time.Duration, req Request, out chan<- StreamEvent) {
	defer close(out)
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	// Chunks stop at the call's own deadline; the terminal event waits only
	// for the caller, so a timeout is still reported to a caller that is
	// listening.
	send := func(ev StreamEvent) bool {
		select {
		case out <- ev:
			return true
		case <-ctx.Done():
			return false
		}
	}
	finish := func(ev StreamEvent) {
		select {
		case out <- ev:
		case <-parent.Done():
		}
	}
	client := o.clients[target.Provider]

	for attempt := 1; ; attempt++ {
		start := time.Now()
		var (
			final   StreamEvent
			aerr    *AIError
			emitted bool
			text    strings.Builder
		)
		if err := o.wait(ctx, target.Provider); err != nil {
			aerr = Normalize(ctx, target.Provider, err)
		} else if events, err := client.Stream(ctx, req); err != nil {
			aerr = Normalize(ctx, target.Provider, err)
		} else {
		read:
			for ev := range events {
				switch {
				case ev.Err != nil:
					aerr = Normalize(ctx, target.Provider, ev.Err)
					break read
				case ev.Done:
					final = ev
					break read
				case ev.Delta != "":
					emitted = true
					if o.cfg.LogContent {
						text.WriteString(ev.Delta)
					}
					if !send(ev) {
						break read
					}
				}
			}
			if aerr == nil && !final.Done {
				// The stream closed without a result: the context ended, or
				// the adapter stopped early.
				aerr = ContextError(ctx, target.Provider)
				if aerr == nil {
					aerr = &AIError{Kind: ErrKindUnavailable, Provider: target.Provider, Message: "stream closed without a result"}
				}
			}
		}

		var usage Usage
		if final.Usage != nil {
			usage = *final.Usage
		}
		o.logAttempt("stream", role, target, req, attempt, time.Since(start), usage, final.FinishReason, "", aerr)
		if aerr == nil {
			o.logContent(role, req, text.String())
			finish(final)
			return
		}
		if emitted {
			finish(StreamEvent{Err: aerr})
			return
		}
		delay, ok := o.retryDelay(ctx, aerr, attempt)
		if !ok {
			finish(StreamEvent{Err: aerr})
			return
		}
		if err := o.sleep(ctx, delay); err != nil {
			finish(StreamEvent{Err: Normalize(ctx, target.Provider, err)})
			return
		}
	}
}

// retryDelay decides whether to try again after attempt failed with aerr,
// and how long to wait first: capped exponential backoff with full jitter,
// or the provider's Retry-After when that is longer. It refuses when the
// wait would run past ctx's deadline.
func (o *Orchestrator) retryDelay(ctx context.Context, aerr *AIError, attempt int) (time.Duration, bool) {
	if !aerr.Retryable() || attempt >= o.cfg.Retry.MaxAttempts || ctx.Err() != nil {
		return 0, false
	}
	ceiling := o.cfg.Retry.MaxDelay
	if shift := attempt - 1; shift < 30 {
		ceiling = min(o.cfg.Retry.BaseDelay<<shift, o.cfg.Retry.MaxDelay)
	}
	delay := o.jitter(ceiling)
	if aerr.RetryAfter > delay {
		delay = aerr.RetryAfter
	}
	if deadline, ok := ctx.Deadline(); ok && time.Until(deadline) <= delay {
		return 0, false
	}
	return delay, true
}

func (o *Orchestrator) wait(ctx context.Context, provider string) error {
	if l, ok := o.limiters[provider]; ok {
		return l.Wait(ctx)
	}
	return ctx.Err()
}

// logAttempt writes one line per attempt. It never logs prompts, responses,
// keys or headers.
func (o *Orchestrator) logAttempt(op string, role Role, target Target, req Request, attempt int, latency time.Duration,
	usage Usage, finish, requestID string, aerr *AIError) {
	cost := "unknown"
	if usage.CostUSD != nil {
		cost = fmt.Sprintf("%.6f", *usage.CostUSD)
	}
	var kind ErrorKind
	status := 0
	if aerr != nil {
		kind, status = aerr.Kind, aerr.StatusCode
		requestID = cmp.Or(requestID, aerr.RequestID)
	}
	o.logger.Printf("ai op=%s role=%s provider=%s model=%s attempt=%d latency_ms=%d input_tokens=%d output_tokens=%d cost_usd=%s finish_reason=%s error_kind=%s status=%d request_id=%s game_id=%s round_id=%s",
		op, role, target.Provider, target.Model, attempt, latency.Milliseconds(), usage.InputTokens, usage.OutputTokens,
		cost, finish, kind, status, requestID, req.Metadata["game_id"], req.Metadata["round_id"])
}

// logContent logs the prompt and response only when LogContent is on.
func (o *Orchestrator) logContent(role Role, req Request, response string) {
	if !o.cfg.LogContent {
		return
	}
	var prompt strings.Builder
	for _, m := range req.Messages {
		prompt.WriteString(m.Content)
	}
	o.logger.Printf("ai op=content role=%s game_id=%s round_id=%s prompt=%q response=%q",
		role, req.Metadata["game_id"], req.Metadata["round_id"], prompt.String(), response)
}
