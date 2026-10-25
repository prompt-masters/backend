package api_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"slices"
	"sync"
	"testing"

	"github.com/google/uuid"
)

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
	})
}
