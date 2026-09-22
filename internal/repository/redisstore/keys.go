// Package redisstore implements live game state storage on Redis.
package redisstore

import (
	"errors"
	"fmt"
	"regexp"

	"github.com/google/uuid"
)

// ErrInvalidKeyID is returned when a key would be built from a nil ID.
var ErrInvalidKeyID = errors.New("invalid ID for Redis key")

var prefixPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)

// Keys builds every Redis key used for live game state. It is the only place
// key strings are assembled.
//
// Schema, for prefix "promptgame" and game G:
//
//	promptgame:game:{G}:state           Hash    lobby state
//	promptgame:game:{G}:players         Hash    userID -> JSON player
//	promptgame:game:{G}:ready           Set     ready user IDs
//	promptgame:game:{G}:round           Hash    current round
//	promptgame:game:{G}:draft:{userID}  String  JSON draft
//	promptgame:game:{G}:points          Hash    userID -> integer
//
// The braces around the game ID are a Redis Cluster hash tag: all keys of one
// game map to the same slot, so the multi-key scripts stay cluster-safe.
type Keys struct {
	prefix string
}

func NewKeys(prefix string) (Keys, error) {
	if !prefixPattern.MatchString(prefix) {
		return Keys{}, fmt.Errorf("invalid Redis key prefix %q", prefix)
	}
	return Keys{prefix: prefix}, nil
}

// GameKeys are the keys of one game.
type GameKeys struct {
	State   string
	Players string
	Ready   string
	Round   string
	Points  string
	// DraftPrefix followed by a user ID is that user's draft key.
	DraftPrefix string
}

// All returns the fixed keys of the game, excluding per-player drafts.
func (g GameKeys) All() []string {
	return []string{g.State, g.Players, g.Ready, g.Round, g.Points}
}

func (g GameKeys) Draft(userID uuid.UUID) (string, error) {
	if userID == uuid.Nil {
		return "", fmt.Errorf("%w: user ID is nil", ErrInvalidKeyID)
	}
	return g.DraftPrefix + userID.String(), nil
}

func (k Keys) Game(gameID uuid.UUID) (GameKeys, error) {
	if gameID == uuid.Nil {
		return GameKeys{}, fmt.Errorf("%w: game ID is nil", ErrInvalidKeyID)
	}
	base := k.prefix + ":game:{" + gameID.String() + "}:"
	return GameKeys{
		State:       base + "state",
		Players:     base + "players",
		Ready:       base + "ready",
		Round:       base + "round",
		Points:      base + "points",
		DraftPrefix: base + "draft:",
	}, nil
}
