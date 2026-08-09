package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

var ErrAccountNotFound = errors.New("account not found")

func (s *Store) CreateAccount(ctx context.Context, keyHash, label string) error {
	_, err := s.db.ExecContext(ctx,
		"INSERT OR IGNORE INTO accounts(key_hash, label, created_at) VALUES (?, ?, ?)",
		keyHash, label, time.Now().Unix())
	if err != nil {
		return fmt.Errorf("create account: %w", err)
	}
	return nil
}

// DeleteAccount removes the account and, via ON DELETE CASCADE, all of its
// synced data.
func (s *Store) DeleteAccount(ctx context.Context, keyHash string) error {
	res, err := s.db.ExecContext(ctx, "DELETE FROM accounts WHERE key_hash = ?", keyHash)
	if err != nil {
		return fmt.Errorf("delete account: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrAccountNotFound
	}
	return nil
}

func (s *Store) AccountByKeyHash(ctx context.Context, keyHash string) (*Account, error) {
	var a Account
	err := s.db.QueryRowContext(ctx,
		"SELECT id, key_hash, label, rev, created_at FROM accounts WHERE key_hash = ?", keyHash).
		Scan(&a.ID, &a.KeyHash, &a.Label, &a.Rev, &a.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrAccountNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("lookup account: %w", err)
	}
	return &a, nil
}

func (s *Store) ListAccounts(ctx context.Context) ([]Account, error) {
	rows, err := s.db.QueryContext(ctx,
		"SELECT id, key_hash, label, rev, created_at FROM accounts ORDER BY created_at")
	if err != nil {
		return nil, fmt.Errorf("list accounts: %w", err)
	}
	defer rows.Close()

	var out []Account
	for rows.Next() {
		var a Account
		if err := rows.Scan(&a.ID, &a.KeyHash, &a.Label, &a.Rev, &a.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}
