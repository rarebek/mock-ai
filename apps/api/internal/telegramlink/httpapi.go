package telegramlink

import (
	"encoding/json"
	"net/http"
	"strings"

	identity "github.com/own/mock-ai/apps/api/internal/identity/httpapi"
)

// Handler serves the student's Telegram connection:
//
//	GET /api/v1/me/telegram            {linked, started, results_enabled}
//	POST /api/v1/me/telegram/link      {token, expires_at} → app opens t.me/<bot>?start=l_<token>
//	PUT /api/v1/me/telegram/results    {enabled}
type Handler struct {
	s      *Service
	auth   *identity.Handler
	origin string
}

func NewHandler(s *Service, auth *identity.Handler, origin string) *Handler {
	return &Handler{s, auth, strings.TrimRight(origin, "/")}
}

func reply(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func failure(w http.ResponseWriter, status int, message string) {
	reply(w, status, map[string]any{"error": map[string]string{"message": message}})
}

func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	student := func(pattern string, fn func(w http.ResponseWriter, r *http.Request, userID string)) {
		mux.Handle(pattern, h.auth.RequireUser(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodGet && r.Header.Get("Origin") != h.origin {
				failure(w, http.StatusForbidden, "Telegram changes must originate from the student app")
				return
			}
			u, _ := identity.UserFromContext(r.Context())
			fn(w, r, u.ID)
		})))
	}
	student("GET /api/v1/me/telegram", func(w http.ResponseWriter, r *http.Request, userID string) {
		st, err := h.s.Status(r.Context(), userID)
		if err != nil {
			failure(w, 500, "Telegram status is temporarily unavailable")
			return
		}
		reply(w, 200, st)
	})
	student("POST /api/v1/me/telegram/link", func(w http.ResponseWriter, r *http.Request, userID string) {
		token, expires, err := h.s.CreateToken(r.Context(), userID)
		if err != nil {
			failure(w, 500, "A Telegram link could not be created")
			return
		}
		reply(w, 201, map[string]any{"token": token, "start": StartPrefix + token, "expires_at": expires})
	})
	student("PUT /api/v1/me/telegram/results", func(w http.ResponseWriter, r *http.Request, userID string) {
		var in struct {
			Enabled bool `json:"enabled"`
		}
		if json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&in) != nil {
			failure(w, 400, "Invalid request")
			return
		}
		st, err := h.s.SetResults(r.Context(), userID, in.Enabled)
		if err != nil {
			failure(w, 500, "The setting could not be saved")
			return
		}
		reply(w, 200, st)
	})
}
