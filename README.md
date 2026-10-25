# Prompt Masters Backend API

Backend API for Prompt Masters, built with Go.

## Project Structure

```
backend/
├── cmd/server/main.go                     # Entry point, wires everything together
│
├── internal/
│   ├── api/                               # HTTP / Transport layer
│   │   ├── response.go                    # JSON response helpers
│   │   └── middleware/                    # Auth, logging, CORS, etc.
│   │
│   ├── config/config.go                   # Env loading + validation
│   │
│   ├── db/
│   │   ├── migrations/                    # Goose SQL migrations
│   │   ├── queries/                       # SQLC query files
│   │   ├── models.go                      # SQLC generated models
│   │   ├── db.go                          # SQLC generated DB interface
│   │   ├── querier.go                     # SQLC generated Querier interface
│   │   └── *.sql.go                       # SQLC generated query code
│   │
│   ├── domain/                            # Pure business entities
│   │   ├── user.go
│   │   └── verification_token.go
│   │
│   ├── repository/                        # Data access interfaces
│   │   ├── user.go
│   │   ├── verification_token.go
│   │   └── postgres/                      # Postgres implementations
│   │       ├── helpers.go                 # UUID converters
│   │       ├── user.go
│   │       └── verification_token.go
│   │
│   ├── service/                           # Business logic / use cases
│   │
│   └── util/                              # Shared helpers
│       └── password.go                    # Argon2id hash + verify
│
├── sqlc.yaml
├── Makefile
├── docker-compose.yaml
├── Dockerfile
├── DEV_GUIDE.md                           # Feature development workflow
└── .env.example
```

## Tech Stack

- **Go** 1.26.5+
- **PostgreSQL** 16 (via Docker)
- **[goose](https://github.com/pressly/goose)** — database migrations
- **[sqlc](https://sqlc.dev)** — type-safe SQL query generation
- **[pgx/v5](https://github.com/jackc/pgx)** — PostgreSQL driver
- **argon2id** — password hashing
- **google/uuid** — UUID generation

## Setup

**Requirements:** Docker and Docker Compose. Go 1.26.5+ only if you want to
run the API or the tests outside a container.

### 1. Environment

```sh
cp .env.example .env
```

Then edit `.env`:

| Variable | Why |
| --- | --- |
| `REDIS_PASSWORD` | **Required.** Docker Compose refuses to start Redis without it, and the API container uses the same value. Pick anything for local dev. |
| `JWT_SECRET` | Signs access tokens. Change it from the example value. |
| `APP_PORT`, `POSTGRES_PORT`, `REDIS_PORT` | Change only if 8080, 5432 or 6379 are already taken on your machine. |
| `TEST_DATABASE_URL`, `TEST_REDIS_*` | Needed to run the tests; see [Tests](#tests). |

### 2. Everything in Docker

```sh
make up            # postgres + redis + the API
make migrate-up    # create the schema (goose)
make seed          # load dev challenges, safe to re-run
make logs
```

The API is then on `http://localhost:${APP_PORT:-8080}`; check
`curl localhost:8080/health`. `make down` stops everything.

### 3. Or run the API locally against containers

```sh
docker compose up -d postgres redis
make migrate-up
make seed
make run
```

`make migrate-up` and `make seed` use `DATABASE_URL` from `.env`, which points
at the published Postgres port, so they work either way.

**Requirements for this path:** Go 1.26.5+, plus
[goose](https://github.com/pressly/goose) for `make migrate-up`. Without
goose installed:

```sh
go run github.com/pressly/goose/v3/cmd/goose@latest \
  -dir internal/db/migrations postgres "$DATABASE_URL" up
```

### Resetting a database created before migrations existed

If `make migrate-up` fails with `relation "users" already exists`, the
database predates goose. Recreate it (this deletes its data):

```sh
# local postgres
dropdb prompters_db && createdb prompters_db
# or the compose volume
make down && docker volume rm backend_postgres_data && make up
```

## Make Commands

| Command | Description |
|---|---|
| `make up` | Start all containers |
| `make down` | Stop all containers |
| `make logs` | Tail container logs |
| `make run` | Build and run server locally |
| `make fmt` | Format Go code |
| `make test` | Run tests |
| `make migrate-up` | Apply all migrations |
| `make migrate-down` | Rollback last migration |
| `make seed` | Load dev challenge data (idempotent, safe to re-run) |
| `make migrate-create <name>` | Create a new migration file |
| `make sqlc` | Regenerate sqlc code |

## Development

See [DEV_GUIDE.md](DEV_GUIDE.md) for the full feature development workflow.

**Quick flow:** domain → migration → sqlc queries → `make sqlc` → repository → service → api handler → wire in main.go

## Tests

```sh
make test
```

Redis tests run against `TEST_REDIS_ADDR` (with `TEST_REDIS_PASSWORD`, and
`TEST_REDIS_DB`, default 15). Each test uses its own key prefix and deletes
its keys afterwards; they are skipped when `TEST_REDIS_ADDR` is unset.

```sh
docker compose up -d postgres redis
make test          # or: go test -race ./...
```

`make test` exports `.env`, so the `TEST_*` variables set there are picked up.
Database tests create a throwaway schema per test and drop it afterwards, so
they leave the database they connect to untouched.

The hub and WebSocket endpoint tests need no extra setup beyond the database
and Redis above; they run real clients against `httptest` servers and assert
with goleak that no goroutine is left behind.

Set `TEST_REDIS_CLUSTER_ADDRS` (comma-separated nodes, optionally with
`TEST_REDIS_CLUSTER_PASSWORD`) to also run the Redis Cluster test, which
checks that a game's keys share one slot. It is skipped when unset.

Repository tests run against `TEST_DATABASE_URL`. Each test migrates a
throwaway schema and drops it afterwards, so existing tables are untouched.
They are skipped when `TEST_DATABASE_URL` is unset. The API integration tests
in `internal/api` use the same database setup.

Run with the race detector to cover the concurrency tests properly:

```sh
go test -race ./...
```

## API

### `POST /api/v1/auth/register`

```json
{ "username": "alice", "email": "alice@example.com", "password": "hunter2secret" }
```

### `GET /api/v1/auth/verify-email?token=...`

Redeems the emailed link and marks the address verified. Tokens last 24 hours
and can be spent once.

### `GET /api/v1/challenges`

Lists challenges. Optional filters, usable alone or together:

- `category`: one of `creative_writing`, `coding`, `data_extraction`, `summarization`, `reasoning`
- `difficulty`: one of `easy`, `medium`, `hard`

Unknown values return 400 with field details.

### `GET /api/v1/challenges/{id}`

Returns one challenge with its `constraints` and `judge_criteria`. Unknown or
malformed IDs return 404.

### `GET /api/v1/challenges/categories`

Returns the list of available categories.

### Games

All game routes require `Authorization: Bearer <access token>`. `{id}` is the
game's UUID or the 6-digit room code of a waiting or in-progress game, so
players can join straight from a shared code: `POST /api/v1/games/004213/join`.

| Method | Path | Success | Notes |
| --- | --- | --- | --- |
| `GET` | `/api/v1/games?status=waiting` | 200 | `status` defaults to `waiting`; newest first, up to 100 |
| `POST` | `/api/v1/games` | 201 | Creator becomes host and first player |
| `GET` | `/api/v1/games/{id}` | 200 | Settings and players |
| `PUT` | `/api/v1/games/{id}/settings` | 200 | Host only, while `waiting`; replaces all settings |
| `POST` | `/api/v1/games/{id}/join` | 200 | |
| `POST` | `/api/v1/games/{id}/leave` | 204 | Only while `waiting` |
| `POST` | `/api/v1/games/{id}/start` | 200 | Host only; `waiting` → `in_progress`, starts round 1 (see [Game engine](#game-engine)) |
| `DELETE` | `/api/v1/games/{id}` | 204 | Host only; sets status `cancelled` |

Settings body, required on create and update:

```json
{ "rounds": 5, "time_per_round": 90, "difficulty": "medium", "category": "coding",
  "ai_model": "claude-sonnet-5", "max_players": 4 }
```

- `rounds`: 3, 5 or 7
- `time_per_round` (seconds): 60, 90 or 120
- `difficulty` and `category`: the challenge values above
- `ai_model`: `claude-opus-5`, `claude-sonnet-5` or `claude-haiku-4-5`
- `max_players`: 2 to 6, and not below the current player count

Rules:

- **Host leaves:** hosting passes to the player who joined earliest. When no
  players remain, the game is cancelled.
- **Start:** needs at least 2 players and at least one challenge for the game's
  category and difficulty.
- **Cancel:** allowed while `waiting` or `in_progress`.
- **Room codes:** unique among active games and reused once a game finishes or
  is cancelled.
- **Concurrency:** every change locks the game row, so concurrent joins cannot
  overfill a room and nothing lands after a game starts.

Status codes: 400 invalid input, 401 unauthenticated, 403 not the host,
404 unknown game, 409 state conflict (already started, room full, already
joined, not a player, not enough players, no eligible challenge).

### Live game state

All routes require authentication. The acting user always comes from the
access token; no route accepts a user ID.

| Method | Path | Success | Notes |
| --- | --- | --- | --- |
| `GET` | `/api/v1/games/{id}/live` | 200 | Reconnect snapshot: lobby, players with `is_ready` and `points`, `round` with server-computed `remaining_ms`, your own `draft`, and the [game engine state](#game-engine) with the stored round deadline |
| `PUT` | `/api/v1/games/{id}/ready` | 204 | Mark yourself ready (`waiting` games) |
| `DELETE` | `/api/v1/games/{id}/ready` | 204 | Unmark |
| `GET` | `/api/v1/games/{id}/draft` | 200 | Your own draft; 404 if you have none |
| `PUT` | `/api/v1/games/{id}/draft` | 200 | `{"content": "..."}`, up to 8 KB, `in_progress` games |
| `POST` | `/api/v1/rounds/{id}/test` | 202 | Run your prompt; see [Prompt testing](#prompt-testing) |
| `GET` | `/api/v1/rounds/{id}/tests` | 200 | Your own tests in the round, by `test_number` |
| `POST` | `/api/v1/rounds/{id}/submit` | 201 / 200 | `{"test_id": "..."}`: your final submission (200 for a repeat) |
| `GET` | `/api/v1/submissions/{id}` | 200 | Yours any time; others' once the round's results are out |
| `GET` | `/api/v1/submissions/{id}/score` | 200 | Scores, rank and rationale once results are out |
| `POST` | `/api/v1/rounds/{id}/judge/retry` | 202 | Host only, after judging failed |
| `POST` | `/api/v1/rounds/{id}/helpers/{hint,suggestion,review}` | 200 | Paid AI helper; see [Helpers](#helpers) |
| `GET` | `/api/v1/rounds/{id}/helpers` | 200 | Your balance, the prices and your own helper use this round |

Only players of the game get live state (403 otherwise). A player can never
read another player's draft. 503 means Redis is unavailable; retry later.

### `GET /health`

`{"status": "ok" | "degraded" | "unavailable", "checks": {"postgres": "up", "redis": "up"}}`.
Returns 503 only when Postgres is down.

### Errors

Every failure has the same shape:

```json
{ "error_code": "VALIDATION_ERROR", "message": "The submitted values are not valid.",
  "fields": { "username": "must be at least 3 characters" } }
```

| Status | `error_code` | Meaning |
| --- | --- | --- |
| 400 | `MALFORMED_REQUEST` | Unparseable body or unknown field |
| 422 | `VALIDATION_ERROR` | Well-formed body, invalid values |
| 409 | `EMAIL_ALREADY_EXISTS` | That email is registered |
| 409 | `USERNAME_ALREADY_EXISTS` | That username is taken |
| 400 | `INVALID_TOKEN` | Verification token missing or unknown |
| 410 | `TOKEN_EXPIRED` | Verification token older than 24 hours |
| 500 | `INTERNAL_ERROR` | Anything else; cause is logged, never returned |

## WebSocket

One connection per game carries live play. The hub only transports events:
services decide what to send and when.

```
GET /ws/games/{id}?token=<access token>
```

`{id}` is the game UUID or an active room code, as for the REST routes.

### Connecting

- **Token:** browsers cannot set headers on a WebSocket handshake, so the
  access token goes in the `token` query parameter. A
  `Sec-WebSocket-Protocol: bearer, <token>` header is accepted too, and is
  echoed back as the `bearer` subprotocol.
- **The handshake is rejected before the upgrade**, as a normal HTTP response:
  401 without a valid token, 404 for an unknown game, 403 for a user who is not
  one of its players, 503 when live state is unavailable.
- **Origin** is checked against same-origin plus `WS_ALLOWED_ORIGINS`.
  Requests with no `Origin` (native and test clients) are allowed.
- **On connect** the player receives `game_state`, including their own draft
  and the stored round deadline, so a refresh never resets the timer.

### Events

Every message is `{"type": ..., "payload": ...}`.

Server to client: `game_state`, `player_joined`, `player_left`, `player_ready`,
`player_typing`, `round_start`, `timer_tick`, `output_chunk`,
`output_complete`, `output_error`, `player_submitted`, `round_end`,
`round_results`, `judging_failed`, `game_end`, `error`.

Client to server: `player_ready`, `prompt_typing`, `heartbeat`,
`submit_prompt` (handled now), and `prompt_test` (answered with an
`unsupported_event` error until the prompt testing service lands).

`submit_prompt` is `{"round_id": "...", "test_id": "..."}`, the same final
submission as `POST /api/v1/rounds/{id}/submit` (see
[Submissions and scoring](#submissions-and-scoring)).

Rules:

- Broadcasts reach only the room of the game they belong to.
- `error`, `output_chunk`, `output_complete`, `output_error` and a player's
  own draft are sent to that one player (all their tabs), never broadcast.
- Before `round_results`, no event carries another player's prompt, output,
  test or score: `player_submitted` is `{player_id, round_id,
  submitted_count, total_players}` only.
- All timing (`round_start`, `timer_tick`, deadlines) comes from the server;
  the client never sets it. Round deadlines are stored in PostgreSQL by the
  game engine.

### Behaviour

| Setting | Default | Meaning |
| --- | --- | --- |
| `WS_PING_INTERVAL` | `30s` | Server ping interval |
| `WS_PONG_WAIT` | `60s` | Silence allowed before a connection is closed; must exceed the ping interval |
| `WS_WRITE_WAIT` | `10s` | Deadline for one socket write |
| `WS_SEND_BUFFER` | `64` | Outbound queue per connection |
| `WS_MAX_MESSAGE_BYTES` | `16384` | Largest accepted client message |
| `WS_MAX_CONNECTIONS_PER_USER` | `3` | Sockets one user may hold in a room |
| `WS_ALLOWED_ORIGINS` | empty | Extra browser origins allowed |

- **Slow clients are disconnected, not served stale.** When a connection's send
  buffer fills, it is closed and logged; game state is sequential, so dropping
  events would leave that player silently wrong. Reconnecting gives them a
  fresh `game_state`. A slow client never blocks its room.
- **Several connections per user are allowed** (two tabs, or a reconnect racing
  an old socket) up to `WS_MAX_CONNECTIONS_PER_USER`; beyond that the oldest is
  closed. All of a user's connections receive their targeted events.
- **Dead connections** are detected by ping/pong and cleaned up; each connection
  has exactly one reader and one writer goroutine, and both exit on disconnect.
  `Shutdown` closes every socket and waits for those goroutines.

## Game engine

`internal/service/game_engine.go` owns every state change of a game in play
and the round timer. The server is the only source of truth; clients display
what they are sent.

### States

| Event | From | To | Guard |
| --- | --- | --- | --- |
| `startGame` | `Lobby` | `RoundActive` | Caller is the host (`POST /start`) |
| `allSubmitted` | `RoundActive` | `Judging` | Every player has submitted |
| `timerExpired` | `RoundActive` | `Judging` | Server time ≥ `round_deadline` |
| `judgeCompleted` | `Judging` | `Results` | |
| `nextRound` | `Results` | `RoundActive` | `current_round < total_rounds` |
| `finalRound` | `Results` | `GameOver` | `current_round == total_rounds` |

Any other pair is rejected with `domain.ErrInvalidTransition` (409) and
changes nothing. The table is `domain/game_phase.go`, which is pure and
tested without a database, sockets or real time.

`games.status` stays the coarse lifecycle: `Lobby` is `waiting`,
`RoundActive`/`Judging`/`Results` are `in_progress`, `GameOver` is
`finished`. A cancelled game keeps its phase and never moves again.

`judgeCompleted` fires once the judging job has committed a round's scores,
and `nextRound`/`finalRound` fire from a server timer `RESULTS_DURATION` after
`Results` begins (re-armed on restart).

### Timing and consistency

- Entering `RoundActive` sets `round_started_at` and `round_deadline` from the
  server clock and stores them in `games` (UTC Unix milliseconds) with a new
  `round_id`, before anything is broadcast.
- A Go timer fires `timerExpired` at the deadline and is stopped when the
  round ends early. Events carry their `round_id`; one for any other round is
  ignored.
- Each transition row-locks the game and stores the new state with a
  compare-and-set on phase, status and `round_id`, so the timer and the last
  submission racing, or two server instances, end a round exactly once.
- **Restart:** on start-up every `RoundActive` game is either ended (deadline
  passed) or has its timer re-armed for the time it has left.

The WebSocket hub is in-process, so with several instances a transition is
only broadcast to clients connected to the instance that made it; the
database stays consistent either way.

### `game_state` fields

Every `game_state` (on connect, and after each transition) carries:

```json
{ "type": "game_state", "payload": { "game": {}, "players": [],
  "state": "RoundActive", "current_round": 2, "total_rounds": 5,
  "round_id": "…", "round_started_at": 1759100000000,
  "round_deadline": 1759100060000, "server_time": 1759100000123 } }
```

`round_*` fields are omitted before round 1. To render the countdown, take
`offset = server_time - client_now` on receipt and show
`round_deadline - (client_now + offset)`. `round_end` (with `round_id` and
`reason`: `all_submitted` or `time_expired`) precedes the `Judging` state.

## Prompt testing

During `RoundActive` a player can run their prompt and watch the output
stream. Every attempt is stored in `prompt_tests` for efficiency scoring and
replay.

`POST /api/v1/rounds/{id}/test` with `{"prompt": "..."}` answers `202` with
`{"data": {"test_id", "test_number", "status": "pending"}}` once the test is
stored; the AI runs afterwards and streams over the WebSocket:

```json
{"type": "output_chunk",    "payload": {"test_id": "…", "test_number": 3, "seq": 12, "delta": "..."}}
{"type": "output_complete", "payload": {"test_id": "…", "test_number": 3, "status": "completed",
                                        "output": "<stored output>", "usage": {"input_tokens": 120, "output_tokens": 340}}}
{"type": "output_error",    "payload": {"test_id": "…", "test_number": 3, "status": "failed",
                                        "error_code": "TIMEOUT", "message": "The AI took too long to answer."}}
```

| Status | `error_code` | When |
| --- | --- | --- |
| 400 | — | Blank prompt, over `PROMPT_TEST_MAX_BYTES`, or not UTF-8 |
| 403 | — | Not a player of the round's game |
| 404 | — | Unknown round |
| 409 | `ROUND_NOT_ACTIVE` | Not the current round, not `RoundActive`, or past the deadline |
| 409 | `TEST_IN_PROGRESS` | `PROMPT_TEST_MAX_CONCURRENT` unfinished tests already |
| 503 | `AI_UNAVAILABLE` | AI is disabled |

- **Consistency:** the checks and the insert run in one transaction holding
  the game's row lock, the same lock every engine transition takes, so a test
  is never accepted for a round that has just ended. The AI call runs after
  the commit, with no lock held.
- **Numbering:** `test_number` starts at 1 per player and round and is
  allocated under that lock (`MAX + 1`, backed by a unique constraint), so
  numbers have no gaps or duplicates. Failed and canceled tests keep theirs.
- **Lifecycle:** `pending` → `streaming` → `completed`, `failed` (the AI
  error's kind, e.g. `TIMEOUT`, `RATE_LIMIT`; partial output is kept) or
  `canceled` (`ROUND_ENDED` at the round deadline, `CANCELED` on shutdown).
  Tests a stopped server left unfinished are failed as `INTERRUPTED` on start.
  The row is written before `output_complete`/`output_error` is sent, so the
  event carries what was stored.
- **Privacy:** `GET .../tests` returns only the caller's own tests; the player
  always comes from the access token, never from a query parameter.

Rounds are recorded in `game_rounds` as they start, so a round keeps resolving
to its game after the game moves on.

## Submissions and scoring

Each round is played on a challenge picked when it starts (the game's
category and difficulty, avoiding repeats while any remain);
`game_state.challenge_id` names it.

### Final submission

`POST /api/v1/rounds/{id}/submit` with `{"test_id": "..."}` makes one of your
completed prompt tests your final entry. Submissions are stored in
`submissions` as a snapshot of the test's prompt and output, locked when
written: there is no update path, and a database trigger refuses any UPDATE.

| Status | `error_code` | When |
| --- | --- | --- |
| 201 | — | Submitted |
| 200 | — | Same `test_id` again: the existing submission |
| 400 | `INVALID_TEST` | Not one of your completed tests in this round |
| 403 | — | Not a player |
| 409 | `ROUND_NOT_ACTIVE` | Not the current round, or the deadline has passed |
| 409 | `ALREADY_SUBMITTED` | You submitted another test |

`prompt_length` is the Unicode code points of the trimmed prompt;
`test_count` is your prompt tests in the round when you submitted, failed and
canceled ones included. The last submission fires `allSubmitted`.

When the timer ends a round, every player without a submission gets their
**last completed test** submitted for them (`source: "auto"`), in the same
transaction, so it is committed before judging reads submissions. A player
with no completed test gets no submission and a zero score marked
`no_submission`. `UNIQUE (round_id, player_id)` with `ON CONFLICT DO NOTHING`
makes a manual submission racing the deadline the only one.

### Judging

- **Once per round:** a worker judges only after claiming the round's
  `judging_runs` row (new, failed, or running for longer than
  `JUDGE_STALE_AFTER`), and writes all scores in the transaction that
  completes its own attempt. Scores are `UNIQUE (round_id, player_id)`. A
  duplicate worker, instance or retry finds the run held or done.
- **Anonymous:** submissions get labels A, B, C… in a fresh random order. The
  judge receives only the challenge, its rubric and each label's prompt and
  output: no player, username, submission ID or timing. Players can still name
  themselves inside their own prompt text; that is not filtered.
- **Failure:** the whole judge call is retried up to `JUDGE_MAX_RUNS` times
  (backoff from `JUDGE_RETRY_DELAY`) on retryable errors and invalid output.
  Then the run is `failed`, the round stays in `Judging`, the room gets
  `judging_failed`, and the host can call
  `POST /api/v1/rounds/{id}/judge/retry`. No partial scores are ever written.
- **Restart:** rounds left in `Judging` are judged again; scores committed but
  not yet published are published without judging again.

### Scores

All components are on a 0–10 scale, stored as integer hundredths.

| Component | Source |
| --- | --- |
| `accuracy` | Judge, against the challenge's own rubric |
| `simplicity` | Judge: how clear and economical the prompt is |
| `creativity` | Judge |
| `adherence` | Judge, against the challenge's constraints |
| `efficiency` | Server: mean of a test score (10 for one test, −1 per extra test, min 0) and a length score (10 up to 100 code points, falling linearly to 0 at 2000) |
| `total` | Weighted mean, weights from `SCORE_WEIGHTS` (equal by default) |

Ranking: `total` desc, then higher `accuracy`, fewer tests, shorter prompt,
earlier submission; exact ties share a rank (1, 1, 3); `no_submission` last.

After the scores commit, the round moves to `Results` and the room receives
`round_results` (ranked, with every prompt, output, score and rationale): the
first moment opponents' work is shown. A reconnect during `Results` gets the
same results in `game_state.round_results`. After `RESULTS_DURATION` the next
round starts, or the game ends after the last.

## Helpers

During `RoundActive` a player can buy AI help with helper points: **100 per
game** (shared by all rounds), set when the game starts.

| Helper | Price | Gives |
| --- | --- | --- |
| `hint` | 10 | A short nudge about what the task needs; never a prompt |
| `suggestion` | 10 (not in the SRS; configurable) | 1–3 concrete edits to your draft (`prompt` required) |
| `review` | 15 | Strengths, weaknesses and likely scoring issues of your prompt and, with `test_id`, its output |

`POST /api/v1/rounds/{id}/helpers/hint` with `{"prompt": "...", "test_id": "..."}`
and an **`Idempotency-Key`** header (required) answers `200`:
`{"data": {"usage_id", "helper_type", "cost", "content", "remaining_balance"}}`.

| Status | `error_code` | When |
| --- | --- | --- |
| 400 | — | No `Idempotency-Key`, input too long, a missing draft, or an invalid `test_id` (`INVALID_TEST`) |
| 402 | `INSUFFICIENT_BALANCE` | Body includes `balance` and `cost` |
| 403 | — | Not a player |
| 409 | `ROUND_NOT_ACTIVE` / `ALREADY_SUBMITTED` | Not the active round, past the deadline, or after your final submission |
| 409 | `IN_PROGRESS` | The same key is still being served |
| 502 / 504 | `error: "HELPER_FAILED"` | The AI failed or timed out; `refunded: true` with `remaining_balance` |
| 503 | `AI_UNAVAILABLE` | AI is disabled |

- **Charging:** one short transaction checks the request, inserts a
  `reserved` `helper_usage` row and takes the points with
  `UPDATE ... WHERE balance >= cost` (backed by `CHECK (balance >= 0)`). The
  AI runs after the commit, with no lock held. Delivery is recorded in a
  transaction before the content is returned; any failure (AI error, timeout,
  empty output, the round ending, a failed write) refunds instead. A refund
  only happens from `reserved`, so it happens once. Reservations older than the
  helper timeout plus 30s are refunded on start and every
  `HELPER_RECOVERY_INTERVAL`.
- **Idempotency:** a repeated key returns the first request's outcome
  (delivered content, or the refunded failure) without charging; while the
  first is still running it gets `409 IN_PROGRESS`.
- **Concurrency:** a player's purchases serialize on their balance row;
  different players' run in parallel (the game row is only share-locked).
- **Privacy:** the AI sees the round's task and the caller's own draft and
  test only, the player's text JSON-escaped inside delimiters it cannot close.
  Helper content is only ever returned to the player who bought it.
  `remaining_balance` is the balance when the helper was delivered.
- **Storage:** PostgreSQL, not Redis: the debit and the usage row must commit
  together, and Redis runs without persistence. Helper use does not affect
  scoring; every usage row is kept for later rules.

## AI layer

`internal/ai` is provider-agnostic: business logic uses its `AIClient`,
`Orchestrator` and `AIError` types only. Provider wire formats live in
`internal/ai/providers/<name>` (Anthropic Messages, OpenAI Chat Completions,
both over plain `net/http` + SSE); only `cmd/server` wires them in, and
`TestProviderCodeStaysInAdapters` fails the build if anything else does.

| Package | Role |
| --- | --- |
| `internal/ai` | Types, `AIError`, `APIKey`, pricing, registry, `Orchestrator` |
| `internal/ai/providers/{anthropic,openai}` | Adapters |
| `internal/ai/providers` | Registry of the built-in adapters |
| `internal/ai/aitest` | Scriptable fake client for tests |

**Roles.** The *executor* runs each player's prompt with the game's
`ai_model`, mapped to a provider and model by `AI_EXECUTOR_MODELS`. The *judge*
scores the outputs against the challenge's rubric and must reply with JSON,
which is validated (invalid output is retried `AI_JUDGE_PARSE_RETRIES` times,
then reported as `invalid_output`). One adapter per provider serves both roles
with different model, timeout and limits.

**Swapping providers** is configuration only: set `AI_EXECUTOR_PROVIDER` /
`AI_JUDGE_PROVIDER` (and the model and mapping variables) to any registered
name; its key is read from `<NAME>_API_KEY`. See `.env.example` for every
variable.

**Calls.** Each call is bounded by its role's timeout, retries included, and
returns `timeout` when it runs out. Retryable kinds (`timeout`, `rate_limit`,
`overloaded`, `unavailable`) are retried up to `AI_RETRY_MAX_ATTEMPTS` with
capped exponential backoff and full jitter, or the provider's `Retry-After`
when longer; never when the caller cancelled or the deadline cannot fit
another wait. Each provider has a client-side token bucket (`AI_RATE_LIMITS`).
Streams are channels that are always closed; a failure before the first chunk
is retried, one after it ends the stream with a terminal error and nothing is
replayed.

**Secrets.** Keys come only from the environment and are held in `ai.APIKey`,
which prints, logs and marshals as `[REDACTED]`; provider messages are scrubbed
of the key before they reach an error. Startup fails with the variable name
(never the key) when a provider in use has none. Logs record provider, role,
model, attempt, latency, tokens, cost, finish reason, error kind and
`game_id`/`round_id`, never prompts or responses unless `AI_LOG_CONTENT=true`.

**Judging.** When a round enters `Judging`, the engine hands it to
`RoundJudge` on its own goroutine, outside the game's lock, so AI latency never
delays a transition or the round timer; see
[Submissions and scoring](#submissions-and-scoring). AI is off unless
`AI_ENABLED=true`, in which case rounds wait in `Judging`.

## Redis (live game state)

PostgreSQL is the source of truth for games, settings and players. Redis
holds fast-changing live state: the lobby snapshot, roster, readiness, the
current round, drafts and helper points.

### Running locally

`docker compose up -d redis` starts Redis 7 on `127.0.0.1:${REDIS_PORT:-6379}`.
It requires `REDIS_PASSWORD` in `.env`; compose refuses to start without it.
The port is bound to localhost only and persistence is off, since everything
in it can be lost or rebuilt.

### Configuration

| Variable | Default | Meaning |
| --- | --- | --- |
| `REDIS_ADDR` | `localhost:6379` | Host and port |
| `REDIS_USERNAME` / `REDIS_PASSWORD` | empty | ACL credentials |
| `REDIS_DB` | `0` | Database index |
| `REDIS_TLS` | `false` | TLS (1.2+, verified against the system roots) |
| `REDIS_TLS_CA_FILE` | empty | PEM bundle to verify the server against instead of the system roots (private CA) |
| `REDIS_TLS_SERVER_NAME` | empty | Overrides the name checked against the server certificate |
| `REDIS_POOL_SIZE` | `20` | Connection pool size |
| `REDIS_DIAL_TIMEOUT` / `REDIS_READ_TIMEOUT` / `REDIS_WRITE_TIMEOUT` | `2s` / `1s` / `1s` | Socket timeouts |
| `REDIS_OP_TIMEOUT` | `2s` | Upper bound for each live-state operation |
| `REDIS_KEY_PREFIX` | `promptgame` | Namespace for every key |
| `GAME_STATE_ACTIVE_TTL` | `2h` | Sliding expiry for waiting and in-progress games |
| `GAME_STATE_ENDED_TTL` | `15m` | Expiry once a game is finished or cancelled |

### Keys and data types

All keys are built in `internal/repository/redisstore/keys.go`:

| Key | Type | Contents |
| --- | --- | --- |
| `promptgame:game:{gameId}:state` | Hash | `status`, `host_id`, `room_code`, `settings` (JSON), `updated_at_ms` |
| `promptgame:game:{gameId}:players` | Hash | user ID → JSON `{user_id, username, joined_at_ms}` |
| `promptgame:game:{gameId}:ready` | Set | ready user IDs |
| `promptgame:game:{gameId}:round` | Hash | `number`, `challenge_id`, `started_at_ms`, `deadline_ms` (server time) |
| `promptgame:game:{gameId}:draft:{userId}` | String | JSON `{content, round_number, updated_at_ms}` |
| `promptgame:game:{gameId}:points` | Hash | user ID → integer (`HINCRBY`) |

The braces around the game ID are a Redis Cluster hash tag, so a game's keys
share one slot. IDs are UUIDs and are checked before a key is built.

- **Atomic writes:** every write is a single Lua script that makes the change
  and then resets the expiry of *all* the game's keys, drafts included. No
  key ever exists without a TTL.
- **Ready, draft and points writes:** the script checks the user is on the
  roster before writing.
- **Reconnect snapshot:** read with `MULTI/EXEC`, so every part comes from the
  same moment.

### When Redis is unavailable

- **At startup:** the API logs a warning and starts anyway (degraded mode);
  go-redis reconnects on its own.
- **`/health`:** reports `redis: down` with status `degraded` (still 200).
  Postgres being down gives 503.
- **Live-state endpoints:** answer `503` with `Retry-After: 5` and a JSON
  error. The failure is logged as
  `live_state op=... game_id=... user_id=... unavailable=true error=...`.
- **Logs never contain** draft content or credentials.
- **Lobby endpoints** keep working. Their changes are mirrored into Redis after
  the Postgres commit, best effort: a failed mirror is logged and does not fail
  the request.

### Reconnecting and rebuilding

`GET /api/v1/games/{id}/live` returns the full live state for a player.

- **Membership** is checked against the Redis roster. If Redis refuses a user,
  Postgres decides, and a stale roster is re-synced.
- **When Redis has no state** for the game (expired, evicted, flushed), the
  lobby and roster are rebuilt from Postgres and written back for active
  games. Readiness, the current round, drafts and points only exist in Redis
  and come back empty. The response has `"rebuilt": true`.

## Email

Set `MAIL_HOST`, `MAIL_PORT`, `MAIL_USERNAME`, `MAIL_PASSWORD` and
`MAIL_FROM_EMAIL` to send through SMTP; on port 587 STARTTLS is
negotiated automatically.

Leave `MAIL_HOST` empty and verification links are written to the server log
instead of emailed, which is usually what you want locally.

`APP_BASE_URL` is the public origin used to build those links.
