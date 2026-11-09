-- Practice credits: AI-evaluated Writing and AI-assessed Speaking in Practice
-- are paid for with their own credits, sold separately and cheaper than a
-- full mock. Stars still pay for full mocks; a full mock's own Writing and
-- Speaking sections are covered by its star (see star_mock_usage).

ALTER TABLE star_wallets
    ADD COLUMN writing_balance integer NOT NULL DEFAULT 0 CHECK (writing_balance >= 0),
    ADD COLUMN speaking_balance integer NOT NULL DEFAULT 0 CHECK (speaking_balance >= 0);
ALTER TABLE star_centre_wallets
    ADD COLUMN writing_balance integer NOT NULL DEFAULT 0 CHECK (writing_balance >= 0),
    ADD COLUMN speaking_balance integer NOT NULL DEFAULT 0 CHECK (speaking_balance >= 0);

-- Every ledger row and purchase now says which balance it moved.
ALTER TABLE star_ledger ADD COLUMN kind text NOT NULL DEFAULT 'star' CHECK (kind IN ('star', 'writing', 'speaking'));
ALTER TABLE star_ledger DROP CONSTRAINT star_ledger_reason_check;
ALTER TABLE star_ledger ADD CONSTRAINT star_ledger_reason_check
    CHECK (reason IN ('welcome', 'full_mock', 'purchase', 'grant', 'refund', 'writing_check', 'speaking_interview'));
ALTER TABLE star_purchases ADD COLUMN kind text NOT NULL DEFAULT 'star' CHECK (kind IN ('star', 'writing', 'speaking'));

-- Prices per Writing check and per Speaking interview: 10 000 so'm each.
ALTER TABLE star_settings
    ADD COLUMN writing_price_minor bigint NOT NULL DEFAULT 1000000 CHECK (writing_price_minor >= 0),
    ADD COLUMN speaking_price_minor bigint NOT NULL DEFAULT 1000000 CHECK (speaking_price_minor >= 0);

-- What a paid full mock has used of its own Writing / Speaking allowance.
-- One row per AI call (item), so a retry of the same call is not counted twice.
CREATE TABLE star_mock_usage (
    user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    mock_reference text NOT NULL,
    kind text NOT NULL CHECK (kind IN ('writing', 'speaking')),
    item text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, kind, item)
);
CREATE INDEX star_mock_usage_mock ON star_mock_usage (user_id, mock_reference, kind);

-- Everyone who already has a wallet gets the welcome Writing check and
-- Speaking interview now; new wallets get them when created.
UPDATE star_wallets SET writing_balance = writing_balance + 1, speaking_balance = speaking_balance + 1;
INSERT INTO star_ledger (user_id, kind, delta, balance_after, reason, reference)
    SELECT user_id, 'writing', 1, writing_balance, 'welcome', 'welcome-writing' FROM star_wallets;
INSERT INTO star_ledger (user_id, kind, delta, balance_after, reason, reference)
    SELECT user_id, 'speaking', 1, speaking_balance, 'welcome', 'welcome-speaking' FROM star_wallets;
