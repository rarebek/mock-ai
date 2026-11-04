ALTER TABLE speaking_turns DROP CONSTRAINT IF EXISTS speaking_turns_voice_status_check;
UPDATE speaking_turns SET voice_status='pending' WHERE voice_status='text';
ALTER TABLE speaking_turns ADD CONSTRAINT speaking_turns_voice_status_check
  CHECK (voice_status IN ('pending','queued','processing','ready','failed'));
DROP INDEX IF EXISTS speaking_awaiting_review_idx;
DROP INDEX IF EXISTS writing_awaiting_review_idx;
ALTER TABLE speaking_sessions DROP COLUMN IF EXISTS reviewed_at;
ALTER TABLE speaking_sessions DROP COLUMN IF EXISTS reviewed_by;
ALTER TABLE writing_submissions DROP COLUMN IF EXISTS reviewed_at;
ALTER TABLE writing_submissions DROP COLUMN IF EXISTS reviewed_by;
-- Work waiting on a person has no equivalent in the old model. 'failed' is the
-- honest reading: there is no report, and the student is told so.
ALTER TABLE speaking_sessions DROP CONSTRAINT IF EXISTS speaking_sessions_status_check;
UPDATE speaking_sessions SET status='failed' WHERE status='awaiting_review';
ALTER TABLE speaking_sessions ADD CONSTRAINT speaking_sessions_status_check
  CHECK (status IN ('active','queued','processing','completed','failed','expired'));
ALTER TABLE writing_submissions DROP CONSTRAINT IF EXISTS writing_submissions_status_check;
UPDATE writing_submissions SET status='failed' WHERE status='awaiting_review';
ALTER TABLE writing_submissions ADD CONSTRAINT writing_submissions_status_check
  CHECK (status IN ('draft','queued','processing','completed','failed'));
ALTER TABLE centres DROP COLUMN IF EXISTS ai_enabled;
