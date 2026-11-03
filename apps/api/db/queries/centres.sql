-- name: CreateCentre :one
INSERT INTO centres (slug, name, join_code, include_global)
VALUES ($1, $2, $3, $4)
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
UPDATE centres SET name = $2, include_global = $3,
  disabled_at = CASE WHEN sqlc.arg(disabled)::boolean THEN coalesce(disabled_at, now()) ELSE NULL END
WHERE id = $1
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
