package localworkbench

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

func (s *Store) readChatSession(id, workspaceID string) (map[string]any, error) {
	var sessionID, agentID, creatorID, title, status, createdAt, updatedAt string
	var projectID, readAt sql.NullString
	var pinned bool
	if err := s.db.QueryRow(`SELECT id, agent_id, creator_id, project_id, title, status, pinned, read_at, created_at, updated_at
		FROM chat_sessions WHERE id=? AND workspace_id=?`, id, workspaceID).Scan(&sessionID, &agentID, &creatorID, &projectID, &title, &status, &pinned, &readAt, &createdAt, &updatedAt); err != nil {
		return nil, err
	}
	value := map[string]any{"id": sessionID, "workspace_id": workspaceID, "agent_id": agentID, "creator_id": creatorID, "project_id": nil, "title": title, "status": status, "has_unread": false, "unread_count": 0, "last_message": nil, "pinned": pinned, "created_at": createdAt, "updated_at": updatedAt}
	if projectID.Valid {
		value["project_id"] = projectID.String
	}
	readBoundary := ""
	if readAt.Valid {
		readBoundary = readAt.String
	}
	var unread int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM chat_messages WHERE chat_session_id=? AND role='assistant' AND created_at>?`, id, readBoundary).Scan(&unread); err != nil {
		return nil, err
	}
	value["unread_count"], value["has_unread"] = unread, unread > 0
	var messageID, role, content, messageCreatedAt string
	if err := s.db.QueryRow(`SELECT id, role, content, created_at FROM chat_messages WHERE chat_session_id=? ORDER BY created_at DESC, id DESC LIMIT 1`, id).Scan(&messageID, &role, &content, &messageCreatedAt); err == nil {
		value["last_message"] = map[string]any{"id": messageID, "role": role, "content": content, "created_at": messageCreatedAt}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	return value, nil
}

func scanChatMessage(row rowScanner) (map[string]any, error) {
	var id, sessionID, role, content, createdAt, messageKind string
	var taskID, failureReason sql.NullString
	var elapsed sql.NullInt64
	if err := row.Scan(&id, &sessionID, &role, &content, &taskID, &failureReason, &elapsed, &messageKind, &createdAt); err != nil {
		return nil, err
	}
	value := map[string]any{"id": id, "chat_session_id": sessionID, "role": role, "content": content, "task_id": nil, "failure_reason": nil, "elapsed_ms": nil, "message_kind": messageKind, "attachments": []any{}, "created_at": createdAt}
	if taskID.Valid {
		value["task_id"] = taskID.String
	}
	if failureReason.Valid {
		value["failure_reason"] = failureReason.String
	}
	if elapsed.Valid {
		value["elapsed_ms"] = elapsed.Int64
	}
	return value, nil
}

func (s *Server) listChatSessions(w http.ResponseWriter, r *http.Request) {
	ws, err := s.workspaceFor(r)
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	query, args := `SELECT id FROM chat_sessions c WHERE workspace_id=? AND NOT EXISTS (SELECT 1 FROM agent_builder_sessions b WHERE b.chat_session_id=c.id)`, []any{ws.ID}
	status := strings.TrimSpace(r.URL.Query().Get("status"))
	if status != "" && status != "all" {
		query += ` AND status=?`
		args = append(args, status)
	}
	query += ` ORDER BY pinned DESC, updated_at DESC, id`
	rows, err := s.store.db.QueryContext(r.Context(), query, args...)
	if err != nil {
		s.fail(w, err)
		return
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			s.fail(w, err)
			return
		}
		ids = append(ids, id)
	}
	_ = rows.Close()
	items := []map[string]any{}
	for _, id := range ids {
		item, err := s.store.readChatSession(id, ws.ID)
		if err != nil {
			s.fail(w, err)
			return
		}
		items = append(items, item)
	}
	writeJSON(w, http.StatusOK, items)
}

func (s *Server) createChatSession(w http.ResponseWriter, r *http.Request) {
	ws, err := s.workspaceFor(r)
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	input := struct {
		AgentID   string  `json:"agent_id"`
		Title     string  `json:"title"`
		ProjectID *string `json:"project_id"`
	}{}
	if err := decodeBody(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	var agentName string
	if err := s.store.db.QueryRowContext(r.Context(), `SELECT name FROM agents WHERE id=? AND workspace_id=? AND archived_at IS NULL`, input.AgentID, ws.ID).Scan(&agentName); err != nil {
		s.respondStoreError(w, err)
		return
	}
	if input.ProjectID != nil && strings.TrimSpace(*input.ProjectID) != "" {
		var exists int
		if err := s.store.db.QueryRowContext(r.Context(), `SELECT 1 FROM projects WHERE id=? AND workspace_id=?`, *input.ProjectID, ws.ID).Scan(&exists); err != nil {
			s.respondStoreError(w, err)
			return
		}
	}
	if strings.TrimSpace(input.Title) == "" {
		input.Title = agentName
	}
	id, stamp := uuid.NewString(), now()
	if _, err := s.store.db.ExecContext(r.Context(), `INSERT INTO chat_sessions(id, workspace_id, agent_id, creator_id, project_id, title, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, id, ws.ID, input.AgentID, LocalOperatorID, input.ProjectID, input.Title, stamp, stamp); err != nil {
		s.fail(w, err)
		return
	}
	item, _ := s.store.readChatSession(id, ws.ID)
	s.hub.broadcast("chat:session_created", map[string]any{"session": item})
	writeJSON(w, http.StatusCreated, item)
}

func (s *Server) chatSessionItem(w http.ResponseWriter, r *http.Request) {
	ws, err := s.workspaceFor(r)
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	id := chi.URLParam(r, "id")
	if _, err := s.store.readChatSession(id, ws.ID); err != nil {
		s.respondStoreError(w, err)
		return
	}
	if r.Method == http.MethodGet {
		item, _ := s.store.readChatSession(id, ws.ID)
		writeJSON(w, http.StatusOK, item)
		return
	}
	if r.Method == http.MethodDelete {
		tx, err := s.store.db.BeginTx(r.Context(), nil)
		if err != nil {
			s.fail(w, err)
			return
		}
		defer func() { _ = tx.Rollback() }()
		_, _ = tx.ExecContext(r.Context(), `DELETE FROM chat_tasks WHERE chat_session_id=?`, id)
		_, _ = tx.ExecContext(r.Context(), `DELETE FROM chat_messages WHERE chat_session_id=?`, id)
		_, _ = tx.ExecContext(r.Context(), `DELETE FROM chat_sessions WHERE id=? AND workspace_id=?`, id, ws.ID)
		if err := tx.Commit(); err != nil {
			s.fail(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	input := map[string]any{}
	if err := decodeBody(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	set, args := []string{"updated_at=?"}, []any{now()}
	if title, ok := input["title"].(string); ok && strings.TrimSpace(title) != "" {
		set, args = append(set, "title=?"), append(args, strings.TrimSpace(title))
	}
	if projectID, exists := input["project_id"]; exists {
		set, args = append(set, "project_id=?"), append(args, projectID)
	}
	args = append(args, id, ws.ID)
	if _, err := s.store.db.ExecContext(r.Context(), `UPDATE chat_sessions SET `+strings.Join(set, ",")+` WHERE id=? AND workspace_id=?`, args...); err != nil {
		s.fail(w, err)
		return
	}
	item, _ := s.store.readChatSession(id, ws.ID)
	writeJSON(w, http.StatusOK, item)
}

func (s *Server) chatSessionFlag(w http.ResponseWriter, r *http.Request, field string) {
	ws, err := s.workspaceFor(r)
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	input := map[string]any{}
	if err := decodeBody(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	value, ok := input[field].(bool)
	if !ok {
		writeError(w, http.StatusBadRequest, errors.New(field+" is required"))
		return
	}
	column, stored := "pinned", any(value)
	if field == "archived" {
		column = "status"
		stored = "active"
		if value {
			stored = "archived"
		}
	}
	id := chi.URLParam(r, "id")
	result, err := s.store.db.ExecContext(r.Context(), `UPDATE chat_sessions SET `+column+`=?, updated_at=? WHERE id=? AND workspace_id=?`, stored, now(), id, ws.ID)
	if err != nil {
		s.fail(w, err)
		return
	}
	if count, _ := result.RowsAffected(); count != 1 {
		s.respondStoreError(w, sql.ErrNoRows)
		return
	}
	item, _ := s.store.readChatSession(id, ws.ID)
	writeJSON(w, http.StatusOK, item)
}

func (s *Server) pinChatSession(w http.ResponseWriter, r *http.Request) {
	s.chatSessionFlag(w, r, "pinned")
}

func (s *Server) archiveChatSession(w http.ResponseWriter, r *http.Request) {
	s.chatSessionFlag(w, r, "archived")
}

func (s *Server) markChatRead(w http.ResponseWriter, r *http.Request) {
	ws, err := s.workspaceFor(r)
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	result, err := s.store.db.ExecContext(r.Context(), `UPDATE chat_sessions SET read_at=? WHERE id=? AND workspace_id=?`, now(), chi.URLParam(r, "id"), ws.ID)
	if err != nil {
		s.fail(w, err)
		return
	}
	if count, _ := result.RowsAffected(); count != 1 {
		s.respondStoreError(w, sql.ErrNoRows)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) regenerateChatQuickActions(w http.ResponseWriter, r *http.Request) {
	ws, err := s.workspaceFor(r)
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	sessionID := chi.URLParam(r, "id")
	if _, err := s.store.readChatSession(sessionID, ws.ID); err != nil {
		s.respondStoreError(w, err)
		return
	}
	var input struct {
		MessageID string `json:"message_id"`
	}
	if err := decodeBody(r, &input); err != nil || strings.TrimSpace(input.MessageID) == "" {
		writeError(w, http.StatusBadRequest, errors.New("message_id is required"))
		return
	}
	var latestID string
	if err := s.store.db.QueryRowContext(r.Context(), `SELECT id FROM chat_messages WHERE chat_session_id=? AND role='assistant' ORDER BY created_at DESC,id DESC LIMIT 1`, sessionID).Scan(&latestID); err != nil {
		s.respondStoreError(w, err)
		return
	}
	if latestID != input.MessageID {
		writeError(w, http.StatusConflict, errors.New("quick actions can only be regenerated for the latest assistant message"))
		return
	}
	s.hub.broadcast("chat:quick_actions", map[string]any{"chat_session_id": sessionID, "message_id": latestID, "quick_actions": []any{}})
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) chatMessages(w http.ResponseWriter, r *http.Request) {
	ws, err := s.workspaceFor(r)
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	sessionID := chi.URLParam(r, "id")
	session, err := s.store.readChatSession(sessionID, ws.ID)
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	if r.Method == http.MethodGet {
		rows, err := s.store.db.QueryContext(r.Context(), `SELECT id, chat_session_id, role, content, task_id, failure_reason, elapsed_ms, message_kind, created_at FROM chat_messages WHERE chat_session_id=? ORDER BY created_at, id`, sessionID)
		if err != nil {
			s.fail(w, err)
			return
		}
		items := []map[string]any{}
		for rows.Next() {
			item, err := scanChatMessage(rows)
			if err != nil {
				_ = rows.Close()
				s.fail(w, err)
				return
			}
			items = append(items, item)
		}
		_ = rows.Close()
		writeJSON(w, http.StatusOK, items)
		return
	}
	input := struct {
		Content       string   `json:"content"`
		AttachmentIDs []string `json:"attachment_ids"`
	}{}
	if err := decodeBody(r, &input); err != nil || strings.TrimSpace(input.Content) == "" {
		writeError(w, http.StatusBadRequest, errors.New("content is required"))
		return
	}
	agentID := stringFromAny(session["agent_id"])
	var runtimeID string
	if err := s.store.db.QueryRowContext(r.Context(), `SELECT a.runtime_id FROM agents a JOIN runtimes rt ON rt.id=a.runtime_id WHERE a.id=? AND a.workspace_id=? AND a.archived_at IS NULL AND rt.status='online'`, agentID, ws.ID).Scan(&runtimeID); err != nil {
		writeError(w, http.StatusConflict, errors.New("agent runtime is unavailable"))
		return
	}
	stamp, messageID, taskID := now(), uuid.NewString(), uuid.NewString()
	tx, err := s.store.db.BeginTx(r.Context(), nil)
	if err != nil {
		s.fail(w, err)
		return
	}
	defer func() { _ = tx.Rollback() }()
	var active int
	if err := tx.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM chat_tasks WHERE chat_session_id=? AND status IN ('queued','dispatched','running')`, sessionID).Scan(&active); err != nil {
		s.fail(w, err)
		return
	}
	if _, err := tx.ExecContext(r.Context(), `INSERT INTO chat_messages(id, chat_session_id, role, content, task_id, created_at) VALUES (?, ?, 'user', ?, ?, ?)`, messageID, sessionID, input.Content, taskID, stamp); err != nil {
		s.fail(w, err)
		return
	}
	if _, err := tx.ExecContext(r.Context(), `INSERT INTO chat_tasks(id, chat_session_id, status, runtime_id, agent_id, created_at, updated_at) VALUES (?, ?, 'queued', ?, ?, ?, ?)`, taskID, sessionID, runtimeID, agentID, stamp, stamp); err != nil {
		s.fail(w, err)
		return
	}
	if _, err := tx.ExecContext(r.Context(), `UPDATE chat_sessions SET updated_at=? WHERE id=?`, stamp, sessionID); err != nil {
		s.fail(w, err)
		return
	}
	if err := bindChatAttachmentsTx(r.Context(), tx, ws.ID, sessionID, messageID, input.AttachmentIDs); err != nil {
		s.respondStoreError(w, err)
		return
	}
	if err := tx.Commit(); err != nil {
		s.fail(w, err)
		return
	}
	s.hub.broadcast("chat:message_created", map[string]any{"message_id": messageID, "chat_session_id": sessionID})
	writeJSON(w, http.StatusCreated, map[string]any{"message_id": messageID, "task_id": taskID, "supports_queue": true, "queued": active > 0, "created_at": stamp, "attachment_ids": input.AttachmentIDs})
}

func bindChatAttachmentsTx(ctx context.Context, tx *sql.Tx, workspaceID, sessionID, messageID string, ids []string) error {
	for _, id := range ids {
		result, err := tx.ExecContext(ctx, `UPDATE attachments SET chat_session_id=?, chat_message_id=? WHERE id=? AND workspace_id=? AND issue_id IS NULL AND comment_id IS NULL AND chat_message_id IS NULL`, sessionID, messageID, id, workspaceID)
		if err != nil {
			return err
		}
		if count, _ := result.RowsAffected(); count != 1 {
			return sql.ErrNoRows
		}
	}
	return nil
}

func (s *Server) chatMessagesPage(w http.ResponseWriter, r *http.Request) {
	ws, err := s.workspaceFor(r)
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	sessionID := chi.URLParam(r, "id")
	if _, err := s.store.readChatSession(sessionID, ws.ID); err != nil {
		s.respondStoreError(w, err)
		return
	}
	limit := parseLimit(r, 50)
	rows, err := s.store.db.QueryContext(r.Context(), `SELECT id, chat_session_id, role, content, task_id, failure_reason, elapsed_ms, message_kind, created_at FROM chat_messages WHERE chat_session_id=? ORDER BY created_at DESC, id DESC LIMIT ?`, sessionID, limit+1)
	if err != nil {
		s.fail(w, err)
		return
	}
	items := []map[string]any{}
	for rows.Next() {
		item, err := scanChatMessage(rows)
		if err != nil {
			_ = rows.Close()
			s.fail(w, err)
			return
		}
		items = append(items, item)
	}
	_ = rows.Close()
	hasMore := len(items) > limit
	if hasMore {
		items = items[:limit]
	}
	for left, right := 0, len(items)-1; left < right; left, right = left+1, right-1 {
		items[left], items[right] = items[right], items[left]
	}
	writeJSON(w, http.StatusOK, map[string]any{"messages": items, "limit": limit, "has_more": hasMore, "next_cursor": nil})
}

func (s *Server) chatPinnedAgents(w http.ResponseWriter, r *http.Request) {
	ws, err := s.workspaceFor(r)
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	if r.Method == http.MethodGet {
		rows, err := s.store.db.QueryContext(r.Context(), `SELECT p.agent_id, p.created_at FROM chat_pinned_agents p JOIN agents a ON a.id=p.agent_id WHERE p.workspace_id=? AND a.archived_at IS NULL ORDER BY p.created_at, p.agent_id`, ws.ID)
		if err != nil {
			s.fail(w, err)
			return
		}
		items := []map[string]any{}
		for rows.Next() {
			var agentID, createdAt string
			if err := rows.Scan(&agentID, &createdAt); err != nil {
				_ = rows.Close()
				s.fail(w, err)
				return
			}
			items = append(items, map[string]any{"agent_id": agentID, "created_at": createdAt})
		}
		_ = rows.Close()
		writeJSON(w, http.StatusOK, items)
		return
	}
	input := struct {
		AgentID string `json:"agent_id"`
	}{}
	if err := decodeBody(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	stamp := now()
	if _, err := s.store.db.ExecContext(r.Context(), `INSERT INTO chat_pinned_agents(agent_id, workspace_id, created_at) SELECT id, ?, ? FROM agents WHERE id=? AND workspace_id=? ON CONFLICT(agent_id) DO NOTHING`, ws.ID, stamp, input.AgentID, ws.ID); err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"agent_id": input.AgentID, "created_at": stamp})
}

func (s *Server) unpinChatAgent(w http.ResponseWriter, r *http.Request) {
	ws, err := s.workspaceFor(r)
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	_, _ = s.store.db.ExecContext(r.Context(), `DELETE FROM chat_pinned_agents WHERE agent_id=? AND workspace_id=?`, chi.URLParam(r, "agentID"), ws.ID)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) pendingChatTasks(w http.ResponseWriter, r *http.Request) {
	ws, err := s.workspaceFor(r)
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	rows, err := s.store.db.QueryContext(r.Context(), `SELECT t.id, t.status, t.chat_session_id FROM chat_tasks t JOIN chat_sessions s ON s.id=t.chat_session_id WHERE s.workspace_id=? AND t.status IN ('queued','dispatched','running') ORDER BY t.created_at`, ws.ID)
	if err != nil {
		s.fail(w, err)
		return
	}
	items := []map[string]any{}
	for rows.Next() {
		var id, status, sessionID string
		if err := rows.Scan(&id, &status, &sessionID); err != nil {
			_ = rows.Close()
			s.fail(w, err)
			return
		}
		items = append(items, map[string]any{"task_id": id, "status": status, "chat_session_id": sessionID})
	}
	_ = rows.Close()
	if strings.HasSuffix(r.URL.Path, "/has-any") {
		writeJSON(w, http.StatusOK, map[string]any{"has_pending": len(items) > 0})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"tasks": items})
}

func (s *Server) pendingChatTask(w http.ResponseWriter, r *http.Request) {
	ws, err := s.workspaceFor(r)
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	sessionID := chi.URLParam(r, "id")
	rows, err := s.store.db.QueryContext(r.Context(), `SELECT t.id, t.status, t.created_at FROM chat_tasks t JOIN chat_sessions s ON s.id=t.chat_session_id WHERE t.chat_session_id=? AND s.workspace_id=? AND t.status IN ('queued','dispatched','running','waiting_local_directory') ORDER BY CASE WHEN t.status='queued' THEN 1 ELSE 0 END, t.priority DESC, t.created_at, t.id`, sessionID, ws.ID)
	if err != nil {
		s.fail(w, err)
		return
	}
	defer rows.Close()
	result := map[string]any{"supports_queue": true, "queued_tasks": []any{}}
	queued := []map[string]any{}
	for rows.Next() {
		var id, status, createdAt string
		if err := rows.Scan(&id, &status, &createdAt); err != nil {
			s.fail(w, err)
			return
		}
		item := map[string]any{"task_id": id, "status": status, "created_at": createdAt}
		if _, exists := result["task_id"]; !exists {
			result["task_id"], result["status"], result["created_at"] = id, status, createdAt
		}
		if status == "queued" {
			queued = append(queued, item)
		}
	}
	result["queued_tasks"] = queued
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) prioritizeQueuedChatTask(w http.ResponseWriter, r *http.Request) {
	ws, _ := s.workspaceFor(r)
	sessionID, taskID := chi.URLParam(r, "id"), chi.URLParam(r, "taskID")
	var priority int
	_ = s.store.db.QueryRowContext(r.Context(), `SELECT COALESCE(MAX(priority),0)+1 FROM chat_tasks WHERE chat_session_id=? AND status='queued'`, sessionID).Scan(&priority)
	result, err := s.store.db.ExecContext(r.Context(), `UPDATE chat_tasks SET priority=?, updated_at=? WHERE id=? AND chat_session_id=? AND status='queued' AND chat_session_id IN (SELECT id FROM chat_sessions WHERE workspace_id=?)`, priority, now(), taskID, sessionID, ws.ID)
	if err != nil {
		s.fail(w, err)
		return
	}
	if count, _ := result.RowsAffected(); count != 1 {
		writeError(w, http.StatusConflict, errors.New("queued task is no longer available"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"task_id": taskID})
}

func (s *Server) clearQueuedChatTasks(w http.ResponseWriter, r *http.Request) {
	ws, _ := s.workspaceFor(r)
	sessionID := chi.URLParam(r, "id")
	tx, err := s.store.db.BeginTx(r.Context(), nil)
	if err != nil {
		s.fail(w, err)
		return
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(r.Context(), `UPDATE attachments SET chat_session_id=NULL, chat_message_id=NULL WHERE chat_message_id IN (SELECT id FROM chat_messages WHERE task_id IN (SELECT t.id FROM chat_tasks t JOIN chat_sessions s ON s.id=t.chat_session_id WHERE t.chat_session_id=? AND s.workspace_id=? AND t.status='queued'))`, sessionID, ws.ID); err != nil {
		s.fail(w, err)
		return
	}
	if _, err := tx.ExecContext(r.Context(), `DELETE FROM chat_messages WHERE task_id IN (SELECT t.id FROM chat_tasks t JOIN chat_sessions s ON s.id=t.chat_session_id WHERE t.chat_session_id=? AND s.workspace_id=? AND t.status='queued')`, sessionID, ws.ID); err != nil {
		s.fail(w, err)
		return
	}
	if _, err := tx.ExecContext(r.Context(), `DELETE FROM chat_tasks WHERE chat_session_id=? AND status='queued' AND chat_session_id IN (SELECT id FROM chat_sessions WHERE workspace_id=?)`, sessionID, ws.ID); err != nil {
		s.fail(w, err)
		return
	}
	if err := tx.Commit(); err != nil {
		s.fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) cancelTaskByID(w http.ResponseWriter, r *http.Request) {
	ws, err := s.workspaceFor(r)
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	taskID := chi.URLParam(r, "taskID")
	ref, err := s.daemonTaskRef(r.Context(), ws.ID, taskID)
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	stamp := now()
	if ref.Kind == "issue" {
		result, err := s.store.db.ExecContext(r.Context(), `UPDATE task_runs SET status='cancelled', completed_at=?, updated_at=? WHERE id=? AND issue_id=? AND status NOT IN ('completed','failed','cancelled')`, stamp, stamp, taskID, ref.ContainerID)
		if err != nil {
			s.fail(w, err)
			return
		}
		if count, _ := result.RowsAffected(); count != 1 {
			writeError(w, http.StatusConflict, errors.New("task is already terminal"))
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"id": taskID, "agent_id": LocalAgentID, "runtime_id": "", "issue_id": ref.ContainerID, "status": "cancelled", "priority": 0, "dispatched_at": nil, "started_at": nil, "completed_at": stamp, "result": nil, "error": nil, "created_at": stamp})
		return
	}
	var runtimeID, agentID, status, createdAt string
	if err := s.store.db.QueryRowContext(r.Context(), `SELECT runtime_id, agent_id, status, created_at FROM chat_tasks WHERE id=? AND chat_session_id=?`, taskID, ref.ContainerID).Scan(&runtimeID, &agentID, &status, &createdAt); err != nil {
		s.respondStoreError(w, err)
		return
	}
	if expected := r.URL.Query().Get("expected_status"); expected != "" && status != expected {
		writeError(w, http.StatusConflict, fmt.Errorf("task status is %s, expected %s", status, expected))
		return
	}
	tx, err := s.store.db.BeginTx(r.Context(), nil)
	if err != nil {
		s.fail(w, err)
		return
	}
	defer func() { _ = tx.Rollback() }()
	var cancelledMessage map[string]any
	var restoredMessageID string
	if status == "queued" {
		var messageID, content string
		if err := tx.QueryRowContext(r.Context(), `SELECT id, content FROM chat_messages WHERE chat_session_id=? AND task_id=? AND role='user'`, ref.ContainerID, taskID).Scan(&messageID, &content); err == nil {
			_, _ = tx.ExecContext(r.Context(), `UPDATE attachments SET chat_session_id=NULL, chat_message_id=NULL WHERE chat_message_id=?`, messageID)
			_, _ = tx.ExecContext(r.Context(), `DELETE FROM chat_messages WHERE id=?`, messageID)
			cancelledMessage = map[string]any{"chat_session_id": ref.ContainerID, "message_id": messageID, "content": content, "restore_to_input": r.URL.Query().Get("queue_action") == "edit", "attachments": []any{}}
		}
	} else {
		var messageID, content string
		if err := tx.QueryRowContext(r.Context(), `SELECT id, content FROM chat_messages WHERE chat_session_id=? AND task_id=? AND role='user'`, ref.ContainerID, taskID).Scan(&messageID, &content); err == nil {
			rows, queryErr := tx.QueryContext(r.Context(), `SELECT id FROM attachments WHERE chat_message_id=? ORDER BY created_at,id`, messageID)
			if queryErr != nil {
				s.fail(w, queryErr)
				return
			}
			attachmentIDs := []string{}
			for rows.Next() {
				var attachmentID string
				if err := rows.Scan(&attachmentID); err != nil {
					_ = rows.Close()
					s.fail(w, err)
					return
				}
				attachmentIDs = append(attachmentIDs, attachmentID)
			}
			if err := rows.Close(); err != nil {
				s.fail(w, err)
				return
			}
			encodedAttachmentIDs, _ := json.Marshal(attachmentIDs)
			if _, err := tx.ExecContext(r.Context(), `UPDATE attachments SET chat_session_id=NULL, chat_message_id=NULL WHERE chat_message_id=?`, messageID); err != nil {
				s.fail(w, err)
				return
			}
			if _, err := tx.ExecContext(r.Context(), `DELETE FROM chat_messages WHERE id=?`, messageID); err != nil {
				s.fail(w, err)
				return
			}
			if _, err := tx.ExecContext(r.Context(), `INSERT INTO chat_draft_restores(id,chat_session_id,task_id,content,attachment_ids,created_at) VALUES(?,?,?,?,?,?) ON CONFLICT(id) DO NOTHING`, messageID, ref.ContainerID, taskID, content, string(encodedAttachmentIDs), stamp); err != nil {
				s.fail(w, err)
				return
			}
			restoredMessageID = messageID
		} else if !errors.Is(err, sql.ErrNoRows) {
			s.fail(w, err)
			return
		}
	}
	result, err := tx.ExecContext(r.Context(), `UPDATE chat_tasks SET status='cancelled', completed_at=?, updated_at=? WHERE id=? AND chat_session_id=? AND status NOT IN ('completed','failed','cancelled')`, stamp, stamp, taskID, ref.ContainerID)
	if err != nil {
		s.fail(w, err)
		return
	}
	if count, _ := result.RowsAffected(); count != 1 {
		writeError(w, http.StatusConflict, errors.New("task is already terminal"))
		return
	}
	if err := tx.Commit(); err != nil {
		s.fail(w, err)
		return
	}
	payload := map[string]any{"id": taskID, "agent_id": agentID, "runtime_id": runtimeID, "issue_id": "", "chat_session_id": ref.ContainerID, "status": "cancelled", "priority": 0, "dispatched_at": nil, "started_at": nil, "completed_at": stamp, "result": nil, "error": nil, "created_at": createdAt}
	if cancelledMessage != nil {
		payload["cancelled_chat_message"] = cancelledMessage
	}
	if restoredMessageID != "" {
		s.hub.broadcast("chat:cancel_finalized", map[string]any{
			"outcome": "restored", "chat_session_id": ref.ContainerID, "task_id": taskID,
			"initiator_user_id": LocalOperatorID, "message_id": restoredMessageID,
		})
	}
	writeJSON(w, http.StatusOK, payload)
}

func (s *Server) listChatDraftRestores(w http.ResponseWriter, r *http.Request) {
	ws, err := s.workspaceFor(r)
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	sessionID := chi.URLParam(r, "id")
	var exists int
	if err := s.store.db.QueryRowContext(r.Context(), `SELECT 1 FROM chat_sessions WHERE id=? AND workspace_id=?`, sessionID, ws.ID).Scan(&exists); err != nil {
		s.respondStoreError(w, err)
		return
	}
	rows, err := s.store.db.QueryContext(r.Context(), `SELECT id,task_id,content,attachment_ids,created_at FROM chat_draft_restores WHERE chat_session_id=? ORDER BY created_at,id`, sessionID)
	if err != nil {
		s.fail(w, err)
		return
	}
	type rawRestore struct {
		id, taskID, content, attachmentIDs, createdAt string
	}
	rawRestores := []rawRestore{}
	for rows.Next() {
		var item rawRestore
		if err := rows.Scan(&item.id, &item.taskID, &item.content, &item.attachmentIDs, &item.createdAt); err != nil {
			_ = rows.Close()
			s.fail(w, err)
			return
		}
		rawRestores = append(rawRestores, item)
	}
	if err := rows.Close(); err != nil {
		s.fail(w, err)
		return
	}
	if err := rows.Err(); err != nil {
		s.fail(w, err)
		return
	}
	restores := []map[string]any{}
	for _, restore := range rawRestores {
		attachmentIDs := []string{}
		_ = json.Unmarshal([]byte(restore.attachmentIDs), &attachmentIDs)
		attachments := []Attachment{}
		for _, attachmentID := range attachmentIDs {
			item, err := s.store.getAttachment(r.Context(), ws.ID, attachmentID)
			if err == nil {
				attachments = append(attachments, attachmentWithURLs(item))
			} else if !errors.Is(err, sql.ErrNoRows) {
				s.fail(w, err)
				return
			}
		}
		restores = append(restores, map[string]any{
			"id": restore.id, "chat_session_id": sessionID, "task_id": restore.taskID, "content": restore.content,
			"attachments": attachments, "created_at": restore.createdAt,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"restores": restores})
}

func (s *Server) consumeChatDraftRestore(w http.ResponseWriter, r *http.Request) {
	ws, err := s.workspaceFor(r)
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	if _, err := s.store.db.ExecContext(r.Context(), `DELETE FROM chat_draft_restores WHERE id=? AND chat_session_id=? AND chat_session_id IN (SELECT id FROM chat_sessions WHERE workspace_id=?)`, chi.URLParam(r, "restoreID"), chi.URLParam(r, "id"), ws.ID); err != nil {
		s.fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
