package mail

import (
	"context"
	"errors"
	"io"
	"net/smtp"
	"strings"
	"testing"
)

func testConfig() Config {
	return Config{
		Host:      "smtp.example.com",
		Port:      587,
		Username:  "postmaster@example.com",
		Password:  "secret",
		FromEmail: "noreply@example.com",
	}
}

const testLink = "https://app.example.com/api/v1/auth/verify-email?token=abc123"

func TestVerificationMessageCarriesTheAddressingHeaders(t *testing.T) {
	msg, err := buildVerificationMessage("noreply@example.com", "alice@example.com", "alice", testLink)
	if err != nil {
		t.Fatalf("buildVerificationMessage() error = %v, want nil", err)
	}

	headers, _, found := strings.Cut(string(msg), "\r\n\r\n")
	if !found {
		t.Fatal("message has no blank line separating headers from body")
	}

	for _, want := range []string{
		"From: noreply@example.com",
		"To: alice@example.com",
		"Subject: ",
		"MIME-Version: 1.0",
		"Date: ",
		"Message-ID: ",
	} {
		if !strings.Contains(headers, want) {
			t.Errorf("headers are missing %q; got:\n%s", want, headers)
		}
	}
}

func TestVerificationMessageBodyCarriesTheLinkAndUsername(t *testing.T) {
	msg, err := buildVerificationMessage("noreply@example.com", "alice@example.com", "alice", testLink)
	if err != nil {
		t.Fatalf("buildVerificationMessage() error = %v, want nil", err)
	}

	_, body, _ := strings.Cut(string(msg), "\r\n\r\n")
	if !strings.Contains(body, testLink) {
		t.Errorf("body is missing the verification link; got:\n%s", body)
	}
	if !strings.Contains(body, "alice") {
		t.Errorf("body does not address the user by name; got:\n%s", body)
	}
}

func TestVerificationMessageUsesCRLFLineEndings(t *testing.T) {
	msg, err := buildVerificationMessage("noreply@example.com", "alice@example.com", "alice", testLink)
	if err != nil {
		t.Fatalf("buildVerificationMessage() error = %v, want nil", err)
	}

	// RFC 5322 requires CRLF; a bare LF between headers breaks strict servers.
	if strings.Contains(strings.ReplaceAll(string(msg), "\r\n", ""), "\n") {
		t.Error("message contains a bare LF not preceded by CR")
	}
}

func TestVerificationMessageRefusesHeaderInjection(t *testing.T) {
	injections := map[string]struct{ from, to, username string }{
		"newline in recipient": {"noreply@example.com", "alice@example.com\r\nBcc: evil@example.com", "alice"},
		"newline in sender":    {"noreply@example.com\nBcc: evil@example.com", "alice@example.com", "alice"},
		"newline in username":  {"noreply@example.com", "alice@example.com", "alice\r\nBcc: evil@example.com"},
	}

	for name, in := range injections {
		t.Run(name, func(t *testing.T) {
			msg, err := buildVerificationMessage(in.from, in.to, in.username, testLink)
			if err == nil {
				t.Fatalf("buildVerificationMessage() error = nil, want a rejection; produced:\n%s", msg)
			}
			if !errors.Is(err, ErrUnsafeHeader) {
				t.Errorf("error = %v, want ErrUnsafeHeader", err)
			}
		})
	}
}

// fakeTransport records what an SMTPSender hands to the SMTP layer.
type fakeTransport struct {
	calls int
	addr  string
	from  string
	to    []string
	msg   []byte
	auth  smtp.Auth
	err   error
}

func (f *fakeTransport) send(addr string, a smtp.Auth, from string, to []string, msg []byte) error {
	f.calls++
	f.addr, f.auth, f.from, f.to, f.msg = addr, a, from, to, msg
	return f.err
}

func TestSMTPSenderAddressesTheConfiguredServer(t *testing.T) {
	transport := &fakeTransport{}
	sender := newSMTPSender(testConfig(), transport.send)

	err := sender.SendVerification(context.Background(), "alice@example.com", "alice", testLink)
	if err != nil {
		t.Fatalf("SendVerification() error = %v, want nil", err)
	}

	if transport.calls != 1 {
		t.Fatalf("transport called %d times, want 1", transport.calls)
	}
	if transport.addr != "smtp.example.com:587" {
		t.Errorf("addr = %q, want %q", transport.addr, "smtp.example.com:587")
	}
	if transport.from != "noreply@example.com" {
		t.Errorf("from = %q, want %q", transport.from, "noreply@example.com")
	}
	if len(transport.to) != 1 || transport.to[0] != "alice@example.com" {
		t.Errorf("to = %v, want [alice@example.com]", transport.to)
	}
	if transport.auth == nil {
		t.Error("auth is nil; the sender must authenticate")
	}
	if !strings.Contains(string(transport.msg), testLink) {
		t.Error("the message handed to the transport does not contain the link")
	}
}

func TestSMTPSenderReportsATransportFailure(t *testing.T) {
	wantErr := errors.New("dial tcp: connection refused")
	transport := &fakeTransport{err: wantErr}
	sender := newSMTPSender(testConfig(), transport.send)

	err := sender.SendVerification(context.Background(), "alice@example.com", "alice", testLink)

	if !errors.Is(err, wantErr) {
		t.Fatalf("SendVerification() error = %v, want it to wrap %v", err, wantErr)
	}
}

func TestSMTPSenderRefusesAnUnsafeRecipientWithoutContactingTheServer(t *testing.T) {
	transport := &fakeTransport{}
	sender := newSMTPSender(testConfig(), transport.send)

	err := sender.SendVerification(context.Background(), "alice@example.com\r\nBcc: evil@example.com", "alice", testLink)

	if !errors.Is(err, ErrUnsafeHeader) {
		t.Fatalf("SendVerification() error = %v, want ErrUnsafeHeader", err)
	}
	if transport.calls != 0 {
		t.Errorf("transport called %d times, want 0", transport.calls)
	}
}

func TestConfigRejectsIncompleteSettings(t *testing.T) {
	tests := map[string]func(*Config){
		"no host": func(c *Config) { c.Host = "" },
		"no from": func(c *Config) { c.FromEmail = "" },
		"no port": func(c *Config) { c.Port = 0 },
	}

	for name, break_ := range tests {
		t.Run(name, func(t *testing.T) {
			cfg := testConfig()
			break_(&cfg)
			if err := cfg.Validate(); err == nil {
				t.Errorf("Validate() = nil, want an error for %s", name)
			}
		})
	}

	if err := testConfig().Validate(); err != nil {
		t.Errorf("Validate() on a complete config = %v, want nil", err)
	}
}

func TestLogSenderRecordsTheLinkInsteadOfSending(t *testing.T) {
	var logged strings.Builder
	sender := NewLogSender(&logged)

	err := sender.SendVerification(context.Background(), "alice@example.com", "alice", testLink)
	if err != nil {
		t.Fatalf("SendVerification() error = %v, want nil", err)
	}

	out := logged.String()
	if !strings.Contains(out, testLink) {
		t.Errorf("log does not contain the link; got %q", out)
	}
	if !strings.Contains(out, "alice@example.com") {
		t.Errorf("log does not contain the recipient; got %q", out)
	}
}

func TestNewSenderFromConfigUsesSMTPWhenConfigured(t *testing.T) {
	sender := NewSenderFromConfig(testConfig(), io.Discard)

	if _, ok := sender.(*SMTPSender); !ok {
		t.Errorf("got %T, want *SMTPSender for a complete config", sender)
	}
}

func TestNewSenderFromConfigFallsBackToLoggingWhenUnconfigured(t *testing.T) {
	incomplete := testConfig()
	incomplete.Host = ""

	sender := NewSenderFromConfig(incomplete, io.Discard)

	if _, ok := sender.(*LogSender); !ok {
		t.Errorf("got %T, want *LogSender when SMTP is not configured", sender)
	}
}
