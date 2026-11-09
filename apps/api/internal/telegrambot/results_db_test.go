package telegrambot

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/jackc/pgx/v5/pgxpool"
	migrations "github.com/own/mock-ai/apps/api/db/migrations"
)

// The worker's guarantees (each result sent once, only results produced after
// the student connected, nothing for students who switched it off) live in
// SQL, so this runs against a real database.
func TestResultsAreDeliveredOnceAfterConnecting(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set")
	}
	src, _ := iofs.New(migrations.Files, ".")
	m, err := migrate.NewWithSourceInstance("iofs", src, dsn)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		t.Fatal(err)
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	subject := time.Now().UnixNano() % 1_000_000_000
	var userID, quietID string
	for _, q := range []struct {
		id      *string
		subject int64
		enabled bool
	}{{&userID, subject, true}, {&quietID, subject + 1, false}} {
		if err := pool.QueryRow(ctx, `INSERT INTO users (display_name) VALUES ('Results test') RETURNING id`).Scan(q.id); err != nil {
			t.Fatal(err)
		}
		id := *q.id
		t.Cleanup(func() {
			_, _ = pool.Exec(ctx, `DELETE FROM telegram_deliveries WHERE user_id = $1`, id)
			_, _ = pool.Exec(ctx, `DELETE FROM writing_submissions WHERE user_id = $1`, id)
			_, _ = pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, id)
		})
		if _, err := pool.Exec(ctx, `INSERT INTO user_identities (user_id, provider, provider_subject, profile) VALUES ($1, 'telegram', $2, '{}')`, id, strconv.FormatInt(q.subject, 10)); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `INSERT INTO telegram_preferences (user_id, results_enabled, chat_started_at) VALUES ($1, $2, now() - interval '1 hour')`, id, q.enabled); err != nil {
			t.Fatal(err)
		}
	}
	report := `{"overall":7,"criteria":[{"key":"task_response","score":7}],"next_steps":["Keep going."]}`
	insert := func(user string, age string) {
		if _, err := pool.Exec(ctx, `INSERT INTO writing_submissions (user_id, prompt, essay, status, report, reference, updated_at)
			VALUES ($1, 'Prompt', 'Essay', 'completed', $2, '{"task":"task_2"}', now() - $3::interval)`, user, report, age); err != nil {
			t.Fatal(err)
		}
	}
	insert(userID, "1 minute")  // after connecting: sent
	insert(userID, "2 hours")   // before connecting: not sent
	insert(quietID, "1 minute") // results switched off: not sent

	var mu sync.Mutex
	var sent []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		sent = append(sent, body)
		mu.Unlock()
		_, _ = w.Write([]byte(`{"ok":true,"result":{}}`))
	}))
	defer server.Close()
	bot, _ := New("test-token", "https://mock.example", "https://mock.example", &userStoreStub{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	bot.apiBase = server.URL

	for range 2 { // a second pass must not send again
		if err := discoverResults(ctx, pool); err != nil {
			t.Fatal(err)
		}
		if err := bot.deliverResults(ctx, pool); err != nil {
			t.Fatal(err)
		}
	}
	var mine []map[string]any
	for _, s := range sent {
		if id, _ := s["chat_id"].(float64); int64(id) == subject || int64(id) == subject+1 {
			mine = append(mine, s)
		}
	}
	if len(mine) != 1 {
		t.Fatalf("sent %d messages, want 1: %v", len(mine), mine)
	}
	if text, _ := mine[0]["text"].(string); !strings.Contains(text, "Overall band <b>7.0</b>") || !strings.Contains(text, "Task 2") {
		t.Fatalf("text = %q", text)
	}
	var status string
	if err := pool.QueryRow(ctx, `SELECT status FROM telegram_deliveries WHERE user_id = $1`, userID).Scan(&status); err != nil || status != "sent" {
		t.Fatalf("delivery status = %q, %v", status, err)
	}
}

func TestBlockedBotMarksTheDeliveryUndeliverable(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	var userID string
	if err := pool.QueryRow(ctx, `INSERT INTO users (display_name) VALUES ('Blocked test') RETURNING id`).Scan(&userID); err != nil {
		t.Skip("database not migrated yet: ", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM telegram_deliveries WHERE user_id = $1`, userID)
		_, _ = pool.Exec(ctx, `DELETE FROM writing_submissions WHERE user_id = $1`, userID)
		_, _ = pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, userID)
	})
	if _, err := pool.Exec(ctx, `INSERT INTO user_identities (user_id, provider, provider_subject, profile) VALUES ($1, 'telegram', $2, '{}')`, userID, strconv.FormatInt(1_100_000_000+time.Now().UnixNano()%1_000_000, 10)); err != nil {
		t.Fatal(err)
	}
	var itemID string
	if err := pool.QueryRow(ctx, `INSERT INTO writing_submissions (user_id, prompt, essay, status, report) VALUES ($1, 'P', 'E', 'completed', '{"overall":6}') RETURNING id`, userID).Scan(&itemID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO telegram_deliveries (kind, item_id, user_id) VALUES ('writing', $1, $2)`, itemID, userID); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"ok":false,"error_code":403,"description":"Forbidden: bot was blocked by the user"}`))
	}))
	defer server.Close()
	bot, _ := New("test-token", "https://mock.example", "https://mock.example", &userStoreStub{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	bot.apiBase = server.URL
	bot.deliver(ctx, pool, delivery{kind: "writing", itemID: itemID, userID: userID, attempts: 1})
	var status string
	if err := pool.QueryRow(ctx, `SELECT status FROM telegram_deliveries WHERE kind = 'writing' AND item_id = $1`, itemID).Scan(&status); err != nil || status != "undeliverable" {
		t.Fatalf("status = %q, %v", status, err)
	}
}
