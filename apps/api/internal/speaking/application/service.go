package application

import (
	"context"
	"encoding/json"
	d "github.com/own/mock-ai/apps/api/internal/speaking/domain"
	"log/slog"
	"time"
)

type Repository interface {
	Available(context.Context) (bool, error)
	Plan(context.Context, string, string) (string, []d.Turn, error)
	Create(context.Context, string, string, d.Examiner, string, []d.Turn) (d.Session, error)
	Get(context.Context, string, string) (d.Session, error)
	List(context.Context, string) ([]d.Session, error)
	Answer(context.Context, string, string, int32, []byte, bool) (d.Session, error)
	Queue(context.Context, string, string) (d.Session, error)
	RetryVoice(context.Context, string, string, int32) error
	Delete(context.Context, string, string) error
	Audio(context.Context, string, string, int32, bool) ([]byte, error)
	ClaimVoice(context.Context) (d.VoiceJob, error)
	FinishVoice(context.Context, d.VoiceJob, []byte) error
	ClaimReport(context.Context) (d.Session, error)
	FinishReport(context.Context, d.Session, []byte, string) error
	Cleanup(context.Context) error
}
type Provider interface {
	Speak(context.Context, d.VoiceJob) ([]byte, error)
	Evaluate(context.Context, d.Session) ([]byte, error)
}
type Service struct {
	Repo     Repository
	Provider Provider
	Model    string
	Log      *slog.Logger
}

func (s *Service) Create(ctx context.Context, u string, consent bool, planID string, part int32) (d.Session, error) {
	if !consent {
		return d.Session{}, d.ErrConsent
	}
	ready, e := s.Repo.Available(ctx)
	if e != nil {
		return d.Session{}, e
	}
	if !ready || s.Provider == nil {
		return d.Session{}, d.ErrUnavailable
	}
	ex, e := d.RandomExaminer()
	if e != nil {
		return d.Session{}, e
	}
	topic, turns, e := s.Repo.Plan(ctx, u, planID)
	if e != nil {
		return d.Session{}, e
	}
	topic, turns, e = d.FocusPlan(topic, turns, part)
	if e != nil {
		return d.Session{}, e
	}
	return s.Repo.Create(ctx, u, s.Model, ex, topic, turns)
}
func (s *Service) Run(ctx context.Context) {
	if s.Provider != nil {
		go s.worker(ctx, true)
		go s.worker(ctx, true)
	}
	if repo, ok := s.Repo.(EvaluationRepository); ok {
		if provider, ok := s.Provider.(AggregateProvider); ok {
			for i := 0; i < 3; i++ {
				go s.evaluationWorker(ctx, repo, provider)
			}
			return
		}
	}
	go s.worker(ctx, false)
}
func (s *Service) worker(ctx context.Context, voice bool) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	lastClean := time.Time{}
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if !voice && time.Since(lastClean) > time.Minute {
				if e := s.Repo.Cleanup(ctx); e != nil {
					s.Log.Warn("speaking cleanup failed")
				}
				lastClean = time.Now()
			}
			if s.Provider == nil {
				continue
			}
			if voice {
				j, e := s.Repo.ClaimVoice(ctx)
				if e != nil {
					continue
				}
				work, cancel := context.WithTimeout(ctx, 100*time.Second)
				audio, e := s.Provider.Speak(work, j)
				cancel()
				if e != nil {
					audio = nil
					s.Log.Warn("speaking voice generation failed", "session", j.SessionID)
				}
				finish, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
				if e = s.Repo.FinishVoice(finish, j, audio); e != nil {
					s.Log.Warn("speaking voice persistence failed")
				}
				cancel()
			} else {
				v, e := s.Repo.ClaimReport(ctx)
				if e != nil {
					continue
				}
				work, cancel := context.WithTimeout(ctx, 180*time.Second)
				raw, e := s.Provider.Evaluate(work, v)
				cancel()
				code := ""
				var report []byte
				if e != nil {
					code = "provider_unavailable"
				} else {
					parsed, err := d.ParseReport(raw, v)
					if err != nil {
						code = "invalid_feedback"
						s.Log.Warn("speaking report validation failed", "session_id", v.ID, "reason", err.Error())
					} else {
						report, _ = json.Marshal(parsed)
					}
				}
				finish, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
				if e = s.Repo.FinishReport(finish, v, report, code); e != nil {
					s.Log.Warn("speaking report persistence failed")
				}
				cancel()
			}
		}
	}
}
