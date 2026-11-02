package identity

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/own/mock-ai/apps/api/internal/identity/domain"
	identitypostgres "github.com/own/mock-ai/apps/api/internal/identity/postgres"
	"github.com/own/mock-ai/apps/api/internal/identity/telegram"
)

var (
	ErrInvalidInput      = errors.New("invalid input")
	ErrInvalidSession    = errors.New("invalid or expired session")
	ErrTelegramDisabled  = errors.New("Telegram authentication is not configured")
	ErrInvalidLoginLink  = errors.New("this login link has expired or has already been used")
	ErrTooManyLoginLinks = errors.New("too many login links requested; try again later")
)

type Repository interface {
	IssueLoginLink(ctx context.Context, userID string, tokenHash []byte, expiresAt time.Time) error
	RecentLoginLinks(ctx context.Context, userID string, since time.Time) (int64, error)
	ConsumeLoginLink(ctx context.Context, tokenHash []byte) (domain.User, error)
	FindOrCreateExternal(ctx context.Context, profile domain.ExternalProfile) (domain.User, bool, error)
	CreateSession(ctx context.Context, userID string, tokenHash []byte, expiresAt time.Time, userAgent, ipAddress string) error
	UserBySession(ctx context.Context, tokenHash []byte) (domain.User, error)
	RevokeSession(ctx context.Context, tokenHash []byte) error
}

type Session struct {
	Token     string
	ExpiresAt time.Time
}

type Service struct {
	repository        Repository
	telegramValidator *telegram.Validator
	telegramEnabled   bool
	sessionTTL        time.Duration
}

func NewService(repository Repository, telegramBotToken string, sessionTTL time.Duration) (*Service, error) {
	return &Service{
		repository:        repository,
		telegramValidator: telegram.NewValidator(telegramBotToken, 10*time.Minute),
		telegramEnabled:   telegramBotToken != "",
		sessionTTL:        sessionTTL,
	}, nil
}

// loginLinkTTL is how long a link the bot sends stays usable. Long enough to
// walk from Telegram to a browser, short enough that a link left in a chat
// history is not a standing key to the account.
const loginLinkTTL = 15 * time.Minute

// loginLinkBurst caps how many links one account can mint in an hour, so
// hammering /start cannot fill the table or paper a chat with live tokens.
const loginLinkBurst = 10

// IssueLoginLink mints a one-time login token for a user. The caller is the
// Telegram bot, which has already proved the account by receiving a private
// message from it; the raw token is returned once and never stored.
func (s *Service) IssueLoginLink(ctx context.Context, userID string) (string, time.Time, error) {
	if !s.telegramEnabled {
		return "", time.Time{}, ErrTelegramDisabled
	}
	recent, err := s.repository.RecentLoginLinks(ctx, userID, time.Now().Add(-time.Hour))
	if err != nil {
		return "", time.Time{}, err
	}
	if recent >= loginLinkBurst {
		return "", time.Time{}, ErrTooManyLoginLinks
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", time.Time{}, fmt.Errorf("generate login token: %w", err)
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	hash := sha256.Sum256([]byte(token))
	expiresAt := time.Now().Add(loginLinkTTL)
	if err := s.repository.IssueLoginLink(ctx, userID, hash[:], expiresAt); err != nil {
		return "", time.Time{}, err
	}
	return token, expiresAt, nil
}

// LoginWithLink spends a login token and starts a session. Spending is atomic
// in the database, so a link that is tapped twice only ever logs in once.
func (s *Service) LoginWithLink(ctx context.Context, token, userAgent, ipAddress string) (domain.User, Session, error) {
	hash, err := tokenHash(token)
	if err != nil {
		return domain.User{}, Session{}, ErrInvalidLoginLink
	}
	user, err := s.repository.ConsumeLoginLink(ctx, hash)
	if errors.Is(err, identitypostgres.ErrNotFound) {
		return domain.User{}, Session{}, ErrInvalidLoginLink
	}
	if err != nil {
		return domain.User{}, Session{}, err
	}
	session, err := s.newSession(ctx, user.ID, userAgent, ipAddress)
	return user, session, err
}

func (s *Service) LoginTelegram(ctx context.Context, initData, userAgent, ipAddress string) (domain.User, Session, error) {
	if !s.telegramEnabled {
		return domain.User{}, Session{}, ErrTelegramDisabled
	}
	result, err := s.telegramValidator.Validate(initData)
	if err != nil {
		return domain.User{}, Session{}, err
	}
	user, _, err := s.repository.FindOrCreateExternal(ctx, domain.ExternalProfile{
		Provider:    "telegram",
		Subject:     fmt.Sprintf("%d", result.User.ID),
		DisplayName: result.User.DisplayName(),
		AvatarURL:   result.User.PhotoURL,
		RawProfile:  result.RawProfile,
	})
	if err != nil {
		return domain.User{}, Session{}, err
	}
	session, err := s.newSession(ctx, user.ID, userAgent, ipAddress)
	return user, session, err
}

func (s *Service) LoginExternal(ctx context.Context, profile domain.ExternalProfile, userAgent, ipAddress string) (domain.User, Session, error) {
	if profile.Provider == "" || profile.Subject == "" || profile.DisplayName == "" {
		return domain.User{}, Session{}, ErrInvalidInput
	}
	user, _, err := s.repository.FindOrCreateExternal(ctx, profile)
	if err != nil {
		return domain.User{}, Session{}, err
	}
	session, err := s.newSession(ctx, user.ID, userAgent, ipAddress)
	return user, session, err
}

// EnsureTelegramUser creates or refreshes a Telegram-backed student without
// creating a browser session. It is used by the bot when a student sends
// /start; the Mini App still establishes its own session from signed initData.
func (s *Service) EnsureTelegramUser(ctx context.Context, user telegram.User, rawProfile []byte) (domain.User, bool, error) {
	if !s.telegramEnabled {
		return domain.User{}, false, ErrTelegramDisabled
	}
	if user.ID == 0 || strings.TrimSpace(user.DisplayName()) == "" {
		return domain.User{}, false, ErrInvalidInput
	}
	return s.repository.FindOrCreateExternal(ctx, domain.ExternalProfile{
		Provider:    "telegram",
		Subject:     fmt.Sprintf("%d", user.ID),
		DisplayName: user.DisplayName(),
		AvatarURL:   user.PhotoURL,
		RawProfile:  rawProfile,
	})
}

func (s *Service) Authenticate(ctx context.Context, token string) (domain.User, error) {
	hash, err := tokenHash(token)
	if err != nil {
		return domain.User{}, ErrInvalidSession
	}
	user, err := s.repository.UserBySession(ctx, hash)
	if errors.Is(err, identitypostgres.ErrNotFound) {
		return domain.User{}, ErrInvalidSession
	}
	return user, err
}

func (s *Service) Logout(ctx context.Context, token string) error {
	hash, err := tokenHash(token)
	if err != nil {
		return nil
	}
	return s.repository.RevokeSession(ctx, hash)
}

func (s *Service) newSession(ctx context.Context, userID, userAgent, ipAddress string) (Session, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return Session{}, fmt.Errorf("generate session token: %w", err)
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	expiresAt := time.Now().Add(s.sessionTTL)
	hash := sha256.Sum256([]byte(token))
	if err := s.repository.CreateSession(ctx, userID, hash[:], expiresAt, userAgent, ipAddress); err != nil {
		return Session{}, err
	}
	return Session{Token: token, ExpiresAt: expiresAt}, nil
}

func tokenHash(token string) ([]byte, error) {
	decoded, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil || len(decoded) != 32 {
		return nil, ErrInvalidSession
	}
	hash := sha256.Sum256([]byte(token))
	return hash[:], nil
}
