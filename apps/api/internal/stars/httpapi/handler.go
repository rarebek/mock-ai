// Package httpapi serves stars to the student app and receives payment
// provider callbacks.
//
//	GET  /api/v1/stars                     balance, packages, whether sales are open
//	POST /api/v1/stars/spend               {purpose, reference} → new balance (402 when empty)
//	POST /api/v1/stars/purchases           {package_id} → checkout_url (503 while payments are off)
//	GET  /api/v1/stars/purchases/{id}      one purchase, for the after-payment page
//	POST /api/v1/payments/{provider}/callback   provider → server, no student session
package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	identity "github.com/own/mock-ai/apps/api/internal/identity/httpapi"
	app "github.com/own/mock-ai/apps/api/internal/stars/application"
)

type Handler struct {
	s      *app.Service
	auth   *identity.Handler
	origin string
}

func New(s *app.Service, auth *identity.Handler, origin string) *Handler {
	return &Handler{s, auth, strings.TrimRight(origin, "/")}
}

func reply(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if v != nil {
		_ = json.NewEncoder(w).Encode(v)
	}
}

func fail(w http.ResponseWriter, err error) {
	status, code, message := 500, "stars_unavailable", "Stars are temporarily unavailable"
	switch {
	case errors.Is(err, app.ErrInsufficient):
		status, code = http.StatusPaymentRequired, "insufficient_stars"
	case errors.Is(err, app.ErrPaymentsOff):
		status, code = http.StatusServiceUnavailable, "payments_unavailable"
	case errors.Is(err, app.ErrNotFound):
		status, code = http.StatusNotFound, "not_found"
	case errors.Is(err, app.ErrUnknownPackage), errors.Is(err, app.ErrUnknownPurpose), errors.Is(err, app.ErrInvalidRef):
		status, code = http.StatusUnprocessableEntity, "invalid"
	}
	if status != 500 {
		message = err.Error()
	}
	reply(w, status, map[string]any{"error": map[string]string{"code": code, "message": message}})
}

func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	student := func(pattern string, fn func(w http.ResponseWriter, r *http.Request, userID string)) {
		mux.Handle(pattern, h.auth.RequireUser(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodGet && r.Header.Get("Origin") != h.origin {
				reply(w, http.StatusForbidden, map[string]any{"error": map[string]string{"code": "forbidden", "message": "Star changes must originate from the student app"}})
				return
			}
			u, _ := identity.UserFromContext(r.Context())
			fn(w, r, u.ID)
		})))
	}

	student("GET /api/v1/stars", func(w http.ResponseWriter, r *http.Request, userID string) {
		v, err := h.s.Overview(r.Context(), userID)
		if err != nil {
			fail(w, err)
			return
		}
		reply(w, 200, v)
	})

	student("POST /api/v1/stars/spend", func(w http.ResponseWriter, r *http.Request, userID string) {
		var in struct {
			Purpose   string `json:"purpose"`
			Reference string `json:"reference"`
		}
		if json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&in) != nil {
			fail(w, app.ErrInvalidRef)
			return
		}
		v, err := h.s.Spend(r.Context(), userID, in.Purpose, in.Reference)
		if err != nil {
			fail(w, err)
			return
		}
		reply(w, 200, v)
	})

	student("POST /api/v1/stars/purchases", func(w http.ResponseWriter, r *http.Request, userID string) {
		var in struct {
			PackageID string `json:"package_id"`
		}
		if json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&in) != nil {
			fail(w, app.ErrUnknownPackage)
			return
		}
		v, err := h.s.StartPurchase(r.Context(), userID, in.PackageID)
		if err != nil {
			fail(w, err)
			return
		}
		reply(w, 201, v)
	})

	student("GET /api/v1/stars/purchases/{id}", func(w http.ResponseWriter, r *http.Request, userID string) {
		v, err := h.s.Purchase(r.Context(), userID, r.PathValue("id"))
		if err != nil {
			fail(w, err)
			return
		}
		reply(w, 200, map[string]any{"purchase": v})
	})

	// Server-to-server: authenticated by the provider's own signature, which
	// the provider verifies inside HandleCallback. Only the configured
	// provider is reachable.
	mux.HandleFunc("POST /api/v1/payments/{provider}/callback", func(w http.ResponseWriter, r *http.Request) {
		p := h.s.Provider()
		if !p.Available() || r.PathValue("provider") != p.Name() {
			http.NotFound(w, r)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
		p.HandleCallback(w, r, h.s.Settler())
	})
}
