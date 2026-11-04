package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/own/mock-ai/apps/api/internal/platform/keyvault"
	db "github.com/own/mock-ai/apps/api/internal/platform/postgres/sqlc"
	d "github.com/own/mock-ai/apps/api/internal/speaking/domain"
	"time"
)

type Repository struct {
	Pool   *pgxpool.Pool
	Q      *db.Queries
	Master string
}

func New(p *pgxpool.Pool, master string) *Repository    { return &Repository{p, db.New(p), master} }
func recordingBinding(id string, position int32) string { return d.RecordingBinding(id, position) }
func uuid(v string) pgtype.UUID                         { var id pgtype.UUID; _ = id.Scan(v); return id }
func str(v pgtype.UUID) string {
	if !v.Valid {
		return ""
	}
	s, _ := v.Value()
	return s.(string)
}
func row(v db.SpeakingSession) d.Session {
	var ex d.Examiner
	_ = json.Unmarshal(v.Examiner, &ex)
	return d.Session{Mode: v.Mode, LiveModel: v.LiveModel, LiveRunID: str(v.LiveRunID), ID: str(v.ID), UserID: str(v.UserID), RunID: str(v.RunID), Examiner: ex, Topic: v.Topic, Status: v.Status, Report: v.Report, Model: v.Model, RubricVersion: v.RubricVersion, ErrorCode: v.ErrorCode, CreatedAt: v.CreatedAt.Time, AudioExpiresAt: v.AudioExpiresAt.Time, Turns: []d.Turn{}}
}
func turn(v db.SpeakingTurnsRow) d.Turn {
	return d.Turn{Transcript: v.LiveTranscript, Position: v.Position, Part: v.Part, Prompt: v.Prompt, Seconds: v.Seconds, PrepSeconds: v.PrepSeconds, VoiceStatus: v.VoiceStatus, VoiceAttempts: v.VoiceAttempts, DurationMS: v.DurationMs, Answered: v.Answered, Skipped: v.Skipped, HasRecording: v.HasRecording}
}
func errRow(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return d.ErrNotFound
	}
	return err
}
func (r *Repository) Available(ctx context.Context) (bool, error) {
	if r.Master == "" {
		return false, nil
	}
	return r.Q.WritingKeyAvailable(ctx)
}
func (r *Repository) Plan(ctx context.Context, u, id string) (string, []d.Turn, error) {
	if id == "" {
		v, e := r.Q.RandomSpeakingPlan(ctx, uuid(u))
		if e != nil {
			return "", nil, d.ErrUnavailable
		}
		return d.ParsePlan(v.Title, v.Data)
	}
	v, e := r.Q.PublishedSpeakingPlan(ctx, db.PublishedSpeakingPlanParams{ID: uuid(id), UserID: uuid(u)})
	if e != nil {
		return "", nil, d.ErrUnavailable
	}
	return d.ParsePlan(v.Title, v.Data)
}
func (r *Repository) Get(ctx context.Context, u, id string) (d.Session, error) {
	v, e := r.Q.GetSpeaking(ctx, db.GetSpeakingParams{UserID: uuid(u), ID: uuid(id)})
	if e != nil {
		return d.Session{}, errRow(e)
	}
	s := row(v)
	turns, e := r.Q.SpeakingTurns(ctx, v.ID)
	for _, t := range turns {
		s.Turns = append(s.Turns, turn(t))
	}
	if e == nil && (s.Status == "queued" || s.Status == "processing") {
		jobs, err := r.Q.ListSpeakingEvaluation(ctx, v.ID)
		if err != nil {
			return s, err
		}
		s.Evaluation = &d.EvaluationProgress{}
		for _, j := range jobs {
			if j.Part > 0 {
				s.Evaluation.Total++
				if j.Status == "completed" {
					s.Evaluation.Completed++
				}
			}
		}
	}
	return s, e
}
func (r *Repository) List(ctx context.Context, u string) ([]d.Session, error) {
	rows, e := r.Q.ListSpeaking(ctx, uuid(u))
	out := []d.Session{}
	for _, v := range rows {
		out = append(out, row(v))
	}
	return out, e
}
func (r *Repository) Create(ctx context.Context, u, model string, ex d.Examiner, topic string, turns []d.Turn) (d.Session, error) {
	return r.create(ctx, u, model, "", ex, topic, turns)
}
func (r *Repository) CreateLive(ctx context.Context, u, model, liveModel string, ex d.Examiner, topic string, turns []d.Turn) (d.Session, error) {
	return r.create(ctx, u, model, liveModel, ex, topic, turns)
}
func (r *Repository) create(ctx context.Context, u, model, liveModel string, ex d.Examiner, topic string, turns []d.Turn) (d.Session, error) {
	tx, e := r.Pool.Begin(ctx)
	if e != nil {
		return d.Session{}, e
	}
	defer tx.Rollback(ctx)
	q := r.Q.WithTx(tx)
	uid := uuid(u)
	if e = q.LockSpeakingUser(ctx, u); e != nil {
		return d.Session{}, e
	}
	list, e := q.ListSpeaking(ctx, uid)
	if e != nil {
		return d.Session{}, e
	}
	for _, v := range list {
		if v.Status == "active" || v.Status == "queued" || v.Status == "processing" {
			return d.Session{}, d.ErrConflict
		}
	}
	n, e := q.CountSpeakingRequests(ctx, db.CountSpeakingRequestsParams{UserID: uid, Kind: "start"})
	if e != nil {
		return d.Session{}, e
	}
	if n >= 3 {
		return d.Session{}, d.ErrLimit
	}
	encoded, _ := json.Marshal(ex)
	v, e := q.CreateSpeaking(ctx, db.CreateSpeakingParams{UserID: uid, Examiner: encoded, Topic: topic, Model: model})
	if e != nil {
		return d.Session{}, e
	}
	for _, t := range turns {
		if e = q.CreateSpeakingTurn(ctx, db.CreateSpeakingTurnParams{SessionID: v.ID, Position: t.Position, Part: t.Part, Prompt: t.Prompt, Seconds: t.Seconds, PrepSeconds: t.PrepSeconds}); e != nil {
			return d.Session{}, e
		}
	}
	if e = q.RecordSpeakingRequest(ctx, db.RecordSpeakingRequestParams{UserID: uid, Kind: "start"}); e != nil {
		return d.Session{}, e
	}
	if liveModel != "" {
		if e = q.SetSpeakingLive(ctx, db.SetSpeakingLiveParams{ID: v.ID, LiveModel: liveModel}); e != nil {
			return d.Session{}, e
		}
		if e = q.PrepareSpeakingLive(ctx, v.ID); e != nil {
			return d.Session{}, e
		}
	}
	if e = tx.Commit(ctx); e != nil {
		return d.Session{}, e
	}
	return r.Get(ctx, u, str(v.ID))
}
func (r *Repository) Answer(ctx context.Context, u, id string, pos int32, audio []byte, skip bool) (d.Session, error) {
	tx, e := r.Pool.Begin(ctx)
	if e != nil {
		return d.Session{}, e
	}
	defer tx.Rollback(ctx)
	q := r.Q.WithTx(tx)
	s, e := q.LockSpeaking(ctx, db.LockSpeakingParams{UserID: uuid(u), ID: uuid(id)})
	if e != nil {
		return d.Session{}, errRow(e)
	}
	if s.Mode == "live" || s.Status != "active" || time.Now().After(s.AudioExpiresAt.Time) {
		return d.Session{}, d.ErrConflict
	}
	turns, e := q.SpeakingTurns(ctx, s.ID)
	if e != nil {
		return d.Session{}, e
	}
	current := int32(len(turns))
	for _, t := range turns {
		if !t.Answered {
			current = t.Position
			break
		}
	}
	if current != pos || pos < 0 || int(pos) >= len(turns) {
		return d.Session{}, d.ErrConflict
	}
	var sealed []byte
	duration := int32(0)
	if !skip {
		duration, e = d.ValidateAudio(audio, turns[pos].Seconds)
		if e != nil {
			return d.Session{}, e
		}
		sealed, e = keyvault.SealBound(r.Master, string(audio), recordingBinding(str(s.ID), pos))
		if e != nil {
			return d.Session{}, e
		}
	}
	n, e := q.AnswerSpeaking(ctx, db.AnswerSpeakingParams{SessionID: s.ID, Position: pos, Recording: sealed, DurationMs: duration, Skipped: skip})
	if e != nil {
		return d.Session{}, e
	}
	if n != 1 {
		return d.Session{}, d.ErrConflict
	}
	if _, e = q.QueueSpeakingVoice(ctx, db.QueueSpeakingVoiceParams{SessionID: s.ID, Position: pos + 1}); e != nil {
		return d.Session{}, e
	}
	if e = tx.Commit(ctx); e != nil {
		return d.Session{}, e
	}
	return r.Get(ctx, u, id)
}
func (r *Repository) Queue(ctx context.Context, u, id string) (d.Session, error) {
	tx, e := r.Pool.Begin(ctx)
	if e != nil {
		return d.Session{}, e
	}
	defer tx.Rollback(ctx)
	q := r.Q.WithTx(tx)
	if e = q.LockSpeakingUser(ctx, u); e != nil {
		return d.Session{}, e
	}
	s, e := q.LockSpeaking(ctx, db.LockSpeakingParams{UserID: uuid(u), ID: uuid(id)})
	if e != nil {
		return d.Session{}, errRow(e)
	}
	if s.Status == "queued" || s.Status == "processing" || s.Status == "completed" {
		// Release the transaction connection before the independent detail read.
		// Concurrent duplicate submissions may otherwise exhaust the pool while
		// waiting for this transaction's per-user lock.
		if e = tx.Commit(ctx); e != nil {
			return d.Session{}, e
		}
		return r.Get(ctx, u, id)
	}
	if time.Now().After(s.AudioExpiresAt.Time) || (s.Status != "active" && s.Status != "failed") {
		return d.Session{}, d.ErrConflict
	}
	turns, e := q.SpeakingTurns(ctx, s.ID)
	if e != nil {
		return d.Session{}, e
	}
	if len(turns) == 0 {
		return d.Session{}, d.ErrConflict
	}
	for _, t := range turns {
		if !t.Answered {
			return d.Session{}, d.ErrConflict
		}
	}
	list, e := q.ListSpeaking(ctx, s.UserID)
	if e != nil {
		return d.Session{}, e
	}
	for _, v := range list {
		if v.ID != s.ID && (v.Status == "active" || v.Status == "queued" || v.Status == "processing") {
			return d.Session{}, d.ErrConflict
		}
	}
	n, e := q.CountSpeakingRequests(ctx, db.CountSpeakingRequestsParams{UserID: s.UserID, Kind: "evaluate"})
	if e != nil {
		return d.Session{}, e
	}
	if n >= 5 {
		return d.Session{}, d.ErrLimit
	}
	if _, e = q.QueueSpeakingReport(ctx, s.ID); e != nil {
		return d.Session{}, e
	}
	if e = q.QueueSpeakingEvaluation(ctx, s.ID); e != nil {
		return d.Session{}, e
	}
	if e = q.RecordSpeakingRequest(ctx, db.RecordSpeakingRequestParams{UserID: s.UserID, Kind: "evaluate"}); e != nil {
		return d.Session{}, e
	}
	if e = tx.Commit(ctx); e != nil {
		return d.Session{}, e
	}
	return r.Get(ctx, u, id)
}
func (r *Repository) RetryVoice(ctx context.Context, u, id string, pos int32) error {
	s, e := r.Get(ctx, u, id)
	if e != nil {
		return e
	}
	if s.Status != "active" {
		return d.ErrConflict
	}
	for _, t := range s.Turns {
		if !t.Answered {
			if t.Position != pos || t.VoiceStatus != "failed" {
				return d.ErrConflict
			}
			n, e := r.Q.QueueSpeakingVoice(ctx, db.QueueSpeakingVoiceParams{SessionID: uuid(id), Position: pos})
			if e != nil {
				return e
			}
			if n != 1 {
				return d.ErrLimit
			}
			return nil
		}
	}
	return d.ErrConflict
}
func (r *Repository) Delete(ctx context.Context, u, id string) error {
	n, e := r.Q.DeleteSpeaking(ctx, db.DeleteSpeakingParams{UserID: uuid(u), ID: uuid(id)})
	if e != nil {
		return e
	}
	if n != 1 {
		return d.ErrNotFound
	}
	return nil
}
func (r *Repository) Audio(ctx context.Context, u, id string, pos int32, examiner bool) ([]byte, error) {
	v, e := r.Q.SpeakingAudio(ctx, db.SpeakingAudioParams{UserID: uuid(u), ID: uuid(id), Position: pos})
	if e != nil {
		return nil, errRow(e)
	}
	if examiner {
		if len(v.ExaminerAudio) == 0 {
			return nil, d.ErrNotFound
		}
		return v.ExaminerAudio, nil
	}
	if len(v.Recording) == 0 {
		return nil, d.ErrNotFound
	}
	plain, e := keyvault.OpenBound(r.Master, v.Recording, recordingBinding(str(uuid(id)), pos))
	return []byte(plain), e
}
func (r *Repository) ClaimVoice(ctx context.Context) (d.VoiceJob, error) {
	v, e := r.Q.ClaimSpeakingVoice(ctx)
	var ex d.Examiner
	_ = json.Unmarshal(v.Examiner, &ex)
	return d.VoiceJob{SessionID: str(v.SessionID), Position: v.Position, RunID: str(v.VoiceRunID), Prompt: v.Prompt, Examiner: ex}, e
}
func (r *Repository) FinishVoice(ctx context.Context, j d.VoiceJob, audio []byte) error {
	status := "ready"
	if len(audio) == 0 {
		status = "failed"
	}
	_, e := r.Q.FinishSpeakingVoice(ctx, db.FinishSpeakingVoiceParams{SessionID: uuid(j.SessionID), Position: j.Position, VoiceRunID: uuid(j.RunID), VoiceStatus: status, ExaminerAudio: audio})
	return e
}
func (r *Repository) ClaimReport(ctx context.Context) (d.Session, error) {
	v, e := r.Q.ClaimSpeakingReport(ctx)
	if e != nil {
		return d.Session{}, e
	}
	s, e := r.Get(ctx, str(v.UserID), str(v.ID))
	if e != nil {
		return s, e
	}
	rows, e := r.Q.SpeakingRecordings(ctx, v.ID)
	if e != nil {
		return s, e
	}
	for _, a := range rows {
		if len(a.Recording) > 0 {
			plain, err := keyvault.OpenBound(r.Master, a.Recording, recordingBinding(s.ID, a.Position))
			if err != nil {
				return s, err
			}
			s.Turns[a.Position].Recording = []byte(plain)
		}
	}
	return s, nil
}
func (r *Repository) FinishReport(ctx context.Context, s d.Session, report []byte, code string) error {
	status := "completed"
	if code != "" {
		status = "failed"
	}
	_, e := r.Q.FinishSpeakingReport(ctx, db.FinishSpeakingReportParams{ID: uuid(s.ID), RunID: uuid(s.RunID), Status: status, Report: report, ErrorCode: code})
	return e
}
func (r *Repository) Cleanup(ctx context.Context) error {
	for _, f := range []func(context.Context) error{r.Q.ExpireSpeakingVoices, r.Q.ExpireSpeakingReports, r.Q.ExpireSpeakingSessions, r.Q.PurgeSpeakingAudio} {
		if e := f(ctx); e != nil {
			return e
		}
	}
	return nil
}

// AIEnabled reports whether this student's work may reach a model.
func (r *Repository) AIEnabled(ctx context.Context, userID string) (bool, error) {
	return r.Q.StudentAIEnabled(ctx, uuid(userID))
}
