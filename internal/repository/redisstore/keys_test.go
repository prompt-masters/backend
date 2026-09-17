package redisstore

import (
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
)

var (
	gameA = uuid.MustParse("0b5f3c0e-8a1d-4e7b-9c2a-1f4d6e8a0b3c")
	gameB = uuid.MustParse("7d2e9a41-3b6c-4f80-a5d7-2c9e1b4f6a80")
	userA = uuid.MustParse("c3a1e5f7-9b2d-4c6e-8a0f-1d3b5c7e9a2b")
)

func TestKeysFollowTheSchema(t *testing.T) {
	keys, err := NewKeys("promptgame")
	if err != nil {
		t.Fatalf("NewKeys() error = %v", err)
	}
	g, err := keys.Game(gameA)
	if err != nil {
		t.Fatalf("Game() error = %v", err)
	}
	draft, err := g.Draft(userA)
	if err != nil {
		t.Fatalf("Draft() error = %v", err)
	}

	base := "promptgame:game:{0b5f3c0e-8a1d-4e7b-9c2a-1f4d6e8a0b3c}:"
	tests := map[string]string{
		g.State:   base + "state",
		g.Players: base + "players",
		g.Ready:   base + "ready",
		g.Round:   base + "round",
		g.Points:  base + "points",
		draft:     base + "draft:c3a1e5f7-9b2d-4c6e-8a0f-1d3b5c7e9a2b",
	}
	for got, want := range tests {
		if got != want {
			t.Errorf("key = %q, want %q", got, want)
		}
	}
	if len(g.All()) != 5 {
		t.Errorf("All() = %v, want the 5 fixed keys", g.All())
	}
}

func TestKeysOfDifferentGamesNeverOverlap(t *testing.T) {
	keys, _ := NewKeys("promptgame")
	a, _ := keys.Game(gameA)
	b, _ := keys.Game(gameB)

	for _, ka := range append(a.All(), a.DraftPrefix) {
		for _, kb := range append(b.All(), b.DraftPrefix) {
			if strings.HasPrefix(ka, kb) || strings.HasPrefix(kb, ka) {
				t.Errorf("keys %q and %q overlap", ka, kb)
			}
		}
	}
}

func TestKeysShareOneHashTagPerGame(t *testing.T) {
	keys, _ := NewKeys("promptgame")
	g, _ := keys.Game(gameA)
	draft, _ := g.Draft(userA)

	want := "{" + gameA.String() + "}"
	for _, k := range append(g.All(), draft) {
		start := strings.Index(k, "{")
		end := strings.Index(k, "}")
		if start < 0 || end < start || k[start:end+1] != want {
			t.Errorf("key %q does not use hash tag %s", k, want)
		}
	}
}

func TestKeysRejectInvalidInput(t *testing.T) {
	for _, prefix := range []string{"", "Prompt", "prompt:game", "{x}", "a b"} {
		if _, err := NewKeys(prefix); err == nil {
			t.Errorf("NewKeys(%q) error = nil, want rejection", prefix)
		}
	}

	keys, _ := NewKeys("promptgame")
	if _, err := keys.Game(uuid.Nil); !errors.Is(err, ErrInvalidKeyID) {
		t.Errorf("Game(uuid.Nil) error = %v, want ErrInvalidKeyID", err)
	}
	g, _ := keys.Game(gameA)
	if _, err := g.Draft(uuid.Nil); !errors.Is(err, ErrInvalidKeyID) {
		t.Errorf("Draft(uuid.Nil) error = %v, want ErrInvalidKeyID", err)
	}
}
