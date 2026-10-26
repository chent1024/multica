package localworkbench

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"maps"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

func scanRuntimeProfile(row rowScanner) (map[string]any, error) {
	var id, workspaceID, name, family, command, fixedRaw, visibility, createdAt, updatedAt string
	var description, createdBy sql.NullString
	var enabled bool
	err := row.Scan(&id, &workspaceID, &name, &family, &command, &description, &fixedRaw, &visibility, &createdBy, &enabled, &createdAt, &updatedAt)
	fixed := []string{}
	_ = json.Unmarshal([]byte(fixedRaw), &fixed)
	return map[string]any{"id": id, "workspace_id": workspaceID, "display_name": name, "protocol_family": family, "command_name": command, "description": nullableString(description), "fixed_args": fixed, "visibility": visibility, "created_by": nullableString(createdBy), "enabled": enabled, "created_at": createdAt, "updated_at": updatedAt}, err
}

func (s *Server) runtimeProfiles(w http.ResponseWriter, r *http.Request) {
	ws, err := s.workspaceFor(r)
	if err != nil || (chi.URLParam(r, "id") != ws.ID && chi.URLParam(r, "id") != ws.Slug) {
		s.respondStoreError(w, sql.ErrNoRows)
		return
	}
	if r.Method == http.MethodPost {
		input := map[string]any{}
		if err := decodeBody(r, &input); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		name, family, command := strings.TrimSpace(stringFromAny(input["display_name"])), strings.TrimSpace(stringFromAny(input["protocol_family"])), strings.TrimSpace(stringFromAny(input["command_name"]))
		if name == "" || family == "" || command == "" {
			writeError(w, http.StatusBadRequest, errors.New("display_name, protocol_family and command_name are required"))
			return
		}
		fixed, _ := json.Marshal(input["fixed_args"])
		if string(fixed) == "null" {
			fixed = []byte("[]")
		}
		id, stamp := uuid.NewString(), now()
		if _, err := s.store.db.ExecContext(r.Context(), `INSERT INTO runtime_profiles(id, workspace_id, display_name, protocol_family, command_name, description, fixed_args, visibility, created_by, enabled, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, id, ws.ID, name, family, command, input["description"], string(fixed), stringDefault(input["visibility"], "private"), LocalOperatorID, boolDefault(input["enabled"], true), stamp, stamp); err != nil {
			s.fail(w, err)
			return
		}
		item, _ := scanRuntimeProfile(s.store.db.QueryRowContext(r.Context(), `SELECT id, workspace_id, display_name, protocol_family, command_name, description, fixed_args, visibility, created_by, enabled, created_at, updated_at FROM runtime_profiles WHERE id=?`, id))
		s.hub.broadcast("daemon:runtime_profiles_changed", map[string]any{"workspace_id": ws.ID})
		writeJSON(w, http.StatusCreated, item)
		return
	}
	rows, err := s.store.db.QueryContext(r.Context(), `SELECT id, workspace_id, display_name, protocol_family, command_name, description, fixed_args, visibility, created_by, enabled, created_at, updated_at FROM runtime_profiles WHERE workspace_id=? ORDER BY display_name, id`, ws.ID)
	if err != nil {
		s.fail(w, err)
		return
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		item, err := scanRuntimeProfile(rows)
		if err != nil {
			s.fail(w, err)
			return
		}
		items = append(items, item)
	}
	writeJSON(w, http.StatusOK, map[string]any{"workspace_id": ws.ID, "runtime_profiles": items})
}

func (s *Server) runtimeProfileItem(w http.ResponseWriter, r *http.Request) {
	ws, _ := s.workspaceFor(r)
	profileID := chi.URLParam(r, "profileID")
	if r.Method == http.MethodGet {
		item, err := scanRuntimeProfile(s.store.db.QueryRowContext(r.Context(), `SELECT id, workspace_id, display_name, protocol_family, command_name, description, fixed_args, visibility, created_by, enabled, created_at, updated_at FROM runtime_profiles WHERE id=? AND workspace_id=?`, profileID, ws.ID))
		if err != nil {
			s.respondStoreError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, item)
		return
	}
	if r.Method == http.MethodDelete {
		var used int
		_ = s.store.db.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM runtimes WHERE profile_id=? AND workspace_id=?`, profileID, ws.ID).Scan(&used)
		if used > 0 {
			writeError(w, http.StatusConflict, errors.New("runtime profile is still used by a registered runtime"))
			return
		}
		_, _ = s.store.db.ExecContext(r.Context(), `DELETE FROM runtime_profiles WHERE id=? AND workspace_id=?`, profileID, ws.ID)
		s.hub.broadcast("daemon:runtime_profiles_changed", map[string]any{"workspace_id": ws.ID})
		w.WriteHeader(http.StatusNoContent)
		return
	}
	input := map[string]any{}
	if err := decodeBody(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	set, args := []string{"updated_at=?"}, []any{now()}
	for _, key := range []string{"display_name", "command_name", "description", "visibility", "enabled"} {
		if value, ok := input[key]; ok {
			set, args = append(set, key+"=?"), append(args, value)
		}
	}
	if value, ok := input["fixed_args"]; ok {
		raw, _ := json.Marshal(value)
		set, args = append(set, "fixed_args=?"), append(args, string(raw))
	}
	args = append(args, profileID, ws.ID)
	if _, err := s.store.db.ExecContext(r.Context(), `UPDATE runtime_profiles SET `+strings.Join(set, ",")+` WHERE id=? AND workspace_id=?`, args...); err != nil {
		s.fail(w, err)
		return
	}
	item, err := scanRuntimeProfile(s.store.db.QueryRowContext(r.Context(), `SELECT id, workspace_id, display_name, protocol_family, command_name, description, fixed_args, visibility, created_by, enabled, created_at, updated_at FROM runtime_profiles WHERE id=? AND workspace_id=?`, profileID, ws.ID))
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	s.hub.broadcast("daemon:runtime_profiles_changed", map[string]any{"workspace_id": ws.ID})
	writeJSON(w, http.StatusOK, item)
}

func boolDefault(value any, fallback bool) bool {
	if candidate, ok := value.(bool); ok {
		return candidate
	}
	return fallback
}

func (s *Server) runtimeItem(w http.ResponseWriter, r *http.Request) {
	ws, _ := s.workspaceFor(r)
	runtimeID := chi.URLParam(r, "id")
	if r.Method == http.MethodPatch {
		input := map[string]any{}
		if err := decodeBody(r, &input); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		name := strings.TrimSpace(stringFromAny(input["custom_name"]))
		if name != "" {
			if _, err := s.store.db.ExecContext(r.Context(), `UPDATE runtimes SET name=?, updated_at=? WHERE id=? AND workspace_id=?`, name, now(), runtimeID, ws.ID); err != nil {
				s.fail(w, err)
				return
			}
		}
		items, _ := s.store.listRuntimes(r.Context(), ws.ID)
		for _, item := range items {
			if item.ID == runtimeID {
				writeJSON(w, http.StatusOK, runtimePayload(item))
				return
			}
		}
		s.respondStoreError(w, sql.ErrNoRows)
		return
	}
	var activeAgents int
	_ = s.store.db.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM agents WHERE runtime_id=? AND workspace_id=? AND archived_at IS NULL`, runtimeID, ws.ID).Scan(&activeAgents)
	if activeAgents > 0 {
		rows, _ := s.store.db.QueryContext(r.Context(), `SELECT id, name FROM agents WHERE runtime_id=? AND workspace_id=? AND archived_at IS NULL ORDER BY name, id`, runtimeID, ws.ID)
		agents := []map[string]any{}
		if rows != nil {
			for rows.Next() {
				var id, name string
				_ = rows.Scan(&id, &name)
				agents = append(agents, map[string]any{"id": id, "name": name})
			}
			_ = rows.Close()
		}
		writeJSON(w, http.StatusConflict, map[string]any{"error": "runtime has active agents; unbind them before deleting", "code": "runtime_has_active_agents", "active_agents": agents})
		return
	}
	if _, err := s.store.db.ExecContext(r.Context(), `DELETE FROM runtimes WHERE id=? AND workspace_id=?`, runtimeID, ws.ID); err != nil {
		s.fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func runtimePayload(item localRuntime) map[string]any {
	metadata := maps.Clone(item.Metadata)
	if metadata == nil {
		metadata = map[string]any{}
	}
	if item.Version != nil {
		metadata["version"] = *item.Version
	}
	return map[string]any{"id": item.ID, "workspace_id": item.WorkspaceID, "daemon_id": item.DaemonID, "name": item.Name, "custom_name": nil, "runtime_mode": "local", "provider": item.Provider, "launch_header": "", "status": item.Status, "device_info": "", "metadata": metadata, "owner_id": LocalOperatorID, "visibility": "private", "profile_id": item.ProfileID, "last_seen_at": item.LastSeenAt, "created_at": item.CreatedAt, "updated_at": item.UpdatedAt}
}

func (s *Server) unbindAndDeleteRuntime(w http.ResponseWriter, r *http.Request) {
	ws, _ := s.workspaceFor(r)
	runtimeID := chi.URLParam(r, "id")
	var input struct {
		Expected []string `json:"expected_active_agent_ids"`
	}
	if err := decodeBody(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	rows, err := s.store.db.QueryContext(r.Context(), `SELECT id FROM agents WHERE runtime_id=? AND workspace_id=? AND archived_at IS NULL ORDER BY id`, runtimeID, ws.ID)
	if err != nil {
		s.fail(w, err)
		return
	}
	actual := []string{}
	for rows.Next() {
		var id string
		_ = rows.Scan(&id)
		actual = append(actual, id)
	}
	_ = rows.Close()
	if strings.Join(actual, "\x00") != strings.Join(input.Expected, "\x00") {
		writeError(w, http.StatusConflict, errors.New("runtime delete plan changed; review the active agents again"))
		return
	}
	tx, err := s.store.db.BeginTx(r.Context(), nil)
	if err != nil {
		s.fail(w, err)
		return
	}
	defer func() { _ = tx.Rollback() }()
	stamp := now()
	result, _ := tx.ExecContext(r.Context(), `UPDATE task_runs SET status='cancelled', completed_at=?, updated_at=? WHERE runtime=? AND status NOT IN ('completed','failed','cancelled')`, stamp, stamp, runtimeID)
	cancelled, _ := result.RowsAffected()
	_, _ = tx.ExecContext(r.Context(), `UPDATE chat_tasks SET status='cancelled', completed_at=?, updated_at=? WHERE runtime_id=? AND status NOT IN ('completed','failed','cancelled')`, stamp, stamp, runtimeID)
	if _, err := tx.ExecContext(r.Context(), `UPDATE agents SET runtime_id='', updated_at=? WHERE runtime_id=? AND workspace_id=?`, stamp, runtimeID, ws.ID); err != nil {
		s.fail(w, err)
		return
	}
	pausedResult, _ := tx.ExecContext(r.Context(), `UPDATE automations SET status='paused', updated_at=? WHERE workspace_id=? AND assignee_type='agent' AND assignee_id IN (`+placeholders(len(actual))+`)`, append([]any{stamp, ws.ID}, stringsToAny(actual)...)...)
	paused, _ := pausedResult.RowsAffected()
	if _, err := tx.ExecContext(r.Context(), `DELETE FROM runtime_requests WHERE runtime_id=?`, runtimeID); err != nil {
		s.fail(w, err)
		return
	}
	if _, err := tx.ExecContext(r.Context(), `DELETE FROM runtimes WHERE id=? AND workspace_id=?`, runtimeID, ws.ID); err != nil {
		s.fail(w, err)
		return
	}
	if err := tx.Commit(); err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "deleted", "agents_unbound": len(actual), "agents_archived": 0, "tasks_cancelled": cancelled, "autopilots_paused": paused})
}

func placeholders(count int) string {
	if count == 0 {
		return "NULL"
	}
	return strings.TrimRight(strings.Repeat("?,", count), ",")
}

func stringsToAny(values []string) []any {
	result := make([]any, len(values))
	for index, value := range values {
		result[index] = value
	}
	return result
}

func (s *Server) createRuntimeRequest(w http.ResponseWriter, r *http.Request) {
	ws, _ := s.workspaceFor(r)
	runtimeID := chi.URLParam(r, "id")
	var online int
	if err := s.store.db.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM runtimes WHERE id=? AND workspace_id=? AND status='online'`, runtimeID, ws.ID).Scan(&online); err != nil || online != 1 {
		writeError(w, http.StatusServiceUnavailable, errors.New("runtime is offline"))
		return
	}
	kind := "models"
	if strings.Contains(r.URL.Path, "/local-skills/import") {
		kind = "local_skill_import"
	} else if strings.Contains(r.URL.Path, "/local-skills") {
		kind = "local_skills"
	} else if strings.HasSuffix(r.URL.Path, "/update") {
		kind = "update"
	}
	payload := map[string]any{}
	if r.ContentLength != 0 {
		if err := decodeBody(r, &payload); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
	}
	id, stamp := uuid.NewString(), now()
	raw, _ := json.Marshal(payload)
	if _, err := s.store.db.ExecContext(r.Context(), `INSERT INTO runtime_requests(id, workspace_id, runtime_id, kind, status, payload, created_at, updated_at) VALUES (?, ?, ?, ?, 'pending', ?, ?, ?)`, id, ws.ID, runtimeID, kind, string(raw), stamp, stamp); err != nil {
		s.fail(w, err)
		return
	}
	s.hub.broadcast("daemon:pending_work", map[string]any{"runtime_id": runtimeID, "kind": kind})
	value, _ := s.readRuntimeRequest(r.Context(), ws.ID, runtimeID, id)
	writeJSON(w, http.StatusOK, value)
}

func (s *Server) getRuntimeRequest(w http.ResponseWriter, r *http.Request) {
	ws, _ := s.workspaceFor(r)
	value, err := s.readRuntimeRequest(r.Context(), ws.ID, chi.URLParam(r, "id"), chi.URLParam(r, "requestID"))
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, value)
}

func (s *Server) readRuntimeRequest(ctx context.Context, workspaceID, runtimeID, requestID string) (map[string]any, error) {
	var kind, status, payloadRaw, resultRaw, createdAt, updatedAt string
	var errorText sql.NullString
	if err := s.store.db.QueryRowContext(ctx, `SELECT kind, status, payload, result, error, created_at, updated_at FROM runtime_requests WHERE id=? AND runtime_id=? AND workspace_id=?`, requestID, runtimeID, workspaceID).Scan(&kind, &status, &payloadRaw, &resultRaw, &errorText, &createdAt, &updatedAt); err != nil {
		return nil, err
	}
	payload, result := map[string]any{}, map[string]any{}
	_ = json.Unmarshal([]byte(payloadRaw), &payload)
	_ = json.Unmarshal([]byte(resultRaw), &result)
	value := map[string]any{"id": requestID, "runtime_id": runtimeID, "status": status, "supported": true, "created_at": createdAt, "updated_at": updatedAt}
	mergeJSONMap(value, payload)
	mergeJSONMap(value, result)
	if errorText.Valid {
		value["error"] = errorText.String
	}
	if kind == "local_skills" {
		if _, ok := value["skills"]; !ok {
			value["skills"] = []any{}
		}
		value["mcp_supported"] = true
	}
	return value, nil
}

func (s *Server) reportRuntimeRequest(w http.ResponseWriter, r *http.Request) {
	runtimeID, requestID := chi.URLParam(r, "runtimeID"), chi.URLParam(r, "requestID")
	var body map[string]any
	if err := decodeBody(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	status := stringDefault(body["status"], "failed")
	if status != "completed" && status != "failed" {
		writeError(w, http.StatusBadRequest, errors.New("status must be completed or failed"))
		return
	}
	errorText := stringFromAny(body["error"])
	delete(body, "status")
	delete(body, "error")
	raw, _ := json.Marshal(body)
	tx, err := s.store.db.BeginTx(r.Context(), nil)
	if err != nil {
		s.fail(w, err)
		return
	}
	defer func() { _ = tx.Rollback() }()
	result, err := tx.ExecContext(r.Context(), `UPDATE runtime_requests SET status=?, result=?, error=?, updated_at=? WHERE id=? AND runtime_id=? AND status IN ('pending','running')`, status, string(raw), nullable(errorText), now(), requestID, runtimeID)
	if err != nil {
		s.fail(w, err)
		return
	}
	var notificationItem map[string]any
	notificationInserted := false
	if count, _ := result.RowsAffected(); count == 0 {
		var terminal int
		_ = tx.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM runtime_requests WHERE id=? AND runtime_id=? AND status IN ('completed','failed')`, requestID, runtimeID).Scan(&terminal)
		if terminal == 0 {
			s.respondStoreError(w, sql.ErrNoRows)
			return
		}
	} else if status == "failed" {
		var workspaceID, kind, runtimeName string
		if err := tx.QueryRowContext(r.Context(), `SELECT rr.workspace_id,rr.kind,rt.name FROM runtime_requests rr JOIN runtimes rt ON rt.id=rr.runtime_id WHERE rr.id=? AND rr.runtime_id=?`, requestID, runtimeID).Scan(&workspaceID, &kind, &runtimeName); err != nil {
			s.fail(w, err)
			return
		}
		notificationItem, notificationInserted, err = insertLocalNotificationTx(r.Context(), tx, localNotificationInput{
			WorkspaceID: workspaceID, Type: "runtime_failed", Severity: "action_required",
			SourceKey: "runtime-request:" + requestID + ":failed", Title: "Runtime request failed: " + runtimeName,
			Body: errorText, Details: map[string]string{"destination": "runtime", "runtime_id": runtimeID, "request_id": requestID, "request_kind": kind},
		})
		if err != nil {
			s.fail(w, err)
			return
		}
	}
	if err := tx.Commit(); err != nil {
		s.fail(w, err)
		return
	}
	if notificationInserted {
		s.hub.broadcast("inbox:new", map[string]any{"item": notificationItem})
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
}

func (s *Server) claimRuntimeRequests(ctx context.Context, workspaceID, runtimeID string, batchImports bool) (map[string]any, error) {
	tx, err := s.store.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	result := map[string]any{}
	for _, kind := range []string{"update", "models", "local_skills"} {
		var id, payloadRaw string
		err := tx.QueryRowContext(ctx, `UPDATE runtime_requests SET status='running', updated_at=? WHERE id=(SELECT id FROM runtime_requests WHERE workspace_id=? AND runtime_id=? AND kind=? AND status='pending' ORDER BY created_at, id LIMIT 1) RETURNING id, payload`, now(), workspaceID, runtimeID, kind).Scan(&id, &payloadRaw)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, err
		}
		payload := map[string]any{}
		_ = json.Unmarshal([]byte(payloadRaw), &payload)
		switch kind {
		case "update":
			result["pending_update"] = map[string]any{"id": id, "target_version": stringFromAny(payload["target_version"])}
		case "models":
			result["pending_model_list"] = map[string]any{"id": id}
		case "local_skills":
			result["pending_local_skills"] = map[string]any{"id": id}
		}
	}
	limit := 1
	if batchImports {
		limit = 16
	}
	rows, err := tx.QueryContext(ctx, `SELECT id, payload FROM runtime_requests WHERE workspace_id=? AND runtime_id=? AND kind='local_skill_import' AND status='pending' ORDER BY created_at, id LIMIT ?`, workspaceID, runtimeID, limit)
	if err != nil {
		return nil, err
	}
	imports := []map[string]any{}
	ids := []string{}
	for rows.Next() {
		var id, payloadRaw string
		if err := rows.Scan(&id, &payloadRaw); err != nil {
			_ = rows.Close()
			return nil, err
		}
		payload := map[string]any{}
		_ = json.Unmarshal([]byte(payloadRaw), &payload)
		imports = append(imports, map[string]any{"id": id, "skill_key": stringFromAny(payload["skill_key"])})
		ids = append(ids, id)
	}
	_ = rows.Close()
	for _, id := range ids {
		if _, err := tx.ExecContext(ctx, `UPDATE runtime_requests SET status='running', updated_at=? WHERE id=? AND status='pending'`, now(), id); err != nil {
			return nil, err
		}
	}
	if len(imports) > 0 {
		result["pending_local_skill_import"] = imports[0]
		if batchImports {
			result["pending_local_skill_imports"] = imports
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return result, nil
}
