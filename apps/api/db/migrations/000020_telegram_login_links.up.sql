-- One-time login links handed out by the Telegram bot. The raw token only ever
-- exists in the message Telegram delivers; the server keeps a hash, so a
-- database leak cannot be replayed into a session.
CREATE TABLE telegram_login_links (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    token_hash bytea NOT NULL UNIQUE,
    expires_at timestamptz NOT NULL,
    consumed_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now()
);
-- Supports both expiring old links and rate-limiting how many a chat can mint.
CREATE INDEX telegram_login_links_user_idx ON telegram_login_links (user_id, created_at DESC);
