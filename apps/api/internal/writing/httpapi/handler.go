package httpapi

import (
	"encoding/json"
	"errors"
	identity "github.com/own/mock-ai/apps/api/internal/identity/httpapi"
	"github.com/own/mock-ai/apps/api/internal/platform/credits"
	app "github.com/own/mock-ai/apps/api/internal/writing/application"
	"net/http"
	"strings"
)

type Handler struct {
	s      *app.Service
	auth   *identity.Handler
	origin string
}

func New(s *app.Service, a *identity.Handler, origin string) *Handler {
	return &Handler{s, a, strings.TrimRight(origin, "/")}
}
func reply(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if v != nil {
		_ = json.NewEncoder(w).Encode(withoutModelMetadata(v))
	}
}
func withoutModelMetadata(v any) any {
	b, err := json.Marshal(v)
	if err != nil {
		return v
	}
	var public any
	if json.Unmarshal(b, &public) != nil {
		return v
	}
	removeModelMetadata(public)
	return public
}
func removeModelMetadata(v any) {
	switch value := v.(type) {
	case map[string]any:
		delete(value, "model")
		delete(value, "model_version")
		delete(value, "live_model")
		for _, child := range value {
			removeModelMetadata(child)
		}
	case []any:
		for _, child := range value {
			removeModelMetadata(child)
		}
	}
}
func fail(w http.ResponseWriter, e error) {
	code := 500
	message := "Writing service unavailable"
	switch {
	case errors.Is(e, app.ErrNotFound):
		code = 404
	case errors.Is(e, app.ErrConflict):
		code = 409
	case errors.Is(e, app.ErrInvalid):
		code = 422
	case errors.Is(e, app.ErrLimit):
		code = 429
	case errors.Is(e, app.ErrUnavailable):
		code = 503
	case errors.Is(e, credits.ErrNoCredits):
		reply(w, http.StatusPaymentRequired, map[string]any{"error": map[string]string{"code": "no_practice_credits", "kind": "writing", "message": credits.Message(e)}})
		return
	}
	if code != 500 {
		message = e.Error()
	}
	reply(w, code, map[string]any{"error": map[string]string{"message": message}})
}
func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 32000)).Decode(v) != nil {
		reply(w, 400, map[string]any{"error": map[string]string{"message": "Invalid writing request"}})
		return false
	}
	return true
}
func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	handle := func(p string, f http.HandlerFunc) {
		mux.Handle(p, h.auth.RequireUser(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != "GET" && r.Header.Get("Origin") != h.origin {
				reply(w, 403, map[string]any{"error": map[string]string{"message": "Writing changes must originate from the student app"}})
				return
			}
			f(w, r)
		})))
	}
	handle("GET /api/v1/writing/settings", func(w http.ResponseWriter, r *http.Request) {
		ready, e := h.s.Repo.Available(r.Context())
		if e != nil {
			fail(w, e)
			return
		}
		reply(w, 200, map[string]any{"enabled": ready && h.s.Evaluator != nil, "daily_limit": 5, "task": "task2"})
	})
	handle("GET /api/v1/writing/submissions", func(w http.ResponseWriter, r *http.Request) {
		u, _ := identity.UserFromContext(r.Context())
		v, e := h.s.Repo.List(r.Context(), u.ID)
		if e != nil {
			fail(w, e)
			return
		}
		reply(w, 200, map[string]any{"submissions": v})
	})
	handle("POST /api/v1/writing/submissions", func(w http.ResponseWriter, r *http.Request) {
		var i app.Input
		if !decode(w, r, &i) {
			return
		}
		u, _ := identity.UserFromContext(r.Context())
		v, e := h.s.Create(r.Context(), u.ID, i)
		if e != nil {
			fail(w, e)
			return
		}
		reply(w, 201, v)
	})
	handle("GET /api/v1/writing/submissions/{id}", func(w http.ResponseWriter, r *http.Request) {
		u, _ := identity.UserFromContext(r.Context())
		v, e := h.s.Repo.Get(r.Context(), u.ID, r.PathValue("id"))
		if e != nil {
			fail(w, e)
			return
		}
		reply(w, 200, v)
	})
	handle("PUT /api/v1/writing/submissions/{id}", func(w http.ResponseWriter, r *http.Request) {
		var i app.Input
		if !decode(w, r, &i) {
			return
		}
		u, _ := identity.UserFromContext(r.Context())
		v, e := h.s.Save(r.Context(), u.ID, r.PathValue("id"), i)
		if e != nil {
			fail(w, e)
			return
		}
		reply(w, 200, v)
	})
	handle("DELETE /api/v1/writing/submissions/{id}", func(w http.ResponseWriter, r *http.Request) {
		u, _ := identity.UserFromContext(r.Context())
		if e := h.s.Repo.Delete(r.Context(), u.ID, r.PathValue("id")); e != nil {
			fail(w, e)
			return
		}
		reply(w, 204, nil)
	})
	handle("POST /api/v1/writing/submissions/{id}/evaluate", func(w http.ResponseWriter, r *http.Request) {
		var i struct {
			Revision int32  `json:"revision"`
			Consent  bool   `json:"consent"`
			FullMock string `json:"full_mock"`
		}
		if !decode(w, r, &i) {
			return
		}
		if !i.Consent {
			reply(w, 422, map[string]any{"error": map[string]string{"message": "Consent to process your essay and prompt for evaluation is required"}})
			return
		}
		u, _ := identity.UserFromContext(r.Context())
		v, e := h.s.Submit(r.Context(), u.ID, r.PathValue("id"), i.Revision, i.Consent, i.FullMock)
		if e != nil {
			fail(w, e)
			return
		}
		reply(w, 202, v)
	})
}
