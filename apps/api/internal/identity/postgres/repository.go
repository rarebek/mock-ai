package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/own/mock-ai/apps/api/internal/identity/domain"
	db "github.com/own/mock-ai/apps/api/internal/platform/postgres/sqlc"
)

var (
	ErrNotFound = errors.New("identity record not found")
	ErrConflict = errors.New("identity record already exists")
)

type Repository struct {
	pool    *pgxpool.Pool
	queries *db.Queries
}

func New(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool, queries: db.New(pool)}
}

// JoinCentre attaches a student to the centre a join code identifies. It is
// one-way: a student who already belongs to a centre keeps it, so a link
// forwarded into the wrong chat cannot move someone out of the centre that is
// teaching them. Returns the centre they ended up in, which may be the one
// they already had.
func (r *Repository) JoinCentre(ctx context.Context, userID, joinCode string) (domain.Centre, bool, error) {
	id, err := parseUUID(userID)
	if err != nil {
		return domain.Centre{}, false, ErrNotFound
	}
	centre, err := r.queries.CentreByJoinCode(ctx, joinCode)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Centre{}, false, ErrNotFound
	}
	if err != nil {
		return domain.Centre{}, false, fmt.Errorf("find centre: %w", err)
	}
	joined := true
	if _, err = r.queries.JoinCentre(ctx, db.JoinCentreParams{CentreID: centre.ID, UserID: id}); errors.Is(err, pgx.ErrNoRows) {
		// Already in a centre, or no such student. Either way nothing moved.
		joined = false
	} else if err != nil {
		return domain.Centre{}, false, fmt.Errorf("join centre: %w", err)
	}
	return domain.Centre{
		ID:            uuidString(centre.ID),
		Slug:          centre.Slug,
		Name:          centre.Name,
		IncludeGlobal: centre.IncludeGlobal,
	}, joined, nil
}

// IssueLoginLink stores the hash of a one-time login token for a user. The raw
// token is never persisted, so the table cannot be replayed into a session.
func (r *Repository) IssueLoginLink(ctx context.Context, userID string, tokenHash []byte, expiresAt time.Time) error {
	id, err := parseUUID(userID)
	if err != nil {
		return ErrNotFound
	}
	if _, err = r.queries.CreateLoginLink(ctx, db.CreateLoginLinkParams{
		UserID:    id,
		TokenHash: tokenHash,
		ExpiresAt: pgtype.Timestamptz{Time: expiresAt, Valid: true},
	}); err != nil {
		return fmt.Errorf("create login link: %w", err)
	}
	return nil
}

// RecentLoginLinks counts the links a user has been issued since a cutoff, so
// repeatedly restarting the bot cannot mint an unbounded supply of them.
func (r *Repository) RecentLoginLinks(ctx context.Context, userID string, since time.Time) (int64, error) {
	id, err := parseUUID(userID)
	if err != nil {
		return 0, ErrNotFound
	}
	n, err := r.queries.CountRecentLoginLinks(ctx, db.CountRecentLoginLinksParams{UserID: id, CreatedAt: pgtype.Timestamptz{Time: since, Valid: true}})
	if err != nil {
		return 0, fmt.Errorf("count login links: %w", err)
	}
	return n, nil
}

// ConsumeLoginLink spends a login token and returns its owner. The update is
// the whole check: a token that is expired, unknown, or already spent matches
// no row, so two taps on the same link cannot both produce a session.
func (r *Repository) ConsumeLoginLink(ctx context.Context, tokenHash []byte) (domain.User, error) {
	owner, err := r.queries.ConsumeLoginLink(ctx, tokenHash)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.User{}, ErrNotFound
	}
	if err != nil {
		return domain.User{}, fmt.Errorf("consume login link: %w", err)
	}
	user, err := r.queries.GetUserByID(ctx, owner)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.User{}, ErrNotFound
	}
	if err != nil {
		return domain.User{}, fmt.Errorf("get login link user: %w", err)
	}
	roles, err := r.queries.ListUserRoles(ctx, owner)
	if err != nil {
		return domain.User{}, fmt.Errorf("list user roles: %w", err)
	}
	return toDomain(user, roles), nil
}

func (r *Repository) FindOrCreateExternal(ctx context.Context, profile domain.ExternalProfile) (domain.User, bool, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return domain.User{}, false, fmt.Errorf("begin external login: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := r.queries.WithTx(tx)
	provider := db.IdentityProvider(profile.Provider)
	created := false

	user, err := q.GetUserByIdentity(ctx, db.GetUserByIdentityParams{
		Provider:        provider,
		ProviderSubject: profile.Subject,
	})
	if err == nil {
		user, err = q.UpdateExternalProfile(ctx, db.UpdateExternalProfileParams{
			ID:          user.ID,
			DisplayName: profile.DisplayName,
			AvatarUrl:   text(profile.AvatarURL),
		})
		if err != nil {
			return domain.User{}, false, fmt.Errorf("update external profile: %w", err)
		}
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return domain.User{}, false, fmt.Errorf("find external identity: %w", err)
	} else {
		user, err = r.resolveExternalUser(ctx, q, profile)
		if err != nil {
			return domain.User{}, false, err
		}
		_, err = q.CreateIdentity(ctx, db.CreateIdentityParams{
			UserID:          user.ID,
			Provider:        provider,
			ProviderSubject: profile.Subject,
			ProviderEmail:   text(profile.Email),
			Profile:         profile.RawProfile,
		})
		if err != nil {
			return domain.User{}, false, classify(err)
		}
		created = true
	}

	if err := q.AssignUserRole(ctx, db.AssignUserRoleParams{UserID: user.ID, Role: db.UserRoleStudent}); err != nil {
		return domain.User{}, false, fmt.Errorf("assign student role: %w", err)
	}
	roles, err := q.ListUserRoles(ctx, user.ID)
	if err != nil {
		return domain.User{}, false, fmt.Errorf("list user roles: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.User{}, false, fmt.Errorf("commit external login: %w", err)
	}
	return toDomain(user, roles), created, nil
}

func (r *Repository) resolveExternalUser(ctx context.Context, q *db.Queries, profile domain.ExternalProfile) (db.User, error) {
	if profile.Email != "" && profile.EmailVerified {
		user, err := q.GetUserByEmail(ctx, profile.Email)
		if err == nil {
			return q.MarkEmailVerified(ctx, user.ID)
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return db.User{}, fmt.Errorf("find user by verified email: %w", err)
		}
	}
	params := db.CreateVerifiedUserParams{
		DisplayName: profile.DisplayName,
		AvatarUrl:   text(profile.AvatarURL),
		Email:       text(profile.Email),
	}
	if profile.EmailVerified {
		user, err := q.CreateVerifiedUser(ctx, params)
		if err != nil {
			return db.User{}, classify(err)
		}
		return user, nil
	}
	user, err := q.CreateUser(ctx, db.CreateUserParams{
		DisplayName: profile.DisplayName,
		AvatarUrl:   text(profile.AvatarURL),
		Email:       text(profile.Email),
	})
	if err != nil {
		return db.User{}, classify(err)
	}
	return user, nil
}

func (r *Repository) CreateSession(ctx context.Context, userID string, tokenHash []byte, expiresAt time.Time, userAgent, ipAddress string) error {
	id, err := parseUUID(userID)
	if err != nil {
		return err
	}
	_, err = r.queries.CreateSession(ctx, db.CreateSessionParams{
		UserID:    id,
		TokenHash: tokenHash,
		ExpiresAt: pgtype.Timestamptz{Time: expiresAt, Valid: true},
		UserAgent: userAgent,
		IpAddress: ipAddress,
	})
	if err != nil {
		return fmt.Errorf("create session: %w", err)
	}
	return nil
}

func (r *Repository) UserBySession(ctx context.Context, tokenHash []byte) (domain.User, error) {
	user, err := r.queries.GetUserBySession(ctx, tokenHash)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.User{}, ErrNotFound
	}
	if err != nil {
		return domain.User{}, fmt.Errorf("get session user: %w", err)
	}
	roles, err := r.queries.ListUserRoles(ctx, user.ID)
	if err != nil {
		return domain.User{}, fmt.Errorf("list user roles: %w", err)
	}
	_ = r.queries.TouchSession(ctx, tokenHash)
	return toDomain(user, roles), nil
}

func (r *Repository) RevokeSession(ctx context.Context, tokenHash []byte) error {
	if err := r.queries.RevokeSession(ctx, tokenHash); err != nil {
		return fmt.Errorf("revoke session: %w", err)
	}
	return nil
}

func parseUUID(value string) (pgtype.UUID, error) {
	parsed, err := uuid.Parse(value)
	if err != nil {
		return pgtype.UUID{}, fmt.Errorf("parse user id: %w", err)
	}
	return pgtype.UUID{Bytes: parsed, Valid: true}, nil
}

func uuidString(value pgtype.UUID) string {
	if !value.Valid {
		return ""
	}
	return uuid.UUID(value.Bytes).String()
}

func text(value string) pgtype.Text {
	value = strings.TrimSpace(value)
	return pgtype.Text{String: value, Valid: value != ""}
}

func textString(value pgtype.Text) string {
	if !value.Valid {
		return ""
	}
	return value.String
}

func toDomain(user db.User, roles []db.UserRole) domain.User {
	return domain.User{
		ID:            uuidString(user.ID),
		DisplayName:   user.DisplayName,
		AvatarURL:     textString(user.AvatarUrl),
		Email:         textString(user.Email),
		EmailVerified: user.EmailVerifiedAt.Valid,
		Roles:         roleStrings(roles),
		CreatedAt:     user.CreatedAt.Time,
	}
}

func roleStrings(roles []db.UserRole) []string {
	values := make([]string, len(roles))
	for i, role := range roles {
		values[i] = string(role)
	}
	return values
}

func classify(err error) error {
	var postgresError *pgconn.PgError
	if errors.As(err, &postgresError) && postgresError.Code == "23505" {
		return ErrConflict
	}
	return err
}
