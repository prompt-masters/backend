package handler

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/google/uuid"
	"github.com/prompt-masters/backend/internal/domain"
	"github.com/prompt-masters/backend/internal/service"
)

type stubChallengeRepository struct {
	challenges []*domain.Challenge
	err        error
}

func (s *stubChallengeRepository) List(_ context.Context, filter domain.ChallengeFilter) ([]*domain.Challenge, error) {
	if s.err != nil {
		return nil, s.err
	}
	out := []*domain.Challenge{}
	for _, c := range s.challenges {
		if filter.Category != nil && c.Category != *filter.Category {
			continue
		}
		if filter.Difficulty != nil && c.Difficulty != *filter.Difficulty {
			continue
		}
		out = append(out, c)
	}
	return out, nil
}

func (s *stubChallengeRepository) GetByID(_ context.Context, id uuid.UUID) (*domain.Challenge, error) {
	if s.err != nil {
		return nil, s.err
	}
	for _, c := range s.challenges {
		if c.ID == id {
			return c, nil
		}
	}
	return nil, domain.ErrNotFound
}

// newChallengeMux registers the handler under the same patterns as
// api.Server.Routes so path values and route precedence are exercised.
func newChallengeMux(repo *stubChallengeRepository) http.Handler {
	h := NewChallengeHandler(service.NewChallengeService(repo), log.New(io.Discard, "", 0))
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/challenges", h.List)
	mux.HandleFunc("GET /api/v1/challenges/categories", h.Categories)
	mux.HandleFunc("GET /api/v1/challenges/{id}", h.Get)
	return mux
}

var (
	codingEasy = &domain.Challenge{
		ID:         uuid.MustParse("11111111-1111-1111-1111-111111111111"),
		Slug:       "coding-easy",
		Title:      "Fizz",
		Category:   domain.CategoryCoding,
		Difficulty: domain.DifficultyEasy,
		Constraints: domain.ChallengeConstraints{
			TimeLimitSeconds: 120,
			MaxPromptChars:   280,
			ForbiddenWords:   []string{"fizzbuzz"},
			RequiredElements: []string{"a loop"},
			OutputFormat:     "python",
		},
		JudgeCriteria: domain.JudgeCriteria{
			PassingScore: 60,
			Criteria: []domain.JudgeCriterion{
				{Name: "correctness", Description: "Code runs.", Weight: 1, MaxScore: 10},
			},
		},
	}
	reasoningHard = &domain.Challenge{
		ID:         uuid.MustParse("22222222-2222-2222-2222-222222222222"),
		Slug:       "reasoning-hard",
		Category:   domain.CategoryReasoning,
		Difficulty: domain.DifficultyHard,
	}
)

type challengeBody struct {
	ID            string                      `json:"id"`
	Slug          string                      `json:"slug"`
	Category      string                      `json:"category"`
	Difficulty    string                      `json:"difficulty"`
	Constraints   domain.ChallengeConstraints `json:"constraints"`
	JudgeCriteria domain.JudgeCriteria        `json:"judge_criteria"`
}

func serve(t *testing.T, handler http.Handler, target string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
	return rec
}

func decodeBody[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	var body T
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decoding response body: %v", err)
	}
	return body
}

func TestChallengeHandlerList(t *testing.T) {
	mux := newChallengeMux(&stubChallengeRepository{challenges: []*domain.Challenge{codingEasy, reasoningHard}})

	tests := []struct {
		name       string
		target     string
		wantStatus int
		wantSlugs  []string
		wantFields []string
	}{
		{name: "all challenges", target: "/api/v1/challenges", wantStatus: http.StatusOK, wantSlugs: []string{"coding-easy", "reasoning-hard"}},
		{name: "filter by category", target: "/api/v1/challenges?category=reasoning", wantStatus: http.StatusOK, wantSlugs: []string{"reasoning-hard"}},
		{name: "filter by difficulty", target: "/api/v1/challenges?difficulty=easy", wantStatus: http.StatusOK, wantSlugs: []string{"coding-easy"}},
		{name: "combined filters", target: "/api/v1/challenges?category=coding&difficulty=hard", wantStatus: http.StatusOK, wantSlugs: []string{}},
		{name: "invalid category", target: "/api/v1/challenges?category=cooking", wantStatus: http.StatusBadRequest, wantFields: []string{"category"}},
		{name: "invalid difficulty", target: "/api/v1/challenges?difficulty=insane", wantStatus: http.StatusBadRequest, wantFields: []string{"difficulty"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := serve(t, mux, tt.target)
			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d; body: %s", rec.Code, tt.wantStatus, rec.Body)
			}

			if tt.wantFields != nil {
				body := decodeBody[struct {
					Fields map[string]string `json:"fields"`
				}](t, rec)
				for _, f := range tt.wantFields {
					if body.Fields[f] == "" {
						t.Errorf("fields[%q] is empty; got %v", f, body.Fields)
					}
				}
				return
			}

			body := decodeBody[struct {
				Data []challengeBody `json:"data"`
			}](t, rec)
			slugs := []string{}
			for _, c := range body.Data {
				slugs = append(slugs, c.Slug)
			}
			if !reflect.DeepEqual(slugs, tt.wantSlugs) {
				t.Errorf("slugs = %v, want %v", slugs, tt.wantSlugs)
			}
		})
	}
}

func TestChallengeHandlerGet(t *testing.T) {
	mux := newChallengeMux(&stubChallengeRepository{challenges: []*domain.Challenge{codingEasy}})

	tests := []struct {
		name       string
		target     string
		wantStatus int
	}{
		{name: "existing challenge", target: "/api/v1/challenges/" + codingEasy.ID.String(), wantStatus: http.StatusOK},
		{name: "unknown ID", target: "/api/v1/challenges/" + uuid.NewString(), wantStatus: http.StatusNotFound},
		{name: "malformed ID", target: "/api/v1/challenges/not-a-uuid", wantStatus: http.StatusNotFound},
		{name: "numeric ID", target: "/api/v1/challenges/42", wantStatus: http.StatusNotFound},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := serve(t, mux, tt.target)
			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d; body: %s", rec.Code, tt.wantStatus, rec.Body)
			}
		})
	}
}

func TestChallengeHandlerGetReturnsConstraintsAndJudgeCriteria(t *testing.T) {
	mux := newChallengeMux(&stubChallengeRepository{challenges: []*domain.Challenge{codingEasy}})

	rec := serve(t, mux, "/api/v1/challenges/"+codingEasy.ID.String())
	body := decodeBody[struct {
		Data challengeBody `json:"data"`
	}](t, rec)

	if body.Data.ID != codingEasy.ID.String() || body.Data.Category != "coding" || body.Data.Difficulty != "easy" {
		t.Errorf("data = %+v, want the coding-easy challenge", body.Data)
	}
	if !reflect.DeepEqual(body.Data.Constraints, codingEasy.Constraints) {
		t.Errorf("constraints = %+v, want %+v", body.Data.Constraints, codingEasy.Constraints)
	}
	if !reflect.DeepEqual(body.Data.JudgeCriteria, codingEasy.JudgeCriteria) {
		t.Errorf("judge_criteria = %+v, want %+v", body.Data.JudgeCriteria, codingEasy.JudgeCriteria)
	}
}

func TestChallengeHandlerRepositoryFailureIsInternalError(t *testing.T) {
	mux := newChallengeMux(&stubChallengeRepository{err: errors.New("connection refused")})

	for _, target := range []string{"/api/v1/challenges", "/api/v1/challenges/" + codingEasy.ID.String()} {
		rec := serve(t, mux, target)
		if rec.Code != http.StatusInternalServerError {
			t.Errorf("GET %s status = %d, want 500", target, rec.Code)
		}
	}
}

func TestChallengeHandlerCategories(t *testing.T) {
	mux := newChallengeMux(&stubChallengeRepository{})

	rec := serve(t, mux, "/api/v1/challenges/categories")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body)
	}

	body := decodeBody[struct {
		Data []domain.Category `json:"data"`
	}](t, rec)
	if !reflect.DeepEqual(body.Data, domain.Categories()) {
		t.Errorf("data = %v, want %v", body.Data, domain.Categories())
	}
}
