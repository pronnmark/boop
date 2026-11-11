-- Browser push subscriptions (the PWA and any browser that granted
-- notification permission). Each subscription also owns a devices row with
-- platform 'web' so deliveries, the Devices page and retention behave the same.
CREATE TABLE web_subscriptions (
    device_id  TEXT PRIMARY KEY REFERENCES devices(id) ON DELETE CASCADE,
    endpoint   TEXT NOT NULL UNIQUE,
    p256dh     TEXT NOT NULL,
    auth       TEXT NOT NULL,
    user_agent TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);
