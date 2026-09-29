// Package httpx holds the HTTP plumbing the provider adapters share.
package httpx

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

const (
	// MaxResponseBytes bounds a whole non-streaming response.
	MaxResponseBytes = 16 << 20
	// MaxErrorBytes bounds an error body.
	MaxErrorBytes = 64 << 10
)

// PostJSON sends body to url bound to ctx, so cancelling ctx aborts the
// request in flight, including a streaming body being read.
func PostJSON(ctx context.Context, client *http.Client, url string, header http.Header, body any) (*http.Response, error) {
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("encoding request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header = header.Clone()
	req.Header.Set("Content-Type", "application/json")
	return client.Do(req)
}

// ReadLimited reads at most limit bytes of r.
func ReadLimited(r io.Reader, limit int64) ([]byte, error) {
	return io.ReadAll(io.LimitReader(r, limit))
}

// Client returns c, or a default client when c is nil.
func Client(c *http.Client) *http.Client {
	if c != nil {
		return c
	}
	return &http.Client{}
}
