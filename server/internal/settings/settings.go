// Package settings is a small key/value store backed by the settings table.
package settings

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"strings"

	"github.com/chrisgreg/boop/server/internal/ids"
)

// Well-known keys.
const (
	KeyRetentionDays  = "retention_days"
	KeySetupCompleted = "setup_completed"
	KeyRedactKeys     = "redact_keys" // comma-separated, added to the built-in list
	KeyMCPEnabled     = "mcp_enabled" // "false" turns the /mcp endpoint off; unset = on
	KeyVAPIDPrivate   = "vapid_private" // VAPID key pair for browser push, generated on first boot
	KeyVAPIDPublic    = "vapid_public"
)

// Store reads and writes settings.
type Store struct {
	db *sql.DB
}

// New returns a Store.
func New(db *sql.DB) *Store { return &Store{db: db} }

// Get returns the value for key, or def when unset.
func (s *Store) Get(ctx context.Context, key, def string) (string, error) {
	var v string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return def, nil
	}
	return v, err
}

// Set writes key.
func (s *Store) Set(ctx context.Context, key, value string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO settings (key, value, updated_at) VALUES (?, ?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`, key, value, ids.Now())
	return err
}

// GetInt returns key parsed as an int, or def when unset or unparsable.
func (s *Store) GetInt(ctx context.Context, key string, def int) (int, error) {
	v, err := s.Get(ctx, key, "")
	if err != nil || v == "" {
		return def, err
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def, nil
	}
	return n, nil
}

// GetBool returns key as a bool ("true"/"1").
func (s *Store) GetBool(ctx context.Context, key string) (bool, error) {
	v, err := s.Get(ctx, key, "")
	return v == "true" || v == "1", err
}

// GetList returns a comma-separated setting as a trimmed, non-empty list.
func (s *Store) GetList(ctx context.Context, key string) ([]string, error) {
	v, err := s.Get(ctx, key, "")
	if err != nil || v == "" {
		return nil, err
	}
	var out []string
	for _, p := range strings.Split(v, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out, nil
}

// SetDefault writes key only when it is not already set.
func (s *Store) SetDefault(ctx context.Context, key, value string) error {
	_, err := s.db.ExecContext(ctx, `INSERT OR IGNORE INTO settings (key, value, updated_at) VALUES (?, ?, ?)`, key, value, ids.Now())
	return err
}
