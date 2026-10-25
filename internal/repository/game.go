package repository

import (
	"context"

	"github.com/google/uuid"
	"github.com/prompt-masters/backend/internal/domain"
)

type GameRepository interface {
	// Create inserts a waiting game. It returns domain.ErrRoomCodeTaken when
	// another active game holds the room code; the transaction stays usable.
	Create(ctx context.Context, hostID uuid.UUID, roomCode string) (*domain.Game, error)
	CreateSettings(ctx context.Context, gameID uuid.UUID, settings domain.GameSettings) error
	UpdateSettings(ctx context.Context, gameID uuid.UUID, settings domain.GameSettings) error

	// GetByID loads the game with its settings and players.
	GetByID(ctx context.Context, id uuid.UUID) (*domain.Game, error)
	// LockByID is GetByID that also row-locks the game until the surrounding
	// transaction ends, serializing every state and membership change.
	LockByID(ctx context.Context, id uuid.UUID) (*domain.Game, error)
	// LockSharedByID is GetByID under a shared lock: no state change can
	// happen until the transaction ends, yet other shared readers proceed.
	LockSharedByID(ctx context.Context, id uuid.UUID) (*domain.Game, error)
	GetActiveIDByRoomCode(ctx context.Context, roomCode string) (uuid.UUID, error)
	// ListByStatus returns games newest first, with settings and player
	// counts but without players.
	ListByStatus(ctx context.Context, status domain.GameStatus, limit int) ([]*domain.Game, error)

	// AddPlayer returns domain.ErrAlreadyJoined when the user is a player.
	AddPlayer(ctx context.Context, gameID, userID uuid.UUID) error
	// RemovePlayer returns domain.ErrNotInGame when the user is not a player.
	RemovePlayer(ctx context.Context, gameID, userID uuid.UUID) error
	UpdateHost(ctx context.Context, gameID, hostID uuid.UUID) error
	UpdateStatus(ctx context.Context, gameID uuid.UUID, status domain.GameStatus) error

	// GetProgress reads the game engine's state of a game.
	GetProgress(ctx context.Context, id uuid.UUID) (*domain.GameProgress, error)
	// UpdateProgress stores the engine's move from one state to the next, with
	// the status that goes with the new phase. It is a compare-and-set: when
	// the stored phase, status or round no longer match from, nothing is
	// written and domain.ErrStaleGameState is returned.
	UpdateProgress(ctx context.Context, gameID uuid.UUID, from, to domain.GameProgress) error
	// ListByPhase returns every game in the phase, with settings but without
	// players.
	ListByPhase(ctx context.Context, phase domain.GamePhase) ([]*domain.Game, error)

	// CreateRound records a round as it starts.
	CreateRound(ctx context.Context, round domain.Round) error
	// GetRound returns domain.ErrNotFound for an unknown round.
	GetRound(ctx context.Context, id uuid.UUID) (*domain.Round, error)
	// ListChallengeIDs returns the challenges the game's rounds were played on.
	ListChallengeIDs(ctx context.Context, gameID uuid.UUID) ([]uuid.UUID, error)
}
