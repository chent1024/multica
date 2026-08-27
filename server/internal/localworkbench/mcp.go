package localworkbench

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

func mcpTransport(config map[string]any) string {
	if transport := strings.TrimSpace(stringFromAny(config["transport"])); transport != "" {
		return transport
	}
	if stringFromAny(config["command"]) != "" {
		return "stdio"
	}
	if stringFromAny(config["url"]) != "" {
		return "http"
	}
	return "unknown"
}

func scanMCP(row rowScanner, includeEnabled bool) (map[string]any, error) {
	var id, workspaceID, name, transport, createdAt, updatedAt string
	value := map[string]any{}
	if includeEnabled {
		var enabled bool
		if err := row.Scan(&id, &workspaceID, &name, &transport, &createdAt, &updatedAt, &enabled); err != nil {
			return nil, err
		}
		value["enabled"] = enabled
	} else if err := row.Scan(&id, &workspaceID, &name, &transport, &createdAt, &updatedAt); err != nil {
		return nil, err
	}
	value["id"], value["workspace_id"], value["name"], value["transport"] = id, workspaceID, name, transport
	value["created_at"], value["updated_at"] = createdAt, updatedAt
	return value, nil
}

func (s *Server) workspaceMCPServers(w http.ResponseWriter, r *http.Request) {
	ws, err := s.workspaceFor(r)
	if err != nil || (chi.URLParam(r, "id") != ws.ID && chi.URLParam(r, "id") != ws.Slug) {
		s.respondStoreError(w, sql.ErrNoRows)
		return
	}
	if r.Method == http.MethodPost {
		var input struct {
			Name   string         `json:"name"`
			Config map[string]any `json:"config"`
		}
		if err := decodeBody(r, &input); err != nil || strings.TrimSpace(input.Name) == "" || input.Config == nil {
			writeError(w, http.StatusBadRequest, errors.New("name and config are required"))
			return
		}
		raw, _ := json.Marshal(input.Config)
		id, stamp := uuid.NewString(), now()
		if _, err := s.store.db.ExecContext(r.Context(), `INSERT INTO mcp_servers(id, workspace_id, name, transport, config, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)`, id, ws.ID, strings.TrimSpace(input.Name), mcpTransport(input.Config), string(raw), stamp, stamp); err != nil {
			s.fail(w, err)
			return
		}
		item, _ := scanMCP(s.store.db.QueryRowContext(r.Context(), `SELECT id, workspace_id, name, transport, created_at, updated_at FROM mcp_servers WHERE id=?`, id), false)
		writeJSON(w, http.StatusCreated, item)
		return
	}
	rows, err := s.store.db.QueryContext(r.Context(), `SELECT id, workspace_id, name, transport, created_at, updated_at FROM mcp_servers WHERE workspace_id=? ORDER BY name, id`, ws.ID)
	if err != nil {
		s.fail(w, err)
		return
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		item, err := scanMCP(rows, false)
		if err != nil {
			s.fail(w, err)
			return
		}
		items = append(items, item)
	}
	writeJSON(w, http.StatusOK, items)
}

func (s *Server) workspaceMCPServerItem(w http.ResponseWriter, r *http.Request) {
	ws, _ := s.workspaceFor(r)
	id := chi.URLParam(r, "serverID")
	if r.Method == http.MethodDelete {
		tx, err := s.store.db.BeginTx(r.Context(), nil)
		if err != nil {
			s.fail(w, err)
			return
		}
		defer func() { _ = tx.Rollback() }()
		_, _ = tx.ExecContext(r.Context(), `DELETE FROM agent_mcp_servers WHERE server_id=?`, id)
		result, err := tx.ExecContext(r.Context(), `DELETE FROM mcp_servers WHERE id=? AND workspace_id=?`, id, ws.ID)
		if err != nil {
			s.fail(w, err)
			return
		}
		if count, _ := result.RowsAffected(); count != 1 {
			s.respondStoreError(w, sql.ErrNoRows)
			return
		}
		if err := tx.Commit(); err != nil {
			s.fail(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	var input struct {
		Name   *string        `json:"name"`
		Config map[string]any `json:"config"`
	}
	if err := decodeBody(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	set, args := []string{"updated_at=?"}, []any{now()}
	if input.Name != nil && strings.TrimSpace(*input.Name) != "" {
		set, args = append(set, "name=?"), append(args, strings.TrimSpace(*input.Name))
	}
	if input.Config != nil {
		raw, _ := json.Marshal(input.Config)
		set, args = append(set, "config=?", "transport=?"), append(args, string(raw), mcpTransport(input.Config))
	}
	args = append(args, id, ws.ID)
	if _, err := s.store.db.ExecContext(r.Context(), `UPDATE mcp_servers SET `+strings.Join(set, ",")+` WHERE id=? AND workspace_id=?`, args...); err != nil {
		s.fail(w, err)
		return
	}
	item, err := scanMCP(s.store.db.QueryRowContext(r.Context(), `SELECT id, workspace_id, name, transport, created_at, updated_at FROM mcp_servers WHERE id=? AND workspace_id=?`, id, ws.ID), false)
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func (s *Server) listAgentMCP(ctx context.Context, workspaceID, agentID string) ([]map[string]any, error) {
	rows, err := s.store.db.QueryContext(ctx, `SELECT m.id, m.workspace_id, m.name, m.transport, m.created_at, m.updated_at, am.enabled FROM agent_mcp_servers am JOIN mcp_servers m ON m.id=am.server_id JOIN agents a ON a.id=am.agent_id WHERE am.agent_id=? AND a.workspace_id=? AND m.workspace_id=? ORDER BY m.name,m.id`, agentID, workspaceID, workspaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		item, err := scanMCP(rows, true)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Server) agentMCPServers(w http.ResponseWriter, r *http.Request) {
	ws, _ := s.workspaceFor(r)
	agentID := chi.URLParam(r, "id")
	if _, err := s.store.readAgent(agentID, ws.ID); err != nil {
		s.respondStoreError(w, err)
		return
	}
	if r.Method == http.MethodPost {
		var input struct {
			ServerID string `json:"server_id"`
		}
		if err := decodeBody(r, &input); err != nil || input.ServerID == "" {
			writeError(w, http.StatusBadRequest, errors.New("server_id is required"))
			return
		}
		result, err := s.store.db.ExecContext(r.Context(), `INSERT INTO agent_mcp_servers(agent_id, server_id, enabled, created_at, updated_at) SELECT ?, id, 1, ?, ? FROM mcp_servers WHERE id=? AND workspace_id=? ON CONFLICT(agent_id,server_id) DO UPDATE SET enabled=1, updated_at=excluded.updated_at`, agentID, now(), now(), input.ServerID, ws.ID)
		if err != nil {
			s.fail(w, err)
			return
		}
		if count, _ := result.RowsAffected(); count == 0 {
			s.respondStoreError(w, sql.ErrNoRows)
			return
		}
	}
	items, err := s.listAgentMCP(r.Context(), ws.ID, agentID)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, items)
}

func (s *Server) agentMCPServerItem(w http.ResponseWriter, r *http.Request) {
	ws, _ := s.workspaceFor(r)
	agentID, serverID := chi.URLParam(r, "id"), chi.URLParam(r, "serverID")
	if r.Method == http.MethodDelete {
		_, _ = s.store.db.ExecContext(r.Context(), `DELETE FROM agent_mcp_servers WHERE agent_id=? AND server_id=? AND agent_id IN (SELECT id FROM agents WHERE workspace_id=?)`, agentID, serverID, ws.ID)
	} else {
		var input struct {
			Enabled bool `json:"enabled"`
		}
		if err := decodeBody(r, &input); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		if _, err := s.store.db.ExecContext(r.Context(), `UPDATE agent_mcp_servers SET enabled=?, updated_at=? WHERE agent_id=? AND server_id=? AND agent_id IN (SELECT id FROM agents WHERE workspace_id=?)`, input.Enabled, now(), agentID, serverID, ws.ID); err != nil {
			s.fail(w, err)
			return
		}
	}
	items, err := s.listAgentMCP(r.Context(), ws.ID, agentID)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, items)
}

func (s *Server) executionAgent(ctx context.Context, workspaceID, agentID string) (map[string]any, error) {
	agent, err := s.store.readAgent(agentID, workspaceID)
	if err != nil {
		return nil, err
	}
	var agentRaw string
	if err := s.store.db.QueryRowContext(ctx, `SELECT data FROM agents WHERE id=? AND workspace_id=?`, agentID, workspaceID).Scan(&agentRaw); err != nil {
		return nil, err
	}
	stored, err := decodeJSONMap(agentRaw)
	if err != nil {
		return nil, err
	}
	if env, ok := stored["custom_env"].(map[string]any); ok {
		agent["custom_env"] = env
	}
	if config, ok := stored["mcp_config"].(map[string]any); ok {
		agent["mcp_config"] = config
	}
	rows, err := s.store.db.QueryContext(ctx, `SELECT m.name, m.config FROM agent_mcp_servers am JOIN mcp_servers m ON m.id=am.server_id WHERE am.agent_id=? AND am.enabled=1 AND m.workspace_id=? ORDER BY m.name,m.id`, agentID, workspaceID)
	if err != nil {
		return nil, err
	}
	servers := map[string]any{}
	for rows.Next() {
		var name, raw string
		if err := rows.Scan(&name, &raw); err != nil {
			_ = rows.Close()
			return nil, err
		}
		var config any
		if err := json.Unmarshal([]byte(raw), &config); err != nil {
			_ = rows.Close()
			return nil, err
		}
		servers[name] = config
	}
	_ = rows.Close()
	if len(servers) > 0 {
		document, _ := agent["mcp_config"].(map[string]any)
		if document == nil {
			document = map[string]any{}
		}
		existing, _ := document["mcpServers"].(map[string]any)
		if existing == nil {
			existing = map[string]any{}
		}
		for name, config := range servers {
			existing[name] = config
		}
		document["mcpServers"] = existing
		agent["mcp_config"] = document
	}
	return agent, nil
}
