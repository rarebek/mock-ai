package telegrambot

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/own/mock-ai/apps/api/internal/identity/domain"
	"github.com/own/mock-ai/apps/api/internal/identity/telegram"
)

const telegramAPI = "https://api.telegram.org"

type UserStore interface {
	EnsureTelegramUser(ctx context.Context, user telegram.User, rawProfile []byte) (domain.User, bool, error)
	IssueLoginLink(ctx context.Context, userID string) (string, time.Time, error)
	JoinCentre(ctx context.Context, userID, joinCode string) (domain.Centre, bool, error)
}

type Bot struct {
	token       string
	miniAppURL  string
	loginOrigin string
	users       UserStore
	logger      *slog.Logger
	client      *http.Client
	apiBase     string
}

type update struct {
	ID      int64    `json:"update_id"`
	Message *message `json:"message"`
}

type message struct {
	Text string        `json:"text"`
	Chat chat          `json:"chat"`
	From telegram.User `json:"from"`
}

type chat struct {
	ID   int64  `json:"id"`
	Type string `json:"type"`
}

type apiResponse struct {
	OK          bool            `json:"ok"`
	Result      json.RawMessage `json:"result"`
	ErrorCode   int             `json:"error_code"`
	Description string          `json:"description"`
}

func New(token, miniAppURL, loginOrigin string, users UserStore, logger *slog.Logger) (*Bot, error) {
	if token == "" {
		return nil, nil
	}
	parsed, err := url.Parse(miniAppURL)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "https" && parsed.Scheme != "http") {
		return nil, errors.New("TELEGRAM_MINI_APP_URL must be an absolute HTTP(S) URL")
	}
	if users == nil {
		return nil, errors.New("Telegram bot requires a user store")
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Bot{
		token:       token,
		miniAppURL:  strings.TrimRight(miniAppURL, "/"),
		loginOrigin: strings.TrimRight(loginOrigin, "/"),
		users:       users,
		logger:      logger,
		client:      &http.Client{Timeout: 60 * time.Second},
		apiBase:     telegramAPI,
	}, nil
}

// Run configures the bot's permanent menu and consumes private messages until
// the application context is cancelled.
func (b *Bot) Run(ctx context.Context) {
	if b == nil {
		return
	}
	for method, payload := range map[string]any{
		"setMyCommands": map[string]any{"commands": []map[string]string{{"command": "start", "description": "Open mock-ai and get a login link"}}},
		"setMyDescription": map[string]string{
			"description": "IELTS mock exams and practice across Reading, Listening, Writing and Speaking, with AI feedback and progress tracking.",
		},
		"setChatMenuButton": map[string]any{"menu_button": b.menuButton()},
	} {
		if err := b.call(ctx, method, payload, nil); err != nil && ctx.Err() == nil {
			b.logger.Warn("configure Telegram bot", "method", method, "error", err)
		}
	}

	b.logger.Info("Telegram bot started")
	var offset int64
	for ctx.Err() == nil {
		var updates []update
		err := b.call(ctx, "getUpdates", map[string]any{
			"offset":          offset,
			"timeout":         50,
			"allowed_updates": []string{"message"},
		}, &updates)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			b.logger.Error("poll Telegram updates", "error", err)
			if !wait(ctx, 3*time.Second) {
				return
			}
			continue
		}
		for _, item := range updates {
			offset = item.ID + 1
			if err := b.handle(ctx, item); err != nil && ctx.Err() == nil {
				b.logger.Error("handle Telegram update", "update_id", item.ID, "error", err)
			}
		}
	}
}

func (b *Bot) handle(ctx context.Context, item update) error {
	if item.Message == nil || item.Message.Chat.Type != "private" || !isStartCommand(item.Message.Text) {
		return nil
	}
	rawProfile, err := json.Marshal(item.Message.From)
	if err != nil {
		return fmt.Errorf("encode Telegram profile: %w", err)
	}
	user, created, err := b.users.EnsureTelegramUser(ctx, item.Message.From, rawProfile)
	if err != nil {
		return fmt.Errorf("ensure Telegram user: %w", err)
	}

	// Per-chat configuration makes the native button appear immediately even
	// for clients that have cached the previous default menu.
	if err := b.call(ctx, "setChatMenuButton", map[string]any{
		"chat_id":     item.Message.Chat.ID,
		"menu_button": b.menuButton(),
	}, nil); err != nil {
		b.logger.Warn("set Telegram chat menu", "error", err)
	}

	name := strings.TrimSpace(item.Message.From.FirstName)
	if name == "" {
		name = user.DisplayName
	}
	// A centre invite attaches the student before anything else, so the first
	// thing they see is their own centre's name rather than a generic welcome.
	centre := domain.Centre{}
	if code, isInvite := strings.CutPrefix(startPayload(item.Message.Text), centrePayloadPrefix); isInvite && code != "" {
		joined, _, err := b.users.JoinCentre(ctx, user.ID, code)
		if err != nil {
			// A bad or retired invite still leaves a working global account, so
			// this is worth recording but not worth failing the student over.
			b.logger.Warn("join learning centre", "error", err)
		} else {
			centre = joined
		}
	}
	// Receiving a private message from this account is the proof of identity,
	// so the link below is the whole login. Failing to mint one must not cost
	// the student the welcome message or the mini app button.
	loginURL := ""
	if b.loginOrigin != "" {
		token, _, err := b.users.IssueLoginLink(ctx, user.ID)
		if err != nil {
			b.logger.Warn("issue Telegram login link", "error", err)
		} else {
			loginURL = b.loginOrigin + "/api/v1/auth/telegram/link?token=" + url.QueryEscape(token)
		}
	}
	text := fmt.Sprintf("Hello, %s! 👋\n\nWelcome back to mock-ai. Continue your IELTS practice when you're ready.", name)
	if created {
		text = fmt.Sprintf("Welcome to mock-ai, %s! 👋\n\nYour student profile is ready. Practice IELTS Reading, Listening, Writing and Speaking, take mock exams, and track your progress.", name)
	}
	if centre.Name != "" {
		text = fmt.Sprintf("Welcome to %s on mock-ai, %s! 👋\n\nYou will practice with the material your centre has prepared for you.", centre.Name, name)
	}
	return b.call(ctx, "sendMessage", map[string]any{
		"chat_id":      item.Message.Chat.ID,
		"text":         text,
		"reply_markup": map[string]any{"inline_keyboard": b.keyboard(loginURL)},
	}, nil)
}

// keyboard offers the mini app first, since staying inside Telegram is the
// shortest path, and the browser login underneath for students who want the
// full-size app. The login row is omitted rather than shown broken when no
// token could be minted.
func (b *Bot) keyboard(loginURL string) [][]any {
	rows := [][]any{{map[string]any{
		"text":    "Open mock-ai",
		"web_app": map[string]string{"url": b.miniAppURL},
	}}}
	if loginURL != "" {
		rows = append(rows, []any{map[string]any{
			"text": "Log in in your browser",
			"url":  loginURL,
		}})
	}
	return rows
}

func (b *Bot) menuButton() map[string]any {
	return map[string]any{
		"type":    "web_app",
		"text":    "Open",
		"web_app": map[string]string{"url": b.miniAppURL},
	}
}

func (b *Bot) call(ctx context.Context, method string, payload, result any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("encode %s request: %w", method, err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, b.apiBase+"/bot"+b.token+"/"+method, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create %s request: %w", method, err)
	}
	req.Header.Set("Content-Type", "application/json")
	response, err := b.client.Do(req)
	if err != nil {
		// net/http errors include the request URL, which contains the bot token.
		// Preserve the useful failure text while ensuring credentials never reach logs.
		return fmt.Errorf("call %s: %s", method, strings.ReplaceAll(err.Error(), b.token, "[redacted]"))
	}
	defer response.Body.Close()
	limited := io.LimitReader(response.Body, 1<<20)
	var envelope apiResponse
	if err := json.NewDecoder(limited).Decode(&envelope); err != nil {
		return fmt.Errorf("decode %s response: %w", method, err)
	}
	if !envelope.OK {
		return fmt.Errorf("Telegram %s failed (%d): %s", method, envelope.ErrorCode, envelope.Description)
	}
	if result != nil && len(envelope.Result) > 0 {
		if err := json.Unmarshal(envelope.Result, result); err != nil {
			return fmt.Errorf("decode %s result: %w", method, err)
		}
	}
	return nil
}

func isStartCommand(text string) bool {
	fields := strings.Fields(strings.TrimSpace(text))
	if len(fields) == 0 {
		return false
	}
	command := strings.ToLower(strings.SplitN(fields[0], "@", 2)[0])
	return command == "/start"
}

// startPayload reads the deep-link argument Telegram appends to /start. A
// learning centre's invite is t.me/<bot>?start=c_<join code>, which arrives
// here as "/start c_<join code>".
func startPayload(text string) string {
	fields := strings.Fields(strings.TrimSpace(text))
	if len(fields) < 2 {
		return ""
	}
	return fields[1]
}

const centrePayloadPrefix = "c_"

func wait(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
