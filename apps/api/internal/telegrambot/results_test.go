package telegrambot

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/own/mock-ai/apps/api/internal/identity/telegram"
)

func TestFormatExamResult(t *testing.T) {
	got := formatExamResult(examResult{Title: "Cambridge 18 <Test 2>", Skill: "listening", Band: 7, Correct: 31, Total: 40})
	for _, want := range []string{"🎧 <b>Listening result</b>", "Cambridge 18 &lt;Test 2&gt;", "Band <b>7.0</b>", "31 of 40 correct (78%)"} {
		if !strings.Contains(got, want) {
			t.Fatalf("%q missing %q", got, want)
		}
	}
}

func TestFormatReportSummarisesBandsAndNextStep(t *testing.T) {
	report := `{"overall":6.5,"criteria":[{"key":"task_response","score":6},{"key":"coherence_cohesion","score":7},{"key":"lexical_resource","score":6.5},{"key":"grammar","score":null}],"next_steps":["Give each paragraph one clear idea & example."]}`
	got, err := formatReport("writing", "Task 2", []byte(report))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"✍️ <b>Writing report ready</b>", "Task 2", "Overall band <b>6.5</b>", "• Task response: 6.0", "• Coherence and cohesion: 7.0", "• Lexical resource: 6.5", "idea &amp; example", "not an official IELTS score"} {
		if !strings.Contains(got, want) {
			t.Fatalf("%q missing %q", got, want)
		}
	}
	if strings.Contains(got, "Grammar") {
		t.Fatal("unscored criterion must be left out")
	}
	if _, err := formatReport("speaking", "", []byte(`{"criteria":[]}`)); err == nil {
		t.Fatal("a report without an overall band must not be sent")
	}
}

type linkerStub struct {
	err      error
	redeemed string
	started  []string
}

func (l *linkerStub) Redeem(_ context.Context, token string, _ telegram.User, _ []byte) (string, error) {
	l.redeemed = token
	return "Aziza", l.err
}
func (l *linkerStub) ChatStarted(_ context.Context, userID string) error {
	l.started = append(l.started, userID)
	return nil
}

func TestStartWithALinkTokenConnectsTheAccount(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name string
		err  error
		want string
	}{
		{"connected", nil, "Connected, Aziza"},
		{"taken", errors.New("this Telegram account is already connected to another mock-ai account"), "already connected to another"},
		{"expired", errors.New("this link has expired or was already used"), "has expired"},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := &userStoreStub{created: true}
			links := &linkerStub{err: test.err}
			var texts []string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body map[string]any
				_ = json.NewDecoder(r.Body).Decode(&body)
				if text, ok := body["text"].(string); ok {
					texts = append(texts, text)
				}
				_, _ = w.Write([]byte(`{"ok":true,"result":true}`))
			}))
			defer server.Close()
			bot, err := New("test-token", "https://mock.example", "https://mock.example", store, slog.New(slog.NewTextHandler(io.Discard, nil)))
			if err != nil {
				t.Fatal(err)
			}
			bot.apiBase = server.URL
			bot.Links = links
			if err := bot.handle(context.Background(), update{Message: &message{
				Text: "/start l_TOKEN123", Chat: chat{ID: 42, Type: "private"}, From: telegram.User{ID: 42, FirstName: "Aziza"},
			}}); err != nil {
				t.Fatal(err)
			}
			if links.redeemed != "TOKEN123" {
				t.Fatalf("redeemed %q", links.redeemed)
			}
			if len(texts) == 0 || !strings.Contains(texts[len(texts)-1], test.want) {
				t.Fatalf("replies %q, want %q", texts, test.want)
			}
		})
	}
}

func TestPlainStartOpensResultDelivery(t *testing.T) {
	store := &userStoreStub{created: true}
	links := &linkerStub{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"ok":true,"result":true}`))
	}))
	defer server.Close()
	bot, _ := New("test-token", "https://mock.example", "https://mock.example", store, slog.New(slog.NewTextHandler(io.Discard, nil)))
	bot.apiBase = server.URL
	bot.Links = links
	if err := bot.handle(context.Background(), update{Message: &message{
		Text: "/start", Chat: chat{ID: 42, Type: "private"}, From: telegram.User{ID: 42, FirstName: "Aziza"},
	}}); err != nil {
		t.Fatal(err)
	}
	if len(links.started) != 1 {
		t.Fatalf("ChatStarted calls = %v", links.started)
	}
}
