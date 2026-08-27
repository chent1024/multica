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

const localMikaAgentID = "00000000-0000-4000-8000-000000000004"

func (s *Server) updateMe(w http.ResponseWriter, r *http.Request) {
	input := map[string]any{}
	if err := decodeBody(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	identity, err := s.store.Identity(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	name, profile := identity.Operator.Name, identity.Operator.ProfileDescription
	var language, timezone any
	if identity.Operator.Language != nil {
		language = *identity.Operator.Language
	}
	if identity.Operator.Timezone != nil {
		timezone = *identity.Operator.Timezone
	}
	if candidate := strings.TrimSpace(stringFromAny(input["name"])); candidate != "" {
		name = candidate
	}
	if candidate, ok := input["language"].(string); ok {
		language = nullable(candidate)
	}
	if candidate, ok := input["profile_description"].(string); ok {
		profile = candidate
	}
	if candidate, ok := input["timezone"].(string); ok {
		timezone = nullable(candidate)
	}
	if _, err := s.store.db.ExecContext(r.Context(), `UPDATE users SET name=?, language=?, profile_description=?, timezone=?, updated_at=? WHERE id=?`, name, language, profile, timezone, now(), LocalOperatorID); err != nil {
		s.fail(w, err)
		return
	}
	s.getMe(w, r)
}

func (s *Server) patchOnboarding(w http.ResponseWriter, r *http.Request) {
	input := struct {
		Questionnaire map[string]any `json:"questionnaire"`
	}{}
	if err := decodeBody(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if input.Questionnaire == nil {
		input.Questionnaire = map[string]any{}
	}
	raw, _ := json.Marshal(input.Questionnaire)
	if _, err := s.store.db.ExecContext(r.Context(), `UPDATE users SET onboarding_questionnaire=?, updated_at=? WHERE id=?`, string(raw), now(), LocalOperatorID); err != nil {
		s.fail(w, err)
		return
	}
	s.getMe(w, r)
}

func (s *Server) completeOnboarding(w http.ResponseWriter, r *http.Request) {
	if _, err := s.store.db.ExecContext(r.Context(), `UPDATE users SET onboarded_at=COALESCE(onboarded_at, ?), updated_at=? WHERE id=?`, now(), now(), LocalOperatorID); err != nil {
		s.fail(w, err)
		return
	}
	s.getMe(w, r)
}

func (s *Server) cloudOnlyUnavailable(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusGone, map[string]any{"error": "This hosted account feature is unavailable in local Desktop mode", "code": "cloud_control_plane_removed"})
}

func (s *Server) createMikaAgent(w http.ResponseWriter, r *http.Request) {
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
	runtimeID := strings.TrimSpace(stringFromAny(input["runtime_id"]))
	if runtimeID == "" {
		writeError(w, http.StatusBadRequest, errors.New("runtime_id is required"))
		return
	}
	var found string
	if err := s.store.db.QueryRowContext(r.Context(), `SELECT id FROM runtimes WHERE id=? AND workspace_id=?`, runtimeID, ws.ID).Scan(&found); err != nil {
		s.respondStoreError(w, err)
		return
	}
	stamp := now()
	value := map[string]any{"id": localMikaAgentID, "workspace_id": ws.ID, "runtime_id": runtimeID, "name": "Mika", "description": "Local chief of staff", "instructions": "Help organize, delegate, and complete work in this local workspace.", "runtime_mode": "local", "runtime_config": map[string]any{}, "custom_args": []any{}, "custom_env": map[string]any{}, "visibility": "private", "permission_mode": "private", "invocation_targets": []any{}, "max_concurrent_tasks": 1, "model": stringFromAny(input["model"]), "owner_id": LocalOperatorID, "created_at": stamp, "updated_at": stamp}
	raw, _ := encodeJSONMap(value)
	if _, err := s.store.db.ExecContext(r.Context(), `INSERT INTO agents(id, workspace_id, runtime_id, name, data, created_at, updated_at) VALUES (?, ?, ?, 'Mika', ?, ?, ?) ON CONFLICT(id) DO UPDATE SET runtime_id=excluded.runtime_id, data=excluded.data, updated_at=excluded.updated_at`, localMikaAgentID, ws.ID, runtimeID, raw, stamp, stamp); err != nil {
		s.fail(w, err)
		return
	}
	var sessionID string
	err = s.store.db.QueryRowContext(r.Context(), `SELECT id FROM chat_sessions WHERE workspace_id=? AND agent_id=? AND creator_id=? ORDER BY created_at LIMIT 1`, ws.ID, localMikaAgentID, LocalOperatorID).Scan(&sessionID)
	if errors.Is(err, sql.ErrNoRows) {
		sessionID = uuid.NewString()
		title := strings.TrimSpace(stringFromAny(input["session_title"]))
		if title == "" {
			title = "Mika"
		}
		if _, err := s.store.db.ExecContext(r.Context(), `INSERT INTO chat_sessions(id, workspace_id, agent_id, creator_id, title, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)`, sessionID, ws.ID, localMikaAgentID, LocalOperatorID, title, stamp, stamp); err != nil {
			s.fail(w, err)
			return
		}
	} else if err != nil {
		s.fail(w, err)
		return
	}
	agent, err := s.store.readAgent(localMikaAgentID, ws.ID)
	if err != nil {
		s.fail(w, err)
		return
	}
	session, _ := s.store.readChatSession(sessionID, ws.ID)
	agent["onboarding_session"] = session
	writeJSON(w, http.StatusOK, agent)
}

func (s *Server) startMikaOnboarding(w http.ResponseWriter, r *http.Request) {
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
	if session["agent_id"] != localMikaAgentID {
		writeError(w, http.StatusConflict, errors.New("onboarding is only available for Mika"))
		return
	}
	var existingID, createdAt string
	err = s.store.db.QueryRowContext(r.Context(), `SELECT id, created_at FROM chat_messages WHERE chat_session_id=? AND message_kind='onboarding' LIMIT 1`, sessionID).Scan(&existingID, &createdAt)
	if err == nil {
		writeJSON(w, http.StatusOK, map[string]any{"started": false, "message_id": existingID, "created_at": createdAt})
		return
	}
	if !errors.Is(err, sql.ErrNoRows) {
		s.fail(w, err)
		return
	}
	input := map[string]any{}
	if err := decodeBody(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	message := "Hi, I'm Mika. Tell me what you want to accomplish and I'll help organize the work."
	if stringFromAny(input["language"]) == "zh" {
		message = "你好，我是 Mika。告诉我你想完成什么，我会帮你梳理并推进工作。"
	}
	id, stamp := uuid.NewString(), now()
	if _, err := s.store.db.ExecContext(r.Context(), `INSERT INTO chat_messages(id, chat_session_id, role, content, message_kind, created_at) VALUES (?, ?, 'assistant', ?, 'onboarding', ?)`, id, sessionID, message, stamp); err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"started": true, "message_id": id, "created_at": stamp})
}
