-- Rows have to go before the constraint comes back, or restoring it fails on
-- the pages uploaded while it was allowed.
DELETE FROM private_media WHERE mime_type = 'image/jpeg';
ALTER TABLE private_media DROP CONSTRAINT private_media_mime_type_check;
ALTER TABLE private_media
ADD CONSTRAINT private_media_mime_type_check
CHECK (mime_type IN ('audio/mpeg', 'image/png'));
