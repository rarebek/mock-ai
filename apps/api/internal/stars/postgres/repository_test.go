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
		if err != nil || balance != (app.Balances{Star: 1, Writing: 1, Speaking: 1}) {
			t.Fatalf("call %d: balance %d err %v, want 1", i, balance, err)
		}
	}
	var entries int
	// One welcome entry per kind: a star, a Writing check, a Speaking interview.
	var kinds int
	_ = pool.QueryRow(ctx, `SELECT count(*), count(DISTINCT kind) FROM star_ledger WHERE user_id = $1 AND reason = 'welcome'`, user).Scan(&entries, &kinds)
	if entries != 3 || kinds != 3 {
		t.Fatalf("welcome ledger: %d entries across %d kinds, want 3 and 3", entries, kinds)
	}
}

func TestSpendIsIdempotentAndNeverNegative(t *testing.T) {
	pool, repo := setup(t)
	ctx, user := context.Background(), newUser(t, pool)
	_, _ = repo.EnsureWallet(ctx, user, 1)

	if b, err := repo.Spend(ctx, user, app.KindStar, app.ReasonFullMock, "mock-aaaaaaaa", 1); err != nil || b != 0 {
		t.Fatalf("first spend: balance %d err %v", b, err)
	}
	// A retry of the same full mock is not charged again.
	if b, err := repo.Spend(ctx, user, app.KindStar, app.ReasonFullMock, "mock-aaaaaaaa", 1); err != nil || b != 0 {
		t.Fatalf("repeat spend: balance %d err %v", b, err)
	}
	// A new full mock with no stars left is refused.
	if _, err := repo.Spend(ctx, user, app.KindStar, app.ReasonFullMock, "mock-bbbbbbbb", 1); !errors.Is(err, app.ErrInsufficient) {
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
			_, err := repo.Spend(ctx, user, app.KindStar, app.ReasonFullMock, fmt.Sprintf("race-%08d", i), 1)
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
	if b, _ := repo.EnsureWallet(ctx, user, 1); b.Star != 6 {
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
	if b, _ := repo.EnsureWallet(ctx, user, 0); b.Star != 1 {
		t.Fatalf("balance = %d, want 1", b)
	}
	// A failure notice after payment must not undo it.
	if got, _ := repo.SettleFailed(ctx, p.ID, "late"); got.Status != "paid" {
		t.Fatalf("status %q after late failure, want paid", got.Status)
	}
}

func newCentre(t *testing.T, pool *pgxpool.Pool) string {
	t.Helper()
	n := time.Now().UnixNano()
	var id string
	if err := pool.QueryRow(context.Background(), `INSERT INTO centres (slug, name, join_code) VALUES ($1, $2, $3) RETURNING id`,
		fmt.Sprintf("stars-test-%d", n), "Stars Test Centre", fmt.Sprintf("join-code-stars-%d", n)).Scan(&id); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM centres WHERE id = $1`, id) })
	return id
}

func TestCentreStudentSpendsTheCentresStarAndIsRecorded(t *testing.T) {
	pool, repo := setup(t)
	ctx := context.Background()
	centre, student := newCentre(t, pool), newUser(t, pool)
	if _, err := pool.Exec(ctx, `UPDATE users SET centre_id = $1 WHERE id = $2`, centre, student); err != nil {
		t.Fatal(err)
	}
	if c, ok, err := repo.StudentCentre(ctx, student); err != nil || !ok || c.ID != centre {
		t.Fatalf("student centre: %+v %v %v", c, ok, err)
	}
	if b, err := repo.EnsureCentreWallet(ctx, centre); err != nil || b != (app.Balances{}) {
		t.Fatalf("new centre wallet: %d %v", b, err)
	}
	if _, err := repo.SpendFromCentre(ctx, centre, student, app.KindStar, app.ReasonFullMock, "mock-cccccccc", 1); !errors.Is(err, app.ErrCentreInsufficient) {
		t.Fatalf("empty centre: %v", err)
	}
	for i := 0; i < 2; i++ { // a repeated grant (double click, retry) counts once
		if b, err := repo.GrantCentre(ctx, centre, "", app.KindStar, 3, "grant-aaaaaaaa", "Invoice 17"); err != nil || b != 3 {
			t.Fatalf("grant %d: %d %v", i, b, err)
		}
	}
	if b, err := repo.SpendFromCentre(ctx, centre, student, app.KindStar, app.ReasonFullMock, "mock-cccccccc", 1); err != nil || b != 2 {
		t.Fatalf("spend: %d %v", b, err)
	}
	if b, err := repo.SpendFromCentre(ctx, centre, student, app.KindStar, app.ReasonFullMock, "mock-cccccccc", 1); err != nil || b != 2 {
		t.Fatalf("repeat spend charged again: %d %v", b, err)
	}
	activity, err := repo.CentreActivity(ctx, centre, 10)
	if err != nil || len(activity) != 2 || activity[0].Reason != app.ReasonFullMock || activity[0].StudentName == "" || activity[1].Note != "Invoice 17" {
		t.Fatalf("activity: %+v %v", activity, err)
	}
	var personal int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM star_wallets WHERE user_id = $1`, student).Scan(&personal)
	if personal != 0 {
		t.Fatal("a centre student's spend touched a personal wallet")
	}
}

func TestConcurrentCentreSpendsNeverOverdraw(t *testing.T) {
	pool, repo := setup(t)
	ctx := context.Background()
	centre := newCentre(t, pool)
	_, _ = repo.EnsureCentreWallet(ctx, centre)
	_, _ = repo.GrantCentre(ctx, centre, "", app.KindStar, 2, "grant-bbbbbbbb", "")
	students := make([]string, 6)
	for i := range students {
		students[i] = newUser(t, pool)
		_, _ = pool.Exec(ctx, `UPDATE users SET centre_id = $1 WHERE id = $2`, centre, students[i])
	}
	var wg sync.WaitGroup
	results := make(chan error, len(students))
	for i, s := range students {
		wg.Add(1)
		go func(i int, s string) {
			defer wg.Done()
			_, err := repo.SpendFromCentre(ctx, centre, s, app.KindStar, app.ReasonFullMock, fmt.Sprintf("race-%08d", i), 1)
			results <- err
		}(i, s)
	}
	wg.Wait()
	close(results)
	ok := 0
	for err := range results {
		if err == nil {
			ok++
		}
	}
	if ok != 2 {
		t.Fatalf("%d spends succeeded with two centre stars, want 2", ok)
	}
}

func TestCentrePurchaseCreditsTheCentre(t *testing.T) {
	pool, repo := setup(t)
	ctx := context.Background()
	centre := newCentre(t, pool)
	var id string
	if err := pool.QueryRow(ctx, `INSERT INTO star_purchases (centre_id, package_id, stars, amount_minor, currency, provider)
		VALUES ($1, 'stars-50', 50, 100, 'UZS', 'test') RETURNING id`, centre).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if p, err := repo.SettlePaid(ctx, id, "prov-c1"); err != nil || p.Status != "paid" || p.CentreID != centre {
		t.Fatalf("settle: %+v %v", p, err)
	}
	if b, _ := repo.EnsureCentreWallet(ctx, centre); b.Star != 50 {
		t.Fatalf("centre balance %d, want 50", b)
	}
	if _, err := repo.EnsureCentreWallet(ctx, "00000000-0000-0000-0000-000000000000"); !errors.Is(err, app.ErrNotFound) {
		t.Fatalf("unknown centre: %v", err)
	}
}

func TestPricingStartsAtThirtyThousandAndCanChange(t *testing.T) {
	pool, repo := setup(t)
	ctx := context.Background()
	original, err := repo.Pricing(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = repo.SetPricing(ctx, original, "") })
	_, _ = pool.Exec(ctx, `UPDATE star_settings SET price_per_star_minor = 3000000`)
	_, _ = pool.Exec(ctx, `UPDATE star_settings SET writing_price_minor = 1000000, speaking_price_minor = 1000000`)
	if p, _ := repo.Pricing(ctx); p.PricePerStarMinor != 3_000_000 || p.WritingPriceMinor != 1_000_000 || p.SpeakingPriceMinor != 1_000_000 || p.Currency != "UZS" {
		t.Fatalf("default pricing %+v", p)
	}
	if p, err := repo.SetPricing(ctx, app.Pricing{PricePerStarMinor: 2_500_000, WritingPriceMinor: 800_000, SpeakingPriceMinor: 1_200_000}, ""); err != nil ||
		p.PricePerStarMinor != 2_500_000 || p.WritingPriceMinor != 800_000 || p.SpeakingPriceMinor != 1_200_000 {
		t.Fatalf("set pricing: %+v %v", p, err)
	}
}

func TestCentrePurchaseIsListedWithTheCentreAsBuyer(t *testing.T) {
	pool, repo := setup(t)
	ctx := context.Background()
	centre := newCentre(t, pool)
	p, err := repo.CreatePurchase(ctx, app.Purchase{CentreID: centre, PackageID: "stars-40", Stars: 40, AmountMinor: 120_000_000, Currency: "UZS", Provider: "inpay"})
	if err != nil || p.CentreID != centre || p.UserID != "" {
		t.Fatalf("create centre purchase: %+v %v", p, err)
	}
	rows, err := repo.RecentPurchases(ctx, 200)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if row.ID == p.ID {
			if row.BuyerKind != "centre" || row.Buyer != "Stars Test Centre" {
				t.Fatalf("row %+v", row)
			}
			return
		}
	}
	t.Fatal("centre purchase not listed")
}

func TestPracticeCreditsAreSeparateFromStarsAndPerKind(t *testing.T) {
	pool, repo := setup(t)
	ctx, user := context.Background(), newUser(t, pool)
	if b, _ := repo.EnsureWallet(ctx, user, 1); b != (app.Balances{Star: 1, Writing: 1, Speaking: 1}) {
		t.Fatalf("welcome balances %+v", b)
	}
	if b, err := repo.Spend(ctx, user, app.KindWriting, app.ReasonWritingCheck, "writing:essay-1", 1); err != nil || b != 0 {
		t.Fatalf("writing check: %d %v", b, err)
	}
	if b, err := repo.Spend(ctx, user, app.KindWriting, app.ReasonWritingCheck, "writing:essay-1", 1); err != nil || b != 0 {
		t.Fatalf("same essay again: %d %v", b, err)
	}
	if _, err := repo.Spend(ctx, user, app.KindWriting, app.ReasonWritingCheck, "writing:essay-2", 1); !errors.Is(err, app.ErrInsufficient) {
		t.Fatalf("second essay: %v", err)
	}
	if b, _ := repo.EnsureWallet(ctx, user, 1); b != (app.Balances{Star: 1, Writing: 0, Speaking: 1}) {
		t.Fatalf("only writing should move: %+v", b)
	}
	p, err := repo.CreatePurchase(ctx, app.Purchase{UserID: user, Kind: app.KindSpeaking, PackageID: "speaking-5", Stars: 5, AmountMinor: 5_000_000, Currency: "UZS", Provider: "test"})
	if err != nil || p.Kind != app.KindSpeaking {
		t.Fatalf("speaking purchase: %+v %v", p, err)
	}
	if _, err := repo.SettlePaid(ctx, p.ID, "prov-s"); err != nil {
		t.Fatal(err)
	}
	if b, _ := repo.EnsureWallet(ctx, user, 1); b != (app.Balances{Star: 1, Writing: 0, Speaking: 6}) {
		t.Fatalf("speaking purchase credited wrong balance: %+v", b)
	}
}

func TestPaidMockAllowanceCoversItsSectionsOnly(t *testing.T) {
	pool, repo := setup(t)
	ctx, user := context.Background(), newUser(t, pool)
	_, _ = repo.EnsureWallet(ctx, user, 1)
	if ok, _ := repo.UseMockAllowance(ctx, user, "mock-not-paid", app.KindWriting, "writing:a", 2, 48*time.Hour); ok {
		t.Fatal("allowance for a mock nobody paid for")
	}
	if _, err := repo.Spend(ctx, user, app.KindStar, app.ReasonFullMock, "mock-paid-123", 1); err != nil {
		t.Fatal(err)
	}
	for _, item := range []string{"writing:t1", "writing:t2", "writing:t1"} {
		if ok, err := repo.UseMockAllowance(ctx, user, "mock-paid-123", app.KindWriting, item, 2, 48*time.Hour); err != nil || !ok {
			t.Fatalf("%s: ok=%v err=%v", item, ok, err)
		}
	}
	if ok, _ := repo.UseMockAllowance(ctx, user, "mock-paid-123", app.KindWriting, "writing:t3", 2, 48*time.Hour); ok {
		t.Fatal("third essay covered by a two-essay allowance")
	}
	if ok, _ := repo.UseMockAllowance(ctx, user, "mock-paid-123", app.KindSpeaking, "speaking:s1", 2, 48*time.Hour); !ok {
		t.Fatal("speaking not covered by the paid mock")
	}
	// An expired window covers nothing.
	if ok, _ := repo.UseMockAllowance(ctx, user, "mock-paid-123", app.KindSpeaking, "speaking:s2", 2, time.Nanosecond); ok {
		t.Fatal("allowance outside its time window")
	}
}
