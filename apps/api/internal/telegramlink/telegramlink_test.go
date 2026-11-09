package telegramlink_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/jackc/pgx/v5/pgxpool"
	migrations "github.com/own/mock-ai/apps/api/db/migrations"
	"github.com/own/mock-ai/apps/api/internal/identity/telegram"
	"github.com/own/mock-ai/apps/api/internal/telegramlink"
)

func setup(t *testing.T) (*pgxpool.Pool, *telegramlink.Service) {
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
	return pool, telegramlink.New(pool)
}

func newUser(t *testing.T, pool *pgxpool.Pool, name string) string {
	t.Helper()
	var id string
	if err := pool.QueryRow(context.Background(), `INSERT INTO users (display_name) VALUES ($1) RETURNING id`, name).Scan(&id); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, id) })
	return id
}

func telegramID() int64 { return 9_000_000_000 + time.Now().UnixNano()%1_000_000_000 }

func TestRedeemConnectsTelegramToTheExistingAccountOnce(t *testing.T) {
	pool, s := setup(t)
	ctx := context.Background()
	user := newUser(t, pool, "Aziza")

	st, err := s.Status(ctx, user)
	if err != nil || st.Linked || st.Started {
		t.Fatalf("fresh status = %+v, %v", st, err)
	}
	token, expires, err := s.CreateToken(ctx, user)
	if err != nil || len(token) < 32 || time.Until(expires) < 25*time.Minute {
		t.Fatalf("token %q expires %v: %v", token, expires, err)
	}
	from := telegram.User{ID: telegramID(), FirstName: "Aziza"}
	name, err := s.Redeem(ctx, token, from, []byte(`{}`))
	if err != nil || name != "Aziza" {
		t.Fatalf("redeem = %q, %v", name, err)
	}
	st, _ = s.Status(ctx, user)
	if !st.Linked || !st.Started || !st.ResultsEnabled {
		t.Fatalf("after redeem status = %+v", st)
	}
	if _, err := s.Redeem(ctx, token, from, []byte(`{}`)); !errors.Is(err, telegramlink.ErrInvalidLink) {
		t.Fatalf("second redeem err = %v, want ErrInvalidLink", err)
	}

	st, err = s.SetResults(ctx, user, false)
	if err != nil || st.ResultsEnabled {
		t.Fatalf("switch off = %+v, %v", st, err)
	}
}

func TestRedeemRefusesATelegramAccountThatBelongsToSomeoneElse(t *testing.T) {
	pool, s := setup(t)
	ctx := context.Background()
	owner, other := newUser(t, pool, "Owner"), newUser(t, pool, "Other")
	from := telegram.User{ID: telegramID(), FirstName: "Owner"}
	if _, err := pool.Exec(ctx, `INSERT INTO user_identities (user_id, provider, provider_subject, profile) VALUES ($1, 'telegram', $2, '{}')`,
		owner, fmt.Sprint(from.ID)); err != nil {
		t.Fatal(err)
	}
	token, _, _ := s.CreateToken(ctx, other)
	if _, err := s.Redeem(ctx, token, from, []byte(`{}`)); !errors.Is(err, telegramlink.ErrTelegramTaken) {
		t.Fatalf("err = %v, want ErrTelegramTaken", err)
	}
	if st, _ := s.Status(ctx, other); st.Linked {
		t.Fatal("other account must stay unlinked")
	}
}

func TestExpiredTokenIsRefused(t *testing.T) {
	pool, s := setup(t)
	ctx := context.Background()
	user := newUser(t, pool, "Late")
	token, _, _ := s.CreateToken(ctx, user)
	if _, err := pool.Exec(ctx, `UPDATE telegram_link_tokens SET expires_at = now() - interval '1 minute' WHERE user_id = $1`, user); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Redeem(ctx, token, telegram.User{ID: telegramID()}, []byte(`{}`)); !errors.Is(err, telegramlink.ErrInvalidLink) {
		t.Fatalf("err = %v, want ErrInvalidLink", err)
	}
}

func TestChatStartedKeepsTheStudentsChoice(t *testing.T) {
	pool, s := setup(t)
	ctx := context.Background()
	user := newUser(t, pool, "Choice")
	if err := s.ChatStarted(ctx, user); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetResults(ctx, user, false); err != nil {
		t.Fatal(err)
	}
	if err := s.ChatStarted(ctx, user); err != nil { // pressing Start again
		t.Fatal(err)
	}
	if st, _ := s.Status(ctx, user); !st.Started || st.ResultsEnabled {
		t.Fatalf("status = %+v, want started with results off", st)
	}
}
