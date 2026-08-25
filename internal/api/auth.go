// Package api holds the HTTP layer: request decoding, domain-error mapping
// and JSON responses. It owns no business rules of its own.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/prompt-masters/backend/internal/auth"
	"github.com/prompt-masters/backend/internal/mail"
	"github.com/prompt-masters/backend/internal/util"
)

const (
	// maxRegisterBody caps the request body. The largest legitimate
	// registration is a few hundred bytes.
	maxRegisterBody = 16 << 10

	// verificationSendTimeout bounds the background SMTP conversation.
	verificationSendTimeout = 30 * time.Second

	verifyEmailPath = "/api/v1/auth/verify-email"
)

// Registrar is the slice of *auth.Service this handler needs.
type Registrar interface {
	Register(ctx context.Context, in auth.RegisterInput) (*auth.Registration, error)
	VerifyEmail(ctx context.Context, rawToken string) (*auth.User, error)
	Login(ctx context.Context, in auth.LoginInput) (*auth.User, error)
}

// AuthHandler serves registration and email verification.
type AuthHandler struct {
	svc     Registrar
	sender  mail.Sender
	tokens  *util.TokenIssuer
	baseURL string
	logger  *log.Logger
}

// NewAuthHandler builds the handler. baseURL is the public origin used to
// construct verification links, e.g. "https://api.example.com".
func NewAuthHandler(svc Registrar, sender mail.Sender, tokens *util.TokenIssuer, baseURL string, logger *log.Logger) *AuthHandler {
	return &AuthHandler{
		svc:     svc,
		sender:  sender,
		tokens:  tokens,
		baseURL: strings.TrimSuffix(baseURL, "/"),
		logger:  logger,
	}
}

func (h *AuthHandler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/auth/register", h.register)
	mux.HandleFunc("GET "+verifyEmailPath, h.verifyEmail)
	mux.HandleFunc("POST /api/v1/auth/login", h.login)
}

type loginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type loginResponse struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	ExpiresIn   int    `json:"expires_in"`
}

// login exchanges an email and password for an access token.
//
// Only a request we could not read at all is a 400; anything that got as far
// as a credential check and failed is a 401 with a single generic message,
// whether the address is unknown, the password wrong, or the email
// unverified. Telling them apart would let anyone map which addresses have
// accounts.
func (h *AuthHandler) login(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, h.logger, http.StatusBadRequest, CodeMalformedRequest, err.Error())
		return
	}

	user, err := h.svc.Login(r.Context(), auth.LoginInput{
		Email:    req.Email,
		Password: req.Password,
	})
	if err != nil {
		if errors.Is(err, auth.ErrInvalidCredentials) {
			h.writeUnauthorized(w)
			return
		}
		writeInternalError(w, h.logger, err)
		return
	}

	token, err := h.tokens.Issue(util.TokenSubject{
		UserID:   user.ID,
		Username: user.Username,
		Email:    user.Email,
	})
	if err != nil {
		writeInternalError(w, h.logger, err)
		return
	}

	writeJSON(w, h.logger, http.StatusOK, loginResponse{
		AccessToken: token,
		TokenType:   "Bearer",
		ExpiresIn:   int(h.tokens.TTL().Seconds()),
	})
}

// writeUnauthorized sends the single response every failed login gets. RFC
// 9110 requires a 401 to name the scheme the client should authenticate with.
func (h *AuthHandler) writeUnauthorized(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", `Bearer realm="api"`)
	writeError(w, h.logger, http.StatusUnauthorized, CodeInvalidCredentials, "Email or password is incorrect.")
}

type registerRequest struct {
	Username string `json:"username"`
	Email    string `json:"email"`
	Password string `json:"password"`
}

type registerResponse struct {
	UserID    int64  `json:"user_id"`
	Username  string `json:"username"`
	Email     string `json:"email"`
	EloRating int    `json:"elo_rating"`
}

type verifyEmailResponse struct {
	UserID        int64  `json:"user_id"`
	Username      string `json:"username"`
	Email         string `json:"email"`
	EloRating     int    `json:"elo_rating"`
	EmailVerified bool   `json:"email_verified"`
}

func (h *AuthHandler) register(w http.ResponseWriter, r *http.Request) {
	var req registerRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, h.logger, http.StatusBadRequest, CodeMalformedRequest, err.Error())
		return
	}

	reg, err := h.svc.Register(r.Context(), auth.RegisterInput{
		Username: req.Username,
		Email:    req.Email,
		Password: req.Password,
	})
	if err != nil {
		h.writeRegisterError(w, err)
		return
	}

	// The account exists; the email is a follow-up. Dispatching it in the
	// background keeps a slow or broken SMTP server from delaying or failing
	// a registration that already succeeded.
	h.dispatchVerificationEmail(reg)

	writeJSON(w, h.logger, http.StatusCreated, registerResponse{
		UserID:    reg.User.ID,
		Username:  reg.User.Username,
		Email:     reg.User.Email,
		EloRating: reg.User.EloRating,
	})
}

func (h *AuthHandler) writeRegisterError(w http.ResponseWriter, err error) {
	var vErr *auth.ValidationError
	switch {
	case errors.As(err, &vErr):
		writeJSON(w, h.logger, http.StatusUnprocessableEntity, errorResponse{
			ErrorCode: CodeValidationError,
			Message:   "The submitted values are not valid.",
			Fields:    vErr.Fields,
		})
	case errors.Is(err, auth.ErrEmailTaken):
		writeError(w, h.logger, http.StatusConflict, CodeEmailAlreadyExists, "That email address is already registered.")
	case errors.Is(err, auth.ErrUsernameTaken):
		writeError(w, h.logger, http.StatusConflict, CodeUsernameAlreadyExists, "That username is already taken.")
	default:
		writeInternalError(w, h.logger, err)
	}
}

// dispatchVerificationEmail sends the verification link on its own goroutine,
// with a context independent of the request: the request's context is
// cancelled the moment the response is written.
func (h *AuthHandler) dispatchVerificationEmail(reg *auth.Registration) {
	link := h.baseURL + verifyEmailPath + "?token=" + url.QueryEscape(reg.VerificationToken)
	to, username := reg.User.Email, reg.User.Username

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), verificationSendTimeout)
		defer cancel()

		if err := h.sender.SendVerification(ctx, to, username, link); err != nil {
			// Nothing to tell the client — they already have their 201. The
			// account exists but is unverifiable until the mail is retried.
			h.logger.Printf("sending verification email to %s: %v", to, err)
		}
	}()
}

func (h *AuthHandler) verifyEmail(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("token")
	if token == "" {
		writeError(w, h.logger, http.StatusBadRequest, CodeInvalidToken, "This verification link is not valid.")
		return
	}

	user, err := h.svc.VerifyEmail(r.Context(), token)
	if err != nil {
		switch {
		case errors.Is(err, auth.ErrInvalidToken):
			writeError(w, h.logger, http.StatusBadRequest, CodeInvalidToken, "This verification link is not valid or has already been used.")
		case errors.Is(err, auth.ErrTokenExpired):
			writeError(w, h.logger, http.StatusGone, CodeTokenExpired, "This verification link has expired.")
		default:
			writeInternalError(w, h.logger, err)
		}
		return
	}

	writeJSON(w, h.logger, http.StatusOK, verifyEmailResponse{
		UserID:        user.ID,
		Username:      user.Username,
		Email:         user.Email,
		EloRating:     user.EloRating,
		EmailVerified: user.EmailVerified,
	})
}

// decodeJSON reads a single JSON object into dst. It rejects the wrong
// content type, an oversized body, unknown fields and trailing data, so a
// caller can treat any error as a malformed request.
func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	if err := requireJSONContentType(r); err != nil {
		return err
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxRegisterBody)

	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()

	if err := dec.Decode(dst); err != nil {
		return describeDecodeError(err)
	}
	if dec.More() {
		return errors.New("Request body must contain a single JSON object.")
	}
	return nil
}

func requireJSONContentType(r *http.Request) error {
	ct := r.Header.Get("Content-Type")
	if ct == "" {
		return errors.New("Content-Type must be application/json.")
	}
	mediaType, _, err := mime.ParseMediaType(ct)
	if err != nil || mediaType != "application/json" {
		return errors.New("Content-Type must be application/json.")
	}
	return nil
}

// describeDecodeError turns a decoder error into something safe to return.
// Messages name the offending field but never echo the body, which holds the
// submitted password.
func describeDecodeError(err error) error {
	var syntaxErr *json.SyntaxError
	var typeErr *json.UnmarshalTypeError
	var maxBytesErr *http.MaxBytesError

	switch {
	case errors.As(err, &syntaxErr):
		return errors.New("Request body is not valid JSON.")
	case errors.As(err, &typeErr):
		if typeErr.Field != "" {
			return errors.New("Field " + typeErr.Field + " has the wrong type.")
		}
		return errors.New("Request body is not a JSON object.")
	case errors.As(err, &maxBytesErr):
		return errors.New("Request body is too large.")
	case errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF):
		return errors.New("Request body is empty or truncated.")
	case strings.HasPrefix(err.Error(), "json: unknown field "):
		return errors.New("Request body contains an unrecognised field.")
	default:
		return errors.New("Request body could not be read.")
	}
}
