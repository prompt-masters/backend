package redisstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/prompt-masters/backend/internal/domain"
	"github.com/prompt-masters/backend/internal/repository"
	"github.com/redis/go-redis/v9"
)

// Data types, per key (see Keys for names):
//
//   - state: Hash with status, host_id, room_code, settings (JSON) and
//     updated_at_ms. A hash lets single fields be read inside scripts, e.g. the
//     status that picks the TTL.
//   - players: Hash of user ID to playerJSON, so one player can be added or
//     removed without rewriting the roster.
//   - ready: Set of user IDs; membership is naturally idempotent.
//   - round: Hash with number, challenge_id, started_at_ms and deadline_ms.
//     Times are server-side Unix milliseconds.
//   - draft:{userID}: String holding draftJSON; only ever read whole.
//   - points: Hash of user ID to integer, updated with HINCRBY.

type Options struct {
	KeyPrefix string
	// ActiveTTL applies while the stored lobby status is waiting, in progress
	// or unknown; EndedTTL once it is finished or cancelled.
	ActiveTTL time.Duration
	EndedTTL  time.Duration
	// OpTimeout bounds every method call.
	OpTimeout time.Duration
}

var _ repository.GameStateRepository = (*Store)(nil)

// Store is a repository.GameStateRepository backed by Redis.
type Store struct {
	client redis.UniversalClient
	keys   Keys
	opts   Options
}

func New(client redis.UniversalClient, opts Options) (*Store, error) {
	keys, err := NewKeys(opts.KeyPrefix)
	if err != nil {
		return nil, err
	}
	if opts.ActiveTTL <= 0 || opts.EndedTTL <= 0 || opts.OpTimeout <= 0 {
		return nil, errors.New("redisstore: TTLs and operation timeout must be positive")
	}
	return &Store{client: client, keys: keys, opts: opts}, nil
}

type settingsJSON struct {
	Rounds       int    `json:"rounds"`
	TimePerRound int    `json:"time_per_round"`
	Difficulty   string `json:"difficulty"`
	Category     string `json:"category"`
	AIModel      string `json:"ai_model"`
	MaxPlayers   int    `json:"max_players"`
}

type playerJSON struct {
	UserID     string `json:"user_id"`
	Username   string `json:"username"`
	JoinedAtMs int64  `json:"joined_at_ms"`
}

type draftJSON struct {
	Content     string `json:"content"`
	RoundNumber int    `json:"round_number"`
	UpdatedAtMs int64  `json:"updated_at_ms"`
}

// notPlayerReply is the error code scripts reply with for non-players. Redis
// reads the first word of an error reply as its code; without a message it
// would prefix the reply with "ERR".
const notPlayerReply = "NOT_PLAYER"

const notPlayerError = `redis.error_reply('` + notPlayerReply + ` user is not a player of this game')`

// scriptPrelude is shared by every write script. Calling convention:
//
//	KEYS[1..5] = state, players, ready, round, points
//	ARGV[1]    = draft key prefix
//	ARGV[2]    = active TTL (ms), ARGV[3] = ended TTL (ms)
//	ARGV[4..]  = script-specific
//
// Draft keys are derived from the roster inside the script. They share the
// game's hash tag, so they live in the same cluster slot as KEYS.
const scriptPrelude = `
local function refresh_ttl()
  local ttl = tonumber(ARGV[2])
  local status = redis.call('HGET', KEYS[1], 'status')
  if status == 'finished' or status == 'cancelled' then
    ttl = tonumber(ARGV[3])
  end
  for i = 1, 5 do
    redis.call('PEXPIRE', KEYS[i], ttl)
  end
  for _, uid in ipairs(redis.call('HKEYS', KEYS[2])) do
    redis.call('PEXPIRE', ARGV[1] .. uid, ttl)
  end
end

local function is_player(uid)
  return redis.call('HEXISTS', KEYS[2], uid) == 1
end
`

func script(body string) *redis.Script {
	return redis.NewScript(scriptPrelude + body)
}

// Lobby fields follow the shared arguments: ARGV[4..8].
const setLobbyFields = `
redis.call('HSET', KEYS[1],
  'status', ARGV[4], 'host_id', ARGV[5], 'room_code', ARGV[6],
  'settings', ARGV[7], 'updated_at_ms', ARGV[8])
`

var (
	setLobbyScript = script(setLobbyFields + `
refresh_ttl()
return 1
`)

	// ARGV[9] = player count, then user ID / player JSON pairs.
	syncLobbyScript = script(setLobbyFields + `
local keep = {}
local n = tonumber(ARGV[9])
for i = 0, n - 1 do
  keep[ARGV[10 + 2 * i]] = ARGV[11 + 2 * i]
end
for _, uid in ipairs(redis.call('HKEYS', KEYS[2])) do
  if keep[uid] == nil then
    redis.call('HDEL', KEYS[2], uid)
    redis.call('SREM', KEYS[3], uid)
    redis.call('DEL', ARGV[1] .. uid)
  end
end
for uid, player in pairs(keep) do
  redis.call('HSET', KEYS[2], uid, player)
end
refresh_ttl()
return 1
`)

	// ARGV[4] = user ID, ARGV[5] = player JSON.
	addPlayerScript = script(`
redis.call('HSET', KEYS[2], ARGV[4], ARGV[5])
refresh_ttl()
return 1
`)

	// ARGV[4] = user ID.
	removePlayerScript = script(`
redis.call('HDEL', KEYS[2], ARGV[4])
redis.call('SREM', KEYS[3], ARGV[4])
redis.call('DEL', ARGV[1] .. ARGV[4])
refresh_ttl()
return 1
`)

	// ARGV[4] = user ID, ARGV[5] = "1" to mark ready, "0" to unmark.
	setReadyScript = script(`
if not is_player(ARGV[4]) then
  return ` + notPlayerError + `
end
if ARGV[5] == '1' then
  redis.call('SADD', KEYS[3], ARGV[4])
else
  redis.call('SREM', KEYS[3], ARGV[4])
end
refresh_ttl()
return 1
`)

	// ARGV[4..7] = number, challenge ID, started at (ms), deadline (ms).
	setRoundScript = script(`
redis.call('HSET', KEYS[4],
  'number', ARGV[4], 'challenge_id', ARGV[5],
  'started_at_ms', ARGV[6], 'deadline_ms', ARGV[7])
refresh_ttl()
return 1
`)

	// ARGV[4] = user ID, ARGV[5] = draft JSON.
	saveDraftScript = script(`
if not is_player(ARGV[4]) then
  return ` + notPlayerError + `
end
redis.call('SET', ARGV[1] .. ARGV[4], ARGV[5])
refresh_ttl()
return 1
`)

	// ARGV[4] = user ID, ARGV[5] = delta.
	incrementPointsScript = script(`
if not is_player(ARGV[4]) then
  return ` + notPlayerError + `
end
local total = redis.call('HINCRBY', KEYS[5], ARGV[4], ARGV[5])
refresh_ttl()
return total
`)

	deleteScript = script(`
for _, uid in ipairs(redis.call('HKEYS', KEYS[2])) do
  redis.call('DEL', ARGV[1] .. uid)
end
redis.call('DEL', KEYS[1], KEYS[2], KEYS[3], KEYS[4], KEYS[5])
return 1
`)
)

// run executes a write script for gameID with the shared arguments followed
// by args.
func (s *Store) run(ctx context.Context, sc *redis.Script, gameID uuid.UUID, args ...any) (any, error) {
	gk, err := s.keys.Game(gameID)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, s.opts.OpTimeout)
	defer cancel()

	argv := append([]any{
		gk.DraftPrefix,
		s.opts.ActiveTTL.Milliseconds(),
		s.opts.EndedTTL.Milliseconds(),
	}, args...)
	res, err := sc.Run(ctx, s.client, gk.All(), argv...).Result()
	if err != nil {
		return nil, translate(err)
	}
	return res, nil
}

func (s *Store) SetLobby(ctx context.Context, lobby domain.LobbyState) error {
	args, err := lobbyArgs(lobby)
	if err != nil {
		return err
	}
	_, err = s.run(ctx, setLobbyScript, lobby.GameID, args...)
	return err
}

func (s *Store) SyncLobby(ctx context.Context, lobby domain.LobbyState, players []domain.LivePlayer) error {
	args, err := lobbyArgs(lobby)
	if err != nil {
		return err
	}
	args = append(args, len(players))
	for _, p := range players {
		raw, err := encodePlayer(p)
		if err != nil {
			return err
		}
		args = append(args, p.UserID.String(), raw)
	}
	_, err = s.run(ctx, syncLobbyScript, lobby.GameID, args...)
	return err
}

func (s *Store) GetLobby(ctx context.Context, gameID uuid.UUID) (*domain.LobbyState, error) {
	gk, err := s.keys.Game(gameID)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, s.opts.OpTimeout)
	defer cancel()

	fields, err := s.client.HGetAll(ctx, gk.State).Result()
	if err != nil {
		return nil, translate(err)
	}
	return decodeLobby(gameID, fields)
}

func (s *Store) AddPlayer(ctx context.Context, gameID uuid.UUID, player domain.LivePlayer) error {
	raw, err := encodePlayer(player)
	if err != nil {
		return err
	}
	_, err = s.run(ctx, addPlayerScript, gameID, player.UserID.String(), raw)
	return err
}

func (s *Store) RemovePlayer(ctx context.Context, gameID, userID uuid.UUID) error {
	if userID == uuid.Nil {
		return fmt.Errorf("%w: user ID is nil", ErrInvalidKeyID)
	}
	_, err := s.run(ctx, removePlayerScript, gameID, userID.String())
	return err
}

func (s *Store) ListPlayers(ctx context.Context, gameID uuid.UUID) ([]domain.LivePlayer, error) {
	gk, err := s.keys.Game(gameID)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, s.opts.OpTimeout)
	defer cancel()

	fields, err := s.client.HGetAll(ctx, gk.Players).Result()
	if err != nil {
		return nil, translate(err)
	}
	return decodePlayers(fields)
}

func (s *Store) SetReady(ctx context.Context, gameID, userID uuid.UUID) error {
	return s.setReady(ctx, gameID, userID, "1")
}

func (s *Store) UnsetReady(ctx context.Context, gameID, userID uuid.UUID) error {
	return s.setReady(ctx, gameID, userID, "0")
}

func (s *Store) setReady(ctx context.Context, gameID, userID uuid.UUID, flag string) error {
	if userID == uuid.Nil {
		return fmt.Errorf("%w: user ID is nil", ErrInvalidKeyID)
	}
	_, err := s.run(ctx, setReadyScript, gameID, userID.String(), flag)
	return err
}

func (s *Store) ListReady(ctx context.Context, gameID uuid.UUID) ([]uuid.UUID, error) {
	gk, err := s.keys.Game(gameID)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, s.opts.OpTimeout)
	defer cancel()

	members, err := s.client.SMembers(ctx, gk.Ready).Result()
	if err != nil {
		return nil, translate(err)
	}
	return decodeReady(members)
}

func (s *Store) SetRound(ctx context.Context, gameID uuid.UUID, round domain.RoundState) error {
	if round.Number <= 0 || round.ChallengeID == uuid.Nil || round.StartedAt.IsZero() || !round.Deadline.After(round.StartedAt) {
		return errors.New("redisstore: round needs a positive number, a challenge and a deadline after its start")
	}
	_, err := s.run(ctx, setRoundScript, gameID,
		round.Number, round.ChallengeID.String(), round.StartedAt.UnixMilli(), round.Deadline.UnixMilli())
	return err
}

func (s *Store) GetRound(ctx context.Context, gameID uuid.UUID) (*domain.RoundState, error) {
	gk, err := s.keys.Game(gameID)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, s.opts.OpTimeout)
	defer cancel()

	fields, err := s.client.HGetAll(ctx, gk.Round).Result()
	if err != nil {
		return nil, translate(err)
	}
	return decodeRound(fields)
}

func (s *Store) SaveDraft(ctx context.Context, gameID, userID uuid.UUID, draft domain.Draft) error {
	if userID == uuid.Nil {
		return fmt.Errorf("%w: user ID is nil", ErrInvalidKeyID)
	}
	raw, err := json.Marshal(draftJSON{
		Content:     draft.Content,
		RoundNumber: draft.RoundNumber,
		UpdatedAtMs: draft.UpdatedAt.UnixMilli(),
	})
	if err != nil {
		return fmt.Errorf("encoding draft: %w", err)
	}
	_, err = s.run(ctx, saveDraftScript, gameID, userID.String(), string(raw))
	return err
}

func (s *Store) GetDraft(ctx context.Context, gameID, userID uuid.UUID) (*domain.Draft, error) {
	gk, err := s.keys.Game(gameID)
	if err != nil {
		return nil, err
	}
	key, err := gk.Draft(userID)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, s.opts.OpTimeout)
	defer cancel()

	raw, err := s.client.Get(ctx, key).Result()
	if err != nil {
		return nil, translate(err)
	}
	return decodeDraft(raw)
}

func (s *Store) IncrementPoints(ctx context.Context, gameID, userID uuid.UUID, delta int64) (int64, error) {
	if userID == uuid.Nil {
		return 0, fmt.Errorf("%w: user ID is nil", ErrInvalidKeyID)
	}
	res, err := s.run(ctx, incrementPointsScript, gameID, userID.String(), delta)
	if err != nil {
		return 0, err
	}
	total, ok := res.(int64)
	if !ok {
		return 0, fmt.Errorf("redisstore: unexpected points reply %T", res)
	}
	return total, nil
}

func (s *Store) GetPoints(ctx context.Context, gameID uuid.UUID) (map[uuid.UUID]int64, error) {
	gk, err := s.keys.Game(gameID)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, s.opts.OpTimeout)
	defer cancel()

	fields, err := s.client.HGetAll(ctx, gk.Points).Result()
	if err != nil {
		return nil, translate(err)
	}
	return decodePoints(fields)
}

func (s *Store) Load(ctx context.Context, gameID, viewerID uuid.UUID) (*domain.LiveGameState, error) {
	gk, err := s.keys.Game(gameID)
	if err != nil {
		return nil, err
	}
	draftKey, err := gk.Draft(viewerID)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, s.opts.OpTimeout)
	defer cancel()

	var (
		state   *redis.MapStringStringCmd
		players *redis.MapStringStringCmd
		ready   *redis.StringSliceCmd
		round   *redis.MapStringStringCmd
		draft   *redis.StringCmd
		points  *redis.MapStringStringCmd
	)
	// MULTI/EXEC so every part comes from the same moment.
	_, err = s.client.TxPipelined(ctx, func(p redis.Pipeliner) error {
		state = p.HGetAll(ctx, gk.State)
		players = p.HGetAll(ctx, gk.Players)
		ready = p.SMembers(ctx, gk.Ready)
		round = p.HGetAll(ctx, gk.Round)
		draft = p.Get(ctx, draftKey)
		points = p.HGetAll(ctx, gk.Points)
		return nil
	})
	// A missing draft reports redis.Nil for the whole pipeline; anything else
	// is a real failure.
	if err != nil && !errors.Is(err, redis.Nil) {
		return nil, translate(err)
	}

	live := &domain.LiveGameState{}
	lobby, err := decodeLobby(gameID, state.Val())
	if err != nil {
		return nil, err
	}
	live.Lobby = *lobby
	if live.Players, err = decodePlayers(players.Val()); err != nil {
		return nil, err
	}
	if live.Ready, err = decodeReady(ready.Val()); err != nil {
		return nil, err
	}
	if r, err := decodeRound(round.Val()); err == nil {
		live.Round = r
	} else if !errors.Is(err, domain.ErrStateNotFound) {
		return nil, err
	}
	if raw, err := draft.Result(); err == nil {
		if live.Draft, err = decodeDraft(raw); err != nil {
			return nil, err
		}
	} else if !errors.Is(err, redis.Nil) {
		return nil, translate(err)
	}
	if live.Points, err = decodePoints(points.Val()); err != nil {
		return nil, err
	}
	return live, nil
}

func (s *Store) Delete(ctx context.Context, gameID uuid.UUID) error {
	_, err := s.run(ctx, deleteScript, gameID)
	return err
}

// unavailableReplies are server error replies that mean Redis cannot serve
// requests right now, as opposed to a bug in how it is used.
var unavailableReplies = []string{"NOAUTH", "WRONGPASS", "LOADING", "READONLY", "MASTERDOWN", "CLUSTERDOWN", "TRYAGAIN", "BUSY"}

// translate maps Redis client errors onto domain errors:
//   - redis.Nil becomes domain.ErrStateNotFound;
//   - the scripts' NOT_PLAYER reply becomes domain.ErrNotGamePlayer;
//   - network failures, timeouts, a closed client and unavailability replies
//     become domain.ErrStateUnavailable;
//   - any other server reply (WRONGTYPE, a script error) is returned wrapped as
//     an internal error, since retrying will not help.
func translate(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, redis.Nil) {
		return domain.ErrStateNotFound
	}

	var reply redis.Error
	if errors.As(err, &reply) {
		msg := reply.Error()
		if strings.HasPrefix(msg, notPlayerReply) {
			return domain.ErrNotGamePlayer
		}
		for _, prefix := range unavailableReplies {
			if strings.HasPrefix(msg, prefix) {
				return fmt.Errorf("%w: %w", domain.ErrStateUnavailable, err)
			}
		}
		return fmt.Errorf("redisstore: %w", err)
	}
	return fmt.Errorf("%w: %w", domain.ErrStateUnavailable, err)
}

func lobbyArgs(l domain.LobbyState) ([]any, error) {
	if l.HostID == uuid.Nil || !l.Status.Valid() {
		return nil, errors.New("redisstore: lobby needs a host and a valid status")
	}
	settings, err := json.Marshal(settingsJSON{
		Rounds:       l.Settings.Rounds,
		TimePerRound: l.Settings.TimePerRoundSeconds,
		Difficulty:   string(l.Settings.Difficulty),
		Category:     string(l.Settings.Category),
		AIModel:      string(l.Settings.AIModel),
		MaxPlayers:   l.Settings.MaxPlayers,
	})
	if err != nil {
		return nil, fmt.Errorf("encoding settings: %w", err)
	}
	return []any{string(l.Status), l.HostID.String(), l.RoomCode, string(settings), l.UpdatedAt.UnixMilli()}, nil
}

func encodePlayer(p domain.LivePlayer) (string, error) {
	if p.UserID == uuid.Nil {
		return "", fmt.Errorf("%w: player user ID is nil", ErrInvalidKeyID)
	}
	raw, err := json.Marshal(playerJSON{UserID: p.UserID.String(), Username: p.Username, JoinedAtMs: p.JoinedAt.UnixMilli()})
	if err != nil {
		return "", fmt.Errorf("encoding player: %w", err)
	}
	return string(raw), nil
}

// errCorrupt reports stored data that cannot be decoded. It is deliberately
// neither ErrStateNotFound nor ErrStateUnavailable.
func errCorrupt(what string, err error) error {
	return fmt.Errorf("redisstore: corrupt %s: %w", what, err)
}

func decodeLobby(gameID uuid.UUID, f map[string]string) (*domain.LobbyState, error) {
	if len(f) == 0 {
		return nil, domain.ErrStateNotFound
	}
	hostID, err := uuid.Parse(f["host_id"])
	if err != nil {
		return nil, errCorrupt("lobby host_id", err)
	}
	status := domain.GameStatus(f["status"])
	if !status.Valid() {
		return nil, errCorrupt("lobby status", fmt.Errorf("unknown status %q", f["status"]))
	}
	var s settingsJSON
	if err := json.Unmarshal([]byte(f["settings"]), &s); err != nil {
		return nil, errCorrupt("lobby settings", err)
	}
	updated, err := strconv.ParseInt(f["updated_at_ms"], 10, 64)
	if err != nil {
		return nil, errCorrupt("lobby updated_at_ms", err)
	}
	return &domain.LobbyState{
		GameID:   gameID,
		RoomCode: f["room_code"],
		HostID:   hostID,
		Status:   status,
		Settings: domain.GameSettings{
			Rounds:              s.Rounds,
			TimePerRoundSeconds: s.TimePerRound,
			Difficulty:          domain.Difficulty(s.Difficulty),
			Category:            domain.Category(s.Category),
			AIModel:             domain.AIModel(s.AIModel),
			MaxPlayers:          s.MaxPlayers,
		},
		UpdatedAt: time.UnixMilli(updated).UTC(),
	}, nil
}

func decodePlayers(f map[string]string) ([]domain.LivePlayer, error) {
	players := make([]domain.LivePlayer, 0, len(f))
	for field, raw := range f {
		var p playerJSON
		if err := json.Unmarshal([]byte(raw), &p); err != nil {
			return nil, errCorrupt("player", err)
		}
		id, err := uuid.Parse(p.UserID)
		if err != nil || id.String() != field {
			return nil, errCorrupt("player", fmt.Errorf("user ID %q does not match field %q", p.UserID, field))
		}
		players = append(players, domain.LivePlayer{UserID: id, Username: p.Username, JoinedAt: time.UnixMilli(p.JoinedAtMs).UTC()})
	}
	slices.SortFunc(players, func(a, b domain.LivePlayer) int {
		if c := a.JoinedAt.Compare(b.JoinedAt); c != 0 {
			return c
		}
		return strings.Compare(a.UserID.String(), b.UserID.String())
	})
	return players, nil
}

func decodeReady(members []string) ([]uuid.UUID, error) {
	ids := make([]uuid.UUID, 0, len(members))
	for _, m := range members {
		id, err := uuid.Parse(m)
		if err != nil {
			return nil, errCorrupt("ready member", err)
		}
		ids = append(ids, id)
	}
	slices.SortFunc(ids, func(a, b uuid.UUID) int { return strings.Compare(a.String(), b.String()) })
	return ids, nil
}

func decodeRound(f map[string]string) (*domain.RoundState, error) {
	if len(f) == 0 {
		return nil, domain.ErrStateNotFound
	}
	number, err := strconv.Atoi(f["number"])
	if err != nil {
		return nil, errCorrupt("round number", err)
	}
	challengeID, err := uuid.Parse(f["challenge_id"])
	if err != nil {
		return nil, errCorrupt("round challenge_id", err)
	}
	started, err := strconv.ParseInt(f["started_at_ms"], 10, 64)
	if err != nil {
		return nil, errCorrupt("round started_at_ms", err)
	}
	deadline, err := strconv.ParseInt(f["deadline_ms"], 10, 64)
	if err != nil {
		return nil, errCorrupt("round deadline_ms", err)
	}
	return &domain.RoundState{
		Number:      number,
		ChallengeID: challengeID,
		StartedAt:   time.UnixMilli(started).UTC(),
		Deadline:    time.UnixMilli(deadline).UTC(),
	}, nil
}

func decodeDraft(raw string) (*domain.Draft, error) {
	var d draftJSON
	if err := json.Unmarshal([]byte(raw), &d); err != nil {
		return nil, errCorrupt("draft", err)
	}
	return &domain.Draft{Content: d.Content, RoundNumber: d.RoundNumber, UpdatedAt: time.UnixMilli(d.UpdatedAtMs).UTC()}, nil
}

func decodePoints(f map[string]string) (map[uuid.UUID]int64, error) {
	points := make(map[uuid.UUID]int64, len(f))
	for field, raw := range f {
		id, err := uuid.Parse(field)
		if err != nil {
			return nil, errCorrupt("points user ID", err)
		}
		n, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			return nil, errCorrupt("points value", err)
		}
		points[id] = n
	}
	return points, nil
}
