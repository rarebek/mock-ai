-- Whether a learning centre's students reach Gemini at all.
--
-- When this is false nothing the centre produces is sent to a model: Writing
-- essays and Speaking recordings are captured and parked for the centre to mark
-- by hand, the Speaking interview presents its questions as text instead of
-- synthesised speech, and the PDF exam import is closed. Reading and Listening
-- are unaffected either way, because they are scored against a stored answer
-- key and never involved a model.
ALTER TABLE centres ADD COLUMN ai_enabled boolean NOT NULL DEFAULT true;

-- Work waiting for a person to mark it. 'awaiting_review' is deliberately not
-- 'queued': nothing will pick it up, and a worker that treats the two alike
-- would hand a centre's private work to a model.
ALTER TABLE writing_submissions DROP CONSTRAINT IF EXISTS writing_submissions_status_check;
ALTER TABLE writing_submissions ADD CONSTRAINT writing_submissions_status_check
  CHECK (status IN ('draft','queued','processing','completed','failed','awaiting_review'));

ALTER TABLE speaking_sessions DROP CONSTRAINT IF EXISTS speaking_sessions_status_check;
ALTER TABLE speaking_sessions ADD CONSTRAINT speaking_sessions_status_check
  CHECK (status IN ('active','queued','processing','completed','failed','expired','awaiting_review'));

-- Who marked it, so a human report is never mistaken for a model's. NULL means
-- the report came from the model, which is what every existing row is.
ALTER TABLE writing_submissions ADD COLUMN reviewed_by uuid REFERENCES admins(id) ON DELETE SET NULL;
ALTER TABLE writing_submissions ADD COLUMN reviewed_at timestamptz;
ALTER TABLE speaking_sessions ADD COLUMN reviewed_by uuid REFERENCES admins(id) ON DELETE SET NULL;
ALTER TABLE speaking_sessions ADD COLUMN reviewed_at timestamptz;

CREATE INDEX writing_awaiting_review_idx ON writing_submissions (status, updated_at DESC) WHERE status = 'awaiting_review';
CREATE INDEX speaking_awaiting_review_idx ON speaking_sessions (status, updated_at DESC) WHERE status = 'awaiting_review';

-- A turn a centre's student reads rather than hears. Nothing synthesises it,
-- so it is answerable immediately; 'ready' continues to mean audio exists.
ALTER TABLE speaking_turns DROP CONSTRAINT IF EXISTS speaking_turns_voice_status_check;
ALTER TABLE speaking_turns ADD CONSTRAINT speaking_turns_voice_status_check
  CHECK (voice_status IN ('pending','queued','processing','ready','failed','text'));
