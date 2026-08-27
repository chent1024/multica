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

func (s *Server) agentBuilderSessions(w http.ResponseWriter, r *http.Request) {
	ws, err := s.workspaceFor(r)
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	if r.Method == http.MethodPost {
		var input struct {
			RuntimeID string `json:"runtime_id"`
			Model     string `json:"model"`
		}
		if err := decodeBody(r, &input); err != nil || strings.TrimSpace(input.RuntimeID) == "" {
			writeError(w, http.StatusBadRequest, errors.New("runtime_id is required"))
			return
		}
		var runtimeExists int
		if err := s.store.db.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM runtimes WHERE id=? AND workspace_id=? AND status='online'`, input.RuntimeID, ws.ID).Scan(&runtimeExists); err != nil || runtimeExists != 1 {
			writeError(w, http.StatusConflict, errors.New("selected local runtime is unavailable"))
			return
		}
		sessionID, agentID, stamp := uuid.NewString(), uuid.NewString(), now()
		agent := map[string]any{
			"name": "Agent Builder", "kind": "system", "description": "Local agent creation assistant",
			"instructions": "Help configure one Multica agent. Keep the proposed configuration in an <agent_draft> JSON block with name, description, instructions, model, skill_ids, and permission_scope.",
			"avatar_url":   nil, "runtime_mode": "local", "runtime_config": map[string]any{}, "custom_args": []any{}, "custom_env": map[string]any{},
			"visibility": "private", "permission_mode": "private", "invocation_targets": []any{}, "max_concurrent_tasks": 1,
			"model": input.Model, "owner_id": LocalOperatorID, "created_at": stamp, "updated_at": stamp,
		}
		raw, _ := encodeJSONMap(agent)
		tx, err := s.store.db.BeginTx(r.Context(), nil)
		if err != nil {
			s.fail(w, err)
			return
		}
		defer func() { _ = tx.Rollback() }()
		if _, err := tx.ExecContext(r.Context(), `INSERT INTO agents(id, workspace_id, runtime_id, name, data, created_at, updated_at) VALUES (?, ?, ?, 'Agent Builder', ?, ?, ?)`, agentID, ws.ID, input.RuntimeID, raw, stamp, stamp); err != nil {
			s.fail(w, err)
			return
		}
		if _, err := tx.ExecContext(r.Context(), `INSERT INTO chat_sessions(id, workspace_id, agent_id, creator_id, title, created_at, updated_at) VALUES (?, ?, ?, ?, 'Create an agent', ?, ?)`, sessionID, ws.ID, agentID, LocalOperatorID, stamp, stamp); err != nil {
			s.fail(w, err)
			return
		}
		if _, err := tx.ExecContext(r.Context(), `INSERT INTO agent_builder_sessions(id, workspace_id, runtime_id, chat_session_id, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)`, sessionID, ws.ID, input.RuntimeID, sessionID, stamp, stamp); err != nil {
			s.fail(w, err)
			return
		}
		if err := tx.Commit(); err != nil {
			s.fail(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{"session_id": sessionID, "builder_agent_id": agentID, "runtime_id": input.RuntimeID})
		return
	}
	rows, err := s.store.db.QueryContext(r.Context(), `SELECT b.id, b.runtime_id, b.draft, b.created_at, b.updated_at, c.title,
		COALESCE((SELECT content FROM chat_messages WHERE chat_session_id=c.id ORDER BY created_at DESC, id DESC LIMIT 1),''),
		COALESCE((SELECT role FROM chat_messages WHERE chat_session_id=c.id ORDER BY created_at DESC, id DESC LIMIT 1),''),
		COALESCE((SELECT created_at FROM chat_messages WHERE chat_session_id=c.id ORDER BY created_at DESC, id DESC LIMIT 1),'')
		FROM agent_builder_sessions b JOIN chat_sessions c ON c.id=b.chat_session_id
		WHERE b.workspace_id=? AND b.status='active' ORDER BY b.updated_at DESC, b.id`, ws.ID)
	if err != nil {
		s.fail(w, err)
		return
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var id, runtimeID, draftRaw, createdAt, updatedAt, title, content, role, messageAt string
		if err := rows.Scan(&id, &runtimeID, &draftRaw, &createdAt, &updatedAt, &title, &content, &role, &messageAt); err != nil {
			s.fail(w, err)
			return
		}
		var draft any
		if draftRaw != "{}" {
			_ = json.Unmarshal([]byte(draftRaw), &draft)
		}
		items = append(items, map[string]any{"session_id": id, "title": title, "runtime_id": runtimeID, "created_at": createdAt, "updated_at": updatedAt, "last_message_content": content, "last_message_role": role, "last_message_at": messageAt, "draft": draft})
	}
	writeJSON(w, http.StatusOK, map[string]any{"sessions": items})
}

func (s *Server) agentBuilderDraft(w http.ResponseWriter, r *http.Request) {
	ws, _ := s.workspaceFor(r)
	var input struct {
		Draft map[string]any `json:"draft"`
	}
	if err := decodeBody(r, &input); err != nil || input.Draft == nil {
		writeError(w, http.StatusBadRequest, errors.New("draft is required"))
		return
	}
	raw, _ := json.Marshal(input.Draft)
	result, err := s.store.db.ExecContext(r.Context(), `UPDATE agent_builder_sessions SET draft=?, updated_at=? WHERE id=? AND workspace_id=? AND status='active'`, string(raw), now(), chi.URLParam(r, "sessionID"), ws.ID)
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

func (s *Server) agentBuilderRuntime(w http.ResponseWriter, r *http.Request) {
	ws, _ := s.workspaceFor(r)
	var input struct {
		RuntimeID string `json:"runtime_id"`
	}
	if err := decodeBody(r, &input); err != nil || input.RuntimeID == "" {
		writeError(w, http.StatusBadRequest, errors.New("runtime_id is required"))
		return
	}
	tx, err := s.store.db.BeginTx(r.Context(), nil)
	if err != nil {
		s.fail(w, err)
		return
	}
	defer func() { _ = tx.Rollback() }()
	var agentID string
	if err := tx.QueryRowContext(r.Context(), `SELECT c.agent_id FROM agent_builder_sessions b JOIN chat_sessions c ON c.id=b.chat_session_id JOIN runtimes rt ON rt.id=? AND rt.workspace_id=b.workspace_id AND rt.status='online' WHERE b.id=? AND b.workspace_id=? AND b.status='active'`, input.RuntimeID, chi.URLParam(r, "sessionID"), ws.ID).Scan(&agentID); err != nil {
		s.respondStoreError(w, err)
		return
	}
	stamp := now()
	if _, err := tx.ExecContext(r.Context(), `UPDATE agent_builder_sessions SET runtime_id=?, updated_at=? WHERE id=?`, input.RuntimeID, stamp, chi.URLParam(r, "sessionID")); err != nil {
		s.fail(w, err)
		return
	}
	if _, err := tx.ExecContext(r.Context(), `UPDATE agents SET runtime_id=?, updated_at=? WHERE id=?`, input.RuntimeID, stamp, agentID); err != nil {
		s.fail(w, err)
		return
	}
	if err := tx.Commit(); err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"runtime_id": input.RuntimeID})
}
