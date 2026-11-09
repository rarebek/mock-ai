// Package postgres stores wallets (stars, Writing checks, Speaking
// interviews), the ledger and purchases.
//
// Every balance change happens in one transaction that locks the wallet row
// (SELECT … FOR UPDATE), so two concurrent requests cannot both spend the
// last credit, and the ledger's unique (user, reason, reference) makes
// repeats harmless.
package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	app "github.com/own/mock-ai/apps/api/internal/stars/application"
)

type Repository struct{ pool *pgxpool.Pool }

func New(pool *pgxpool.Pool) *Repository { return &Repository{pool} }

// column maps a kind to its balance column. Only these fixed names are ever
// interpolated into SQL.
func column(kind app.Kind) (string, error) {
	switch kind {
	case app.KindStar:
		return "balance", nil
	case app.KindWriting:
		return "writing_balance", nil
	case app.KindSpeaking:
		return "speaking_balance", nil
	}
	return "", fmt.Errorf("unknown credit kind %q", kind)
}

const balanceColumns = `balance, writing_balance, speaking_balance`

func scanBalances(row pgx.Row) (app.Balances, error) {
	var b app.Balances
	err := row.Scan(&b.Star, &b.Writing, &b.Speaking)
	return b, err
}

// EnsureWallet creates the wallet the first time a user is seen, with
// `welcome` of each kind, and returns its balances.
func (r *Repository) EnsureWallet(ctx context.Context, userID string, welcome int32) (app.Balances, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return app.Balances{}, err
	}
	defer tx.Rollback(ctx)
	b, err := scanBalances(tx.QueryRow(ctx, `INSERT INTO star_wallets (user_id, balance, writing_balance, speaking_balance)
		VALUES ($1, $2, $2, $2) ON CONFLICT (user_id) DO NOTHING RETURNING `+balanceColumns, userID, welcome))
	switch {
	case err == nil:
		if welcome > 0 {
			for _, k := range []struct{ kind, ref string }{{"star", "welcome"}, {"writing", "welcome-writing"}, {"speaking", "welcome-speaking"}} {
				if _, err := tx.Exec(ctx, `INSERT INTO star_ledger (user_id, kind, delta, balance_after, reason, reference)
					VALUES ($1, $2, $3, $3, $4, $5)`, userID, k.kind, welcome, app.ReasonWelcome, k.ref); err != nil {
					return app.Balances{}, err
				}
			}
		}
	case errors.Is(err, pgx.ErrNoRows):
		if b, err = scanBalances(tx.QueryRow(ctx, `SELECT `+balanceColumns+` FROM star_wallets WHERE user_id = $1`, userID)); err != nil {
			return app.Balances{}, err
		}
	default:
		return app.Balances{}, err
	}
	return b, tx.Commit(ctx)
}

// Spend takes cost of kind from a student's own wallet once per
// (user, reason, reference).
func (r *Repository) Spend(ctx context.Context, userID string, kind app.Kind, reason, reference string, cost int32) (int32, error) {
	col, err := column(kind)
	if err != nil {
		return 0, err
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	var balance int32
	if err := tx.QueryRow(ctx, `SELECT `+col+` FROM star_wallets WHERE user_id = $1 FOR UPDATE`, userID).Scan(&balance); err != nil {
		return 0, err
	}
	var already bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM star_ledger WHERE user_id = $1 AND reason = $2 AND reference = $3)`,
		userID, reason, reference).Scan(&already); err != nil {
		return 0, err
	}
	if already {
		return balance, tx.Commit(ctx)
	}
	if balance < cost {
		return balance, app.ErrInsufficient
	}
	if err := tx.QueryRow(ctx, `UPDATE star_wallets SET `+col+` = `+col+` - $2, updated_at = now()
		WHERE user_id = $1 RETURNING `+col, userID, cost).Scan(&balance); err != nil {
		return 0, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO star_ledger (user_id, kind, delta, balance_after, reason, reference)
		VALUES ($1, $2, $3, $4, $5, $6)`, userID, string(kind), -cost, balance, reason, reference); err != nil {
		return 0, err
	}
	return balance, tx.Commit(ctx)
}

const purchaseColumns = `id, COALESCE(user_id::text, ''), COALESCE(centre_id::text, ''), COALESCE(admin_id::text, ''), kind, package_id,
	stars, amount_minor, currency, provider, COALESCE(provider_reference, ''), status, COALESCE(failure_reason, ''), created_at, settled_at`

func scanPurchase(row pgx.Row) (app.Purchase, error) {
	var p app.Purchase
	var kind string
	err := row.Scan(&p.ID, &p.UserID, &p.CentreID, &p.AdminID, &kind, &p.PackageID, &p.Stars, &p.AmountMinor, &p.Currency, &p.Provider,
		&p.ProviderReference, &p.Status, &p.FailureReason, &p.CreatedAt, &p.SettledAt)
	p.Kind = app.Kind(kind)
	if errors.Is(err, pgx.ErrNoRows) {
		return app.Purchase{}, app.ErrNotFound
	}
	return p, err
}

func (r *Repository) CreatePurchase(ctx context.Context, p app.Purchase) (app.Purchase, error) {
	if p.Kind == "" {
		p.Kind = app.KindStar
	}
	return scanPurchase(r.pool.QueryRow(ctx, `INSERT INTO star_purchases
		(user_id, centre_id, admin_id, kind, package_id, stars, amount_minor, currency, provider)
		VALUES (NULLIF($1, '')::uuid, NULLIF($2, '')::uuid, NULLIF($3, '')::uuid, $4, $5, $6, $7, $8, $9) RETURNING `+purchaseColumns,
		p.UserID, p.CentreID, p.AdminID, string(p.Kind), p.PackageID, p.Stars, p.AmountMinor, p.Currency, p.Provider))
}

func (r *Repository) SetProviderReference(ctx context.Context, purchaseID, reference string) error {
	_, err := r.pool.Exec(ctx, `UPDATE star_purchases SET provider_reference = $2 WHERE id = $1`, purchaseID, reference)
	return err
}

func (r *Repository) Purchase(ctx context.Context, purchaseID string) (app.Purchase, error) {
	if !looksLikeUUID(purchaseID) {
		return app.Purchase{}, app.ErrNotFound
	}
	return scanPurchase(r.pool.QueryRow(ctx, `SELECT `+purchaseColumns+` FROM star_purchases WHERE id = $1`, purchaseID))
}

// SettlePaid credits a purchase exactly once, to the student or centre that
// made it and to the balance of its kind. A provider that repeats its
// notification gets the already-paid purchase back and nothing changes. A
// purchase previously marked failed is still credited if the provider later
// confirms payment, because the money has been taken.
func (r *Repository) SettlePaid(ctx context.Context, purchaseID, providerReference string) (app.Purchase, error) {
	if !looksLikeUUID(purchaseID) {
		return app.Purchase{}, app.ErrNotFound
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return app.Purchase{}, err
	}
	defer tx.Rollback(ctx)
	p, err := scanPurchase(tx.QueryRow(ctx, `SELECT `+purchaseColumns+` FROM star_purchases WHERE id = $1 FOR UPDATE`, purchaseID))
	if err != nil {
		return app.Purchase{}, err
	}
	if p.Status == "paid" {
		return p, tx.Commit(ctx)
	}
	p, err = scanPurchase(tx.QueryRow(ctx, `UPDATE star_purchases SET status = 'paid', settled_at = now(), failure_reason = NULL,
		provider_reference = COALESCE(NULLIF($2, ''), provider_reference)
		WHERE id = $1 RETURNING `+purchaseColumns, purchaseID, providerReference))
	if err != nil {
		return app.Purchase{}, err
	}
	col, err := column(p.Kind)
	if err != nil {
		return app.Purchase{}, err
	}
	var balance int32
	if p.CentreID != "" { // bought by a centre for its students
		if _, err := tx.Exec(ctx, `INSERT INTO star_centre_wallets (centre_id, balance) VALUES ($1, 0) ON CONFLICT (centre_id) DO NOTHING`, p.CentreID); err != nil {
			return app.Purchase{}, err
		}
		if err := tx.QueryRow(ctx, `UPDATE star_centre_wallets SET `+col+` = `+col+` + $2, updated_at = now()
			WHERE centre_id = $1 RETURNING `+col, p.CentreID, p.Stars).Scan(&balance); err != nil {
			return app.Purchase{}, err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO star_ledger (centre_id, kind, delta, balance_after, reason, reference)
			VALUES ($1, $2, $3, $4, $5, $6)`, p.CentreID, string(p.Kind), p.Stars, balance, app.ReasonPurchase, p.ID); err != nil {
			return app.Purchase{}, err
		}
		return p, tx.Commit(ctx)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO star_wallets (user_id, balance) VALUES ($1, 0) ON CONFLICT (user_id) DO NOTHING`, p.UserID); err != nil {
		return app.Purchase{}, err
	}
	if err := tx.QueryRow(ctx, `UPDATE star_wallets SET `+col+` = `+col+` + $2, updated_at = now()
		WHERE user_id = $1 RETURNING `+col, p.UserID, p.Stars).Scan(&balance); err != nil {
		return app.Purchase{}, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO star_ledger (user_id, kind, delta, balance_after, reason, reference)
		VALUES ($1, $2, $3, $4, $5, $6)`, p.UserID, string(p.Kind), p.Stars, balance, app.ReasonPurchase, p.ID); err != nil {
		return app.Purchase{}, err
	}
	return p, tx.Commit(ctx)
}

func (r *Repository) SettleFailed(ctx context.Context, purchaseID, reason string) (app.Purchase, error) {
	if !looksLikeUUID(purchaseID) {
		return app.Purchase{}, app.ErrNotFound
	}
	p, err := scanPurchase(r.pool.QueryRow(ctx, `UPDATE star_purchases SET status = 'failed', failure_reason = $2, settled_at = now()
		WHERE id = $1 AND status = 'pending' RETURNING `+purchaseColumns, purchaseID, reason))
	if errors.Is(err, app.ErrNotFound) {
		return r.Purchase(ctx, purchaseID)
	}
	return p, err
}

func (r *Repository) StudentCentre(ctx context.Context, userID string) (app.Centre, bool, error) {
	var c app.Centre
	err := r.pool.QueryRow(ctx, `SELECT c.id, c.name FROM users u JOIN centres c ON c.id = u.centre_id WHERE u.id = $1`, userID).Scan(&c.ID, &c.Name)
	if errors.Is(err, pgx.ErrNoRows) {
		return app.Centre{}, false, nil
	}
	return c, err == nil, err
}

func (r *Repository) EnsureCentreWallet(ctx context.Context, centreID string) (app.Balances, error) {
	if !looksLikeUUID(centreID) {
		return app.Balances{}, app.ErrNotFound
	}
	b, err := scanBalances(r.pool.QueryRow(ctx, `WITH created AS (
			INSERT INTO star_centre_wallets (centre_id, balance)
			SELECT id, 0 FROM centres WHERE id = $1
			ON CONFLICT (centre_id) DO NOTHING RETURNING `+balanceColumns+`)
		SELECT `+balanceColumns+` FROM created
		UNION ALL SELECT `+balanceColumns+` FROM star_centre_wallets WHERE centre_id = $1
		LIMIT 1`, centreID))
	if errors.Is(err, pgx.ErrNoRows) {
		return app.Balances{}, app.ErrNotFound
	}
	return b, err
}

// SpendFromCentre takes cost of kind from the centre once per
// (student, reason, reference), recording which student used it.
func (r *Repository) SpendFromCentre(ctx context.Context, centreID, userID string, kind app.Kind, reason, reference string, cost int32) (int32, error) {
	col, err := column(kind)
	if err != nil {
		return 0, err
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	var balance int32
	if err := tx.QueryRow(ctx, `SELECT `+col+` FROM star_centre_wallets WHERE centre_id = $1 FOR UPDATE`, centreID).Scan(&balance); err != nil {
		return 0, err
	}
	var already bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM star_ledger WHERE user_id = $1 AND reason = $2 AND reference = $3)`,
		userID, reason, reference).Scan(&already); err != nil {
		return 0, err
	}
	if already {
		return balance, tx.Commit(ctx)
	}
	if balance < cost {
		return balance, app.ErrCentreInsufficient
	}
	if err := tx.QueryRow(ctx, `UPDATE star_centre_wallets SET `+col+` = `+col+` - $2, updated_at = now()
		WHERE centre_id = $1 RETURNING `+col, centreID, cost).Scan(&balance); err != nil {
		return 0, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO star_ledger (user_id, centre_id, kind, delta, balance_after, reason, reference)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`, userID, centreID, string(kind), -cost, balance, reason, reference); err != nil {
		return 0, err
	}
	return balance, tx.Commit(ctx)
}

// GrantCentre adds count of kind to a centre once per reference.
func (r *Repository) GrantCentre(ctx context.Context, centreID, adminID string, kind app.Kind, count int32, reference, note string) (int32, error) {
	col, err := column(kind)
	if err != nil {
		return 0, err
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	var balance int32
	if err := tx.QueryRow(ctx, `SELECT `+col+` FROM star_centre_wallets WHERE centre_id = $1 FOR UPDATE`, centreID).Scan(&balance); err != nil {
		return 0, err
	}
	var already bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM star_ledger WHERE centre_id = $1 AND user_id IS NULL AND reason = $2 AND reference = $3)`,
		centreID, app.ReasonGrant, reference).Scan(&already); err != nil {
		return 0, err
	}
	if already {
		return balance, tx.Commit(ctx)
	}
	if err := tx.QueryRow(ctx, `UPDATE star_centre_wallets SET `+col+` = `+col+` + $2, updated_at = now()
		WHERE centre_id = $1 RETURNING `+col, centreID, count).Scan(&balance); err != nil {
		return 0, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO star_ledger (centre_id, admin_id, kind, delta, balance_after, reason, reference, note)
		VALUES ($1, NULLIF($2, '')::uuid, $3, $4, $5, $6, $7, NULLIF($8, ''))`, centreID, adminID, string(kind), count, balance, app.ReasonGrant, reference, note); err != nil {
		return 0, err
	}
	return balance, tx.Commit(ctx)
}

func (r *Repository) CentreActivity(ctx context.Context, centreID string, limit int) ([]app.LedgerEntry, error) {
	rows, err := r.pool.Query(ctx, `SELECT l.id, l.kind, l.reason, l.delta, l.balance_after, COALESCE(u.display_name, ''), COALESCE(l.note, ''), l.created_at
		FROM star_ledger l LEFT JOIN users u ON u.id = l.user_id
		WHERE l.centre_id = $1 ORDER BY l.created_at DESC LIMIT $2`, centreID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	entries := []app.LedgerEntry{}
	for rows.Next() {
		var e app.LedgerEntry
		var kind string
		if err := rows.Scan(&e.ID, &kind, &e.Reason, &e.Delta, &e.BalanceAfter, &e.StudentName, &e.Note, &e.CreatedAt); err != nil {
			return nil, err
		}
		e.Kind = app.Kind(kind)
		entries = append(entries, e)
	}
	return entries, rows.Err()
}

const pricingColumns = `price_per_star_minor, writing_price_minor, speaking_price_minor, currency, updated_at`

func scanPricing(row pgx.Row) (app.Pricing, error) {
	var p app.Pricing
	err := row.Scan(&p.PricePerStarMinor, &p.WritingPriceMinor, &p.SpeakingPriceMinor, &p.Currency, &p.UpdatedAt)
	return p, err
}

func (r *Repository) Pricing(ctx context.Context) (app.Pricing, error) {
	return scanPricing(r.pool.QueryRow(ctx, `SELECT `+pricingColumns+` FROM star_settings WHERE id`))
}

func (r *Repository) SetPricing(ctx context.Context, p app.Pricing, adminID string) (app.Pricing, error) {
	return scanPricing(r.pool.QueryRow(ctx, `UPDATE star_settings SET price_per_star_minor = $1, writing_price_minor = $2,
		speaking_price_minor = $3, updated_by = NULLIF($4, '')::uuid, updated_at = now()
		WHERE id RETURNING `+pricingColumns, p.PricePerStarMinor, p.WritingPriceMinor, p.SpeakingPriceMinor, adminID))
}

func (r *Repository) RecentPurchases(ctx context.Context, limit int) ([]app.PurchaseRow, error) {
	rows, err := r.pool.Query(ctx, `SELECT p.id, COALESCE(p.user_id::text, ''), COALESCE(p.centre_id::text, ''), COALESCE(p.admin_id::text, ''),
			p.kind, p.package_id, p.stars, p.amount_minor, p.currency, p.provider, COALESCE(p.provider_reference, ''), p.status,
			COALESCE(p.failure_reason, ''), p.created_at, p.settled_at,
			COALESCE(c.name, u.display_name, ''), CASE WHEN p.centre_id IS NOT NULL THEN 'centre' ELSE 'student' END
		FROM star_purchases p LEFT JOIN users u ON u.id = p.user_id LEFT JOIN centres c ON c.id = p.centre_id
		ORDER BY p.created_at DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []app.PurchaseRow{}
	for rows.Next() {
		var row app.PurchaseRow
		p := &row.Purchase
		var kind string
		if err := rows.Scan(&p.ID, &p.UserID, &p.CentreID, &p.AdminID, &kind, &p.PackageID, &p.Stars, &p.AmountMinor, &p.Currency, &p.Provider,
			&p.ProviderReference, &p.Status, &p.FailureReason, &p.CreatedAt, &p.SettledAt, &row.Buyer, &row.BuyerKind); err != nil {
			return nil, err
		}
		p.Kind = app.Kind(kind)
		out = append(out, row)
	}
	return out, rows.Err()
}

func (r *Repository) PendingPurchases(ctx context.Context, limit int) ([]app.Purchase, error) {
	rows, err := r.pool.Query(ctx, `SELECT `+purchaseColumns+` FROM star_purchases WHERE status = 'pending' ORDER BY created_at LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []app.Purchase{}
	for rows.Next() {
		p, err := scanPurchase(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// UseMockAllowance lets an AI call inside a paid full mock go uncharged. It
// succeeds when the student spent a star on mockReference within maxAge and
// that mock has used fewer than limit calls of this kind; the same item is
// never counted twice.
func (r *Repository) UseMockAllowance(ctx context.Context, userID, mockReference string, kind app.Kind, item string, limit int, maxAge time.Duration) (bool, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	var paid bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM star_ledger WHERE user_id = $1 AND reason = $2 AND reference = $3
		AND created_at > now() - make_interval(secs => $4))`, userID, app.ReasonFullMock, mockReference, maxAge.Seconds()).Scan(&paid); err != nil {
		return false, err
	}
	if !paid {
		return false, nil
	}
	// Serialise this mock's allowance so two parallel calls cannot both take
	// the last use.
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, userID+":"+mockReference); err != nil {
		return false, err
	}
	var counted bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM star_mock_usage WHERE user_id = $1 AND kind = $2 AND item = $3)`,
		userID, string(kind), item).Scan(&counted); err != nil {
		return false, err
	}
	if counted {
		return true, tx.Commit(ctx)
	}
	var used int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM star_mock_usage WHERE user_id = $1 AND mock_reference = $2 AND kind = $3`,
		userID, mockReference, string(kind)).Scan(&used); err != nil {
		return false, err
	}
	if used >= limit {
		return false, nil
	}
	if _, err := tx.Exec(ctx, `INSERT INTO star_mock_usage (user_id, mock_reference, kind, item) VALUES ($1, $2, $3, $4)`,
		userID, mockReference, string(kind), item); err != nil {
		return false, err
	}
	return true, tx.Commit(ctx)
}

func looksLikeUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, c := range s {
		switch i {
		case 8, 13, 18, 23:
			if c != '-' {
				return false
			}
		default:
			if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F') {
				return false
			}
		}
	}
	return true
}
