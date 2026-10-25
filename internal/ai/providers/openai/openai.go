// Package openai adapts the OpenAI Chat Completions API to ai.AIClient, over
// plain net/http and its SSE stream.
package openai

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
	Name                = "openai"
	DefaultBaseURL      = "https://api.openai.com"
	finishContentFilter = "content_filter"
	streamDone          = "[DONE]"
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
		return nil, errors.New("openai: API key is not set")
	}
	base := cmp.Or(cfg.BaseURL, DefaultBaseURL)
	return &Client{
		key:     cfg.APIKey,
		url:     strings.TrimSuffix(base, "/") + "/v1/chat/completions",
		http:    httpx.Client(cfg.HTTPClient),
		pricing: cfg.Pricing,
	}, nil
}

// --- wire format ---

type chatRequest struct {
	Model               string         `json:"model"`
	Messages            []chatMessage  `json:"messages"`
	MaxCompletionTokens int            `json:"max_completion_tokens,omitempty"`
	Temperature         *float64       `json:"temperature,omitempty"`
	Stream              bool           `json:"stream,omitempty"`
	StreamOptions       *streamOptions `json:"stream_options,omitempty"`
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type streamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

type wireUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
}

type chatResponse struct {
	ID      string `json:"id"`
	Model   string `json:"model"`
	Choices []struct {
		Message struct {
			Content string `json:"content"`
			Refusal string `json:"refusal"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage *wireUsage `json:"usage"`
}

type apiError struct {
	Message string `json:"message"`
	Type    string `json:"type"`
	Code    string `json:"code"`
}

type errorBody struct {
	Error apiError `json:"error"`
}

func toWire(req ai.Request, stream bool) chatRequest {
	msgs := make([]chatMessage, 0, len(req.Messages)+1)
	if req.System != "" {
		msgs = append(msgs, chatMessage{Role: "system", Content: req.System})
	}
	for _, m := range req.Messages {
		msgs = append(msgs, chatMessage{Role: string(m.Role), Content: m.Content})
	}
	w := chatRequest{
		Model:               req.Model,
		Messages:            msgs,
		MaxCompletionTokens: req.MaxTokens,
		Temperature:         req.Temperature,
		Stream:              stream,
	}
	if stream {
		w.StreamOptions = &streamOptions{IncludeUsage: true}
	}
	return w
}

func (u *wireUsage) toUsage() ai.Usage {
	if u == nil {
		return ai.Usage{}
	}
	return ai.Usage{InputTokens: u.PromptTokens, OutputTokens: u.CompletionTokens}
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
	requestID := resp.Header.Get("x-request-id")
	var cr chatResponse
	if err := json.Unmarshal(body, &cr); err != nil || len(cr.Choices) == 0 {
		return ai.Response{}, &ai.AIError{Kind: ai.ErrKindUnknown, Provider: Name, StatusCode: resp.StatusCode,
			Message: "malformed response body", RequestID: requestID, Cause: err}
	}
	choice := cr.Choices[0]
	if choice.Message.Refusal != "" || choice.FinishReason == finishContentFilter {
		return ai.Response{}, filtered(resp.StatusCode, requestID)
	}
	model := cmp.Or(cr.Model, req.Model)
	return ai.Response{
		Text:              choice.Message.Content,
		Model:             model,
		Usage:             c.pricing.WithCost(model, cr.Usage.toUsage()),
		FinishReason:      choice.FinishReason,
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

func (c *Client) post(ctx context.Context, req ai.Request, stream bool) (*http.Response, error) {
	header := http.Header{}
	header.Set("Authorization", "Bearer "+c.key.Reveal())
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
		Kind:       kindFor(resp.StatusCode, eb.Error),
		Provider:   Name,
		StatusCode: resp.StatusCode,
		RetryAfter: ai.ParseRetryAfter(resp.Header, time.Now()),
		Message:    ai.SafeMessage(cmp.Or(eb.Error.Message, http.StatusText(resp.StatusCode)), c.key),
		RequestID:  resp.Header.Get("x-request-id"),
	}
}

// kindFor refines the status mapping with OpenAI's error code.
func kindFor(status int, e apiError) ai.ErrorKind {
	switch e.Code {
	case "insufficient_quota":
		// A 429 that waiting will not fix: the account is out of credit.
		return ai.ErrKindAuth
	case "rate_limit_exceeded":
		return ai.ErrKindRateLimit
	case "context_length_exceeded":
		return ai.ErrKindInvalidRequest
	case "content_filter", "content_policy_violation":
		return ai.ErrKindContentFiltered
	}
	if e.Type == "server_error" && status == 0 {
		return ai.ErrKindUnavailable
	}
	return ai.KindForStatus(status)
}

// --- streaming ---

type streamChunk struct {
	Model   string `json:"model"`
	Choices []struct {
		Delta struct {
			Content string `json:"content"`
			Refusal string `json:"refusal"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage *wireUsage `json:"usage"`
	Error *apiError  `json:"error"`
}

// readStream forwards content deltas as they arrive. It always closes
// events and stops as soon as ctx is done.
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
	requestID := resp.Header.Get("x-request-id")
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
			fail(&ai.AIError{Kind: ai.ErrKindUnavailable, Provider: Name, Message: "stream ended before [DONE]"})
			return
		}
		if err != nil {
			fail(ai.TransportError(ctx, Name, err))
			return
		}
		if raw.Data == streamDone {
			final := c.pricing.WithCost(model, usage)
			send(ai.StreamEvent{Done: true, Usage: &final, FinishReason: stop})
			return
		}
		if raw.Data == "" {
			continue
		}
		var chunk streamChunk
		if err := json.Unmarshal([]byte(raw.Data), &chunk); err != nil {
			fail(&ai.AIError{Kind: ai.ErrKindUnknown, Provider: Name, Message: "malformed stream event", Cause: err})
			return
		}
		if chunk.Error != nil {
			fail(&ai.AIError{Kind: kindFor(0, *chunk.Error), Provider: Name,
				Message: ai.SafeMessage(cmp.Or(chunk.Error.Message, chunk.Error.Type), c.key)})
			return
		}
		model = cmp.Or(chunk.Model, model)
		if chunk.Usage != nil {
			usage = chunk.Usage.toUsage()
		}
		for _, choice := range chunk.Choices {
			if choice.Delta.Refusal != "" || choice.FinishReason == finishContentFilter {
				fail(filtered(resp.StatusCode, requestID))
				return
			}
			stop = cmp.Or(choice.FinishReason, stop)
			if choice.Delta.Content != "" && !send(ai.StreamEvent{Delta: choice.Delta.Content}) {
				return
			}
		}
	}
}

func filtered(status int, requestID string) *ai.AIError {
	return &ai.AIError{Kind: ai.ErrKindContentFiltered, Provider: Name, StatusCode: status,
		Message: "the provider filtered the content", RequestID: requestID}
}
