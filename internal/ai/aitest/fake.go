// Package aitest provides a scriptable fake ai.AIClient, so business logic
// is tested without any provider or network.
package aitest

import (
	"context"
	"slices"
	"sync"
	"time"

	"github.com/prompt-masters/backend/internal/ai"
)

// Step scripts one call to the fake. The zero Step answers at once with an
// empty response.
type Step struct {
	// Response is returned by Generate; its Text is also streamed as one
	// chunk when Chunks is empty.
	Response ai.Response
	// Err fails the call before any output (Generate's error, or Stream's
	// synchronous error).
	Err error
	// Delay waits before answering; Block waits until the context is done.
	Delay time.Duration
	Block bool
	// Chunks are streamed in order, ChunkDelay apart.
	Chunks     []string
	ChunkDelay time.Duration
	// StreamErr ends the stream after Chunks as its terminal event.
	StreamErr error
}

// Client plays its steps in order, repeating the last one once they run out.
type Client struct {
	Provider string

	mu       sync.Mutex
	steps    []Step
	requests []ai.Request
}

func New(provider string, steps ...Step) *Client {
	if len(steps) == 0 {
		steps = []Step{{}}
	}
	return &Client{Provider: provider, steps: steps}
}

// Calls counts the calls made so far.
func (c *Client) Calls() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.requests)
}

// Requests returns every request received, in order.
func (c *Client) Requests() []ai.Request {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.requests)
}

func (c *Client) next(req ai.Request) Step {
	c.mu.Lock()
	defer c.mu.Unlock()
	step := c.steps[min(len(c.requests), len(c.steps)-1)]
	c.requests = append(c.requests, req)
	return step
}

// wait honours Delay and Block, returning the context's error as an AIError
// when it ends first.
func (c *Client) wait(ctx context.Context, s Step) error {
	if s.Block {
		<-ctx.Done()
		return ai.ContextError(ctx, c.Provider)
	}
	if s.Delay > 0 {
		t := time.NewTimer(s.Delay)
		defer t.Stop()
		select {
		case <-t.C:
		case <-ctx.Done():
			return ai.ContextError(ctx, c.Provider)
		}
	}
	return nil
}

func (c *Client) Generate(ctx context.Context, req ai.Request) (ai.Response, error) {
	s := c.next(req)
	if err := c.wait(ctx, s); err != nil {
		return ai.Response{}, err
	}
	if s.Err != nil {
		return ai.Response{}, s.Err
	}
	resp := s.Response
	if resp.Model == "" {
		resp.Model = req.Model
	}
	return resp, nil
}

func (c *Client) Stream(ctx context.Context, req ai.Request) (<-chan ai.StreamEvent, error) {
	s := c.next(req)
	if err := c.wait(ctx, s); err != nil {
		return nil, err
	}
	if s.Err != nil {
		return nil, s.Err
	}
	chunks := s.Chunks
	if len(chunks) == 0 && s.Response.Text != "" {
		chunks = []string{s.Response.Text}
	}

	events := make(chan ai.StreamEvent)
	go func() {
		defer close(events)
		send := func(ev ai.StreamEvent) bool {
			select {
			case events <- ev:
				return true
			case <-ctx.Done():
				return false
			}
		}
		for i, chunk := range chunks {
			if i > 0 && s.ChunkDelay > 0 {
				if err := c.wait(ctx, Step{Delay: s.ChunkDelay}); err != nil {
					send(ai.StreamEvent{Err: err})
					return
				}
			}
			if !send(ai.StreamEvent{Delta: chunk}) {
				return
			}
		}
		if s.StreamErr != nil {
			send(ai.StreamEvent{Err: s.StreamErr})
			return
		}
		usage := s.Response.Usage
		send(ai.StreamEvent{Done: true, Usage: &usage, FinishReason: s.Response.FinishReason})
	}()
	return events, nil
}
