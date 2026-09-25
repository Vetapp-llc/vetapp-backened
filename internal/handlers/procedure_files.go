package handlers

import (
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"vetapp-backend/internal/middleware"
	"vetapp-backend/internal/models"
	"vetapp-backend/internal/services"

	"github.com/go-chi/chi/v5"
	"gorm.io/gorm"
)

// ProcedureFileHandler serves attachments on procedure records — lab
// results, scans and PDFs.
//
// Authorisation is by pet ownership on every route, including the
// download: a signed URL is only minted after confirming the caller
// owns the pet the file belongs to. Attachment ids are sequential and
// therefore guessable, so checking ownership per request is what stops
// one owner reading another's records.
type ProcedureFileHandler struct {
	db      *gorm.DB
	storage *services.StorageService
	// clinic selects the authorisation rule: false = the owner app (the
	// caller owns the pet), true = the clinic portal (the procedure was
	// recorded by the caller's clinic) — lab results the clinic attaches,
	// replacing vet/upload77.php.
	clinic bool
}

// NewProcedureFileHandler creates the owner-app ProcedureFileHandler.
func NewProcedureFileHandler(db *gorm.DB, storage *services.StorageService) *ProcedureFileHandler {
	return &ProcedureFileHandler{db: db, storage: storage}
}

// NewClinicProcedureFileHandler creates the clinic-portal handler.
func NewClinicProcedureFileHandler(db *gorm.DB, storage *services.StorageService) *ProcedureFileHandler {
	return &ProcedureFileHandler{db: db, storage: storage, clinic: true}
}

// allowed reports whether the caller may see and change the files on
// procedure procID of pet petID. The procedure must belong to the pet in
// both modes, so a file can never be attached to a record the caller
// cannot see.
func (h *ProcedureFileHandler) allowed(r *http.Request, petID, procID string) bool {
	if petID == "" || procID == "" {
		return false
	}
	if !h.clinic {
		if !h.ownsPet(r, petID) {
			return false
		}
		var n int64
		h.db.Model(&models.Procedure{}).Where("id = ? AND uuid = ?", procID, petID).Count(&n)
		return n > 0
	}
	var n int64
	ownedByCaller(h.db.Model(&models.Procedure{}), r, "sk").
		Where("id = ? AND uuid = ?", procID, petID).Count(&n)
	return n > 0
}

// signedURLTTL bounds how long a download link stays valid. The URL is
// a bearer credential — anyone holding it can fetch the object — so it
// is deliberately short: long enough to open the file, not long enough
// to be worth sharing.
const signedURLTTL = 5 * time.Minute

// ProcedureFileResponse is one attachment as returned to a client.
type ProcedureFileResponse struct {
	ID          uint   `json:"id" validate:"required"`
	FileName    string `json:"fileName" validate:"required"`
	ContentType string `json:"contentType"`
	SizeBytes   int64  `json:"sizeBytes"`
	CreatedAt   string `json:"createdAt"`
}

func fileToResponse(f models.ProcedureFile) ProcedureFileResponse {
	return ProcedureFileResponse{
		ID:          f.ID,
		FileName:    f.FileName,
		ContentType: f.ContentType,
		SizeBytes:   f.SizeBytes,
		CreatedAt:   f.CreatedAt.Format(time.RFC3339),
	}
}

// ownsPet reports whether the authenticated owner owns petID.
//
// Pets are linked to their owner by the owner's personal ID in
// `pets.uuid` (a legacy naming quirk — it is not a UUID).
func (h *ProcedureFileHandler) ownsPet(r *http.Request, petID string) bool {
	personalID := ownerPersonalID(r)
	if personalID == "" || petID == "" {
		return false
	}
	var count int64
	h.db.Model(&models.Pet{}).
		Where("id = ? AND uuid = ?", petID, personalID).
		Count(&count)
	return count > 0
}

// List returns the attachments on a procedure.
// @Summary List procedure attachments
// @Tags owner
// @Produce json
// @Security BearerAuth
// @Param id path int true "Pet ID"
// @Param procId path int true "Procedure ID"
// @Success 200 {array} ProcedureFileResponse
// @Failure 403 {object} ErrorResponse
// @Router /owner/pets/{id}/procedures/{procId}/files [get]
func (h *ProcedureFileHandler) List(w http.ResponseWriter, r *http.Request) {
	petID := chi.URLParam(r, "id")
	procID := chi.URLParam(r, "procId")

	if !h.allowed(r, petID, chi.URLParam(r, "procId")) {
		// 404 rather than 403 so the endpoint doesn't confirm whether a
		// pet id exists for someone probing ids they don't own.
		writeJSON(w, http.StatusNotFound, ErrorResponse{Error: "pet not found"})
		return
	}

	var files []models.ProcedureFile
	h.db.Where("procedure_id = ? AND pet_id = ?", procID, petID).
		Order("created_at DESC, id DESC").
		Find(&files)

	items := make([]ProcedureFileResponse, len(files))
	for i, f := range files {
		items[i] = fileToResponse(f)
	}
	writeJSON(w, http.StatusOK, items)
}

// Upload attaches a file to a procedure.
// @Summary Upload a procedure attachment
// @Tags owner
// @Accept multipart/form-data
// @Produce json
// @Security BearerAuth
// @Param id path int true "Pet ID"
// @Param procId path int true "Procedure ID"
// @Param file formData file true "File to attach"
// @Success 201 {object} ProcedureFileResponse
// @Failure 400 {object} ErrorResponse
// @Failure 413 {object} ErrorResponse
// @Router /owner/pets/{id}/procedures/{procId}/files [post]
func (h *ProcedureFileHandler) Upload(w http.ResponseWriter, r *http.Request) {
	log := middleware.RequestLogger(r)
	claims := middleware.GetClaims(r)
	petID := chi.URLParam(r, "id")
	procID := chi.URLParam(r, "procId")

	if !h.storage.Enabled() {
		writeJSON(w, http.StatusServiceUnavailable, ErrorResponse{
			Error: "file storage is not configured",
		})
		return
	}
	if !h.allowed(r, petID, chi.URLParam(r, "procId")) {
		writeJSON(w, http.StatusNotFound, ErrorResponse{Error: "pet not found"})
		return
	}

	// Bound the request body before parsing so an oversized upload is
	// rejected without buffering it all first.
	r.Body = http.MaxBytesReader(w, r.Body, services.MaxUploadBytes+(1<<20))
	if err := r.ParseMultipartForm(8 << 20); err != nil {
		writeJSON(w, http.StatusRequestEntityTooLarge, ErrorResponse{
			Error: "file too large or malformed upload",
		})
		return
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "missing 'file' field"})
		return
	}
	defer file.Close()

	if header.Size > services.MaxUploadBytes {
		writeJSON(w, http.StatusRequestEntityTooLarge, ErrorResponse{
			Error: "file exceeds the maximum size",
		})
		return
	}

	// Trust the sniffed type over the client-declared one: Content-Type
	// in a multipart part is attacker-controlled, so validating it alone
	// would let a renamed executable through.
	head := make([]byte, 512)
	n, _ := io.ReadFull(file, head)
	head = head[:n]
	sniffed := http.DetectContentType(head)
	// DetectContentType appends charset for text types; compare the base.
	if i := strings.IndexByte(sniffed, ';'); i >= 0 {
		sniffed = strings.TrimSpace(sniffed[:i])
	}
	if !services.IsAllowedContentType(sniffed) {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{
			Error: "unsupported file type; allowed: " + services.AllowedContentTypeList(),
		})
		return
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "could not read file"})
		return
	}

	objectKey, err := services.ObjectKey(petID, header.Filename)
	if err != nil {
		log.Error("procedure_file_key_failed", "error", err)
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "could not store file"})
		return
	}

	if err := h.storage.Upload(objectKey, sniffed, file); err != nil {
		log.Error("procedure_file_upload_failed", "pet_id", petID, "error", err)
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "could not store file"})
		return
	}

	procIDNum, _ := strconv.ParseUint(procID, 10, 64)
	rec := models.ProcedureFile{
		ProcedureID: uint(procIDNum),
		PetID:       petID,
		ObjectKey:   objectKey,
		FileName:    sanitizeFileName(header.Filename),
		ContentType: sniffed,
		SizeBytes:   header.Size,
		UploadedBy:  claims.UserID,
	}
	if err := h.db.Create(&rec).Error; err != nil {
		// The object is already stored; drop it so we don't leave an
		// unreferenced blob behind.
		if delErr := h.storage.Delete(objectKey); delErr != nil {
			log.Warn("procedure_file_orphan", "object_key", objectKey, "error", delErr)
		}
		log.Error("procedure_file_persist_failed", "error", err)
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "could not record file"})
		return
	}

	log.Info("procedure_file_uploaded", "file_id", rec.ID, "pet_id", petID, "size", header.Size)
	writeJSON(w, http.StatusCreated, fileToResponse(rec))
}

// Download returns a short-lived signed URL for an attachment.
// @Summary Get a download link for an attachment
// @Tags owner
// @Produce json
// @Security BearerAuth
// @Param id path int true "Pet ID"
// @Param procId path int true "Procedure ID"
// @Param fileId path int true "File ID"
// @Success 200 {object} map[string]string
// @Failure 404 {object} ErrorResponse
// @Router /owner/pets/{id}/procedures/{procId}/files/{fileId} [get]
func (h *ProcedureFileHandler) Download(w http.ResponseWriter, r *http.Request) {
	log := middleware.RequestLogger(r)
	petID := chi.URLParam(r, "id")
	fileID := chi.URLParam(r, "fileId")

	if !h.storage.Enabled() {
		writeJSON(w, http.StatusServiceUnavailable, ErrorResponse{
			Error: "file storage is not configured",
		})
		return
	}
	if !h.allowed(r, petID, chi.URLParam(r, "procId")) {
		writeJSON(w, http.StatusNotFound, ErrorResponse{Error: "pet not found"})
		return
	}

	// Scope the lookup by pet as well as id: ids are sequential, so
	// without this an owner could read another owner's attachment by
	// guessing.
	var rec models.ProcedureFile
	if err := h.db.Where("id = ? AND pet_id = ? AND procedure_id = ?", fileID, petID, chi.URLParam(r, "procId")).First(&rec).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			writeJSON(w, http.StatusNotFound, ErrorResponse{Error: "file not found"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "lookup failed"})
		return
	}

	url, err := h.storage.SignedURL(rec.ObjectKey, signedURLTTL)
	if err != nil {
		log.Error("procedure_file_sign_failed", "file_id", rec.ID, "error", err)
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "could not create download link"})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"url":       url,
		"fileName":  rec.FileName,
		"expiresIn": int(signedURLTTL.Seconds()),
	})
}

// Delete removes an attachment.
// @Summary Delete a procedure attachment
// @Tags owner
// @Produce json
// @Security BearerAuth
// @Param id path int true "Pet ID"
// @Param procId path int true "Procedure ID"
// @Param fileId path int true "File ID"
// @Success 200 {object} MessageResponse
// @Failure 404 {object} ErrorResponse
// @Router /owner/pets/{id}/procedures/{procId}/files/{fileId} [delete]
func (h *ProcedureFileHandler) Delete(w http.ResponseWriter, r *http.Request) {
	log := middleware.RequestLogger(r)
	petID := chi.URLParam(r, "id")
	fileID := chi.URLParam(r, "fileId")

	if !h.allowed(r, petID, chi.URLParam(r, "procId")) {
		writeJSON(w, http.StatusNotFound, ErrorResponse{Error: "pet not found"})
		return
	}

	var rec models.ProcedureFile
	if err := h.db.Where("id = ? AND pet_id = ? AND procedure_id = ?", fileID, petID, chi.URLParam(r, "procId")).First(&rec).Error; err != nil {
		writeJSON(w, http.StatusNotFound, ErrorResponse{Error: "file not found"})
		return
	}

	if err := h.db.Delete(&rec).Error; err != nil {
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "could not delete file"})
		return
	}
	// Remove the object after the row: an orphaned object costs storage,
	// but a row pointing at a deleted object shows the user a broken
	// file. Failure here is logged, not surfaced.
	if h.storage.Enabled() {
		if err := h.storage.Delete(rec.ObjectKey); err != nil {
			log.Warn("procedure_file_object_delete_failed", "object_key", rec.ObjectKey, "error", err)
		}
	}

	writeJSON(w, http.StatusOK, MessageResponse{Message: "file deleted"})
}

// sanitizeFileName keeps a display name safe to render and to send in a
// Content-Disposition header. The stored object key is random and
// independent of this, so this is purely about presentation.
func sanitizeFileName(name string) string {
	name = strings.TrimSpace(name)
	// Drop any path components a client may have included.
	if i := strings.LastIndexAny(name, `/\`); i >= 0 {
		name = name[i+1:]
	}
	name = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || r == '"' {
			return -1
		}
		return r
	}, name)
	if name == "" {
		name = "attachment"
	}
	if len(name) > 120 {
		name = name[:120]
	}
	return name
}
