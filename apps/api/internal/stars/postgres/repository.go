// Package postgres stores star wallets, the ledger and purchases.
//
// Every balance change happens in one transaction that locks the wallet row
// (SELECT … FOR UPDATE), so two concurrent requests cannot both spend the
// last star, and the ledger's unique (user, reason, reference) makes repeats
// harmless.
package postgres

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	app "github.com/own/mock-ai/apps/api/internal/stars/application"
)

type Repository struct{ pool *pgxpool.Pool }

func New(pool *pgxpool.Pool) *Repository { return &Repository{pool} }

func (r *Repository) EnsureWallet(ctx context.Context, userID string, welcome int32) (int32, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	var balance int32
	err = tx.QueryRow(ctx, `INSERT INTO star_wallets (user_id, balance) VALUES ($1, $2)
		ON CONFLICT (user_id) DO NOTHING RETURNING balance`, userID, welcome).Scan(&balance)
	switch {
	case err == nil:
		if welcome > 0 {
			if _, err := tx.Exec(ctx, `INSERT INTO star_ledger (user_id, delta, balance_after, reason, reference)
				VALUES ($1, $2, $2, $3, 'welcome')`, userID, welcome, app.ReasonWelcome); err != nil {
				return 0, err
			}
		}
	case errors.Is(err, pgx.ErrNoRows):
		if err := tx.QueryRow(ctx, `SELECT balance FROM star_wallets WHERE user_id = $1`, userID).Scan(&balance); err != nil {
			return 0, err
		}
	default:
		return 0, err
	}
	return balance, tx.Commit(ctx)
}

func (r *Repository) Spend(ctx context.Context, userID, reason, reference string, cost int32) (int32, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	var balance int32
	if err := tx.QueryRow(ctx, `SELECT balance FROM star_wallets WHERE user_id = $1 FOR UPDATE`, userID).Scan(&balance); err != nil {
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
	if err := tx.QueryRow(ctx, `UPDATE star_wallets SET balance = balance - $2, updated_at = now()
		WHERE user_id = $1 RETURNING balance`, userID, cost).Scan(&balance); err != nil {
		return 0, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO star_ledger (user_id, delta, balance_after, reason, reference)
		VALUES ($1, $2, $3, $4, $5)`, userID, -cost, balance, reason, reference); err != nil {
		return 0, err
	}
	return balance, tx.Commit(ctx)
}

const purchaseColumns = `id, COALESCE(user_id::text, ''), COALESCE(centre_id::text, ''), package_id, stars, amount_minor, currency, provider,
	COALESCE(provider_reference, ''), status, COALESCE(failure_reason, ''), created_at, settled_at`

func scanPurchase(row pgx.Row) (app.Purchase, error) {
	var p app.Purchase
	err := row.Scan(&p.ID, &p.UserID, &p.CentreID, &p.PackageID, &p.Stars, &p.AmountMinor, &p.Currency, &p.Provider,
		&p.ProviderReference, &p.Status, &p.FailureReason, &p.CreatedAt, &p.SettledAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return app.Purchase{}, app.ErrNotFound
	}
	return p, err
}

func (r *Repository) CreatePurchase(ctx context.Context, p app.Purchase) (app.Purchase, error) {
	return scanPurchase(r.pool.QueryRow(ctx, `INSERT INTO star_purchases
		(user_id, package_id, stars, amount_minor, currency, provider)
		VALUES ($1, $2, $3, $4, $5, $6) RETURNING `+purchaseColumns,
		p.UserID, p.PackageID, p.Stars, p.AmountMinor, p.Currency, p.Provider))
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

// SettlePaid credits a purchase's stars exactly once. A provider that repeats
// its notification gets the already-paid purchase back and nothing changes.
// A purchase previously marked failed is still credited if the provider later
// confirms payment, because the student's money has been taken.
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
	var balance int32
	if p.CentreID != "" { // bought by a centre for its students
		if _, err := tx.Exec(ctx, `INSERT INTO star_centre_wallets (centre_id, balance) VALUES ($1, 0) ON CONFLICT (centre_id) DO NOTHING`, p.CentreID); err != nil {
			return app.Purchase{}, err
		}
		if err := tx.QueryRow(ctx, `UPDATE star_centre_wallets SET balance = balance + $2, updated_at = now()
			WHERE centre_id = $1 RETURNING balance`, p.CentreID, p.Stars).Scan(&balance); err != nil {
			return app.Purchase{}, err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO star_ledger (centre_id, delta, balance_after, reason, reference)
			VALUES ($1, $2, $3, $4, $5)`, p.CentreID, p.Stars, balance, app.ReasonPurchase, p.ID); err != nil {
			return app.Purchase{}, err
		}
		return p, tx.Commit(ctx)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO star_wallets (user_id, balance) VALUES ($1, 0) ON CONFLICT (user_id) DO NOTHING`, p.UserID); err != nil {
		return app.Purchase{}, err
	}
	if err := tx.QueryRow(ctx, `UPDATE star_wallets SET balance = balance + $2, updated_at = now()
		WHERE user_id = $1 RETURNING balance`, p.UserID, p.Stars).Scan(&balance); err != nil {
		return app.Purchase{}, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO star_ledger (user_id, delta, balance_after, reason, reference)
		VALUES ($1, $2, $3, $4, $5)`, p.UserID, p.Stars, balance, app.ReasonPurchase, p.ID); err != nil {
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

func (r *Repository) EnsureCentreWallet(ctx context.Context, centreID string) (int32, error) {
	if !looksLikeUUID(centreID) {
		return 0, app.ErrNotFound
	}
	var balance int32
	err := r.pool.QueryRow(ctx, `WITH created AS (
			INSERT INTO star_centre_wallets (centre_id, balance)
			SELECT id, 0 FROM centres WHERE id = $1
			ON CONFLICT (centre_id) DO NOTHING RETURNING balance)
		SELECT balance FROM created
		UNION ALL SELECT balance FROM star_centre_wallets WHERE centre_id = $1
		LIMIT 1`, centreID).Scan(&balance)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, app.ErrNotFound
	}
	return balance, err
}

func (r *Repository) SpendFromCentre(ctx context.Context, centreID, userID, reason, reference string, cost int32) (int32, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	var balance int32
	if err := tx.QueryRow(ctx, `SELECT balance FROM star_centre_wallets WHERE centre_id = $1 FOR UPDATE`, centreID).Scan(&balance); err != nil {
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
	if err := tx.QueryRow(ctx, `UPDATE star_centre_wallets SET balance = balance - $2, updated_at = now()
		WHERE centre_id = $1 RETURNING balance`, centreID, cost).Scan(&balance); err != nil {
		return 0, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO star_ledger (user_id, centre_id, delta, balance_after, reason, reference)
		VALUES ($1, $2, $3, $4, $5, $6)`, userID, centreID, -cost, balance, reason, reference); err != nil {
		return 0, err
	}
	return balance, tx.Commit(ctx)
}

func (r *Repository) GrantCentre(ctx context.Context, centreID, adminID string, stars int32, reference, note string) (int32, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	var balance int32
	if err := tx.QueryRow(ctx, `SELECT balance FROM star_centre_wallets WHERE centre_id = $1 FOR UPDATE`, centreID).Scan(&balance); err != nil {
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
	if err := tx.QueryRow(ctx, `UPDATE star_centre_wallets SET balance = balance + $2, updated_at = now()
		WHERE centre_id = $1 RETURNING balance`, centreID, stars).Scan(&balance); err != nil {
		return 0, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO star_ledger (centre_id, admin_id, delta, balance_after, reason, reference, note)
		VALUES ($1, NULLIF($2, '')::uuid, $3, $4, $5, $6, NULLIF($7, ''))`, centreID, adminID, stars, balance, app.ReasonGrant, reference, note); err != nil {
		return 0, err
	}
	return balance, tx.Commit(ctx)
}

func (r *Repository) CentreActivity(ctx context.Context, centreID string, limit int) ([]app.LedgerEntry, error) {
	rows, err := r.pool.Query(ctx, `SELECT l.id, l.reason, l.delta, l.balance_after, COALESCE(u.display_name, ''), COALESCE(l.note, ''), l.created_at
		FROM star_ledger l LEFT JOIN users u ON u.id = l.user_id
		WHERE l.centre_id = $1 ORDER BY l.created_at DESC LIMIT $2`, centreID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	entries := []app.LedgerEntry{}
	for rows.Next() {
		var e app.LedgerEntry
		if err := rows.Scan(&e.ID, &e.Reason, &e.Delta, &e.BalanceAfter, &e.StudentName, &e.Note, &e.CreatedAt); err != nil {
			return nil, err
		}
		entries = append(entries, e)
	}
	return entries, rows.Err()
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
