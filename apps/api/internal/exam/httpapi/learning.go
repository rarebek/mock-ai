package httpapi

import (
	"bytes"
	adminhttp "github.com/own/mock-ai/apps/api/internal/admin/httpapi"
	app "github.com/own/mock-ai/apps/api/internal/exam/application"
	identity "github.com/own/mock-ai/apps/api/internal/identity/httpapi"
	"io"
	"net/http"
	"path/filepath"
	"strings"
)

func (h *Handler) registerLearning(mux *http.ServeMux) {
	handle := func(path string, fn http.HandlerFunc) { mux.Handle(path, h.auth.RequireUser(fn)) }
	handle("GET /api/v1/private-media/{media_id}", func(w http.ResponseWriter, r *http.Request) {
		u, _ := identity.UserFromContext(r.Context())
		media, e := h.service.GetPrivateMedia(r.Context(), u.ID, r.PathValue("media_id"))
		if e != nil {
			h.fail(w, e)
			return
		}
		w.Header().Set("Content-Type", media.MimeType)
		w.Header().Set("Cache-Control", "private, max-age=3600")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		http.ServeContent(w, r, media.Filename, media.CreatedAt, bytes.NewReader(media.Data))
	})
	handle("GET /api/v1/me/community", func(w http.ResponseWriter, r *http.Request) {
		u, _ := identity.UserFromContext(r.Context())
		v, e := h.service.GetCommunity(r.Context(), u.ID)
		if e != nil {
			h.fail(w, e)
			return
		}
		writeJSON(w, 200, v)
	})
	handle("PUT /api/v1/me/community", func(w http.ResponseWriter, r *http.Request) {
		var p app.CommunityProfile
		if e := decodeJSON(w, r, &p); e != nil {
			writeError(w, 400, "invalid_request", e.Error())
			return
		}
		u, _ := identity.UserFromContext(r.Context())
		if e := h.service.SaveCommunityProfile(r.Context(), u.ID, p); e != nil {
			h.fail(w, e)
			return
		}
		w.WriteHeader(204)
	})
	handle("GET /api/v1/attempts/{attempt_id}/workspace", func(w http.ResponseWriter, r *http.Request) {
		u, _ := identity.UserFromContext(r.Context())
		v, e := h.service.GetWorkspace(r.Context(), u.ID, r.PathValue("attempt_id"))
		if e != nil {
			h.fail(w, e)
			return
		}
		writeJSON(w, 200, v)
	})
	handle("PUT /api/v1/attempts/{attempt_id}/workspace", func(w http.ResponseWriter, r *http.Request) {
		var v app.Workspace
		if e := decodeJSON(w, r, &v); e != nil {
			writeError(w, 400, "invalid_request", e.Error())
			return
		}
		u, _ := identity.UserFromContext(r.Context())
		if e := h.service.SaveWorkspace(r.Context(), u.ID, r.PathValue("attempt_id"), v); e != nil {
			h.fail(w, e)
			return
		}
		w.WriteHeader(204)
	})
	handle("GET /api/v1/me/mistakes", func(w http.ResponseWriter, r *http.Request) {
		u, _ := identity.UserFromContext(r.Context())
		v, e := h.service.ListMistakes(r.Context(), u.ID)
		if e != nil {
			h.fail(w, e)
			return
		}
		writeJSON(w, 200, map[string]any{"mistakes": v})
	})
	handle("POST /api/v1/me/mistakes/{question_id}/retry", func(w http.ResponseWriter, r *http.Request) {
		var v struct {
			Values []string `json:"values"`
		}
		if e := decodeJSON(w, r, &v); e != nil {
			writeError(w, 400, "invalid_request", e.Error())
			return
		}
		u, _ := identity.UserFromContext(r.Context())
		result, e := h.service.RetryMistake(r.Context(), u.ID, r.PathValue("question_id"), v.Values)
		if e != nil {
			h.fail(w, e)
			return
		}
		writeJSON(w, 200, map[string]any{"feedback": result})
	})
	handle("GET /api/v1/me/reminder", func(w http.ResponseWriter, r *http.Request) {
		u, _ := identity.UserFromContext(r.Context())
		v, e := h.service.GetReminder(r.Context(), u.ID)
		if e != nil {
			h.fail(w, e)
			return
		}
		writeJSON(w, 200, v)
	})
	handle("PUT /api/v1/me/reminder", func(w http.ResponseWriter, r *http.Request) {
		var v app.Reminder
		if e := decodeJSON(w, r, &v); e != nil {
			writeError(w, 400, "invalid_request", e.Error())
			return
		}
		u, _ := identity.UserFromContext(r.Context())
		if e := h.service.SaveReminder(r.Context(), u.ID, v); e != nil {
			h.fail(w, e)
			return
		}
		w.WriteHeader(204)
	})
	admin := func(fn http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if h.admins == nil {
				writeError(w, 401, "unauthorized", "Admin sign-in required")
				return
			}
			h.admins.RequireAdmin(fn).ServeHTTP(w, r)
		}
	}
	mux.HandleFunc("GET /api/v1/admin/content", admin(func(w http.ResponseWriter, r *http.Request) {
		v, e := h.service.ListDrafts(r.Context(), adminhttp.AdminFromContext(r.Context()).ID)
		if e != nil {
			h.fail(w, e)
			return
		}
		writeJSON(w, 200, map[string]any{"drafts": v})
	}))
	save := admin(func(w http.ResponseWriter, r *http.Request) {
		var v app.Content
		if e := decodeJSON(w, r, &v); e != nil {
			writeError(w, 400, "invalid_request", e.Error())
			return
		}
		u := adminhttp.AdminFromContext(r.Context())
		id, e := h.service.SaveDraft(r.Context(), u.ID, r.PathValue("draft_id"), v)
		if e != nil {
			h.fail(w, e)
			return
		}
		writeJSON(w, 200, map[string]string{"id": id})
	})
	mux.HandleFunc("POST /api/v1/admin/content", save)
	mux.HandleFunc("PUT /api/v1/admin/content/{draft_id}", save)
	mux.HandleFunc("POST /api/v1/admin/content/{draft_id}/publish", admin(func(w http.ResponseWriter, r *http.Request) {
		id, e := h.service.PublishDraft(r.Context(), r.PathValue("draft_id"))
		if e != nil {
			h.fail(w, e)
			return
		}
		writeJSON(w, 200, map[string]string{"exam_id": id})
	}))
	mux.HandleFunc("GET /api/v1/admin/content-imports", admin(func(w http.ResponseWriter, r *http.Request) {
		u := adminhttp.AdminFromContext(r.Context())
		imports, e := h.service.ListImports(r.Context(), u.ID)
		if e != nil {
			h.fail(w, e)
			return
		}
		writeJSON(w, 200, map[string]any{"imports": imports})
	}))
	mux.HandleFunc("POST /api/v1/admin/content-imports", admin(func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, app.MaxPDFBytes+(1<<20))
		if e := r.ParseMultipartForm(1 << 20); e != nil {
			writeError(w, 400, "invalid_upload", "Upload one PDF up to 50 MB")
			return
		}
		if r.MultipartForm != nil {
			defer r.MultipartForm.RemoveAll()
		}
		file, header, e := r.FormFile("file")
		if e != nil {
			writeError(w, 400, "invalid_upload", "Choose a PDF to import")
			return
		}
		defer file.Close()
		u := adminhttp.AdminFromContext(r.Context())
		id, e := h.service.CreateImport(r.Context(), u.ID, header.Filename, r.FormValue("skills"), r.FormValue("access_scope"), r.FormValue("owner_user_id"), r.FormValue("rights_confirmed") == "true", header.Size, file)
		if e != nil {
			h.fail(w, e)
			return
		}
		writeJSON(w, http.StatusAccepted, map[string]string{"id": id})
	}))
	mux.HandleFunc("POST /api/v1/admin/private-media", admin(func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, (26<<20)+(1<<20))
		if e := r.ParseMultipartForm(1 << 20); e != nil {
			writeError(w, 400, "invalid_upload", "Upload one MP3, PNG or JPEG up to 25 MB")
			return
		}
		if r.MultipartForm != nil {
			defer r.MultipartForm.RemoveAll()
		}
		file, header, e := r.FormFile("file")
		if e != nil {
			writeError(w, 400, "invalid_upload", "Choose an MP3 recording or a PNG or JPEG source page")
			return
		}
		defer file.Close()
		data, e := io.ReadAll(io.LimitReader(file, (25<<20)+1))
		mime := sniff(data, header.Filename)
		if e != nil || len(data) > 25<<20 || mime == "" {
			writeError(w, 400, "invalid_upload", "Upload a valid MP3, PNG or JPEG up to 25 MB")
			return
		}
		u := adminhttp.AdminFromContext(r.Context())
		id, e := h.service.SavePrivateMedia(r.Context(), u.ID, r.FormValue("owner_user_id"), header.Filename, mime, data)
		if e != nil {
			h.fail(w, e)
			return
		}
		writeJSON(w, http.StatusCreated, map[string]string{"id": id, "url": "/api/v1/private-media/" + id})
	}))
	mux.HandleFunc("GET /api/v1/admin/private-media/{media_id}", admin(func(w http.ResponseWriter, r *http.Request) {
		media, e := h.service.GetPrivateMediaForAdmin(r.Context(), r.PathValue("media_id"))
		if e != nil {
			h.fail(w, e)
			return
		}
		w.Header().Set("Content-Type", media.MimeType)
		w.Header().Set("Cache-Control", "private, max-age=3600")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		http.ServeContent(w, r, media.Filename, media.CreatedAt, bytes.NewReader(media.Data))
	}))
}

func mediaType(data []byte, filename string) string {
	extension := strings.ToLower(filepath.Ext(filename))
	if extension == ".mp3" && len(data) >= 3 && (string(data[:3]) == "ID3" || (data[0] == 0xff && data[1]&0xe0 == 0xe0)) {
		return "audio/mpeg"
	}
	if extension == ".png" && len(data) >= 8 && bytes.Equal(data[:8], []byte("\x89PNG\r\n\x1a\n")) {
		return "image/png"
	}
	// A page scan usually arrives as a photograph of a book, and a phone
	// camera writes JPEG. Refusing it would send the author to a converter
	// before they could upload the page.
	if (extension == ".jpg" || extension == ".jpeg") && len(data) >= 3 && bytes.Equal(data[:3], []byte{0xff, 0xd8, 0xff}) {
		return "image/jpeg"
	}
	return ""
}

// sniff identifies an upload and then checks the one place that says what may
// be stored, so the handler cannot accept something the service will refuse.
func sniff(data []byte, filename string) string {
	mime := mediaType(data, filename)
	if !app.StorableMedia(mime) {
		return ""
	}
	return mime
}
