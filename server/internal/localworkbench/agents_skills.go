package localworkbench

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

func ensureDefaultAgentTx(ctx context.Context, tx *sql.Tx, workspaceID, runtimeID string) error {
	stamp := now()
	value := map[string]any{
		"id": LocalAgentID, "workspace_id": workspaceID, "runtime_id": runtimeID,
		"name": "Codex", "description": "Local Codex runtime",
		"instructions": "Work on the assigned local task and report the result.",
		"avatar_url":   nil, "runtime_mode": "local", "runtime_config": map[string]any{},
		"custom_args": []any{}, "custom_env": map[string]any{}, "visibility": "private",
		"permission_mode": "private", "invocation_targets": []any{},
		"max_concurrent_tasks": 1, "model": "", "owner_id": LocalOperatorID,
		"archived_by": nil, "created_at": stamp, "updated_at": stamp,
	}
	raw, err := encodeJSONMap(value)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO agents(id, workspace_id, runtime_id, name, data, created_at, updated_at)
		VALUES (?, ?, ?, 'Codex', ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET runtime_id=excluded.runtime_id, updated_at=excluded.updated_at`,
		LocalAgentID, workspaceID, runtimeID, raw, stamp, stamp)
	return err
}

func decodeJSONMap(raw string) (map[string]any, error) {
	value := map[string]any{}
	if err := json.Unmarshal([]byte(raw), &value); err != nil {
		return nil, err
	}
	return value, nil
}

func encodeJSONMap(value map[string]any) (string, error) {
	raw, err := json.Marshal(value)
	return string(raw), err
}

func mergeJSONMap(current, updates map[string]any, blocked ...string) {
	denied := map[string]bool{}
	for _, key := range blocked {
		denied[key] = true
	}
	for key, value := range updates {
		if !denied[key] {
			current[key] = value
		}
	}
}

func (s *Store) readAgent(id, workspaceID string) (map[string]any, error) {
	var raw, runtimeID, createdAt, updatedAt string
	var archivedAt sql.NullString
	err := s.db.QueryRow(`SELECT data, runtime_id, archived_at, created_at, updated_at
		FROM agents WHERE id=? AND workspace_id=?`, id, workspaceID).
		Scan(&raw, &runtimeID, &archivedAt, &createdAt, &updatedAt)
	if err != nil {
		return nil, err
	}
	agent, err := decodeJSONMap(raw)
	if err != nil {
		return nil, err
	}
	agent["id"], agent["workspace_id"], agent["runtime_id"] = id, workspaceID, runtimeID
	agent["runtime_bound"] = runtimeID != ""
	agent["created_at"], agent["updated_at"] = createdAt, updatedAt
	agent["archived_at"] = nil
	if archivedAt.Valid {
		agent["archived_at"] = archivedAt.String
	}
	var runtimeStatus string
	if err := s.db.QueryRow(`SELECT status FROM runtimes WHERE id=? AND workspace_id=?`, runtimeID, workspaceID).Scan(&runtimeStatus); err != nil {
		agent["status"] = "offline"
	} else if runtimeStatus == "online" {
		agent["status"] = "idle"
	} else {
		agent["status"] = "offline"
	}
	skills, err := s.listAgentSkills(id, workspaceID)
	if err != nil {
		return nil, err
	}
	agent["skills"] = skills
	customEnv, _ := agent["custom_env"].(map[string]any)
	agent["custom_env_key_count"] = len(customEnv)
	agent["has_custom_env"] = len(customEnv) > 0
	delete(agent, "custom_env")
	if _, exists := agent["mcp_config"]; exists {
		delete(agent, "mcp_config")
		agent["mcp_config_redacted"] = true
	}
	return agent, nil
}

func (s *Store) listAgentSkills(agentID, workspaceID string) ([]map[string]any, error) {
	rows, err := s.db.Query(`SELECT sk.id, sk.name, sk.data, ask.enabled
		FROM agent_skills ask JOIN skills sk ON sk.id=ask.skill_id
		WHERE ask.agent_id=? AND sk.workspace_id=? ORDER BY sk.name, sk.id`, agentID, workspaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []map[string]any{}
	for rows.Next() {
		var id, name, raw string
		var enabled bool
		if err := rows.Scan(&id, &name, &raw, &enabled); err != nil {
			return nil, err
		}
		value, err := decodeJSONMap(raw)
		if err != nil {
			return nil, err
		}
		result = append(result, map[string]any{
			"id": id, "name": name, "description": stringFromAny(value["description"]), "enabled": enabled,
		})
	}
	return result, rows.Err()
}

func stringFromAny(value any) string {
	text, _ := value.(string)
	return text
}

func (s *Server) createAgent(w http.ResponseWriter, r *http.Request) {
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
	name, runtimeID := strings.TrimSpace(stringFromAny(input["name"])), strings.TrimSpace(stringFromAny(input["runtime_id"]))
	if name == "" || runtimeID == "" {
		writeError(w, http.StatusBadRequest, errors.New("name and runtime_id are required"))
		return
	}
	var runtimeExists int
	if err := s.store.db.QueryRowContext(r.Context(), `SELECT 1 FROM runtimes WHERE id=? AND workspace_id=?`, runtimeID, ws.ID).Scan(&runtimeExists); err != nil {
		s.respondStoreError(w, err)
		return
	}
	id, stamp := uuid.NewString(), now()
	defaults := map[string]any{
		"name": name, "description": "", "instructions": "", "avatar_url": nil,
		"runtime_mode": "local", "runtime_config": map[string]any{}, "custom_args": []any{},
		"custom_env": map[string]any{}, "visibility": "private", "permission_mode": "private",
		"invocation_targets": []any{}, "max_concurrent_tasks": 1, "model": "",
		"owner_id": LocalOperatorID, "archived_by": nil,
	}
	mergeJSONMap(defaults, input, "id", "workspace_id", "created_at", "updated_at", "archived_at", "status", "skills")
	raw, err := encodeJSONMap(defaults)
	if err != nil {
		s.fail(w, err)
		return
	}
	tx, err := s.store.db.BeginTx(r.Context(), nil)
	if err != nil {
		s.fail(w, err)
		return
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(r.Context(), `INSERT INTO agents(id, workspace_id, runtime_id, name, data, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`, id, ws.ID, runtimeID, name, raw, stamp, stamp); err != nil {
		s.fail(w, err)
		return
	}
	if ids, ok := input["skill_ids"].([]any); ok {
		for _, rawID := range ids {
			skillID := stringFromAny(rawID)
			if skillID == "" {
				continue
			}
			if _, err := tx.ExecContext(r.Context(), `INSERT INTO agent_skills(agent_id, skill_id, enabled, created_at)
				SELECT ?, id, 1, ? FROM skills WHERE id=? AND workspace_id=?`, id, stamp, skillID, ws.ID); err != nil {
				s.fail(w, err)
				return
			}
		}
	}
	if err := tx.Commit(); err != nil {
		s.fail(w, err)
		return
	}
	agent, err := s.store.readAgent(id, ws.ID)
	if err != nil {
		s.fail(w, err)
		return
	}
	s.hub.broadcast("agent:created", map[string]any{"agent": agent})
	writeJSON(w, http.StatusCreated, agent)
}

func (s *Server) listPersistentAgents(w http.ResponseWriter, r *http.Request) {
	ws, err := s.workspaceFor(r)
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	query := `SELECT id FROM agents WHERE workspace_id=? AND COALESCE(json_extract(data, '$.kind'),'user')!='system'`
	if r.URL.Query().Get("include_archived") != "true" {
		query += ` AND archived_at IS NULL`
	}
	query += ` ORDER BY updated_at DESC, id`
	rows, err := s.store.db.QueryContext(r.Context(), query, ws.ID)
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
	result := []map[string]any{}
	for _, id := range ids {
		agent, err := s.store.readAgent(id, ws.ID)
		if err != nil {
			s.fail(w, err)
			return
		}
		result = append(result, agent)
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) getPersistentAgent(w http.ResponseWriter, r *http.Request) {
	ws, err := s.workspaceFor(r)
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	agent, err := s.store.readAgent(chi.URLParam(r, "id"), ws.ID)
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, agent)
}

func (s *Server) updateAgent(w http.ResponseWriter, r *http.Request) {
	ws, err := s.workspaceFor(r)
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	id := chi.URLParam(r, "id")
	input := map[string]any{}
	if err := decodeBody(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if _, exists := input["custom_env"]; exists {
		writeError(w, http.StatusBadRequest, errors.New("custom_env must use the dedicated env endpoint"))
		return
	}
	var raw, runtimeID, name string
	if err := s.store.db.QueryRowContext(r.Context(), `SELECT data, runtime_id, name FROM agents WHERE id=? AND workspace_id=?`, id, ws.ID).Scan(&raw, &runtimeID, &name); err != nil {
		s.respondStoreError(w, err)
		return
	}
	value, err := decodeJSONMap(raw)
	if err != nil {
		s.fail(w, err)
		return
	}
	mergeJSONMap(value, input, "id", "workspace_id", "created_at", "updated_at", "archived_at", "custom_env", "skills")
	if candidate := strings.TrimSpace(stringFromAny(input["runtime_id"])); candidate != "" {
		if err := s.store.db.QueryRowContext(r.Context(), `SELECT id FROM runtimes WHERE id=? AND workspace_id=?`, candidate, ws.ID).Scan(&runtimeID); err != nil {
			s.respondStoreError(w, err)
			return
		}
	}
	if candidate := strings.TrimSpace(stringFromAny(input["name"])); candidate != "" {
		name = candidate
	}
	encoded, _ := encodeJSONMap(value)
	result, err := s.store.db.ExecContext(r.Context(), `UPDATE agents SET runtime_id=?, name=?, data=?, updated_at=? WHERE id=? AND workspace_id=?`, runtimeID, name, encoded, now(), id, ws.ID)
	if err != nil {
		s.fail(w, err)
		return
	}
	if count, _ := result.RowsAffected(); count != 1 {
		s.respondStoreError(w, sql.ErrNoRows)
		return
	}
	agent, _ := s.store.readAgent(id, ws.ID)
	s.hub.broadcast("agent:updated", map[string]any{"agent": agent})
	writeJSON(w, http.StatusOK, agent)
}

func (s *Server) setAgentArchived(w http.ResponseWriter, r *http.Request, archived bool) {
	ws, err := s.workspaceFor(r)
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	id, stamp := chi.URLParam(r, "id"), now()
	var archivedAt any
	if archived {
		archivedAt = stamp
	}
	result, err := s.store.db.ExecContext(r.Context(), `UPDATE agents SET archived_at=?, updated_at=? WHERE id=? AND workspace_id=?`, archivedAt, stamp, id, ws.ID)
	if err != nil {
		s.fail(w, err)
		return
	}
	if count, _ := result.RowsAffected(); count != 1 {
		s.respondStoreError(w, sql.ErrNoRows)
		return
	}
	agent, _ := s.store.readAgent(id, ws.ID)
	writeJSON(w, http.StatusOK, agent)
}

func (s *Server) archiveAgent(w http.ResponseWriter, r *http.Request) { s.setAgentArchived(w, r, true) }
func (s *Server) restoreAgent(w http.ResponseWriter, r *http.Request) {
	s.setAgentArchived(w, r, false)
}

func (s *Server) agentEnv(w http.ResponseWriter, r *http.Request) {
	ws, err := s.workspaceFor(r)
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	id := chi.URLParam(r, "id")
	var raw string
	if err := s.store.db.QueryRowContext(r.Context(), `SELECT data FROM agents WHERE id=? AND workspace_id=?`, id, ws.ID).Scan(&raw); err != nil {
		s.respondStoreError(w, err)
		return
	}
	value, err := decodeJSONMap(raw)
	if err != nil {
		s.fail(w, err)
		return
	}
	if r.Method == http.MethodPut {
		input := map[string]any{}
		if err := decodeBody(r, &input); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		current, _ := value["custom_env"].(map[string]any)
		next, _ := input["custom_env"].(map[string]any)
		for key, candidate := range next {
			if candidate == "****" && current != nil {
				next[key] = current[key]
			}
		}
		value["custom_env"] = next
		encoded, _ := encodeJSONMap(value)
		if _, err := s.store.db.ExecContext(r.Context(), `UPDATE agents SET data=?, updated_at=? WHERE id=? AND workspace_id=?`, encoded, now(), id, ws.ID); err != nil {
			s.fail(w, err)
			return
		}
	}
	env, _ := value["custom_env"].(map[string]any)
	if env == nil {
		env = map[string]any{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"agent_id": id, "custom_env": env})
}

func (s *Server) listSkills(w http.ResponseWriter, r *http.Request) {
	s.skillCollection(w, r, false)
}

func (s *Server) createSkill(w http.ResponseWriter, r *http.Request) {
	s.skillCollection(w, r, true)
}

func (s *Server) skillCollection(w http.ResponseWriter, r *http.Request, create bool) {
	ws, err := s.workspaceFor(r)
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	if !create {
		rows, err := s.store.db.QueryContext(r.Context(), `SELECT data FROM skills WHERE workspace_id=? ORDER BY name, id`, ws.ID)
		if err != nil {
			s.fail(w, err)
			return
		}
		result := []map[string]any{}
		for rows.Next() {
			var raw string
			if err := rows.Scan(&raw); err != nil {
				_ = rows.Close()
				s.fail(w, err)
				return
			}
			value, _ := decodeJSONMap(raw)
			delete(value, "content")
			delete(value, "files")
			result = append(result, value)
		}
		_ = rows.Close()
		writeJSON(w, http.StatusOK, result)
		return
	}
	input := map[string]any{}
	if err := decodeBody(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	name := strings.TrimSpace(stringFromAny(input["name"]))
	if name == "" {
		writeError(w, http.StatusBadRequest, errors.New("name is required"))
		return
	}
	id, stamp := uuid.NewString(), now()
	value := map[string]any{"id": id, "workspace_id": ws.ID, "name": name, "description": "", "content": "", "config": map[string]any{}, "files": []any{}, "created_by": LocalOperatorID, "created_at": stamp, "updated_at": stamp}
	mergeJSONMap(value, input, "id", "workspace_id", "created_by", "created_at", "updated_at")
	value["files"] = normalizeSkillFiles(id, value["files"], stamp)
	raw, _ := encodeJSONMap(value)
	if _, err := s.store.db.ExecContext(r.Context(), `INSERT INTO skills(id, workspace_id, name, data, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)`, id, ws.ID, name, raw, stamp, stamp); err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, value)
}

func (s *Server) skillItem(w http.ResponseWriter, r *http.Request) {
	ws, err := s.workspaceFor(r)
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	id := chi.URLParam(r, "id")
	var raw, name, createdAt string
	if err := s.store.db.QueryRowContext(r.Context(), `SELECT data, name, created_at FROM skills WHERE id=? AND workspace_id=?`, id, ws.ID).Scan(&raw, &name, &createdAt); err != nil {
		s.respondStoreError(w, err)
		return
	}
	if r.Method == http.MethodGet {
		value, _ := decodeJSONMap(raw)
		writeJSON(w, http.StatusOK, value)
		return
	}
	if r.Method == http.MethodDelete {
		tx, err := s.store.db.BeginTx(r.Context(), nil)
		if err != nil {
			s.fail(w, err)
			return
		}
		defer func() { _ = tx.Rollback() }()
		_, _ = tx.ExecContext(r.Context(), `DELETE FROM agent_skills WHERE skill_id=?`, id)
		_, err = tx.ExecContext(r.Context(), `DELETE FROM skills WHERE id=? AND workspace_id=?`, id, ws.ID)
		if err != nil {
			s.fail(w, err)
			return
		}
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
	value, _ := decodeJSONMap(raw)
	mergeJSONMap(value, input, "id", "workspace_id", "created_by", "created_at", "updated_at")
	if candidate := strings.TrimSpace(stringFromAny(input["name"])); candidate != "" {
		name = candidate
	}
	stamp := now()
	value["updated_at"] = stamp
	if _, supplied := input["files"]; supplied {
		value["files"] = normalizeSkillFiles(id, value["files"], stamp)
	}
	encoded, _ := encodeJSONMap(value)
	if _, err := s.store.db.ExecContext(r.Context(), `UPDATE skills SET name=?, data=?, updated_at=? WHERE id=? AND workspace_id=?`, name, encoded, stamp, id, ws.ID); err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, value)
}

func normalizeSkillFiles(skillID string, input any, stamp string) []any {
	rawFiles, _ := input.([]any)
	result := make([]any, 0, len(rawFiles))
	for _, raw := range rawFiles {
		file, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		filePath := path.Clean(strings.TrimSpace(stringFromAny(file["path"])))
		if filePath == "." || strings.HasPrefix(filePath, "../") || strings.HasPrefix(filePath, "/") {
			continue
		}
		createdAt := stringFromAny(file["created_at"])
		if createdAt == "" {
			createdAt = stamp
		}
		result = append(result, map[string]any{"id": firstNonEmpty(stringFromAny(file["id"]), uuid.NewString()), "skill_id": skillID, "path": filePath, "content": stringFromAny(file["content"]), "created_at": createdAt, "updated_at": stamp})
	}
	return result
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func (s *Server) importSkill(w http.ResponseWriter, r *http.Request) {
	input := struct {
		URL string `json:"url"`
	}{}
	if err := decodeBody(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	content, finalURL, err := fetchLocalSkillSource(r, input.URL)
	if err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}
	name := skillNameFromSource(finalURL, content)
	request := map[string]any{"name": name, "content": content, "config": map[string]any{"origin": map[string]any{"url": finalURL}}}
	requestBody, _ := json.Marshal(request)
	clone := r.Clone(r.Context())
	clone.Body = io.NopCloser(strings.NewReader(string(requestBody)))
	s.skillCollection(w, clone, true)
}

func (s *Server) refreshSkill(w http.ResponseWriter, r *http.Request) {
	ws, err := s.workspaceFor(r)
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	id := chi.URLParam(r, "id")
	var raw string
	if err := s.store.db.QueryRowContext(r.Context(), `SELECT data FROM skills WHERE id=? AND workspace_id=?`, id, ws.ID).Scan(&raw); err != nil {
		s.respondStoreError(w, err)
		return
	}
	value, err := decodeJSONMap(raw)
	if err != nil {
		s.fail(w, err)
		return
	}
	config, _ := value["config"].(map[string]any)
	origin, _ := config["origin"].(map[string]any)
	sourceURL := stringFromAny(origin["url"])
	if sourceURL == "" {
		writeError(w, http.StatusConflict, errors.New("skill has no refreshable origin URL"))
		return
	}
	content, finalURL, err := fetchLocalSkillSource(r, sourceURL)
	if err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}
	stamp := now()
	value["name"], value["content"], value["updated_at"] = skillNameFromSource(finalURL, content), content, stamp
	raw, _ = encodeJSONMap(value)
	if _, err := s.store.db.ExecContext(r.Context(), `UPDATE skills SET name=?, data=?, updated_at=? WHERE id=? AND workspace_id=?`, value["name"], raw, stamp, id, ws.ID); err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, value)
}

func fetchLocalSkillSource(r *http.Request, rawURL string) (string, string, error) {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return "", "", errors.New("skill URL must use http or https")
	}
	client := &http.Client{Timeout: 15 * time.Second}
	request, err := http.NewRequestWithContext(r.Context(), http.MethodGet, parsed.String(), nil)
	if err != nil {
		return "", "", err
	}
	response, err := client.Do(request)
	if err != nil {
		return "", "", fmt.Errorf("download skill: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return "", "", fmt.Errorf("download skill returned HTTP %d", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 1<<20+1))
	if err != nil {
		return "", "", err
	}
	if len(body) > 1<<20 {
		return "", "", errors.New("skill source exceeds 1 MiB")
	}
	return string(body), response.Request.URL.String(), nil
}

func skillNameFromSource(rawURL, content string) string {
	for _, line := range strings.Split(content, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "name:") {
			if name := strings.Trim(strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "name:")), `"'`); name != "" {
				return name
			}
		}
	}
	parsed, _ := url.Parse(rawURL)
	name := strings.TrimSuffix(path.Base(parsed.Path), path.Ext(parsed.Path))
	if name == "" || strings.EqualFold(name, "skill") {
		name = path.Base(path.Dir(parsed.Path))
	}
	if name == "" || name == "." || name == "/" {
		return "Imported Skill"
	}
	return name
}

func (s *Server) agentRuntimeSkills(w http.ResponseWriter, r *http.Request) {
	ws, err := s.workspaceFor(r)
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	id := chi.URLParam(r, "id")
	input := map[string]any{}
	if err := decodeBody(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	var raw string
	if err := s.store.db.QueryRowContext(r.Context(), `SELECT data FROM agents WHERE id=? AND workspace_id=?`, id, ws.ID).Scan(&raw); err != nil {
		s.respondStoreError(w, err)
		return
	}
	value, _ := decodeJSONMap(raw)
	value["runtime_skills"] = input
	encoded, _ := encodeJSONMap(value)
	if _, err := s.store.db.ExecContext(r.Context(), `UPDATE agents SET data=?, updated_at=? WHERE id=? AND workspace_id=?`, encoded, now(), id, ws.ID); err != nil {
		s.fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) agentSkills(w http.ResponseWriter, r *http.Request) {
	ws, err := s.workspaceFor(r)
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	agentID := chi.URLParam(r, "id")
	if _, err := s.store.readAgent(agentID, ws.ID); err != nil {
		s.respondStoreError(w, err)
		return
	}
	if r.Method == http.MethodGet {
		items, err := s.store.listAgentSkills(agentID, ws.ID)
		if err != nil {
			s.fail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, items)
		return
	}
	input := struct {
		SkillIDs []string `json:"skill_ids"`
	}{}
	if err := decodeBody(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	tx, err := s.store.db.BeginTx(r.Context(), nil)
	if err != nil {
		s.fail(w, err)
		return
	}
	defer func() { _ = tx.Rollback() }()
	if r.Method == http.MethodPut {
		if _, err := tx.ExecContext(r.Context(), `DELETE FROM agent_skills WHERE agent_id=?`, agentID); err != nil {
			s.fail(w, err)
			return
		}
	}
	for _, skillID := range input.SkillIDs {
		result, err := tx.ExecContext(r.Context(), `INSERT INTO agent_skills(agent_id, skill_id, enabled, created_at)
			SELECT ?, id, 1, ? FROM skills WHERE id=? AND workspace_id=?
			ON CONFLICT(agent_id, skill_id) DO UPDATE SET enabled=1`, agentID, now(), skillID, ws.ID)
		if err != nil {
			s.fail(w, err)
			return
		}
		if count, _ := result.RowsAffected(); count == 0 {
			writeError(w, http.StatusBadRequest, errors.New("skill not found"))
			return
		}
	}
	if err := tx.Commit(); err != nil {
		s.fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) agentSkillItem(w http.ResponseWriter, r *http.Request) {
	ws, err := s.workspaceFor(r)
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	agentID, skillID := chi.URLParam(r, "id"), chi.URLParam(r, "skillID")
	if r.Method == http.MethodDelete {
		_, err = s.store.db.ExecContext(r.Context(), `DELETE FROM agent_skills WHERE agent_id=? AND skill_id=? AND EXISTS(SELECT 1 FROM agents WHERE id=? AND workspace_id=?)`, agentID, skillID, agentID, ws.ID)
	} else {
		input := struct {
			Enabled bool `json:"enabled"`
		}{}
		if err = decodeBody(r, &input); err == nil {
			_, err = s.store.db.ExecContext(r.Context(), `UPDATE agent_skills SET enabled=? WHERE agent_id=? AND skill_id=? AND EXISTS(SELECT 1 FROM agents WHERE id=? AND workspace_id=?)`, input.Enabled, agentID, skillID, agentID, ws.ID)
		}
	}
	if err != nil {
		s.fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
