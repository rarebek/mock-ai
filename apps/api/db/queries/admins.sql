-- name: CreateAdmin :one
INSERT INTO admins(email,display_name,password_hash) VALUES($1,$2,$3) RETURNING id;

-- name: ReserveAdminLogin :one
UPDATE admins SET
 login_attempts=CASE WHEN login_window<now()-interval '15 minutes' THEN 1 ELSE login_attempts+1 END,
 login_window=CASE WHEN login_window<now()-interval '15 minutes' THEN now() ELSE login_window END
WHERE email=$1 AND NOT disabled AND (login_attempts<5 OR login_window<now()-interval '15 minutes')
RETURNING id,email,display_name,password_hash,centre_id,
 coalesce((SELECT c.name FROM centres c WHERE c.id=admins.centre_id),'')::text AS centre_name;

-- name: ResetAdminLogin :exec
UPDATE admins SET login_attempts=0,login_window=now() WHERE id=$1;

-- name: CreateAdminSession :exec
INSERT INTO admin_sessions(token_hash,admin_id,expires_at) VALUES($1,$2,$3);

-- name: GetAdminSession :one
SELECT a.id,a.email,a.display_name,a.centre_id,coalesce((SELECT c.name FROM centres c WHERE c.id=a.centre_id),'')::text AS centre_name
FROM admins a JOIN admin_sessions s ON s.admin_id=a.id
WHERE s.token_hash=$1 AND s.expires_at>now() AND NOT a.disabled;

-- name: DeleteAdminSession :exec
DELETE FROM admin_sessions WHERE token_hash=$1;

-- Every figure here is scoped to the administrator's own centre; a platform
-- administrator, who has no centre, sees the students and content that belong
-- to no centre. The scope is read from the administrator's own row, the same
-- way every other admin query derives it.
-- name: AdminDashboardSummary :one
WITH me AS (SELECT a.centre_id FROM admins a WHERE a.id = sqlc.arg(admin_id)),
mine AS (
  SELECT u.id FROM users u
  WHERE u.deleted_at IS NULL
    AND coalesce(u.email,'') NOT LIKE '%@mock-ai.internal'
    AND coalesce(u.email,'') NOT LIKE '%@example.test'
    AND u.display_name NOT ILIKE '%E2E%'
    AND u.centre_id IS NOT DISTINCT FROM (SELECT centre_id FROM me)
)
SELECT
 (SELECT count(*) FROM mine)::integer AS users,
 (SELECT count(*) FROM users u JOIN mine ON mine.id=u.id WHERE u.created_at>=now()-interval '7 days')::integer AS new_users_7d,
 (SELECT count(DISTINCT active.user_id) FROM (
   SELECT user_id FROM community_activity WHERE occurred_at>=now()-interval '7 days'
   UNION ALL SELECT user_id FROM attempts WHERE started_at>=now()-interval '7 days'
   UNION ALL SELECT user_id FROM practice_answers WHERE attempted_at>=now()-interval '7 days'
   UNION ALL SELECT user_id FROM writing_submissions WHERE updated_at>=now()-interval '7 days'
   UNION ALL SELECT user_id FROM speaking_sessions WHERE updated_at>=now()-interval '7 days'
 ) active JOIN mine ON mine.id=active.user_id)::integer AS active_users_7d,
 (SELECT count(*) FROM attempts a JOIN mine ON mine.id=a.user_id WHERE a.status='scored')::integer AS completed_mocks,
 coalesce((SELECT round(avg(a.band_score),1) FROM attempts a JOIN mine ON mine.id=a.user_id WHERE a.status='scored'),0)::numeric AS average_band,
 (SELECT count(*) FROM writing_submissions w JOIN mine ON mine.id=w.user_id WHERE w.status='completed')::integer AS writing_reports,
 (SELECT count(*) FROM speaking_sessions sp JOIN mine ON mine.id=sp.user_id WHERE sp.status='completed')::integer AS speaking_reports,
 (SELECT count(*) FROM exam_versions v JOIN exams e ON e.id=v.exam_id
   WHERE v.status='published' AND e.centre_id IS NOT DISTINCT FROM (SELECT centre_id FROM me))::integer AS published_exams,
 (SELECT count(*) FROM content_drafts d
   WHERE d.centre_id IS NOT DISTINCT FROM (SELECT centre_id FROM me))::integer AS exam_drafts,
 (SELECT count(*) FROM managed_content m
   WHERE m.status='published' AND m.centre_id IS NOT DISTINCT FROM (SELECT centre_id FROM me))::integer AS published_content,
 (SELECT count(*) FROM managed_content m
   WHERE m.status='draft' AND m.centre_id IS NOT DISTINCT FROM (SELECT centre_id FROM me))::integer AS managed_drafts;

-- name: AdminDashboardTrend :many
WITH me AS (SELECT a.centre_id FROM admins a WHERE a.id = sqlc.arg(admin_id)),
mine AS (
  SELECT u.id, u.created_at FROM users u
  WHERE u.deleted_at IS NULL
    AND coalesce(u.email,'') NOT LIKE '%@mock-ai.internal'
    AND coalesce(u.email,'') NOT LIKE '%@example.test'
    AND u.display_name NOT ILIKE '%E2E%'
    AND u.centre_id IS NOT DISTINCT FROM (SELECT centre_id FROM me)
),
days AS (SELECT generate_series(current_date-13,current_date,interval '1 day')::date AS activity_date),
signups AS (SELECT m.created_at::date activity_date,count(*)::integer value FROM mine m
  WHERE m.created_at>=current_date-13 GROUP BY 1),
activity AS (SELECT events.activity_at::date activity_date,count(DISTINCT events.user_id)::integer value FROM (
 SELECT user_id,occurred_at activity_at FROM community_activity WHERE occurred_at>=current_date-13
 UNION ALL SELECT user_id,started_at FROM attempts WHERE started_at>=current_date-13
 UNION ALL SELECT user_id,attempted_at FROM practice_answers WHERE attempted_at>=current_date-13
 UNION ALL SELECT user_id,updated_at FROM writing_submissions WHERE updated_at>=current_date-13
 UNION ALL SELECT user_id,updated_at FROM speaking_sessions WHERE updated_at>=current_date-13
) events JOIN mine ON mine.id=events.user_id GROUP BY 1),
attempts_by_day AS (SELECT a.started_at::date activity_date,count(*)::integer value FROM attempts a
  JOIN mine ON mine.id=a.user_id WHERE a.started_at>=current_date-13 GROUP BY 1)
SELECT d.activity_date,coalesce(s.value,0)::integer AS signups,coalesce(a.value,0)::integer AS active_students,coalesce(t.value,0)::integer AS attempts
FROM days d LEFT JOIN signups s USING(activity_date) LEFT JOIN activity a USING(activity_date) LEFT JOIN attempts_by_day t USING(activity_date) ORDER BY d.activity_date;

-- name: AdminContentBreakdown :many
SELECT kind,status,count(*)::integer AS count FROM managed_content
WHERE centre_id IS NOT DISTINCT FROM (SELECT a.centre_id FROM admins a WHERE a.id = sqlc.arg(admin_id))
GROUP BY kind,status ORDER BY kind,status;

-- name: ListAdminStudents :many
SELECT u.id,
       u.display_name,
       coalesce(u.email, '')::text AS email,
       coalesce(telegram.profile->>'username', '')::text AS telegram_username,
       coalesce(telegram.provider_subject, '')::text AS telegram_id
FROM users u
LEFT JOIN LATERAL (
  SELECT i.profile, i.provider_subject
  FROM user_identities i
  WHERE i.user_id = u.id AND i.provider = 'telegram'
  LIMIT 1
) telegram ON true
WHERE u.deleted_at IS NULL
  AND u.centre_id IS NOT DISTINCT FROM (SELECT a.centre_id FROM admins a WHERE a.id = sqlc.arg(admin_id))
ORDER BY lower(u.display_name), u.id
LIMIT 500;

-- An administrator belonging to a learning centre. Created only by a platform
-- administrator, and confined to that centre by every other query.
-- name: CreateCentreAdmin :one
INSERT INTO admins (email, display_name, password_hash, centre_id)
VALUES ($1, $2, $3, $4)
RETURNING id;

-- name: ListCentreAdmins :many
SELECT id, email, display_name, disabled, created_at
FROM admins WHERE centre_id = $1
ORDER BY lower(display_name), id;

-- The centre is part of the check, so a platform administrator cannot
-- accidentally disable or re-key an administrator of a different centre, and a
-- centre administrator has no route here at all.
-- name: SetCentreAdminState :one
UPDATE admins SET disabled = sqlc.arg(disabled)
WHERE id = sqlc.arg(id) AND centre_id = sqlc.arg(centre_id)
RETURNING id;

-- name: ResetCentreAdminPassword :one
UPDATE admins SET password_hash = sqlc.arg(password_hash), login_attempts = 0, login_window = now()
WHERE id = sqlc.arg(id) AND centre_id = sqlc.arg(centre_id)
RETURNING id;
