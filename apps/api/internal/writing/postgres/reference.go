package postgres

import (
	"context"
	"encoding/json"
	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/own/mock-ai/apps/api/internal/platform/postgres/sqlc"
	app "github.com/own/mock-ai/apps/api/internal/writing/application"
	"strings"
)

func (r *Repository) reference(ctx context.Context, user pgtype.UUID, input app.Input) ([]byte, error) {
	if input.ContentID == "" {
		return []byte("{}"), nil
	}
	contentID, err := id(input.ContentID)
	if err != nil {
		return nil, app.ErrInvalid
	}
	raw, err := r.q.GetPublishedWritingReference(ctx, contentID)
	if err != nil {
		return nil, app.ErrNotFound
	}
	var content struct {
		Task   string   `json:"task"`
		Prompt string   `json:"prompt"`
		Images []string `json:"source_image_urls"`
	}
	if json.Unmarshal(raw, &content) != nil || content.Prompt != input.Prompt || (content.Task != "task_1" && content.Task != "task_2") {
		return nil, app.ErrInvalid
	}
	reference := app.Reference{Task: content.Task}
	if len(content.Images) > 5 {
		return nil, app.ErrInvalid
	}
	total := 0
	for _, path := range content.Images {
		// Never fetch arbitrary URLs supplied through content: media must pass the
		// same owner/published-reference check used by the student media endpoint.
		if !strings.HasPrefix(path, "/api/v1/private-media/") {
			return nil, app.ErrInvalid
		}
		mediaID, err := id(strings.TrimPrefix(path, "/api/v1/private-media/"))
		if err != nil {
			return nil, app.ErrInvalid
		}
		media, err := r.q.GetPrivateMedia(ctx, db.GetPrivateMediaParams{ID: mediaID, OwnerUserID: user})
		if err != nil {
			return nil, app.ErrNotFound
		}
		total += len(media.Data)
		if total > 8*1024*1024 || (media.MimeType != "image/png" && media.MimeType != "image/jpeg" && media.MimeType != "image/webp") {
			return nil, app.ErrInvalid
		}
		reference.Images = append(reference.Images, app.ReferenceImage{MIME: media.MimeType, Data: media.Data})
	}
	return json.Marshal(reference)
}
