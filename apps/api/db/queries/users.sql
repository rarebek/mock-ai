-- name: CreateUser :one
INSERT INTO users (display_name, avatar_url, email)
VALUES ($1, $2, $3)
RETURNING *;

-- name: CreateVerifiedUser :one
INSERT INTO users (display_name, avatar_url, email, email_verified_at)
VALUES ($1, $2, $3, now())
RETURNING *;

-- name: GetUserByID :one
SELECT * FROM users
WHERE id = $1 AND deleted_at IS NULL;

-- name: GetUserByIdentity :one
SELECT u.*
FROM users u
JOIN user_identities i ON i.user_id = u.id
WHERE i.provider = $1
  AND i.provider_subject = $2
  AND u.deleted_at IS NULL;

-- name: GetUserByEmail :one
SELECT * FROM users
WHERE lower(email) = lower($1) AND deleted_at IS NULL;

-- name: CreateIdentity :one
INSERT INTO user_identities (
    user_id, provider, provider_subject, provider_email, profile
)
VALUES ($1, $2, $3, $4, $5)
RETURNING *;

-- name: AssignUserRole :exec
INSERT INTO user_role_assignments (user_id, role)
VALUES ($1, $2)
ON CONFLICT DO NOTHING;

-- name: ListUserRoles :many
SELECT role FROM user_role_assignments
WHERE user_id = $1
ORDER BY role;

-- name: UpdateExternalProfile :one
UPDATE users
SET display_name = $2,
    avatar_url = COALESCE($3, avatar_url),
    updated_at = now()
WHERE id = $1 AND deleted_at IS NULL
RETURNING *;

-- name: MarkEmailVerified :one
UPDATE users
SET email_verified_at = COALESCE(email_verified_at, now()),
    updated_at = now()
WHERE id = $1 AND deleted_at IS NULL
RETURNING *;

-- name: CreateSession :one
INSERT INTO sessions (
    user_id, token_hash, expires_at, user_agent, ip_address
)
VALUES (
    $1,
    $2,
    $3,
    NULLIF(sqlc.arg(user_agent), ''),
    NULLIF(sqlc.arg(ip_address), '')::inet
)
RETURNING *;

-- name: GetUserBySession :one
SELECT
    u.id,
    u.display_name,
    u.avatar_url,
    u.email,
    u.email_verified_at,
    u.created_at,
    u.updated_at,
    u.deleted_at
FROM sessions s
JOIN users u ON u.id = s.user_id
WHERE s.token_hash = $1
  AND s.revoked_at IS NULL
  AND s.expires_at > now()
  AND u.deleted_at IS NULL;

-- name: TouchSession :exec
UPDATE sessions
SET last_seen_at = now()
WHERE token_hash = $1
  AND revoked_at IS NULL
  AND expires_at > now();

-- name: RevokeSession :exec
UPDATE sessions
SET revoked_at = now()
WHERE token_hash = $1 AND revoked_at IS NULL;

-- name: CreateLoginLink :one
INSERT INTO telegram_login_links (user_id, token_hash, expires_at)
VALUES ($1, $2, $3)
RETURNING id;

-- name: CountRecentLoginLinks :one
SELECT count(*) FROM telegram_login_links
WHERE user_id = $1 AND created_at > $2;

-- name: ConsumeLoginLink :one
UPDATE telegram_login_links SET consumed_at = now()
WHERE token_hash = $1 AND consumed_at IS NULL AND expires_at > now()
RETURNING user_id;
