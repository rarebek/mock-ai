package postgres

import (
	"context"
	"errors"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	db "github.com/own/mock-ai/apps/api/internal/platform/postgres/sqlc"
	app "github.com/own/mock-ai/apps/api/internal/writing/application"
)

type Repository struct {
	pool *pgxpool.Pool
	q    *db.Queries
}

func New(p *pgxpool.Pool) *Repository { return &Repository{p, db.New(p)} }
func id(s string) (pgtype.UUID, error) {
	var v pgtype.UUID
	if s == "" {
		return v, nil
	}
	e := v.Scan(s)
	return v, e
}
func str(v pgtype.UUID) string {
	if !v.Valid {
		return ""
	}
	s, _ := v.Value()
	return s.(string)
}
func mapRow(v db.WritingSubmission) app.Submission {
	return app.Submission{Reference: v.Reference, RunID: str(v.RunID), ID: str(v.ID), ParentID: str(v.ParentID), Prompt: v.Prompt, Essay: v.Essay, Revision: v.Revision, Status: v.Status, Report: v.Report, Model: v.Model, RubricVersion: v.RubricVersion, ErrorCode: v.ErrorCode, CreatedAt: v.CreatedAt.Time, UpdatedAt: v.UpdatedAt.Time}
}
func result(v db.WritingSubmission, e error) (app.Submission, error) {
	if errors.Is(e, pgx.ErrNoRows) {
		return app.Submission{}, app.ErrNotFound
	}
	return mapRow(v), e
}
func (r *Repository) Create(ctx context.Context, u string, i app.Input) (app.Submission, error) {
	uid, e := id(u)
	if e != nil {
		return app.Submission{}, app.ErrNotFound
	}
	parent, e := id(i.ParentID)
	if e != nil {
		return app.Submission{}, app.ErrNotFound
	}
	var reference []byte
	if i.ParentID != "" {
		previous, err := r.Get(ctx, u, i.ParentID)
		if err != nil {
			return app.Submission{}, err
		}
		reference = previous.Reference
	} else {
		reference, e = r.reference(ctx, uid, i)
		if e != nil {
			return app.Submission{}, e
		}
	}
	return result(r.q.CreateWriting(ctx, db.CreateWritingParams{UserID: uid, ParentID: parent, Prompt: i.Prompt, Essay: i.Essay, Reference: reference}))
}
func (r *Repository) Get(ctx context.Context, u, s string) (app.Submission, error) {
	uid, e := id(u)
	if e != nil {
		return app.Submission{}, app.ErrNotFound
	}
	sid, e := id(s)
	if e != nil {
		return app.Submission{}, app.ErrNotFound
	}
	return result(r.q.GetWriting(ctx, db.GetWritingParams{UserID: uid, ID: sid}))
}
func (r *Repository) List(ctx context.Context, u string) ([]app.Submission, error) {
	uid, e := id(u)
	if e != nil {
		return nil, app.ErrNotFound
	}
	rows, e := r.q.ListWriting(ctx, uid)
	out := make([]app.Submission, 0, len(rows))
	for _, v := range rows {
		out = append(out, mapRow(v))
	}
	return out, e
}
func (r *Repository) Save(ctx context.Context, u, s string, i app.Input) (app.Submission, error) {
	uid, e := id(u)
	if e != nil {
		return app.Submission{}, app.ErrNotFound
	}
	sid, e := id(s)
	if e != nil {
		return app.Submission{}, app.ErrNotFound
	}
	previous, e := r.Get(ctx, u, s)
	if e != nil {
		return app.Submission{}, e
	}
	reference := previous.Reference
	if previous.Prompt != i.Prompt || (i.ContentID != "" && string(reference) == "{}") {
		reference, e = r.reference(ctx, uid, i)
		if e != nil {
			return app.Submission{}, e
		}
	}
	v, e := r.q.SaveWriting(ctx, db.SaveWritingParams{UserID: uid, ID: sid, Prompt: i.Prompt, Essay: i.Essay, Revision: i.Revision, Reference: reference})
	if errors.Is(e, pgx.ErrNoRows) {
		_, err := r.Get(ctx, u, s)
		if err != nil {
			return app.Submission{}, err
		}
		return app.Submission{}, app.ErrConflict
	}
	return result(v, e)
}
func (r *Repository) Delete(ctx context.Context, u, s string) error {
	v, e := r.Get(ctx, u, s)
	if e != nil {
		return e
	}
	uid, _ := id(u)
	sid, _ := id(v.ID)
	n, e := r.q.DeleteWriting(ctx, db.DeleteWritingParams{UserID: uid, ID: sid})
	if e == nil && n == 0 {
		return app.ErrConflict
	}
	return e
}
func (r *Repository) Available(ctx context.Context) (bool, error) {
	return r.q.WritingKeyAvailable(ctx)
}
func (r *Repository) Queue(ctx context.Context, u, s string, revision int32, model string) (app.Submission, error) {
	uid, e := id(u)
	if e != nil {
		return app.Submission{}, app.ErrNotFound
	}
	sid, e := id(s)
	if e != nil {
		return app.Submission{}, app.ErrNotFound
	}
	tx, e := r.pool.Begin(ctx)
	if e != nil {
		return app.Submission{}, e
	}
	defer tx.Rollback(ctx)
	q := r.q.WithTx(tx)
	if e = q.LockWritingUser(ctx, u); e != nil {
		return app.Submission{}, e
	}
	current, e := q.GetWriting(ctx, db.GetWritingParams{UserID: uid, ID: sid})
	if e != nil {
		return result(current, e)
	}
	if current.Status == "queued" || current.Status == "processing" || current.Status == "completed" {
		return mapRow(current), nil
	}
	n, e := q.CountWritingToday(ctx, uid)
	if e != nil {
		return app.Submission{}, e
	}
	if n >= 5 {
		return app.Submission{}, app.ErrLimit
	}
	v, e := q.QueueWriting(ctx, db.QueueWritingParams{UserID: uid, ID: sid, Revision: revision, Model: model, RubricVersion: app.RubricVersion})
	if errors.Is(e, pgx.ErrNoRows) {
		return app.Submission{}, app.ErrConflict
	}
	if e != nil {
		return app.Submission{}, e
	}
	if e = q.RecordWritingRequest(ctx, db.RecordWritingRequestParams{UserID: uid, SubmissionID: sid}); e != nil {
		return app.Submission{}, e
	}
	if e = tx.Commit(ctx); e != nil {
		return app.Submission{}, e
	}
	return mapRow(v), nil
}
func (r *Repository) Claim(ctx context.Context) (app.Submission, error) {
	return result(r.q.ClaimWriting(ctx))
}
func (r *Repository) Expire(ctx context.Context) error { return r.q.ExpireWritingJobs(ctx) }
func (r *Repository) Finish(ctx context.Context, s, run string, report []byte, code string) error {
	sid, e := id(s)
	if e != nil {
		return e
	}
	status := "completed"
	if code != "" {
		status = "failed"
	}
	runID, e := id(run)
	if e != nil {
		return e
	}
	n, e := r.q.FinishWriting(ctx, db.FinishWritingParams{ID: sid, RunID: runID, Status: status, Report: report, ErrorCode: code})
	if e == nil && n == 0 {
		return app.ErrConflict
	}
	return e
}

// AIEnabled reports whether this student's work may be sent to a model. A
// student with no centre is a direct registrant, on the platform's own terms.
func (r *Repository) AIEnabled(ctx context.Context, userID string) (bool, error) {
	uid, e := id(userID)
	if e != nil {
		return false, app.ErrNotFound
	}
	return r.q.StudentAIEnabled(ctx, uid)
}
