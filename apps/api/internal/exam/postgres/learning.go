package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	app "github.com/own/mock-ai/apps/api/internal/exam/application"
	db "github.com/own/mock-ai/apps/api/internal/platform/postgres/sqlc"
)

func text(s string) pgtype.Text { return pgtype.Text{String: s, Valid: s != ""} }

func learningError(e error) error {
	if errors.Is(e, pgx.ErrNoRows) {
		return app.ErrNotFound
	}
	return e
}
func (r *Repository) GetWorkspace(ctx context.Context, u, a string) (app.Workspace, error) {
	p, e := attemptParams(u, a)
	if e != nil {
		return app.Workspace{}, app.ErrNotFound
	}
	b, e := r.queries.GetWorkspace(ctx, db.GetWorkspaceParams{AttemptID: p.ID, UserID: p.UserID})
	if e != nil {
		return app.Workspace{}, learningError(e)
	}
	var w app.Workspace
	e = json.Unmarshal(b, &w)
	return w, e
}
func (r *Repository) SaveWorkspace(ctx context.Context, u, a string, w app.Workspace) error {
	p, e := attemptParams(u, a)
	if e != nil {
		return app.ErrNotFound
	}
	b, e := json.Marshal(w)
	if e != nil {
		return e
	}
	_, e = r.queries.SaveWorkspace(ctx, db.SaveWorkspaceParams{AttemptID: p.ID, UserID: p.UserID, Data: b})
	return learningError(e)
}
func (r *Repository) ListMistakes(ctx context.Context, u string) ([]app.Mistake, error) {
	uid, e := parseUUID(u)
	if e != nil {
		return nil, app.ErrNotFound
	}
	rows, e := r.queries.ListMistakes(ctx, uid)
	if e != nil {
		return nil, e
	}
	out := make([]app.Mistake, 0, len(rows))
	for _, row := range rows {
		options, e := r.queries.ListQuestionOptions(ctx, row.ID)
		if e != nil {
			return nil, e
		}
		opts := make([]app.Option, 0, len(options))
		for _, o := range options {
			opts = append(opts, app.Option{ID: uuidString(o.ID), Position: o.Position, Label: o.Label, Value: o.Value})
		}
		missed, _ := row.LastMissed.(time.Time)
		out = append(out, app.Mistake{PracticeQuestion: app.PracticeQuestion{ID: uuidString(row.ID), Type: string(row.Type), Prompt: row.Prompt, Options: opts, Passage: textString(row.Passage), AudioURL: textString(row.AudioUrl), SectionTitle: row.SectionTitle, Skill: string(row.Skill)}, Resolved: row.Resolved, LastMissed: missed})
	}
	return out, nil
}
func (r *Repository) RetryMistake(ctx context.Context, u, q string, values []string) (app.PracticeFeedback, error) {
	uid, e := parseUUID(u)
	if e != nil {
		return app.PracticeFeedback{}, app.ErrNotFound
	}
	qid, e := parseUUID(q)
	if e != nil {
		return app.PracticeFeedback{}, app.ErrNotFound
	}
	rows, e := r.queries.GetOwnedMistakeKey(ctx, db.GetOwnedMistakeKeyParams{UserID: uid, QuestionID: qid})
	if e != nil {
		return app.PracticeFeedback{}, e
	}
	if len(rows) == 0 {
		return app.PracticeFeedback{}, app.ErrNotFound
	}
	keys := make([]acceptedAnswer, 0, len(rows))
	valuesKey := []string{}
	for _, row := range rows {
		keys = append(keys, acceptedAnswer{value: row.Value, caseSensitive: row.IsCaseSensitive})
		valuesKey = append(valuesKey, row.Value)
	}
	correct := answerCorrect(rows[0].Type, values, keys)
	payload, _ := json.Marshal(map[string]any{"values": values})
	e = r.queries.RecordMistakeRetry(ctx, db.RecordMistakeRetryParams{UserID: uid, QuestionID: qid, Answer: payload, IsCorrect: correct})
	return app.PracticeFeedback{QuestionID: q, Correct: correct, AcceptedAnswer: valuesKey, Explanation: rows[0].Explanation}, e
}
func (r *Repository) GetReminder(ctx context.Context, u string) (app.Reminder, error) {
	uid, e := parseUUID(u)
	if e != nil {
		return app.Reminder{}, app.ErrNotFound
	}
	b, e := r.queries.GetReminder(ctx, uid)
	if e != nil {
		return app.Reminder{}, e
	}
	var reminder app.Reminder
	e = json.Unmarshal(b, &reminder)
	return reminder, e
}
func (r *Repository) SaveReminder(ctx context.Context, u string, v app.Reminder) error {
	uid, e := parseUUID(u)
	if e != nil {
		return app.ErrNotFound
	}
	return r.queries.SaveReminder(ctx, db.SaveReminderParams{UserID: uid, Enabled: v.Enabled, LocalTime: v.LocalTime, Timezone: v.Timezone})
}
func (r *Repository) ListDrafts(ctx context.Context, adminID string) ([]app.Draft, error) {
	a, e := parseUUID(adminID)
	if e != nil {
		return nil, e
	}
	rows, e := r.queries.ListContentDrafts(ctx, a)
	if e != nil {
		return nil, e
	}
	out := []app.Draft{}
	for _, row := range rows {
		var c app.Content
		if e = json.Unmarshal(row.Data, &c); e != nil {
			return nil, e
		}
		c.AccessScope = row.AccessScope
		c.OwnerUserID = uuidString(row.OwnerUserID)
		out = append(out, app.Draft{ID: uuidString(row.ID), ExamID: uuidString(row.ExamID), Data: c})
	}
	return out, nil
}
func (r *Repository) SaveDraft(ctx context.Context, u, id string, c app.Content) (string, error) {
	b, e := json.Marshal(c)
	if e != nil {
		return "", e
	}
	ownerID, e := optionalUUID(c.OwnerUserID)
	if e != nil {
		return "", app.ErrInvalidProfile
	}
	if id == "" {
		uid, e := parseUUID(u)
		if e != nil {
			return "", app.ErrNotFound
		}
		key, e := r.queries.CreateContentDraft(ctx, db.CreateContentDraftParams{AdminAuthorID: uid, Data: b, AccessScope: c.AccessScope, OwnerUserID: ownerID})
		return uuidString(key), e
	}
	key, e := parseUUID(id)
	if e != nil {
		return "", app.ErrNotFound
	}
	_, e = r.queries.UpdateContentDraft(ctx, db.UpdateContentDraftParams{ID: key, Data: b, AccessScope: c.AccessScope, OwnerUserID: ownerID})
	return id, learningError(e)
}
func (r *Repository) PublishDraft(ctx context.Context, id string) (string, error) {
	key, e := parseUUID(id)
	if e != nil {
		return "", app.ErrNotFound
	}
	tx, e := r.pool.Begin(ctx)
	if e != nil {
		return "", e
	}
	defer tx.Rollback(ctx)
	q := r.queries.WithTx(tx)
	draft, e := q.LockContentDraft(ctx, key)
	if e != nil {
		return "", learningError(e)
	}
	var c app.Content
	if e = json.Unmarshal(draft.Data, &c); e != nil {
		return "", e
	}
	c.AccessScope = draft.AccessScope
	c.OwnerUserID = uuidString(draft.OwnerUserID)
	if e = app.ValidateContent(c, true); e != nil {
		return "", e
	}
	examID := draft.ExamID
	if !examID.Valid {
		examID, e = q.CreateContentExam(ctx, db.CreateContentExamParams{Slug: "content-" + uuid.NewString(), CreatedByAdmin: draft.AdminAuthorID, AccessScope: draft.AccessScope, OwnerUserID: draft.OwnerUserID})
		if e != nil {
			return "", e
		}
		if e = q.SetDraftExam(ctx, db.SetDraftExamParams{ID: key, ExamID: examID}); e != nil {
			return "", e
		}
	}
	if e = q.UpdateContentExamAccess(ctx, db.UpdateContentExamAccessParams{ID: examID, AccessScope: draft.AccessScope, OwnerUserID: draft.OwnerUserID}); e != nil {
		return "", e
	}
	if e = q.ArchiveContentVersions(ctx, examID); e != nil {
		return "", e
	}
	version, e := q.PublishContentVersion(ctx, db.PublishContentVersionParams{ExamID: examID, Title: c.Title, Description: c.Description, Skill: db.ExamSkill(c.Skill), Purpose: db.ExamPurpose(c.Purpose), DurationSeconds: c.Duration, Instructions: c.Instructions})
	if e != nil {
		return "", e
	}
	for i, s := range c.Sections {
		passage, transcript := s.Passage, ""
		if c.Skill == "listening" {
			passage, transcript = "", s.Passage
		}
		pages := s.SourceImageURLs
		if pages == nil {
			pages = []string{}
		}
		sourcePages, e := json.Marshal(pages)
		if e != nil {
			return "", e
		}
		section, e := q.CreateContentSection(ctx, db.CreateContentSectionParams{ExamVersionID: version, Position: int32(i + 1), Title: s.Title, Instructions: s.Instructions, AudioUrl: text(s.AudioURL), Passage: text(passage), Transcript: text(transcript), SourceImageUrls: sourcePages})
		if e != nil {
			return "", e
		}
		for j, question := range s.Questions {
			questionID, e := q.CreateContentQuestion(ctx, db.CreateContentQuestionParams{SectionID: section, Position: int32(j + 1), Type: db.QuestionType(question.Type), Prompt: question.Prompt, Explanation: question.Explanation})
			if e != nil {
				return "", e
			}
			for k, o := range question.Options {
				if e = q.CreateContentOption(ctx, db.CreateContentOptionParams{QuestionID: questionID, Position: int32(k + 1), Label: fmt.Sprintf("%c", 'A'+k), Value: o}); e != nil {
					return "", e
				}
			}
			for k, a := range question.Answers {
				if e = q.CreateContentAnswer(ctx, db.CreateContentAnswerParams{QuestionID: questionID, Position: int32(k + 1), Value: a}); e != nil {
					return "", e
				}
			}
		}
	}
	for _, b := range c.Bands {
		if e = q.CreateContentBand(ctx, db.CreateContentBandParams{ExamVersionID: version, MinimumRawScore: numeric(float64(b.Minimum)), Band: numeric(b.Band)}); e != nil {
			return "", e
		}
	}
	if e = tx.Commit(ctx); e != nil {
		return "", e
	}
	return uuidString(examID), nil
}

func (r *Repository) CreateImport(ctx context.Context, adminID, filename string, size int64, digest, skills, model, uri, accessScope, ownerUserID string) (string, error) {
	uid, e := parseUUID(adminID)
	if e != nil {
		return "", app.ErrNotFound
	}
	ownerID, e := optionalUUID(ownerUserID)
	if e != nil {
		return "", app.ErrInvalidProfile
	}
	id, e := r.queries.CreateContentImport(ctx, db.CreateContentImportParams{AdminAuthorID: uid, Filename: filename, SizeBytes: size, Sha256: digest, RequestedSkills: skills, Model: model, ProviderFileUri: uri, AccessScope: accessScope, OwnerUserID: ownerID})
	return uuidString(id), e
}

func (r *Repository) ListImports(ctx context.Context, adminID string) ([]app.ContentImport, error) {
	uid, e := parseUUID(adminID)
	if e != nil {
		return nil, app.ErrNotFound
	}
	rows, e := r.queries.ListContentImports(ctx, uid)
	if e != nil {
		return nil, e
	}
	out := make([]app.ContentImport, 0, len(rows))
	for _, row := range rows {
		var drafts, warnings []string
		if e = json.Unmarshal(row.DraftIds, &drafts); e != nil {
			return nil, e
		}
		if e = json.Unmarshal(row.Warnings, &warnings); e != nil {
			return nil, e
		}
		item := app.ContentImport{ID: uuidString(row.ID), Filename: row.Filename, SizeBytes: row.SizeBytes, SHA256: row.Sha256, RequestedSkills: row.RequestedSkills, Status: row.Status, Model: row.Model, ProgressCurrent: row.ProgressCurrent, ProgressTotal: row.ProgressTotal, DraftIDs: drafts, Warnings: warnings, ErrorMessage: row.ErrorMessage, AccessScope: row.AccessScope, OwnerUserID: uuidString(row.OwnerUserID)}
		item.CreatedAt = row.CreatedAt.Time
		item.UpdatedAt = row.UpdatedAt.Time
		if row.CompletedAt.Valid {
			item.CompletedAt = row.CompletedAt.Time
		}
		out = append(out, item)
	}
	return out, nil
}

func (r *Repository) ClaimImport(ctx context.Context) (app.ClaimedImport, error) {
	row, e := r.queries.ClaimContentImport(ctx)
	if e != nil {
		return app.ClaimedImport{}, learningError(e)
	}
	return app.ClaimedImport{ID: uuidString(row.ID), AdminID: uuidString(row.AdminAuthorID), Filename: row.Filename, Skills: row.RequestedSkills, Model: row.Model, FileURI: row.ProviderFileUri, AccessScope: row.AccessScope, OwnerUserID: uuidString(row.OwnerUserID)}, nil
}

func optionalUUID(value string) (pgtype.UUID, error) {
	if value == "" {
		return pgtype.UUID{}, nil
	}
	return parseUUID(value)
}

func (r *Repository) SavePrivateMedia(ctx context.Context, adminID, ownerID, filename, mime string, data []byte) (string, error) {
	adminUUID, e := parseUUID(adminID)
	if e != nil {
		return "", app.ErrNotFound
	}
	ownerUUID, e := optionalUUID(ownerID)
	if e != nil {
		return "", app.ErrInvalidProfile
	}
	digest := fmt.Sprintf("%x", sha256.Sum256(data))
	id, e := r.queries.CreatePrivateMedia(ctx, db.CreatePrivateMediaParams{OwnerUserID: ownerUUID, AdminAuthorID: adminUUID, Filename: filename, MimeType: mime, Sha256: digest, Data: data})
	return uuidString(id), e
}

func (r *Repository) GetPrivateMediaForAdmin(ctx context.Context, mediaID string) (app.PrivateMedia, error) {
	mid, e := parseUUID(mediaID)
	if e != nil {
		return app.PrivateMedia{}, app.ErrNotFound
	}
	row, e := r.queries.GetPrivateMediaForAdmin(ctx, mid)
	if e != nil {
		return app.PrivateMedia{}, learningError(e)
	}
	return app.PrivateMedia{Filename: row.Filename, MimeType: row.MimeType, Data: row.Data, CreatedAt: row.CreatedAt.Time}, nil
}

func (r *Repository) GetPrivateMedia(ctx context.Context, userID, mediaID string) (app.PrivateMedia, error) {
	uid, e := parseUUID(userID)
	if e != nil {
		return app.PrivateMedia{}, app.ErrNotFound
	}
	mid, e := parseUUID(mediaID)
	if e != nil {
		return app.PrivateMedia{}, app.ErrNotFound
	}
	row, e := r.queries.GetPrivateMedia(ctx, db.GetPrivateMediaParams{ID: mid, OwnerUserID: uid})
	if e != nil {
		return app.PrivateMedia{}, learningError(e)
	}
	return app.PrivateMedia{Filename: row.Filename, MimeType: row.MimeType, Data: row.Data, CreatedAt: row.CreatedAt.Time}, nil
}
func (r *Repository) SetImportTotal(ctx context.Context, id string, total int32) error {
	key, e := parseUUID(id)
	if e != nil {
		return app.ErrNotFound
	}
	return r.queries.SetContentImportTotal(ctx, db.SetContentImportTotalParams{ID: key, ProgressTotal: total})
}
func (r *Repository) AddImportDraft(ctx context.Context, id, draft string) error {
	key, e := parseUUID(id)
	if e != nil {
		return app.ErrNotFound
	}
	return r.queries.AddContentImportDraft(ctx, db.AddContentImportDraftParams{ID: key, Column2: draft})
}
func (r *Repository) CompleteImport(ctx context.Context, id string, warnings []string) error {
	key, e := parseUUID(id)
	if e != nil {
		return app.ErrNotFound
	}
	raw, _ := json.Marshal(warnings)
	return r.queries.CompleteContentImport(ctx, db.CompleteContentImportParams{ID: key, Warnings: raw})
}
func (r *Repository) FailImport(ctx context.Context, id, message string) error {
	key, e := parseUUID(id)
	if e != nil {
		return app.ErrNotFound
	}
	return r.queries.FailContentImport(ctx, db.FailContentImportParams{ID: key, ErrorMessage: message})
}
func (r *Repository) FailStaleImports(ctx context.Context) error {
	return r.queries.FailStaleContentImports(ctx)
}
