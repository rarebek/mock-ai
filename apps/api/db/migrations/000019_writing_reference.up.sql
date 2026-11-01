ALTER TABLE writing_submissions ADD COLUMN reference jsonb NOT NULL DEFAULT '{}'::jsonb;
