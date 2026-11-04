-- name: ImportAIKey :exec
INSERT INTO ai_provider_keys(provider,fingerprint,ciphertext) VALUES('gemini',$1,$2)
ON CONFLICT(fingerprint) DO NOTHING;

-- name: GetPrimaryAIKey :one
SELECT ciphertext FROM ai_provider_keys WHERE provider='gemini' AND enabled ORDER BY created_at,id LIMIT 1;

-- name: WritingKeyAvailable :one
SELECT EXISTS(SELECT 1 FROM ai_provider_keys WHERE provider='gemini' AND enabled)::boolean;

-- name: CreateWriting :one
INSERT INTO writing_submissions(user_id,parent_id,prompt,essay,reference) VALUES($1,$2,$3,$4,$5) RETURNING *;

-- name: ListWriting :many
SELECT * FROM writing_submissions WHERE user_id=$1 ORDER BY updated_at DESC LIMIT 50;

-- name: GetWriting :one
SELECT * FROM writing_submissions WHERE user_id=$1 AND id=$2;

-- name: SaveWriting :one
UPDATE writing_submissions SET prompt=$3,essay=$4,reference=$6,revision=revision+1,updated_at=now()
WHERE user_id=$1 AND id=$2 AND revision=$5 AND status='draft' RETURNING *;

-- name: GetPublishedWritingReference :one
SELECT data FROM managed_content WHERE id=$1 AND kind='writing_prompt' AND status='published'
AND (starts_at IS NULL OR starts_at<=now()) AND (ends_at IS NULL OR ends_at>now());

-- name: DeleteWriting :execrows
DELETE FROM writing_submissions WHERE user_id=$1 AND id=$2 AND status NOT IN ('queued','processing');

-- name: LockWritingUser :exec
SELECT pg_advisory_xact_lock(hashtextextended('writing:' || sqlc.arg(user_id)::text,0));

-- name: CountWritingToday :one
SELECT count(*)::integer FROM writing_evaluation_requests WHERE user_id=$1
AND created_at>=date_trunc('day',now() AT TIME ZONE 'UTC') AT TIME ZONE 'UTC';

-- name: RecordWritingRequest :exec
INSERT INTO writing_evaluation_requests(user_id,submission_id) VALUES($1,$2);

-- Submitting parks the essay for a person when the student's centre has AI
-- switched off. The decision is made here rather than in the caller so there
-- is no path that can queue a centre's work for a model by mistake: a worker
-- only ever claims 'queued'.
-- name: QueueWriting :one
UPDATE writing_submissions SET
 status=CASE WHEN (SELECT coalesce((SELECT c.ai_enabled FROM centres c WHERE c.id=u.centre_id),true)
                   FROM users u WHERE u.id=sqlc.arg(user_id)) THEN 'queued' ELSE 'awaiting_review' END,
 model=sqlc.arg(model),rubric_version=sqlc.arg(rubric_version),error_code='',run_id=gen_random_uuid(),updated_at=now()
WHERE writing_submissions.user_id=sqlc.arg(user_id) AND writing_submissions.id=sqlc.arg(id)
  AND writing_submissions.revision=sqlc.arg(revision)
  AND status IN ('draft','failed') RETURNING *;

-- name: ClaimWriting :one
WITH next AS (SELECT id FROM writing_submissions WHERE status='queued' ORDER BY created_at FOR UPDATE SKIP LOCKED LIMIT 1)
UPDATE writing_submissions w SET status='processing',lease_until=now()+interval '3 minutes',updated_at=now()
FROM next WHERE w.id=next.id RETURNING w.*;

-- name: ExpireWritingJobs :exec
UPDATE writing_submissions SET status='failed',error_code='interrupted',lease_until=NULL,updated_at=now()
WHERE status='processing' AND lease_until<now();

-- name: FinishWriting :execrows
UPDATE writing_submissions SET status=$2,report=$3,error_code=$4,lease_until=NULL,updated_at=now()
WHERE id=$1 AND status='processing' AND lease_until>now() AND run_id=$5;
