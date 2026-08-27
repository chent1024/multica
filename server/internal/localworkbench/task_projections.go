package localworkbench

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
)

func scanProjectedTask(row rowScanner) (map[string]any, error) {
	var id, agentID, runtimeID, issueID, status, createdAt string
	var chatSessionID, summary, errorText, startedAt, completedAt sql.NullString
	err := row.Scan(&id, &agentID, &runtimeID, &issueID, &chatSessionID, &status, &summary, &errorText, &startedAt, &completedAt, &createdAt)
	value := map[string]any{"id": id, "agent_id": agentID, "runtime_id": runtimeID, "issue_id": issueID, "status": status, "priority": 0, "dispatched_at": nil, "started_at": nullableString(startedAt), "completed_at": nullableString(completedAt), "result": nil, "error": nullableString(errorText), "created_at": createdAt}
	if summary.Valid {
		value["result"] = map[string]any{"summary": summary.String}
	}
	if chatSessionID.Valid {
		value["chat_session_id"] = chatSessionID.String
	}
	return value, err
}

const projectedTasksSQL = `SELECT id, agent_id, runtime_id, issue_id, chat_session_id, status, summary, error, started_at, completed_at, created_at FROM (
	SELECT tr.id,
		COALESCE(tr.agent_id,CASE WHEN i.assignee_type='squad' THEN COALESCE((SELECT leader_id FROM squads WHERE id=i.assignee_id),COALESCE(i.assignee_id,'')) ELSE COALESCE(i.assignee_id,'') END) agent_id,
		COALESCE(tr.runtime,'') runtime_id, tr.issue_id issue_id, NULL chat_session_id, tr.status, tr.summary, tr.error, tr.started_at, tr.completed_at, tr.created_at,
		i.workspace_id workspace_id, 'issue' source
	FROM task_runs tr JOIN issues i ON i.id=tr.issue_id
	UNION ALL
	SELECT ct.id, ct.agent_id, ct.runtime_id, '' issue_id, ct.chat_session_id, ct.status, ct.summary, ct.error, ct.started_at, ct.completed_at, ct.created_at,
		cs.workspace_id, 'chat' source
	FROM chat_tasks ct JOIN chat_sessions cs ON cs.id=ct.chat_session_id
) WHERE workspace_id=?`

func (s *Server) projectedTasks(w http.ResponseWriter, r *http.Request) {
	ws, err := s.workspaceFor(r)
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	query, args := projectedTasksSQL, []any{ws.ID}
	if agentID := chi.URLParam(r, "id"); strings.HasPrefix(r.URL.Path, "/api/agents/") && agentID != "" {
		query += ` AND agent_id=?`
		args = append(args, agentID)
	}
	query += ` ORDER BY created_at DESC, id DESC LIMIT 500`
	rows, err := s.store.db.QueryContext(r.Context(), query, args...)
	if err != nil {
		s.fail(w, err)
		return
	}
	defer rows.Close()
	items := []map[string]any{}
	snapshot := strings.HasSuffix(r.URL.Path, "/agent-task-snapshot")
	terminalSeen := map[string]bool{}
	for rows.Next() {
		item, err := scanProjectedTask(rows)
		if err != nil {
			s.fail(w, err)
			return
		}
		if snapshot {
			status := stringFromAny(item["status"])
			if status == "completed" || status == "failed" || status == "cancelled" {
				agentID := stringFromAny(item["agent_id"])
				if terminalSeen[agentID] {
					continue
				}
				terminalSeen[agentID] = true
			}
		}
		items = append(items, item)
	}
	writeJSON(w, http.StatusOK, items)
}

func (s *Server) workingAgents(w http.ResponseWriter, r *http.Request) {
	ws, err := s.workspaceFor(r)
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	kind := r.URL.Query().Get("type")
	query := `WITH active AS (
		SELECT COALESCE(tr.agent_id,CASE WHEN i.assignee_type='squad' THEN COALESCE((SELECT leader_id FROM squads WHERE id=i.assignee_id),i.assignee_id) ELSE i.assignee_id END) agent_id, i.id issue_id, 'issue' source
		FROM task_runs tr JOIN issues i ON i.id=tr.issue_id WHERE i.workspace_id=? AND tr.status IN ('dispatched','waiting_local_directory','running')
		UNION ALL SELECT ct.agent_id, NULL, 'chat' FROM chat_tasks ct JOIN chat_sessions cs ON cs.id=ct.chat_session_id WHERE cs.workspace_id=? AND ct.status IN ('dispatched','waiting_local_directory','running')
	) SELECT a.id, a.name, json_extract(a.data,'$.avatar_url'), COUNT(*), COALESCE(json_group_array(DISTINCT active.issue_id) FILTER (WHERE active.issue_id IS NOT NULL),'[]') FROM active JOIN agents a ON a.id=active.agent_id WHERE a.workspace_id=?`
	args := []any{ws.ID, ws.ID, ws.ID}
	if kind == "issue" || kind == "chat" {
		query += ` AND active.source=?`
		args = append(args, kind)
	}
	if parent := strings.TrimSpace(r.URL.Query().Get("parent")); parent != "" {
		query += ` AND active.issue_id IN (SELECT id FROM issues WHERE parent_issue_id=?)`
		args = append(args, parent)
	}
	query += ` GROUP BY a.id,a.name,a.data ORDER BY a.name,a.id`
	rows, err := s.store.db.QueryContext(r.Context(), query, args...)
	if err != nil {
		s.fail(w, err)
		return
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var id, name, issueIDsRaw string
		var avatar sql.NullString
		var count int
		if err := rows.Scan(&id, &name, &avatar, &count, &issueIDsRaw); err != nil {
			s.fail(w, err)
			return
		}
		issueIDs := []string{}
		_ = json.Unmarshal([]byte(issueIDsRaw), &issueIDs)
		items = append(items, map[string]any{"id": id, "name": name, "avatar_url": nullableString(avatar), "running_task_count": count, "issue_ids": issueIDs})
	}
	writeJSON(w, http.StatusOK, items)
}

func (s *Server) cancelAgentTasks(w http.ResponseWriter, r *http.Request) {
	ws, _ := s.workspaceFor(r)
	agentID, stamp := chi.URLParam(r, "id"), now()
	tx, err := s.store.db.BeginTx(r.Context(), nil)
	if err != nil {
		s.fail(w, err)
		return
	}
	defer func() { _ = tx.Rollback() }()
	result, err := tx.ExecContext(r.Context(), `UPDATE task_runs SET status='cancelled', completed_at=?, updated_at=? WHERE status NOT IN ('completed','failed','cancelled') AND issue_id IN (SELECT i.id FROM issues i WHERE i.workspace_id=? AND ((i.assignee_type='agent' AND i.assignee_id=?) OR (i.assignee_type='squad' AND i.assignee_id IN (SELECT id FROM squads WHERE leader_id=?))))`, stamp, stamp, ws.ID, agentID, agentID)
	if err != nil {
		s.fail(w, err)
		return
	}
	count, _ := result.RowsAffected()
	result, err = tx.ExecContext(r.Context(), `UPDATE chat_tasks SET status='cancelled', completed_at=?, updated_at=? WHERE agent_id=? AND status NOT IN ('completed','failed','cancelled') AND chat_session_id IN (SELECT id FROM chat_sessions WHERE workspace_id=?)`, stamp, stamp, agentID, ws.ID)
	if err != nil {
		s.fail(w, err)
		return
	}
	chatCount, _ := result.RowsAffected()
	if err := tx.Commit(); err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"cancelled": count + chatCount})
}
