package httpapi

import (
	"encoding/json"
	"errors"
	adminhttp "github.com/own/mock-ai/apps/api/internal/admin/httpapi"
	"io"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/own/mock-ai/apps/api/internal/exam/application"
	identityhttp "github.com/own/mock-ai/apps/api/internal/identity/httpapi"
)

type Handler struct {
	service *application.Service
	auth    *identityhttp.Handler
	logger  *slog.Logger
	admins  *adminhttp.Handler
}

func New(service *application.Service, auth *identityhttp.Handler, logger *slog.Logger, admins ...*adminhttp.Handler) *Handler {
	h := &Handler{service: service, auth: auth, logger: logger}
	if len(admins) > 0 {
		h.admins = admins[0]
	}
	return h
}

func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	h.registerLearning(mux)
	handle := func(pattern string, handler http.HandlerFunc) {
		mux.Handle(pattern, h.auth.RequireUser(handler))
	}
	handle("GET /api/v1/exams", h.listExams)
	handle("GET /api/v1/exams/{exam_id}", h.getExam)
	handle("POST /api/v1/exams/{exam_id}/attempts", h.startAttempt)
	handle("GET /api/v1/attempts/{attempt_id}", h.getAttempt)
	handle("GET /api/v1/attempts/{attempt_id}/review", h.getAttemptReview)
	handle("POST /api/v1/attempts/{attempt_id}/reviewed", h.markAttemptReviewed)
	handle("PUT /api/v1/attempts/{attempt_id}/answers/{question_id}", h.saveAnswer)
	handle("POST /api/v1/attempts/{attempt_id}/submit", h.submitAttempt)
	handle("GET /api/v1/me/attempts", h.history)
	handle("GET /api/v1/me/progress", h.progress)
	handle("GET /api/v1/practice/questions", h.listPracticeQuestions)
	handle("GET /api/v1/content", h.listPublishedContent)
	handle("POST /api/v1/practice/questions/{question_id}/check", h.checkPracticeAnswer)
	handle("GET /api/v1/me/study-plan", h.getStudyPlan)
	handle("PUT /api/v1/me/study-profile", h.updateStudyProfile)
}

func (h *Handler) listPublishedContent(w http.ResponseWriter, r *http.Request) {
	user, _ := identityhttp.UserFromContext(r.Context())
	items, e := h.service.ListPublishedContent(r.Context(), user.ID, r.URL.Query().Get("kind"))
	if e != nil {
		h.fail(w, e)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (h *Handler) listPracticeQuestions(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.ParseInt(r.URL.Query().Get("limit"), 10, 32)
	user, _ := identityhttp.UserFromContext(r.Context())
	questions, err := h.service.ListPracticeQuestions(
		r.Context(), user.ID, r.URL.Query().Get("skill"), r.URL.Query().Get("type"), int32(limit),
	)
	if err != nil {
		h.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"questions": questions})
}

func (h *Handler) checkPracticeAnswer(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Values []string `json:"values"`
	}
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	user, _ := identityhttp.UserFromContext(r.Context())
	feedback, err := h.service.CheckPracticeAnswer(r.Context(), user.ID, r.PathValue("question_id"), input.Values)
	if err != nil {
		h.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"feedback": feedback})
}

func (h *Handler) getStudyPlan(w http.ResponseWriter, r *http.Request) {
	user, _ := identityhttp.UserFromContext(r.Context())
	plan, err := h.service.GetStudyPlan(r.Context(), user.ID)
	if err != nil {
		h.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"plan": plan})
}

func (h *Handler) updateStudyProfile(w http.ResponseWriter, r *http.Request) {
	var input application.StudyProfileInput
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	user, _ := identityhttp.UserFromContext(r.Context())
	profile, err := h.service.UpdateStudyProfile(r.Context(), user.ID, input)
	if err != nil {
		h.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"profile": profile})
}

func (h *Handler) getAttemptReview(w http.ResponseWriter, r *http.Request) {
	user, _ := identityhttp.UserFromContext(r.Context())
	review, err := h.service.GetAttemptReview(r.Context(), user.ID, r.PathValue("attempt_id"))
	if err != nil {
		h.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"review": review})
}

func (h *Handler) markAttemptReviewed(w http.ResponseWriter, r *http.Request) {
	user, _ := identityhttp.UserFromContext(r.Context())
	if err := h.service.MarkAttemptReviewed(r.Context(), user.ID, r.PathValue("attempt_id")); err != nil {
		h.fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) progress(w http.ResponseWriter, r *http.Request) {
	user, _ := identityhttp.UserFromContext(r.Context())
	progress, err := h.service.GetProgress(r.Context(), user.ID)
	if err != nil {
		h.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"skills": progress})
}

func (h *Handler) listExams(w http.ResponseWriter, r *http.Request) {
	user, _ := identityhttp.UserFromContext(r.Context())
	exams, err := h.service.ListExams(r.Context(), user.ID)
	if err != nil {
		h.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"exams": exams})
}

func (h *Handler) getExam(w http.ResponseWriter, r *http.Request) {
	user, _ := identityhttp.UserFromContext(r.Context())
	exam, err := h.service.GetExam(r.Context(), user.ID, r.PathValue("exam_id"))
	if err != nil {
		h.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"exam": exam})
}

func (h *Handler) startAttempt(w http.ResponseWriter, r *http.Request) {
	user, _ := identityhttp.UserFromContext(r.Context())
	attempt, err := h.service.StartAttempt(r.Context(), user.ID, r.PathValue("exam_id"))
	if err != nil {
		h.fail(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"attempt": attempt})
}

func (h *Handler) getAttempt(w http.ResponseWriter, r *http.Request) {
	user, _ := identityhttp.UserFromContext(r.Context())
	attempt, err := h.service.GetAttempt(r.Context(), user.ID, r.PathValue("attempt_id"))
	if err != nil {
		h.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"attempt": attempt})
}

func (h *Handler) saveAnswer(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Values []string `json:"values"`
	}
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	user, _ := identityhttp.UserFromContext(r.Context())
	answer, err := h.service.SaveAnswer(r.Context(), user.ID, r.PathValue("attempt_id"), r.PathValue("question_id"), input.Values)
	if err != nil {
		h.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"answer": answer})
}

func (h *Handler) submitAttempt(w http.ResponseWriter, r *http.Request) {
	user, _ := identityhttp.UserFromContext(r.Context())
	attempt, err := h.service.SubmitAttempt(r.Context(), user.ID, r.PathValue("attempt_id"))
	if err != nil {
		h.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"attempt": attempt})
}

func (h *Handler) history(w http.ResponseWriter, r *http.Request) {
	user, _ := identityhttp.UserFromContext(r.Context())
	limit, _ := strconv.ParseInt(r.URL.Query().Get("limit"), 10, 32)
	offset, _ := strconv.ParseInt(r.URL.Query().Get("offset"), 10, 32)
	history, err := h.service.ListHistory(r.Context(), user.ID, int32(limit), int32(offset))
	if err != nil {
		h.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"attempts": history})
}

func (h *Handler) fail(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, application.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", err.Error())
	case errors.Is(err, application.ErrAttemptClosed):
		writeError(w, http.StatusConflict, "attempt_closed", err.Error())
	case errors.Is(err, application.ErrInvalidAnswer):
		writeError(w, http.StatusUnprocessableEntity, "invalid_answer", err.Error())
	case errors.Is(err, application.ErrInvalidProfile):
		writeError(w, http.StatusUnprocessableEntity, "invalid_profile", err.Error())
	case errors.Is(err, application.ErrImportUnavailable):
		writeError(w, http.StatusServiceUnavailable, "import_unavailable", err.Error())
	default:
		h.logger.Error("exam request failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "exam request failed")
	}
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

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{"error": map[string]string{"code": code, "message": message}})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
