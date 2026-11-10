package telegrambot

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
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

// APIError is a refusal from the Telegram Bot API. Code 403 means the bot may
// not message this chat (the user never pressed Start or blocked the bot).
type APIError struct {
	Method      string
	Code        int
	Description string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("Telegram %s failed (%d): %s", e.Method, e.Code, e.Description)
}

// Linker connects Telegram accounts to existing mock-ai accounts
// (see internal/telegramlink).
type Linker interface {
	Redeem(ctx context.Context, token string, from telegram.User, rawProfile []byte) (string, error)
	ChatStarted(ctx context.Context, userID string) error
}

type Bot struct {
	// Links, when set, lets /start l_<token> connect an existing account and
	// records which students have a chat with the bot.
	Links       Linker
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
	// "Connect Telegram" from the app: attach this Telegram account to the
	// student's existing account instead of creating a new one.
	if token, isLink := strings.CutPrefix(startPayload(item.Message.Text), linkPayloadPrefix); isLink && b.Links != nil {
		return b.connect(ctx, item, token, rawProfile)
	}
	user, created, err := b.users.EnsureTelegramUser(ctx, item.Message.From, rawProfile)
	if err != nil {
		return fmt.Errorf("ensure Telegram user: %w", err)
	}
	if b.Links != nil {
		if err := b.Links.ChatStarted(ctx, user.ID); err != nil {
			b.logger.Warn("record Telegram chat", "error", err)
		}
	}

	// Per-chat configuration makes the native button appear immediately even
	// for clients that have cached the previous default menu.
	if err := b.call(ctx, "setChatMenuButton", map[string]any{
		"chat_id":     item.Message.Chat.ID,
		"menu_button": b.menuButton(),
	}, nil); err != nil {
		b.logger.Warn("set Telegram chat menu", "error", err)
	}

	name := html.EscapeString(strings.TrimSpace(item.Message.From.FirstName))
	if name == "" {
		name = html.EscapeString(user.DisplayName)
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
	body := fmt.Sprintf("<h2>Welcome back to mock-ai, %s 👋</h2><p>Continue your IELTS practice when you're ready.</p>", name)
	effect := ""
	if created {
		effect = celebrateEffect
		body = fmt.Sprintf(`<h2>Welcome to mock-ai, %s 👋</h2><p>Your student profile is ready.</p>
<ul><li>📖 Reading and 🎧 Listening mock exams</li><li>✍️ Writing and 🎤 Speaking with AI feedback</li><li>📈 Your band progress in one place</li></ul>
<footer>Your results will arrive in this chat as soon as they are ready.</footer>`, name)
	}
	if centre.Name != "" {
		body = fmt.Sprintf("<h2>Welcome to %s on mock-ai, %s 👋</h2><p>You will practise with the material your centre has prepared for you.</p>", html.EscapeString(centre.Name), name)
	}
	return b.sendRich(ctx, item.Message.Chat.ID, body, b.keyboard(loginURL), effect)
}

// keyboard offers the mini app first, since staying inside Telegram is the
// shortest path, and the browser login underneath for students who want the
// full-size app. The login row is omitted rather than shown broken when no
// token could be minted.
func (b *Bot) keyboard(loginURL string) [][]any {
	rows := [][]any{{map[string]any{
		"text":    "Open mock-ai",
		"style":   "primary",
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

// celebrateEffect is Telegram's 🎉 message effect, played once on arrival in
// private chats: a first welcome, a connected account, a reached target.
const celebrateEffect = "5046509860389126442"

// sendRich sends body as a rich message (Bot API 10.1 HTML: headings, lists,
// tables) with an inline keyboard and an optional message effect.
func (b *Bot) sendRich(ctx context.Context, chatID int64, body string, keyboard [][]any, effect string) error {
	payload := map[string]any{
		"chat_id":      chatID,
		"rich_message": map[string]any{"html": body, "skip_entity_detection": true},
		"reply_markup": map[string]any{"inline_keyboard": keyboard},
	}
	if effect != "" {
		payload["message_effect_id"] = effect
	}
	return b.call(ctx, "sendRichMessage", payload, nil)
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
		return &APIError{Method: method, Code: envelope.ErrorCode, Description: envelope.Description}
	}
	if result != nil && len(envelope.Result) > 0 {
		if err := json.Unmarshal(envelope.Result, result); err != nil {
			return fmt.Errorf("decode %s result: %w", method, err)
		}
	}
	return nil
}

// linkPayloadPrefix starts a "Connect Telegram" token: /start l_<token>.
const linkPayloadPrefix = "l_"

func (b *Bot) connect(ctx context.Context, item update, token string, rawProfile []byte) error {
	chatID := item.Message.Chat.ID
	name, err := b.Links.Redeem(ctx, token, item.Message.From, rawProfile)
	body, effect := "", ""
	switch {
	case err == nil:
		effect = celebrateEffect
		body = fmt.Sprintf(`<h2>✅ Connected, %s</h2><p>Your mock exam results will arrive here as soon as they are ready:</p>
<ul><li>📖 Reading and 🎧 Listening right after you submit</li><li>✍️ Writing and 🎤 Speaking when the AI report is done</li></ul>
<footer>You can switch this off in your profile in the app.</footer>`, html.EscapeString(name))
	case strings.Contains(err.Error(), "already connected"):
		body = "<p>This Telegram account is already connected to another mock-ai account, so it cannot be connected to this one.</p>"
	case strings.Contains(err.Error(), "expired"):
		body = "<p>This link has expired or was already used. Open your profile in the app and press <b>Connect Telegram</b> again.</p>"
	default:
		b.logger.Warn("connect Telegram account", "error", err)
		body = "<p>Something went wrong connecting your account. Please try again from the app in a moment.</p>"
	}
	if err := b.call(ctx, "setChatMenuButton", map[string]any{"chat_id": chatID, "menu_button": b.menuButton()}, nil); err != nil {
		b.logger.Warn("set Telegram chat menu", "error", err)
	}
	return b.sendRich(ctx, chatID, body, b.keyboard(""), effect)
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
