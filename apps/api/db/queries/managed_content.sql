-- name: ListManagedContent :many
SELECT * FROM managed_content
WHERE (sqlc.arg(kind)::text = '' OR kind = sqlc.arg(kind))
  AND centre_id IS NOT DISTINCT FROM (SELECT a.centre_id FROM admins a WHERE a.id = sqlc.arg(admin_id))
ORDER BY kind, sort_order, updated_at DESC;

-- name: ListPublishedContent :many
SELECT * FROM managed_content
WHERE status='published' AND (sqlc.arg(kind)::text = '' OR kind=sqlc.arg(kind))
  AND (starts_at IS NULL OR starts_at<=now()) AND (ends_at IS NULL OR ends_at>now())
  AND (managed_content.centre_id IS NOT DISTINCT FROM (SELECT vme.centre_id FROM users vme WHERE vme.id = sqlc.arg(user_id))
       OR (managed_content.centre_id IS NULL AND EXISTS (
             SELECT 1 FROM users vu JOIN centres vc ON vc.id = vu.centre_id
             WHERE vu.id = sqlc.arg(user_id) AND vc.include_global)))
ORDER BY kind, sort_order, created_at;

-- name: CreateManagedContent :one
INSERT INTO managed_content(kind,slug,title,summary,status,sort_order,starts_at,ends_at,data,created_by,updated_by,centre_id)
VALUES(sqlc.arg(kind),sqlc.arg(slug),sqlc.arg(title),sqlc.arg(summary),sqlc.arg(status),sqlc.arg(sort_order),
 sqlc.arg(starts_at),sqlc.arg(ends_at),sqlc.arg(data),sqlc.arg(created_by),sqlc.arg(created_by),
 (SELECT a.centre_id FROM admins a WHERE a.id = sqlc.arg(created_by)))
RETURNING *;

-- name: UpdateManagedContent :one
UPDATE managed_content SET kind=sqlc.arg(kind),slug=sqlc.arg(slug),title=sqlc.arg(title),summary=sqlc.arg(summary),
 status=sqlc.arg(status),sort_order=sqlc.arg(sort_order),starts_at=sqlc.arg(starts_at),ends_at=sqlc.arg(ends_at),
 data=sqlc.arg(data),updated_by=sqlc.arg(updated_by),updated_at=now()
WHERE managed_content.id=sqlc.arg(id)
  AND managed_content.centre_id IS NOT DISTINCT FROM (SELECT a.centre_id FROM admins a WHERE a.id = sqlc.arg(updated_by))
RETURNING *;

-- name: DeleteManagedContent :execrows
DELETE FROM managed_content WHERE managed_content.id=sqlc.arg(id)
 AND managed_content.centre_id IS NOT DISTINCT FROM (SELECT a.centre_id FROM admins a WHERE a.id = sqlc.arg(admin_id));

-- name: RandomSpeakingPlan :one
SELECT title,data FROM managed_content
WHERE kind='speaking_plan' AND status='published'
 AND (starts_at IS NULL OR starts_at<=now()) AND (ends_at IS NULL OR ends_at>now())
  AND (managed_content.centre_id IS NOT DISTINCT FROM (SELECT vme.centre_id FROM users vme WHERE vme.id = sqlc.arg(user_id))
       OR (managed_content.centre_id IS NULL AND EXISTS (
             SELECT 1 FROM users vu JOIN centres vc ON vc.id = vu.centre_id
             WHERE vu.id = sqlc.arg(user_id) AND vc.include_global)))
ORDER BY random() LIMIT 1;

-- name: PublishedSpeakingPlan :one
SELECT title,data FROM managed_content
WHERE managed_content.id=sqlc.arg(id) AND kind='speaking_plan' AND status='published'
 AND (starts_at IS NULL OR starts_at<=now()) AND (ends_at IS NULL OR ends_at>now())
  AND (managed_content.centre_id IS NOT DISTINCT FROM (SELECT vme.centre_id FROM users vme WHERE vme.id = sqlc.arg(user_id))
       OR (managed_content.centre_id IS NULL AND EXISTS (
             SELECT 1 FROM users vu JOIN centres vc ON vc.id = vu.centre_id
             WHERE vu.id = sqlc.arg(user_id) AND vc.include_global)));
