package anthropic

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/prompt-masters/backend/internal/ai"
	"go.uber.org/goleak"
)

func TestMain(m *testing.M) {
	// No stream reader or connection may outlive the tests.
	goleak.VerifyTestMain(m)
}

const testKey = "sk-ant-test-0123456789-secret"

// newTestClient points the adapter at handler.
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
		Pricing:    ai.Pricing{"claude-sonnet-5": {InputPerMTok: 2, OutputPerMTok: 10}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return c.(*Client)
}

var testRequest = ai.Request{
	Model:     "claude-sonnet-5",
	System:    "be brief",
	Messages:  []ai.Message{{Role: ai.MessageRoleUser, Content: "hello"}},
	MaxTokens: 256,
	Metadata:  map[string]string{"game_id": "g1"},
}

func TestGenerateMapsRequestAndResponse(t *testing.T) {
	var got map[string]any
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" || r.Method != http.MethodPost {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("x-api-key") != testKey || r.Header.Get("anthropic-version") != apiVersion {
			t.Errorf("auth headers = %q / %q", r.Header.Get("x-api-key"), r.Header.Get("anthropic-version"))
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.Header().Set("request-id", "req_123")
		fmt.Fprint(w, `{"id":"msg_1","model":"claude-sonnet-5","content":[{"type":"text","text":"Hi "},{"type":"text","text":"there"}],
			"stop_reason":"end_turn","usage":{"input_tokens":1000,"output_tokens":500}}`)
	})

	resp, err := c.Generate(context.Background(), testRequest)
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	if got["model"] != "claude-sonnet-5" || got["system"] != "be brief" || got["max_tokens"] != float64(256) {
		t.Errorf("request body = %v", got)
	}
	if _, ok := got["temperature"]; ok {
		t.Error("temperature was sent although unset; several models reject it")
	}
	if _, ok := got["metadata"]; ok {
		t.Error("our metadata leaked into the provider request")
	}
	msgs, _ := got["messages"].([]any)
	if len(msgs) != 1 || msgs[0].(map[string]any)["content"] != "hello" || msgs[0].(map[string]any)["role"] != "user" {
		t.Errorf("messages = %v", got["messages"])
	}

	if resp.Text != "Hi there" || resp.Model != "claude-sonnet-5" || resp.FinishReason != "end_turn" || resp.ProviderRequestID != "req_123" {
		t.Errorf("response = %+v", resp)
	}
	if resp.Usage.InputTokens != 1000 || resp.Usage.OutputTokens != 500 || resp.Usage.CostUSD == nil || *resp.Usage.CostUSD != 0.007 {
		t.Errorf("usage = %+v (cost %v), want 1000/500 at $0.007", resp.Usage, resp.Usage.CostUSD)
	}
}

func TestGenerateSendsTemperatureWhenSet(t *testing.T) {
	var got map[string]any
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		fmt.Fprint(w, `{"model":"claude-haiku-4-5","content":[],"stop_reason":"end_turn","usage":{}}`)
	})
	req := testRequest
	temp := 0.0
	req.Temperature, req.Model = &temp, "claude-haiku-4-5"
	resp, err := c.Generate(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if v, ok := got["temperature"]; !ok || v != 0.0 {
		t.Errorf("temperature = %v, want 0 sent explicitly", got["temperature"])
	}
	if resp.Usage.CostUSD != nil {
		t.Errorf("cost = %v for an unpriced model, want nil", *resp.Usage.CostUSD)
	}
}

func TestErrorMapping(t *testing.T) {
	tests := []struct {
		name       string
		status     int
		body       string
		header     map[string]string
		wantKind   ai.ErrorKind
		retryable  bool
		retryAfter time.Duration
	}{
		{name: "400", status: 400, body: `{"type":"error","error":{"type":"invalid_request_error","message":"prompt is too long"}}`, wantKind: ai.ErrKindInvalidRequest},
		{name: "401", status: 401, body: `{"type":"error","error":{"type":"authentication_error","message":"invalid x-api-key"}}`, wantKind: ai.ErrKindAuth},
		{name: "403", status: 403, body: `{"type":"error","error":{"type":"permission_error","message":"no access"}}`, wantKind: ai.ErrKindAuth},
		{name: "404 model", status: 404, body: `{"type":"error","error":{"type":"not_found_error","message":"model: nope"}}`, wantKind: ai.ErrKindInvalidRequest},
		{
			name: "429 with retry-after", status: 429, body: `{"type":"error","error":{"type":"rate_limit_error","message":"slow down"}}`,
			header: map[string]string{"retry-after": "7"}, wantKind: ai.ErrKindRateLimit, retryable: true, retryAfter: 7 * time.Second,
		},
		{name: "500", status: 500, body: `{"type":"error","error":{"type":"api_error","message":"oops"}}`, wantKind: ai.ErrKindUnavailable, retryable: true},
		{name: "503 non-JSON", status: 503, body: `<html>upstream down</html>`, wantKind: ai.ErrKindOverloaded, retryable: true},
		{name: "529", status: 529, body: `{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`, wantKind: ai.ErrKindOverloaded, retryable: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
				for k, v := range tt.header {
					w.Header().Set(k, v)
				}
				w.Header().Set("request-id", "req_err")
				w.WriteHeader(tt.status)
				fmt.Fprint(w, tt.body)
			})
			for _, call := range []func() error{
				func() error { _, err := c.Generate(context.Background(), testRequest); return err },
				func() error { _, err := c.Stream(context.Background(), testRequest); return err },
			} {
				var aerr *ai.AIError
				if err := call(); !errors.As(err, &aerr) {
					t.Fatalf("error = %v, want *ai.AIError", err)
				}
				if aerr.Kind != tt.wantKind || aerr.Retryable() != tt.retryable || aerr.StatusCode != tt.status || aerr.Provider != Name {
					t.Errorf("AIError = %+v, want kind %s (retryable %v) for %d", aerr, tt.wantKind, tt.retryable, tt.status)
				}
				if aerr.RetryAfter != tt.retryAfter || aerr.RequestID != "req_err" {
					t.Errorf("retry-after = %s, request id = %q; want %s and req_err", aerr.RetryAfter, aerr.RequestID, tt.retryAfter)
				}
			}
		})
	}
}

func TestMalformedAndDroppedResponses(t *testing.T) {
	t.Run("malformed JSON body", func(t *testing.T) {
		c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, `{"content": [`) })
		_, err := c.Generate(context.Background(), testRequest)
		if ai.KindOf(err) != ai.ErrKindUnknown {
			t.Errorf("error = %v, want unknown kind", err)
		}
	})

	t.Run("connection dropped", func(t *testing.T) {
		c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
			conn, _, err := w.(http.Hijacker).Hijack()
			if err == nil {
				conn.Close()
			}
		})
		_, err := c.Generate(context.Background(), testRequest)
		var aerr *ai.AIError
		if !errors.As(err, &aerr) || aerr.Kind != ai.ErrKindUnavailable || !aerr.Retryable() {
			t.Errorf("error = %v, want retryable unavailable", err)
		}
	})

	t.Run("refusal", func(t *testing.T) {
		c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
			fmt.Fprint(w, `{"model":"claude-sonnet-5","content":[],"stop_reason":"refusal","usage":{}}`)
		})
		_, err := c.Generate(context.Background(), testRequest)
		if ai.KindOf(err) != ai.ErrKindContentFiltered {
			t.Errorf("error = %v, want content_filtered", err)
		}
	})
}

func TestKeyNeverAppearsInErrors(t *testing.T) {
	// A provider that echoes the credential back must not leak it through us.
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprintf(w, `{"type":"error","error":{"type":"authentication_error","message":"bad key %s"}}`, r.Header.Get("x-api-key"))
	})
	_, err := c.Generate(context.Background(), testRequest)
	if err == nil || strings.Contains(err.Error(), testKey) || strings.Contains(fmt.Sprintf("%+v", err), testKey) {
		t.Fatalf("error %q leaks the API key", err)
	}
	if !strings.Contains(err.Error(), "[REDACTED]") {
		t.Errorf("error = %q, want the echoed key redacted", err)
	}
}

// sseWriter writes Anthropic stream events and flushes each one.
func sseWriter(w http.ResponseWriter) func(event, data string) {
	w.Header().Set("Content-Type", "text/event-stream")
	flusher := w.(http.Flusher)
	return func(event, data string) {
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, data)
		flusher.Flush()
	}
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

func TestStreamDeliversChunksIncrementally(t *testing.T) {
	release := make(chan struct{})
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		write := sseWriter(w)
		write("message_start", `{"type":"message_start","message":{"model":"claude-sonnet-5","usage":{"input_tokens":1000,"output_tokens":1}}}`)
		write("ping", `{"type":"ping"}`)
		write("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hel"}}`)
		<-release // the rest is only written once the client has the first chunk
		write("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"lo"}}`)
		write("message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":500}}`)
		write("message_stop", `{"type":"message_stop"}`)
	})

	events, err := c.Stream(context.Background(), testRequest)
	if err != nil {
		t.Fatalf("Stream() error = %v", err)
	}
	if first := recv(t, events); first.Delta != "Hel" {
		t.Fatalf("first event = %+v, want the first chunk before the server finishes", first)
	}
	close(release)
	if second := recv(t, events); second.Delta != "lo" {
		t.Fatalf("second event = %+v", second)
	}
	done := recv(t, events)
	if !done.Done || done.FinishReason != "end_turn" || done.Usage == nil || done.Usage.OutputTokens != 500 || done.Usage.InputTokens != 1000 {
		t.Fatalf("final event = %+v (usage %+v)", done, done.Usage)
	}
	if done.Usage.CostUSD == nil || *done.Usage.CostUSD != 0.007 {
		t.Errorf("final cost = %v, want 0.007", done.Usage.CostUSD)
	}
	if _, ok := <-events; ok {
		t.Error("stream not closed after its final event")
	}
}

func TestStreamTerminalErrors(t *testing.T) {
	tests := map[string]struct {
		handler  func(write func(event, data string), w http.ResponseWriter)
		wantKind ai.ErrorKind
	}{
		"error event mid-stream": {
			handler: func(write func(event, data string), _ http.ResponseWriter) {
				write("error", `{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`)
			},
			wantKind: ai.ErrKindOverloaded,
		},
		"refusal": {
			handler: func(write func(event, data string), _ http.ResponseWriter) {
				write("message_delta", `{"type":"message_delta","delta":{"stop_reason":"refusal"},"usage":{"output_tokens":3}}`)
			},
			wantKind: ai.ErrKindContentFiltered,
		},
		"connection dropped": {
			handler:  func(_ func(event, data string), _ http.ResponseWriter) { panic(http.ErrAbortHandler) },
			wantKind: ai.ErrKindUnavailable,
		},
		"ended without message_stop": {
			handler:  func(func(event, data string), http.ResponseWriter) {},
			wantKind: ai.ErrKindUnavailable,
		},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
				write := sseWriter(w)
				write("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"partial"}}`)
				tt.handler(write, w)
			})
			events, err := c.Stream(context.Background(), testRequest)
			if err != nil {
				t.Fatal(err)
			}
			if ev := recv(t, events); ev.Delta != "partial" {
				t.Fatalf("first event = %+v", ev)
			}
			last := recv(t, events)
			if ai.KindOf(last.Err) != tt.wantKind {
				t.Fatalf("terminal event = %+v, want error kind %s", last, tt.wantKind)
			}
			if _, ok := <-events; ok {
				t.Error("stream not closed after its terminal error")
			}
		})
	}
}

func TestStreamCancellationAbortsTheRequest(t *testing.T) {
	serverSawClose := make(chan struct{})
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		write := sseWriter(w)
		write("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"first"}}`)
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
	if ev := recv(t, events); ev.Delta != "first" {
		t.Fatalf("first event = %+v", ev)
	}
	cancel()

	select {
	case <-serverSawClose:
	case <-time.After(3 * time.Second):
		t.Fatal("the server never saw the request aborted")
	}
	deadline := time.After(3 * time.Second)
	for {
		select {
		case _, ok := <-events:
			if !ok {
				return // closed; goleak in TestMain checks the reader exited
			}
		case <-deadline:
			t.Fatal("stream channel not closed after cancellation")
		}
	}
}

func TestNewRequiresAKey(t *testing.T) {
	if _, err := New(ai.ProviderConfig{}); err == nil {
		t.Error("New() without a key succeeded")
	}
	// The request body never carries the key either.
	var body []byte
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ = io.ReadAll(r.Body)
		fmt.Fprint(w, `{"content":[],"usage":{}}`)
	})
	if _, err := c.Generate(context.Background(), testRequest); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), testKey) {
		t.Error("the API key appears in the request body")
	}
}
