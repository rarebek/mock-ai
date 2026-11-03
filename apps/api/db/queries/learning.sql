-- name: GetWorkspace :one
SELECT COALESCE(w.data, '{}'::jsonb)::jsonb AS data FROM attempts a
LEFT JOIN attempt_workspace w ON w.attempt_id = a.id
WHERE a.id = sqlc.arg(attempt_id) AND a.user_id = sqlc.arg(user_id);

-- name: SaveWorkspace :one
INSERT INTO attempt_workspace (attempt_id, data)
SELECT id, sqlc.arg(data)::jsonb FROM attempts
WHERE id = sqlc.arg(attempt_id) AND user_id = sqlc.arg(user_id) AND status = 'in_progress'
ON CONFLICT (attempt_id) DO UPDATE SET data = EXCLUDED.data, updated_at = now()
RETURNING data;

-- name: ListMistakes :many
WITH failures AS (
 SELECT question_id, attempted_at AS occurred_at FROM practice_answers WHERE user_id = sqlc.arg(user_id) AND NOT is_correct
 UNION ALL
 SELECT q.id, a.scored_at FROM attempts a
 JOIN exam_sections s ON s.exam_version_id = a.exam_version_id
 JOIN questions q ON q.section_id = s.id
 LEFT JOIN attempt_answers aa ON aa.attempt_id = a.id AND aa.question_id = q.id
 WHERE a.user_id = sqlc.arg(user_id) AND a.status = 'scored' AND NOT COALESCE(aa.is_correct, false)
), missed AS (SELECT question_id, max(occurred_at) AS last_missed FROM failures GROUP BY question_id)
SELECT q.id, q.type, q.prompt, s.passage, s.audio_url, s.title AS section_title, v.skill,
 m.last_missed,
 COALESCE((SELECT r.is_correct FROM mistake_retries r WHERE r.user_id = sqlc.arg(user_id) AND r.question_id = q.id AND r.attempted_at > m.last_missed ORDER BY r.attempted_at DESC LIMIT 1), false)::boolean AS resolved
FROM missed m JOIN questions q ON q.id = m.question_id
JOIN exam_sections s ON s.id = q.section_id JOIN exam_versions v ON v.id = s.exam_version_id
ORDER BY m.last_missed DESC, q.id LIMIT 200;

-- name: GetOwnedMistakeKey :many
SELECT q.type, q.explanation, aa.value, aa.is_case_sensitive
FROM questions q JOIN accepted_answers aa ON aa.question_id = q.id
WHERE q.id = sqlc.arg(question_id) AND (
 EXISTS (SELECT 1 FROM practice_answers p WHERE p.user_id = sqlc.arg(user_id) AND p.question_id = q.id AND NOT p.is_correct)
 OR EXISTS (SELECT 1 FROM attempts a JOIN exam_sections s ON s.exam_version_id = a.exam_version_id
 LEFT JOIN attempt_answers ans ON ans.attempt_id = a.id AND ans.question_id = q.id
 WHERE a.user_id = sqlc.arg(user_id) AND a.status = 'scored' AND s.id = q.section_id AND NOT COALESCE(ans.is_correct, false)))
ORDER BY aa.position;

-- name: RecordMistakeRetry :exec
INSERT INTO mistake_retries(user_id, question_id, answer, is_correct) VALUES ($1,$2,$3,$4);

-- name: GetReminder :one
SELECT jsonb_build_object('enabled', COALESCE(r.enabled, false), 'local_time', COALESCE(r.local_time, '19:00'),
 'timezone', COALESCE(r.timezone, 'Asia/Tashkent'), 'telegram_linked', EXISTS(SELECT 1 FROM user_identities i WHERE i.user_id = u.id AND i.provider = 'telegram'))::jsonb AS data
FROM users u LEFT JOIN study_reminders r ON r.user_id = u.id WHERE u.id = $1;

-- name: SaveReminder :exec
INSERT INTO study_reminders(user_id, enabled, local_time, timezone) VALUES ($1,$2,$3,$4)
ON CONFLICT(user_id) DO UPDATE SET enabled = EXCLUDED.enabled, local_time = EXCLUDED.local_time, timezone = EXCLUDED.timezone, updated_at = now();

-- name: ClaimReminders :many
WITH due AS (
 SELECT r.user_id FROM study_reminders r
 WHERE r.enabled AND r.local_time <= to_char(now() AT TIME ZONE r.timezone, 'HH24:MI')
 AND r.last_sent_date IS DISTINCT FROM (now() AT TIME ZONE r.timezone)::date
 AND EXISTS(SELECT 1 FROM user_identities i WHERE i.user_id = r.user_id AND i.provider = 'telegram')
 ORDER BY r.user_id LIMIT 100 FOR UPDATE SKIP LOCKED
), claimed AS (
 UPDATE study_reminders r SET last_sent_date = (now() AT TIME ZONE r.timezone)::date
 FROM due WHERE r.user_id = due.user_id RETURNING r.user_id
)
SELECT c.user_id, i.provider_subject FROM claimed c JOIN user_identities i ON i.user_id = c.user_id AND i.provider = 'telegram';

-- name: DisableReminder :exec
UPDATE study_reminders SET enabled = false WHERE user_id = $1;

-- name: ListContentDrafts :many
SELECT id, exam_id, data, access_scope, owner_user_id, updated_at FROM content_drafts
WHERE centre_id IS NOT DISTINCT FROM (SELECT a.centre_id FROM admins a WHERE a.id = sqlc.arg(admin_id))
ORDER BY updated_at DESC LIMIT 100;

-- name: CreateContentDraft :one
INSERT INTO content_drafts(admin_author_id,data,access_scope,owner_user_id,centre_id)
VALUES (sqlc.arg(admin_author_id),sqlc.arg(data),sqlc.arg(access_scope),sqlc.arg(owner_user_id),
 (SELECT a.centre_id FROM admins a WHERE a.id = sqlc.arg(admin_author_id))) RETURNING id;

-- name: UpdateContentDraft :one
UPDATE content_drafts SET data=$2,access_scope=$3,owner_user_id=$4,updated_at=now() WHERE id=$1 RETURNING id;

-- name: LockContentDraft :one
SELECT * FROM content_drafts WHERE id = $1 FOR UPDATE;

-- name: SetDraftExam :exec
UPDATE content_drafts SET exam_id = $2 WHERE id = $1;

-- name: CreateContentExam :one
INSERT INTO exams(slug,created_by_admin,access_scope,owner_user_id,centre_id)
VALUES (sqlc.arg(slug),sqlc.arg(created_by_admin),sqlc.arg(access_scope),sqlc.arg(owner_user_id),
 (SELECT a.centre_id FROM admins a WHERE a.id = sqlc.arg(created_by_admin))) RETURNING id;

-- name: UpdateContentExamAccess :exec
UPDATE exams SET access_scope=$2, owner_user_id=$3 WHERE id=$1;

-- name: ArchiveContentVersions :exec
UPDATE exam_versions SET status = 'archived' WHERE exam_id = $1 AND status = 'published';

-- name: PublishContentVersion :one
INSERT INTO exam_versions(exam_id,version,title,description,skill,purpose,status,duration_seconds,instructions,published_at)
SELECT sqlc.arg(exam_id), COALESCE(max(version),0)+1, sqlc.arg(title),sqlc.arg(description),sqlc.arg(skill)::exam_skill,sqlc.arg(purpose)::exam_purpose,'published',sqlc.arg(duration_seconds),sqlc.arg(instructions),now()
FROM exam_versions WHERE exam_id = sqlc.arg(exam_id) RETURNING id;

-- name: CreateContentSection :one
INSERT INTO exam_sections(exam_version_id,position,title,instructions,audio_url,passage,transcript,source_image_urls) VALUES ($1,$2,$3,$4,$5,$6,$7,$8) RETURNING id;

-- name: CreateContentQuestion :one
INSERT INTO questions(section_id,position,type,prompt,explanation) VALUES ($1,$2,$3,$4,$5) RETURNING id;

-- name: CreateContentOption :exec
INSERT INTO question_options(question_id,position,label,value) VALUES ($1,$2,$3,$4);

-- name: CreateContentAnswer :exec
INSERT INTO accepted_answers(question_id,value,position) VALUES ($1,$2,$3);

-- name: CreateContentBand :exec
INSERT INTO band_conversions(exam_version_id,minimum_raw_score,band) VALUES ($1,$2,$3);

-- name: CreateContentImport :one
INSERT INTO content_imports(admin_author_id,filename,size_bytes,sha256,requested_skills,model,provider_file_uri,access_scope,owner_user_id,centre_id)
VALUES (sqlc.arg(admin_author_id),sqlc.arg(filename),sqlc.arg(size_bytes),sqlc.arg(sha256),sqlc.arg(requested_skills),
 sqlc.arg(model),sqlc.arg(provider_file_uri),sqlc.arg(access_scope),sqlc.arg(owner_user_id),
 (SELECT a.centre_id FROM admins a WHERE a.id = sqlc.arg(admin_author_id))) RETURNING id;

-- name: ListContentImports :many
SELECT id,filename,size_bytes,sha256,requested_skills,status,model,progress_current,progress_total,draft_ids,warnings,error_message,access_scope,owner_user_id,created_at,updated_at,completed_at
FROM content_imports WHERE admin_author_id=$1 ORDER BY created_at DESC LIMIT 30;

-- name: ClaimContentImport :one
WITH candidate AS (
 SELECT id FROM content_imports WHERE status='queued' ORDER BY created_at LIMIT 1 FOR UPDATE SKIP LOCKED
)
UPDATE content_imports i SET status='processing',updated_at=now()
FROM candidate WHERE i.id=candidate.id
RETURNING i.id,i.admin_author_id,i.filename,i.requested_skills,i.model,i.provider_file_uri,i.access_scope,i.owner_user_id;

-- name: CreatePrivateMedia :one
INSERT INTO private_media(owner_user_id,admin_author_id,filename,mime_type,sha256,data)
VALUES($1,$2,$3,$4,$5,$6)
RETURNING id;

-- name: GetPrivateMediaForAdmin :one
SELECT filename,mime_type,data,created_at
FROM private_media
WHERE id=$1;

-- name: GetPrivateMedia :one
SELECT m.filename,m.mime_type,m.data,m.created_at
FROM private_media m
WHERE m.id=$1 AND (
  m.owner_user_id=$2 OR EXISTS (
    SELECT 1
    FROM exam_sections s
    JOIN exam_versions v ON v.id=s.exam_version_id
    JOIN exams e ON e.id=v.exam_id
    WHERE e.archived_at IS NULL
      AND e.access_scope='public'
      AND v.status='published'
      AND (
        s.audio_url='/api/v1/private-media/'||m.id::text
        OR s.source_image_urls ? ('/api/v1/private-media/'||m.id::text)
      )
  ) OR EXISTS (
    SELECT 1
    FROM managed_content c
    WHERE c.kind='writing_prompt'
      AND c.status='published'
      AND (c.starts_at IS NULL OR c.starts_at<=now())
      AND (c.ends_at IS NULL OR c.ends_at>now())
      AND COALESCE(c.data->'source_image_urls','[]'::jsonb) ? ('/api/v1/private-media/'||m.id::text)
  )
);

-- name: SetContentImportTotal :exec
UPDATE content_imports SET progress_total=$2,updated_at=now() WHERE id=$1 AND status='processing';

-- name: AddContentImportDraft :exec
UPDATE content_imports SET progress_current=progress_current+1,draft_ids=draft_ids || to_jsonb($2::text),updated_at=now()
WHERE id=$1 AND status='processing';

-- name: CompleteContentImport :exec
UPDATE content_imports SET status='completed',warnings=$2,completed_at=now(),updated_at=now() WHERE id=$1 AND status='processing';

-- name: FailContentImport :exec
UPDATE content_imports SET status='failed',error_message=$2,completed_at=now(),updated_at=now() WHERE id=$1 AND status='processing';

-- name: FailStaleContentImports :exec
UPDATE content_imports SET status='failed',error_message='The server restarted during extraction. Upload the PDF again.',completed_at=now(),updated_at=now()
WHERE status='processing';
