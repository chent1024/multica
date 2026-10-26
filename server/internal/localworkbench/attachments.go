package localworkbench

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

const (
	maxAttachmentBytes        = 100 << 20
	maxAttachmentPreviewBytes = 5 << 20
)

var errAttachmentTooLarge = errors.New("file exceeds 100 MB limit")

type boundedAttachmentReader struct {
	reader    io.Reader
	remaining int64
}

func (r *boundedAttachmentReader) Read(buffer []byte) (int, error) {
	if r.remaining == 0 {
		var probe [1]byte
		count, err := r.reader.Read(probe[:])
		if count > 0 {
			return 0, errAttachmentTooLarge
		}
		return 0, err
	}
	if int64(len(buffer)) > r.remaining {
		buffer = buffer[:r.remaining]
	}
	count, err := r.reader.Read(buffer)
	r.remaining -= int64(count)
	return count, err
}

type Attachment struct {
	ID                    string  `json:"id"`
	WorkspaceID           string  `json:"workspace_id"`
	IssueID               *string `json:"issue_id"`
	CommentID             *string `json:"comment_id"`
	ChatSessionID         *string `json:"chat_session_id"`
	ChatMessageID         *string `json:"chat_message_id"`
	UploaderType          string  `json:"uploader_type"`
	UploaderID            string  `json:"uploader_id"`
	Filename              string  `json:"filename"`
	URL                   string  `json:"url"`
	DownloadURL           string  `json:"download_url"`
	AttachmentDownloadURL string  `json:"attachment_download_url"`
	MarkdownURL           string  `json:"markdown_url"`
	ContentType           string  `json:"content_type"`
	SizeBytes             int64   `json:"size_bytes"`
	CreatedAt             string  `json:"created_at"`
	storagePath           string
}

func scanAttachment(row rowScanner) (Attachment, error) {
	var item Attachment
	var issueID, commentID, storagePath sql.NullString
	err := row.Scan(&item.ID, &item.WorkspaceID, &issueID, &commentID, &item.Filename, &item.ContentType, &item.SizeBytes, &storagePath, &item.CreatedAt)
	item.IssueID, item.CommentID = stringPtr(issueID), stringPtr(commentID)
	if storagePath.Valid {
		item.storagePath = storagePath.String
	}
	item.ChatSessionID, item.ChatMessageID = nil, nil
	item.UploaderType, item.UploaderID = "system", LocalActorID
	return item, err
}

func (s *Store) createAttachmentMetadata(ctx context.Context, id, workspaceID, issueID, filename, contentType, storagePath string, size int64) (Attachment, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Attachment{}, err
	}
	defer tx.Rollback()
	cursor, origin, err := beginLocalWakeupMutation(ctx, tx, workspaceID)
	if err != nil {
		return Attachment{}, err
	}
	stamp := now()
	_, err = tx.ExecContext(ctx, `INSERT INTO attachments(id, workspace_id, issue_id, filename, content_type, size_bytes, data, storage_path, created_at) VALUES (?, ?, ?, ?, ?, ?, x'', ?, ?)`, id, workspaceID, nullable(issueID), filename, contentType, size, storagePath, stamp)
	if err != nil {
		return Attachment{}, err
	}
	if err := commitLocalWakeupMutation(ctx, tx, workspaceID, cursor, origin); err != nil {
		return Attachment{}, err
	}
	return s.getAttachment(ctx, workspaceID, id)
}

func (s *Store) getAttachment(ctx context.Context, workspaceID, id string) (Attachment, error) {
	return scanAttachment(s.db.QueryRowContext(ctx, `SELECT id, workspace_id, issue_id, comment_id, filename, content_type, size_bytes, storage_path, created_at FROM attachments WHERE workspace_id=? AND id=?`, workspaceID, id))
}

func (s *Store) listAttachments(ctx context.Context, workspaceID, issueID, commentID string) ([]Attachment, error) {
	query := `SELECT id, workspace_id, issue_id, comment_id, filename, content_type, size_bytes, storage_path, created_at FROM attachments WHERE workspace_id=?`
	args := []any{workspaceID}
	if issueID != "" {
		query += ` AND issue_id=?`
		args = append(args, issueID)
	}
	if commentID != "" {
		query += ` AND comment_id=?`
		args = append(args, commentID)
	}
	query += ` ORDER BY created_at, id`
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []Attachment{}
	for rows.Next() {
		item, err := scanAttachment(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func bindAttachmentsTx(ctx context.Context, tx *sql.Tx, workspaceID, issueID, commentID string, ids []string) error {
	for _, id := range ids {
		result, err := tx.ExecContext(ctx, `UPDATE attachments SET issue_id=?, comment_id=? WHERE id=? AND workspace_id=? AND (issue_id IS NULL OR issue_id=?) AND (comment_id IS NULL OR comment_id=?)`, issueID, nullable(commentID), id, workspaceID, issueID, nullable(commentID))
		if err != nil {
			return err
		}
		count, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if count != 1 {
			return sql.ErrNoRows
		}
	}
	return nil
}

func (s *Store) bindAttachmentsToIssue(ctx context.Context, workspaceID, issueID string, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	cursor, origin, err := beginLocalWakeupMutation(ctx, tx, workspaceID)
	if err != nil {
		return err
	}

	if err := bindAttachmentsTx(ctx, tx, workspaceID, issueID, "", ids); err != nil {
		return err
	}
	return commitLocalWakeupMutation(ctx, tx, workspaceID, cursor, origin)
}

func (s *Store) deleteAttachment(ctx context.Context, workspaceID, id string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	cursor, origin, err := beginLocalWakeupMutation(ctx, tx, workspaceID)
	if err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `DELETE FROM attachments WHERE workspace_id=? AND id=?`, workspaceID, id)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return sql.ErrNoRows
	}
	return commitLocalWakeupMutation(ctx, tx, workspaceID, cursor, origin)
}

func attachmentWithURLs(item Attachment) Attachment {
	path := "/api/attachments/" + item.ID + "/download"
	item.URL = path
	item.DownloadURL = path
	item.AttachmentDownloadURL = path + "?download=1"
	item.MarkdownURL = path
	return item
}

func attachmentContentType(headerValue string, data []byte) string {
	if parsed, _, err := mime.ParseMediaType(headerValue); err == nil && parsed != "" && parsed != "application/octet-stream" {
		return strings.ToLower(parsed)
	}
	return http.DetectContentType(data)
}

func safeAttachmentFilename(value string) string {
	name := strings.TrimSpace(filepath.Base(value))
	if name == "" || name == "." {
		return "attachment"
	}
	return name
}

func (s *Server) uploadFile(w http.ResponseWriter, r *http.Request) {
	ws, err := s.workspaceFor(r)
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxAttachmentBytes+(1<<20))
	if err := r.ParseMultipartForm(8 << 20); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid multipart upload: %w", err))
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		writeError(w, http.StatusBadRequest, errors.New("file is required"))
		return
	}
	defer file.Close()
	if header.Size > maxAttachmentBytes {
		writeError(w, http.StatusRequestEntityTooLarge, errAttachmentTooLarge)
		return
	}
	prefix := make([]byte, 512)
	prefixSize, err := io.ReadFull(file, prefix)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		s.fail(w, err)
		return
	}
	prefix = prefix[:prefixSize]
	issueID := strings.TrimSpace(r.FormValue("issue_id"))
	if issueID != "" {
		issue, err := s.store.GetIssue(r.Context(), ws.ID, issueID)
		if err != nil {
			s.respondStoreError(w, err)
			return
		}
		issueID = issue.ID
	}
	id := uuid.NewString()
	storagePath := filepath.Join("attachments", id)
	stream := &boundedAttachmentReader{reader: io.MultiReader(bytes.NewReader(prefix), file), remaining: maxAttachmentBytes}
	if err := s.files.Put(r.Context(), storagePath, stream); err != nil {
		if errors.Is(err, errAttachmentTooLarge) {
			writeError(w, http.StatusRequestEntityTooLarge, errAttachmentTooLarge)
			return
		}
		s.fail(w, err)
		return
	}
	size := maxAttachmentBytes - stream.remaining
	item, err := s.store.createAttachmentMetadata(r.Context(), id, ws.ID, issueID, safeAttachmentFilename(header.Filename), attachmentContentType(header.Header.Get("Content-Type"), prefix), storagePath, size)
	if err != nil {
		_ = s.files.Remove(storagePath)
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, attachmentWithURLs(item))
}

func (s *Server) getAttachment(w http.ResponseWriter, r *http.Request) {
	ws, err := s.workspaceFor(r)
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	item, err := s.store.getAttachment(r.Context(), ws.ID, chi.URLParam(r, "id"))
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, attachmentWithURLs(item))
}

func (s *Server) downloadAttachment(w http.ResponseWriter, r *http.Request) {
	ws, err := s.workspaceFor(r)
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	item, err := s.store.getAttachment(r.Context(), ws.ID, chi.URLParam(r, "id"))
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	file, err := s.files.Open(item.storagePath)
	if err != nil {
		s.fail(w, err)
		return
	}
	defer file.Close()
	disposition := "inline"
	if r.URL.Query().Get("download") == "1" {
		disposition = "attachment"
	}
	w.Header().Set("Content-Type", item.ContentType)
	w.Header().Set("Content-Length", fmt.Sprint(item.SizeBytes))
	w.Header().Set("Content-Disposition", mime.FormatMediaType(disposition, map[string]string{"filename": item.Filename}))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "private, no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, file)
}

func previewableAttachment(contentType string) bool {
	return strings.HasPrefix(contentType, "text/") || contentType == "application/json" || contentType == "application/xml" || contentType == "application/javascript"
}

func (s *Server) attachmentContent(w http.ResponseWriter, r *http.Request) {
	ws, err := s.workspaceFor(r)
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	item, err := s.store.getAttachment(r.Context(), ws.ID, chi.URLParam(r, "id"))
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	if !previewableAttachment(item.ContentType) {
		writeError(w, http.StatusUnsupportedMediaType, errors.New("attachment is not text-previewable"))
		return
	}
	if item.SizeBytes > maxAttachmentPreviewBytes {
		writeError(w, http.StatusRequestEntityTooLarge, errors.New("attachment is too large to preview"))
		return
	}
	file, err := s.files.Open(item.storagePath)
	if err != nil {
		s.fail(w, err)
		return
	}
	defer file.Close()
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("X-Original-Content-Type", item.ContentType)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, io.LimitReader(file, maxAttachmentPreviewBytes+1))
}

func (s *Server) deleteAttachment(w http.ResponseWriter, r *http.Request) {
	ws, err := s.workspaceFor(r)
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	item, err := s.store.getAttachment(r.Context(), ws.ID, chi.URLParam(r, "id"))
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	stagedPath := filepath.Join(".trash", item.ID+"-"+uuid.NewString())
	if err := s.files.Rename(item.storagePath, stagedPath); err != nil {
		s.fail(w, err)
		return
	}
	if err := s.store.deleteAttachment(r.Context(), ws.ID, item.ID); err != nil {
		if restoreErr := s.files.Rename(stagedPath, item.storagePath); restoreErr != nil {
			s.fail(w, errors.Join(err, fmt.Errorf("restore attachment after metadata delete failure: %w", restoreErr)))
			return
		}
		s.respondStoreError(w, err)
		return
	}
	if err := s.files.Remove(stagedPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		s.logger.Error("remove staged attachment after metadata delete", "error", err, "attachment_id", item.ID)
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) listIssueAttachments(w http.ResponseWriter, r *http.Request) {
	ws, err := s.workspaceFor(r)
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	issue, err := s.store.GetIssue(r.Context(), ws.ID, chi.URLParam(r, "id"))
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	items, err := s.store.listAttachments(r.Context(), ws.ID, issue.ID, "")
	if err != nil {
		s.fail(w, err)
		return
	}
	for index := range items {
		items[index] = attachmentWithURLs(items[index])
	}
	writeJSON(w, http.StatusOK, items)
}
