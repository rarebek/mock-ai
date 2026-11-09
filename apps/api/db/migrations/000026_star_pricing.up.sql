-- The price of one star (one full mock), set by a platform administrator in
-- the admin panel. Every package and centre purchase is priced from it at the
-- moment of purchase; the purchase row keeps the amount actually charged.
-- A single row: id is always true.
CREATE TABLE star_settings (
    id boolean PRIMARY KEY DEFAULT true CHECK (id),
    price_per_star_minor bigint NOT NULL CHECK (price_per_star_minor >= 0),
    currency text NOT NULL DEFAULT 'UZS',
    updated_by uuid REFERENCES admins(id) ON DELETE SET NULL,
    updated_at timestamptz NOT NULL DEFAULT now()
);
-- 30 000 so'm; amounts are in tiyin (1 so'm = 100).
INSERT INTO star_settings (price_per_star_minor) VALUES (3000000);
