package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/prompt-masters/backend/internal/ai"
	"github.com/prompt-masters/backend/internal/service"
)

// failingAI answers helpers through next until failWith sets an error.
type failingAI struct {
	next service.HelperAI
	mu   sync.Mutex
	err  error
}

func (f *failingAI) Execute(ctx context.Context, in ai.ExecuteInput) (ai.ExecuteOutput, error) {
	f.mu.Lock()
	err := f.err
	f.mu.Unlock()
	if err != nil {
		return ai.ExecuteOutput{}, err
	}
	return f.next.Execute(ctx, in)
}

func (f *failingAI) failWith(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.err = err
}

type usageRow struct {
	Type, Status, ErrorCode string
	Cost                    int
}

// dbBalance reads the player's helper balance straight from the database;
// -1 means they have none.
func (a *testAPI) dbBalance(u testUser) int {
	a.t.Helper()
	balance := -1
	rows, err := a.pool.Query(context.Background(), "SELECT balance FROM helper_balances WHERE player_id = $1", u.ID)
	if err != nil {
		a.t.Fatalf("reading balance: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		if err := rows.Scan(&balance); err != nil {
			a.t.Fatalf("reading balance: %v", err)
		}
	}
	return balance
}

// dbUsage reads the player's helper_usage rows, oldest first.
func (a *testAPI) dbUsage(u testUser) []usageRow {
	a.t.Helper()
	rows, err := a.pool.Query(context.Background(),
		"SELECT helper_type, status, COALESCE(error_code, ''), cost FROM helper_usage WHERE player_id = $1 ORDER BY created_at_ms, id", u.ID)
	if err != nil {
		a.t.Fatalf("reading usage: %v", err)
	}
	defer rows.Close()
	var out []usageRow
	for rows.Next() {
		var r usageRow
		if err := rows.Scan(&r.Type, &r.Status, &r.ErrorCode, &r.Cost); err != nil {
			a.t.Fatalf("reading usage: %v", err)
		}
		out = append(out, r)
	}
	return out
}

// delivered sums the player's delivered usage rows.
func delivered(rows []usageRow) (count, spent int) {
	for _, r := range rows {
		if r.Status == "delivered" {
			count++
			spent += r.Cost
		}
	}
	return count, spent
}

type helperJSON struct {
	UsageID          string `json:"usage_id"`
	HelperType       string `json:"helper_type"`
	Cost             int    `json:"cost"`
	Content          string `json:"content"`
	RemainingBalance int    `json:"remaining_balance"`
}

type helperOverviewJSON struct {
	Balance int            `json:"balance"`
	Prices  map[string]int `json:"prices"`
	Usage   []struct {
		UsageID    string `json:"usage_id"`
		HelperType string `json:"helper_type"`
		Cost       int    `json:"cost"`
		Status     string `json:"status"`
	} `json:"usage"`
}

// buyHelper posts a helper request with an Idempotency-Key header.
func (a *testAPI) buyHelper(u testUser, roundID, helper, key string, body map[string]any) apiResponse {
	a.t.Helper()
	raw, _ := json.Marshal(body)
	req, _ := http.NewRequest("POST", a.server.URL+"/api/v1/rounds/"+roundID+"/helpers/"+helper, bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+u.Token)
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	resp, err := a.server.Client().Do(req)
	if err != nil {
		a.t.Fatalf("buying %s: %v", helper, err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return apiResponse{Status: resp.StatusCode, Body: out}
}

func (a *testAPI) helperOverview(u testUser, roundID string) helperOverviewJSON {
	a.t.Helper()
	return decode[helperOverviewJSON](a.t, a.expect(http.StatusOK, "GET", "/api/v1/rounds/"+roundID+"/helpers", &u, nil))
}

// startedRound starts a game for the players and returns its round ID.
func startedRound(t *testing.T, a *liveTestAPI, host testUser, guests ...testUser) string {
	t.Helper()
	g := a.createGame(host, 4, guests...)
	a.expect(http.StatusOK, "POST", "/api/v1/games/"+g.ID+"/start", &host, nil)
	return decode[struct {
		RoundID string `json:"round_id"`
	}](t, a.expect(http.StatusOK, "GET", "/api/v1/games/"+g.ID+"/live", &host, nil)).RoundID
}

// buyConcurrently fires the helpers at once, each with its own key unless
// key is set, and returns the responses.
func buyConcurrently(a *liveTestAPI, u testUser, roundID string, helpers []string, key string) []apiResponse {
	out := make([]apiResponse, len(helpers))
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i, h := range helpers {
		wg.Go(func() {
			<-start
			k := key
			if k == "" {
				k = uuid.NewString()
			}
			out[i] = a.buyHelper(u, roundID, h, k, map[string]any{"prompt": "my draft"})
		})
	}
	close(start)
	wg.Wait()
	return out
}

func TestHelpersEndToEnd(t *testing.T) {
	a := newLiveTestAPI(t)
	host, guest, outsider := a.user("host"), a.user("guest"), a.user("outsider")
	round := startedRound(t, a, host, guest)

	if b := a.dbBalance(host); b != 100 {
		t.Fatalf("stored starting balance = %d, want 100", b)
	}
	o := a.helperOverview(host, round)
	if o.Balance != 100 || o.Prices["hint"] != 10 || o.Prices["suggestion"] != 10 || o.Prices["review"] != 15 || len(o.Usage) != 0 {
		t.Fatalf("overview = %+v, want 100 points and the configured prices", o)
	}
	if resp := a.buyHelper(host, round, "hint", "", nil); resp.Status != http.StatusBadRequest {
		t.Errorf("no Idempotency-Key = %s, want 400", resp)
	}
	if resp := a.buyHelper(outsider, round, "hint", "k", nil); resp.Status != http.StatusForbidden {
		t.Errorf("outsider = %s, want 403", resp)
	}

	resp := a.buyHelper(host, round, "hint", "first", map[string]any{"prompt": "my draft"})
	got := decode[helperJSON](t, resp)
	if resp.Status != http.StatusOK || got.HelperType != "hint" || got.Cost != 10 || got.Content != "Hello" || got.RemainingBalance != 90 {
		t.Fatalf("hint = %s", resp)
	}
	if again := decode[helperJSON](t, a.buyHelper(host, round, "hint", "first", map[string]any{"prompt": "my draft"})); again.UsageID != got.UsageID || again.RemainingBalance != 90 {
		t.Errorf("repeated key = %+v, want the same usage and no charge", again)
	}
	if o := a.helperOverview(host, round); o.Balance != 90 || len(o.Usage) != 1 || o.Usage[0].Status != "delivered" {
		t.Errorf("overview after a hint = %+v", o)
	}
	if rows := a.dbUsage(host); a.dbBalance(host) != 90 || !slices.Equal(rows, []usageRow{{Type: "hint", Status: "delivered", Cost: 10}}) {
		t.Errorf("stored balance %d and usage %+v, want 90 and one delivered hint", a.dbBalance(host), rows)
	}
	if a.dbBalance(outsider) != -1 || len(a.dbUsage(outsider)) != 0 {
		t.Errorf("the outsider has a balance or usage")
	}
	if o := a.helperOverview(guest, round); o.Balance != 100 || len(o.Usage) != 0 {
		t.Errorf("the guest's overview shows %+v, want only their own (none)", o)
	}
}

// TestHelpersSimultaneousPurchases fires concurrent purchases at the real
// database: no balance ever goes negative and every success is one row.
func TestHelpersSimultaneousPurchases(t *testing.T) {
	a := newLiveTestAPI(t)
	host, guest, carol := a.user("host"), a.user("guest"), a.user("carol")
	round := startedRound(t, a, host, guest, carol)

	t.Run("20 reviews from 100 points", func(t *testing.T) {
		reviews := make([]string, 20)
		for i := range reviews {
			reviews[i] = "review"
		}
		var remaining []int
		refused := 0
		for _, resp := range buyConcurrently(a, host, round, reviews, "") {
			switch resp.Status {
			case http.StatusOK:
				remaining = append(remaining, decode[helperJSON](t, resp).RemainingBalance)
			case http.StatusPaymentRequired:
				var body struct {
					ErrorCode        string `json:"error_code"`
					RemainingBalance *int   `json:"remaining_balance"`
					Cost             int    `json:"cost"`
				}
				_ = json.Unmarshal(resp.Body, &body)
				if body.ErrorCode != "INSUFFICIENT_BALANCE" || body.RemainingBalance == nil || *body.RemainingBalance < 0 || *body.RemainingBalance >= 15 || body.Cost != 15 {
					t.Errorf("refusal body = %s", resp.Body)
				}
				refused++
			default:
				t.Errorf("unexpected response %s", resp)
			}
		}
		// remaining_balance is the balance when that helper was delivered, so
		// with purchases overlapping it can already reflect later ones.
		if len(remaining) != 6 || refused != 14 || slices.Min(remaining) < 10 || slices.Max(remaining) > 85 {
			t.Errorf("successes left %v (refused %d), want exactly 6, each between 85 and 10", remaining, refused)
		}
		o := a.helperOverview(host, round)
		delivered := 0
		for _, u := range o.Usage {
			if u.Status == "delivered" && u.HelperType == "review" && u.Cost == 15 {
				delivered++
			}
		}
		if o.Balance != 10 || delivered != 6 || len(o.Usage) != 6 {
			t.Errorf("balance %d with %d delivered of %d rows, want 10 and 6 of 6", o.Balance, delivered, len(o.Usage))
		}
		assertSpent(t, a, host, len(remaining))
	})

	t.Run("mixed hints and reviews", func(t *testing.T) {
		var mixed []string
		for i := range 20 {
			mixed = append(mixed, []string{"hint", "review"}[i%2])
		}
		spent, successes := 0, 0
		for _, resp := range buyConcurrently(a, guest, round, mixed, "") {
			if resp.Status == http.StatusOK {
				h := decode[helperJSON](t, resp)
				spent += h.Cost
				successes++
				if h.RemainingBalance < 0 {
					t.Errorf("negative remaining balance %d", h.RemainingBalance)
				}
			} else if resp.Status != http.StatusPaymentRequired {
				t.Errorf("unexpected response %s", resp)
			}
		}
		assertSpent(t, a, guest, successes)
		o := a.helperOverview(guest, round)
		if o.Balance != 100-spent || o.Balance < 0 || len(o.Usage) != successes || o.Balance >= 10 {
			t.Errorf("balance %d after spending %d over %d purchases (%d rows); want the rest, under the cheapest price", o.Balance, spent, successes, len(o.Usage))
		}
	})

	t.Run("one idempotency key sent concurrently charges once", func(t *testing.T) {
		hints := make([]string, 10)
		for i := range hints {
			hints[i] = "hint"
		}
		for _, resp := range buyConcurrently(a, carol, round, hints, "the-same-key") {
			if resp.Status != http.StatusOK && resp.Status != http.StatusConflict {
				t.Errorf("unexpected response %s", resp)
			}
		}
		if o := a.helperOverview(carol, round); o.Balance != 90 || len(o.Usage) != 1 {
			t.Errorf("balance %d with %d rows, want one charge of 10", o.Balance, len(o.Usage))
		}
		assertSpent(t, a, carol, 1)
	})
}

// assertSpent checks the database after concurrent purchases: the balance
// is not negative, it fell by exactly what the delivered rows cost, there is
// one row per success, and nothing is left pending or refunded.
func assertSpent(t *testing.T, a *liveTestAPI, u testUser, successes int) {
	t.Helper()
	rows := a.dbUsage(u)
	count, spent := delivered(rows)
	if b := a.dbBalance(u); b < 0 || b != 100-spent || count != successes || len(rows) != successes {
		t.Errorf("stored balance %d, %d delivered rows costing %d of %d rows; want 100-%d, %d rows, all delivered",
			b, count, spent, len(rows), spent, successes)
	}
}

// TestHelpersAIFailureRefunds checks, against the real database, that a
// helper the AI fails to produce is refunded and recorded as such.
func TestHelpersAIFailureRefunds(t *testing.T) {
	a := newLiveTestAPI(t)
	host, guest := a.user("host"), a.user("guest")
	round := startedRound(t, a, host, guest)
	draft := map[string]any{"prompt": "my draft"}

	if got := decode[helperJSON](t, a.buyHelper(host, round, "hint", "h1", draft)); got.RemainingBalance != 90 {
		t.Fatalf("hint left %d, want 90", got.RemainingBalance)
	}

	a.helperAI.failWith(&ai.AIError{Kind: ai.ErrKindOverloaded, Provider: "fake"})
	type failure struct {
		Error            string `json:"error"`
		ErrorCode        string `json:"error_code"`
		Refunded         bool   `json:"refunded"`
		RemainingBalance *int   `json:"remaining_balance"`
	}
	for _, attempt := range []string{"first", "retry with the same key"} {
		resp := a.buyHelper(host, round, "review", "r1", draft)
		var f failure
		_ = json.Unmarshal(resp.Body, &f)
		if resp.Status != http.StatusBadGateway || f.ErrorCode != "OVERLOADED" || !f.Refunded || f.RemainingBalance == nil || *f.RemainingBalance != 90 {
			t.Errorf("%s: failed review = %s, want 502 OVERLOADED, refunded, remaining_balance 90", attempt, resp)
		}
	}
	want := []usageRow{
		{Type: "hint", Status: "delivered", Cost: 10},
		{Type: "review", Status: "refunded", ErrorCode: "OVERLOADED", Cost: 15},
	}
	if rows := a.dbUsage(host); a.dbBalance(host) != 90 || !slices.Equal(rows, want) {
		t.Errorf("stored balance %d and usage %+v, want 90 and %+v", a.dbBalance(host), rows, want)
	}

	a.helperAI.failWith(nil)
	if got := decode[helperJSON](t, a.buyHelper(host, round, "review", "r2", draft)); got.RemainingBalance != 75 {
		t.Errorf("review after recovery left %d, want 75", got.RemainingBalance)
	}
	if o := a.helperOverview(host, round); o.Balance != 75 || len(o.Usage) != 3 || o.Usage[1].Status != "refunded" {
		t.Errorf("overview = %+v, want 75 and the refunded review in the history", o)
	}
}

// TestHelpersRefusedOutsideTheActiveRound checks that a closed round and an
// outsider are refused before anything is charged or recorded.
func TestHelpersRefusedOutsideTheActiveRound(t *testing.T) {
	a := newLiveTestAPI(t)
	host, guest, outsider := a.user("host"), a.user("guest"), a.user("outsider")
	round := startedRound(t, a, host, guest)
	draft := map[string]any{"prompt": "my draft"}

	if resp := a.buyHelper(outsider, round, "hint", "o1", draft); resp.Status != http.StatusForbidden {
		t.Errorf("outsider = %s, want 403", resp)
	}
	if resp := a.buyHelper(host, uuid.NewString(), "hint", "h0", draft); resp.Status != http.StatusNotFound {
		t.Errorf("unknown round = %s, want 404", resp)
	}

	// Time is up, but the round timer has not fired yet.
	now := time.Now().UnixMilli()
	if _, err := a.pool.Exec(context.Background(),
		"UPDATE games SET round_started_at_ms = $2, round_deadline_ms = $3 WHERE round_id = $1", round, now-2000, now-1000); err != nil {
		t.Fatal(err)
	}
	for _, helper := range []string{"hint", "suggestion", "review"} {
		resp := a.buyHelper(host, round, helper, "late-"+helper, draft)
		if resp.Status != http.StatusConflict || errorCode(t, resp) != "ROUND_NOT_ACTIVE" {
			t.Errorf("%s after the deadline = %s, want 409 ROUND_NOT_ACTIVE", helper, resp)
		}
	}
	if a.dbBalance(host) != 100 || len(a.dbUsage(host)) != 0 || a.dbBalance(outsider) != -1 || len(a.dbUsage(outsider)) != 0 {
		t.Errorf("refusals charged or recorded something: host %d %+v, outsider %d %+v",
			a.dbBalance(host), a.dbUsage(host), a.dbBalance(outsider), a.dbUsage(outsider))
	}
}
