package httpapi

import (
	"context"
	"encoding/json"
	"github.com/coder/websocket"
	identity "github.com/own/mock-ai/apps/api/internal/identity/httpapi"
	d "github.com/own/mock-ai/apps/api/internal/speaking/domain"
	"net/http"
	"time"
)

func (h *Handler) registerLive(mux *http.ServeMux) {
	protect := func(f http.HandlerFunc) http.Handler {
		return h.auth.RequireUser(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Cache-Control", "private, no-store")
			if r.Header.Get("Origin") != h.origin {
				http.Error(w, "Use the student app", 403)
				return
			}
			if h.Live == nil || h.Live.Provider == nil {
				fail(w, d.ErrUnavailable)
				return
			}
			f(w, r)
		}))
	}
	mux.Handle("POST /api/v1/speaking/live/sessions", protect(func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Consent bool   `json:"consent"`
			PlanID  string `json:"plan_id"`
			Part    int32  `json:"part"`
		}
		if json.NewDecoder(http.MaxBytesReader(w, r.Body, 1000)).Decode(&input) != nil {
			fail(w, d.ErrInvalid)
			return
		}
		if !input.Consent {
			fail(w, d.ErrConsent)
			return
		}
		ready, e := h.s.Repo.Available(r.Context())
		if e != nil || !ready {
			fail(w, d.ErrUnavailable)
			return
		}
		ex, e := d.RandomExaminer()
		if e != nil {
			fail(w, e)
			return
		}
		u, _ := identity.UserFromContext(r.Context())
		// The live examiner is a model talking. A centre that has AI switched
		// off does not get one; its students use the read-and-record
		// interview, which never reaches a provider.
		if ai, e := h.s.Repo.AIEnabled(r.Context(), u.ID); e != nil || !ai {
			fail(w, d.ErrUnavailable)
			return
		}
		topic, turns, e := h.s.Repo.Plan(r.Context(), u.ID, input.PlanID)
		if e != nil {
			fail(w, e)
			return
		}
		topic, turns, e = d.FocusPlan(topic, turns, input.Part)
		if e != nil {
			fail(w, e)
			return
		}
		for i := range turns {
			if turns[i].Part != 2 {
				turns[i].Seconds = 120
			}
		}
		v, e := h.Live.Repo.CreateLive(r.Context(), u.ID, h.s.Model, h.Live.Model, ex, topic, turns)
		if e != nil {
			fail(w, e)
			return
		}
		reply(w, 201, v)
	}))
	mux.Handle("GET /api/v1/speaking/live/sessions/{id}/stream", protect(func(w http.ResponseWriter, r *http.Request) {
		u, _ := identity.UserFromContext(r.Context())
		session, e := h.Live.Repo.ClaimLive(r.Context(), u.ID, r.PathValue("id"))
		if e != nil {
			fail(w, e)
			return
		}
		// Origin was checked against the exact configured student origin above.
		conn, e := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if e != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = h.Live.Repo.ReleaseLive(ctx, session)
			return
		}
		defer conn.CloseNow()
		if e = h.Live.Run(r.Context(), conn, session); e != nil {
			_ = conn.Close(websocket.StatusGoingAway, "Connection paused. Completed answers are saved. Reconnect to repeat the unfinished answer.")
		} else {
			_ = conn.Close(websocket.StatusNormalClosure, "Interview complete")
		}
	}))
}
