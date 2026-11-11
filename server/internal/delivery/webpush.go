package delivery

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	webpush "github.com/SherClockHolmes/webpush-go"

	"github.com/chrisgreg/boop/server/internal/apns"
	"github.com/chrisgreg/boop/server/internal/devices"
	"github.com/chrisgreg/boop/server/internal/events"
	"github.com/chrisgreg/boop/server/internal/ids"
	"github.com/chrisgreg/boop/server/internal/projects"
	"github.com/chrisgreg/boop/server/internal/settings"
)

// WebPush delivers events to browser push subscriptions: the installed PWA and
// any browser that granted notification permission.
type WebPush struct {
	db       *sql.DB
	devices  *devices.Store
	log      *slog.Logger
	private  string
	public   string
	subject  string
	timeout  time.Duration
	pushHost string
}

// Subscription is one browser push endpoint owned by a devices row.
type Subscription struct {
	DeviceID   string
	DeviceName string
	Endpoint   string
	P256DH     string
	Auth       string
}

// NewWebPush loads the VAPID pair from settings, generating it on first boot so
// subscriptions survive restarts without extra configuration.
func NewWebPush(ctx context.Context, db *sql.DB, dev *devices.Store, st *settings.Store, subject string, log *slog.Logger) (*WebPush, error) {
	priv, err := st.Get(ctx, settings.KeyVAPIDPrivate, "")
	if err != nil {
		return nil, err
	}
	pub, err := st.Get(ctx, settings.KeyVAPIDPublic, "")
	if err != nil {
		return nil, err
	}
	if priv == "" || pub == "" {
		priv, pub, err = webpush.GenerateVAPIDKeys()
		if err != nil {
			return nil, err
		}
		if err := st.Set(ctx, settings.KeyVAPIDPrivate, priv); err != nil {
			return nil, err
		}
		if err := st.Set(ctx, settings.KeyVAPIDPublic, pub); err != nil {
			return nil, err
		}
		log.Info("webpush.vapid_generated")
	}
	if subject == "" {
		subject = "https://boop.hostbun.cc"
	}
	return &WebPush{db: db, devices: dev, log: log, private: priv, public: pub, subject: subject, timeout: 10 * time.Second}, nil
}

// PublicKey is the VAPID application server key the browser subscribes with.
func (w *WebPush) PublicKey() string { return w.public }

// Subscribe stores or refreshes a browser subscription and returns its device id.
func (w *WebPush) Subscribe(ctx context.Context, userAgent, endpoint, p256dh, auth string) (string, error) {
	if strings.TrimSpace(endpoint) == "" || strings.TrimSpace(p256dh) == "" || strings.TrimSpace(auth) == "" {
		return "", fmt.Errorf("%w: endpoint, p256dh and auth are required", devices.ErrInvalid)
	}
	now := ids.Now()
	var id string
	err := w.db.QueryRowContext(ctx, `SELECT device_id FROM web_subscriptions WHERE endpoint = ?`, endpoint).Scan(&id)
	if err == nil {
		_, err = w.db.ExecContext(ctx, `UPDATE web_subscriptions SET p256dh=?, auth=?, user_agent=?, updated_at=? WHERE device_id=?`,
			p256dh, auth, userAgent, now, id)
		return id, err
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	d, _, err := w.devices.Create(ctx, browserName(userAgent), "web")
	if err != nil {
		return "", err
	}
	_, err = w.db.ExecContext(ctx, `INSERT INTO web_subscriptions (device_id, endpoint, p256dh, auth, user_agent, created_at, updated_at) VALUES (?,?,?,?,?,?,?)`,
		d.ID, endpoint, p256dh, auth, userAgent, now, now)
	if err != nil {
		return "", err
	}
	w.log.Info("webpush.subscribed", "device_id", d.ID, "ua", userAgent)
	return d.ID, nil
}

// Count reports how many browser subscriptions exist.
func (w *WebPush) Count(ctx context.Context) (int, error) {
	var n int
	err := w.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM web_subscriptions`).Scan(&n)
	return n, err
}

func (w *WebPush) subscriptions(ctx context.Context) ([]Subscription, error) {
	rows, err := w.db.QueryContext(ctx, `SELECT s.device_id, COALESCE(d.name, ''), s.endpoint, s.p256dh, s.auth
		FROM web_subscriptions s LEFT JOIN devices d ON d.id = s.device_id ORDER BY s.created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Subscription
	for rows.Next() {
		var s Subscription
		if err := rows.Scan(&s.DeviceID, &s.DeviceName, &s.Endpoint, &s.P256DH, &s.Auth); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// forget removes a subscription the push service says no longer exists.
func (w *WebPush) forget(ctx context.Context, deviceID string) {
	if _, err := w.db.ExecContext(ctx, `DELETE FROM web_subscriptions WHERE device_id = ?`, deviceID); err != nil {
		w.log.Error("webpush.forget_failed", "device_id", deviceID, "error", err.Error())
		return
	}
	if err := w.devices.Delete(ctx, deviceID); err != nil {
		w.log.Error("webpush.device_delete_failed", "device_id", deviceID, "error", err.Error())
	}
}

type pushPayload struct {
	Title   string        `json:"title"`
	Body    string        `json:"body"`
	EventID string        `json:"event_id"`
	Project string        `json:"project"`
	Slug    string        `json:"project_slug"`
	Level   string        `json:"level"`
	URL     string        `json:"url"`
	Actions []events.Action `json:"actions,omitempty"`
}

// Deliver pushes the event to every browser subscription and returns the
// recorded deliveries.
func (w *WebPush) Deliver(ctx context.Context, e events.Event, p projects.Project, n apns.Notification) []Delivery {
	subs, err := w.subscriptions(ctx)
	if err != nil {
		w.log.Error("webpush.failed", "event_id", e.ID, "error", err.Error())
		return nil
	}
	if len(subs) == 0 {
		return nil
	}
	payload, err := json.Marshal(pushPayload{
		Title: n.Title, Body: n.Body, EventID: e.ID, Project: p.Name, Slug: p.Slug,
		Level: e.Level, URL: "/", Actions: e.Actions,
	})
	if err != nil {
		w.log.Error("webpush.payload_failed", "event_id", e.ID, "error", err.Error())
		return nil
	}
	opts := &webpush.Options{
		Subscriber:      w.subject,
		VAPIDPublicKey:  w.public,
		VAPIDPrivateKey: w.private,
		TTL:             86400,
	}
	var out []Delivery
	for _, s := range subs {
		rec := Delivery{ID: ids.New("dlv"), EventID: e.ID, DeviceID: s.DeviceID, DeviceName: s.DeviceName, AttemptedAt: ids.Now()}
		sctx, cancel := context.WithTimeout(ctx, w.timeout)
		res, err := webpush.SendNotificationWithContext(sctx, payload, &webpush.Subscription{
			Endpoint: s.Endpoint,
			Keys:     webpush.Keys{P256dh: s.P256DH, Auth: s.Auth},
		}, opts)
		cancel()
		switch {
		case err != nil:
			rec.Status = StatusFailed
			rec.Error = err.Error()
		case res == nil:
			rec.Status = StatusFailed
			rec.Error = "empty push response"
		default:
			res.Body.Close()
			switch {
			case res.StatusCode == http.StatusNotFound || res.StatusCode == http.StatusGone:
				rec.Status = StatusFailed
				rec.Error = fmt.Sprintf("subscription gone (%d)", res.StatusCode)
				w.forget(ctx, s.DeviceID)
			case res.StatusCode >= 300:
				rec.Status = StatusFailed
				rec.Error = fmt.Sprintf("push service returned %d", res.StatusCode)
			default:
				rec.Status = StatusSent
			}
		}
		if rec.Status == StatusFailed {
			w.log.Warn("webpush.failed", "event_id", e.ID, "device_id", s.DeviceID, "error", rec.Error)
		} else {
			w.log.Info("webpush.sent", "event_id", e.ID, "device_id", s.DeviceID)
		}
		out = append(out, rec)
	}
	return out
}

// browserName labels a subscription in the Devices list from its user agent.
func browserName(ua string) string {
	product := "Browser"
	switch {
	case strings.Contains(ua, "Edg/"):
		product = "Edge"
	case strings.Contains(ua, "OPR/"):
		product = "Opera"
	case strings.Contains(ua, "Firefox/"):
		product = "Firefox"
	case strings.Contains(ua, "Chrome/"):
		product = "Chrome"
	case strings.Contains(ua, "Safari/"):
		product = "Safari"
	}
	platform := ""
	switch {
	case strings.Contains(ua, "Macintosh") || strings.Contains(ua, "Mac OS X"):
		platform = "macOS"
	case strings.Contains(ua, "iPhone") || strings.Contains(ua, "iPad"):
		platform = "iOS"
	case strings.Contains(ua, "Android"):
		platform = "Android"
	case strings.Contains(ua, "Windows"):
		platform = "Windows"
	case strings.Contains(ua, "Linux"):
		platform = "Linux"
	}
	if platform == "" {
		return product
	}
	return product + " · " + platform
}

// Unsubscribe removes a browser subscription by endpoint.
func (w *WebPush) Unsubscribe(ctx context.Context, endpoint string) error {
	var id string
	err := w.db.QueryRowContext(ctx, `SELECT device_id FROM web_subscriptions WHERE endpoint = ?`, endpoint).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	w.forget(ctx, id)
	return nil
}
