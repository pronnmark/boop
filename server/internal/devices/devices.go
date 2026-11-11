// Package devices manages paired phones and their APNs tokens.
package devices

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/chrisgreg/boop/server/internal/auth"
	"github.com/chrisgreg/boop/server/internal/ids"
)

// ErrNotFound is returned for unknown devices.
var ErrNotFound = errors.New("device not found")

// ErrInvalid wraps validation failures.
var ErrInvalid = errors.New("invalid device")

// Device is a paired client. The credential is never returned after creation.
type Device struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	DeviceToken *string `json:"-"`
	HasToken    bool    `json:"push_registered"`
	Platform    string  `json:"platform"`
	AppBundleID string  `json:"app_bundle_id"`
	LastSeenAt  *string `json:"last_seen_at"`
	CreatedAt   string  `json:"created_at"`
	UpdatedAt   string  `json:"updated_at"`
}

// Registration is the body of POST /devices and PATCH /devices/:id.
type Registration struct {
	DeviceToken *string `json:"device_token"`
	Name        *string `json:"name"`
	Platform    *string `json:"platform"`
	AppBundleID *string `json:"app_bundle_id"`
}

// Store persists devices.
type Store struct {
	db *sql.DB
}

// New returns a Store.
func New(db *sql.DB) *Store { return &Store{db: db} }

const cols = `id, name, device_token, platform, app_bundle_id, last_seen_at, created_at, updated_at`

func scan(row interface{ Scan(...any) error }) (Device, error) {
	var d Device
	err := row.Scan(&d.ID, &d.Name, &d.DeviceToken, &d.Platform, &d.AppBundleID, &d.LastSeenAt, &d.CreatedAt, &d.UpdatedAt)
	d.HasToken = (d.DeviceToken != nil && *d.DeviceToken != "") || d.Platform == "web"
	return d, err
}

// Create inserts a device (without an APNs token yet) and returns it with its raw credential.
func (s *Store) Create(ctx context.Context, name, platform string) (Device, string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		name = "iPhone"
	}
	if platform == "" {
		platform = "ios"
	}
	cred := auth.NewSecret(auth.PrefixDevice)
	now := ids.Now()
	d := Device{ID: ids.New("dev"), Name: name, Platform: platform, CreatedAt: now, UpdatedAt: now}
	_, err := s.db.ExecContext(ctx, `INSERT INTO devices (id, name, platform, credential_hash, created_at, updated_at) VALUES (?,?,?,?,?,?)`,
		d.ID, d.Name, d.Platform, auth.Hash(cred), now, now)
	if err != nil {
		return Device{}, "", err
	}
	return d, cred, nil
}

// Authenticate resolves a raw device credential to its device and bumps last_seen_at.
func (s *Store) Authenticate(ctx context.Context, raw string) (Device, error) {
	if !auth.HasPrefix(raw, auth.PrefixDevice) {
		return Device{}, ErrNotFound
	}
	d, err := scan(s.db.QueryRowContext(ctx, `SELECT `+cols+` FROM devices WHERE credential_hash = ?`, auth.Hash(raw)))
	if errors.Is(err, sql.ErrNoRows) {
		return Device{}, ErrNotFound
	}
	if err != nil {
		return Device{}, err
	}
	now := ids.Now()
	if _, err := s.db.ExecContext(ctx, `UPDATE devices SET last_seen_at = ? WHERE id = ?`, now, d.ID); err != nil {
		return Device{}, err
	}
	d.LastSeenAt = &now
	return d, nil
}

// Register applies a registration to device id. When another device already
// holds the same APNs token (a re-paired phone), that stale device is removed
// so tokens stay unique.
func (s *Store) Register(ctx context.Context, id string, in Registration) (Device, error) {
	d, err := s.Get(ctx, id)
	if err != nil {
		return Device{}, err
	}
	if in.Name != nil {
		n := strings.TrimSpace(*in.Name)
		if n == "" || len(n) > 80 {
			return Device{}, fmt.Errorf("%w: name must be 1-80 characters", ErrInvalid)
		}
		d.Name = n
	}
	if in.Platform != nil && *in.Platform != "" {
		d.Platform = *in.Platform
	}
	if in.AppBundleID != nil {
		d.AppBundleID = strings.TrimSpace(*in.AppBundleID)
	}
	if in.DeviceToken != nil {
		t := strings.TrimSpace(*in.DeviceToken)
		if t == "" {
			d.DeviceToken = nil
		} else {
			if len(t) > 512 {
				return Device{}, fmt.Errorf("%w: device_token is too long", ErrInvalid)
			}
			if _, err := s.db.ExecContext(ctx, `DELETE FROM devices WHERE device_token = ? AND id <> ?`, t, d.ID); err != nil {
				return Device{}, err
			}
			d.DeviceToken = &t
		}
	}
	d.HasToken = d.DeviceToken != nil
	d.UpdatedAt = ids.Now()
	_, err = s.db.ExecContext(ctx, `UPDATE devices SET name=?, device_token=?, platform=?, app_bundle_id=?, updated_at=? WHERE id=?`,
		d.Name, d.DeviceToken, d.Platform, d.AppBundleID, d.UpdatedAt, d.ID)
	return d, err
}

// ClearToken removes a device's APNs token (e.g. after APNs reports it unregistered).
func (s *Store) ClearToken(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE devices SET device_token = NULL, updated_at = ? WHERE id = ?`, ids.Now(), id)
	return err
}

// Get returns a device by id.
func (s *Store) Get(ctx context.Context, id string) (Device, error) {
	d, err := scan(s.db.QueryRowContext(ctx, `SELECT `+cols+` FROM devices WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Device{}, ErrNotFound
	}
	return d, err
}

// List returns all devices, newest first.
func (s *Store) List(ctx context.Context) ([]Device, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+cols+` FROM devices ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Device{}
	for rows.Next() {
		d, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// Pushable returns devices that hold an APNs token.
func (s *Store) Pushable(ctx context.Context) ([]Device, error) {
	all, err := s.List(ctx)
	if err != nil {
		return nil, err
	}
	out := all[:0]
	for _, d := range all {
		if d.HasToken && d.Platform != "web" {
			out = append(out, d)
		}
	}
	return out, nil
}

// Delete removes a device.
func (s *Store) Delete(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM devices WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}
