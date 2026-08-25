package api

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/prompt-masters/backend/internal/auth"
)

func loggedInUser() *auth.User {
	return &auth.User{
		ID: 7, Username: "alice", Email: "alice@example.com",
		PasswordHash: "$2a$10$notarealhash", EloRating: 1200, EmailVerified: true,
	}
}

func postLogin(t *testing.T, h http.Handler, body string, contentType ...string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", strings.NewReader(body))
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

const validLogin = `{"email":"alice@example.com","password":"hunter2secret"}`

func TestLoginReturns200WithAnAccessToken(t *testing.T) {
	svc := &fakeRegistrar{loggedIn: loggedInUser()}

	rec := postLogin(t, newTestHandler(svc, newFakeSender()), validLogin)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	body := decodeBody(t, rec)

	token, ok := body["access_token"].(string)
	if !ok || token == "" {
		t.Fatalf("access_token = %v, want a non-empty string", body["access_token"])
	}
	if body["token_type"] != "Bearer" {
		t.Errorf("token_type = %v, want %q", body["token_type"], "Bearer")
	}
	if body["expires_in"] != float64(900) {
		t.Errorf("expires_in = %v, want 900", body["expires_in"])
	}
	if len(body) != 3 {
		t.Errorf("body has %d fields (%v), want exactly 3", len(body), body)
	}
}

func TestLoginIssuesATokenCarryingTheAccount(t *testing.T) {
	svc := &fakeRegistrar{loggedIn: loggedInUser()}

	rec := postLogin(t, newTestHandler(svc, newFakeSender()), validLogin)

	token, _ := decodeBody(t, rec)["access_token"].(string)
	claims, err := testIssuer(t).Parse(token)
	if err != nil {
		t.Fatalf("the issued token does not validate: %v", err)
	}

	if claims.UserID != 7 {
		t.Errorf("UserID = %d, want 7", claims.UserID)
	}
	if claims.Username != "alice" {
		t.Errorf("Username = %q, want %q", claims.Username, "alice")
	}
	if claims.Email != "alice@example.com" {
		t.Errorf("Email = %q, want %q", claims.Email, "alice@example.com")
	}
}

func TestLoginPassesTheCredentialsToTheService(t *testing.T) {
	svc := &fakeRegistrar{loggedIn: loggedInUser()}

	postLogin(t, newTestHandler(svc, newFakeSender()), validLogin)

	if svc.loginArg == nil {
		t.Fatal("the service was never called")
	}
	if svc.loginArg.Email != "alice@example.com" || svc.loginArg.Password != "hunter2secret" {
		t.Errorf("service received %+v, want the submitted credentials", *svc.loginArg)
	}
}

func TestLoginReturns401ForBadCredentials(t *testing.T) {
	svc := &fakeRegistrar{loginErr: auth.ErrInvalidCredentials}

	rec := postLogin(t, newTestHandler(svc, newFakeSender()), validLogin)

	assertError(t, rec, http.StatusUnauthorized, "INVALID_CREDENTIALS")
}

// RFC 9110 requires a 401 to say how to authenticate.
func TestLoginSetsWWWAuthenticateOn401(t *testing.T) {
	svc := &fakeRegistrar{loginErr: auth.ErrInvalidCredentials}

	rec := postLogin(t, newTestHandler(svc, newFakeSender()), validLogin)

	if got := rec.Header().Get("WWW-Authenticate"); !strings.HasPrefix(got, "Bearer") {
		t.Errorf("WWW-Authenticate = %q, want it to start with Bearer", got)
	}
}

// The 401 body must not hint at whether the address has an account.
func TestLoginResponseDoesNotRevealWhetherTheAccountExists(t *testing.T) {
	svc := &fakeRegistrar{loginErr: auth.ErrInvalidCredentials}

	rec := postLogin(t, newTestHandler(svc, newFakeSender()), validLogin)

	lower := strings.ToLower(rec.Body.String())
	for _, leak := range []string{"not found", "no such", "unknown user", "unverified", "not verified", "wrong password", "alice@example.com"} {
		if strings.Contains(lower, leak) {
			t.Errorf("401 body leaks %q: %s", leak, rec.Body.String())
		}
	}
}

func TestLoginNeverReturnsThePassword(t *testing.T) {
	svc := &fakeRegistrar{loggedIn: loggedInUser()}

	rec := postLogin(t, newTestHandler(svc, newFakeSender()), validLogin)

	for _, leak := range []string{"hunter2secret", "$2a$10$notarealhash", "password_hash"} {
		if strings.Contains(rec.Body.String(), leak) {
			t.Errorf("response leaks %q: %s", leak, rec.Body.String())
		}
	}
}

func TestLoginRejectsAMalformedBody(t *testing.T) {
	for name, body := range map[string]string{
		"invalid json":  `{"email":`,
		"empty body":    ``,
		"unknown field": `{"email":"a@b.com","password":"x","remember":true}`,
		"wrong types":   `{"email":123,"password":"x"}`,
	} {
		t.Run(name, func(t *testing.T) {
			svc := &fakeRegistrar{loggedIn: loggedInUser()}
			rec := postLogin(t, newTestHandler(svc, newFakeSender()), body)
			assertError(t, rec, http.StatusBadRequest, "MALFORMED_REQUEST")
			if svc.loginArg != nil {
				t.Error("a malformed body reached the service")
			}
		})
	}
}

func TestLoginRejectsANonJSONContentType(t *testing.T) {
	rec := postLogin(t, newTestHandler(&fakeRegistrar{}, newFakeSender()), validLogin, "text/plain")

	assertError(t, rec, http.StatusBadRequest, "MALFORMED_REQUEST")
}

func TestLoginReturns500AndHidesTheCause(t *testing.T) {
	svc := &fakeRegistrar{loginErr: errors.New("pq: connection refused on 10.0.0.5")}

	rec := postLogin(t, newTestHandler(svc, newFakeSender()), validLogin)

	assertError(t, rec, http.StatusInternalServerError, "INTERNAL_ERROR")
	if strings.Contains(rec.Body.String(), "10.0.0.5") {
		t.Errorf("the internal error leaked to the client: %s", rec.Body.String())
	}
}
