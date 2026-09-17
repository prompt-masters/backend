package domain

import "errors"

var (
	ErrNotFound                 = errors.New("not found")
	ErrEmailTaken               = errors.New("email already registered")
	ErrUsernameTaken            = errors.New("username already taken")
	ErrInvalidCredentials       = errors.New("invalid credentials")
	ErrEmailNotVerified         = errors.New("email not verified")
	ErrInvalidVerificationToken = errors.New("verification token is invalid")
	ErrVerificationTokenExpired = errors.New("verification token has expired")
	ErrInvalidRefreshToken      = errors.New("refresh token is invalid")
	ErrNoEligibleChallenge      = errors.New("no challenge matches the game configuration")
	ErrRoomCodeTaken            = errors.New("room code is already in use")
	ErrNotGameHost              = errors.New("only the host can do this")
	ErrGameNotWaiting           = errors.New("game is no longer waiting for players")
	ErrGameNotCancellable       = errors.New("game has already ended")
	ErrGameFull                 = errors.New("game is full")
	ErrAlreadyJoined            = errors.New("already joined this game")
	ErrNotInGame                = errors.New("not a player in this game")
	ErrNotEnoughPlayers         = errors.New("not enough players to start")
	ErrNotGamePlayer            = errors.New("only players of this game can do this")
	ErrGameNotInProgress        = errors.New("game is not in progress")
	ErrStateNotFound            = errors.New("live game state not found")
	ErrStateUnavailable         = errors.New("live game state is temporarily unavailable")
)
