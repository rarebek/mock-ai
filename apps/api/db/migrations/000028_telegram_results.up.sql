-- Exam results in Telegram.
--
-- A student connects Telegram from the app: the app gets a one-time token,
-- opens t.me/<bot>?start=l_<token>, and the bot links that Telegram account
-- to the student's existing mock-ai account when they press Start. Tokens are
-- stored hashed and expire after 30 minutes.
CREATE TABLE telegram_link_tokens (
    token_hash bytea PRIMARY KEY,
    user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    expires_at timestamptz NOT NULL,
    used_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now()
);

-- A row means the student has a chat with the bot (pressed Start, so the bot
-- may message them). Results produced after chat_started_at are sent while
-- results_enabled is on.
CREATE TABLE telegram_preferences (
    user_id uuid PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    results_enabled boolean NOT NULL DEFAULT true,
    chat_started_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

-- One row per result to send; the primary key means each is sent once.
CREATE TABLE telegram_deliveries (
    kind text NOT NULL CHECK (kind IN ('exam', 'writing', 'speaking')),
    item_id uuid NOT NULL,
    user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    status text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'sent', 'undeliverable', 'failed')),
    attempts integer NOT NULL DEFAULT 0,
    last_error text,
    next_attempt_at timestamptz NOT NULL DEFAULT now(),
    created_at timestamptz NOT NULL DEFAULT now(),
    sent_at timestamptz,
    PRIMARY KEY (kind, item_id)
);
CREATE INDEX telegram_deliveries_due ON telegram_deliveries (next_attempt_at) WHERE status = 'pending';

-- Students who already sign in with Telegram have a chat with the bot.
INSERT INTO telegram_preferences (user_id)
    SELECT DISTINCT user_id FROM user_identities WHERE provider = 'telegram'
    ON CONFLICT (user_id) DO NOTHING;
