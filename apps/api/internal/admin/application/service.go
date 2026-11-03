package application

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/own/mock-ai/apps/api/internal/identity/password"
	"net/mail"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

var ErrUnauthorized = errors.New("invalid admin credentials or temporarily unavailable; try again later")
var ErrInvalid = errors.New("valid email, name and a password of 12–1024 bytes are required")
var ErrInvalidContent = errors.New("content fields are invalid")
var ErrContentNotFound = errors.New("content item not found")
var ErrStudentNotFound = errors.New("student not found")

var slugPattern = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)

type Admin struct {
	ID          string `json:"id"`
	Email       string `json:"email"`
	DisplayName string `json:"display_name"`
	// CentreID is empty for a platform administrator, who owns the global
	// library and creates centres. Otherwise this administrator can only see
	// and change their own centre's content.
	CentreID   string `json:"centre_id,omitempty"`
	CentreName string `json:"centre_name,omitempty"`
}

// Platform reports whether this administrator owns the platform rather than a
// single centre.
func (a Admin) Platform() bool { return a.CentreID == "" }

type Student struct {
	ID               string     `json:"id"`
	DisplayName      string     `json:"display_name"`
	Email            string     `json:"email,omitempty"`
	TelegramUsername string     `json:"telegram_username,omitempty"`
	TelegramID       string     `json:"telegram_id,omitempty"`
	JoinedAt         time.Time  `json:"joined_at"`
	LastActivityAt   *time.Time `json:"last_activity_at,omitempty"`
	Attempts         int32      `json:"attempts"`
	CompletedMocks   int32      `json:"completed_mocks"`
	PracticeAnswers  int32      `json:"practice_answers"`
	WritingReports   int32      `json:"writing_reports"`
	SpeakingReports  int32      `json:"speaking_reports"`
	AverageBand      *float64   `json:"average_band"`
	BestBand         *float64   `json:"best_band"`
	LatestBand       *float64   `json:"latest_band"`
}
type StudentAttempt struct {
	ID            string     `json:"id"`
	Title         string     `json:"title"`
	Skill         string     `json:"skill"`
	Status        string     `json:"status"`
	StartedAt     time.Time  `json:"started_at"`
	SubmittedAt   *time.Time `json:"submitted_at,omitempty"`
	BandScore     *float64   `json:"band_score"`
	Answered      int32      `json:"answered"`
	Correct       int32      `json:"correct"`
	QuestionCount int32      `json:"question_count"`
}
type StudentSkill struct {
	Skill       string  `json:"skill"`
	Completed   int32   `json:"completed"`
	AverageBand float64 `json:"average_band"`
	BestBand    float64 `json:"best_band"`
	LatestBand  float64 `json:"latest_band"`
}
type StudentAIReport struct {
	ID        string    `json:"id"`
	Kind      string    `json:"kind"`
	Title     string    `json:"title"`
	Status    string    `json:"status"`
	Overall   *float64  `json:"overall"`
	UpdatedAt time.Time `json:"updated_at"`
}
type StudentProgress struct {
	Student  Student           `json:"student"`
	Skills   []StudentSkill    `json:"skills"`
	Attempts []StudentAttempt  `json:"attempts"`
	Reports  []StudentAIReport `json:"reports"`
}
type Credential struct {
	Admin
	Hash string
}
type ManagedContent struct {
	ID        string          `json:"id"`
	Kind      string          `json:"kind"`
	Slug      string          `json:"slug"`
	Title     string          `json:"title"`
	Summary   string          `json:"summary"`
	Status    string          `json:"status"`
	SortOrder int32           `json:"sort_order"`
	StartsAt  *time.Time      `json:"starts_at,omitempty"`
	EndsAt    *time.Time      `json:"ends_at,omitempty"`
	Data      json.RawMessage `json:"data"`
	CreatedAt time.Time       `json:"created_at"`
	UpdatedAt time.Time       `json:"updated_at"`
}
type DashboardSummary struct {
	Users            int32   `json:"users"`
	NewUsers7d       int32   `json:"new_users_7d"`
	ActiveUsers7d    int32   `json:"active_users_7d"`
	CompletedMocks   int32   `json:"completed_mocks"`
	AverageBand      float64 `json:"average_band"`
	WritingReports   int32   `json:"writing_reports"`
	SpeakingReports  int32   `json:"speaking_reports"`
	PublishedExams   int32   `json:"published_exams"`
	ExamDrafts       int32   `json:"exam_drafts"`
	PublishedContent int32   `json:"published_content"`
	ManagedDrafts    int32   `json:"managed_drafts"`
}
type DashboardDay struct {
	Date           string `json:"date"`
	Signups        int32  `json:"signups"`
	ActiveStudents int32  `json:"active_students"`
	Attempts       int32  `json:"attempts"`
}
type ContentCount struct {
	Kind   string `json:"kind"`
	Status string `json:"status"`
	Count  int32  `json:"count"`
}
type Dashboard struct {
	Summary DashboardSummary `json:"summary"`
	Trend   []DashboardDay   `json:"trend"`
	Content []ContentCount   `json:"content"`
}
type Repository interface {
	Create(context.Context, string, string, string) (string, error)
	ReserveLogin(context.Context, string) (Credential, error)
	ResetLogin(context.Context, string) error
	SaveSession(context.Context, string, string, time.Time) error
	Session(context.Context, string) (Admin, error)
	DeleteSession(context.Context, string) error
	Dashboard(context.Context) (Dashboard, error)
	ListCentres(context.Context) ([]Centre, error)
	CreateCentre(context.Context, string, string, string, bool) (Centre, error)
	UpdateCentre(context.Context, string, string, bool, bool) (Centre, error)
	ListContent(context.Context, string, string) ([]ManagedContent, error)
	CreateContent(context.Context, string, ManagedContent) (ManagedContent, error)
	UpdateContent(context.Context, string, ManagedContent) (ManagedContent, error)
	DeleteContent(context.Context, string, string) error
	ListStudents(context.Context) ([]Student, error)
	StudentProgress(context.Context, string) (StudentProgress, error)
}
type Service struct {
	repository Repository
	dummyHash  string
}

func New(r Repository) *Service {
	// A fixed dummy hash makes missing/locked accounts follow password verification too.
	hash, e := password.Hash("non-account-dummy-password")
	if e != nil {
		panic(e)
	}
	return &Service{repository: r, dummyHash: hash}
}
func (s *Service) Create(ctx context.Context, email, name, plain string) (string, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	name = strings.TrimSpace(name)
	parsed, e := mail.ParseAddress(email)
	if e != nil || parsed.Address != email || len(email) > 254 || name == "" || len(name) > 100 || len(plain) < 12 || len(plain) > 1024 {
		return "", ErrInvalid
	}
	hash, e := password.Hash(plain)
	if e != nil {
		return "", e
	}
	return s.repository.Create(ctx, email, name, hash)
}
func tokenHash(token string) string {
	v := sha256.Sum256([]byte(token))
	return hex.EncodeToString(v[:])
}
func (s *Service) Login(ctx context.Context, email, plain string) (Admin, string, error) {
	if len(email) > 254 || len(plain) > 1024 {
		return Admin{}, "", ErrUnauthorized
	}
	c, e := s.repository.ReserveLogin(ctx, strings.ToLower(strings.TrimSpace(email)))
	if e != nil {
		_, _ = password.Verify(plain, s.dummyHash)
		return Admin{}, "", ErrUnauthorized
	}
	ok, e := password.Verify(plain, c.Hash)
	if e != nil || !ok {
		return Admin{}, "", ErrUnauthorized
	}
	raw := make([]byte, 32)
	if _, e = rand.Read(raw); e != nil {
		return Admin{}, "", e
	}
	token := hex.EncodeToString(raw)
	if e = s.repository.SaveSession(ctx, c.ID, tokenHash(token), time.Now().Add(8*time.Hour)); e != nil {
		return Admin{}, "", e
	}
	if e = s.repository.ResetLogin(ctx, c.ID); e != nil {
		return Admin{}, "", e
	}
	return c.Admin, token, nil
}
func (s *Service) Session(ctx context.Context, token string) (Admin, error) {
	if len(token) != 64 {
		return Admin{}, ErrUnauthorized
	}
	a, e := s.repository.Session(ctx, tokenHash(token))
	if e != nil {
		return Admin{}, ErrUnauthorized
	}
	return a, nil
}
func (s *Service) Logout(ctx context.Context, token string) error {
	return s.repository.DeleteSession(ctx, tokenHash(token))
}

func (s *Service) Dashboard(ctx context.Context) (Dashboard, error) {
	return s.repository.Dashboard(ctx)
}

// Centre is a learning centre: a tenant whose uploaded content is private to
// its own students.
type Centre struct {
	ID            string `json:"id"`
	Slug          string `json:"slug"`
	Name          string `json:"name"`
	JoinCode      string `json:"join_code"`
	IncludeGlobal bool   `json:"include_global"`
	Disabled      bool   `json:"disabled"`
	Students      int64  `json:"students"`
	Exams         int64  `json:"exams"`
}

var (
	ErrNotPlatformAdmin = errors.New("only a platform administrator can manage learning centres")
	ErrInvalidCentre    = errors.New("a centre needs a name and a short url-safe slug")
	ErrCentreTaken      = errors.New("that centre slug is already in use")
	ErrCentreNotFound   = errors.New("learning centre not found")
)

var centreSlug = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,38}[a-z0-9]$`)

func (s *Service) ListCentres(ctx context.Context, actor Admin) ([]Centre, error) {
	if !actor.Platform() {
		return nil, ErrNotPlatformAdmin
	}
	return s.repository.ListCentres(ctx)
}

// CreateCentre adds a tenant. The join code is generated here rather than
// chosen: it travels in a Telegram deep link and in chat messages, so it has
// to be unguessable, and letting a human pick it guarantees it will not be.
func (s *Service) CreateCentre(ctx context.Context, actor Admin, name, slug string, includeGlobal bool) (Centre, error) {
	if !actor.Platform() {
		return Centre{}, ErrNotPlatformAdmin
	}
	name = strings.TrimSpace(name)
	slug = strings.ToLower(strings.TrimSpace(slug))
	if name == "" || utf8.RuneCountInString(name) > 120 || !centreSlug.MatchString(slug) {
		return Centre{}, ErrInvalidCentre
	}
	code, e := joinCode()
	if e != nil {
		return Centre{}, e
	}
	return s.repository.CreateCentre(ctx, name, slug, code, includeGlobal)
}

func (s *Service) UpdateCentre(ctx context.Context, actor Admin, id, name string, includeGlobal, disabled bool) (Centre, error) {
	if !actor.Platform() {
		return Centre{}, ErrNotPlatformAdmin
	}
	name = strings.TrimSpace(name)
	if name == "" || utf8.RuneCountInString(name) > 120 {
		return Centre{}, ErrInvalidCentre
	}
	return s.repository.UpdateCentre(ctx, id, name, includeGlobal, disabled)
}

// joinCode returns 160 bits of randomness in an alphabet without characters a
// student can confuse when a link is read aloud or retyped.
func joinCode() (string, error) {
	const alphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	raw := make([]byte, 20)
	if _, e := rand.Read(raw); e != nil {
		return "", e
	}
	out := make([]byte, len(raw))
	for i, b := range raw {
		out[i] = alphabet[int(b)%len(alphabet)]
	}
	return string(out), nil
}

func (s *Service) ListContent(ctx context.Context, adminID, kind string) ([]ManagedContent, error) {
	if kind != "" && !validKinds[kind] {
		return nil, ErrInvalidContent
	}
	return s.repository.ListContent(ctx, adminID, kind)
}
func (s *Service) ListStudents(ctx context.Context) ([]Student, error) {
	return s.repository.ListStudents(ctx)
}
func (s *Service) StudentProgress(ctx context.Context, id string) (StudentProgress, error) {
	if !regexp.MustCompile(`^[0-9a-fA-F-]{36}$`).MatchString(id) {
		return StudentProgress{}, ErrStudentNotFound
	}
	return s.repository.StudentProgress(ctx, id)
}
func (s *Service) SaveContent(ctx context.Context, adminID string, item ManagedContent) (ManagedContent, error) {
	item.Kind = strings.TrimSpace(item.Kind)
	item.Slug = strings.ToLower(strings.TrimSpace(item.Slug))
	item.Title = strings.TrimSpace(item.Title)
	item.Summary = strings.TrimSpace(item.Summary)
	if !validKinds[item.Kind] || !slugPattern.MatchString(item.Slug) || len(item.Slug) > 80 || item.Title == "" || len(item.Title) > 180 || len(item.Summary) > 1000 || !validStatuses[item.Status] || item.SortOrder < -10000 || item.SortOrder > 10000 || len(item.Data) > 100000 || !json.Valid(item.Data) || item.StartsAt != nil && item.EndsAt != nil && !item.StartsAt.Before(*item.EndsAt) || !validContentData(item.Kind, item.Data) {
		return ManagedContent{}, ErrInvalidContent
	}
	if item.ID == "" {
		return s.repository.CreateContent(ctx, adminID, item)
	}
	return s.repository.UpdateContent(ctx, adminID, item)
}
func (s *Service) DeleteContent(ctx context.Context, adminID, id string) error {
	return s.repository.DeleteContent(ctx, adminID, id)
}

var validKinds = map[string]bool{"daily_challenge": true, "achievement": true, "vocabulary": true, "writing_prompt": true, "speaking_plan": true}
var validStatuses = map[string]bool{"draft": true, "published": true, "archived": true}

func validContentData(kind string, raw []byte) bool {
	var v map[string]any
	if json.Unmarshal(raw, &v) != nil {
		return false
	}
	text := func(key string, min, max int) bool {
		s, ok := v[key].(string)
		n := len(strings.TrimSpace(s))
		return ok && n >= min && n <= max
	}
	number := func(key string, min, max float64) bool {
		n, ok := v[key].(float64)
		return ok && n >= min && n <= max && n == float64(int(n))
	}
	switch kind {
	case "daily_challenge":
		metric, _ := v["metric"].(string)
		action, _ := v["action"].(string)
		return map[string]bool{"today_questions": true, "week_days": true, "week_mocks": true}[metric] && number("target", 1, 1000) && map[string]bool{"practice": true, "exams": true}[action]
	case "achievement":
		metric, _ := v["metric"].(string)
		return map[string]bool{"questions": true, "mocks": true, "days": true}[metric] && number("target", 1, 1000000) && text("icon", 1, 4)
	case "vocabulary":
		a, ok := v["distractors"].([]any)
		if !ok || len(a) != 2 || !text("meaning", 2, 300) {
			return false
		}
		for _, value := range a {
			word, ok := value.(string)
			if !ok || len(strings.TrimSpace(word)) < 2 || len(word) > 300 {
				return false
			}
		}
		return true
	case "writing_prompt":
		task, _ := v["task"].(string)
		if !text("prompt", 20, 4000) || (task != "task_1" && task != "task_2") {
			return false
		}
		if rawPages, exists := v["source_image_urls"]; exists {
			pages, ok := rawPages.([]any)
			if !ok || len(pages) > 4 {
				return false
			}
			for _, rawPage := range pages {
				page, ok := rawPage.(string)
				if !ok || len(page) > 2000 || (!strings.HasPrefix(page, "/api/v1/private-media/") && !strings.HasPrefix(page, "https://")) {
					return false
				}
			}
		}
		return true
	case "speaking_plan":
		a, ok := v["turns"].([]any)
		if !ok || len(a) != 11 {
			return false
		}
		parts := map[int]int{}
		for _, rawTurn := range a {
			turn, ok := rawTurn.(map[string]any)
			if !ok {
				return false
			}
			p, _ := turn["prompt"].(string)
			part, _ := turn["part"].(float64)
			seconds, _ := turn["seconds"].(float64)
			prep, _ := turn["prep_seconds"].(float64)
			if len(strings.TrimSpace(p)) < 5 || len(p) > 2000 || part < 1 || part > 3 || seconds < 20 || seconds > 120 || prep < 0 || prep > 60 {
				return false
			}
			parts[int(part)]++
		}
		return parts[1] == 6 && parts[2] == 1 && parts[3] == 4
	}
	return false
}
