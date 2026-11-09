package httpapi

import (
	"bytes"
	"encoding/json"
	"errors"
	identity "github.com/own/mock-ai/apps/api/internal/identity/httpapi"
	"github.com/own/mock-ai/apps/api/internal/platform/credits"
	app "github.com/own/mock-ai/apps/api/internal/speaking/application"
	d "github.com/own/mock-ai/apps/api/internal/speaking/domain"
	"github.com/own/mock-ai/apps/api/internal/speaking/live"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type Handler struct {
	s      *app.Service
	auth   *identity.Handler
	origin string
	Live   *live.Service
}

func New(s *app.Service, auth *identity.Handler, origin string) *Handler {
	return &Handler{s: s, auth: auth, origin: strings.TrimRight(origin, "/")}
}
func reply(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
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
	message := "Speaking service unavailable"
	switch {
	case errors.Is(e, d.ErrNotFound):
		code = 404
	case errors.Is(e, d.ErrConflict):
		code = 409
	case errors.Is(e, d.ErrInvalid), errors.Is(e, d.ErrConsent):
		code = 422
	case errors.Is(e, d.ErrLimit):
		code = 429
	case errors.Is(e, d.ErrUnavailable):
		code = 503
	case errors.Is(e, credits.ErrNoCredits):
		reply(w, http.StatusPaymentRequired, map[string]any{"error": map[string]string{"code": "no_practice_credits", "kind": "speaking", "message": credits.Message(e)}})
		return
	}
	if code != 500 {
		message = e.Error()
	}
	reply(w, code, map[string]any{"error": map[string]string{"message": message}})
}
func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	h.registerLive(mux)
	handle := func(path string, f http.HandlerFunc) {
		mux.Handle(path, h.auth.RequireUser(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Cache-Control", "private, no-store")
			if r.Method != "GET" && r.Header.Get("Origin") != h.origin {
				reply(w, 403, map[string]any{"error": map[string]string{"message": "Use the student app to change speaking sessions"}})
				return
			}
			f(w, r)
		})))
	}
	handle("GET /api/v1/speaking/settings", func(w http.ResponseWriter, r *http.Request) {
		ready, e := h.s.Repo.Available(r.Context())
		if e != nil {
			fail(w, e)
			return
		}
		u, _ := identity.UserFromContext(r.Context())
		ai, e := h.s.Repo.AIEnabled(r.Context(), u.ID)
		if e != nil {
			fail(w, e)
			return
		}
		// Two different questions. "live" is whether this student gets the
		// spoken examiner, which needs both a provider and a centre that uses
		// AI. "enabled" is whether they can practise speaking at all: a centre
		// with AI switched off still reads the questions on screen and records,
		// and needs no provider for that.
		live := ready && h.s.Provider != nil && ai
		reply(w, 200, map[string]any{
			"enabled": live || !ai, "live": live,
			"countries": d.Countries, "daily_limit": 3, "audio_retention_days": 30,
		})
	})
	handle("GET /api/v1/speaking/sessions", func(w http.ResponseWriter, r *http.Request) {
		u, _ := identity.UserFromContext(r.Context())
		v, e := h.s.Repo.List(r.Context(), u.ID)
		if e != nil {
			fail(w, e)
			return
		}
		reply(w, 200, map[string]any{"sessions": v})
	})
	handle("POST /api/v1/speaking/sessions", func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Consent  bool   `json:"consent"`
			PlanID   string `json:"plan_id"`
			Part     int32  `json:"part"`
			FullMock string `json:"full_mock"`
		}
		if json.NewDecoder(http.MaxBytesReader(w, r.Body, 1000)).Decode(&input) != nil {
			fail(w, d.ErrInvalid)
			return
		}
		u, _ := identity.UserFromContext(r.Context())
		v, e := h.s.Create(r.Context(), u.ID, input.Consent, input.PlanID, input.Part, input.FullMock)
		if e != nil {
			fail(w, e)
			return
		}
		reply(w, 201, v)
	})
	handle("GET /api/v1/speaking/sessions/{id}", func(w http.ResponseWriter, r *http.Request) {
		u, _ := identity.UserFromContext(r.Context())
		v, e := h.s.Repo.Get(r.Context(), u.ID, r.PathValue("id"))
		if e != nil {
			fail(w, e)
			return
		}
		reply(w, 200, v)
	})
	handle("DELETE /api/v1/speaking/sessions/{id}", func(w http.ResponseWriter, r *http.Request) {
		u, _ := identity.UserFromContext(r.Context())
		e := h.s.Repo.Delete(r.Context(), u.ID, r.PathValue("id"))
		if e != nil {
			fail(w, e)
			return
		}
		reply(w, 204, nil)
	})
	handle("POST /api/v1/speaking/sessions/{id}/submit", func(w http.ResponseWriter, r *http.Request) {
		if h.s.Provider == nil {
			fail(w, d.ErrUnavailable)
			return
		}
		u, _ := identity.UserFromContext(r.Context())
		v, e := h.s.Repo.Queue(r.Context(), u.ID, r.PathValue("id"))
		if e != nil {
			fail(w, e)
			return
		}
		reply(w, 202, v)
	})
	position := func(w http.ResponseWriter, r *http.Request) (int32, bool) {
		n, e := strconv.Atoi(r.PathValue("position"))
		if e != nil || n < 0 || n > 10 {
			fail(w, d.ErrNotFound)
			return 0, false
		}
		return int32(n), true
	}
	for _, kind := range []string{"examiner", "recording"} {
		handle("GET /api/v1/speaking/sessions/{id}/turns/{position}/"+kind, func(w http.ResponseWriter, r *http.Request) {
			pos, ok := position(w, r)
			if !ok {
				return
			}
			u, _ := identity.UserFromContext(r.Context())
			audio, e := h.s.Repo.Audio(r.Context(), u.ID, r.PathValue("id"), pos, kind == "examiner")
			if e != nil {
				fail(w, e)
				return
			}
			w.Header().Set("Content-Type", "audio/wav")
			w.Header().Set("X-Content-Type-Options", "nosniff")
			http.ServeContent(w, r, "audio.wav", time.Time{}, bytes.NewReader(audio))
		})
	}
	for _, kind := range []string{"answer", "skip"} {
		method := "PUT"
		if kind == "skip" {
			method = "POST"
		}
		handle(method+" /api/v1/speaking/sessions/{id}/turns/{position}/"+kind, func(w http.ResponseWriter, r *http.Request) {
			pos, ok := position(w, r)
			if !ok {
				return
			}
			var audio []byte
			if kind == "answer" {
				_ = http.NewResponseController(w).SetReadDeadline(time.Now().Add(60 * time.Second))
				var e error
				audio, e = io.ReadAll(http.MaxBytesReader(w, r.Body, d.MaxAudio))
				if e != nil {
					fail(w, d.ErrInvalid)
					return
				}
			}
			u, _ := identity.UserFromContext(r.Context())
			v, e := h.s.Repo.Answer(r.Context(), u.ID, r.PathValue("id"), pos, audio, kind == "skip")
			if e != nil {
				fail(w, e)
				return
			}
			reply(w, 200, v)
		})
	}
	handle("POST /api/v1/speaking/sessions/{id}/turns/{position}/voice/retry", func(w http.ResponseWriter, r *http.Request) {
		pos, ok := position(w, r)
		if !ok {
			return
		}
		u, _ := identity.UserFromContext(r.Context())
		if e := h.s.Repo.RetryVoice(r.Context(), u.ID, r.PathValue("id"), pos); e != nil {
			fail(w, e)
			return
		}
		reply(w, 202, map[string]bool{"queued": true})
	})
}
