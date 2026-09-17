package domain

import (
	"testing"

	"github.com/google/uuid"
)

func TestIsRoomCode(t *testing.T) {
	tests := []struct {
		in   string
		want bool
	}{
		{"482913", true},
		{"000042", true},
		{"48291", false},
		{"4829130", false},
		{"48a913", false},
		{" 482913", false},
		{"４８２９１３", false}, // full-width digits are not ASCII digits
		{"", false},
	}
	for _, tt := range tests {
		if got := IsRoomCode(tt.in); got != tt.want {
			t.Errorf("IsRoomCode(%q) = %v, want %v", tt.in, got, tt.want)
		}
	}
}

func TestGameStatusRules(t *testing.T) {
	tests := []struct {
		status          GameStatus
		wantActive      bool
		wantCancellable bool
	}{
		{GameStatusWaiting, true, true},
		{GameStatusInProgress, true, true},
		{GameStatusFinished, false, false},
		{GameStatusCancelled, false, false},
	}
	for _, tt := range tests {
		if !tt.status.Valid() {
			t.Errorf("%s.Valid() = false, want true", tt.status)
		}
		if got := tt.status.Active(); got != tt.wantActive {
			t.Errorf("%s.Active() = %v, want %v", tt.status, got, tt.wantActive)
		}
		if got := tt.status.Cancellable(); got != tt.wantCancellable {
			t.Errorf("%s.Cancellable() = %v, want %v", tt.status, got, tt.wantCancellable)
		}
	}
	if GameStatus("paused").Valid() {
		t.Error(`GameStatus("paused").Valid() = true, want false`)
	}
}

func TestAIModelValid(t *testing.T) {
	for _, m := range AIModels() {
		if !m.Valid() {
			t.Errorf("AIModel(%q).Valid() = false, want true", m)
		}
	}
	if AIModel("gpt-4").Valid() {
		t.Error(`AIModel("gpt-4").Valid() = true, want false`)
	}
}

func TestGameMembership(t *testing.T) {
	host, guest, stranger := uuid.New(), uuid.New(), uuid.New()
	g := &Game{HostID: host, Players: []GamePlayer{{UserID: host}, {UserID: guest}}}

	if !g.IsHost(host) || g.IsHost(guest) {
		t.Error("IsHost does not match HostID")
	}
	if !g.HasPlayer(guest) || g.HasPlayer(stranger) {
		t.Error("HasPlayer does not match Players")
	}
}
