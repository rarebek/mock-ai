package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	app "github.com/own/mock-ai/apps/api/internal/admin/application"
	"net/http"
	"strings"
)

type contextKey struct{}

const CookieName = "mock_ai_admin_session"

type Handler struct {
	service *app.Service
	origin  string
	secure  bool
	slots   chan struct{}
}

func New(s *app.Service, origin string, secure bool) *Handler {
	return &Handler{s, strings.TrimRight(origin, "/"), secure, make(chan struct{}, 4)}
}
func AdminFromContext(ctx context.Context) app.Admin {
	a, _ := ctx.Value(contextKey{}).(app.Admin)
	return a
}
func respond(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if v != nil {
		_ = json.NewEncoder(w).Encode(v)
	}
}
func fail(w http.ResponseWriter, status int, message string) {
	respond(w, status, map[string]any{"error": map[string]string{"message": message}})
}
func (h *Handler) sameOrigin(w http.ResponseWriter, r *http.Request) bool {
	if r.Method == "GET" || r.Method == "HEAD" {
		return true
	}
	if r.Header.Get("Origin") != h.origin {
		fail(w, 403, "Admin requests must originate from the admin panel")
		return false
	}
	return true
}
func (h *Handler) RequireAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !h.sameOrigin(w, r) {
			return
		}
		cookie, e := r.Cookie(CookieName)
		if e != nil {
			fail(w, 401, "Admin sign-in required")
			return
		}
		a, e := h.service.Session(r.Context(), cookie.Value)
		if e != nil {
			fail(w, 401, "Admin sign-in required")
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), contextKey{}, a)))
	})
}
func (h *Handler) cookie(w http.ResponseWriter, value string, age int) {
	http.SetCookie(w, &http.Cookie{Name: CookieName, Value: value, Path: "/api/v1/admin", MaxAge: age, HttpOnly: true, Secure: h.secure, SameSite: http.SameSiteStrictMode})
}
func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/admin/auth/login", func(w http.ResponseWriter, r *http.Request) {
		if !h.sameOrigin(w, r) {
			return
		}
		select {
		case h.slots <- struct{}{}:
			defer func() { <-h.slots }()
		default:
			fail(w, 429, "Please try again shortly")
			return
		}
		var input struct {
			Email    string `json:"email"`
			Password string `json:"password"`
		}
		if e := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&input); e != nil {
			fail(w, 400, "Invalid login request")
			return
		}
		a, token, e := h.service.Login(r.Context(), input.Email, input.Password)
		if e != nil {
			if errors.Is(e, app.ErrUnauthorized) {
				fail(w, 401, e.Error())
			} else {
				fail(w, 500, "Admin login unavailable")
			}
			return
		}
		// Signing in again replaces the current browser's admin session.
		if previous, e := r.Cookie(CookieName); e == nil {
			_ = h.service.Logout(r.Context(), previous.Value)
		}
		h.cookie(w, token, 8*60*60)
		respond(w, 200, map[string]any{"admin": a})
	})
	mux.Handle("GET /api/v1/admin/me", h.RequireAdmin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		respond(w, 200, map[string]any{"admin": AdminFromContext(r.Context())})
	})))
	mux.Handle("GET /api/v1/admin/dashboard", h.RequireAdmin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		v, e := h.service.Dashboard(r.Context(), AdminFromContext(r.Context()).ID)
		if e != nil {
			fail(w, 500, "Dashboard unavailable")
			return
		}
		respond(w, 200, v)
	})))
	mux.Handle("GET /api/v1/admin/students", h.RequireAdmin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		students, e := h.service.ListStudents(r.Context(), AdminFromContext(r.Context()).ID)
		if e != nil {
			fail(w, 500, "Students could not be loaded")
			return
		}
		respond(w, 200, map[string]any{"students": students})
	})))
	mux.Handle("GET /api/v1/admin/students/{student_id}", h.RequireAdmin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		student, e := h.service.StudentProgress(r.Context(), AdminFromContext(r.Context()).ID, r.PathValue("student_id"))
		if e != nil {
			if errors.Is(e, app.ErrStudentNotFound) {
				fail(w, 404, e.Error())
			} else {
				fail(w, 500, "Student progress could not be loaded")
			}
			return
		}
		respond(w, 200, student)
	})))
	// Learning centres. Only a platform administrator reaches these; a centre's
	// own administrator is scoped to their centre everywhere else.
	mux.Handle("GET /api/v1/admin/centres", h.RequireAdmin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		v, e := h.service.ListCentres(r.Context(), AdminFromContext(r.Context()))
		if e != nil {
			h.failCentre(w, e)
			return
		}
		respond(w, 200, map[string]any{"centres": v})
	})))
	mux.Handle("POST /api/v1/admin/centres", h.RequireAdmin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Name          string `json:"name"`
			Slug          string `json:"slug"`
			IncludeGlobal *bool  `json:"include_global"`
		}
		if !decode(w, r, &input) {
			return
		}
		// A centre that forgets to say otherwise gets the global library, so
		// its students open a working app rather than an empty one.
		includeGlobal := input.IncludeGlobal == nil || *input.IncludeGlobal
		v, e := h.service.CreateCentre(r.Context(), AdminFromContext(r.Context()), input.Name, input.Slug, includeGlobal)
		if e != nil {
			h.failCentre(w, e)
			return
		}
		respond(w, 201, map[string]any{"centre": v})
	})))
	mux.Handle("PUT /api/v1/admin/centres/{centre_id}", h.RequireAdmin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Name          string `json:"name"`
			IncludeGlobal bool   `json:"include_global"`
			Disabled      bool   `json:"disabled"`
		}
		if !decode(w, r, &input) {
			return
		}
		v, e := h.service.UpdateCentre(r.Context(), AdminFromContext(r.Context()), r.PathValue("centre_id"), input.Name, input.IncludeGlobal, input.Disabled)
		if e != nil {
			h.failCentre(w, e)
			return
		}
		respond(w, 200, map[string]any{"centre": v})
	})))
	// A centre's administrators. Provisioning these was a SQL job; it is the
	// one step between creating a centre and handing it over, so it belongs on
	// the same screen as the centre itself.
	mux.Handle("GET /api/v1/admin/centres/{centre_id}/admins", h.RequireAdmin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		v, e := h.service.ListCentreAdmins(r.Context(), AdminFromContext(r.Context()), r.PathValue("centre_id"))
		if e != nil {
			h.failCentre(w, e)
			return
		}
		respond(w, 200, map[string]any{"admins": v})
	})))
	mux.Handle("POST /api/v1/admin/centres/{centre_id}/admins", h.RequireAdmin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Email       string `json:"email"`
			DisplayName string `json:"display_name"`
		}
		if !decode(w, r, &input) {
			return
		}
		v, e := h.service.CreateCentreAdmin(r.Context(), AdminFromContext(r.Context()), r.PathValue("centre_id"), input.Email, input.DisplayName)
		if e != nil {
			h.failCentre(w, e)
			return
		}
		respond(w, 201, map[string]any{"admin": v})
	})))
	mux.Handle("PUT /api/v1/admin/centres/{centre_id}/admins/{admin_id}", h.RequireAdmin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Disabled      bool `json:"disabled"`
			ResetPassword bool `json:"reset_password"`
		}
		if !decode(w, r, &input) {
			return
		}
		actor, centre, admin := AdminFromContext(r.Context()), r.PathValue("centre_id"), r.PathValue("admin_id")
		if input.ResetPassword {
			v, e := h.service.ResetCentreAdminPassword(r.Context(), actor, centre, admin)
			if e != nil {
				h.failCentre(w, e)
				return
			}
			respond(w, 200, map[string]any{"admin": v})
			return
		}
		if e := h.service.SetCentreAdminState(r.Context(), actor, centre, admin, input.Disabled); e != nil {
			h.failCentre(w, e)
			return
		}
		w.WriteHeader(204)
	})))
	mux.Handle("GET /api/v1/admin/managed-content", h.RequireAdmin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		v, e := h.service.ListContent(r.Context(), AdminFromContext(r.Context()).ID, r.URL.Query().Get("kind"))
		if e != nil {
			fail(w, 422, e.Error())
			return
		}
		respond(w, 200, map[string]any{"items": v})
	})))
	mux.Handle("POST /api/v1/admin/managed-content", h.RequireAdmin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { h.saveContent(w, r, false) })))
	mux.Handle("PUT /api/v1/admin/managed-content/{content_id}", h.RequireAdmin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { h.saveContent(w, r, true) })))
	mux.Handle("DELETE /api/v1/admin/managed-content/{content_id}", h.RequireAdmin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if e := h.service.DeleteContent(r.Context(), AdminFromContext(r.Context()).ID, r.PathValue("content_id")); e != nil {
			if errors.Is(e, app.ErrContentNotFound) {
				fail(w, 404, e.Error())
			} else {
				fail(w, 500, "Could not delete content")
			}
			return
		}
		respond(w, 204, nil)
	})))
	mux.HandleFunc("POST /api/v1/admin/auth/logout", func(w http.ResponseWriter, r *http.Request) {
		if !h.sameOrigin(w, r) {
			return
		}
		if c, e := r.Cookie(CookieName); e == nil {
			if e = h.service.Logout(r.Context(), c.Value); e != nil {
				fail(w, 500, "Could not sign out")
				return
			}
		}
		h.cookie(w, "", -1)
		respond(w, 204, nil)
	})
}

func (h *Handler) saveContent(w http.ResponseWriter, r *http.Request, update bool) {
	var input app.ManagedContent
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 110000))
	decoder.DisallowUnknownFields()
	if e := decoder.Decode(&input); e != nil {
		fail(w, 400, "Invalid content request")
		return
	}
	if update {
		input.ID = r.PathValue("content_id")
	} else {
		input.ID = ""
	}
	admin := AdminFromContext(r.Context())
	saved, e := h.service.SaveContent(r.Context(), admin.ID, input)
	if e != nil {
		if errors.Is(e, app.ErrInvalidContent) {
			fail(w, 422, e.Error())
		} else {
			fail(w, 500, "Could not save content")
		}
		return
	}
	status := 200
	if !update {
		status = 201
	}
	respond(w, status, saved)
}

// decode reads a small JSON body, answering the client itself when it cannot.
func decode(w http.ResponseWriter, r *http.Request, into any) bool {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4000))
	decoder.DisallowUnknownFields()
	if decoder.Decode(into) != nil {
		fail(w, 400, "Invalid request")
		return false
	}
	return true
}

func (h *Handler) failCentre(w http.ResponseWriter, e error) {
	switch {
	case errors.Is(e, app.ErrNotPlatformAdmin):
		fail(w, 403, e.Error())
	case errors.Is(e, app.ErrInvalidCentre), errors.Is(e, app.ErrCentreTaken):
		fail(w, 422, e.Error())
	case errors.Is(e, app.ErrCentreNotFound), errors.Is(e, app.ErrAdminNotFound):
		fail(w, 404, e.Error())
	case errors.Is(e, app.ErrAdminEmailTaken):
		fail(w, 409, e.Error())
	case errors.Is(e, app.ErrInvalid):
		fail(w, 422, e.Error())
	default:
		fail(w, 500, "Learning centre request failed")
	}
}
