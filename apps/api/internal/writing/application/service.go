package application

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

var ErrNotFound = errors.New("writing submission not found")
var ErrConflict = errors.New("draft changed or is no longer editable; reload before continuing")
var ErrInvalid = errors.New("provide a task prompt (20–4000 characters) and an essay of 50–1800 words for evaluation")
var ErrLimit = errors.New("daily limit reached: five evaluation requests per UTC day")
var ErrUnavailable = errors.New("writing evaluation is temporarily unavailable; your draft can still be saved")

type Submission struct {
	Reference     json.RawMessage `json:"-"`
	RunID         string          `json:"-"`
	ID            string          `json:"id"`
	ParentID      string          `json:"parent_id,omitempty"`
	Prompt        string          `json:"prompt"`
	Essay         string          `json:"essay"`
	Revision      int32           `json:"revision"`
	Status        string          `json:"status"`
	Report        json.RawMessage `json:"report,omitempty"`
	Model         string          `json:"-"`
	RubricVersion string          `json:"rubric_version"`
	ErrorCode     string          `json:"error_code"`
	CreatedAt     time.Time       `json:"created_at"`
	UpdatedAt     time.Time       `json:"updated_at"`
}
type Input struct {
	ContentID string `json:"content_id"`
	Prompt    string `json:"prompt"`
	Essay     string `json:"essay"`
	ParentID  string `json:"parent_id"`
	Revision  int32  `json:"revision"`
}

// Reference is a server-created snapshot, never accepted from student input.
type Reference struct {
	Task   string           `json:"task"`
	Images []ReferenceImage `json:"images,omitempty"`
}
type ReferenceImage struct {
	MIME string `json:"mime_type"`
	Data []byte `json:"data"`
}
type Repository interface {
	Create(context.Context, string, Input) (Submission, error)
	List(context.Context, string) ([]Submission, error)
	Get(context.Context, string, string) (Submission, error)
	Save(context.Context, string, string, Input) (Submission, error)
	Delete(context.Context, string, string) error
	Queue(context.Context, string, string, int32, string) (Submission, error)
	Available(context.Context) (bool, error)
	Claim(context.Context) (Submission, error)
	Finish(context.Context, string, string, []byte, string) error
	Expire(context.Context) error
}
type Evaluator interface {
	Evaluate(context.Context, Submission) ([]byte, error)
}
type Service struct {
	Repo      Repository
	Evaluator Evaluator
	Model     string
}

func New(r Repository, e Evaluator, model string) *Service { return &Service{r, e, model} }
func validDraft(i Input) bool {
	return len(i.Prompt) <= 4000 && len(i.Essay) <= 20000 && len(strings.Fields(i.Essay)) <= 1800
}
func (s *Service) Create(ctx context.Context, u string, i Input) (Submission, error) {
	if !validDraft(i) {
		return Submission{}, ErrInvalid
	}
	if i.ParentID != "" {
		p, e := s.Repo.Get(ctx, u, i.ParentID)
		if e != nil {
			return Submission{}, e
		}
		if p.Status != "completed" {
			return Submission{}, ErrConflict
		}
		i.Prompt = p.Prompt
	}
	return s.Repo.Create(ctx, u, i)
}
func (s *Service) Save(ctx context.Context, u, id string, i Input) (Submission, error) {
	if !validDraft(i) {
		return Submission{}, ErrInvalid
	}
	return s.Repo.Save(ctx, u, id, i)
}
func (s *Service) Submit(ctx context.Context, u, id string, revision int32, consent bool) (Submission, error) {
	if !consent {
		return Submission{}, errors.New("consent to process this essay and prompt for evaluation is required")
	}
	v, e := s.Repo.Get(ctx, u, id)
	if e != nil {
		return v, e
	}
	if v.Status == "queued" || v.Status == "processing" || v.Status == "completed" {
		return v, nil
	}
	if v.Revision != revision {
		return v, ErrConflict
	}
	if len(strings.TrimSpace(v.Prompt)) < 20 || len(strings.Fields(v.Essay)) < 50 || !validDraft(Input{Prompt: v.Prompt, Essay: v.Essay}) {
		return v, ErrInvalid
	}
	if s.Evaluator == nil {
		return v, ErrUnavailable
	}
	ready, e := s.Repo.Available(ctx)
	if e != nil {
		return v, e
	}
	if !ready {
		return v, ErrUnavailable
	}
	return s.Repo.Queue(ctx, u, id, revision, s.Model)
}
func (s *Service) Run(ctx context.Context) {
	if s.Evaluator == nil {
		return
	}
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_ = s.Repo.Expire(ctx)
			v, e := s.Repo.Claim(ctx)
			if e != nil {
				continue
			}
			job, cancel := context.WithTimeout(ctx, 100*time.Second)
			raw, e := s.Evaluator.Evaluate(job, v)
			cancel()
			code := ""
			var report []byte
			if e != nil {
				code = "provider_unavailable"
			} else {
				r, err := ParseReport(raw, v.Essay)
				if err != nil {
					code = "invalid_feedback"
				} else {
					report, _ = json.Marshal(r)
				}
			}
			save, done := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			_ = s.Repo.Finish(save, v.ID, v.RunID, report, code)
			done()
		}
	}
}
