package ai

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// ErrorKind classifies every AI failure independently of the provider.
type ErrorKind string

const (
	ErrKindTimeout         ErrorKind = "timeout"
	ErrKindCanceled        ErrorKind = "canceled"
	ErrKindRateLimit       ErrorKind = "rate_limit"
	ErrKindOverloaded      ErrorKind = "overloaded"
	ErrKindUnavailable     ErrorKind = "unavailable"
	ErrKindAuth            ErrorKind = "auth"
	ErrKindInvalidRequest  ErrorKind = "invalid_request"
	ErrKindContentFiltered ErrorKind = "content_filtered"
	ErrKindInvalidOutput   ErrorKind = "invalid_output"
	ErrKindUnknown         ErrorKind = "unknown"
)

// Retryable reports whether a call that failed this way may succeed if
// repeated. A caller's own cancellation never is.
func (k ErrorKind) Retryable() bool {
	switch k {
	case ErrKindTimeout, ErrKindRateLimit, ErrKindOverloaded, ErrKindUnavailable:
		return true
	}
	return false
}

// maxMessageLen bounds the provider message kept in an AIError.
const maxMessageLen = 300

// AIError is the normalized form of every provider failure. Its message is
// safe to log: it never carries API keys or request bodies.
type AIError struct {
	Kind     ErrorKind
	Provider string
	// StatusCode is the HTTP status, or 0 when there was no response.
	StatusCode int
	// RetryAfter is the provider's requested wait, or 0.
	RetryAfter time.Duration
	// Message is a short, safe description.
	Message string
	// RequestID is the provider's request ID, when known.
	RequestID string
	Cause     error
}

func (e *AIError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "ai: %s %s", e.Provider, e.Kind)
	if e.StatusCode != 0 {
		fmt.Fprintf(&b, " (status %d)", e.StatusCode)
	}
	if e.Message != "" {
		b.WriteString(": ")
		b.WriteString(e.Message)
	}
	return b.String()
}

func (e *AIError) Unwrap() error { return e.Cause }

// Retryable reports whether the call may be retried.
func (e *AIError) Retryable() bool { return e.Kind.Retryable() }

// KindOf returns the kind of err, or ErrKindUnknown when err is not an
// AIError.
func KindOf(err error) ErrorKind {
	var aerr *AIError
	if errors.As(err, &aerr) {
		return aerr.Kind
	}
	return ErrKindUnknown
}

// KindForStatus maps an HTTP status onto a kind. Adapters refine it with the
// provider's error type where that is more precise.
func KindForStatus(status int) ErrorKind {
	switch {
	case status == http.StatusRequestTimeout:
		return ErrKindTimeout
	case status == http.StatusUnauthorized, status == http.StatusForbidden:
		return ErrKindAuth
	case status == http.StatusTooManyRequests:
		return ErrKindRateLimit
	case status == http.StatusServiceUnavailable, status == 529:
		return ErrKindOverloaded
	case status >= 500:
		return ErrKindUnavailable
	case status == http.StatusBadRequest, status == http.StatusNotFound,
		status == http.StatusRequestEntityTooLarge, status == http.StatusUnprocessableEntity:
		return ErrKindInvalidRequest
	}
	return ErrKindUnknown
}

// ContextError converts ctx's error, if any, into an AIError: a deadline is a
// timeout, a cancellation is the caller's.
func ContextError(ctx context.Context, provider string) *AIError {
	switch err := ctx.Err(); {
	case errors.Is(err, context.DeadlineExceeded):
		return &AIError{Kind: ErrKindTimeout, Provider: provider, Message: "deadline exceeded", Cause: err}
	case err != nil:
		return &AIError{Kind: ErrKindCanceled, Provider: provider, Message: "canceled by caller", Cause: err}
	}
	return nil
}

// TransportError normalizes a failure to reach the provider or to read its
// response. The context decides first, since a cancelled request surfaces as
// a transport error.
func TransportError(ctx context.Context, provider string, err error) *AIError {
	if aerr := ContextError(ctx, provider); aerr != nil {
		return aerr
	}
	var aerr *AIError
	if errors.As(err, &aerr) {
		return aerr
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return &AIError{Kind: ErrKindTimeout, Provider: provider, Message: "deadline exceeded", Cause: err}
	}
	return &AIError{Kind: ErrKindUnavailable, Provider: provider, Message: "connection failed", Cause: err}
}

// Normalize returns err as an *AIError, wrapping anything else.
func Normalize(ctx context.Context, provider string, err error) *AIError {
	if err == nil {
		return nil
	}
	var aerr *AIError
	if errors.As(err, &aerr) {
		// A provider may see only its own per-call deadline; the caller's
		// cancellation takes precedence.
		if cerr := ContextError(ctx, provider); cerr != nil && cerr.Kind == ErrKindCanceled {
			return cerr
		}
		return aerr
	}
	if cerr := ContextError(ctx, provider); cerr != nil {
		return cerr
	}
	if errors.Is(err, context.Canceled) {
		return &AIError{Kind: ErrKindCanceled, Provider: provider, Message: "canceled", Cause: err}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return &AIError{Kind: ErrKindTimeout, Provider: provider, Message: "deadline exceeded", Cause: err}
	}
	return &AIError{Kind: ErrKindUnknown, Provider: provider, Message: "unexpected error", Cause: err}
}

// SafeMessage trims a provider-supplied message and removes any occurrence of
// secret from it, so a provider echoing a credential cannot leak it.
func SafeMessage(msg string, secret APIKey) string {
	msg = strings.TrimSpace(msg)
	if key := secret.Reveal(); key != "" {
		msg = strings.ReplaceAll(msg, key, "[REDACTED]")
	}
	if len(msg) > maxMessageLen {
		msg = msg[:maxMessageLen] + "…"
	}
	return msg
}

// ParseRetryAfter reads a provider's requested wait from retry-after-ms or
// retry-after (seconds or an HTTP date). It returns 0 when there is none.
func ParseRetryAfter(h http.Header, now time.Time) time.Duration {
	if ms, err := strconv.ParseFloat(h.Get("Retry-After-Ms"), 64); err == nil && ms > 0 {
		return time.Duration(ms * float64(time.Millisecond))
	}
	v := strings.TrimSpace(h.Get("Retry-After"))
	if v == "" {
		return 0
	}
	if secs, err := strconv.ParseFloat(v, 64); err == nil {
		return max(time.Duration(secs*float64(time.Second)), 0)
	}
	if t, err := http.ParseTime(v); err == nil {
		return max(t.Sub(now), 0)
	}
	return 0
}
