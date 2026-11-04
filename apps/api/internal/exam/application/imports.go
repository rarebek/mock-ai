package application

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"
)

const MaxPDFBytes int64 = 50 << 20

var ErrImportUnavailable = errors.New("PDF import is temporarily unavailable")

type ContentImport struct {
	ID              string    `json:"id"`
	Filename        string    `json:"filename"`
	SizeBytes       int64     `json:"size_bytes"`
	SHA256          string    `json:"sha256"`
	RequestedSkills string    `json:"requested_skills"`
	Status          string    `json:"status"`
	Model           string    `json:"-"`
	ProgressCurrent int32     `json:"progress_current"`
	ProgressTotal   int32     `json:"progress_total"`
	DraftIDs        []string  `json:"draft_ids"`
	Warnings        []string  `json:"warnings"`
	ErrorMessage    string    `json:"error_message"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
	CompletedAt     time.Time `json:"completed_at,omitempty"`
	AccessScope     string    `json:"access_scope"`
	OwnerUserID     string    `json:"owner_user_id"`
}

type ClaimedImport struct {
	ID, AdminID, Filename, Skills, Model, FileURI, AccessScope, OwnerUserID string
}

type ImportProvider interface {
	UploadPDF(context.Context, string, io.Reader, int64) (uri, digest string, err error)
	Extract(context.Context, string, string, string) ([]Content, []string, error)
}

func (s *Service) CreateImport(ctx context.Context, adminID, filename, skills, accessScope, ownerID string, rights bool, size int64, file io.Reader) (string, error) {
	if s.importer == nil {
		return "", ErrImportUnavailable
	}
	filename = filepath.Base(strings.TrimSpace(filename))
	if !rights || filename == "." || filename == "" || !strings.EqualFold(filepath.Ext(filename), ".pdf") || size < 5 || size > MaxPDFBytes {
		return "", fmt.Errorf("%w: choose a PDF up to 50 MB and confirm you may use its content", ErrInvalidProfile)
	}
	if skills != "all" && skills != "reading" && skills != "listening" {
		return "", fmt.Errorf("%w: choose Reading, Listening or both", ErrInvalidProfile)
	}
	if accessScope == "" {
		accessScope = "public"
	}
	if accessScope != "public" && accessScope != "private" || (accessScope == "private") != (len(ownerID) == 36) {
		return "", fmt.Errorf("%w: private imports require one owner", ErrInvalidProfile)
	}
	var head [5]byte
	if _, err := io.ReadFull(file, head[:]); err != nil || !bytes.Equal(head[:], []byte("%PDF-")) {
		return "", fmt.Errorf("%w: the uploaded file is not a PDF", ErrInvalidProfile)
	}
	// The import reads the paper with a model, so a centre that has AI
	// switched off is refused here -- after the request has been checked, and
	// before a single byte is uploaded anywhere.
	ai, err := s.repository.AdminAIEnabled(ctx, adminID)
	if err != nil {
		return "", err
	}
	if !ai {
		return "", ErrImportUnavailable
	}
	reader := io.MultiReader(bytes.NewReader(head[:]), file)
	uri, digest, err := s.importer.UploadPDF(ctx, filename, reader, size)
	if err != nil {
		if s.log != nil {
			s.log.Error("content import upload failed", "filename", filename, "error", err)
		}
		return "", ErrImportUnavailable
	}
	return s.repository.CreateImport(ctx, adminID, filename, size, digest, skills, s.importModel, uri, accessScope, ownerID)
}

func (s *Service) ListImports(ctx context.Context, adminID string) ([]ContentImport, error) {
	return s.repository.ListImports(ctx, adminID)
}

func (s *Service) RunImports(ctx context.Context) {
	if s.importer == nil {
		return
	}
	_ = s.repository.FailStaleImports(ctx)
	go func() {
		ticker := time.NewTicker(3 * time.Second)
		defer ticker.Stop()
		for {
			s.runOneImport(ctx)
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}

func (s *Service) runOneImport(ctx context.Context) {
	job, err := s.repository.ClaimImport(ctx)
	if err != nil {
		return
	}
	contents, warnings, err := s.importer.Extract(ctx, job.FileURI, job.Skills, job.Model)
	if err != nil || len(contents) == 0 {
		if s.log != nil {
			s.log.Error("content import extraction failed", "import_id", job.ID, "error", err)
		}
		_ = s.repository.FailImport(ctx, job.ID, "The document could not be extracted reliably. Check that it is readable and contains complete tests, then upload it again.")
		return
	}
	if err = s.repository.SetImportTotal(ctx, job.ID, int32(len(contents))); err != nil {
		_ = s.repository.FailImport(ctx, job.ID, "The import could not be saved.")
		return
	}
	for i := range contents {
		normalizeImported(&contents[i])
		contents[i].AccessScope, contents[i].OwnerUserID = job.AccessScope, job.OwnerUserID
		if err = ValidateContent(contents[i], false); err != nil {
			_ = s.repository.FailImport(ctx, job.ID, "An extracted test failed validation. No content was published; review the source and retry.")
			return
		}
		draftID, saveErr := s.repository.SaveDraft(ctx, job.AdminID, "", contents[i])
		if saveErr != nil || s.repository.AddImportDraft(ctx, job.ID, draftID) != nil {
			_ = s.repository.FailImport(ctx, job.ID, "An extracted draft could not be saved.")
			return
		}
	}
	_ = s.repository.CompleteImport(ctx, job.ID, warnings)
}

func normalizeImported(c *Content) {
	c.Title = strings.TrimSpace(c.Title)
	c.Description = strings.TrimSpace(c.Description)
	c.Purpose = "mock"
	if c.Duration == 0 {
		c.Duration = 3600
	}
	if strings.TrimSpace(c.Instructions) == "" {
		c.Instructions = "Complete every question. Review your answers before submitting."
	}
	count := 0
	for i := range c.Sections {
		c.Sections[i].Title = strings.TrimSpace(c.Sections[i].Title)
		for j := range c.Sections[i].Questions {
			q := &c.Sections[i].Questions[j]
			q.Prompt, q.Explanation = strings.TrimSpace(q.Prompt), strings.TrimSpace(q.Explanation)
			q.Options, q.Answers = cleanStrings(q.Options), cleanStrings(q.Answers)
			if q.Type == "true_false_not_given" {
				q.Options = []string{"True", "False", "Not Given"}
			}
			if q.Type == "yes_no_not_given" {
				q.Options = []string{"Yes", "No", "Not Given"}
			}
			canonicalizeChoiceAnswers(q)
			count++
		}
	}
	if count == 40 && len(c.Bands) == 0 {
		c.Bands = standardBands(c.Skill)
	}
}

func cleanStrings(values []string) []string {
	out, seen := []string{}, map[string]bool{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" && !seen[value] {
			out, seen[value] = append(out, value), true
		}
	}
	return out
}

func standardBands(skill string) []ContentBand {
	thresholds := [][2]float64{{0, 0}, {4, 2.5}, {6, 3}, {8, 3.5}, {10, 4}, {13, 4.5}, {15, 5}, {19, 5.5}, {23, 6}, {27, 6.5}, {30, 7}, {33, 7.5}, {35, 8}, {37, 8.5}, {39, 9}}
	if skill == "listening" {
		thresholds = [][2]float64{{0, 0}, {4, 2.5}, {6, 3}, {8, 3.5}, {10, 4}, {13, 4.5}, {16, 5}, {18, 5.5}, {23, 6}, {26, 6.5}, {30, 7}, {32, 7.5}, {35, 8}, {37, 8.5}, {39, 9}}
	}
	out := make([]ContentBand, 0, len(thresholds))
	for _, v := range thresholds {
		out = append(out, ContentBand{Minimum: int(v[0]), Band: v[1]})
	}
	return out
}

func PDFDigest(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}
