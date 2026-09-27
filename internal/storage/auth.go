package storage

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

const userColumns = `id, team_id, email, name, password_hash, role, must_change_password, created_at`

func scanUser(row pgx.Row) (*User, error) {
	var u User
	err := row.Scan(&u.ID, &u.TeamID, &u.Email, &u.Name, &u.PasswordHash, &u.Role, &u.MustChangePassword, &u.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &u, err
}

// UserByEmail looks up a user for login, before any team is known.
func (s *Store) UserByEmail(ctx context.Context, email string) (*User, error) {
	return scanUser(s.pool.QueryRow(ctx,
		`SELECT `+userColumns+` FROM users WHERE email = $1`, strings.ToLower(strings.TrimSpace(email))))
}

// CreateSession stores a new session under the hash of its token.
func (s *Store) CreateSession(ctx context.Context, tokenHash []byte, userID string, expiresAt time.Time) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO sessions (token_hash, user_id, expires_at) VALUES ($1, $2, $3)`,
		tokenHash, userID, expiresAt)
	return err
}

// SessionUser resolves a session token hash to its user, enforcing both the
// absolute expiry and the idle timeout, and bumps last_seen_at. An expired
// or idle session is deleted and reported as ErrNotFound.
func (s *Store) SessionUser(ctx context.Context, tokenHash []byte, idleTimeout time.Duration) (*User, error) {
	u, err := scanUser(s.pool.QueryRow(ctx, `
		UPDATE sessions SET last_seen_at = now()
		FROM users u
		WHERE sessions.token_hash = $1 AND u.id = sessions.user_id
		  AND sessions.expires_at > now() AND sessions.last_seen_at > now() - make_interval(secs => $2)
		RETURNING u.id, u.team_id, u.email, u.name, u.password_hash, u.role, u.must_change_password, u.created_at`,
		tokenHash, idleTimeout.Seconds()))
	if errors.Is(err, ErrNotFound) {
		_, _ = s.pool.Exec(ctx, `DELETE FROM sessions WHERE token_hash = $1`, tokenHash)
	}
	return u, err
}

func (s *Store) DeleteSession(ctx context.Context, tokenHash []byte) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM sessions WHERE token_hash = $1`, tokenHash)
	return err
}

// DeleteUserSessions signs a user out everywhere.
func (s *Store) DeleteUserSessions(ctx context.Context, userID string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM sessions WHERE user_id = $1`, userID)
	return err
}

// SetOwnPassword changes a signed-in user's password and signs out every
// other session of theirs, keeping the one in keepTokenHash.
func (s *Store) SetOwnPassword(ctx context.Context, userID, passwordHash string, keepTokenHash []byte) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx,
		`UPDATE users SET password_hash = $1, must_change_password = false WHERE id = $2`,
		passwordHash, userID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx,
		`DELETE FROM sessions WHERE user_id = $1 AND token_hash <> $2`, userID, keepTokenHash); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// PurgeExpiredSessions removes dead session rows; safe to call periodically.
func (s *Store) PurgeExpiredSessions(ctx context.Context, idleTimeout time.Duration) error {
	_, err := s.pool.Exec(ctx,
		`DELETE FROM sessions WHERE expires_at < now() OR last_seen_at < now() - make_interval(secs => $1)`,
		idleTimeout.Seconds())
	return err
}
