package storage

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/soumya-ranjan-000/tdms/internal/model"
)

type Repo struct {
	pool *pgxpool.Pool
}

func NewRepo(pool *pgxpool.Pool) *Repo {
	return &Repo{pool: pool}
}

// UpsertSlot creates the slot for a test case if it doesn't exist yet, or
// updates its block/hash if the hash has changed. changed reports whether
// the block actually differs from what's stored, so the caller knows
// whether to (re)validate/regenerate — "an edit that does not actually
// invalidate the data ... costs nothing" when it hasn't changed.
func (r *Repo) UpsertSlot(
	ctx context.Context,
	testCaseKey, qmetryTCID string,
	class model.Class,
	environment string,
	block *model.Block,
	hash string,
) (changed bool, err error) {
	blockJSON, err := json.Marshal(block)
	if err != nil {
		return false, err
	}

	var existingHash string
	err = r.pool.QueryRow(ctx,
		`SELECT block_hash FROM slots WHERE test_case_key = $1`, testCaseKey,
	).Scan(&existingHash)

	switch {
	case errors.Is(err, pgx.ErrNoRows):
		_, err = r.pool.Exec(ctx, `
			INSERT INTO slots (test_case_key, qmetry_tc_id, class, environment, block, block_hash)
			VALUES ($1, $2, $3, $4, $5, $6)`,
			testCaseKey, qmetryTCID, string(class), environment, blockJSON, hash)
		return true, err
	case err != nil:
		return false, err
	case existingHash == hash:
		return false, nil
	default:
		_, err = r.pool.Exec(ctx, `
			UPDATE slots SET block = $1, block_hash = $2, updated_at = now()
			WHERE test_case_key = $3`,
			blockJSON, hash, testCaseKey)
		return true, err
	}
}
