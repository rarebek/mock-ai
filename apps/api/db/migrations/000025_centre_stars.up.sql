-- A learning centre buys stars for its students. Its students do not spend
-- personal stars: each full mock they start takes one star from the centre.
CREATE TABLE star_centre_wallets (
    centre_id uuid PRIMARY KEY REFERENCES centres(id) ON DELETE CASCADE,
    balance integer NOT NULL CHECK (balance >= 0),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

-- Ledger rows now belong to a student, a centre, or both:
--   personal spend/purchase  user_id set,  centre_id NULL
--   centre student's spend   user_id set,  centre_id set (who used the centre's star)
--   centre top-up            user_id NULL, centre_id set, admin_id = who granted it
ALTER TABLE star_ledger ALTER COLUMN user_id DROP NOT NULL;
ALTER TABLE star_ledger ADD COLUMN centre_id uuid REFERENCES centres(id) ON DELETE CASCADE;
ALTER TABLE star_ledger ADD COLUMN admin_id uuid REFERENCES admins(id) ON DELETE SET NULL;
ALTER TABLE star_ledger ADD COLUMN note text CHECK (note IS NULL OR length(note) <= 200);
ALTER TABLE star_ledger ADD CONSTRAINT star_ledger_owner CHECK (user_id IS NOT NULL OR centre_id IS NOT NULL);
-- The existing UNIQUE (user_id, reason, reference) does not cover rows with
-- no user (NULLs are distinct), so centre top-ups get their own guard.
CREATE UNIQUE INDEX star_ledger_centre_once ON star_ledger (centre_id, reason, reference) WHERE user_id IS NULL;
CREATE INDEX star_ledger_centre_created ON star_ledger (centre_id, created_at DESC) WHERE centre_id IS NOT NULL;

-- A purchase can later be made by a centre (through its administrator).
ALTER TABLE star_purchases ALTER COLUMN user_id DROP NOT NULL;
ALTER TABLE star_purchases ADD COLUMN centre_id uuid REFERENCES centres(id) ON DELETE CASCADE;
ALTER TABLE star_purchases ADD COLUMN admin_id uuid REFERENCES admins(id) ON DELETE SET NULL;
ALTER TABLE star_purchases ADD CONSTRAINT star_purchases_owner CHECK (user_id IS NOT NULL OR centre_id IS NOT NULL);
