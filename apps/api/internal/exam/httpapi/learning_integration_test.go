package httpapi_test

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/own/mock-ai/apps/api/internal/platform/keyvault"
	speakingapp "github.com/own/mock-ai/apps/api/internal/speaking/application"
	speakinghttp "github.com/own/mock-ai/apps/api/internal/speaking/httpapi"
	speakinglive "github.com/own/mock-ai/apps/api/internal/speaking/live"
	speakingpg "github.com/own/mock-ai/apps/api/internal/speaking/postgres"
	writingapp "github.com/own/mock-ai/apps/api/internal/writing/application"
	writinghttp "github.com/own/mock-ai/apps/api/internal/writing/httpapi"
	writingpg "github.com/own/mock-ai/apps/api/internal/writing/postgres"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	migrations "github.com/own/mock-ai/apps/api/db/migrations"
	adminapp "github.com/own/mock-ai/apps/api/internal/admin/application"
	adminhttp "github.com/own/mock-ai/apps/api/internal/admin/httpapi"
	adminpg "github.com/own/mock-ai/apps/api/internal/admin/postgres"
	app "github.com/own/mock-ai/apps/api/internal/exam/application"
	examhttp "github.com/own/mock-ai/apps/api/internal/exam/httpapi"
	exampg "github.com/own/mock-ai/apps/api/internal/exam/postgres"
	"github.com/own/mock-ai/apps/api/internal/identity"
	identitydomain "github.com/own/mock-ai/apps/api/internal/identity/domain"
	"github.com/own/mock-ai/apps/api/internal/identity/googleauth"
	authhttp "github.com/own/mock-ai/apps/api/internal/identity/httpapi"
	authpg "github.com/own/mock-ai/apps/api/internal/identity/postgres"
	"github.com/own/mock-ai/apps/api/internal/platform/httpserver"
	db "github.com/own/mock-ai/apps/api/internal/platform/postgres/sqlc"
)

// Run only against a newly created mock_ai_learning_test database. The URL's
// database is overridden so this test cannot migrate the production database.
func TestLearningIntegration(t *testing.T) {
	if os.Getenv("MOCKAI_INTEGRATION") != "1" {
		t.Skip("requires isolated mock_ai_learning_test database")
	}
	address, e := url.Parse(os.Getenv("DATABASE_URL"))
	if e != nil {
		t.Fatal(e)
	}
	address.Path = "/mock_ai_learning_test"
	source, e := iofs.New(migrations.Files, ".")
	if e != nil {
		t.Fatal(e)
	}
	migration, e := migrate.NewWithSourceInstance("iofs", source, address.String())
	if e != nil {
		t.Fatal("connect test migrations:", e)
	}
	defer migration.Close()
	if e = migration.Up(); e != nil {
		t.Fatal(e)
	}
	pool, e := pgxpool.New(context.Background(), address.String())
	if e != nil {
		t.Fatal(e)
	}
	defer pool.Close()
	repo := exampg.New(pool)
	service := app.NewService(repo)
	auth, e := identity.NewService(authpg.New(pool), "test-token", 3600000000000)
	if e != nil {
		t.Fatal(e)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	authHandler := authhttp.New(auth, googleauth.New("", "", "", ""), logger, "http://localhost", false)
	adminService := adminapp.New(adminpg.New(pool))
	adminHandler := adminhttp.New(adminService, "http://localhost", false)
	writingService := writingapp.New(writingpg.New(pool), writingTestEvaluator{}, "gemini-3.7-flash")
	speakingRepo := speakingpg.New(pool, strings.Repeat("ab", 32))
	speakingService := &speakingapp.Service{Repo: speakingRepo, Provider: speakingTestProvider{}, Model: "test-audio-model", Log: logger}
	speakingHandler := speakinghttp.New(speakingService, authHandler, "http://localhost")
	speakingHandler.Live = &speakinglive.Service{Repo: speakingRepo, Provider: speakingTestProvider{}, Model: "test-live"}
	handler := httpserver.New(logger, "http://localhost", authHandler, examhttp.New(service, authHandler, logger, adminHandler), adminHandler, writinghttp.New(writingService, authHandler, "http://localhost"), speakingHandler)
	request := func(method, path string, payload any, cookie *http.Cookie, status int) map[string]json.RawMessage {
		t.Helper()
		b, _ := json.Marshal(payload)
		req := httptest.NewRequest(method, path, bytes.NewReader(b))
		req.Header.Set("Origin", "http://localhost")
		if cookie != nil {
			req.AddCookie(cookie)
		}
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
		if rr.Code != status {
			t.Fatalf("%s %s: got %d want %d: %s", method, path, rr.Code, status, rr.Body.String())
		}
		var out map[string]json.RawMessage
		if status != 204 {
			if e := json.Unmarshal(rr.Body.Bytes(), &out); e != nil {
				t.Fatal(e)
			}
		}
		return out
	}
	// Sign-in is Telegram-only, so the test signs Mini App init data with the
	// same bot token the service above was given. The server then verifies it
	// with exactly the HMAC it verifies in production.
	signIn := func(id int64, name string) (identitydomain.User, identity.Session) {
		t.Helper()
		user, session, err := auth.LoginTelegram(context.Background(), signedInitData("test-token", id, name), "", "")
		if err != nil {
			t.Fatal(err)
		}
		return user, session
	}
	// Telegram login links: the bot mints one per /start, and redeeming it is
	// the whole sign-in. Single use and expiry are enforced by the UPDATE that
	// spends the row, so they are only really tested against Postgres.
	linkStudent, _ := signIn(90003, "Link QA")
	redeem := func(token string) *httptest.ResponseRecorder {
		t.Helper()
		rr := httptest.NewRecorder()
		req := httptest.NewRequest("GET", "/api/v1/auth/telegram/link?token="+url.QueryEscape(token), nil)
		req.Header.Set("Origin", "http://localhost")
		handler.ServeHTTP(rr, req)
		return rr
	}
	linkToken, expiresAt, e := auth.IssueLoginLink(context.Background(), linkStudent.ID)
	if e != nil {
		t.Fatal(e)
	}
	if !expiresAt.After(time.Now().Add(10 * time.Minute)) {
		t.Fatalf("login link expires too soon: %v", expiresAt)
	}
	redeemed := redeem(linkToken)
	if redeemed.Code != http.StatusFound || redeemed.Header().Get("Location") != "http://localhost/?auth=success" {
		t.Fatalf("login link did not sign the student in: %d %s", redeemed.Code, redeemed.Header().Get("Location"))
	}
	issued := ""
	for _, c := range redeemed.Result().Cookies() {
		if c.Name == "mock_ai_session" {
			issued = c.Value
		}
	}
	if issued == "" {
		t.Fatal("login link issued no session cookie")
	}
	if who, err := auth.Authenticate(context.Background(), issued); err != nil || who.ID != linkStudent.ID {
		t.Fatalf("login link session belongs to the wrong student: %v %v", who.ID, err)
	}
	// Tapping the same link again must not mint a second session.
	if replay := redeem(linkToken); replay.Header().Get("Location") != "http://localhost/?auth_error=login_link_invalid" {
		t.Fatalf("a spent login link was accepted again: %s", replay.Header().Get("Location"))
	}
	if forged := redeem("Zm9yZ2VkLXRva2VuLXRoYXQtaXMtbm90LXJlYWwtYXQtYWxsLi4"); forged.Header().Get("Location") != "http://localhost/?auth_error=login_link_invalid" {
		t.Fatalf("a forged token was accepted: %s", forged.Header().Get("Location"))
	}
	// An expired link is refused even though it was never spent.
	stale, _, e := auth.IssueLoginLink(context.Background(), linkStudent.ID)
	if e != nil {
		t.Fatal(e)
	}
	staleHash := sha256.Sum256([]byte(stale))
	if _, e = pool.Exec(context.Background(), "UPDATE telegram_login_links SET expires_at = now() - interval '1 minute' WHERE token_hash = $1", staleHash[:]); e != nil {
		t.Fatal(e)
	}
	if expired := redeem(stale); expired.Header().Get("Location") != "http://localhost/?auth_error=login_link_invalid" {
		t.Fatalf("an expired login link was accepted: %s", expired.Header().Get("Location"))
	}
	// Hammering /start cannot mint an unbounded supply of live tokens.
	var burstErr error
	for i := 0; i < 20 && burstErr == nil; i++ {
		_, _, burstErr = auth.IssueLoginLink(context.Background(), linkStudent.ID)
	}
	if !errors.Is(burstErr, identity.ErrTooManyLoginLinks) {
		t.Fatalf("login links are not rate limited: %v", burstErr)
	}
	t.Log("Telegram login links: single use, expiry, forgery and burst limits passed")

	student, session := signIn(90001, "Learning QA")
	otherStudent, otherSession := signIn(90002, "Other QA")
	cookie := &http.Cookie{Name: "mock_ai_session", Value: session.Token}
	other := &http.Cookie{Name: "mock_ai_session", Value: otherSession.Token}
	request("GET", "/api/v1/me/mistakes", nil, nil, 401)
	request("GET", "/api/v1/admin/content", nil, cookie, 401)
	exams, e := service.ListExams(context.Background(), student.ID)
	if e != nil || len(exams) == 0 {
		t.Fatal("missing exams", e)
	}
	attempt, e := service.StartAttempt(context.Background(), student.ID, exams[0].ID)
	if e != nil {
		t.Fatal(e)
	}
	path := "/api/v1/attempts/" + attempt.ID + "/workspace"
	workspace := app.Workspace{Flags: []string{"question-1"}, Notes: map[string]string{"question-1": "Recheck the date"}, Highlights: map[string][]string{"section-1": {"important"}}}
	request("PUT", path, workspace, cookie, 204)
	request("GET", path, nil, other, 404)
	request("PUT", path, workspace, other, 404)
	saved := request("GET", path, nil, cookie, 200)
	if !strings.Contains(string(saved["notes"]), "Recheck the date") {
		t.Fatal("notes not persisted")
	}
	// Production may contain only timed mocks. Their accessible questions must
	// still support quick practice and checking answers without changing purpose.
	rows, e := pool.Query(context.Background(), "UPDATE exam_versions SET purpose='mock' WHERE purpose='practice' RETURNING id::text")
	if e != nil {
		t.Fatal(e)
	}
	var practiceVersions []string
	for rows.Next() {
		var id string
		if e = rows.Scan(&id); e != nil {
			t.Fatal(e)
		}
		practiceVersions = append(practiceVersions, id)
	}
	if e = rows.Err(); e != nil {
		t.Fatal(e)
	}
	rows.Close()
	practice, e := service.ListPracticeQuestions(context.Background(), student.ID, "reading", "", 5)
	if e != nil || len(practice) == 0 {
		t.Fatal("no practice", e)
	}
	qid := practice[0].ID
	request("POST", "/api/v1/practice/questions/"+qid+"/check", map[string]any{"values": []string{"mock practice answer"}}, cookie, 200)
	if _, e = pool.Exec(context.Background(), "UPDATE exam_versions SET purpose='practice' WHERE id=ANY($1::uuid[])", practiceVersions); e != nil {
		t.Fatal(e)
	}
	request("GET", "/api/v1/me/community", nil, nil, 401)
	request("PUT", "/api/v1/me/community", app.CommunityProfile{Nickname: "x", Enabled: true}, cookie, 422)
	request("PUT", "/api/v1/me/community", app.CommunityProfile{Nickname: "<script>", Enabled: true}, cookie, 422)
	request("POST", "/api/v1/me/mistakes/"+qid+"/retry", map[string]any{"values": []string{"wrong"}}, other, 404)
	bad := request("POST", "/api/v1/practice/questions/"+qid+"/check", map[string]any{"values": []string{"deliberately wrong"}}, cookie, 200)
	var feedback app.PracticeFeedback
	json.Unmarshal(bad["feedback"], &feedback)
	// Activity is private by default and repetitions cannot inflate the weekly score.
	community := func(c *http.Cookie) app.Community {
		t.Helper()
		raw := request("GET", "/api/v1/me/community", nil, c, 200)
		b, _ := json.Marshal(raw)
		var out app.Community
		if e := json.Unmarshal(b, &out); e != nil {
			t.Fatal(e)
		}
		return out
	}
	initial := community(cookie)
	if initial.Profile.Enabled || initial.Stats.WeekPoints != 1 || len(initial.Board) != 0 {
		t.Fatalf("unexpected initial community: %+v", initial)
	}
	request("POST", "/api/v1/practice/questions/"+qid+"/check", map[string]any{"values": []string{"wrong again"}}, cookie, 200)
	if community(cookie).Stats.WeekPoints != 1 {
		t.Fatal("duplicate practice inflated points")
	}
	request("PUT", "/api/v1/me/community", app.CommunityProfile{Nickname: "Quiet Learner", Enabled: true}, cookie, 204)
	visible := community(other)
	if len(visible.Board) != 1 || visible.Board[0].Nickname != "Quiet Learner" || visible.Board[0].IsMe || visible.Board[0].Position != 1 {
		t.Fatalf("bad public board: %+v", visible)
	}
	if !community(cookie).Board[0].IsMe {
		t.Fatal("own rank not identified")
	}
	request("PUT", "/api/v1/me/community", app.CommunityProfile{Nickname: "Quiet Learner", Enabled: false}, cookie, 204)
	if len(community(other).Board) != 0 {
		t.Fatal("opted-out student still visible")
	}
	notebook := request("GET", "/api/v1/me/mistakes", nil, cookie, 200)
	var mistakes []app.Mistake
	json.Unmarshal(notebook["mistakes"], &mistakes)
	if len(mistakes) != 1 || mistakes[0].Resolved {
		t.Fatal("missing unresolved mistake")
	}
	// A "choose TWO" question is only correct when both answers are given, while
	// a single-answer question lists alternatives of which exactly one counts.
	// The feed's first question is whichever the practice ordering returns, so
	// the retry has to follow its type rather than assume one of them.
	retry := feedback.AcceptedAnswer[:1]
	if practice[0].Type == "multiple_select" {
		retry = feedback.AcceptedAnswer
	}
	request("POST", "/api/v1/me/mistakes/"+qid+"/retry", map[string]any{"values": retry}, cookie, 200)
	notebook = request("GET", "/api/v1/me/mistakes", nil, cookie, 200)
	json.Unmarshal(notebook["mistakes"], &mistakes)
	if !mistakes[0].Resolved {
		t.Fatal("mistake did not resolve")
	}
	// Reminders need a linked Telegram account. Every student now has one by
	// the time they can sign in at all, so the rule is satisfied rather than
	// blocking -- what still has to be refused is a bad timezone.
	request("PUT", "/api/v1/me/reminder", app.Reminder{Enabled: true, LocalTime: "19:00", Timezone: "Asia/Tashkent"}, cookie, 204)
	if reminder := request("GET", "/api/v1/me/reminder", nil, cookie, 200); string(reminder["enabled"]) != "true" || string(reminder["telegram_linked"]) != "true" {
		t.Fatalf("reminder not stored for a Telegram student: %v", reminder)
	}
	request("PUT", "/api/v1/me/reminder", app.Reminder{Enabled: true, LocalTime: "19:00", Timezone: "Invalid/Zone"}, cookie, 422)
	request("PUT", "/api/v1/me/reminder", app.Reminder{Enabled: false, LocalTime: "19:00", Timezone: "Asia/Tashkent"}, cookie, 204)
	var uid pgtype.UUID
	uid.Scan(student.ID)
	queries := db.New(pool)
	if e = queries.AssignUserRole(context.Background(), db.AssignUserRoleParams{UserID: uid, Role: db.UserRoleAdmin}); e != nil {
		t.Fatal(e)
	}
	// A legacy student admin role must never unlock the separate admin realm.
	request("GET", "/api/v1/admin/content", nil, cookie, 401)
	request("GET", "/api/v1/admin/me", nil, cookie, 401)
	adminID, e := adminService.Create(context.Background(), "editor@example.com", "Editor", "separate-admin-password")
	if e != nil {
		t.Fatal(e)
	}
	request("POST", "/api/v1/admin/auth/login", map[string]string{"email": "learning@example.com", "password": "long-test-password"}, nil, 401)
	loginBody := `{"email":"editor@example.com","password":"separate-admin-password"}`
	login := httptest.NewRequest("POST", "/api/v1/admin/auth/login", strings.NewReader(loginBody))
	login.Header.Set("Origin", "https://untrusted.example")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, login)
	if response.Code != 403 {
		t.Fatal("cross-origin login accepted")
	}
	login = httptest.NewRequest("POST", "/api/v1/admin/auth/login", strings.NewReader(loginBody))
	login.Header.Set("Origin", "http://localhost")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, login)
	if response.Code != 200 {
		t.Fatal("admin login failed", response.Body.String())
	}
	adminCookie := response.Result().Cookies()[0]
	if adminCookie.Name != adminhttp.CookieName || !adminCookie.HttpOnly || adminCookie.Path != "/api/v1/admin" || adminCookie.SameSite != http.SameSiteStrictMode {
		t.Fatal("unsafe admin cookie")
	}
	request("GET", "/api/v1/me", nil, adminCookie, 401)
	request("GET", "/api/v1/admin/me", nil, adminCookie, 200)
	request("GET", "/api/v1/admin/students", nil, cookie, 401)
	students := request("GET", "/api/v1/admin/students", nil, adminCookie, 200)
	if !strings.Contains(string(students["students"]), "Learning QA") || !strings.Contains(string(students["students"]), "Other QA") {
		t.Fatal("admin student selector is incomplete")
	}
	adminRead := httptest.NewRequest("GET", "/api/v1/admin/me", nil)
	adminRead.AddCookie(adminCookie)
	adminRead.Header.Set("Origin", "http://localhost")
	adminResponse := httptest.NewRecorder()
	handler.ServeHTTP(adminResponse, adminRead)
	if adminResponse.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatal("student CORS policy leaked into admin realm")
	}
	var studentCount int
	if e = pool.QueryRow(context.Background(), "SELECT count(*) FROM users WHERE email='editor@example.com'").Scan(&studentCount); e != nil || studentCount != 0 {
		t.Fatal("admin must not create a student", e)
	}
	sharedAudio := append([]byte("ID3"), bytes.Repeat([]byte{1}, 125)...)
	sharedMediaID, e := service.SavePrivateMedia(context.Background(), adminID, "", "replacement.mp3", "audio/mpeg", sharedAudio)
	if e != nil {
		t.Fatal("save shared managed audio:", e)
	}
	if media, mediaErr := service.GetPrivateMediaForAdmin(context.Background(), sharedMediaID); mediaErr != nil || media.Filename != "replacement.mp3" {
		t.Fatal("admin cannot review shared managed audio", mediaErr)
	}
	if _, e = repo.GetPrivateMedia(context.Background(), student.ID, sharedMediaID); !errors.Is(e, app.ErrNotFound) {
		t.Fatal("unpublished shared audio leaked to a student", e)
	}
	mediaID, e := queries.CreatePrivateMedia(context.Background(), db.CreatePrivateMediaParams{OwnerUserID: uid, AdminAuthorID: func() pgtype.UUID { var v pgtype.UUID; _ = v.Scan(adminID); return v }(), Filename: "source.png", MimeType: "image/png", Sha256: strings.Repeat("a", 64), Data: []byte("integration media")})
	if e != nil {
		t.Fatal(e)
	}
	mediaURL := "/api/v1/private-media/" + mediaID.String()
	if _, e = repo.GetPrivateMedia(context.Background(), otherStudent.ID, mediaID.String()); !errors.Is(e, app.ErrNotFound) {
		t.Fatal("unpublished media leaked to another student", e)
	}
	content := app.Content{Title: "Integration reading", Skill: "reading", Purpose: "practice", Duration: 600, AccessScope: "public", Sections: []app.ContentSection{{Title: "Passage", Passage: "The library opens at nine.", SourceImageURLs: []string{mediaURL}, Questions: []app.ContentQuestion{{Type: "short_answer", Prompt: "When does it open?", Answers: []string{"nine"}, Explanation: "The opening time is stated in the passage."}}}}}
	result := request("POST", "/api/v1/admin/content", content, adminCookie, 200)
	var draftID string
	json.Unmarshal(result["id"], &draftID)
	request("POST", "/api/v1/admin/content/"+draftID+"/publish", nil, other, 401)
	request("POST", "/api/v1/admin/content/"+draftID+"/publish", nil, adminCookie, 200)
	if _, e = repo.GetPrivateMedia(context.Background(), otherStudent.ID, mediaID.String()); e != nil {
		t.Fatal("public exam media is unavailable to another student", e)
	}
	content.Sections[0].Questions[0].Explanation = "An improved explanation."
	request("PUT", "/api/v1/admin/content/"+draftID, content, adminCookie, 200)
	request("POST", "/api/v1/admin/content/"+draftID+"/publish", nil, adminCookie, 200)
	content.Skill = "listening"
	request("PUT", "/api/v1/admin/content/"+draftID, content, adminCookie, 200)
	request("POST", "/api/v1/admin/content/"+draftID+"/publish", nil, adminCookie, 422)
	var author string
	if e = pool.QueryRow(context.Background(), "SELECT admin_author_id::text FROM content_drafts WHERE id=$1", draftID).Scan(&author); e != nil || author != adminID {
		t.Fatal("wrong admin content author", e)
	}
	if _, e = pool.Exec(context.Background(), "UPDATE admins SET disabled=true WHERE id=$1", adminID); e != nil {
		t.Fatal(e)
	}
	request("GET", "/api/v1/admin/content", nil, adminCookie, 401)
	if _, e = pool.Exec(context.Background(), "UPDATE admins SET disabled=false WHERE id=$1", adminID); e != nil {
		t.Fatal(e)
	}
	request("POST", "/api/v1/admin/auth/logout", nil, adminCookie, 204)
	request("GET", "/api/v1/admin/content", nil, adminCookie, 401)
	for i := 0; i < 5; i++ {
		request("POST", "/api/v1/admin/auth/login", map[string]string{"email": "editor@example.com", "password": "wrong"}, nil, 401)
	}
	request("POST", "/api/v1/admin/auth/login", map[string]string{"email": "editor@example.com", "password": "separate-admin-password"}, nil, 401)
	t.Log("separate admin table, session isolation, legacy role denial, origin protection, content publishing, disable, logout and login throttle passed")
	// No Telegram identity has to be fabricated here any more: signing in is
	// what created one. Adding a second would give this student two chat IDs
	// and claim the reminder twice.
	request("PUT", "/api/v1/me/reminder", app.Reminder{Enabled: true, LocalTime: "00:00", Timezone: "UTC"}, cookie, 204)
	first, e := queries.ClaimReminders(context.Background())
	if e != nil || len(first) != 1 {
		t.Fatal("expected one due reminder", len(first), e)
	}
	again, e := queries.ClaimReminders(context.Background())
	if e != nil || len(again) != 0 {
		t.Fatal("reminder claimed twice", e)
	}
	request("PUT", "/api/v1/me/reminder", app.Reminder{Enabled: false, LocalTime: "00:00", Timezone: "UTC"}, cookie, 204)
	t.Log("ownership, notebook resolution, annotations, admin publishing/versioning, reminder validation and single daily claim passed")
	// Equal scores share rank; nicknames never reveal another user's identifier.
	request("POST", "/api/v1/practice/questions/"+qid+"/check", map[string]any{"values": []string{"test"}}, other, 200)
	request("PUT", "/api/v1/me/community", app.CommunityProfile{Nickname: "Second Reader", Enabled: true}, other, 204)
	request("PUT", "/api/v1/me/community", app.CommunityProfile{Nickname: "Quiet Learner", Enabled: true}, cookie, 204)
	tied := community(cookie)
	if len(tied.Board) != 2 || tied.Board[0].Position != 1 || tied.Board[1].Position != 1 {
		t.Fatalf("ties should share rank: %+v", tied.Board)
	}
	if _, e = service.SubmitAttempt(context.Background(), student.ID, attempt.ID); e != nil {
		t.Fatal(e)
	}
	if community(cookie).Stats.WeekPoints != 1 {
		t.Fatal("empty mock earned points")
	}
	exam, e := service.GetExam(context.Background(), student.ID, exams[0].ID)
	if e != nil {
		t.Fatal(e)
	}
	for i := 0; i < 2; i++ {
		a, e := service.StartAttempt(context.Background(), student.ID, exams[0].ID)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = service.SaveAnswer(context.Background(), student.ID, a.ID, exam.Sections[0].Questions[0].ID, []string{"answer"}); e != nil {
			t.Fatal(e)
		}
		if _, e = service.SubmitAttempt(context.Background(), student.ID, a.ID); e != nil {
			t.Fatal(e)
		}
	}
	if community(cookie).Stats.WeekPoints != 11 {
		t.Fatal("mock version should count once each week")
	}
	if _, e = pool.Exec(context.Background(), "UPDATE practice_answers SET attempted_at=date_trunc('week',now() AT TIME ZONE 'UTC') AT TIME ZONE 'UTC' - interval '1 second'"); e != nil {
		t.Fatal(e)
	}
	if _, e = pool.Exec(context.Background(), "UPDATE attempts SET scored_at=date_trunc('week',now() AT TIME ZONE 'UTC') AT TIME ZONE 'UTC' - interval '1 second' WHERE status='scored'"); e != nil {
		t.Fatal(e)
	}
	reset := community(cookie)
	if reset.Stats.WeekPoints != 0 || len(reset.Board) != 0 || reset.Stats.Questions != 1 {
		t.Fatalf("week reset must preserve lifetime progress: %+v", reset)
	}
	t.Log("community privacy, opt-out, validation, ties, duplicate prevention, empty mocks and UTC reset passed")
	request("GET", "/api/v1/writing/submissions", nil, nil, 401)
	request("GET", "/api/v1/writing/submissions", nil, adminCookie, 401)
	sealed, e := keyvault.Seal(strings.Repeat("ab", 32), "test-gemini-key-not-a-live-credential")
	if e != nil {
		t.Fatal(e)
	}
	if e = queries.ImportAIKey(context.Background(), db.ImportAIKeyParams{Fingerprint: "integration-only", Ciphertext: sealed}); e != nil {
		t.Fatal(e)
	}
	essay := strings.Repeat("Public transport helps workers reach their jobs and saves road space. ", 25)
	input := writingapp.Input{Prompt: "Should cities invest in public transport instead of roads?", Essay: essay}
	// Task 1 evaluation must receive the same visual reference the student saw.
	var referenceID string
	referencePrompt := "Task 1: Describe the chart of library visits between 2000 and 2020."
	referenceData, _ := json.Marshal(map[string]any{"task": "task_1", "prompt": referencePrompt, "source_image_urls": []string{mediaURL}})
	if e = pool.QueryRow(context.Background(), "INSERT INTO managed_content(kind,slug,title,status,data) VALUES('writing_prompt','reference-qa','Reference QA','published',$1) RETURNING id::text", referenceData).Scan(&referenceID); e != nil {
		t.Fatal(e)
	}
	referenceDraft, e := writingService.Create(context.Background(), student.ID, writingapp.Input{ContentID: referenceID, Prompt: referencePrompt, Essay: essay})
	if e != nil {
		t.Fatal("create referenced writing", e)
	}
	var snapshot writingapp.Reference
	if json.Unmarshal(referenceDraft.Reference, &snapshot) != nil || snapshot.Task != "task_1" || len(snapshot.Images) != 1 || string(snapshot.Images[0].Data) != "integration media" {
		t.Fatal("missing visual snapshot")
	}
	publicDraft, _ := json.Marshal(referenceDraft)
	if bytes.Contains(publicDraft, []byte("mime_type")) || bytes.Contains(publicDraft, []byte("reference")) {
		t.Fatal("internal reference leaked in response")
	}
	if _, e = writingService.Create(context.Background(), student.ID, writingapp.Input{ContentID: referenceID, Prompt: "A different prompt that should not match", Essay: essay}); !errors.Is(e, writingapp.ErrInvalid) {
		t.Fatal("mismatched reference accepted", e)
	}
	if _, e = pool.Exec(context.Background(), "UPDATE managed_content SET status='archived' WHERE id=$1", referenceID); e != nil {
		t.Fatal(e)
	}
	if _, e = writingService.Create(context.Background(), student.ID, writingapp.Input{ContentID: referenceID, Prompt: referencePrompt, Essay: essay}); !errors.Is(e, writingapp.ErrNotFound) {
		t.Fatal("archived reference accepted", e)
	}
	referenceSaved, e := writingService.Save(context.Background(), student.ID, referenceDraft.ID, writingapp.Input{Prompt: referencePrompt, Essay: essay, Revision: referenceDraft.Revision})
	if e != nil || !bytes.Equal(referenceSaved.Reference, referenceDraft.Reference) {
		t.Fatal("existing snapshot lost after content archive", e)
	}
	if e = writingService.Repo.Delete(context.Background(), student.ID, referenceDraft.ID); e != nil {
		t.Fatal(e)
	}
	if _, e = pool.Exec(context.Background(), "DELETE FROM managed_content WHERE id=$1", referenceID); e != nil {
		t.Fatal(e)
	}
	draft, e := writingService.Create(context.Background(), student.ID, input)
	if e != nil {
		t.Fatal(e)
	}
	wpath := "/api/v1/writing/submissions/" + draft.ID
	request("GET", wpath, nil, other, 404)
	request("PUT", wpath, input, other, 404)
	request("DELETE", wpath, nil, other, 404)
	request("POST", wpath+"/evaluate", map[string]any{"revision": 1, "consent": true}, other, 404)
	request("POST", wpath+"/evaluate", map[string]any{"revision": 1, "consent": false}, cookie, 422)
	request("POST", wpath+"/evaluate", map[string]any{"revision": 999, "consent": true}, cookie, 409)
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, e := writingService.Submit(context.Background(), student.ID, draft.ID, 1, true)
			errs <- e
		}()
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal("concurrent submit failed", e)
		}
	}
	requests, e := queries.CountWritingToday(context.Background(), uid)
	if e != nil || requests != 1 {
		t.Fatal("duplicate requests consumed quota", requests, e)
	}
	request("PUT", wpath, input, cookie, 409)
	request("DELETE", wpath, nil, cookie, 409)
	workerCtx, stopWorker := context.WithCancel(context.Background())
	go writingService.Run(workerCtx)
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		draft, e = writingService.Repo.Get(context.Background(), student.ID, draft.ID)
		if e != nil {
			t.Fatal(e)
		}
		if draft.Status == "completed" {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	stopWorker()
	if draft.Status != "completed" {
		t.Fatal("writing worker did not complete", draft.Status)
	}
	var wr writingapp.Report
	if e = json.Unmarshal(draft.Report, &wr); e != nil || wr.Overall != 6.5 {
		t.Fatal("invalid writing aggregate", e)
	}
	copy, e := writingService.Create(context.Background(), student.ID, writingapp.Input{ParentID: draft.ID, Essay: essay})
	if e != nil || copy.Prompt != input.Prompt {
		t.Fatal("revision lost task", e)
	}
	request("POST", "/api/v1/writing/submissions", writingapp.Input{ParentID: draft.ID, Essay: essay}, other, 404)
	if _, e = writingService.Submit(context.Background(), student.ID, copy.ID, 1, true); e != nil {
		t.Fatal(e)
	}
	oldRun, e := writingService.Repo.Claim(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	if _, e = pool.Exec(context.Background(), "UPDATE writing_submissions SET lease_until=now()-interval '1 second' WHERE id=$1", copy.ID); e != nil {
		t.Fatal(e)
	}
	if e = writingService.Repo.Expire(context.Background()); e != nil {
		t.Fatal(e)
	}
	interrupted, e := writingService.Repo.Get(context.Background(), student.ID, copy.ID)
	if e != nil || interrupted.Status != "failed" || interrupted.ErrorCode != "interrupted" {
		t.Fatal("interrupted job not recoverable", e)
	}
	if _, e = writingService.Submit(context.Background(), student.ID, copy.ID, 1, true); e != nil {
		t.Fatal(e)
	}
	newRun, e := writingService.Repo.Claim(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	if e = writingService.Repo.Finish(context.Background(), oldRun.ID, oldRun.RunID, nil, "invalid_feedback"); e != writingapp.ErrConflict {
		t.Fatal("stale worker overwrote retry", e)
	}
	if e = writingService.Repo.Finish(context.Background(), newRun.ID, newRun.RunID, nil, "provider_unavailable"); e != nil {
		t.Fatal(e)
	}
	for i := 0; i < 2; i++ {
		v, e := writingService.Create(context.Background(), student.ID, input)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = writingService.Submit(context.Background(), student.ID, v.ID, 1, true); e != nil {
			t.Fatal(e)
		}
	}
	request("DELETE", wpath, nil, cookie, 204)
	request("GET", wpath, nil, cookie, 404)
	request("POST", "/api/v1/writing/submissions/"+copy.ID+"/evaluate", map[string]any{"revision": 1, "consent": true}, cookie, 429)
	t.Log("writing ownership, consent, revision locking, concurrent idempotency, durable worker report, computed score, revisions and deletion-resistant quota passed")
	testSpeaking(t, handler, speakingService, pool, cookie, other)
	testLiveSpeakingStorage(t, pool)
	testLiveSpeakingHTTP(t, handler, other, cookie)
	// Only disposable integration content is removed. Production downgrades refuse
	// to erase attribution while admin-authored content still exists.
	if _, e = pool.Exec(context.Background(), "WITH removed AS (DELETE FROM content_drafts WHERE id=$1 RETURNING exam_id) DELETE FROM exams WHERE id IN (SELECT exam_id FROM removed)", draftID); e != nil {
		t.Fatal(e)
	}
	if e = migration.Steps(-8); e != nil {
		t.Fatal("rollback new migrations:", e)
	}
	if e = migration.Up(); e != nil {
		t.Fatal("reapply new migrations:", e)
	}
	t.Log("new migrations rolled back and reapplied successfully")
}

type writingTestEvaluator struct{}

func (writingTestEvaluator) Evaluate(_ context.Context, s writingapp.Submission) ([]byte, error) {
	return json.Marshal(writingapp.Report{Overall: 9, Summary: "Add a specific example.", TopicAlignment: writingapp.TopicAlignment{Status: "aligned", PromptTopic: "public transport investment", EssayTopic: "public transport benefits", Explanation: "The essay addresses the prompt."}, Criteria: []writingapp.Criterion{{Key: "task_response", Score: 6, Reason: "Needs development.", HowToImprove: "Add a relevant example."}, {Key: "coherence_cohesion", Score: 7, Reason: "Clear links.", HowToImprove: "Link the example to the claim."}, {Key: "lexical_resource", Score: 6, Reason: "Repetitive vocabulary.", HowToImprove: "Use more precise transport terms."}, {Key: "grammar", Score: 7, Reason: "Accurate sentences.", HowToImprove: "Vary sentence structures."}}, Strengths: []string{"Clear claim."}, Issues: []writingapp.Issue{{Category: "argument", Quote: "Public transport helps workers", Explanation: "Explain the mechanism.", Suggestion: "Add a realistic example.", Example: "An early bus connects workers to an industrial estate."}}, NextSteps: []string{"Develop a causal explanation."}, Rewrite: writingapp.Rewrite{Original: "Public transport helps workers", Improved: "Frequent buses give workers a reliable commute", Explanation: "More specific."}})
}

// signedInitData builds Telegram Mini App init data the way Telegram does.
func signedInitData(botToken string, id int64, name string) string {
	profile := fmt.Sprintf(`{"id":%d,"first_name":%q}`, id, name)
	fields := map[string]string{
		"auth_date": strconv.FormatInt(time.Now().Unix(), 10),
		"user":      profile,
	}
	keys := make([]string, 0, len(fields))
	for key := range fields {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, key+"="+fields[key])
	}
	secret := hmac.New(sha256.New, []byte("WebAppData"))
	secret.Write([]byte(botToken))
	signature := hmac.New(sha256.New, secret.Sum(nil))
	signature.Write([]byte(strings.Join(parts, "\n")))
	query := url.Values{}
	for key, value := range fields {
		query.Set(key, value)
	}
	query.Set("hash", hex.EncodeToString(signature.Sum(nil)))
	return query.Encode()
}
