package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/prompt-masters/backend/internal/auth"
)

const testBaseURL = "https://app.example.com"

// fakeRegistrar stands in for *auth.Service.
type fakeRegistrar struct {
	registration *auth.Registration
	registerErr  error
	registerCall *auth.RegisterInput

	verified  *auth.User
	verifyErr error
	verifyArg string
}

func (f *fakeRegistrar) Register(ctx context.Context, in auth.RegisterInput) (*auth.Registration, error) {
	f.registerCall = &in
	if f.registerErr != nil {
		return nil, f.registerErr
	}
	return f.registration, nil
}

func (f *fakeRegistrar) VerifyEmail(ctx context.Context, rawToken string) (*auth.User, error) {
	f.verifyArg = rawToken
	if f.verifyErr != nil {
		return nil, f.verifyErr
	}
	return f.verified, nil
}

// fakeSender records sends and lets a test wait for the background dispatch.
type fakeSender struct {
	sent chan sentMail
	err  error
}

type sentMail struct{ to, username, link string }

func newFakeSender() *fakeSender { return &fakeSender{sent: make(chan sentMail, 4)} }

func (f *fakeSender) SendVerification(ctx context.Context, to, username, link string) error {
	f.sent <- sentMail{to, username, link}
	return f.err
}

// await returns the next send, failing if none arrives.
func (f *fakeSender) await(t *testing.T) sentMail {
	t.Helper()
	select {
	case m := <-f.sent:
		return m
	case <-time.After(2 * time.Second):
		t.Fatal("no verification email was dispatched")
		return sentMail{}
	}
}

func registeredUser() *auth.Registration {
	return &auth.Registration{
		User: &auth.User{
			ID: 7, Username: "alice", Email: "alice@example.com",
			PasswordHash: "$2a$10$notarealhash", EloRating: 1200,
		},
		VerificationToken: "tok-en_value",
	}
}

func newTestHandler(svc Registrar, sender *fakeSender) http.Handler {
	mux := http.NewServeMux()
	NewAuthHandler(svc, sender, testBaseURL, log.New(io.Discard, "", 0)).RegisterRoutes(mux)
	return mux
}

func postRegister(t *testing.T, h http.Handler, body string, contentType ...string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/register", strings.NewReader(body))
	ct := "application/json"
	if len(contentType) > 0 {
		ct = contentType[0]
	}
	if ct != "" {
		req.Header.Set("Content-Type", ct)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func decodeBody(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("response body is not JSON (%v): %s", err, rec.Body.String())
	}
	return got
}

func assertError(t *testing.T, rec *httptest.ResponseRecorder, status int, code string) map[string]any {
	t.Helper()
	if rec.Code != status {
		t.Errorf("status = %d, want %d; body: %s", rec.Code, status, rec.Body.String())
	}
	body := decodeBody(t, rec)
	if body["error_code"] != code {
		t.Errorf("error_code = %v, want %q", body["error_code"], code)
	}
	if msg, ok := body["message"].(string); !ok || msg == "" {
		t.Errorf("message = %v, want a non-empty string", body["message"])
	}
	return body
}

func TestRegisterReturns201WithTheAccountSummary(t *testing.T) {
	svc := &fakeRegistrar{registration: registeredUser()}
	sender := newFakeSender()

	rec := postRegister(t, newTestHandler(svc, sender), `{"username":"alice","email":"alice@example.com","password":"hunter2secret"}`)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d; body: %s", rec.Code, http.StatusCreated, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}

	body := decodeBody(t, rec)
	want := map[string]any{
		"user_id":    float64(7),
		"username":   "alice",
		"email":      "alice@example.com",
		"elo_rating": float64(1200),
	}
	for k, v := range want {
		if body[k] != v {
			t.Errorf("%s = %v, want %v", k, body[k], v)
		}
	}
	if len(body) != len(want) {
		t.Errorf("body has %d fields (%v), want exactly %v", len(body), body, want)
	}
}

func TestRegisterNeverReturnsThePassword(t *testing.T) {
	svc := &fakeRegistrar{registration: registeredUser()}

	rec := postRegister(t, newTestHandler(svc, newFakeSender()), `{"username":"alice","email":"alice@example.com","password":"hunter2secret"}`)

	for _, leak := range []string{"hunter2secret", "password", "$2a$10$notarealhash", "tok-en_value"} {
		if strings.Contains(rec.Body.String(), leak) {
			t.Errorf("response leaks %q: %s", leak, rec.Body.String())
		}
	}
}

func TestRegisterPassesTheSubmittedFieldsToTheService(t *testing.T) {
	svc := &fakeRegistrar{registration: registeredUser()}

	postRegister(t, newTestHandler(svc, newFakeSender()), `{"username":"alice","email":"alice@example.com","password":"hunter2secret"}`)

	if svc.registerCall == nil {
		t.Fatal("the service was never called")
	}
	if svc.registerCall.Username != "alice" || svc.registerCall.Email != "alice@example.com" || svc.registerCall.Password != "hunter2secret" {
		t.Errorf("service received %+v, want the submitted fields", *svc.registerCall)
	}
}

func TestRegisterEmailsAVerificationLink(t *testing.T) {
	svc := &fakeRegistrar{registration: registeredUser()}
	sender := newFakeSender()

	postRegister(t, newTestHandler(svc, sender), `{"username":"alice","email":"alice@example.com","password":"hunter2secret"}`)

	sent := sender.await(t)
	if sent.to != "alice@example.com" {
		t.Errorf("to = %q, want %q", sent.to, "alice@example.com")
	}
	if sent.username != "alice" {
		t.Errorf("username = %q, want %q", sent.username, "alice")
	}
	if !strings.HasPrefix(sent.link, testBaseURL+"/api/v1/auth/verify-email?token=") {
		t.Errorf("link = %q, want it to point at the verify endpoint on %s", sent.link, testBaseURL)
	}
	if !strings.Contains(sent.link, "tok-en_value") {
		t.Errorf("link = %q, want it to carry the verification token", sent.link)
	}
}

func TestRegisterStillSucceedsWhenTheEmailCannotBeSent(t *testing.T) {
	svc := &fakeRegistrar{registration: registeredUser()}
	sender := newFakeSender()
	sender.err = errors.New("smtp: connection refused")

	rec := postRegister(t, newTestHandler(svc, sender), `{"username":"alice","email":"alice@example.com","password":"hunter2secret"}`)

	if rec.Code != http.StatusCreated {
		t.Errorf("status = %d, want %d; a mail outage must not fail registration", rec.Code, http.StatusCreated)
	}
	sender.await(t)
}

func TestRegisterRejectsAMalformedBody(t *testing.T) {
	bodies := map[string]string{
		"invalid json":  `{"username":`,
		"empty body":    ``,
		"not an object": `["alice"]`,
		"unknown field": `{"username":"alice","email":"a@b.com","password":"hunter2secret","is_admin":true}`,
		"trailing data": `{"username":"alice","email":"a@b.com","password":"hunter2secret"}{}`,
		"wrong types":   `{"username":123,"email":"a@b.com","password":"hunter2secret"}`,
	}

	for name, body := range bodies {
		t.Run(name, func(t *testing.T) {
			svc := &fakeRegistrar{registration: registeredUser()}
			rec := postRegister(t, newTestHandler(svc, newFakeSender()), body)
			assertError(t, rec, http.StatusBadRequest, "MALFORMED_REQUEST")
			if svc.registerCall != nil {
				t.Error("a malformed body reached the service")
			}
		})
	}
}

func TestRegisterRejectsANonJSONContentType(t *testing.T) {
	rec := postRegister(t, newTestHandler(&fakeRegistrar{}, newFakeSender()),
		`{"username":"alice","email":"a@b.com","password":"hunter2secret"}`, "text/plain")

	assertError(t, rec, http.StatusBadRequest, "MALFORMED_REQUEST")
}

func TestRegisterAcceptsAContentTypeWithParameters(t *testing.T) {
	svc := &fakeRegistrar{registration: registeredUser()}
	rec := postRegister(t, newTestHandler(svc, newFakeSender()),
		`{"username":"alice","email":"alice@example.com","password":"hunter2secret"}`, "application/json; charset=utf-8")

	if rec.Code != http.StatusCreated {
		t.Errorf("status = %d, want %d; body: %s", rec.Code, http.StatusCreated, rec.Body.String())
	}
}

func TestRegisterReturns422WithTheOffendingFields(t *testing.T) {
	svc := &fakeRegistrar{registerErr: &auth.ValidationError{Fields: map[string]string{
		"username": "must be at least 3 characters",
		"password": "must be at least 8 characters",
	}}}

	rec := postRegister(t, newTestHandler(svc, newFakeSender()), `{"username":"ab","email":"a@b.com","password":"short"}`)

	body := assertError(t, rec, http.StatusUnprocessableEntity, "VALIDATION_ERROR")
	fields, ok := body["fields"].(map[string]any)
	if !ok {
		t.Fatalf("fields = %v, want an object naming the invalid fields", body["fields"])
	}
	if fields["username"] != "must be at least 3 characters" {
		t.Errorf("fields[username] = %v, want the service's message", fields["username"])
	}
	if fields["password"] != "must be at least 8 characters" {
		t.Errorf("fields[password] = %v, want the service's message", fields["password"])
	}
}

func TestRegisterReturns409ForADuplicateEmail(t *testing.T) {
	svc := &fakeRegistrar{registerErr: auth.ErrEmailTaken}

	rec := postRegister(t, newTestHandler(svc, newFakeSender()), `{"username":"alice","email":"a@b.com","password":"hunter2secret"}`)

	assertError(t, rec, http.StatusConflict, "EMAIL_ALREADY_EXISTS")
}

func TestRegisterReturns409ForADuplicateUsername(t *testing.T) {
	svc := &fakeRegistrar{registerErr: auth.ErrUsernameTaken}

	rec := postRegister(t, newTestHandler(svc, newFakeSender()), `{"username":"alice","email":"a@b.com","password":"hunter2secret"}`)

	assertError(t, rec, http.StatusConflict, "USERNAME_ALREADY_EXISTS")
}

func TestRegisterReturns500AndHidesTheCause(t *testing.T) {
	svc := &fakeRegistrar{registerErr: errors.New("pq: password authentication failed for user \"postgres\"")}

	rec := postRegister(t, newTestHandler(svc, newFakeSender()), `{"username":"alice","email":"a@b.com","password":"hunter2secret"}`)

	assertError(t, rec, http.StatusInternalServerError, "INTERNAL_ERROR")
	if strings.Contains(rec.Body.String(), "postgres") {
		t.Errorf("the internal error leaked to the client: %s", rec.Body.String())
	}
}

func TestRegisterRejectsAnOversizedBody(t *testing.T) {
	huge := `{"username":"` + strings.Repeat("a", 2<<20) + `","email":"a@b.com","password":"hunter2secret"}`

	rec := postRegister(t, newTestHandler(&fakeRegistrar{}, newFakeSender()), huge)

	assertError(t, rec, http.StatusBadRequest, "MALFORMED_REQUEST")
}

func getVerify(t *testing.T, h http.Handler, query string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/verify-email"+query, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestVerifyEmailReturns200(t *testing.T) {
	svc := &fakeRegistrar{verified: &auth.User{
		ID: 7, Username: "alice", Email: "alice@example.com", EloRating: 1200, EmailVerified: true,
	}}

	rec := getVerify(t, newTestHandler(svc, newFakeSender()), "?token=tok-en_value")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if svc.verifyArg != "tok-en_value" {
		t.Errorf("service received token %q, want %q", svc.verifyArg, "tok-en_value")
	}
	body := decodeBody(t, rec)
	if body["user_id"] != float64(7) {
		t.Errorf("user_id = %v, want 7", body["user_id"])
	}
	if body["email_verified"] != true {
		t.Errorf("email_verified = %v, want true", body["email_verified"])
	}
}

func TestVerifyEmailRejectsAMissingToken(t *testing.T) {
	svc := &fakeRegistrar{}

	rec := getVerify(t, newTestHandler(svc, newFakeSender()), "")

	assertError(t, rec, http.StatusBadRequest, "INVALID_TOKEN")
	if svc.verifyArg != "" {
		t.Error("an empty token reached the service")
	}
}

func TestVerifyEmailReturns400ForAnInvalidToken(t *testing.T) {
	svc := &fakeRegistrar{verifyErr: auth.ErrInvalidToken}

	rec := getVerify(t, newTestHandler(svc, newFakeSender()), "?token=nope")

	assertError(t, rec, http.StatusBadRequest, "INVALID_TOKEN")
}

func TestVerifyEmailReturns410ForAnExpiredToken(t *testing.T) {
	svc := &fakeRegistrar{verifyErr: auth.ErrTokenExpired}

	rec := getVerify(t, newTestHandler(svc, newFakeSender()), "?token=stale")

	assertError(t, rec, http.StatusGone, "TOKEN_EXPIRED")
}

func TestVerifyEmailReturns500AndHidesTheCause(t *testing.T) {
	svc := &fakeRegistrar{verifyErr: errors.New("pq: connection refused on 10.0.0.5")}

	rec := getVerify(t, newTestHandler(svc, newFakeSender()), "?token=whatever")

	assertError(t, rec, http.StatusInternalServerError, "INTERNAL_ERROR")
	if strings.Contains(rec.Body.String(), "10.0.0.5") {
		t.Errorf("the internal error leaked to the client: %s", rec.Body.String())
	}
}
