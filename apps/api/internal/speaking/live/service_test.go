package live

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"github.com/coder/websocket"
	d "github.com/own/mock-ai/apps/api/internal/speaking/domain"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type saved struct {
	pos        int32
	wav        []byte
	text, next string
}
type testRepo struct {
	Repository
	saves    chan saved
	skips    chan saved
	released chan bool
}

func (r *testRepo) SaveLive(_ context.Context, _ d.Session, p int32, b []byte, text string, done bool, next string) error {
	r.saves <- saved{p, b, text, next}
	return nil
}
func (r *testRepo) SkipLive(_ context.Context, _ d.Session, p int32, next string) error {
	if r.skips != nil {
		r.skips <- saved{pos: p, next: next}
	}
	return nil
}
func (r *testRepo) ReleaseLive(context.Context, d.Session) error { r.released <- true; return nil }

type testProvider struct{ url string }

func TestPermanentProviderClose(t *testing.T) {
	for _, code := range []websocket.StatusCode{1003, 1007, 1008} {
		if !permanentClose(code) {
			t.Fatalf("must not retry %d", code)
		}
	}
	for _, code := range []websocket.StatusCode{-1, 1000, 1001, 1006, 1011, 1012, 1013} {
		if permanentClose(code) {
			t.Fatalf("transient close %d must remain recoverable", code)
		}
	}
}

func (p testProvider) ConnectLive(ctx context.Context) (*websocket.Conn, error) {
	c, _, e := websocket.Dial(ctx, p.url, nil)
	return c, e
}
func TestLiveProgressionWaitAndOutputGate(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	upstream := make(chan *websocket.Conn, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, e := websocket.Accept(w, r, nil)
		if e != nil {
			return
		}
		upstream <- c
		<-ctx.Done()
		c.CloseNow()
	}))
	defer func() { cancel(); server.Close() }()
	topic, turns, _ := d.NewPlan()
	session := d.Session{ID: "session", UserID: "user", LiveModel: "test", Turns: turns, Topic: topic}
	repo := &testRepo{saves: make(chan saved, 2), released: make(chan bool, 1)}
	service := Service{Repo: repo, Provider: testProvider{strings.Replace(server.URL, "http", "ws", 1)}}
	app := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, e := websocket.Accept(w, r, nil)
		if e != nil {
			return
		}
		defer c.CloseNow()
		_ = service.Run(ctx, c, session)
	}))
	defer func() { cancel(); app.Close() }()
	client, _, e := websocket.Dial(ctx, strings.Replace(app.URL, "http", "ws", 1), nil)
	if e != nil {
		t.Fatal(e)
	}
	defer client.CloseNow()
	provider := <-upstream
	receive := func(c *websocket.Conn) map[string]any {
		t.Helper()
		kind, b, e := c.Read(ctx)
		if e != nil {
			t.Fatal(e)
		}
		if kind != websocket.MessageText {
			t.Fatal("unexpected audio while examiner must be silent")
		}
		var v map[string]any
		if json.Unmarshal(b, &v) != nil {
			t.Fatal("bad JSON")
		}
		return v
	}
	send := func(c *websocket.Conn, v any) {
		t.Helper()
		if e := write(ctx, c, v); e != nil {
			t.Fatal(e)
		}
	}
	tool := func(name string) {
		send(provider, map[string]any{"toolCall": map[string]any{"functionCalls": []any{map[string]any{"id": "call", "name": name, "args": map[string]string{"question": "What is your typical weekday like?"}}}}})
	}
	toolResult := func(message map[string]any) map[string]any {
		t.Helper()
		return message["toolResponse"].(map[string]any)["functionResponses"].([]any)[0].(map[string]any)["response"].(map[string]any)
	}
	setupMessage, _ := json.Marshal(receive(provider))
	if !strings.Contains(string(setupMessage), "DELIVERY: sound attentive, warm and composed") {
		t.Fatal("conversational delivery instructions missing from live setup")
	}
	send(provider, map[string]any{"setupComplete": map[string]any{}})
	receive(client)
	receive(provider)
	send(provider, map[string]any{"serverContent": map[string]bool{"turnComplete": true}})
	// Empty provider/tool-only turns trigger a bounded retry, not listening.
	if receive(provider)["clientContent"] == nil {
		t.Fatal("missing empty-audio retry")
	}
	send(provider, map[string]any{"serverContent": map[string]any{"modelTurn": map[string]any{"parts": []any{map[string]any{"inlineData": map[string]string{"data": base64.StdEncoding.EncodeToString(make([]byte, 4800)), "mimeType": "audio/pcm;rate=24000"}}}}, "turnComplete": true}})
	if kind, _, err := client.Read(ctx); err != nil || kind != websocket.MessageBinary {
		t.Fatal("examiner audio missing", err)
	}
	receive(client)
	send(client, map[string]string{"type": "playback_done"})
	if receive(client)["phase"] != "listening" {
		t.Fatal("not listening")
	}
	// Noise/no transcript cannot advance, even if a provider proposes doing so.
	tool("advance_question")
	if result := toolResult(receive(provider)); result["instruction"] != "Stay silent and wait for the student to finish." {
		t.Fatal("noise must not receive a conversational acknowledgment", result)
	}
	if receive(client)["position"] != float64(0) {
		t.Fatal("noise advanced question")
	}
	pcm := make([]byte, 8000)
	for i := 0; i < len(pcm); i += 2 {
		binary.LittleEndian.PutUint16(pcm[i:], 2000)
	}
	for i := 0; i < 4; i++ {
		if e = client.Write(ctx, websocket.MessageBinary, pcm); e != nil {
			t.Fatal(e)
		}
		receive(provider)
	}
	// Spoken help is answered at the same position; it cannot become an answer.
	for _, name := range []string{"repeat_question", "clarify_question"} {
		send(provider, map[string]any{"serverContent": map[string]any{"inputTranscription": map[string]string{"text": " Could you please explain the question?"}}})
		send(provider, map[string]any{"toolCall": map[string]any{"functionCalls": []any{map[string]any{"id": "help", "name": name, "args": map[string]string{"explanation": "I am asking what you like about your work or studies."}}}}})
		if result := toolResult(receive(provider)); result["instruction"] != helpQuestionInstruction {
			t.Fatal("missing natural same-question help instruction", result)
		}
		state := receive(client)
		if state["position"] != float64(0) || state["phase"] != "examiner" {
			t.Fatal("help request advanced or stayed silent")
		}
		select {
		case <-repo.saves:
			t.Fatal("help request was saved as answer")
		default:
		}
		send(provider, map[string]any{"serverContent": map[string]any{"modelTurn": map[string]any{"parts": []any{map[string]any{"inlineData": map[string]string{"data": base64.StdEncoding.EncodeToString(make([]byte, 4800)), "mimeType": "audio/pcm;rate=24000"}}}}, "turnComplete": true}})
		if kind, _, err := client.Read(ctx); err != nil || kind != websocket.MessageBinary {
			t.Fatal("help audio missing", err)
		}
		receive(client)
		send(client, map[string]string{"type": "playback_done"})
		receive(client)
		tool("advance_question")
		receive(provider)
		if receive(client)["position"] != float64(0) {
			t.Fatal("old help transcript advanced interview")
		}
	}
	send(provider, map[string]any{"serverContent": map[string]any{"inputTranscription": map[string]string{"text": "I study computer science because"}}})
	tool("wait_for_student")
	if result := toolResult(receive(provider)); result["instruction"] != "Stay silent and wait for the student to finish." {
		t.Fatal("thinking pause must remain silent", result)
	}
	if receive(client)["phase"] != "listening" {
		t.Fatal("thinking pause interrupted")
	}
	// Unapproved output after a pause must not reach the browser.
	send(provider, map[string]any{"serverContent": map[string]any{"modelTurn": map[string]any{"parts": []any{map[string]any{"inlineData": map[string]string{"data": "AAAAAA==", "mimeType": "audio/pcm;rate=24000"}}}}, "turnComplete": true}})
	send(provider, map[string]any{"serverContent": map[string]any{"inputTranscription": map[string]string{"text": " I enjoy solving practical problems."}}})
	tool("advance_question")
	if result := toolResult(receive(provider)); result["instruction"] != nextQuestionInstruction || result["question"] != "What is your typical weekday like?" {
		t.Fatal("expected natural transition with separate unchanged question", result)
	}
	state := receive(client)
	if state["position"] != float64(1) || state["phase"] != "examiner" {
		t.Fatalf("did not advance: %v", state)
	}
	select {
	case v := <-repo.saves:
		if v.pos != 0 || len(v.wav) != 32044 || !strings.Contains(v.text, "practical problems") || v.next != "What is your typical weekday like?" {
			t.Fatal("answer not saved")
		}
	case <-ctx.Done():
		t.Fatal("no saved answer")
	}
	// A second call in the same provider turn cannot skip another question.
	tool("advance_question")
	receive(provider)
	if receive(client)["position"] != float64(1) {
		t.Fatal("double advancement")
	}
	client.CloseNow()
	select {
	case <-repo.released:
	case <-ctx.Done():
		t.Fatal("lease not released")
	}
	cancel()
}
func TestMeaningful(t *testing.T) {
	for _, s := range []string{"", "um uh hmm", "[background noise]"} {
		if Meaningful(s) {
			t.Fatalf("noise accepted: %s", s)
		}
	}
	if !Meaningful("I enjoy studying languages.") {
		t.Fatal("speech rejected")
	}
}

func TestPartTwoTimeLimitTransition(t *testing.T) {
	message := timeLimitTransition(2, 7, "Why do adults find it difficult to learn new skills?")
	for _, want := range []string{"two-minute Part 2 time limit", "time is up", "introduce Part 3", "Why do adults"} {
		if !strings.Contains(message, want) {
			t.Fatalf("missing %q in transition: %s", want, message)
		}
	}
}

func TestLiveResumesUpstreamAfterGoAway(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	connections := make(chan *websocket.Conn, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		connections <- c
		<-ctx.Done()
		c.CloseNow()
	}))
	defer func() { cancel(); server.Close() }()
	topic, turns, _ := d.NewPlan()
	session := d.Session{ID: "resumable", UserID: "user", LiveModel: "test", Turns: turns, Topic: topic}
	repo := &testRepo{saves: make(chan saved, 1), released: make(chan bool, 1)}
	service := Service{Repo: repo, Provider: testProvider{strings.Replace(server.URL, "http", "ws", 1)}}
	app := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer c.CloseNow()
		_ = service.Run(ctx, c, session)
	}))
	defer func() { cancel(); app.Close() }()
	client, _, err := websocket.Dial(ctx, strings.Replace(app.URL, "http", "ws", 1), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer client.CloseNow()

	first := <-connections
	_, initial, err := first.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var setup map[string]map[string]any
	if json.Unmarshal(initial, &setup) != nil || setup["setup"]["sessionResumption"] == nil {
		t.Fatal("initial setup did not request session resumption")
	}
	if err = write(ctx, first, map[string]any{"sessionResumptionUpdate": map[string]any{"newHandle": "resume-token", "resumable": true}}); err != nil {
		t.Fatal(err)
	}
	if err = write(ctx, first, map[string]any{"goAway": map[string]any{"timeLeft": "5s"}}); err != nil {
		t.Fatal(err)
	}

	second := <-connections
	_, resumed, err := second.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var resumedSetup struct {
		Setup struct {
			SessionResumption struct {
				Handle string `json:"handle"`
			} `json:"sessionResumption"`
		} `json:"setup"`
	}
	if json.Unmarshal(resumed, &resumedSetup) != nil || resumedSetup.Setup.SessionResumption.Handle != "resume-token" {
		t.Fatalf("resumption handle missing from reconnect setup: %s", resumed)
	}
	if err = write(ctx, second, map[string]any{"setupComplete": map[string]any{}}); err != nil {
		t.Fatal(err)
	}
	_, state, err := client.Read(ctx)
	if err != nil || !strings.Contains(string(state), `"phase":"connecting"`) {
		t.Fatalf("browser did not receive reconnect state: %s (%v)", state, err)
	}
	_, state, err = client.Read(ctx)
	if err != nil || !strings.Contains(string(state), `"phase":"examiner"`) {
		t.Fatalf("browser did not resume interview: %s (%v)", state, err)
	}
}

func TestLiveEndsTimedAnswerAndAdvancesWhenTranscriptIsEmpty(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	upstream := make(chan *websocket.Conn, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		upstream <- c
		<-ctx.Done()
		c.CloseNow()
	}))
	defer func() { cancel(); server.Close() }()
	turns := []d.Turn{
		{Position: 0, Part: 1, Prompt: "What do you enjoy studying?", Seconds: 1},
		{Position: 1, Part: 3, Prompt: "Why is lifelong learning important?", Seconds: 1},
	}
	session := d.Session{ID: "skip-empty", UserID: "user", LiveModel: "test", Turns: turns, Topic: "learning"}
	repo := &testRepo{saves: make(chan saved, 1), skips: make(chan saved, 1), released: make(chan bool, 1)}
	service := Service{Repo: repo, Provider: testProvider{strings.Replace(server.URL, "http", "ws", 1)}}
	app := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer c.CloseNow()
		_ = service.Run(ctx, c, session)
	}))
	defer func() { cancel(); app.Close() }()
	client, _, err := websocket.Dial(ctx, strings.Replace(app.URL, "http", "ws", 1), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer client.CloseNow()
	provider := <-upstream

	// Setup, opening question, and playback completion move the browser to listening.
	if _, _, err = provider.Read(ctx); err != nil {
		t.Fatal(err)
	}
	if err = write(ctx, provider, map[string]any{"setupComplete": map[string]any{}}); err != nil {
		t.Fatal(err)
	}
	if _, _, err = client.Read(ctx); err != nil {
		t.Fatal(err)
	}
	if _, _, err = provider.Read(ctx); err != nil {
		t.Fatal(err)
	}
	audio := base64.StdEncoding.EncodeToString(make([]byte, 4800))
	if err = write(ctx, provider, map[string]any{"serverContent": map[string]any{"modelTurn": map[string]any{"parts": []any{map[string]any{"inlineData": map[string]string{"data": audio, "mimeType": "audio/pcm;rate=24000"}}}}, "turnComplete": true}}); err != nil {
		t.Fatal(err)
	}
	if kind, _, readErr := client.Read(ctx); readErr != nil || kind != websocket.MessageBinary {
		t.Fatal("opening audio missing", readErr)
	}
	if _, _, err = client.Read(ctx); err != nil {
		t.Fatal(err)
	}
	if err = write(ctx, client, map[string]string{"type": "playback_done"}); err != nil {
		t.Fatal(err)
	}
	if _, state, readErr := client.Read(ctx); readErr != nil || !strings.Contains(string(state), `"phase":"listening"`) {
		t.Fatalf("interview did not start listening: %s (%v)", state, readErr)
	}
	frame := make([]byte, 3200)
	for i := 0; i < 11; i++ {
		if err = client.Write(ctx, websocket.MessageBinary, frame); err != nil {
			t.Fatal(err)
		}
	}
	if _, state, readErr := client.Read(ctx); readErr != nil || !strings.Contains(string(state), `"position":1`) || !strings.Contains(string(state), `"phase":"examiner"`) {
		t.Fatalf("timed answer did not advance: %s (%v)", state, readErr)
	}
	select {
	case skipped := <-repo.skips:
		if skipped.pos != 0 || skipped.next != turns[1].Prompt {
			t.Fatalf("wrong skipped turn: %+v", skipped)
		}
	case <-ctx.Done():
		t.Fatal("timed empty answer was not persisted as skipped")
	}
}

// A student who finishes must be moved on; a student who is still thinking must
// not be. The examiner decides either way — the server only guarantees it is
// asked, because without that the session hangs in "listening" forever.
func TestLiveSilenceNudgeWaitsForThinkersAndPromptsAfterAnswers(t *testing.T) {
	for _, tc := range []struct {
		name       string
		transcript string
		wantNudge  bool
	}{
		{"a finished answer is handed to the examiner to decide", "I live in Tashkent with my family", true},
		{"a student still gathering their thought is left alone", "well um", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			defer cancel()
			upstream := make(chan *websocket.Conn, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				c, e := websocket.Accept(w, r, nil)
				if e != nil {
					return
				}
				upstream <- c
				<-ctx.Done()
				c.CloseNow()
			}))
			defer func() { cancel(); server.Close() }()
			topic, turns, _ := d.NewPlan()
			session := d.Session{ID: "session", UserID: "user", LiveModel: "test", Turns: turns, Topic: topic}
			repo := &testRepo{saves: make(chan saved, 2), released: make(chan bool, 1)}
			service := Service{Repo: repo, Provider: testProvider{strings.Replace(server.URL, "http", "ws", 1)}, Settle: 150 * time.Millisecond}
			app := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				c, e := websocket.Accept(w, r, nil)
				if e != nil {
					return
				}
				defer c.CloseNow()
				_ = service.Run(ctx, c, session)
			}))
			defer func() { cancel(); app.Close() }()
			client, _, e := websocket.Dial(ctx, strings.Replace(app.URL, "http", "ws", 1), nil)
			if e != nil {
				t.Fatal(e)
			}
			defer client.CloseNow()
			provider := <-upstream
			read := func(c *websocket.Conn) map[string]any {
				t.Helper()
				_, b, err := c.Read(ctx)
				if err != nil {
					t.Fatal(err)
				}
				var v map[string]any
				_ = json.Unmarshal(b, &v)
				return v
			}
			send := func(c *websocket.Conn, v any) {
				t.Helper()
				if err := write(ctx, c, v); err != nil {
					t.Fatal(err)
				}
			}
			// Mirror the handshake the progression test establishes.
			read(provider) // setup
			send(provider, map[string]any{"setupComplete": map[string]any{}})
			read(client)
			read(provider)
			send(provider, map[string]any{"serverContent": map[string]bool{"turnComplete": true}})
			if read(provider)["clientContent"] == nil {
				t.Fatal("missing empty-audio retry")
			}
			send(provider, map[string]any{"serverContent": map[string]any{"modelTurn": map[string]any{"parts": []any{map[string]any{"inlineData": map[string]string{"data": base64.StdEncoding.EncodeToString(make([]byte, 4800)), "mimeType": "audio/pcm;rate=24000"}}}}, "turnComplete": true}})
			if kind, _, err := client.Read(ctx); err != nil || kind != websocket.MessageBinary {
				t.Fatal("examiner audio missing", err)
			}
			read(client)
			send(client, map[string]string{"type": "playback_done"})
			if read(client)["phase"] != "listening" {
				t.Fatal("not listening")
			}
			// The student speaks, then goes quiet.
			send(provider, map[string]any{"serverContent": map[string]any{"inputTranscription": map[string]string{"text": tc.transcript}}})

			// Read on a goroutine: cancelling a read context closes the websocket,
			// so polling with short timeouts would kill the connection under test.
			messages := make(chan string, 8)
			go func() {
				for {
					_, b, err := provider.Read(ctx)
					if err != nil {
						close(messages)
						return
					}
					messages <- string(b)
				}
			}()
			nudged := false
			deadline := time.After(2500 * time.Millisecond)
		wait:
			for {
				select {
				case m, ok := <-messages:
					if !ok {
						break wait
					}
					if strings.Contains(m, "Decide now") || strings.Contains(m, "You must call a tool now") {
						nudged = true
						break wait
					}
				case <-deadline:
					break wait
				}
			}
			if nudged != tc.wantNudge {
				t.Fatalf("nudge=%v, want %v (transcript %q)", nudged, tc.wantNudge, tc.transcript)
			}
		})
	}
}

// Production deadlock: the examiner answered wait_for_student to both nudges
// and then sat in silence while the student waited for it. After a long quiet
// spell following a substantive, audible answer the server resolves the turn
// itself rather than leaving the interview hanging.
func TestLiveAdvancesWhenExaminerIgnoresBothNudges(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	upstream := make(chan *websocket.Conn, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, e := websocket.Accept(w, r, nil)
		if e != nil {
			return
		}
		upstream <- c
		<-ctx.Done()
		c.CloseNow()
	}))
	defer func() { cancel(); server.Close() }()
	topic, turns, _ := d.NewPlan()
	session := d.Session{ID: "session", UserID: "user", LiveModel: "test", Turns: turns, Topic: topic}
	repo := &testRepo{saves: make(chan saved, 4), released: make(chan bool, 1)}
	service := Service{Repo: repo, Provider: testProvider{strings.Replace(server.URL, "http", "ws", 1)}, Settle: 120 * time.Millisecond}
	app := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, e := websocket.Accept(w, r, nil)
		if e != nil {
			return
		}
		defer c.CloseNow()
		_ = service.Run(ctx, c, session)
	}))
	defer func() { cancel(); app.Close() }()
	client, _, e := websocket.Dial(ctx, strings.Replace(app.URL, "http", "ws", 1), nil)
	if e != nil {
		t.Fatal(e)
	}
	defer client.CloseNow()
	provider := <-upstream
	read := func(c *websocket.Conn) map[string]any {
		t.Helper()
		_, b, err := c.Read(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var v map[string]any
		_ = json.Unmarshal(b, &v)
		return v
	}
	send := func(c *websocket.Conn, v any) {
		t.Helper()
		if err := write(ctx, c, v); err != nil {
			t.Fatal(err)
		}
	}
	read(provider)
	send(provider, map[string]any{"setupComplete": map[string]any{}})
	read(client)
	read(provider)
	send(provider, map[string]any{"serverContent": map[string]bool{"turnComplete": true}})
	if read(provider)["clientContent"] == nil {
		t.Fatal("missing empty-audio retry")
	}
	send(provider, map[string]any{"serverContent": map[string]any{"modelTurn": map[string]any{"parts": []any{map[string]any{"inlineData": map[string]string{"data": base64.StdEncoding.EncodeToString(make([]byte, 4800)), "mimeType": "audio/pcm;rate=24000"}}}}, "turnComplete": true}})
	if kind, _, err := client.Read(ctx); err != nil || kind != websocket.MessageBinary {
		t.Fatal("examiner audio missing", err)
	}
	read(client)
	send(client, map[string]string{"type": "playback_done"})
	if read(client)["phase"] != "listening" {
		t.Fatal("not listening")
	}
	// Audible speech, so the answer is real and not silence.
	tone := make([]byte, 64000)
	for i := 0; i+1 < len(tone); i += 2 {
		v := int16(9000)
		if (i/2)%40 < 20 {
			v = -9000
		}
		binary.LittleEndian.PutUint16(tone[i:], uint16(v))
	}
	// The browser streams the microphone in small frames and the server caps a
	// client read at 8KB, so send it the way the real client does.
	for off := 0; off < len(tone); off += 4096 {
		end := off + 4096
		if end > len(tone) {
			end = len(tone)
		}
		if err := client.Write(ctx, websocket.MessageBinary, tone[off:end]); err != nil {
			t.Fatal(err)
		}
	}
	send(provider, map[string]any{"serverContent": map[string]any{"inputTranscription": map[string]string{"text": "I live in Tashkent with my family"}}})

	// Drain the upstream; the examiner never answers a nudge.
	messages := make(chan string, 32)
	go func() {
		for {
			_, b, err := provider.Read(ctx)
			if err != nil {
				close(messages)
				return
			}
			messages <- string(b)
		}
	}()
	select {
	case got := <-repo.saves:
		if got.pos != 0 {
			t.Fatalf("saved position %d, want 0", got.pos)
		}
		if !strings.Contains(got.text, "Tashkent") {
			t.Fatalf("saved transcript %q lost the answer", got.text)
		}
	case <-time.After(8 * time.Second):
		t.Fatal("interview stayed deadlocked: the server never resolved the turn")
	}
}

// A near-empty transcript must not strand the student: Gemini returned seven
// characters for a 27-second answer in production and the interview hung at
// position 0 until the student closed the tab.
func TestLiveResolvesTurnWhenTranscriptionIsEmpty(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	upstream := make(chan *websocket.Conn, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, e := websocket.Accept(w, r, nil)
		if e != nil {
			return
		}
		upstream <- c
		<-ctx.Done()
		c.CloseNow()
	}))
	defer func() { cancel(); server.Close() }()
	topic, turns, _ := d.NewPlan()
	session := d.Session{ID: "session", UserID: "user", LiveModel: "test", Turns: turns, Topic: topic}
	repo := &testRepo{saves: make(chan saved, 4), released: make(chan bool, 1)}
	service := Service{Repo: repo, Provider: testProvider{strings.Replace(server.URL, "http", "ws", 1)}, Settle: 120 * time.Millisecond}
	app := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, e := websocket.Accept(w, r, nil)
		if e != nil {
			return
		}
		defer c.CloseNow()
		_ = service.Run(ctx, c, session)
	}))
	defer func() { cancel(); app.Close() }()
	client, _, e := websocket.Dial(ctx, strings.Replace(app.URL, "http", "ws", 1), nil)
	if e != nil {
		t.Fatal(e)
	}
	defer client.CloseNow()
	provider := <-upstream
	read := func(c *websocket.Conn) map[string]any {
		t.Helper()
		_, b, err := c.Read(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var v map[string]any
		_ = json.Unmarshal(b, &v)
		return v
	}
	send := func(c *websocket.Conn, v any) {
		t.Helper()
		if err := write(ctx, c, v); err != nil {
			t.Fatal(err)
		}
	}
	read(provider)
	send(provider, map[string]any{"setupComplete": map[string]any{}})
	read(client)
	read(provider)
	send(provider, map[string]any{"serverContent": map[string]bool{"turnComplete": true}})
	if read(provider)["clientContent"] == nil {
		t.Fatal("missing empty-audio retry")
	}
	send(provider, map[string]any{"serverContent": map[string]any{"modelTurn": map[string]any{"parts": []any{map[string]any{"inlineData": map[string]string{"data": base64.StdEncoding.EncodeToString(make([]byte, 4800)), "mimeType": "audio/pcm;rate=24000"}}}}, "turnComplete": true}})
	if kind, _, err := client.Read(ctx); err != nil || kind != websocket.MessageBinary {
		t.Fatal("examiner audio missing", err)
	}
	read(client)
	send(client, map[string]string{"type": "playback_done"})
	if read(client)["phase"] != "listening" {
		t.Fatal("not listening")
	}
	// Audible speech. The transcript is far too short to count as a finished
	// answer, so only the measured audio can tell the server they spoke.
	tone := make([]byte, 96000)
	for i := 0; i+1 < len(tone); i += 2 {
		v := int16(9000)
		if (i/2)%40 < 20 {
			v = -9000
		}
		binary.LittleEndian.PutUint16(tone[i:], uint16(v))
	}
	// The browser streams the microphone in small frames and the server caps a
	// client read at 8KB, so send it the way the real client does.
	for off := 0; off < len(tone); off += 4096 {
		end := off + 4096
		if end > len(tone) {
			end = len(tone)
		}
		if err := client.Write(ctx, websocket.MessageBinary, tone[off:end]); err != nil {
			t.Fatal(err)
		}
		// The server caps ingress at a little over real time, so a burst looks
		// like a client flooding it. Pace the frames as a microphone would.
		time.Sleep(40 * time.Millisecond)
	}
	send(provider, map[string]any{"serverContent": map[string]any{"inputTranscription": map[string]string{"text": "Hello."}}})

	// Production: the examiner answered wait_for_student and then said nothing
	// while Gemini had transcribed seven characters of a 27-second turn. The
	// server must resolve the turn on the speech it measured.
	messages := make(chan string, 32)
	go func() {
		for {
			_, b, err := provider.Read(ctx)
			if err != nil {
				close(messages)
				return
			}
			messages <- string(b)
		}
	}()
	select {
	case got := <-repo.saves:
		if got.pos != 0 {
			t.Fatalf("saved position %d, want 0", got.pos)
		}
		if got.wav == nil {
			t.Fatal("saved no recording")
		}
	case <-time.After(8 * time.Second):
		t.Fatal("interview stayed deadlocked: the server never resolved the turn")
	}
}

// A refusal that asks the examiner to speak must actually let it be heard.
// Entering "listening" disables model output, and the refusal path never
// re-enabled it, so the examiner's reply was discarded byte for byte and the
// student sat in silence with nothing able to move the interview on.
func TestLiveRefusedAdvanceIsStillSpokenToTheStudent(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	upstream := make(chan *websocket.Conn, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, e := websocket.Accept(w, r, nil)
		if e != nil {
			return
		}
		upstream <- c
		<-ctx.Done()
		c.CloseNow()
	}))
	defer func() { cancel(); server.Close() }()
	topic, turns, _ := d.NewPlan()
	session := d.Session{ID: "session", UserID: "user", LiveModel: "test", Turns: turns, Topic: topic}
	repo := &testRepo{saves: make(chan saved, 4), released: make(chan bool, 1)}
	service := Service{Repo: repo, Provider: testProvider{strings.Replace(server.URL, "http", "ws", 1)}, Settle: 120 * time.Millisecond}
	app := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, e := websocket.Accept(w, r, nil)
		if e != nil {
			return
		}
		defer c.CloseNow()
		_ = service.Run(ctx, c, session)
	}))
	defer func() { cancel(); app.Close() }()
	client, _, e := websocket.Dial(ctx, strings.Replace(app.URL, "http", "ws", 1), nil)
	if e != nil {
		t.Fatal(e)
	}
	defer client.CloseNow()
	provider := <-upstream
	read := func(c *websocket.Conn) map[string]any {
		t.Helper()
		_, b, err := c.Read(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var v map[string]any
		_ = json.Unmarshal(b, &v)
		return v
	}
	send := func(c *websocket.Conn, v any) {
		t.Helper()
		if err := write(ctx, c, v); err != nil {
			t.Fatal(err)
		}
	}
	read(provider)
	send(provider, map[string]any{"setupComplete": map[string]any{}})
	read(client)
	read(provider)
	send(provider, map[string]any{"serverContent": map[string]bool{"turnComplete": true}})
	if read(provider)["clientContent"] == nil {
		t.Fatal("missing empty-audio retry")
	}
	send(provider, map[string]any{"serverContent": map[string]any{"modelTurn": map[string]any{"parts": []any{map[string]any{"inlineData": map[string]string{"data": base64.StdEncoding.EncodeToString(make([]byte, 4800)), "mimeType": "audio/pcm;rate=24000"}}}}, "turnComplete": true}})
	if kind, _, err := client.Read(ctx); err != nil || kind != websocket.MessageBinary {
		t.Fatal("examiner audio missing", err)
	}
	read(client)
	send(client, map[string]string{"type": "playback_done"})
	if read(client)["phase"] != "listening" {
		t.Fatal("not listening")
	}
	// Half a second of speech inside a second and a half of recording: long
	// enough to validate, well under the speech that counts as a finished
	// answer, so the advance is refused and the examiner is told to invite a
	// little more.
	tone := make([]byte, 48000)
	for i := 0; i+1 < 16000; i += 2 {
		v := int16(9000)
		if (i/2)%40 < 20 {
			v = -9000
		}
		binary.LittleEndian.PutUint16(tone[i:], uint16(v))
	}
	for off := 0; off < len(tone); off += 4096 {
		end := off + 4096
		if end > len(tone) {
			end = len(tone)
		}
		if err := client.Write(ctx, websocket.MessageBinary, tone[off:end]); err != nil {
			t.Fatal(err)
		}
		time.Sleep(40 * time.Millisecond)
	}
	send(provider, map[string]any{"serverContent": map[string]any{"inputTranscription": map[string]string{"text": "Hello."}}})
	send(provider, map[string]any{"toolCall": map[string]any{"functionCalls": []any{map[string]any{"id": "call", "name": "advance_question", "args": map[string]string{"question": "What is your typical weekday like?"}}}}})

	var instruction string
	for instruction == "" {
		v := read(provider)
		if v["realtimeInput"] != nil || v["clientContent"] != nil {
			continue
		}
		raw, _ := json.Marshal(v)
		var frame struct {
			ToolResponse struct {
				FunctionResponses []struct {
					Name     string `json:"name"`
					Response struct {
						Instruction string `json:"instruction"`
						Question    string `json:"question"`
					} `json:"response"`
				} `json:"functionResponses"`
			} `json:"toolResponse"`
		}
		if json.Unmarshal(raw, &frame) != nil || len(frame.ToolResponse.FunctionResponses) == 0 {
			continue
		}
		instruction = frame.ToolResponse.FunctionResponses[0].Response.Instruction
	}
	if !strings.Contains(instruction, "too brief") {
		t.Fatalf("expected a too-brief refusal, got %q", instruction)
	}
	if len(repo.saves) != 0 {
		t.Fatal("a refused advance must not save the turn")
	}
	// The examiner now replies. Before the fix every byte of this was dropped.
	send(provider, map[string]any{"serverContent": map[string]any{"modelTurn": map[string]any{"parts": []any{map[string]any{"inlineData": map[string]string{"data": base64.StdEncoding.EncodeToString(make([]byte, 4800)), "mimeType": "audio/pcm;rate=24000"}}}}, "turnComplete": true}})
	deadline, stop := context.WithTimeout(ctx, 5*time.Second)
	defer stop()
	for {
		kind, _, err := client.Read(deadline)
		if err != nil {
			t.Fatal("the examiner's reply never reached the student:", err)
		}
		if kind == websocket.MessageBinary {
			return
		}
	}
}

// The examiner called wait_for_student after a brief answer and then tried to
// ask for a little more. Output is disabled while the student holds the turn,
// so six chunks of that reply were discarded and both sides waited for each
// other. Once the student has spoken and gone quiet, the examiner gets heard.
func TestLiveExaminerIsHeardOnceTheStudentHasGoneQuiet(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	upstream := make(chan *websocket.Conn, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, e := websocket.Accept(w, r, nil)
		if e != nil {
			return
		}
		upstream <- c
		<-ctx.Done()
		c.CloseNow()
	}))
	defer func() { cancel(); server.Close() }()
	topic, turns, _ := d.NewPlan()
	session := d.Session{ID: "session", UserID: "user", LiveModel: "test", Turns: turns, Topic: topic}
	repo := &testRepo{saves: make(chan saved, 4), released: make(chan bool, 1)}
	service := Service{Repo: repo, Provider: testProvider{strings.Replace(server.URL, "http", "ws", 1)}, Settle: 300 * time.Millisecond}
	app := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, e := websocket.Accept(w, r, nil)
		if e != nil {
			return
		}
		defer c.CloseNow()
		_ = service.Run(ctx, c, session)
	}))
	defer func() { cancel(); app.Close() }()
	client, _, e := websocket.Dial(ctx, strings.Replace(app.URL, "http", "ws", 1), nil)
	if e != nil {
		t.Fatal(e)
	}
	defer client.CloseNow()
	provider := <-upstream
	read := func(c *websocket.Conn) map[string]any {
		t.Helper()
		_, b, err := c.Read(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var v map[string]any
		_ = json.Unmarshal(b, &v)
		return v
	}
	send := func(c *websocket.Conn, v any) {
		t.Helper()
		if err := write(ctx, c, v); err != nil {
			t.Fatal(err)
		}
	}
	audio := func() any {
		return map[string]any{"serverContent": map[string]any{"modelTurn": map[string]any{"parts": []any{map[string]any{"inlineData": map[string]string{"data": base64.StdEncoding.EncodeToString(make([]byte, 4800)), "mimeType": "audio/pcm;rate=24000"}}}}}}
	}
	read(provider)
	send(provider, map[string]any{"setupComplete": map[string]any{}})
	read(client)
	read(provider)
	send(provider, map[string]any{"serverContent": map[string]bool{"turnComplete": true}})
	if read(provider)["clientContent"] == nil {
		t.Fatal("missing empty-audio retry")
	}
	send(provider, map[string]any{"serverContent": map[string]any{"modelTurn": map[string]any{"parts": []any{map[string]any{"inlineData": map[string]string{"data": base64.StdEncoding.EncodeToString(make([]byte, 4800)), "mimeType": "audio/pcm;rate=24000"}}}}, "turnComplete": true}})
	if kind, _, err := client.Read(ctx); err != nil || kind != websocket.MessageBinary {
		t.Fatal("examiner audio missing", err)
	}
	read(client)
	send(client, map[string]string{"type": "playback_done"})
	if read(client)["phase"] != "listening" {
		t.Fatal("not listening")
	}
	tone := make([]byte, 32000)
	for i := 0; i+1 < len(tone); i += 2 {
		v := int16(9000)
		if (i/2)%40 < 20 {
			v = -9000
		}
		binary.LittleEndian.PutUint16(tone[i:], uint16(v))
	}
	for off := 0; off < len(tone); off += 4096 {
		end := off + 4096
		if end > len(tone) {
			end = len(tone)
		}
		if err := client.Write(ctx, websocket.MessageBinary, tone[off:end]); err != nil {
			t.Fatal(err)
		}
		time.Sleep(40 * time.Millisecond)
	}
	send(provider, map[string]any{"serverContent": map[string]any{"inputTranscription": map[string]string{"text": "I work."}}})
	// Mid-thought: the student's own pause must not hand the turn away.
	send(provider, audio())
	drained := make(chan websocket.MessageType, 8)
	go func() {
		for {
			kind, _, err := client.Read(ctx)
			if err != nil {
				return
			}
			drained <- kind
		}
	}()
	select {
	case kind := <-drained:
		if kind == websocket.MessageBinary {
			t.Fatal("the examiner was let in while the student had only just stopped speaking")
		}
	case <-time.After(200 * time.Millisecond):
	}
	// Now they have been quiet for longer than the examiner is asked to wait.
	time.Sleep(500 * time.Millisecond)
	send(provider, audio())
	deadline := time.After(5 * time.Second)
	for {
		select {
		case kind := <-drained:
			if kind == websocket.MessageBinary {
				return
			}
		case <-deadline:
			t.Fatal("the examiner's reply never reached the student")
		}
	}
}
