package ai

import (
	"fmt"
	"log/slog"
)

const redacted = "[REDACTED]"

// APIKey holds a provider credential. Every way of printing or encoding it
// yields "[REDACTED]"; only Reveal returns the key, for the one header that
// needs it.
type APIKey struct {
	value string
}

func NewAPIKey(value string) APIKey { return APIKey{value: value} }

// Reveal returns the key itself. Call it only to set the request header.
func (k APIKey) Reveal() string { return k.value }

// Empty reports whether no key is set.
func (k APIKey) Empty() bool { return k.value == "" }

func (k APIKey) String() string               { return redacted }
func (k APIKey) GoString() string             { return redacted }
func (k APIKey) MarshalJSON() ([]byte, error) { return []byte(`"` + redacted + `"`), nil }
func (k APIKey) MarshalText() ([]byte, error) { return []byte(redacted), nil }
func (k APIKey) LogValue() slog.Value         { return slog.StringValue(redacted) }
func (k APIKey) Format(f fmt.State, _ rune)   { _, _ = f.Write([]byte(redacted)) }
