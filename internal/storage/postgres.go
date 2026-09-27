// Package storage is TDMS's own database: slots, current and retired
// instances, teams, users, and scan runs.
package storage

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Schema is the Postgres schema every TDMS table lives in. It is kept out
// of `public` because Supabase serves `public` over its REST Data API,
// which would expose tables like users and sessions to anyone holding the
// project's anon key. A separate schema also lets TDMS share a Supabase
// project with the PSS without their tables mixing.
const Schema = "tdms"

// Connect opens a pooled connection to the TDMS Postgres database, with
// every connection's search_path pinned to the TDMS schema.
func Connect(ctx context.Context, databaseURL string) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, err
	}
	cfg.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
		_, err := conn.Exec(ctx, "SET search_path TO "+Schema)
		return err
	}
	return pgxpool.NewWithConfig(ctx, cfg)
}
