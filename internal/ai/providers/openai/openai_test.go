package openai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/prompt-masters/backend/internal/ai"
	"go.uber.org/goleak"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

const testKey = "sk-openai-test-9876543210-secret"

func newTestClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	httpClient := &http.Client{Transport: &http.Transport{}}
	t.Cleanup(func() {
		srv.Close()
		httpClient.CloseIdleConnections()
	})
	c, err := New(ai.ProviderConfig{
		APIKey:     ai.NewAPIKey(testKey),
		BaseURL:    srv.URL,
		HTTPClient: httpClient,
		Pricing:    ai.Pricing{"gpt-test": {InputPerMTok: 1, OutputPerMTok: 4}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return c.(*Client)
}

var testRequest = ai.Request{
	Model:     "gpt-test",
	System:    "be brief",
	Messages:  []ai.Message{{Role: ai.MessageRoleUser, Content: "hello"}},
	MaxTokens: 128,
}

func TestGenerateMapsRequestAndResponse(t *testing.T) {
	var got map[string]any
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer "+testKey {
			t.Errorf("request = %s, auth = %q", r.URL.Path, r.Header.Get("Authorization"))
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.Header().Set("x-request-id", "req_oa")
		fmt.Fprint(w, `{"id":"c1","model":"gpt-test","choices":[{"message":{"content":"Hi"},"finish_reason":"stop"}],
			"usage":{"prompt_tokens":1000,"completion_tokens":250}}`)
	})
	resp, err := c.Generate(context.Background(), testRequest)
	if err != nil {
		t.Fatal(err)
	}
	msgs, _ := got["messages"].([]any)
	if len(msgs) != 2 || msgs[0].(map[string]any)["role"] != "system" || msgs[1].(map[string]any)["content"] != "hello" {
		t.Errorf("messages = %v, want the system prompt first", got["messages"])
	}
	if got["max_completion_tokens"] != float64(128) || got["model"] != "gpt-test" {
		t.Errorf("request body = %v", got)
	}
	if _, ok := got["temperature"]; ok {
		t.Error("temperature sent although unset")
	}
	if resp.Text != "Hi" || resp.FinishReason != "stop" || resp.ProviderRequestID != "req_oa" ||
		resp.Usage.InputTokens != 1000 || resp.Usage.OutputTokens != 250 || resp.Usage.CostUSD == nil || *resp.Usage.CostUSD != 0.002 {
		t.Errorf("response = %+v (cost %v)", resp, resp.Usage.CostUSD)
	}
}

func TestErrorMapping(t *testing.T) {
	tests := []struct {
		name       string
		status     int
		body       string
		retryAfter string
		wantKind   ai.ErrorKind
		wantAfter  time.Duration
	}{
		{name: "400 context too long", status: 400, body: `{"error":{"message":"too long","type":"invalid_request_error","code":"context_length_exceeded"}}`, wantKind: ai.ErrKindInvalidRequest},
		{name: "401", status: 401, body: `{"error":{"message":"bad key","type":"invalid_request_error","code":"invalid_api_key"}}`, wantKind: ai.ErrKindAuth},
		{name: "403", status: 403, body: `{"error":{"message":"region"}}`, wantKind: ai.ErrKindAuth},
		{name: "429 rate limit", status: 429, retryAfter: "2", body: `{"error":{"message":"slow","code":"rate_limit_exceeded"}}`, wantKind: ai.ErrKindRateLimit, wantAfter: 2 * time.Second},
		{name: "429 out of quota", status: 429, body: `{"error":{"message":"quota","code":"insufficient_quota"}}`, wantKind: ai.ErrKindAuth},
		{name: "500", status: 500, body: `{"error":{"message":"boom","type":"server_error"}}`, wantKind: ai.ErrKindUnavailable},
		{name: "503", status: 503, body: `not json`, wantKind: ai.ErrKindOverloaded},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
				if tt.retryAfter != "" {
					w.Header().Set("Retry-After", tt.retryAfter)
				}
				w.WriteHeader(tt.status)
				fmt.Fprint(w, tt.body)
			})
			_, err := c.Generate(context.Background(), testRequest)
			var aerr *ai.AIError
			if !errors.As(err, &aerr) || aerr.Kind != tt.wantKind || aerr.StatusCode != tt.status || aerr.RetryAfter != tt.wantAfter {
				t.Errorf("error = %+v, want kind %s, retry-after %s", aerr, tt.wantKind, tt.wantAfter)
			}
		})
	}

	t.Run("malformed JSON", func(t *testing.T) {
		c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, `{"choices":`) })
		if _, err := c.Generate(context.Background(), testRequest); ai.KindOf(err) != ai.ErrKindUnknown {
			t.Errorf("error = %v, want unknown", err)
		}
	})

	t.Run("connection dropped", func(t *testing.T) {
		c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
			if conn, _, err := w.(http.Hijacker).Hijack(); err == nil {
				conn.Close()
			}
		})
		if _, err := c.Generate(context.Background(), testRequest); ai.KindOf(err) != ai.ErrKindUnavailable {
			t.Errorf("error = %v, want unavailable", err)
		}
	})

	t.Run("refusal", func(t *testing.T) {
		c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
			fmt.Fprint(w, `{"choices":[{"message":{"content":null,"refusal":"I can't"},"finish_reason":"stop"}]}`)
		})
		if _, err := c.Generate(context.Background(), testRequest); ai.KindOf(err) != ai.ErrKindContentFiltered {
			t.Errorf("error = %v, want content_filtered", err)
		}
	})

	t.Run("key never in errors", func(t *testing.T) {
		c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(401)
			fmt.Fprintf(w, `{"error":{"message":"Incorrect API key provided: %s"}}`, strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		})
		_, err := c.Generate(context.Background(), testRequest)
		if err == nil || strings.Contains(err.Error(), testKey) {
			t.Errorf("error %q leaks the key", err)
		}
	})
}

func recv(t *testing.T, events <-chan ai.StreamEvent) ai.StreamEvent {
	t.Helper()
	select {
	case ev, ok := <-events:
		if !ok {
			t.Fatal("stream closed early")
		}
		return ev
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for a stream event")
		return ai.StreamEvent{}
	}
}

func sseData(w http.ResponseWriter) func(data string) {
	w.Header().Set("Content-Type", "text/event-stream")
	f := w.(http.Flusher)
	return func(data string) {
		fmt.Fprintf(w, "data: %s\n\n", data)
		f.Flush()
	}
}

func TestStreamIncrementalWithUsage(t *testing.T) {
	release := make(chan struct{})
	var got map[string]any
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		write := sseData(w)
		write(`{"model":"gpt-test","choices":[{"delta":{"content":"Hel"},"finish_reason":null}]}`)
		<-release
		write(`{"choices":[{"delta":{"content":"lo"},"finish_reason":"stop"}]}`)
		write(`{"choices":[],"usage":{"prompt_tokens":1000,"completion_tokens":250}}`)
		write(`[DONE]`)
	})
	events, err := c.Stream(context.Background(), testRequest)
	if err != nil {
		t.Fatal(err)
	}
	if ev := recv(t, events); ev.Delta != "Hel" {
		t.Fatalf("first event = %+v, want it before the server finishes", ev)
	}
	close(release)
	if ev := recv(t, events); ev.Delta != "lo" {
		t.Fatalf("second event = %+v", ev)
	}
	done := recv(t, events)
	if !done.Done || done.FinishReason != "stop" || done.Usage == nil || done.Usage.OutputTokens != 250 || *done.Usage.CostUSD != 0.002 {
		t.Fatalf("final = %+v (usage %+v)", done, done.Usage)
	}
	if opts, _ := got["stream_options"].(map[string]any); got["stream"] != true || opts["include_usage"] != true {
		t.Errorf("stream request = %v, want stream with usage", got)
	}
}

func TestStreamErrorsAndCancellation(t *testing.T) {
	t.Run("error chunk", func(t *testing.T) {
		c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
			write := sseData(w)
			write(`{"choices":[{"delta":{"content":"a"}}]}`)
			write(`{"error":{"message":"server busy","type":"server_error"}}`)
		})
		events, err := c.Stream(context.Background(), testRequest)
		if err != nil {
			t.Fatal(err)
		}
		recv(t, events)
		if ev := recv(t, events); ai.KindOf(ev.Err) != ai.ErrKindUnavailable {
			t.Errorf("terminal = %+v, want unavailable", ev)
		}
	})

	t.Run("cancel mid-stream", func(t *testing.T) {
		serverSawClose := make(chan struct{})
		c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			sseData(w)(`{"choices":[{"delta":{"content":"first"}}]}`)
			select {
			case <-r.Context().Done():
				close(serverSawClose)
			case <-time.After(10 * time.Second):
			}
		})
		ctx, cancel := context.WithCancel(context.Background())
		events, err := c.Stream(ctx, testRequest)
		if err != nil {
			t.Fatal(err)
		}
		recv(t, events)
		cancel()
		select {
		case <-serverSawClose:
		case <-time.After(3 * time.Second):
			t.Fatal("server never saw the abort")
		}
		for range events { // must close
		}
	})
}
