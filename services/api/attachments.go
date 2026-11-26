package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
)

// Attachments are photos, signatures and files for a submission. The phone
// uploads each one for an alert before it sends the submission, with an ID
// it made itself, so a retried upload finds the first one. The bytes are
// stored once under their SHA-256 hash and never replaced.
//
// Photos are kept exactly as uploaded, including any location in their
// EXIF data. PLAN.md section 6 requires that location data is removed
// before any public release, so a future public view must strip it; access
// inside the platform follows the alert's permissions.

type attachmentSettings struct {
	store    attachmentStore
	maxBytes int64
}

// withAttachments turns on the attachment endpoints.
func withAttachments(store attachmentStore, maxBytes int64) func(*api) {
	return func(a *api) { a.attachments = attachmentSettings{store: store, maxBytes: maxBytes} }
}

// defaultAttachmentBytes is the default upload limit, 25 MB.
const defaultAttachmentBytes = 25 << 20

// attachmentTypes are the media types an upload may have. Images and PDF
// files must also look like what they claim to be.
var attachmentTypes = map[string]string{
	"image/jpeg":      "image/jpeg",
	"image/png":       "image/png",
	"image/webp":      "image/webp",
	"application/pdf": "application/pdf",
	"application/zip": "application/zip",
	"text/plain":      "text/",
	"text/csv":        "text/",
}

func attachmentFileName(raw string) (string, bool) {
	name := strings.TrimSpace(path.Base(strings.ReplaceAll(raw, `\`, "/")))
	if name == "" || name == "." || name == "/" || len([]rune(name)) > 200 {
		return "", false
	}
	for _, r := range name {
		if unicode.IsControl(r) || r == '"' {
			return "", false
		}
	}
	return name, true
}

const attachmentJSON = `jsonb_build_object('id', a.id, 'alertId', a.alert_id::text, 'sha256', a.sha256,
    'sizeBytes', a.size_bytes, 'contentType', a.content_type, 'fileName', a.file_name, 'uploadedAt', a.uploaded_at)`

// uploadAttachment stores one file for an alert: PUT with the file as the
// body, its media type in Content-Type and its name in ?name=.
func (a api) uploadAttachment(w http.ResponseWriter, r *http.Request) {
	// A phone on a weak signal may need longer than the server's usual limit.
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(5 * time.Minute))
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	if a.attachments.store == nil {
		writeError(w, http.StatusServiceUnavailable, "Attachments are not set up on this server.")
		return
	}
	subject := ctx.Value(identityKey{}).(identity).Subject
	slug := chi.URLParam(r, "slug")
	attachmentID := chi.URLParam(r, "attachment")
	alert, err := alertRoute(r)
	if err != nil || !uuidPattern.MatchString(attachmentID) {
		writeError(w, http.StatusBadRequest, "Use an alert ID and a new attachment ID in UUID form, in small letters.")
		return
	}
	name, ok := attachmentFileName(r.URL.Query().Get("name"))
	if !ok {
		writeError(w, http.StatusBadRequest, "Give the file name in ?name=, up to 200 characters.")
		return
	}
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	sniffAs, allowed := attachmentTypes[mediaType]
	if err != nil || !allowed {
		writeError(w, http.StatusUnsupportedMediaType, "Attachments can be JPEG, PNG or WebP images, PDF, ZIP, plain text or CSV files.")
		return
	}
	// Check access before reading the body, so a caller who cannot see the
	// alert cannot fill the disk.
	err = pgx.BeginFunc(ctx, a.reader, func(tx pgx.Tx) error {
		_, err := alertUser(ctx, tx, subject, slug, alert)
		return err
	})
	if err != nil {
		a.writeProblem(w, err, "Alert not found.", "Could not check the alert")
		return
	}

	spool, err := os.CreateTemp(a.attachments.store.SpoolDir(), "upload-*")
	if err != nil {
		a.logger.Error("Could not spool an attachment", "error", err)
		writeError(w, http.StatusInternalServerError, "Could not save the attachment.")
		return
	}
	defer os.Remove(spool.Name())
	defer spool.Close()
	hash := sha256.New()
	size, err := io.Copy(io.MultiWriter(spool, hash), http.MaxBytesReader(w, r.Body, a.attachments.maxBytes))
	var tooLarge *http.MaxBytesError
	switch {
	case errors.As(err, &tooLarge):
		writeError(w, http.StatusRequestEntityTooLarge, "An attachment can be at most "+strconv.FormatInt(a.attachments.maxBytes>>20, 10)+" MB.")
		return
	case err != nil:
		writeError(w, http.StatusBadRequest, "Could not read the uploaded file.")
		return
	case size == 0:
		writeError(w, http.StatusBadRequest, "The uploaded file is empty.")
		return
	}
	head := make([]byte, 512)
	n, _ := spool.ReadAt(head, 0)
	if sniffed := http.DetectContentType(head[:n]); !strings.HasPrefix(sniffed, sniffAs) {
		writeError(w, http.StatusUnsupportedMediaType, "The file does not look like "+mediaType+".")
		return
	}
	if err := spool.Sync(); err != nil {
		a.logger.Error("Could not write an attachment to disk", "error", err)
		writeError(w, http.StatusInternalServerError, "Could not save the attachment.")
		return
	}
	sum := hex.EncodeToString(hash.Sum(nil))

	var result json.RawMessage
	status := http.StatusCreated
	err = pgx.BeginFunc(ctx, a.reader, func(tx pgx.Tx) error {
		user, err := alertUser(ctx, tx, subject, slug, alert)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('open-fimra:attachment:' || $1, 0))`, attachmentID); err != nil {
			return err
		}
		var same bool
		err = tx.QueryRow(ctx, `SELECT sha256 = $2 AND alert_id = $3 AND uploaded_by = $4 AND file_name = $5 AND content_type = $6
FROM attachments WHERE id = $1`, attachmentID, sum, alert, user, name, mediaType).Scan(&same)
		switch {
		case err == nil && !same:
			return problem(http.StatusConflict, "This attachment ID already belongs to another file. Attachments are never replaced.")
		case err == nil:
			status = http.StatusOK
		case errors.Is(err, pgx.ErrNoRows):
			if err := a.attachments.store.Put(ctx, attachmentKeyFor(sum), spool.Name(), size, mediaType); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO attachments (id, workspace_id, alert_id, sha256, size_bytes, content_type, file_name, uploaded_by)
SELECT $1, a.workspace_id, a.id, $3, $4, $5, $6, $7 FROM alerts a WHERE a.id = $2`,
				attachmentID, alert, sum, size, mediaType, name, user); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO audit_log (workspace_id, actor_kind, actor_id, action, target_type, target_id, details)
SELECT workspace_id, 'user', $2, 'attachment.upload', 'attachment', id::text,
       jsonb_build_object('alertId', alert_id, 'sha256', sha256, 'sizeBytes', size_bytes)
FROM attachments WHERE id = $1`, attachmentID, subject); err != nil {
				return err
			}
		default:
			return err
		}
		return tx.QueryRow(ctx, `SELECT `+attachmentJSON+` FROM attachments a WHERE a.id = $1`, attachmentID).Scan(&result)
	})
	if err != nil {
		a.writeProblem(w, err, "Alert not found.", "Could not save the attachment")
		return
	}
	w.Header().Set("Location", "/api/v1/workspaces/"+slug+"/attachments/"+attachmentID)
	writeJSON(w, status, result)
}

// attachmentReceipt lets the uploader recover from losing the upload response
// without sending the bytes again. The phone compares the receipt to its file.
func (a api) attachmentReceipt(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	alert, err := alertRoute(r)
	id := chi.URLParam(r, "attachment")
	if err != nil || !uuidPattern.MatchString(id) {
		writeError(w, http.StatusBadRequest, "Provide a valid alert and attachment ID.")
		return
	}
	var result json.RawMessage
	err = a.reader.QueryRow(ctx, `SELECT `+attachmentJSON+`
FROM attachments a WHERE a.id = $1 AND a.alert_id = $2
AND EXISTS (SELECT FROM alert_roles($3, $4, a.alert_id) WHERE user_id = a.uploaded_by)`,
		id, alert, r.Context().Value(identityKey{}).(identity).Subject, chi.URLParam(r, "slug")).Scan(&result)
	if err != nil {
		a.writeProblem(w, err, "Attachment not found.", "Could not check the attachment")
		return
	}
	writeJSON(w, http.StatusOK, result)
}

// downloadAttachment sends a stored file to a caller who can see its alert,
// with the same checks as the alert's detail, and records the download.
func (a api) downloadAttachment(w http.ResponseWriter, r *http.Request) {
	// A phone on a weak signal may need longer than the server's usual limit.
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(5 * time.Minute))
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	if a.attachments.store == nil {
		writeError(w, http.StatusServiceUnavailable, "Attachments are not set up on this server.")
		return
	}
	subject := ctx.Value(identityKey{}).(identity).Subject
	id := chi.URLParam(r, "attachment")
	if !uuidPattern.MatchString(id) {
		writeError(w, http.StatusNotFound, "Attachment not found.")
		return
	}
	var sum, contentType, name string
	var size int64
	err := pgx.BeginFunc(ctx, a.reader, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `SELECT a.sha256, a.content_type, a.file_name, a.size_bytes
FROM attachments a JOIN workspaces w ON w.id = a.workspace_id AND w.slug = $2
WHERE a.id = $1 AND EXISTS (SELECT FROM alert_roles($3, $2, a.alert_id))`, id, chi.URLParam(r, "slug"), subject).Scan(&sum, &contentType, &name, &size)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO audit_log (workspace_id, actor_kind, actor_id, action, target_type, target_id, details)
SELECT workspace_id, 'user', $2, 'attachment.download', 'attachment', id::text, jsonb_build_object('alertId', alert_id)
FROM attachments WHERE id = $1`, id, subject)
		return err
	})
	if err != nil {
		a.writeProblem(w, err, "Attachment not found.", "Could not load the attachment")
		return
	}
	file, err := a.attachments.store.Open(ctx, attachmentKeyFor(sum))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			a.logger.Error("An attachment's file is missing from the store", "attachment", id)
		} else {
			a.logger.Error("Could not read an attachment", "attachment", id, "error", err)
		}
		writeError(w, http.StatusInternalServerError, "Could not read the attachment.")
		return
	}
	defer file.Close()
	header := w.Header()
	header.Set("Content-Type", contentType)
	header.Set("Content-Length", strconv.FormatInt(size, 10))
	header.Set("Content-Disposition", "attachment; filename*=UTF-8''"+url.PathEscape(name))
	header.Set("Cache-Control", "private, no-store")
	header.Set("X-Content-Type-Options", "nosniff")
	header.Set("Content-Security-Policy", "default-src 'none'; sandbox")
	header.Set("ETag", `"`+sum+`"`)
	w.WriteHeader(http.StatusOK)
	if _, err := io.Copy(w, file); err != nil {
		a.logger.Warn("An attachment download stopped early", "attachment", id, "error", err)
	}
}

// attachmentStoreFromEnv builds the attachment store from the environment.
func attachmentStoreFromEnv(getenv func(string) string) (attachmentStore, int64, error) {
	maxBytes := int64(defaultAttachmentBytes)
	if raw := getenv("FIMRA_ATTACHMENT_MAX_MB"); raw != "" {
		mb, err := strconv.Atoi(raw)
		if err != nil || mb < 1 || mb > 1024 {
			return nil, 0, errors.New("FIMRA_ATTACHMENT_MAX_MB must be a whole number of megabytes from 1 to 1024")
		}
		maxBytes = int64(mb) << 20
	}
	dir := getenv("FIMRA_ATTACHMENT_DIR")
	if dir == "" {
		dir = ".form-attachments"
	}
	switch kind := getenv("FIMRA_ATTACHMENT_STORE"); kind {
	case "", "local":
		store, err := newLocalAttachments(dir)
		return store, maxBytes, err
	case "s3":
		spool := path.Join(dir, ".incoming")
		store, err := newS3Attachments(s3Settings{Endpoint: getenv("FIMRA_S3_ENDPOINT"), Region: getenv("FIMRA_S3_REGION"),
			Bucket: getenv("FIMRA_S3_BUCKET"), Prefix: getenv("FIMRA_S3_PREFIX"), AccessKey: getenv("FIMRA_S3_ACCESS_KEY"),
			SecretKey: getenv("FIMRA_S3_SECRET_KEY"), SpoolDir: spool})
		return store, maxBytes, err
	}
	return nil, 0, errors.New("FIMRA_ATTACHMENT_STORE must be local or s3")
}
