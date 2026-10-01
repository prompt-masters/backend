package api_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/prompt-masters/backend/internal/ws"
)

type submissionJSON struct {
	ID           string `json:"id"`
	PlayerID     string `json:"player_id"`
	TestID       string `json:"test_id"`
	Prompt       string `json:"prompt"`
	Output       string `json:"output"`
	PromptLength int    `json:"prompt_length"`
	TestCount    int    `json:"test_count"`
	Source       string `json:"source"`
	LockedAt     int64  `json:"locked_at"`
}

func errorCode(t *testing.T, resp apiResponse) string {
	t.Helper()
	var body struct {
		ErrorCode string `json:"error_code"`
	}
	_ = json.Unmarshal(resp.Body, &body)
	return body.ErrorCode
}

// TestSubmitJudgeAndPublishEndToEnd plays a round through the real stack:
// tests, final submissions, anonymous judging with a fake judge, results
// published to the room, and the game moving on to the next round.
func TestSubmitJudgeAndPublishEndToEnd(t *testing.T) {
	a := newLiveTestAPI(t)
	host, guest, outsider := a.user("host"), a.user("guest"), a.user("outsider")
	g := a.createGame(host, 4, guest)
	hostConn, guestConn := a.connect(host, g.ID), a.connect(guest, g.ID)
	for _, c := range []*wsClient{hostConn, guestConn} {
		c.nextOfType(t, ws.TypeGameState, 3*time.Second)
	}
	a.expect(http.StatusOK, "POST", "/api/v1/games/"+g.ID+"/start", &host, nil)
	var round ws.GameStatePayload
	for _, c := range []*wsClient{hostConn, guestConn} {
		round = decodePayload[ws.GameStatePayload](t, c.nextOfType(t, ws.TypeGameState, 3*time.Second))
	}
	if round.ChallengeID == "" {
		t.Fatal("the round has no challenge")
	}
	rounds := "/api/v1/rounds/" + round.RoundID

	runTest := func(u testUser, conn *wsClient, prompt string) string {
		t.Helper()
		test := decode[struct {
			TestID string `json:"test_id"`
		}](t, a.expect(http.StatusAccepted, "POST", rounds+"/test", &u, map[string]any{"prompt": prompt}))
		conn.nextOfType(t, ws.TypeOutputComplete, 3*time.Second)
		return test.TestID
	}
	hostTest := runTest(host, hostConn, "a plain prompt")
	guestTest := runTest(guest, guestConn, "the best prompt")

	// Refusals.
	if resp := a.expect(http.StatusBadRequest, "POST", rounds+"/submit", &host, map[string]any{"test_id": guestTest}); errorCode(t, resp) != "INVALID_TEST" {
		t.Errorf("submitting another player's test: %s", resp)
	}
	a.expect(http.StatusForbidden, "POST", rounds+"/submit", &outsider, map[string]any{"test_id": hostTest})

	sub := decode[submissionJSON](t, a.expect(http.StatusCreated, "POST", rounds+"/submit", &host, map[string]any{"test_id": hostTest}))
	if sub.TestID != hostTest || sub.Prompt != "a plain prompt" || sub.Output != "Hello" || sub.PromptLength != 14 || sub.TestCount != 1 || sub.Source != "manual" || sub.LockedAt == 0 {
		t.Errorf("submission = %+v", sub)
	}
	again := decode[submissionJSON](t, a.expect(http.StatusOK, "POST", rounds+"/submit", &host, map[string]any{"test_id": hostTest}))
	if again.ID != sub.ID {
		t.Errorf("repeated submit returned %s, want the existing %s", again.ID, sub.ID)
	}
	other := runTest(host, hostConn, "another prompt")
	if resp := a.expect(http.StatusConflict, "POST", rounds+"/submit", &host, map[string]any{"test_id": other}); errorCode(t, resp) != "ALREADY_SUBMITTED" {
		t.Errorf("second submit: %s", resp)
	}

	// Before results, only the owner sees the submission, and nobody its score.
	a.expect(http.StatusOK, "GET", "/api/v1/submissions/"+sub.ID, &host, nil)
	a.expect(http.StatusNotFound, "GET", "/api/v1/submissions/"+sub.ID, &guest, nil)
	a.expect(http.StatusNotFound, "GET", "/api/v1/submissions/"+sub.ID, &outsider, nil)
	if resp := a.expect(http.StatusConflict, "GET", "/api/v1/submissions/"+sub.ID+"/score", &host, nil); errorCode(t, resp) != "RESULTS_NOT_READY" {
		t.Errorf("own score before results: %s", resp)
	}
	a.expect(http.StatusNotFound, "GET", "/api/v1/submissions/"+sub.ID+"/score", &guest, nil)

	// The guest's submission ends the round. Until round_results, nothing the
	// guest receives carries the host's prompt or output.
	a.expect(http.StatusCreated, "POST", rounds+"/submit", &guest, map[string]any{"test_id": guestTest})
	var results ws.RoundResultsPayload
	submittedSeen := false
	deadline := time.After(5 * time.Second)
	for results.RoundID == "" {
		select {
		case env := <-guestConn.events:
			if env.Type == ws.TypeRoundResults {
				results = decodePayload[ws.RoundResultsPayload](t, env)
				continue
			}
			if strings.Contains(string(env.Payload), "a plain prompt") {
				t.Fatalf("%s before results carries the host's prompt: %s", env.Type, env.Payload)
			}
			if env.Type == ws.TypePlayerSubmitted {
				p := decodePayload[ws.PlayerSubmittedPayload](t, env)
				submittedSeen = submittedSeen || (p.SubmittedCount == 2 && p.TotalPlayers == 2)
			}
		case <-deadline:
			t.Fatal("no round_results")
		}
	}
	if !submittedSeen {
		t.Error("no player_submitted with 2 of 2")
	}

	// Sorted by rank, with every prompt now revealed.
	if r := results.Results; len(r) != 2 || r[0].PlayerID != guest.ID.String() || r[0].Rank != 1 || r[0].Prompt != "the best prompt" ||
		r[1].PlayerID != host.ID.String() || r[1].Rank != 2 || r[1].Prompt != "a plain prompt" || r[1].DisplayName != "host" ||
		r[0].Scores.Total <= r[1].Scores.Total || r[0].Rationale == "" {
		t.Errorf("round_results = %+v", results)
	}

	// A reconnect while results show gets the same results.
	snapshot := decodePayload[ws.GameStatePayload](t, a.connect(host, g.ID).nextOfType(t, ws.TypeGameState, 3*time.Second))
	if snapshot.State != "Results" || snapshot.RoundResults == nil || len(snapshot.RoundResults.Results) != 2 ||
		snapshot.RoundResults.Results[0].Prompt != "the best prompt" {
		t.Errorf("reconnect snapshot = %s with results %+v, want Results with both entries", snapshot.State, snapshot.RoundResults)
	}

	// After results, opponents may read each other's submission and score.
	opp := decode[submissionJSON](t, a.expect(http.StatusOK, "GET", "/api/v1/submissions/"+sub.ID, &guest, nil))
	if opp.Prompt != "a plain prompt" {
		t.Errorf("opponent's submission after results = %+v", opp)
	}
	score := decode[struct {
		Rank   int `json:"rank"`
		Scores struct {
			Total float64 `json:"total"`
		} `json:"scores"`
	}](t, a.expect(http.StatusOK, "GET", "/api/v1/submissions/"+sub.ID+"/score", &guest, nil))
	if score.Rank != 2 || score.Scores.Total != results.Results[1].Scores.Total {
		t.Errorf("score = %+v", score)
	}

	// The results time runs out and round 2 starts by itself.
	for {
		state := decodePayload[ws.GameStatePayload](t, hostConn.nextOfType(t, ws.TypeGameState, 5*time.Second))
		if state.State == "RoundActive" {
			if state.CurrentRound != 2 || state.RoundID == round.RoundID {
				t.Errorf("next round = %+v", state)
			}
			break
		}
	}
}
