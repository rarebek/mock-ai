package domain

import "time"

type User struct {
	ID            string    `json:"id"`
	DisplayName   string    `json:"display_name"`
	AvatarURL     string    `json:"avatar_url,omitempty"`
	Email         string    `json:"email,omitempty"`
	EmailVerified bool      `json:"email_verified"`
	Roles         []string  `json:"roles"`
	CreatedAt     time.Time `json:"created_at"`
}

// Centre is a learning centre: a tenant whose content is private to its own
// students. A student with no centre sees the global library.
type Centre struct {
	ID            string `json:"id"`
	Slug          string `json:"slug"`
	Name          string `json:"name"`
	IncludeGlobal bool   `json:"include_global"`
}

type ExternalProfile struct {
	Provider      string
	Subject       string
	DisplayName   string
	AvatarURL     string
	Email         string
	EmailVerified bool
	RawProfile    []byte
}
