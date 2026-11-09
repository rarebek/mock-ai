package postgres_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/jackc/pgx/v5/pgxpool"
	migrations "github.com/own/mock-ai/apps/api/db/migrations"
	app "github.com/own/mock-ai/apps/api/internal/stars/application"
	starspg "github.com/own/mock-ai/apps/api/internal/stars/postgres"
)

// These run against a real database because the guarantees that matter
// (never negative, never charged twice, credited once) live in SQL.
func setup(t *testing.T) (*pgxpool.Pool, *starspg.Repository) {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set")
	}
	src, err := iofs.New(migrations.Files, ".")
	if err != nil {
		t.Fatal(err)
	}
	m, err := migrate.NewWithSourceInstance("iofs", src, dsn)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		t.Fatal(err)
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool, starspg.New(pool)
}

func newUser(t *testing.T, pool *pgxpool.Pool) string {
	t.Helper()
	var id string
	if err := pool.QueryRow(context.Background(), `INSERT INTO users (display_name) VALUES ($1) RETURNING id`,
		fmt.Sprintf("Stars test %d", time.Now().UnixNano())).Scan(&id); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, id) })
	return id
}

func TestWelcomeStarIsGrantedOnce(t *testing.T) {
	pool, repo := setup(t)
	ctx, user := context.Background(), newUser(t, pool)
	for i := 0; i < 3; i++ {
		balance, err := repo.EnsureWallet(ctx, user, 1)
		if err != nil || balance != 1 {
			t.Fatalf("call %d: balance %d err %v, want 1", i, balance, err)
		}
	}
	var entries int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM star_ledger WHERE user_id = $1 AND reason = 'welcome'`, user).Scan(&entries)
	if entries != 1 {
		t.Fatalf("welcome ledger entries = %d, want 1", entries)
	}
}

func TestSpendIsIdempotentAndNeverNegative(t *testing.T) {
	pool, repo := setup(t)
	ctx, user := context.Background(), newUser(t, pool)
	_, _ = repo.EnsureWallet(ctx, user, 1)

	if b, err := repo.Spend(ctx, user, app.ReasonFullMock, "mock-aaaaaaaa", 1); err != nil || b != 0 {
		t.Fatalf("first spend: balance %d err %v", b, err)
	}
	// A retry of the same full mock is not charged again.
	if b, err := repo.Spend(ctx, user, app.ReasonFullMock, "mock-aaaaaaaa", 1); err != nil || b != 0 {
		t.Fatalf("repeat spend: balance %d err %v", b, err)
	}
	// A new full mock with no stars left is refused.
	if _, err := repo.Spend(ctx, user, app.ReasonFullMock, "mock-bbbbbbbb", 1); !errors.Is(err, app.ErrInsufficient) {
		t.Fatalf("spend at zero: err %v, want ErrInsufficient", err)
	}
}

func TestConcurrentSpendsTakeTheLastStarOnce(t *testing.T) {
	pool, repo := setup(t)
	ctx, user := context.Background(), newUser(t, pool)
	_, _ = repo.EnsureWallet(ctx, user, 1)

	var wg sync.WaitGroup
	results := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := repo.Spend(ctx, user, app.ReasonFullMock, fmt.Sprintf("race-%08d", i), 1)
			results <- err
		}(i)
	}
	wg.Wait()
	close(results)
	ok := 0
	for err := range results {
		if err == nil {
			ok++
		} else if !errors.Is(err, app.ErrInsufficient) {
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if ok != 1 {
		t.Fatalf("%d spends succeeded with one star, want 1", ok)
	}
}

func TestPaidPurchaseCreditsOnceEvenIfRepeated(t *testing.T) {
	pool, repo := setup(t)
	ctx, user := context.Background(), newUser(t, pool)
	_, _ = repo.EnsureWallet(ctx, user, 1)

	p, err := repo.CreatePurchase(ctx, app.Purchase{UserID: user, PackageID: "stars-5", Stars: 5, AmountMinor: 100, Currency: "UZS", Provider: "test"})
	if err != nil || p.Status != "pending" {
		t.Fatalf("create: %+v %v", p, err)
	}
	for i := 0; i < 3; i++ { // providers retry notifications
		if got, err := repo.SettlePaid(ctx, p.ID, "prov-123"); err != nil || got.Status != "paid" {
			t.Fatalf("settle %d: %+v %v", i, got, err)
		}
	}
	if b, _ := repo.EnsureWallet(ctx, user, 1); b != 6 {
		t.Fatalf("balance after purchase = %d, want 6 (1 welcome + 5 bought)", b)
	}
}

func TestPaymentConfirmedAfterFailureStillCredits(t *testing.T) {
	pool, repo := setup(t)
	ctx, user := context.Background(), newUser(t, pool)
	_, _ = repo.EnsureWallet(ctx, user, 0)
	p, _ := repo.CreatePurchase(ctx, app.Purchase{UserID: user, PackageID: "stars-1", Stars: 1, AmountMinor: 100, Currency: "UZS", Provider: "test"})
	if got, _ := repo.SettleFailed(ctx, p.ID, "timeout"); got.Status != "failed" {
		t.Fatalf("status %q, want failed", got.Status)
	}
	if got, _ := repo.SettlePaid(ctx, p.ID, ""); got.Status != "paid" {
		t.Fatalf("status %q, want paid", got.Status)
	}
	if b, _ := repo.EnsureWallet(ctx, user, 0); b != 1 {
		t.Fatalf("balance = %d, want 1", b)
	}
	// A failure notice after payment must not undo it.
	if got, _ := repo.SettleFailed(ctx, p.ID, "late"); got.Status != "paid" {
		t.Fatalf("status %q after late failure, want paid", got.Status)
	}
}
