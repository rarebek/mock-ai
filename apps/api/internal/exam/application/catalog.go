package application

import (
	"context"
	"encoding/json"
	"time"
)

type ManagedContent struct {
	ID        string          `json:"id"`
	Kind      string          `json:"kind"`
	Slug      string          `json:"slug"`
	Title     string          `json:"title"`
	Summary   string          `json:"summary"`
	SortOrder int32           `json:"sort_order"`
	StartsAt  *time.Time      `json:"starts_at,omitempty"`
	EndsAt    *time.Time      `json:"ends_at,omitempty"`
	Data      json.RawMessage `json:"data"`
}

func (s *Service) ListPublishedContent(ctx context.Context, u, kind string) ([]ManagedContent, error) {
	if !isPublishedContentKind(kind) {
		return nil, ErrNotFound
	}
	return s.repository.ListPublishedContent(ctx, u, kind)
}

func isPublishedContentKind(kind string) bool {
	switch kind {
	case "daily_challenge", "achievement", "vocabulary", "writing_prompt", "speaking_plan":
		return true
	default:
		return false
	}
}
