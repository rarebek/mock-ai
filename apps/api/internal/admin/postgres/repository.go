package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	app "github.com/own/mock-ai/apps/api/internal/admin/application"
	db "github.com/own/mock-ai/apps/api/internal/platform/postgres/sqlc"
	"strings"
	"time"
)

type Repository struct {
	q *db.Queries
	p *pgxpool.Pool
}

func New(p *pgxpool.Pool) *Repository  { return &Repository{q: db.New(p), p: p} }
func id(s string) (pgtype.UUID, error) { var v pgtype.UUID; e := v.Scan(s); return v, e }
func str(v pgtype.UUID) string         { s, _ := v.Value(); return s.(string) }

// optional renders a nullable id. str asserts the driver returned a string,
// which a NULL uuid does not, so a platform administrator -- who has no centre
// -- would panic on login if this went through str.
func optional(v pgtype.UUID) string {
	if !v.Valid {
		return ""
	}
	return str(v)
}
func (r *Repository) Create(ctx context.Context, email, name, hash string) (string, error) {
	v, e := r.q.CreateAdmin(ctx, db.CreateAdminParams{Email: email, DisplayName: name, PasswordHash: hash})
	if e != nil {
		return "", e
	}
	return str(v), nil
}

// CreateCentreAdmin provisions an administrator for one learning centre.
func (r *Repository) CreateCentreAdmin(ctx context.Context, centreID, email, name, hash string) (string, error) {
	c, e := id(centreID)
	if e != nil {
		return "", app.ErrCentreNotFound
	}
	v, e := r.q.CreateCentreAdmin(ctx, db.CreateCentreAdminParams{Email: email, DisplayName: name, PasswordHash: hash, CentreID: c})
	if e != nil {
		if strings.Contains(e.Error(), "admins_email_key") {
			return "", app.ErrAdminEmailTaken
		}
		return "", e
	}
	return str(v), nil
}

func (r *Repository) ListCentreAdmins(ctx context.Context, centreID string) ([]app.CentreAdmin, error) {
	c, e := id(centreID)
	if e != nil {
		return nil, app.ErrCentreNotFound
	}
	rows, e := r.q.ListCentreAdmins(ctx, c)
	if e != nil {
		return nil, e
	}
	out := make([]app.CentreAdmin, 0, len(rows))
	for _, v := range rows {
		out = append(out, app.CentreAdmin{ID: str(v.ID), Email: v.Email, DisplayName: v.DisplayName,
			Disabled: v.Disabled, CreatedAt: v.CreatedAt.Time})
	}
	return out, nil
}

func (r *Repository) SetCentreAdminState(ctx context.Context, centreID, adminID string, disabled bool) error {
	c, e := id(centreID)
	if e != nil {
		return app.ErrCentreNotFound
	}
	a, e := id(adminID)
	if e != nil {
		return app.ErrAdminNotFound
	}
	if _, e = r.q.SetCentreAdminState(ctx, db.SetCentreAdminStateParams{ID: a, CentreID: c, Disabled: disabled}); errors.Is(e, pgx.ErrNoRows) {
		return app.ErrAdminNotFound
	}
	return e
}

func (r *Repository) ResetCentreAdminPassword(ctx context.Context, centreID, adminID, hash string) error {
	c, e := id(centreID)
	if e != nil {
		return app.ErrCentreNotFound
	}
	a, e := id(adminID)
	if e != nil {
		return app.ErrAdminNotFound
	}
	if _, e = r.q.ResetCentreAdminPassword(ctx, db.ResetCentreAdminPasswordParams{ID: a, CentreID: c, PasswordHash: hash}); errors.Is(e, pgx.ErrNoRows) {
		return app.ErrAdminNotFound
	}
	return e
}

func (r *Repository) ReserveLogin(ctx context.Context, email string) (app.Credential, error) {
	v, e := r.q.ReserveAdminLogin(ctx, email)
	if e != nil {
		return app.Credential{}, e
	}
	return app.Credential{Admin: app.Admin{ID: str(v.ID), Email: v.Email, DisplayName: v.DisplayName}, Hash: v.PasswordHash}, nil
}
func (r *Repository) ResetLogin(ctx context.Context, s string) error {
	v, e := id(s)
	if e != nil {
		return e
	}
	return r.q.ResetAdminLogin(ctx, v)
}
func (r *Repository) SaveSession(ctx context.Context, s, hash string, expires time.Time) error {
	v, e := id(s)
	if e != nil {
		return e
	}
	return r.q.CreateAdminSession(ctx, db.CreateAdminSessionParams{TokenHash: hash, AdminID: v, ExpiresAt: pgtype.Timestamptz{Time: expires, Valid: true}})
}
func (r *Repository) Session(ctx context.Context, hash string) (app.Admin, error) {
	v, e := r.q.GetAdminSession(ctx, hash)
	if e != nil {
		return app.Admin{}, e
	}
	return app.Admin{ID: str(v.ID), Email: v.Email, DisplayName: v.DisplayName,
		CentreID: optional(v.CentreID), CentreName: v.CentreName}, nil
}
func (r *Repository) DeleteSession(ctx context.Context, hash string) error {
	return r.q.DeleteAdminSession(ctx, hash)
}

func (r *Repository) Dashboard(ctx context.Context, adminID string) (app.Dashboard, error) {
	s, e := r.q.AdminDashboardSummary(ctx)
	if e != nil {
		return app.Dashboard{}, e
	}
	trend, e := r.q.AdminDashboardTrend(ctx)
	if e != nil {
		return app.Dashboard{}, e
	}
	a, e := id(adminID)
	if e != nil {
		return app.Dashboard{}, e
	}
	counts, e := r.q.AdminContentBreakdown(ctx, a)
	if e != nil {
		return app.Dashboard{}, e
	}
	avg, _ := s.AverageBand.Float64Value()
	out := app.Dashboard{Summary: app.DashboardSummary{Users: s.Users, NewUsers7d: s.NewUsers7d, ActiveUsers7d: s.ActiveUsers7d, CompletedMocks: s.CompletedMocks, AverageBand: avg.Float64, WritingReports: s.WritingReports, SpeakingReports: s.SpeakingReports, PublishedExams: s.PublishedExams, ExamDrafts: s.ExamDrafts, PublishedContent: s.PublishedContent, ManagedDrafts: s.ManagedDrafts}, Trend: make([]app.DashboardDay, 0, len(trend)), Content: make([]app.ContentCount, 0, len(counts))}
	for _, v := range trend {
		out.Trend = append(out.Trend, app.DashboardDay{Date: v.ActivityDate.Time.Format("2006-01-02"), Signups: v.Signups, ActiveStudents: v.ActiveStudents, Attempts: v.Attempts})
	}
	for _, v := range counts {
		out.Content = append(out.Content, app.ContentCount{Kind: v.Kind, Status: v.Status, Count: v.Count})
	}
	return out, nil
}
func (r *Repository) ListStudents(ctx context.Context, adminID string) ([]app.Student, error) {
	a, e := id(adminID)
	if e != nil {
		return nil, e
	}
	rows, e := r.p.Query(ctx, `
SELECT u.id,u.display_name,coalesce(u.email,''),coalesce(t.profile->>'username',''),coalesce(t.provider_subject,''),u.created_at,
 activity.last_activity,coalesce(a.total,0)::int,coalesce(a.completed,0)::int,coalesce(p.total,0)::int,
 coalesce(w.completed,0)::int,coalesce(s.completed,0)::int,a.average_band,a.best_band,a.latest_band
FROM users u
LEFT JOIN LATERAL (SELECT profile,provider_subject FROM user_identities WHERE user_id=u.id AND provider='telegram' LIMIT 1) t ON true
LEFT JOIN LATERAL (SELECT count(*) total,count(*) FILTER(WHERE status='scored') completed,avg(band_score)::float8 average_band,max(band_score)::float8 best_band,(array_agg(band_score ORDER BY scored_at DESC) FILTER(WHERE status='scored'))[1]::float8 latest_band,max(started_at) last_at FROM attempts WHERE user_id=u.id) a ON true
LEFT JOIN LATERAL (SELECT count(*) total,max(attempted_at) last_at FROM practice_answers WHERE user_id=u.id) p ON true
LEFT JOIN LATERAL (SELECT count(*) FILTER(WHERE status='completed') completed,max(updated_at) last_at FROM writing_submissions WHERE user_id=u.id) w ON true
LEFT JOIN LATERAL (SELECT count(*) FILTER(WHERE status='completed') completed,max(updated_at) last_at FROM speaking_sessions WHERE user_id=u.id) s ON true
LEFT JOIN LATERAL (SELECT max(v) last_activity FROM (VALUES(a.last_at),(p.last_at),(w.last_at),(s.last_at)) x(v)) activity ON true
WHERE u.deleted_at IS NULL
 AND coalesce(u.email,'') NOT LIKE '%@mock-ai.internal'
 AND coalesce(u.email,'') NOT LIKE '%@example.test'
 AND u.display_name NOT ILIKE '%E2E%'
 -- A centre's administrator sees their own students; the platform
 -- administrator sees the students who belong to no centre.
 AND u.centre_id IS NOT DISTINCT FROM (SELECT adm.centre_id FROM admins adm WHERE adm.id=$1)
ORDER BY coalesce(activity.last_activity,u.created_at) DESC,u.id LIMIT 500`, a)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := make([]app.Student, 0)
	for rows.Next() {
		var v app.Student
		var uid pgtype.UUID
		var last pgtype.Timestamptz
		var avg, best, latest pgtype.Float8
		if e = rows.Scan(&uid, &v.DisplayName, &v.Email, &v.TelegramUsername, &v.TelegramID, &v.JoinedAt, &last, &v.Attempts, &v.CompletedMocks, &v.PracticeAnswers, &v.WritingReports, &v.SpeakingReports, &avg, &best, &latest); e != nil {
			return nil, e
		}
		v.ID = str(uid)
		v.LastActivityAt = timePtr(last)
		v.AverageBand = floatPtr(avg)
		v.BestBand = floatPtr(best)
		v.LatestBand = floatPtr(latest)
		out = append(out, v)
	}
	return out, rows.Err()
}
func timePtr(v pgtype.Timestamptz) *time.Time {
	if !v.Valid {
		return nil
	}
	t := v.Time
	return &t
}
func floatPtr(v pgtype.Float8) *float64 {
	if !v.Valid {
		return nil
	}
	n := v.Float64
	return &n
}
func (r *Repository) StudentProgress(ctx context.Context, adminID, studentID string) (app.StudentProgress, error) {
	uid, e := id(studentID)
	if e != nil {
		return app.StudentProgress{}, app.ErrStudentNotFound
	}
	students, e := r.ListStudents(ctx, adminID)
	if e != nil {
		return app.StudentProgress{}, e
	}
	var student *app.Student
	for i := range students {
		if students[i].ID == studentID {
			student = &students[i]
			break
		}
	}
	if student == nil {
		return app.StudentProgress{}, app.ErrStudentNotFound
	}
	out := app.StudentProgress{Student: *student, Skills: []app.StudentSkill{}, Attempts: []app.StudentAttempt{}, Reports: []app.StudentAIReport{}}
	rows, e := r.p.Query(ctx, `SELECT ev.skill::text,count(*)::int,avg(a.band_score)::float8,max(a.band_score)::float8,(array_agg(a.band_score ORDER BY a.scored_at DESC))[1]::float8 FROM attempts a JOIN exam_versions ev ON ev.id=a.exam_version_id WHERE a.user_id=$1 AND a.status='scored' GROUP BY ev.skill ORDER BY ev.skill`, uid)
	if e != nil {
		return out, e
	}
	for rows.Next() {
		var v app.StudentSkill
		if e = rows.Scan(&v.Skill, &v.Completed, &v.AverageBand, &v.BestBand, &v.LatestBand); e != nil {
			rows.Close()
			return out, e
		}
		out.Skills = append(out.Skills, v)
	}
	if e = rows.Err(); e != nil {
		rows.Close()
		return out, e
	}
	rows.Close()
	rows, e = r.p.Query(ctx, `SELECT a.id,ev.title,ev.skill::text,a.status::text,a.started_at,a.submitted_at,a.band_score::float8,count(aa.question_id)::int,count(*) FILTER(WHERE aa.is_correct)::int,a.question_count FROM attempts a JOIN exam_versions ev ON ev.id=a.exam_version_id LEFT JOIN attempt_answers aa ON aa.attempt_id=a.id WHERE a.user_id=$1 GROUP BY a.id,ev.title,ev.skill ORDER BY a.started_at DESC LIMIT 100`, uid)
	if e != nil {
		return out, e
	}
	for rows.Next() {
		var v app.StudentAttempt
		var aid pgtype.UUID
		var submitted pgtype.Timestamptz
		var band pgtype.Float8
		if e = rows.Scan(&aid, &v.Title, &v.Skill, &v.Status, &v.StartedAt, &submitted, &band, &v.Answered, &v.Correct, &v.QuestionCount); e != nil {
			rows.Close()
			return out, e
		}
		v.ID = str(aid)
		v.SubmittedAt = timePtr(submitted)
		v.BandScore = floatPtr(band)
		out.Attempts = append(out.Attempts, v)
	}
	if e = rows.Err(); e != nil {
		rows.Close()
		return out, e
	}
	rows.Close()
	rows, e = r.p.Query(ctx, `SELECT id,'writing',left(prompt,180),status,CASE WHEN jsonb_typeof(report->'overall')='number' THEN (report->>'overall')::float8 END,updated_at FROM writing_submissions WHERE user_id=$1 UNION ALL SELECT id,'speaking',left(topic,180),status,CASE WHEN jsonb_typeof(report->'overall')='number' THEN (report->>'overall')::float8 END,updated_at FROM speaking_sessions WHERE user_id=$1 ORDER BY updated_at DESC LIMIT 100`, uid)
	if e != nil {
		return out, e
	}
	for rows.Next() {
		var v app.StudentAIReport
		var rid pgtype.UUID
		var overall pgtype.Float8
		if e = rows.Scan(&rid, &v.Kind, &v.Title, &v.Status, &overall, &v.UpdatedAt); e != nil {
			rows.Close()
			return out, e
		}
		v.ID = str(rid)
		v.Overall = floatPtr(overall)
		out.Reports = append(out.Reports, v)
	}
	if e = rows.Err(); e != nil {
		rows.Close()
		return out, e
	}
	rows.Close()
	return out, nil
}

func content(v db.ManagedContent) app.ManagedContent {
	out := app.ManagedContent{ID: str(v.ID), Kind: v.Kind, Slug: v.Slug, Title: v.Title, Summary: v.Summary, Status: v.Status, SortOrder: v.SortOrder, Data: json.RawMessage(v.Data), CreatedAt: v.CreatedAt.Time, UpdatedAt: v.UpdatedAt.Time}
	if v.StartsAt.Valid {
		t := v.StartsAt.Time
		out.StartsAt = &t
	}
	if v.EndsAt.Valid {
		t := v.EndsAt.Time
		out.EndsAt = &t
	}
	return out
}
func nullableTime(v *time.Time) pgtype.Timestamptz {
	if v == nil {
		return pgtype.Timestamptz{}
	}
	return pgtype.Timestamptz{Time: *v, Valid: true}
}
func centreOf(v db.Centre) app.Centre {
	return app.Centre{ID: str(v.ID), Slug: v.Slug, Name: v.Name, JoinCode: v.JoinCode,
		IncludeGlobal: v.IncludeGlobal, AIEnabled: v.AiEnabled, Disabled: v.DisabledAt.Valid}
}

func (r *Repository) ListCentres(ctx context.Context) ([]app.Centre, error) {
	rows, e := r.q.ListCentres(ctx)
	if e != nil {
		return nil, e
	}
	out := make([]app.Centre, 0, len(rows))
	for _, v := range rows {
		centre := centreOf(db.Centre{ID: v.ID, Slug: v.Slug, Name: v.Name, JoinCode: v.JoinCode,
			IncludeGlobal: v.IncludeGlobal, AiEnabled: v.AiEnabled, DisabledAt: v.DisabledAt, CreatedAt: v.CreatedAt})
		centre.Students, centre.Exams = v.StudentCount, v.ExamCount
		out = append(out, centre)
	}
	return out, nil
}

func (r *Repository) CreateCentre(ctx context.Context, name, slug, code string, includeGlobal, aiEnabled bool) (app.Centre, error) {
	row, e := r.q.CreateCentre(ctx, db.CreateCentreParams{Slug: slug, Name: name, JoinCode: code, IncludeGlobal: includeGlobal, AiEnabled: aiEnabled})
	if e != nil {
		if strings.Contains(e.Error(), "centres_slug_key") {
			return app.Centre{}, app.ErrCentreTaken
		}
		return app.Centre{}, e
	}
	return centreOf(row), nil
}

func (r *Repository) UpdateCentre(ctx context.Context, value, name string, includeGlobal, aiEnabled, disabled bool) (app.Centre, error) {
	v, e := id(value)
	if e != nil {
		return app.Centre{}, app.ErrCentreNotFound
	}
	row, e := r.q.UpdateCentre(ctx, db.UpdateCentreParams{ID: v, Name: name, IncludeGlobal: includeGlobal, AiEnabled: aiEnabled, Disabled: disabled})
	if errors.Is(e, pgx.ErrNoRows) {
		return app.Centre{}, app.ErrCentreNotFound
	}
	if e != nil {
		return app.Centre{}, e
	}
	return centreOf(row), nil
}

// ListMarking gathers the work this administrator's centre has to assess.
func (r *Repository) ListMarking(ctx context.Context, adminID string) (app.Marking, error) {
	a, e := id(adminID)
	if e != nil {
		return app.Marking{}, e
	}
	out := app.Marking{Writing: []app.MarkingItem{}, Speaking: []app.MarkingItem{}}
	essays, e := r.q.ListWritingAwaitingReview(ctx, a)
	if e != nil {
		return app.Marking{}, e
	}
	for _, v := range essays {
		out.Writing = append(out.Writing, app.MarkingItem{ID: str(v.ID), StudentID: str(v.UserID),
			StudentName: v.DisplayName, Title: v.Prompt, Essay: v.Essay, SubmittedAt: v.UpdatedAt.Time})
	}
	interviews, e := r.q.ListSpeakingAwaitingReview(ctx, a)
	if e != nil {
		return app.Marking{}, e
	}
	for _, v := range interviews {
		out.Speaking = append(out.Speaking, app.MarkingItem{ID: str(v.ID), StudentID: str(v.UserID),
			StudentName: v.DisplayName, Title: v.Topic, Answers: v.Answers, SubmittedAt: v.UpdatedAt.Time})
	}
	return out, nil
}

// PublishReview stores a marked report. The centre match lives in the UPDATE,
// so a miss means the work belongs to another centre or has already been
// marked; either way it is not this marker's to publish.
func (r *Repository) PublishReview(ctx context.Context, adminID, skill, value string, report []byte) error {
	a, e := id(adminID)
	if e != nil {
		return e
	}
	v, e := id(value)
	if e != nil {
		return app.ErrReviewNotFound
	}
	var rows int64
	if skill == "speaking" {
		rows, e = r.q.PublishSpeakingReview(ctx, db.PublishSpeakingReviewParams{ID: v, AdminID: a, Report: report})
	} else {
		rows, e = r.q.PublishWritingReview(ctx, db.PublishWritingReviewParams{ID: v, AdminID: a, Report: report})
	}
	if e != nil {
		return e
	}
	if rows == 0 {
		return app.ErrReviewNotFound
	}
	return nil
}

func (r *Repository) ListContent(ctx context.Context, adminID, kind string) ([]app.ManagedContent, error) {
	a, e := id(adminID)
	if e != nil {
		return nil, e
	}
	rows, e := r.q.ListManagedContent(ctx, db.ListManagedContentParams{AdminID: a, Kind: kind})
	if e != nil {
		return nil, e
	}
	out := make([]app.ManagedContent, 0, len(rows))
	for _, v := range rows {
		out = append(out, content(v))
	}
	return out, nil
}
func (r *Repository) CreateContent(ctx context.Context, adminID string, v app.ManagedContent) (app.ManagedContent, error) {
	a, e := id(adminID)
	if e != nil {
		return app.ManagedContent{}, e
	}
	row, e := r.q.CreateManagedContent(ctx, db.CreateManagedContentParams{Kind: v.Kind, Slug: v.Slug, Title: v.Title, Summary: v.Summary, Status: v.Status, SortOrder: v.SortOrder, StartsAt: nullableTime(v.StartsAt), EndsAt: nullableTime(v.EndsAt), Data: v.Data, CreatedBy: a})
	if e != nil {
		return app.ManagedContent{}, e
	}
	return content(row), nil
}
func (r *Repository) UpdateContent(ctx context.Context, adminID string, v app.ManagedContent) (app.ManagedContent, error) {
	a, e := id(adminID)
	if e != nil {
		return app.ManagedContent{}, e
	}
	contentID, e := id(v.ID)
	if e != nil {
		return app.ManagedContent{}, app.ErrContentNotFound
	}
	row, e := r.q.UpdateManagedContent(ctx, db.UpdateManagedContentParams{ID: contentID, Kind: v.Kind, Slug: v.Slug, Title: v.Title, Summary: v.Summary, Status: v.Status, SortOrder: v.SortOrder, StartsAt: nullableTime(v.StartsAt), EndsAt: nullableTime(v.EndsAt), Data: v.Data, UpdatedBy: a})
	if e != nil {
		return app.ManagedContent{}, e
	}
	return content(row), nil
}
func (r *Repository) DeleteContent(ctx context.Context, adminID, value string) error {
	a, e := id(adminID)
	if e != nil {
		return app.ErrContentNotFound
	}
	v, e := id(value)
	if e != nil {
		return app.ErrContentNotFound
	}
	n, e := r.q.DeleteManagedContent(ctx, db.DeleteManagedContentParams{ID: v, AdminID: a})
	if e == nil && n == 0 {
		return app.ErrContentNotFound
	}
	return e
}
