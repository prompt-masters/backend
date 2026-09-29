package postgres

import (
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
)

func pgUUID(id uuid.UUID) pgtype.UUID {
	return pgtype.UUID{Bytes: id, Valid: true}
}

func uuidFromPg(p pgtype.UUID) uuid.UUID {
	return p.Bytes
}

// pgOptionalUUID stores uuid.Nil as NULL.
func pgOptionalUUID(id uuid.UUID) pgtype.UUID {
	return pgtype.UUID{Bytes: id, Valid: id != uuid.Nil}
}

// pgUnixMilli stores t as UTC Unix milliseconds, and the zero time as NULL.
func pgUnixMilli(t time.Time) pgtype.Int8 {
	return pgtype.Int8{Int64: t.UnixMilli(), Valid: !t.IsZero()}
}
