package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/own/mock-ai/apps/api/internal/identity"
	"github.com/own/mock-ai/apps/api/internal/identity/domain"
	"github.com/own/mock-ai/apps/api/internal/identity/googleauth"
	"github.com/own/mock-ai/apps/api/internal/identity/telegram"
)

const (
	sessionCookieName = "mock_ai_session"
	googleCookieName  = "mock_ai_google_flow"
)

type Handler struct {
	service      *identity.Service
	google       *googleauth.Provider
	logger       *slog.Logger
	webOrigin    string
	secureCookie bool
}

type userContextKey struct{}

func New(service *identity.Service, google *googleauth.Provider, logger *slog.Logger, webOrigin string, secureCookie bool) *Handler {
	return &Handler{
		service:      service,
		google:       google,
		logger:       logger,
		webOrigin:    strings.TrimRight(webOrigin, "/"),
		secureCookie: secureCookie,
	}
}

func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/auth/telegram", h.loginTelegram)
	mux.HandleFunc("GET /api/v1/auth/telegram/link", h.loginLink)
	mux.HandleFunc("GET /api/v1/auth/google/start", h.googleStart)
	mux.HandleFunc("GET /api/v1/auth/google/callback", h.googleCallback)
	mux.HandleFunc("POST /api/v1/auth/logout", h.logout)
	mux.HandleFunc("GET /api/v1/me", h.me)
}

func (h *Handler) RequireUser(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie(sessionCookieName)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "unauthenticated", "authentication required")
			return
		}
		user, err := h.service.Authenticate(r.Context(), cookie.Value)
		if err != nil {
			h.clearSessionCookie(w)
			h.handleServiceError(w, err)
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), userContextKey{}, user)))
	})
}

func UserFromContext(ctx context.Context) (domain.User, bool) {
	user, ok := ctx.Value(userContextKey{}).(domain.User)
	return user, ok
}

// loginLink redeems a one-time link the Telegram bot sent. It is a GET because
// the student arrives by tapping a button in a chat, and it redirects straight
// away so the token does not linger in the address bar or get carried into a
// Referer header on the next navigation.
func (h *Handler) loginLink(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("token")
	if token == "" {
		h.redirectAuthResult(w, r, "login_link_invalid")
		return
	}
	_, session, err := h.service.LoginWithLink(r.Context(), token, r.UserAgent(), clientIP(r))
	if err != nil {
		// Never log the token: it is a bearer credential until it is spent.
		if !errors.Is(err, identity.ErrInvalidLoginLink) {
			h.logger.Error("Telegram login link failed", "error", err)
		}
		h.redirectAuthResult(w, r, "login_link_invalid")
		return
	}
	h.setSessionCookie(w, session)
	http.Redirect(w, r, h.webOrigin+"/?auth=success", http.StatusFound)
}

func (h *Handler) loginTelegram(w http.ResponseWriter, r *http.Request) {
	var input struct {
		InitData string `json:"init_data"`
	}
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	user, session, err := h.service.LoginTelegram(r.Context(), input.InitData, r.UserAgent(), clientIP(r))
	if err != nil {
		h.handleServiceError(w, err)
		return
	}
	h.setSessionCookie(w, session)
	writeJSON(w, http.StatusOK, map[string]any{"user": user})
}

func (h *Handler) googleStart(w http.ResponseWriter, r *http.Request) {
	authorizationURL, flowCookie, err := h.google.Start()
	if err != nil {
		h.handleServiceError(w, err)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     googleCookieName,
		Value:    flowCookie,
		Path:     "/api/v1/auth/google",
		MaxAge:   600,
		HttpOnly: true,
		Secure:   h.secureCookie,
		SameSite: http.SameSiteLaxMode,
	})
	http.Redirect(w, r, authorizationURL, http.StatusFound)
}

func (h *Handler) googleCallback(w http.ResponseWriter, r *http.Request) {
	flowCookie, err := r.Cookie(googleCookieName)
	if err != nil || r.URL.Query().Get("error") != "" {
		h.redirectAuthResult(w, r, "google_flow_failed")
		return
	}
	profile, err := h.google.Complete(r.Context(), r.URL.Query().Get("code"), r.URL.Query().Get("state"), flowCookie.Value)
	h.clearGoogleCookie(w)
	if err != nil {
		h.logger.Warn("Google authentication failed", "error", err)
		h.redirectAuthResult(w, r, "google_flow_failed")
		return
	}
	_, session, err := h.service.LoginExternal(r.Context(), profile, r.UserAgent(), clientIP(r))
	if err != nil {
		h.logger.Error("Google user login failed", "error", err)
		h.redirectAuthResult(w, r, "google_login_failed")
		return
	}
	h.setSessionCookie(w, session)
	http.Redirect(w, r, h.webOrigin+"/?auth=success", http.StatusFound)
}

func (h *Handler) logout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(sessionCookieName); err == nil {
		if err := h.service.Logout(r.Context(), cookie.Value); err != nil {
			h.logger.Error("logout failed", "error", err)
			writeError(w, http.StatusInternalServerError, "internal_error", "could not log out")
			return
		}
	}
	h.clearSessionCookie(w)
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) me(w http.ResponseWriter, r *http.Request) {
	cookie, err := r.Cookie(sessionCookieName)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "unauthenticated", "authentication required")
		return
	}
	user, err := h.service.Authenticate(r.Context(), cookie.Value)
	if err != nil {
		h.clearSessionCookie(w)
		h.handleServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"user": user})
}

func (h *Handler) handleServiceError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, identity.ErrInvalidInput):
		writeError(w, http.StatusUnprocessableEntity, "validation_error", err.Error())
	case errors.Is(err, identity.ErrInvalidLoginLink):
		writeError(w, http.StatusUnauthorized, "invalid_login_link", err.Error())
	case errors.Is(err, identity.ErrTooManyLoginLinks):
		writeError(w, http.StatusTooManyRequests, "too_many_login_links", err.Error())
	case errors.Is(err, identity.ErrInvalidSession):
		writeError(w, http.StatusUnauthorized, "unauthenticated", "authentication required")
	case errors.Is(err, identity.ErrTelegramDisabled), errors.Is(err, googleauth.ErrDisabled):
		writeError(w, http.StatusServiceUnavailable, "provider_unavailable", err.Error())
	case errors.Is(err, telegram.ErrInvalidData), errors.Is(err, telegram.ErrExpiredData):
		writeError(w, http.StatusUnauthorized, "invalid_telegram_data", "Telegram authentication data is invalid or expired")
	default:
		h.logger.Error("authentication request failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "authentication request failed")
	}
}

func (h *Handler) setSessionCookie(w http.ResponseWriter, session identity.Session) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    session.Token,
		Path:     "/",
		Expires:  session.ExpiresAt,
		MaxAge:   int(time.Until(session.ExpiresAt).Seconds()),
		HttpOnly: true,
		Secure:   h.secureCookie,
		SameSite: http.SameSiteLaxMode,
	})
}

func (h *Handler) clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   h.secureCookie,
		SameSite: http.SameSiteLaxMode,
	})
}

func (h *Handler) clearGoogleCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     googleCookieName,
		Value:    "",
		Path:     "/api/v1/auth/google",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   h.secureCookie,
		SameSite: http.SameSiteLaxMode,
	})
}

func (h *Handler) redirectAuthResult(w http.ResponseWriter, r *http.Request, code string) {
	target := h.webOrigin + "/?auth_error=" + url.QueryEscape(code)
	http.Redirect(w, r, target, http.StatusFound)
}

func decodeJSON(w http.ResponseWriter, r *http.Request, destination any) error {
	body := http.MaxBytesReader(w, r.Body, 1<<20)
	decoder := json.NewDecoder(body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return errors.New("request body must be valid JSON")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("request body must contain one JSON object")
	}
	return nil
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil {
		return host
	}
	return ""
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{
		"error": map[string]string{"code": code, "message": message},
	})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
