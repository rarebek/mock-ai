-- name: CreateCentre :one
INSERT INTO centres (slug, name, join_code, include_global, ai_enabled)
VALUES ($1, $2, $3, $4, $5)
RETURNING *;

-- name: ListCentres :many
SELECT c.*,
  (SELECT count(*) FROM users u WHERE u.centre_id = c.id)::bigint AS student_count,
  (SELECT count(*) FROM exams e WHERE e.centre_id = c.id)::bigint AS exam_count
FROM centres c
ORDER BY c.name;

-- name: GetCentre :one
SELECT * FROM centres WHERE id = $1;

-- name: UpdateCentre :one
UPDATE centres SET name = sqlc.arg(name), include_global = sqlc.arg(include_global),
  ai_enabled = sqlc.arg(ai_enabled),
  disabled_at = CASE WHEN sqlc.arg(disabled)::boolean THEN coalesce(disabled_at, now()) ELSE NULL END
WHERE id = sqlc.arg(id)
RETURNING *;

-- A join code identifies a centre from a Telegram deep link. A disabled centre
-- stops accepting new students; the ones it already has are untouched.
-- name: CentreByJoinCode :one
SELECT * FROM centres WHERE join_code = $1 AND disabled_at IS NULL;

-- Joining is one-way for now: a student already attached to a centre keeps it,
-- so a stray link cannot move someone out of the centre teaching them. The
-- WHERE clause is the whole check, so a concurrent second tap cannot race it.
-- name: JoinCentre :one
UPDATE users SET centre_id = sqlc.arg(centre_id), updated_at = now()
WHERE id = sqlc.arg(user_id) AND centre_id IS NULL AND deleted_at IS NULL
RETURNING centre_id;

-- Whether a student's work may be sent to a model. Derived from the student so
-- no caller can pass the wrong answer, and true for a student with no centre:
-- a direct registrant is on the platform's own terms.
-- name: StudentAIEnabled :one
SELECT coalesce((SELECT c.ai_enabled FROM centres c WHERE c.id = u.centre_id), true)::boolean
FROM users u WHERE u.id = $1 AND u.deleted_at IS NULL;

-- The same question for an administrator, used to close the PDF import.
-- name: AdminAIEnabled :one
SELECT coalesce((SELECT c.ai_enabled FROM centres c WHERE c.id = a.centre_id), true)::boolean
FROM admins a WHERE a.id = $1;

-- Work a centre must mark itself. Scoped to the administrator's own centre, so
-- a marker only ever sees their own students' work.
-- name: ListWritingAwaitingReview :many
SELECT w.id, w.user_id, u.display_name, w.prompt, w.essay, w.updated_at
FROM writing_submissions w JOIN users u ON u.id = w.user_id
WHERE w.status = 'awaiting_review'
  AND u.centre_id IS NOT DISTINCT FROM (SELECT a.centre_id FROM admins a WHERE a.id = sqlc.arg(admin_id))
ORDER BY w.updated_at LIMIT 200;

-- name: ListSpeakingAwaitingReview :many
SELECT s.id, s.user_id, u.display_name, s.topic, s.updated_at,
  (SELECT count(*) FROM speaking_turns t WHERE t.session_id = s.id AND t.answered)::bigint AS answers
FROM speaking_sessions s JOIN users u ON u.id = s.user_id
WHERE s.status = 'awaiting_review'
  AND u.centre_id IS NOT DISTINCT FROM (SELECT a.centre_id FROM admins a WHERE a.id = sqlc.arg(admin_id))
ORDER BY s.updated_at LIMIT 200;

-- Publishing a marked report. The centre match is part of the update, so a
-- marker cannot publish onto another centre's student, and 'awaiting_review'
-- is required so this can never overwrite a report a model produced.
-- name: PublishWritingReview :execrows
UPDATE writing_submissions w SET status='completed', report=sqlc.arg(report), error_code='',
  reviewed_by=sqlc.arg(admin_id), reviewed_at=now(), updated_at=now()
FROM users u
WHERE w.id=sqlc.arg(id) AND u.id=w.user_id AND w.status='awaiting_review'
  AND u.centre_id IS NOT DISTINCT FROM (SELECT a.centre_id FROM admins a WHERE a.id = sqlc.arg(admin_id));

-- name: PublishSpeakingReview :execrows
UPDATE speaking_sessions s SET status='completed', report=sqlc.arg(report), error_code='',
  reviewed_by=sqlc.arg(admin_id), reviewed_at=now(), updated_at=now()
FROM users u
WHERE s.id=sqlc.arg(id) AND u.id=s.user_id AND s.status='awaiting_review'
  AND u.centre_id IS NOT DISTINCT FROM (SELECT a.centre_id FROM admins a WHERE a.id = sqlc.arg(admin_id));
