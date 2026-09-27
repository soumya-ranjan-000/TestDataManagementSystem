// Package storage is TDMS's own database: slots, current and retired
// instances, environment, and (once TDMS calls into a real airline system)
// reservations and traceability links. Not wired into cmd/tdms yet — the
// first milestone proves ingest end-to-end without needing a live
// Postgres instance; this package is the next piece to wire in.
package storage

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Connect opens a pooled connection to the TDMS Postgres database. Schema
// lives in migrations/, applied out of band by a migration tool — this
// package only ever reads/writes rows, never DDL.
func Connect(ctx context.Context, databaseURL string) (*pgxpool.Pool, error) {
	return pgxpool.New(ctx, databaseURL)
}
