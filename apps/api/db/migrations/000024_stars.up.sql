-- Stars: the unit a student spends to sit a full mock exam.
--
-- star_wallets holds the current balance and can never go negative.
-- star_ledger records every change and why, so a balance can always be
-- explained and audited. A (user, reason, reference) triple is unique, which
-- makes spending and crediting idempotent: retrying the same request never
-- charges or credits twice.
CREATE TABLE star_wallets (
    user_id uuid PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    balance integer NOT NULL CHECK (balance >= 0),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE star_ledger (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    delta integer NOT NULL CHECK (delta <> 0),
    balance_after integer NOT NULL CHECK (balance_after >= 0),
    reason text NOT NULL CHECK (reason IN ('welcome', 'full_mock', 'purchase', 'grant', 'refund')),
    reference text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (user_id, reason, reference)
);
CREATE INDEX star_ledger_user_created ON star_ledger (user_id, created_at DESC);

-- A purchase is created before the student is sent to the payment provider
-- and settled by the provider's server-to-server callback, never by the
-- browser. Amounts are in the currency's minor unit (tiyin for UZS).
CREATE TABLE star_purchases (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    package_id text NOT NULL,
    stars integer NOT NULL CHECK (stars > 0),
    amount_minor bigint NOT NULL CHECK (amount_minor >= 0),
    currency text NOT NULL,
    provider text NOT NULL,
    provider_reference text,
    status text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'paid', 'failed', 'cancelled')),
    failure_reason text,
    created_at timestamptz NOT NULL DEFAULT now(),
    settled_at timestamptz
);
CREATE UNIQUE INDEX star_purchases_provider_reference ON star_purchases (provider, provider_reference) WHERE provider_reference IS NOT NULL;
CREATE INDEX star_purchases_user_created ON star_purchases (user_id, created_at DESC);
