package postgres

import (
	"context"
	"github.com/jackc/pgx/v5/pgtype"
	app "github.com/own/mock-ai/apps/api/internal/exam/application"
	db "github.com/own/mock-ai/apps/api/internal/platform/postgres/sqlc"
)

func (r *Repository) ListPublishedContent(ctx context.Context, u, kind string) ([]app.ManagedContent, error) {
	rows, e := r.queries.ListPublishedContent(ctx, db.ListPublishedContentParams{UserID: mustUUID(u), Kind: kind})
	if e != nil {
		return nil, e
	}
	out := make([]app.ManagedContent, 0, len(rows))
	for _, v := range rows {
		item := app.ManagedContent{ID: uuidString(v.ID), Kind: v.Kind, Slug: v.Slug, Title: v.Title, Summary: v.Summary, SortOrder: v.SortOrder, Data: v.Data}
		if v.StartsAt.Valid {
			t := v.StartsAt.Time
			item.StartsAt = &t
		}
		if v.EndsAt.Valid {
			t := v.EndsAt.Time
			item.EndsAt = &t
		}
		out = append(out, item)
	}
	return out, nil
}

// mustUUID narrows a caller-supplied id, returning the zero UUID when it is not
// one. A zero viewer matches no user row, so a malformed id sees nothing rather
// than everything.
func mustUUID(value string) pgtype.UUID {
	id, err := parseUUID(value)
	if err != nil {
		return pgtype.UUID{}
	}
	return id
}
