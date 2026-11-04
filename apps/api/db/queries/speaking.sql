-- name: LockSpeakingUser :exec
SELECT pg_advisory_xact_lock(hashtextextended('speaking:' || sqlc.arg(user_id)::text,0));

-- name: CountSpeakingRequests :one
SELECT count(*)::integer FROM speaking_requests WHERE user_id=$1 AND kind=$2
AND created_at>=date_trunc('day',now() AT TIME ZONE 'UTC') AT TIME ZONE 'UTC';

-- name: RecordSpeakingRequest :exec
INSERT INTO speaking_requests(user_id,kind) VALUES($1,$2);

-- name: CreateSpeaking :one
INSERT INTO speaking_sessions(user_id,examiner,topic,model) VALUES($1,$2,$3,$4) RETURNING *;

-- name: CreateSpeakingTurn :exec
-- A centre with AI switched off gets turns its students read: no voice is
-- ever synthesised for them, so nothing about this session reaches a model.
INSERT INTO speaking_turns(session_id,position,part,prompt,seconds,prep_seconds,voice_status)
VALUES(sqlc.arg(session_id),sqlc.arg(position),sqlc.arg(part),sqlc.arg(prompt),sqlc.arg(seconds),sqlc.arg(prep_seconds),
 CASE WHEN NOT (SELECT coalesce((SELECT c.ai_enabled FROM centres c WHERE c.id=u.centre_id),true)
                FROM speaking_sessions s JOIN users u ON u.id=s.user_id WHERE s.id=sqlc.arg(session_id))
      THEN 'text'
      WHEN sqlc.arg(position)=0 THEN 'queued' ELSE 'pending' END);

-- name: GetSpeaking :one
SELECT * FROM speaking_sessions WHERE user_id=$1 AND id=$2;

-- name: LockSpeaking :one
SELECT * FROM speaking_sessions WHERE user_id=$1 AND id=$2 FOR UPDATE;

-- name: ListSpeaking :many
SELECT * FROM speaking_sessions WHERE user_id=$1 ORDER BY created_at DESC LIMIT 30;

-- name: SpeakingTurns :many
SELECT position,part,prompt,seconds,prep_seconds,voice_status,voice_attempts,duration_ms,answered,skipped,live_transcript,
(recording IS NOT NULL)::boolean AS has_recording FROM speaking_turns WHERE session_id=$1 ORDER BY position;

-- name: SpeakingAudio :one
SELECT t.recording,t.examiner_audio FROM speaking_turns t JOIN speaking_sessions s ON s.id=t.session_id
WHERE s.user_id=$1 AND s.id=$2 AND t.position=$3 AND s.audio_expires_at>now();

-- name: AnswerSpeaking :execrows
UPDATE speaking_turns SET recording=$3,duration_ms=$4,answered=true,skipped=$5
WHERE session_id=$1 AND position=$2 AND NOT answered AND voice_status IN ('ready','text');

-- name: QueueSpeakingVoice :execrows
UPDATE speaking_turns SET voice_status='queued',voice_run_id=gen_random_uuid()
WHERE session_id=$1 AND position=$2 AND voice_status IN ('pending','failed') AND voice_attempts<3;

-- Like writing: a centre with AI switched off parks the recordings for a
-- person. Workers only ever claim 'queued'.
-- name: QueueSpeakingReport :one
UPDATE speaking_sessions SET
 status=CASE WHEN (SELECT coalesce((SELECT c.ai_enabled FROM centres c WHERE c.id=u.centre_id),true)
                   FROM users u WHERE u.id=speaking_sessions.user_id) THEN 'queued' ELSE 'awaiting_review' END,
 run_id=gen_random_uuid(),error_code='',updated_at=now()
WHERE speaking_sessions.id=sqlc.arg(id) RETURNING *;

-- name: DeleteSpeaking :execrows
DELETE FROM speaking_sessions WHERE user_id=$1 AND id=$2;

-- name: ClaimSpeakingVoice :one
WITH candidate AS (SELECT t.session_id,t.position FROM speaking_turns t JOIN speaking_sessions s ON s.id=t.session_id
 WHERE t.voice_status='queued' AND s.status='active' AND s.mode='recorded' ORDER BY s.created_at,t.position FOR UPDATE OF t SKIP LOCKED LIMIT 1)
UPDATE speaking_turns t SET voice_status='processing',voice_attempts=voice_attempts+1,voice_lease_until=now()+interval '3 minutes'
FROM candidate c,speaking_sessions s WHERE t.session_id=c.session_id AND t.position=c.position AND s.id=t.session_id
RETURNING t.session_id,t.position,t.prompt,t.voice_run_id,s.examiner;

-- name: FinishSpeakingVoice :execrows
UPDATE speaking_turns SET voice_status=$4,examiner_audio=$5,voice_lease_until=NULL
WHERE session_id=$1 AND position=$2 AND voice_run_id=$3 AND voice_status='processing' AND voice_lease_until>now();

-- name: ClaimSpeakingReport :one
WITH candidate AS (SELECT id FROM speaking_sessions WHERE status='queued' ORDER BY created_at FOR UPDATE SKIP LOCKED LIMIT 1)
UPDATE speaking_sessions s SET status='processing',lease_until=now()+interval '4 minutes',updated_at=now()
FROM candidate c WHERE s.id=c.id RETURNING s.*;

-- name: SpeakingRecordings :many
SELECT position,recording FROM speaking_turns WHERE session_id=$1 ORDER BY position;

-- name: FinishSpeakingReport :execrows
UPDATE speaking_sessions SET status=$3,report=$4,error_code=$5,lease_until=NULL,updated_at=now()
WHERE id=$1 AND run_id=$2 AND status='processing' AND lease_until>now();

-- name: ExpireSpeakingVoices :exec
UPDATE speaking_turns SET voice_status='failed',voice_lease_until=NULL WHERE voice_status='processing' AND voice_lease_until<now();

-- name: ExpireSpeakingReports :exec
UPDATE speaking_sessions SET status='failed',error_code='interrupted',lease_until=NULL,updated_at=now()
WHERE status='processing' AND lease_until<now();

-- name: ExpireSpeakingSessions :exec
UPDATE speaking_sessions SET status='expired' WHERE status IN ('active','failed') AND audio_expires_at<now();

-- name: PurgeSpeakingAudio :exec
UPDATE speaking_turns t SET recording=NULL,examiner_audio=NULL FROM speaking_sessions s
WHERE s.id=t.session_id AND s.audio_expires_at<now() AND (t.recording IS NOT NULL OR t.examiner_audio IS NOT NULL);
