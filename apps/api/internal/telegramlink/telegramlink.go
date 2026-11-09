// Package telegramlink connects a student's mock-ai account to Telegram so
// the bot can send them their results.
//
// The app asks for a one-time token and opens t.me/<bot>?start=l_<token>.
// When the student presses Start, the bot calls Redeem with the token and the
// Telegram account that sent it, which attaches that Telegram account to the
// student's existing account. Students who already sign in with Telegram are
// linked from the start; they only need a chat with the bot.
package telegramlink

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/own/mock-ai/apps/api/internal/identity/telegram"
)

// StartPrefix marks a link token in a /start payload: /start l_<token>.
const StartPrefix = "l_"

const tokenLifetime = 30 * time.Minute

var (
	ErrInvalidLink   = errors.New("this link has expired or was already used; open a fresh one from the app")
	ErrTelegramTaken = errors.New("this Telegram account is already connected to another mock-ai account")
)

type Status struct {
	// Linked: the account has a Telegram identity.
	Linked bool `json:"linked"`
	// Started: the student has a chat with the bot, so it can message them.
	Started        bool `json:"started"`
	ResultsEnabled bool `json:"results_enabled"`
}

type Service struct{ pool *pgxpool.Pool }

func New(pool *pgxpool.Pool) *Service { return &Service{pool} }

func hash(token string) []byte {
	h := sha256.Sum256([]byte(token))
	return h[:]
}

// CreateToken issues a one-time link token for the student.
func (s *Service) CreateToken(ctx context.Context, userID string) (string, time.Time, error) {
	raw := make([]byte, 24)
	if _, err := rand.Read(raw); err != nil {
		return "", time.Time{}, err
	}
	token := base64.RawURLEncoding.EncodeToString(raw) // 32 chars, fits Telegram's 64-char start payload
	expires := time.Now().Add(tokenLifetime)
	_, err := s.pool.Exec(ctx, `INSERT INTO telegram_link_tokens (token_hash, user_id, expires_at) VALUES ($1, $2, $3)`, hash(token), userID, expires)
	return token, expires, err
}

func (s *Service) Status(ctx context.Context, userID string) (Status, error) {
	var st Status
	err := s.pool.QueryRow(ctx, `SELECT
			EXISTS (SELECT 1 FROM user_identities WHERE user_id = $1 AND provider = 'telegram'),
			EXISTS (SELECT 1 FROM telegram_preferences WHERE user_id = $1),
			COALESCE((SELECT results_enabled FROM telegram_preferences WHERE user_id = $1), true)`, userID).
		Scan(&st.Linked, &st.Started, &st.ResultsEnabled)
	return st, err
}

// SetResults turns result messages on or off. It only applies once the
// student has a chat with the bot.
func (s *Service) SetResults(ctx context.Context, userID string, enabled bool) (Status, error) {
	if _, err := s.pool.Exec(ctx, `UPDATE telegram_preferences SET results_enabled = $2, updated_at = now() WHERE user_id = $1`, userID, enabled); err != nil {
		return Status{}, err
	}
	return s.Status(ctx, userID)
}

// Redeem attaches the Telegram account that pressed Start to the account the
// token was issued for, and opens result delivery. It returns the student's
// display name for the bot's reply.
func (s *Service) Redeem(ctx context.Context, token string, from telegram.User, rawProfile []byte) (string, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx)
	var userID, name string
	err = tx.QueryRow(ctx, `SELECT t.user_id, u.display_name FROM telegram_link_tokens t JOIN users u ON u.id = t.user_id
		WHERE t.token_hash = $1 AND t.used_at IS NULL AND t.expires_at > now() FOR UPDATE OF t`, hash(token)).Scan(&userID, &name)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrInvalidLink
	}
	if err != nil {
		return "", err
	}
	subject := strconv.FormatInt(from.ID, 10)
	var owner string
	err = tx.QueryRow(ctx, `SELECT user_id FROM user_identities WHERE provider = 'telegram' AND provider_subject = $1`, subject).Scan(&owner)
	switch {
	case err == nil && owner != userID:
		return "", ErrTelegramTaken
	case err == nil: // already linked to this account
	case errors.Is(err, pgx.ErrNoRows):
		// A student can have one Telegram account: re-linking replaces it.
		if _, err := tx.Exec(ctx, `DELETE FROM user_identities WHERE user_id = $1 AND provider = 'telegram'`, userID); err != nil {
			return "", err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO user_identities (user_id, provider, provider_subject, profile) VALUES ($1, 'telegram', $2, $3)`,
			userID, subject, rawProfile); err != nil {
			return "", err
		}
	default:
		return "", err
	}
	if _, err := tx.Exec(ctx, `UPDATE telegram_link_tokens SET used_at = now() WHERE token_hash = $1`, hash(token)); err != nil {
		return "", err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO telegram_preferences (user_id) VALUES ($1)
		ON CONFLICT (user_id) DO UPDATE SET results_enabled = true, updated_at = now()`, userID); err != nil {
		return "", err
	}
	return name, tx.Commit(ctx)
}

// ChatStarted records that a student has a chat with the bot (they pressed
// Start), so results from now on can be delivered.
func (s *Service) ChatStarted(ctx context.Context, userID string) error {
	_, err := s.pool.Exec(ctx, `INSERT INTO telegram_preferences (user_id) VALUES ($1) ON CONFLICT (user_id) DO NOTHING`, userID)
	return err
}
