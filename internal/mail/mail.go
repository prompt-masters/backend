// Package mail sends the transactional email the API needs. It deliberately
// exposes a small Sender interface so callers can be tested without a server.
package mail

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/smtp"
	"strconv"
	"strings"
	"time"
)

// ErrUnsafeHeader reports a value carrying a line break. Such a value could
// terminate the header it sits in and inject another (a Bcc, say), so it is
// refused rather than escaped.
var ErrUnsafeHeader = errors.New("mail: value contains a line break")

const verificationSubject = "Verify your Prompt Masters email address"

// Config describes the SMTP server to send through.
type Config struct {
	Host      string
	Port      int
	Username  string
	Password  string
	FromEmail string
}

// Validate reports whether the config is complete enough to send with.
func (c Config) Validate() error {
	switch {
	case c.Host == "":
		return errors.New("mail: host is required")
	case c.Port <= 0:
		return errors.New("mail: port is required")
	case c.FromEmail == "":
		return errors.New("mail: from address is required")
	}
	return nil
}

// Sender delivers a verification link to a new user.
type Sender interface {
	SendVerification(ctx context.Context, to, username, link string) error
}

// sendFunc matches smtp.SendMail so tests can substitute a transport.
type sendFunc func(addr string, a smtp.Auth, from string, to []string, msg []byte) error

// SMTPSender delivers mail through an SMTP server. On port 587 the standard
// library negotiates STARTTLS when the server advertises it.
type SMTPSender struct {
	cfg  Config
	send sendFunc
}

// NewSMTPSender returns a Sender that talks to the server in cfg.
func NewSMTPSender(cfg Config) *SMTPSender {
	return newSMTPSender(cfg, smtp.SendMail)
}

func newSMTPSender(cfg Config, send sendFunc) *SMTPSender {
	return &SMTPSender{cfg: cfg, send: send}
}

// SendVerification emails a verification link to a new user. The call blocks
// for as long as the SMTP conversation takes.
func (s *SMTPSender) SendVerification(ctx context.Context, to, username, link string) error {
	msg, err := buildVerificationMessage(s.cfg.FromEmail, to, username, link)
	if err != nil {
		return err
	}

	addr := net.JoinHostPort(s.cfg.Host, strconv.Itoa(s.cfg.Port))
	auth := smtp.PlainAuth("", s.cfg.Username, s.cfg.Password, s.cfg.Host)

	if err := s.send(addr, auth, s.cfg.FromEmail, []string{to}, msg); err != nil {
		return fmt.Errorf("sending verification email: %w", err)
	}
	return nil
}

// LogSender writes the verification link instead of sending it. It is what
// runs when no SMTP server is configured, so registration still works in
// development and the link is available from the log.
type LogSender struct {
	w io.Writer
}

func NewLogSender(w io.Writer) *LogSender { return &LogSender{w: w} }

func (s *LogSender) SendVerification(ctx context.Context, to, username, link string) error {
	_, err := fmt.Fprintf(s.w, "mail not configured; verification link for %s (%s): %s\n", username, to, link)
	return err
}

// buildVerificationMessage renders an RFC 5322 message. Every value that
// reaches a header is rejected if it carries a line break, so no input can
// inject a header of its own; the username reaches only the body but is held
// to the same rule.
func buildVerificationMessage(from, to, username, link string) ([]byte, error) {
	for _, v := range []string{from, to, username, link} {
		if strings.ContainsAny(v, "\r\n") {
			return nil, fmt.Errorf("%w: %q", ErrUnsafeHeader, v)
		}
	}

	messageID, err := generateMessageID(from)
	if err != nil {
		return nil, fmt.Errorf("generating Message-ID: %w", err)
	}

	body := "Hi " + username + ",\r\n\r\n" +
		"Confirm this address to activate your Prompt Masters account:\r\n\r\n" +
		link + "\r\n\r\n" +
		"The link is good for 24 hours and can be used once.\r\n" +
		"If you did not sign up, you can ignore this email.\r\n"

	headers := []string{
		"From: " + from,
		"To: " + to,
		"Subject: " + verificationSubject,
		"Date: " + time.Now().Format(time.RFC1123Z),
		"Message-ID: " + messageID,
		"MIME-Version: 1.0",
		`Content-Type: text/plain; charset="UTF-8"`,
		"Content-Transfer-Encoding: 8bit",
	}

	return []byte(strings.Join(headers, "\r\n") + "\r\n\r\n" + body), nil
}

// generateMessageID builds a globally unique Message-ID. Without one, and
// without a Date, mail is far more likely to be filed as spam.
func generateMessageID(from string) (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}

	domain := "localhost"
	if at := strings.LastIndex(from, "@"); at >= 0 && at+1 < len(from) {
		domain = from[at+1:]
	}
	return "<" + hex.EncodeToString(b) + "@" + domain + ">", nil
}
