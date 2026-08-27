package localworkbench

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

func localMember() map[string]any {
	stamp := "1970-01-01T00:00:00Z"
	return map[string]any{
		"id": LocalOperatorID, "workspace_id": LocalWorkspaceID, "user_id": LocalOperatorID,
		"role": "owner", "created_at": stamp, "name": "Local Operator",
		"email": "local@multica.invalid", "avatar_url": nil,
	}
}

func (s *Server) workspaceMembers(w http.ResponseWriter, r *http.Request) {
	ws, err := s.workspaceFor(r)
	if err != nil || (chi.URLParam(r, "id") != ws.ID && chi.URLParam(r, "id") != ws.Slug) {
		s.respondStoreError(w, sql.ErrNoRows)
		return
	}
	if r.Method != http.MethodGet {
		writeError(w, http.StatusConflict, errors.New("the fixed local workspace does not support human membership changes"))
		return
	}
	writeJSON(w, http.StatusOK, []any{localMember()})
}

func (s *Server) assigneeFrequency(w http.ResponseWriter, r *http.Request) {
	ws, err := s.workspaceFor(r)
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	rows, err := s.store.db.QueryContext(r.Context(), `SELECT assignee_type, assignee_id, COUNT(*) FROM issues WHERE workspace_id=? AND assignee_type IS NOT NULL AND assignee_id IS NOT NULL GROUP BY assignee_type, assignee_id ORDER BY COUNT(*) DESC, assignee_type, assignee_id`, ws.ID)
	if err != nil {
		s.fail(w, err)
		return
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var kind, id string
		var count int
		if err := rows.Scan(&kind, &id, &count); err != nil {
			s.fail(w, err)
			return
		}
		items = append(items, map[string]any{"assignee_type": kind, "assignee_id": id, "frequency": count})
	}
	if err := rows.Err(); err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, items)
}

func scanInbox(row rowScanner) (map[string]any, error) {
	var id, workspaceID, recipientType, recipientID, kind, severity, title, createdAt string
	var issueID, body sql.NullString
	var read, archived bool
	err := row.Scan(&id, &workspaceID, &recipientType, &recipientID, &kind, &severity, &issueID, &title, &body, &read, &archived, &createdAt)
	return map[string]any{
		"id": id, "workspace_id": workspaceID, "recipient_type": recipientType,
		"recipient_id": recipientID, "type": kind, "severity": severity,
		"issue_id": nullableString(issueID), "title": title, "body": nullableString(body),
		"read": read, "archived": archived, "created_at": createdAt,
	}, err
}

func insertInboxTx(tx *sql.Tx, workspaceID, kind, severity, issueID, title, body string) (string, error) {
	id := uuid.NewString()
	_, err := tx.Exec(`INSERT INTO inbox_items(id, workspace_id, recipient_type, recipient_id, type, severity, issue_id, title, body, created_at) VALUES (?, ?, 'member', ?, ?, ?, ?, ?, ?, ?)`, id, workspaceID, LocalOperatorID, kind, severity, nullable(issueID), title, nullable(body), now())
	return id, err
}

func (s *Server) listInbox(w http.ResponseWriter, r *http.Request) {
	ws, err := s.workspaceFor(r)
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	archived := strings.HasSuffix(r.URL.Path, "/archived")
	rows, err := s.store.db.QueryContext(r.Context(), `SELECT id, workspace_id, recipient_type, recipient_id, type, severity, issue_id, title, body, read, archived, created_at FROM inbox_items WHERE workspace_id=? AND recipient_id=? AND archived=? ORDER BY created_at DESC, id DESC LIMIT 500`, ws.ID, LocalOperatorID, archived)
	if err != nil {
		s.fail(w, err)
		return
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		item, err := scanInbox(rows)
		if err != nil {
			s.fail(w, err)
			return
		}
		items = append(items, item)
	}
	if err := rows.Close(); err != nil {
		s.fail(w, err)
		return
	}
	for _, item := range items {
		if issueID, ok := item["issue_id"].(string); ok && issueID != "" {
			var status, priority string
			if err := s.store.db.QueryRowContext(r.Context(), `SELECT status, priority FROM issues WHERE id=? AND workspace_id=?`, issueID, ws.ID).Scan(&status, &priority); err == nil {
				item["issue_status"], item["issue_priority"] = status, priority
			}
		}
	}
	writeJSON(w, http.StatusOK, items)
}

func (s *Server) inboxItemState(w http.ResponseWriter, r *http.Request) {
	ws, err := s.workspaceFor(r)
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	action := chi.URLParam(r, "action")
	column, value := "read", true
	switch action {
	case "read":
	case "unread":
		value = false
	case "archive":
		column, value = "archived", true
	case "unarchive":
		column, value = "archived", false
	default:
		writeError(w, http.StatusNotFound, errors.New("unknown inbox action"))
		return
	}
	id := chi.URLParam(r, "id")
	result, err := s.store.db.ExecContext(r.Context(), `UPDATE inbox_items SET `+column+`=? WHERE id=? AND workspace_id=? AND recipient_id=?`, value, id, ws.ID, LocalOperatorID)
	if err != nil {
		s.fail(w, err)
		return
	}
	if count, _ := result.RowsAffected(); count != 1 {
		s.respondStoreError(w, sql.ErrNoRows)
		return
	}
	item, err := scanInbox(s.store.db.QueryRowContext(r.Context(), `SELECT id, workspace_id, recipient_type, recipient_id, type, severity, issue_id, title, body, read, archived, created_at FROM inbox_items WHERE id=?`, id))
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func (s *Server) inboxCount(w http.ResponseWriter, r *http.Request) {
	ws, err := s.workspaceFor(r)
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	var count int
	if err := s.store.db.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM inbox_items WHERE workspace_id=? AND recipient_id=? AND read=0 AND archived=0`, ws.ID, LocalOperatorID).Scan(&count); err != nil {
		s.fail(w, err)
		return
	}
	if strings.HasSuffix(r.URL.Path, "/unread-summary") {
		items := []map[string]any{}
		if count > 0 {
			items = append(items, map[string]any{"workspace_id": ws.ID, "count": count})
		}
		writeJSON(w, http.StatusOK, items)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"count": count})
}

func (s *Server) inboxBulk(w http.ResponseWriter, r *http.Request) {
	ws, err := s.workspaceFor(r)
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	set, where := "read=1", "archived=0"
	switch {
	case strings.HasSuffix(r.URL.Path, "/archive-all-read"):
		set, where = "archived=1", "read=1 AND archived=0"
	case strings.HasSuffix(r.URL.Path, "/archive-completed"):
		set, where = "archived=1", `archived=0 AND issue_id IN (SELECT id FROM issues WHERE status IN ('done','cancelled','canceled'))`
	case strings.HasSuffix(r.URL.Path, "/archive-all"):
		set = "archived=1"
	}
	result, err := s.store.db.ExecContext(r.Context(), `UPDATE inbox_items SET `+set+` WHERE workspace_id=? AND recipient_id=? AND `+where, ws.ID, LocalOperatorID)
	if err != nil {
		s.fail(w, err)
		return
	}
	count, _ := result.RowsAffected()
	writeJSON(w, http.StatusOK, map[string]any{"count": count})
}

func defaultNotificationPreferences() map[string]any {
	return map[string]any{"assignments": "all", "status_changes": "all", "comments": "all", "mentions": "all", "updates": "all", "agent_activity": "all", "system_notifications": "all"}
}

func (s *Server) notificationPreferences(w http.ResponseWriter, r *http.Request) {
	ws, err := s.workspaceFor(r)
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	preferences := defaultNotificationPreferences()
	var raw string
	if err := s.store.db.QueryRowContext(r.Context(), `SELECT data FROM local_preferences WHERE workspace_id=? AND owner_id=? AND kind='notifications'`, ws.ID, LocalOperatorID).Scan(&raw); err == nil {
		_ = json.Unmarshal([]byte(raw), &preferences)
	} else if !errors.Is(err, sql.ErrNoRows) {
		s.fail(w, err)
		return
	}
	if r.Method == http.MethodPatch {
		var input struct {
			Preferences map[string]any `json:"preferences"`
		}
		if err := decodeBody(r, &input); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		for key, value := range input.Preferences {
			if value != "all" && value != "muted" {
				writeError(w, http.StatusBadRequest, errors.New("notification preference must be all or muted"))
				return
			}
			preferences[key] = value
		}
		raw, _ := json.Marshal(preferences)
		if _, err := s.store.db.ExecContext(r.Context(), `INSERT INTO local_preferences(workspace_id, owner_id, kind, data, updated_at) VALUES (?, ?, 'notifications', ?, ?) ON CONFLICT(workspace_id, owner_id, kind) DO UPDATE SET data=excluded.data, updated_at=excluded.updated_at`, ws.ID, LocalOperatorID, string(raw), now()); err != nil {
			s.fail(w, err)
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"workspace_id": ws.ID, "preferences": preferences})
}
