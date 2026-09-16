package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// ErrInvalidInvite is returned when an invite token is missing, expired,
// already used, or otherwise unknown. The message shown to clients stays
// generic to avoid giving attackers an oracle.
var ErrInvalidInvite = errors.New("invalid or expired invite code")

// InviteToken is a single-use registration token minted by the operator.
type InviteToken struct {
	ID        int64
	TokenHash string
	Label     string
	CreatedAt int64
	ExpiresAt int64
	UsedAt    sql.NullInt64
}

// CreateInviteToken stores the hash of a new invite token.
func (s *Store) CreateInviteToken(ctx context.Context, tokenHash, label string, expiresAt int64) error {
	_, err := s.db.ExecContext(ctx,
		"INSERT INTO registration_tokens(token_hash, label, created_at, expires_at) VALUES (?, ?, ?, ?)",
		tokenHash, label, time.Now().Unix(), expiresAt)
	if err != nil {
		return fmt.Errorf("create invite token: %w", err)
	}
	return nil
}

// RegisterWithInvite consumes one invite token and creates the account in a
// single transaction. The token burns only on success: any failure rolls the
// transaction back and leaves the token valid.
func (s *Store) RegisterWithInvite(ctx context.Context, keyHash, label, inviteHash string, now int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("register with invite: %w", err)
	}
	defer tx.Rollback()

	res, err := tx.ExecContext(ctx,
		"UPDATE registration_tokens SET used_at = ? WHERE token_hash = ? AND used_at IS NULL AND expires_at > ?",
		now, inviteHash, now)
	if err != nil {
		return fmt.Errorf("consume invite token: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("consume invite token: %w", err)
	}
	if n != 1 {
		return ErrInvalidInvite
	}

	if _, err := tx.ExecContext(ctx,
		"INSERT OR IGNORE INTO accounts(key_hash, label, created_at) VALUES (?, ?, ?)",
		keyHash, label, now); err != nil {
		return fmt.Errorf("create account: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("register with invite: %w", err)
	}
	return nil
}

// ListInviteTokens returns all invite tokens ordered by creation time.
func (s *Store) ListInviteTokens(ctx context.Context) ([]InviteToken, error) {
	rows, err := s.db.QueryContext(ctx,
		"SELECT id, token_hash, label, created_at, expires_at, used_at FROM registration_tokens ORDER BY created_at")
	if err != nil {
		return nil, fmt.Errorf("list invite tokens: %w", err)
	}
	defer rows.Close()

	var out []InviteToken
	for rows.Next() {
		var t InviteToken
		if err := rows.Scan(&t.ID, &t.TokenHash, &t.Label, &t.CreatedAt, &t.ExpiresAt, &t.UsedAt); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// DeleteInviteTokenByID revokes an invite token before it is used.
func (s *Store) DeleteInviteTokenByID(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, "DELETE FROM registration_tokens WHERE id = ?", id)
	if err != nil {
		return fmt.Errorf("delete invite token: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrInvalidInvite
	}
	return nil
}

// GCInviteTokens deletes used or expired invite tokens.
func (s *Store) GCInviteTokens(ctx context.Context, now int64) error {
	_, err := s.db.ExecContext(ctx,
		"DELETE FROM registration_tokens WHERE used_at IS NOT NULL OR expires_at <= ?", now)
	if err != nil {
		return fmt.Errorf("gc invite tokens: %w", err)
	}
	return nil
}
