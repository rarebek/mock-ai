package telegrambot

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/own/mock-ai/apps/api/internal/identity/domain"
	"github.com/own/mock-ai/apps/api/internal/identity/telegram"
)

type userStoreStub struct {
	created    bool
	calls      int
	links      int
	linkError  error
	joinedCode string
	centreName string
	joinError  error
}

func (s *userStoreStub) JoinCentre(_ context.Context, userID, joinCode string) (domain.Centre, bool, error) {
	s.joinedCode = joinCode
	if s.joinError != nil {
		return domain.Centre{}, false, s.joinError
	}
	return domain.Centre{ID: "centre-1", Name: s.centreName, Slug: "centre"}, true, nil
}

func (s *userStoreStub) EnsureTelegramUser(_ context.Context, user telegram.User, raw []byte) (domain.User, bool, error) {
	s.calls++
	if user.ID != 42 || !json.Valid(raw) {
		return domain.User{}, false, io.ErrUnexpectedEOF
	}
	return domain.User{ID: "user-1", DisplayName: user.DisplayName()}, s.created, nil
}

func (s *userStoreStub) IssueLoginLink(_ context.Context, userID string) (string, time.Time, error) {
	s.links++
	if s.linkError != nil {
		return "", time.Time{}, s.linkError
	}
	if userID != "user-1" {
		return "", time.Time{}, io.ErrUnexpectedEOF
	}
	return "one-time-token", time.Now().Add(time.Minute), nil
}

func TestHandleStartCreatesProfileAndSendsMiniAppButtons(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name      string
		created   bool
		wantWords string
	}{
		{name: "first visit", created: true, wantWords: "Your student profile is ready"},
		{name: "returning", created: false, wantWords: "Welcome back to mock-ai"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			var requests []map[string]any
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Fatalf("decode request: %v", err)
				}
				body["method"] = strings.TrimPrefix(r.URL.Path, "/bottest-token/")
				requests = append(requests, body)
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, `{"ok":true,"result":true}`)
			}))
			defer server.Close()

			store := &userStoreStub{created: test.created}
			bot, err := New("test-token", "https://mock.web.chessinsight.uz", "https://mock.web.chessinsight.uz", store, slog.New(slog.NewTextHandler(io.Discard, nil)))
			if err != nil {
				t.Fatalf("new bot: %v", err)
			}
			bot.apiBase = server.URL
			bot.client = server.Client()
			err = bot.handle(context.Background(), update{ID: 7, Message: &message{
				Text: "/start campaign",
				Chat: chat{ID: 99, Type: "private"},
				From: telegram.User{ID: 42, FirstName: "Donik", LastName: "Student"},
			}})
			if err != nil {
				t.Fatalf("handle: %v", err)
			}
			if store.calls != 1 {
				t.Fatalf("profile calls = %d, want 1", store.calls)
			}
			if len(requests) != 2 {
				t.Fatalf("Telegram calls = %d, want 2", len(requests))
			}
			if requests[0]["method"] != "setChatMenuButton" {
				t.Fatalf("first method = %v", requests[0]["method"])
			}
			menu := requests[0]["menu_button"].(map[string]any)
			if menu["text"] != "Open" || menu["type"] != "web_app" {
				t.Fatalf("unexpected menu button: %#v", menu)
			}
			if requests[1]["method"] != "sendMessage" {
				t.Fatalf("second method = %v", requests[1]["method"])
			}
			text, _ := requests[1]["text"].(string)
			if !strings.Contains(text, "Donik") || !strings.Contains(text, test.wantWords) {
				t.Fatalf("unexpected greeting: %q", text)
			}
			markup := requests[1]["reply_markup"].(map[string]any)
			keyboard := markup["inline_keyboard"].([]any)
			row := keyboard[0].([]any)
			button := row[0].(map[string]any)
			if button["text"] != "Open mock-ai" {
				t.Fatalf("unexpected inline button: %#v", button)
			}
			webApp := button["web_app"].(map[string]any)
			if webApp["url"] != "https://mock.web.chessinsight.uz" {
				t.Fatalf("unexpected Mini App URL: %v", webApp["url"])
			}
			// Starting the bot is the login: the second row carries a one-time
			// link, and the raw token must travel in it exactly once.
			if store.links != 1 {
				t.Fatalf("login links issued = %d, want 1", store.links)
			}
			login := keyboard[1].([]any)[0].(map[string]any)
			if login["url"] != "https://mock.web.chessinsight.uz/api/v1/auth/telegram/link?token=one-time-token" {
				t.Fatalf("unexpected login URL: %v", login["url"])
			}
		})
	}
}

func TestHandleIgnoresNonStartAndNonPrivateMessages(t *testing.T) {
	t.Parallel()
	store := &userStoreStub{}
	bot, err := New("test-token", "https://mock.web.chessinsight.uz", "https://mock.web.chessinsight.uz", store, slog.Default())
	if err != nil {
		t.Fatalf("new bot: %v", err)
	}
	for _, item := range []update{
		{Message: &message{Text: "hello", Chat: chat{Type: "private"}}},
		{Message: &message{Text: "/start", Chat: chat{Type: "group"}}},
		{},
	} {
		if err := bot.handle(context.Background(), item); err != nil {
			t.Fatalf("handle ignored update: %v", err)
		}
	}
	if store.calls != 0 {
		t.Fatalf("profile calls = %d, want 0", store.calls)
	}
}

func TestStartCommandRecognition(t *testing.T) {
	t.Parallel()
	for input, want := range map[string]bool{
		"/start": true, "/start referral": true, "/START@mockai_bot": true,
		"start": false, "/starter": false, "": false,
	} {
		if got := isStartCommand(input); got != want {
			t.Errorf("isStartCommand(%q) = %v, want %v", input, got, want)
		}
	}
}

// A student who starts the bot must still get the welcome and the mini app
// button when no login link could be minted -- a rate limit or a database
// hiccup must not cost them the way in they already had.
func TestHandleStartSurvivesLoginLinkFailure(t *testing.T) {
	t.Parallel()
	store := &userStoreStub{linkError: io.ErrUnexpectedEOF}
	var requests []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		body["method"] = strings.TrimPrefix(r.URL.Path, "/bottest-token/")
		requests = append(requests, body)
		_, _ = w.Write([]byte(`{"ok":true,"result":true}`))
	}))
	defer server.Close()
	bot, err := New("test-token", "https://mock.web.chessinsight.uz", "https://mock.web.chessinsight.uz", store, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("new bot: %v", err)
	}
	bot.apiBase = server.URL
	if err := bot.handle(context.Background(), update{Message: &message{
		Text: "/start", Chat: chat{ID: 7, Type: "private"},
		From: telegram.User{ID: 42, FirstName: "Donik"},
	}}); err != nil {
		t.Fatalf("handle: %v", err)
	}
	send := requests[len(requests)-1]
	keyboard := send["reply_markup"].(map[string]any)["inline_keyboard"].([]any)
	if len(keyboard) != 1 {
		t.Fatalf("keyboard rows = %d, want only the mini app row", len(keyboard))
	}
	if text, _ := send["text"].(string); !strings.Contains(text, "Donik") {
		t.Fatalf("greeting lost: %q", text)
	}
}

// A centre's invite is t.me/<bot>?start=c_<join code>. Starting the bot
// through one attaches the student and greets them with their centre's name;
// an invite that no longer works still leaves them a usable global account.
func TestHandleStartJoinsALearningCentreFromTheInvite(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name      string
		payload   string
		joinError error
		centre    string
		wantCode  string
		wantWords string
	}{
		{name: "centre invite", payload: " c_JOIN1234567890AB", wantCode: "JOIN1234567890AB", centre: "Oxford Study Centre", wantWords: "Welcome to Oxford Study Centre on mock-ai"},
		{name: "no invite", payload: "", wantCode: "", wantWords: "Your student profile is ready"},
		{name: "retired invite", payload: " c_GONE123456789012", joinError: io.ErrUnexpectedEOF, wantCode: "GONE123456789012", wantWords: "Your student profile is ready"},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := &userStoreStub{created: true, centreName: test.centre, joinError: test.joinError}
			var requests []map[string]any
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body map[string]any
				_ = json.NewDecoder(r.Body).Decode(&body)
				requests = append(requests, body)
				_, _ = w.Write([]byte(`{"ok":true,"result":true}`))
			}))
			defer server.Close()
			bot, err := New("test-token", "https://mock.web.chessinsight.uz", "https://mock.web.chessinsight.uz", store, slog.New(slog.NewTextHandler(io.Discard, nil)))
			if err != nil {
				t.Fatalf("new bot: %v", err)
			}
			bot.apiBase = server.URL
			if err := bot.handle(context.Background(), update{Message: &message{
				Text: "/start" + test.payload, Chat: chat{ID: 7, Type: "private"},
				From: telegram.User{ID: 42, FirstName: "Donik"},
			}}); err != nil {
				t.Fatalf("handle: %v", err)
			}
			if store.joinedCode != test.wantCode {
				t.Fatalf("join code = %q, want %q", store.joinedCode, test.wantCode)
			}
			text, _ := requests[len(requests)-1]["text"].(string)
			if !strings.Contains(text, test.wantWords) {
				t.Fatalf("greeting %q does not contain %q", text, test.wantWords)
			}
		})
	}
}
