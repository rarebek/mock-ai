-- A source page is usually a photograph of a book page, and a phone camera
-- writes JPEG. The upload path accepts one; this is the last place that did
-- not, and it failed as a 500 rather than a refusal.
ALTER TABLE private_media DROP CONSTRAINT private_media_mime_type_check;
ALTER TABLE private_media
ADD CONSTRAINT private_media_mime_type_check
CHECK (mime_type IN ('audio/mpeg', 'image/png', 'image/jpeg'));
