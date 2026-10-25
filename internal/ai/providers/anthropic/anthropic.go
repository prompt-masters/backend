// Package anthropic adapts the Anthropic Messages API to ai.AIClient, over
// plain net/http and its SSE stream.
package anthropic

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/prompt-masters/backend/internal/ai"
	"github.com/prompt-masters/backend/internal/ai/internal/httpx"
	"github.com/prompt-masters/backend/internal/ai/internal/sse"
)

const (
	Name           = "anthropic"
	DefaultBaseURL = "https://api.anthropic.com"
	apiVersion     = "2023-06-01"
	stopRefusal    = "refusal"
)

type Client struct {
	key     ai.APIKey
	url     string
	http    *http.Client
	pricing ai.Pricing
}

// New builds the adapter. It is an ai.Factory.
func New(cfg ai.ProviderConfig) (ai.AIClient, error) {
	if cfg.APIKey.Empty() {
		return nil, errors.New("anthropic: API key is not set")
	}
	base := cfg.BaseURL
	if base == "" {
		base = DefaultBaseURL
	}
	return &Client{
		key:     cfg.APIKey,
		url:     strings.TrimSuffix(base, "/") + "/v1/messages",
		http:    httpx.Client(cfg.HTTPClient),
		pricing: cfg.Pricing,
	}, nil
}

// --- wire format ---

type messageRequest struct {
	Model       string    `json:"model"`
	MaxTokens   int       `json:"max_tokens"`
	System      string    `json:"system,omitempty"`
	Messages    []message `json:"messages"`
	Temperature *float64  `json:"temperature,omitempty"`
	Stream      bool      `json:"stream,omitempty"`
}

type message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type wireUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

type messageResponse struct {
	ID      string `json:"id"`
	Model   string `json:"model"`
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	StopReason string    `json:"stop_reason"`
	Usage      wireUsage `json:"usage"`
}

type errorBody struct {
	Error struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
	RequestID string `json:"request_id"`
}

func toWire(req ai.Request, stream bool) messageRequest {
	msgs := make([]message, len(req.Messages))
	for i, m := range req.Messages {
		msgs[i] = message{Role: string(m.Role), Content: m.Content}
	}
	return messageRequest{
		Model:       req.Model,
		MaxTokens:   req.MaxTokens,
		System:      req.System,
		Messages:    msgs,
		Temperature: req.Temperature,
		Stream:      stream,
	}
}

// --- AIClient ---

func (c *Client) Generate(ctx context.Context, req ai.Request) (ai.Response, error) {
	resp, err := c.post(ctx, req, false)
	if err != nil {
		return ai.Response{}, err
	}
	defer resp.Body.Close()

	body, err := httpx.ReadLimited(resp.Body, httpx.MaxResponseBytes)
	if err != nil {
		return ai.Response{}, ai.TransportError(ctx, Name, err)
	}
	requestID := resp.Header.Get("request-id")
	var mr messageResponse
	if err := json.Unmarshal(body, &mr); err != nil {
		return ai.Response{}, &ai.AIError{Kind: ai.ErrKindUnknown, Provider: Name, StatusCode: resp.StatusCode,
			Message: "malformed response body", RequestID: requestID, Cause: err}
	}
	if mr.StopReason == stopRefusal {
		return ai.Response{}, refusal(resp.StatusCode, requestID)
	}

	var text strings.Builder
	for _, block := range mr.Content {
		if block.Type == "text" {
			text.WriteString(block.Text)
		}
	}
	model := cmp.Or(mr.Model, req.Model)
	return ai.Response{
		Text:              text.String(),
		Model:             model,
		Usage:             c.pricing.WithCost(model, ai.Usage{InputTokens: mr.Usage.InputTokens, OutputTokens: mr.Usage.OutputTokens}),
		FinishReason:      mr.StopReason,
		ProviderRequestID: requestID,
	}, nil
}

func (c *Client) Stream(ctx context.Context, req ai.Request) (<-chan ai.StreamEvent, error) {
	resp, err := c.post(ctx, req, true)
	if err != nil {
		return nil, err
	}
	events := make(chan ai.StreamEvent)
	go c.readStream(ctx, resp, req.Model, events)
	return events, nil
}

// post sends the request and turns a non-2xx status into an *ai.AIError.
func (c *Client) post(ctx context.Context, req ai.Request, stream bool) (*http.Response, error) {
	header := http.Header{}
	header.Set("x-api-key", c.key.Reveal())
	header.Set("anthropic-version", apiVersion)
	if stream {
		header.Set("Accept", "text/event-stream")
	}
	resp, err := httpx.PostJSON(ctx, c.http, c.url, header, toWire(req, stream))
	if err != nil {
		return nil, ai.TransportError(ctx, Name, err)
	}
	if resp.StatusCode/100 != 2 {
		defer resp.Body.Close()
		return nil, c.statusError(ctx, resp)
	}
	return resp, nil
}

func (c *Client) statusError(ctx context.Context, resp *http.Response) error {
	body, err := httpx.ReadLimited(resp.Body, httpx.MaxErrorBytes)
	if err != nil {
		return ai.TransportError(ctx, Name, err)
	}
	var eb errorBody
	_ = json.Unmarshal(body, &eb) // a non-JSON body (a proxy's page) keeps the status mapping
	return &ai.AIError{
		Kind:       kindFor(resp.StatusCode, eb.Error.Type),
		Provider:   Name,
		StatusCode: resp.StatusCode,
		RetryAfter: ai.ParseRetryAfter(resp.Header, time.Now()),
		Message:    ai.SafeMessage(cmp.Or(eb.Error.Message, http.StatusText(resp.StatusCode)), c.key),
		RequestID:  cmp.Or(resp.Header.Get("request-id"), eb.RequestID),
	}
}

// kindFor refines the status mapping with Anthropic's error type.
func kindFor(status int, errType string) ai.ErrorKind {
	switch errType {
	case "rate_limit_error":
		return ai.ErrKindRateLimit
	case "overloaded_error":
		return ai.ErrKindOverloaded
	case "api_error":
		return ai.ErrKindUnavailable
	case "authentication_error", "permission_error":
		return ai.ErrKindAuth
	case "invalid_request_error", "not_found_error", "request_too_large":
		return ai.ErrKindInvalidRequest
	}
	return ai.KindForStatus(status)
}

// --- streaming ---

type streamEvent struct {
	Type    string `json:"type"`
	Message struct {
		Model string    `json:"model"`
		Usage wireUsage `json:"usage"`
	} `json:"message"`
	Delta struct {
		Type       string `json:"type"`
		Text       string `json:"text"`
		StopReason string `json:"stop_reason"`
	} `json:"delta"`
	Usage wireUsage `json:"usage"`
	Error struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}

// readStream forwards text deltas as they arrive. It always closes events,
// and it stops as soon as ctx is done: the request is bound to ctx, so the
// body read fails and the connection is closed.
func (c *Client) readStream(ctx context.Context, resp *http.Response, model string, events chan<- ai.StreamEvent) {
	defer close(events)
	defer resp.Body.Close()

	send := func(ev ai.StreamEvent) bool {
		select {
		case events <- ev:
			return true
		case <-ctx.Done():
			return false
		}
	}
	requestID := resp.Header.Get("request-id")
	fail := func(aerr *ai.AIError) {
		if aerr.RequestID == "" {
			aerr.RequestID = requestID
		}
		send(ai.StreamEvent{Err: aerr})
	}

	var (
		usage ai.Usage
		stop  string
	)
	reader := sse.NewReader(resp.Body)
	for {
		raw, err := reader.Next()
		if errors.Is(err, io.EOF) {
			fail(&ai.AIError{Kind: ai.ErrKindUnavailable, Provider: Name, Message: "stream ended before message_stop"})
			return
		}
		if err != nil {
			fail(ai.TransportError(ctx, Name, err))
			return
		}
		if raw.Data == "" {
			continue
		}
		var ev streamEvent
		if err := json.Unmarshal([]byte(raw.Data), &ev); err != nil {
			fail(&ai.AIError{Kind: ai.ErrKindUnknown, Provider: Name, Message: "malformed stream event", Cause: err})
			return
		}

		switch cmp.Or(ev.Type, raw.Name) {
		case "message_start":
			model = cmp.Or(ev.Message.Model, model)
			usage.InputTokens = ev.Message.Usage.InputTokens
			usage.OutputTokens = ev.Message.Usage.OutputTokens
		case "content_block_delta":
			if ev.Delta.Type == "text_delta" && ev.Delta.Text != "" {
				if !send(ai.StreamEvent{Delta: ev.Delta.Text}) {
					return
				}
			}
		case "message_delta":
			stop = cmp.Or(ev.Delta.StopReason, stop)
			if ev.Usage.OutputTokens > 0 {
				usage.OutputTokens = ev.Usage.OutputTokens
			}
			if ev.Usage.InputTokens > 0 {
				usage.InputTokens = ev.Usage.InputTokens
			}
			if stop == stopRefusal {
				fail(refusal(resp.StatusCode, requestID))
				return
			}
		case "message_stop":
			final := c.pricing.WithCost(model, usage)
			send(ai.StreamEvent{Done: true, Usage: &final, FinishReason: stop})
			return
		case "error":
			fail(&ai.AIError{
				Kind:     kindFor(0, ev.Error.Type),
				Provider: Name,
				Message:  ai.SafeMessage(cmp.Or(ev.Error.Message, ev.Error.Type), c.key),
			})
			return
		}
	}
}

func refusal(status int, requestID string) *ai.AIError {
	return &ai.AIError{Kind: ai.ErrKindContentFiltered, Provider: Name, StatusCode: status,
		Message: "the model declined the request", RequestID: requestID}
}
