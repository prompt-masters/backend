package postgres

import (
	"context"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/prompt-masters/backend/internal/db"
	"github.com/prompt-masters/backend/internal/domain"
)

// newTestQueries migrates a fresh, uniquely named schema in TEST_DATABASE_URL
// and returns queries bound to it. The schema is dropped when the test ends,
// so tests never touch existing tables and can run against a shared database.
func newTestQueries(t *testing.T) *db.Queries {
	t.Helper()

	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL is not set; skipping repository test")
	}
	ctx := context.Background()

	admin, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatalf("connecting to test database: %v", err)
	}
	defer admin.Close(ctx)

	schema := "test_" + uuid.NewString()[:8]
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+pgx.Identifier{schema}.Sanitize()); err != nil {
		t.Fatalf("creating schema: %v", err)
	}
	t.Cleanup(func() {
		conn, err := pgx.Connect(context.Background(), url)
		if err != nil {
			t.Errorf("connecting to drop schema: %v", err)
			return
		}
		defer conn.Close(context.Background())
		if _, err := conn.Exec(context.Background(), "DROP SCHEMA "+pgx.Identifier{schema}.Sanitize()+" CASCADE"); err != nil {
			t.Errorf("dropping schema: %v", err)
		}
	})

	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatalf("parsing TEST_DATABASE_URL: %v", err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema

	sqlDB := stdlib.OpenDB(*cfg.ConnConfig)
	defer sqlDB.Close()
	provider, err := goose.NewProvider(goose.DialectPostgres, sqlDB, os.DirFS("../../db/migrations"))
	if err != nil {
		t.Fatalf("creating migration provider: %v", err)
	}
	if _, err := provider.Up(ctx); err != nil {
		t.Fatalf("applying migrations: %v", err)
	}

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("creating pool: %v", err)
	}
	t.Cleanup(pool.Close)

	return db.New(pool)
}

func testChallenge(slug string, category domain.Category, difficulty domain.Difficulty) *domain.Challenge {
	return &domain.Challenge{
		Slug:        slug,
		Title:       "Title " + slug,
		Description: "Description " + slug,
		Category:    category,
		Difficulty:  difficulty,
		Constraints: domain.ChallengeConstraints{
			TimeLimitSeconds: 120,
			MaxPromptChars:   300,
			ForbiddenWords:   []string{"please"},
			RequiredElements: []string{"a title"},
			OutputFormat:     "markdown",
		},
		JudgeCriteria: domain.JudgeCriteria{
			PassingScore: 70,
			Criteria: []domain.JudgeCriterion{
				{Name: "relevance", Description: "Stays on topic.", Weight: 0.6, MaxScore: 10},
				{Name: "creativity", Description: "Original ideas.", Weight: 0.4, MaxScore: 10},
			},
		},
	}
}

func seedChallenges(t *testing.T, repo *ChallengeRepository, challenges ...*domain.Challenge) map[string]*domain.Challenge {
	t.Helper()
	bySlug := make(map[string]*domain.Challenge, len(challenges))
	for _, c := range challenges {
		created, err := repo.Upsert(context.Background(), c)
		if err != nil {
			t.Fatalf("Upsert(%q) error = %v", c.Slug, err)
		}
		bySlug[created.Slug] = created
	}
	return bySlug
}

func ptr[T any](v T) *T { return &v }

func TestChallengeRepositoryList(t *testing.T) {
	repo := NewChallengeRepository(newTestQueries(t))
	seedChallenges(t, repo,
		testChallenge("a-coding-easy", domain.CategoryCoding, domain.DifficultyEasy),
		testChallenge("b-coding-hard", domain.CategoryCoding, domain.DifficultyHard),
		testChallenge("c-reasoning-easy", domain.CategoryReasoning, domain.DifficultyEasy),
		testChallenge("d-reasoning-medium", domain.CategoryReasoning, domain.DifficultyMedium),
	)

	tests := []struct {
		name      string
		filter    domain.ChallengeFilter
		wantSlugs []string
	}{
		{
			name:      "no filter returns every challenge ordered by slug",
			wantSlugs: []string{"a-coding-easy", "b-coding-hard", "c-reasoning-easy", "d-reasoning-medium"},
		},
		{
			name:      "by category",
			filter:    domain.ChallengeFilter{Category: ptr(domain.CategoryCoding)},
			wantSlugs: []string{"a-coding-easy", "b-coding-hard"},
		},
		{
			name:      "by difficulty",
			filter:    domain.ChallengeFilter{Difficulty: ptr(domain.DifficultyEasy)},
			wantSlugs: []string{"a-coding-easy", "c-reasoning-easy"},
		},
		{
			name:      "by category and difficulty",
			filter:    domain.ChallengeFilter{Category: ptr(domain.CategoryReasoning), Difficulty: ptr(domain.DifficultyMedium)},
			wantSlugs: []string{"d-reasoning-medium"},
		},
		{
			name:      "no match returns an empty list",
			filter:    domain.ChallengeFilter{Category: ptr(domain.CategorySummarization)},
			wantSlugs: []string{},
		},
		{
			name:      "filter value containing SQL is treated as data",
			filter:    domain.ChallengeFilter{Category: ptr(domain.Category("coding' OR '1'='1"))},
			wantSlugs: []string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := repo.List(context.Background(), tt.filter)
			if err != nil {
				t.Fatalf("List() error = %v", err)
			}
			slugs := make([]string, 0, len(got))
			for _, c := range got {
				slugs = append(slugs, c.Slug)
			}
			if !reflect.DeepEqual(slugs, tt.wantSlugs) {
				t.Errorf("List() slugs = %v, want %v", slugs, tt.wantSlugs)
			}
		})
	}
}

func TestChallengeRepositoryGetByIDLoadsJSONBColumns(t *testing.T) {
	repo := NewChallengeRepository(newTestQueries(t))
	want := testChallenge("json-roundtrip", domain.CategoryDataExtraction, domain.DifficultyMedium)
	created := seedChallenges(t, repo, want)["json-roundtrip"]

	got, err := repo.GetByID(context.Background(), created.ID)
	if err != nil {
		t.Fatalf("GetByID() error = %v", err)
	}

	if got.ID != created.ID || got.Slug != want.Slug || got.Category != want.Category || got.Difficulty != want.Difficulty {
		t.Errorf("GetByID() = %+v, want fields of %+v", got, want)
	}
	if !reflect.DeepEqual(got.Constraints, want.Constraints) {
		t.Errorf("Constraints = %+v, want %+v", got.Constraints, want.Constraints)
	}
	if !reflect.DeepEqual(got.JudgeCriteria, want.JudgeCriteria) {
		t.Errorf("JudgeCriteria = %+v, want %+v", got.JudgeCriteria, want.JudgeCriteria)
	}
	if got.CreatedAt.IsZero() || got.UpdatedAt.IsZero() {
		t.Error("timestamps were not loaded")
	}
}

func TestChallengeRepositoryGetByIDReturnsNotFound(t *testing.T) {
	repo := NewChallengeRepository(newTestQueries(t))

	_, err := repo.GetByID(context.Background(), uuid.New())
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("GetByID() error = %v, want domain.ErrNotFound", err)
	}
}

func TestChallengeRepositoryUpsertIsIdempotentBySlug(t *testing.T) {
	queries := newTestQueries(t)
	repo := NewChallengeRepository(queries)
	ctx := context.Background()

	first, err := repo.Upsert(ctx, testChallenge("same-slug", domain.CategoryCoding, domain.DifficultyEasy))
	if err != nil {
		t.Fatalf("first Upsert() error = %v", err)
	}

	updated := testChallenge("same-slug", domain.CategoryCoding, domain.DifficultyHard)
	updated.Title = "Updated title"
	second, err := repo.Upsert(ctx, updated)
	if err != nil {
		t.Fatalf("second Upsert() error = %v", err)
	}

	if second.ID != first.ID {
		t.Errorf("Upsert changed the ID from %s to %s", first.ID, second.ID)
	}
	if second.Title != "Updated title" || second.Difficulty != domain.DifficultyHard {
		t.Errorf("Upsert did not overwrite fields: %+v", second)
	}

	all, err := repo.List(ctx, domain.ChallengeFilter{})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(all) != 1 {
		t.Errorf("List() returned %d challenges, want 1", len(all))
	}
}

func TestChallengeRepositoryUpsertRejectsUnknownCategory(t *testing.T) {
	repo := NewChallengeRepository(newTestQueries(t))

	_, err := repo.Upsert(context.Background(), testChallenge("bad", domain.Category("cooking"), domain.DifficultyEasy))
	if err == nil {
		t.Fatal("Upsert() error = nil, want the CHECK constraint to reject the category")
	}
}

func TestChallengeRepositoryListFailsOnMalformedJSONB(t *testing.T) {
	queries := newTestQueries(t)
	repo := NewChallengeRepository(queries)
	ctx := context.Background()

	_, err := queries.UpsertChallenge(ctx, db.UpsertChallengeParams{
		Slug:          "malformed",
		Title:         "t",
		Description:   "d",
		Category:      string(domain.CategoryCoding),
		Difficulty:    string(domain.DifficultyEasy),
		Constraints:   []byte(`{"time_limit_seconds": "soon"}`),
		JudgeCriteria: []byte(`{}`),
	})
	if err != nil {
		t.Fatalf("inserting malformed row: %v", err)
	}

	_, err = repo.List(ctx, domain.ChallengeFilter{})
	if err == nil {
		t.Fatal("List() error = nil, want a decoding error")
	}
	if want := "decoding constraints"; !strings.Contains(err.Error(), want) {
		t.Errorf("List() error = %v, want it to mention %q", err, want)
	}
}
