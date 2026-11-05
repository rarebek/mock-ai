package application

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type Workspace struct {
	Flags      []string            `json:"flags"`
	Notes      map[string]string   `json:"notes"`
	Highlights map[string][]string `json:"highlights"`
}
type Mistake struct {
	PracticeQuestion
	Resolved   bool      `json:"resolved"`
	LastMissed time.Time `json:"last_missed"`
}
type Reminder struct {
	Enabled        bool   `json:"enabled"`
	LocalTime      string `json:"local_time"`
	Timezone       string `json:"timezone"`
	TelegramLinked bool   `json:"telegram_linked"`
}
type ContentQuestion struct {
	Type        string   `json:"type"`
	Prompt      string   `json:"prompt"`
	Options     []string `json:"options"`
	Answers     []string `json:"answers"`
	Explanation string   `json:"explanation"`
}
type ContentSection struct {
	Title           string            `json:"title"`
	Passage         string            `json:"passage"`
	AudioURL        string            `json:"audio_url"`
	SourceImageURLs []string          `json:"source_image_urls"`
	Instructions    string            `json:"instructions"`
	Questions       []ContentQuestion `json:"questions"`
}
type ContentBand struct {
	Minimum int     `json:"minimum"`
	Band    float64 `json:"band"`
}
type Content struct {
	Title        string           `json:"title"`
	Description  string           `json:"description"`
	Skill        string           `json:"skill"`
	Purpose      string           `json:"purpose"`
	Duration     int32            `json:"duration_seconds"`
	Instructions string           `json:"instructions"`
	Sections     []ContentSection `json:"sections"`
	Bands        []ContentBand    `json:"bands"`
	AccessScope  string           `json:"access_scope"`
	OwnerUserID  string           `json:"owner_user_id"`
}
type Draft struct {
	ID     string  `json:"id"`
	ExamID string  `json:"exam_id"`
	Data   Content `json:"data"`
}
type PrivateMedia struct {
	Filename  string
	MimeType  string
	Data      []byte
	CreatedAt time.Time
}
type LearningRepository interface {
	GetCommunity(context.Context, string) (Community, error)
	SaveCommunityProfile(context.Context, string, CommunityProfile) error
	GetWorkspace(context.Context, string, string) (Workspace, error)
	SaveWorkspace(context.Context, string, string, Workspace) error
	ListMistakes(context.Context, string) ([]Mistake, error)
	RetryMistake(context.Context, string, string, []string) (PracticeFeedback, error)
	GetReminder(context.Context, string) (Reminder, error)
	SaveReminder(context.Context, string, Reminder) error
	ListDrafts(context.Context, string) ([]Draft, error)
	AdminAIEnabled(ctx context.Context, adminID string) (bool, error)
	SaveDraft(context.Context, string, string, Content) (string, error)
	PublishDraft(context.Context, string) (string, error)
	CreateImport(context.Context, string, string, int64, string, string, string, string, string, string) (string, error)
	ListImports(context.Context, string) ([]ContentImport, error)
	ClaimImport(context.Context) (ClaimedImport, error)
	SetImportTotal(context.Context, string, int32) error
	AddImportDraft(context.Context, string, string) error
	CompleteImport(context.Context, string, []string) error
	FailImport(context.Context, string, string) error
	FailStaleImports(context.Context) error
	SavePrivateMedia(context.Context, string, string, string, string, []byte) (string, error)
	GetPrivateMedia(context.Context, string, string) (PrivateMedia, error)
	GetPrivateMediaForAdmin(context.Context, string) (PrivateMedia, error)
}

func (s *Service) GetWorkspace(ctx context.Context, u, a string) (Workspace, error) {
	return s.repository.GetWorkspace(ctx, u, a)
}
func (s *Service) SaveWorkspace(ctx context.Context, u, a string, w Workspace) error {
	b, _ := json.Marshal(w)
	if len(b) > 64000 || len(w.Flags) > 200 || len(w.Notes) > 200 || len(w.Highlights) > 30 {
		return fmt.Errorf("%w: notes are too large", ErrInvalidProfile)
	}
	return s.repository.SaveWorkspace(ctx, u, a, w)
}
func (s *Service) ListMistakes(ctx context.Context, u string) ([]Mistake, error) {
	return s.repository.ListMistakes(ctx, u)
}
func (s *Service) RetryMistake(ctx context.Context, u, q string, values []string) (PracticeFeedback, error) {
	v, e := cleanAnswers(values)
	if e != nil {
		return PracticeFeedback{}, e
	}
	return s.repository.RetryMistake(ctx, u, q, v)
}
func (s *Service) GetReminder(ctx context.Context, u string) (Reminder, error) {
	return s.repository.GetReminder(ctx, u)
}
func (s *Service) SaveReminder(ctx context.Context, u string, r Reminder) error {
	if _, e := time.Parse("15:04", r.LocalTime); e != nil {
		return ErrInvalidProfile
	}
	if _, e := time.LoadLocation(r.Timezone); e != nil || r.Timezone == "Local" {
		return ErrInvalidProfile
	}
	current, e := s.repository.GetReminder(ctx, u)
	if e != nil {
		return e
	}
	if r.Enabled && !current.TelegramLinked {
		return fmt.Errorf("%w: connect through the Telegram bot first", ErrInvalidProfile)
	}
	return s.repository.SaveReminder(ctx, u, r)
}
func (s *Service) ListDrafts(ctx context.Context, adminID string) ([]Draft, error) {
	return s.repository.ListDrafts(ctx, adminID)
}
func (s *Service) SaveDraft(ctx context.Context, u, id string, c Content) (string, error) {
	if c.AccessScope == "" {
		c.AccessScope = "public"
	}
	for i := range c.Sections {
		for j := range c.Sections[i].Questions {
			q := &c.Sections[i].Questions[j]
			options := []string{}
			for _, o := range q.Options {
				if strings.TrimSpace(o) != "" {
					options = append(options, strings.TrimSpace(o))
				}
			}
			q.Options = options
			if q.Type == "true_false_not_given" {
				q.Options = []string{"True", "False", "Not Given"}
			}
			if q.Type == "yes_no_not_given" {
				q.Options = []string{"Yes", "No", "Not Given"}
			}
			canonicalizeChoiceAnswers(q)
		}
	}
	if e := ValidateContent(c, false); e != nil {
		return "", e
	}
	return s.repository.SaveDraft(ctx, u, id, c)
}

func canonicalizeChoiceAnswers(q *ContentQuestion) {
	if len(q.Options) == 0 {
		return
	}
	for i, answer := range q.Answers {
		for _, option := range q.Options {
			if strings.EqualFold(strings.TrimSpace(answer), strings.TrimSpace(option)) {
				q.Answers[i] = option
				break
			}
		}
		trimmed := strings.TrimSpace(answer)
		if len(trimmed) == 1 {
			index := int(strings.ToUpper(trimmed)[0]) - int('A')
			if index >= 0 && index < len(q.Options) {
				q.Answers[i] = q.Options[index]
			}
		}
	}
}
func (s *Service) PublishDraft(ctx context.Context, id string) (string, error) {
	return s.repository.PublishDraft(ctx, id)
}

func ValidateContent(c Content, publish bool) error {
	invalid := func(message string) error { return fmt.Errorf("%w: %s", ErrInvalidProfile, message) }
	if strings.TrimSpace(c.Title) == "" || len(c.Title) > 200 {
		return invalid("provide a title of up to 200 characters")
	}
	if c.Skill != "reading" && c.Skill != "listening" {
		return invalid("choose Reading or Listening")
	}
	if c.Purpose != "practice" && c.Purpose != "mock" {
		return invalid("choose practice or mock")
	}
	if c.AccessScope != "public" && c.AccessScope != "private" {
		return invalid("choose public or private access")
	}
	if (c.AccessScope == "private") != (strings.TrimSpace(c.OwnerUserID) != "") {
		return invalid("private content requires exactly one owner")
	}
	if c.Duration < 60 || c.Duration > 14400 {
		return invalid("duration must be 1–240 minutes")
	}
	if len(c.Sections) > 10 {
		return invalid("maximum 10 sections")
	}
	if !publish {
		return nil
	}
	count := 0
	for _, section := range c.Sections {
		if strings.TrimSpace(section.Title) == "" || len(section.Questions) == 0 {
			return invalid("each section needs a title and questions")
		}
		if c.Skill == "reading" && strings.TrimSpace(section.Passage) == "" {
			return invalid("Reading needs a passage")
		}
		if c.Skill == "listening" {
			u, e := url.Parse(section.AudioURL)
			managedPath := e == nil && u.Scheme == "" && u.Host == "" && privateMediaPath(u.Path)
			if e != nil || (!managedPath && (u.Scheme != "https" || u.Host == "")) {
				return invalid("Listening needs an HTTPS recording or managed audio")
			}
		}
		if len(section.SourceImageURLs) > 12 {
			return invalid("a section supports at most 12 source pages")
		}
		for _, rawURL := range section.SourceImageURLs {
			u, e := url.Parse(rawURL)
			managedPath := e == nil && u.Scheme == "" && u.Host == "" && privateMediaPath(u.Path)
			if !managedPath && (e != nil || u.Scheme != "https" || u.Host == "") {
				return invalid("source pages need HTTPS or managed media")
			}
		}
		for _, q := range section.Questions {
			if len(q.Answers) > 10 || len(q.Options) > 20 {
				return invalid("a question supports at most 10 accepted answers and 20 options")
			}
			if (q.Type == "multiple_choice" || q.Type == "multiple_select" || q.Type == "true_false_not_given" || q.Type == "yes_no_not_given") && len(q.Options) < 2 {
				return invalid("choice questions need at least two options")
			}
			if q.Type == "multiple_choice" && len(q.Answers) != 1 {
				return invalid("multiple choice questions require exactly one correct option")
			}
			count++
			if _, ok := questionTypes[q.Type]; !ok {
				return invalid("unsupported question type")
			}
			if strings.TrimSpace(q.Prompt) == "" || strings.TrimSpace(q.Explanation) == "" || len(q.Answers) == 0 {
				return invalid("every question needs a prompt, answer and explanation")
			}
			seen := map[string]bool{}
			for _, a := range q.Answers {
				if strings.TrimSpace(a) == "" || seen[a] {
					return invalid("answers must be nonempty and unique")
				}
				seen[a] = true
			}
			if len(q.Options) > 0 {
				for _, a := range q.Answers {
					found := false
					for _, o := range q.Options {
						if a == o {
							found = true
						}
					}
					if !found {
						return invalid("choice answers must match an option")
					}
				}
			}
		}
	}
	if count == 0 || count > 200 {
		return invalid("content needs 1–200 questions")
	}
	if c.Purpose == "mock" {
		if len(c.Bands) == 0 {
			return invalid("mocks require an explicit band conversion")
		}
		seenScores := map[int]bool{}
		seenBands := map[float64]bool{}
		zero := false
		for _, b := range c.Bands {
			if b.Minimum < 0 || b.Minimum > count || b.Band < 0 || b.Band > 9 || b.Band*2 != float64(int(b.Band*2)) || seenScores[b.Minimum] || seenBands[b.Band] {
				return invalid("invalid or duplicate band conversion")
			}
			seenScores[b.Minimum] = true
			seenBands[b.Band] = true
			if b.Minimum == 0 {
				zero = true
			}
		}
		if !zero {
			return invalid("band conversion must include a zero-score threshold")
		}
		bands := append([]ContentBand(nil), c.Bands...)
		sort.Slice(bands, func(i, j int) bool { return bands[i].Minimum < bands[j].Minimum })
		for i := 1; i < len(bands); i++ {
			if bands[i].Band <= bands[i-1].Band {
				return invalid("bands must increase as score thresholds increase")
			}
		}
	}
	return nil
}

func privateMediaPath(path string) bool {
	const prefix = "/api/v1/private-media/"
	if !strings.HasPrefix(path, prefix) {
		return false
	}
	id := strings.TrimPrefix(path, prefix)
	return filepath.Base(path) == id && len(id) == 36
}

// What may be stored. The transport layer sniffs the bytes and then asks this,
// so there is one list rather than two that have to be kept in step -- when
// there were two, a JPEG passed the sniff and was refused here.
var storableMedia = map[string]bool{"audio/mpeg": true, "image/png": true, "image/jpeg": true}

// StorableMedia reports whether a sniffed media type may be saved.
func StorableMedia(mime string) bool { return storableMedia[mime] }

func (s *Service) SavePrivateMedia(ctx context.Context, adminID, ownerID, filename, mime string, data []byte) (string, error) {
	filename = filepath.Base(strings.TrimSpace(filename))
	if filename == "" || filename == "." || !storableMedia[mime] || len(data) < 128 || len(data) > 25<<20 || (ownerID != "" && len(ownerID) != 36) {
		return "", ErrInvalidProfile
	}
	return s.repository.SavePrivateMedia(ctx, adminID, ownerID, filename, mime, data)
}

func (s *Service) GetPrivateMedia(ctx context.Context, userID, id string) (PrivateMedia, error) {
	return s.repository.GetPrivateMedia(ctx, userID, id)
}

func (s *Service) GetPrivateMediaForAdmin(ctx context.Context, id string) (PrivateMedia, error) {
	return s.repository.GetPrivateMediaForAdmin(ctx, id)
}
