package api_test

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/prompt-masters/backend/internal/ws"
)

type promptTestJSON struct {
	TestID       string  `json:"test_id"`
	TestNumber   int     `json:"test_number"`
	Prompt       string  `json:"prompt"`
	Output       *string `json:"output"`
	Status       string  `json:"status"`
	ErrorCode    *string `json:"error_code"`
	InputTokens  *int    `json:"input_tokens"`
	OutputTokens *int    `json:"output_tokens"`
	LatencyMs    *int64  `json:"latency_ms"`
	CreatedAt    int64   `json:"created_at"`
	CompletedAt  *int64  `json:"completed_at"`
}

// TestPromptTestEndToEnd runs a prompt test through HTTP, the database and
// the WebSocket hub: the output streams to the tester alone, is stored, and
// is readable only by them.
func TestPromptTestEndToEnd(t *testing.T) {
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
	testPath := "/api/v1/rounds/" + round.RoundID + "/test"
	listPath := "/api/v1/rounds/" + round.RoundID + "/tests"

	a.expect(http.StatusBadRequest, "POST", testPath, &guest, map[string]any{"prompt": "  "})
	a.expect(http.StatusForbidden, "POST", testPath, &outsider, map[string]any{"prompt": "p"})
	a.expect(http.StatusNotFound, "POST", "/api/v1/rounds/not-a-round/test", &guest, map[string]any{"prompt": "p"})

	started := decode[promptTestJSON](t, a.expect(http.StatusAccepted, "POST", testPath, &guest, map[string]any{"prompt": "write a haiku"}))
	if started.TestID == "" || started.TestNumber != 1 || started.Status != "pending" {
		t.Fatalf("202 body = %+v, want pending test 1", started)
	}

	first := decodePayload[ws.OutputChunkPayload](t, guestConn.nextOfType(t, ws.TypeOutputChunk, 3*time.Second))
	if first.TestID != started.TestID || first.Seq != 1 || first.Delta != "Hel" {
		t.Errorf("first chunk = %+v", first)
	}
	done := decodePayload[ws.OutputCompletePayload](t, guestConn.nextOfType(t, ws.TypeOutputComplete, 3*time.Second))
	if done.TestID != started.TestID || done.Output != "Hello" || done.Status != "completed" || done.Usage == nil || done.Usage.InputTokens != 12 {
		t.Errorf("output_complete = %+v", done)
	}
	hostConn.expectNothing(300 * time.Millisecond) // nothing reaches the other player

	history := decode[struct {
		Tests []promptTestJSON `json:"tests"`
	}](t, a.expect(http.StatusOK, "GET", listPath, &guest, nil))
	if len(history.Tests) != 1 {
		t.Fatalf("history = %+v, want one test", history.Tests)
	}
	got := history.Tests[0]
	if got.TestID != started.TestID || got.Status != "completed" || got.Prompt != "write a haiku" || got.Output == nil || *got.Output != "Hello" ||
		got.ErrorCode != nil || *got.InputTokens != 12 || *got.OutputTokens != 34 || got.LatencyMs == nil ||
		got.CreatedAt == 0 || got.CompletedAt == nil || *got.CompletedAt < got.CreatedAt {
		t.Errorf("stored test = %+v", got)
	}

	// Another player's tests are never returned, whatever the query says.
	hostView := a.expect(http.StatusOK, "GET", listPath+"?player_id="+guest.ID.String(), &host, nil)
	if tests := decode[struct {
		Tests []promptTestJSON `json:"tests"`
	}](t, hostView).Tests; len(tests) != 0 {
		t.Errorf("the host sees %d of the guest's tests", len(tests))
	}
	a.expect(http.StatusForbidden, "GET", listPath, &outsider, nil)

	// Once the game is over, testing is refused with a stable code.
	a.expect(http.StatusNoContent, "DELETE", "/api/v1/games/"+g.ID, &host, nil)
	refused := a.expect(http.StatusConflict, "POST", testPath, &guest, map[string]any{"prompt": "p"})
	var body struct {
		ErrorCode string `json:"error_code"`
	}
	if err := json.Unmarshal(refused.Body, &body); err != nil || body.ErrorCode != "ROUND_NOT_ACTIVE" {
		t.Errorf("409 body = %s, want error_code ROUND_NOT_ACTIVE", refused.Body)
	}
}
