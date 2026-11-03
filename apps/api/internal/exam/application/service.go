package application

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"
)

var (
	ErrNotFound       = errors.New("exam or attempt not found")
	ErrAttemptClosed  = errors.New("attempt is closed or its time has expired")
	ErrInvalidAnswer  = errors.New("answer must contain between one and ten values")
	ErrInvalidProfile = errors.New("study profile values are invalid")
)

var questionTypes = map[string]struct{}{
	"multiple_choice": {}, "multiple_select": {}, "true_false_not_given": {},
	"yes_no_not_given": {}, "short_answer": {}, "sentence_completion": {},
	"summary_completion": {}, "matching": {}, "map_labeling": {},
}

type Repository interface {
	LearningRepository
	ListPublishedContent(ctx context.Context, u, kind string) ([]ManagedContent, error)
	ListExams(ctx context.Context, userID string) ([]ExamSummary, error)
	GetExam(ctx context.Context, userID, examID string) (Exam, error)
	StartAttempt(ctx context.Context, userID, versionID string) (Attempt, error)
	GetAttempt(ctx context.Context, userID, attemptID string) (Attempt, error)
	SaveAnswer(ctx context.Context, userID, attemptID, questionID string, values []string) (SavedAnswer, error)
	SubmitAttempt(ctx context.Context, userID, attemptID string) (Attempt, error)
	ListHistory(ctx context.Context, userID string, limit, offset int32) ([]HistoryItem, error)
	GetProgress(ctx context.Context, userID string) ([]SkillProgress, error)
	ListPracticeQuestions(ctx context.Context, userID, skill, questionType string, limit int32) ([]PracticeQuestion, error)
	CheckPracticeAnswer(ctx context.Context, userID, questionID string, values []string) (PracticeFeedback, error)
	GetAttemptReview(ctx context.Context, userID, attemptID string) (AttemptReview, error)
	GetStudyPlan(ctx context.Context, userID string) (StudyPlan, error)
	UpdateStudyProfile(ctx context.Context, userID string, input StudyProfileInput) (StudyProfile, error)
	MarkAttemptReviewed(ctx context.Context, userID, attemptID string) error
}

func (s *Service) GetProgress(ctx context.Context, userID string) ([]SkillProgress, error) {
	return s.repository.GetProgress(ctx, userID)
}

func (s *Service) ListPracticeQuestions(ctx context.Context, userID, skill, questionType string, limit int32) ([]PracticeQuestion, error) {
	if skill != "" && skill != "reading" && skill != "listening" {
		return nil, ErrNotFound
	}
	if questionType != "" {
		if _, ok := questionTypes[questionType]; !ok {
			return nil, ErrNotFound
		}
	}
	if limit <= 0 || limit > 10 {
		limit = 5
	}
	return s.repository.ListPracticeQuestions(ctx, userID, skill, questionType, limit)
}

func (s *Service) CheckPracticeAnswer(ctx context.Context, userID, questionID string, values []string) (PracticeFeedback, error) {
	cleaned, err := cleanAnswers(values)
	if err != nil {
		return PracticeFeedback{}, err
	}
	return s.repository.CheckPracticeAnswer(ctx, userID, questionID, cleaned)
}

func (s *Service) GetStudyPlan(ctx context.Context, userID string) (StudyPlan, error) {
	return s.repository.GetStudyPlan(ctx, userID)
}

func (s *Service) UpdateStudyProfile(ctx context.Context, userID string, input StudyProfileInput) (StudyProfile, error) {
	if input.TargetBand < 4 || input.TargetBand > 9 || input.TargetBand*2 != float64(int(input.TargetBand*2)) {
		return StudyProfile{}, ErrInvalidProfile
	}
	if input.Track != "academic" && input.Track != "general_training" {
		return StudyProfile{}, ErrInvalidProfile
	}
	if input.DailyGoalMinutes < 10 || input.DailyGoalMinutes > 180 {
		return StudyProfile{}, ErrInvalidProfile
	}
	if input.ExamDate != "" {
		date, err := time.Parse("2006-01-02", input.ExamDate)
		if err != nil || date.Before(time.Now().UTC().Truncate(24*time.Hour)) {
			return StudyProfile{}, ErrInvalidProfile
		}
	}
	return s.repository.UpdateStudyProfile(ctx, userID, input)
}

func (s *Service) GetAttemptReview(ctx context.Context, userID, attemptID string) (AttemptReview, error) {
	return s.repository.GetAttemptReview(ctx, userID, attemptID)
}

func (s *Service) MarkAttemptReviewed(ctx context.Context, userID, attemptID string) error {
	return s.repository.MarkAttemptReviewed(ctx, userID, attemptID)
}

type Service struct {
	repository  Repository
	importer    ImportProvider
	importModel string
	log         *slog.Logger
}

func NewService(repository Repository) *Service { return &Service{repository: repository} }

func (s *Service) ConfigureImporter(provider ImportProvider, model string, logger ...*slog.Logger) {
	s.importer, s.importModel = provider, model
	if len(logger) > 0 {
		s.log = logger[0]
	}
}

func (s *Service) ListExams(ctx context.Context, userID string) ([]ExamSummary, error) {
	return s.repository.ListExams(ctx, userID)
}

func (s *Service) GetExam(ctx context.Context, userID, examID string) (Exam, error) {
	return s.repository.GetExam(ctx, userID, examID)
}

func (s *Service) StartAttempt(ctx context.Context, userID, examID string) (Attempt, error) {
	exam, err := s.repository.GetExam(ctx, userID, examID)
	if err != nil {
		return Attempt{}, err
	}
	return s.repository.StartAttempt(ctx, userID, exam.VersionID)
}

func (s *Service) GetAttempt(ctx context.Context, userID, attemptID string) (Attempt, error) {
	return s.repository.GetAttempt(ctx, userID, attemptID)
}

func (s *Service) SaveAnswer(ctx context.Context, userID, attemptID, questionID string, values []string) (SavedAnswer, error) {
	cleaned, err := cleanAnswers(values)
	if err != nil {
		return SavedAnswer{}, err
	}
	return s.repository.SaveAnswer(ctx, userID, attemptID, questionID, cleaned)
}

func cleanAnswers(values []string) ([]string, error) {
	if len(values) == 0 || len(values) > 10 {
		return nil, ErrInvalidAnswer
	}
	cleaned := make([]string, len(values))
	for i, value := range values {
		cleaned[i] = strings.TrimSpace(value)
		if cleaned[i] == "" || len(cleaned[i]) > 500 {
			return nil, ErrInvalidAnswer
		}
	}
	return cleaned, nil
}

func (s *Service) SubmitAttempt(ctx context.Context, userID, attemptID string) (Attempt, error) {
	return s.repository.SubmitAttempt(ctx, userID, attemptID)
}

func (s *Service) ListHistory(ctx context.Context, userID string, limit, offset int32) ([]HistoryItem, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	if offset < 0 {
		offset = 0
	}
	return s.repository.ListHistory(ctx, userID, limit, offset)
}
