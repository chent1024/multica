package localworkbench

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
)

const statisticsTaskRefs = `SELECT tr.id task_id, i.workspace_id, i.project_id,
	CASE WHEN i.assignee_type='squad' THEN COALESCE((SELECT leader_id FROM squads WHERE id=i.assignee_id),i.assignee_id) ELSE COALESCE(i.assignee_id,'') END agent_id,
	tr.runtime runtime_id, tr.status, tr.error, tr.started_at, tr.completed_at, tr.created_at, tr.updated_at
	FROM task_runs tr JOIN issues i ON i.id=tr.issue_id
	UNION ALL
	SELECT ct.id, cs.workspace_id, cs.project_id, ct.agent_id, ct.runtime_id, ct.status, ct.error, ct.started_at, ct.completed_at, ct.created_at, ct.updated_at
	FROM chat_tasks ct JOIN chat_sessions cs ON cs.id=ct.chat_session_id`

func dashboardDays(r *http.Request) int {
	days, err := strconv.Atoi(r.URL.Query().Get("days"))
	if err != nil || days <= 0 {
		return 30
	}
	if days > 366 {
		return 366
	}
	return days
}

func projectFilter(r *http.Request, alias string) (string, []any) {
	projectID := strings.TrimSpace(r.URL.Query().Get("project_id"))
	if projectID == "" {
		return "", nil
	}
	return " AND " + alias + ".project_id=?", []any{projectID}
}

func (s *Server) dashboardUsageDaily(w http.ResponseWriter, r *http.Request) {
	ws, err := s.workspaceFor(r)
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	filter, projectArgs := projectFilter(r, "refs")
	args := []any{ws.ID, dashboardDays(r)}
	args = append(args, projectArgs...)
	rows, err := s.store.db.QueryContext(r.Context(), `WITH refs AS (`+statisticsTaskRefs+`) SELECT substr(u.updated_at,1,10), u.provider, u.model,
		SUM(u.input_tokens), SUM(u.output_tokens), SUM(u.cache_read_tokens), SUM(u.cache_write_tokens),
		SUM(COALESCE(u.cost_usd_ticks,0)), COUNT(DISTINCT u.task_id)
		FROM task_run_usage u JOIN refs ON refs.task_id=u.task_id
		WHERE refs.workspace_id=? AND datetime(u.updated_at)>=datetime('now', '-' || ? || ' days')`+filter+`
		GROUP BY substr(u.updated_at,1,10), u.provider, u.model ORDER BY 1,2,3`, args...)
	if err != nil {
		s.fail(w, err)
		return
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var date, provider, model string
		var input, output, cacheRead, cacheWrite, cost, tasks int64
		if err := rows.Scan(&date, &provider, &model, &input, &output, &cacheRead, &cacheWrite, &cost, &tasks); err != nil {
			s.fail(w, err)
			return
		}
		items = append(items, map[string]any{"date": date, "provider": provider, "model": model, "input_tokens": input, "output_tokens": output, "cache_read_tokens": cacheRead, "cache_write_tokens": cacheWrite, "cost_usd_ticks": cost, "task_count": tasks})
	}
	writeJSON(w, http.StatusOK, items)
}

func (s *Server) dashboardUsageByAgent(w http.ResponseWriter, r *http.Request) {
	ws, err := s.workspaceFor(r)
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	filter, projectArgs := projectFilter(r, "refs")
	args := []any{ws.ID, dashboardDays(r)}
	args = append(args, projectArgs...)
	rows, err := s.store.db.QueryContext(r.Context(), `WITH refs AS (`+statisticsTaskRefs+`) SELECT refs.agent_id, u.provider, u.model,
		SUM(u.input_tokens), SUM(u.output_tokens), SUM(u.cache_read_tokens), SUM(u.cache_write_tokens),
		SUM(COALESCE(u.cost_usd_ticks,0)), COUNT(DISTINCT u.task_id)
		FROM task_run_usage u JOIN refs ON refs.task_id=u.task_id
		WHERE refs.workspace_id=? AND datetime(u.updated_at)>=datetime('now', '-' || ? || ' days')`+filter+`
		GROUP BY refs.agent_id, u.provider, u.model ORDER BY 1,2,3`, args...)
	if err != nil {
		s.fail(w, err)
		return
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var agentID, provider, model string
		var input, output, cacheRead, cacheWrite, cost, tasks int64
		if err := rows.Scan(&agentID, &provider, &model, &input, &output, &cacheRead, &cacheWrite, &cost, &tasks); err != nil {
			s.fail(w, err)
			return
		}
		items = append(items, map[string]any{"agent_id": agentID, "provider": provider, "model": model, "input_tokens": input, "output_tokens": output, "cache_read_tokens": cacheRead, "cache_write_tokens": cacheWrite, "cost_usd_ticks": cost, "task_count": tasks})
	}
	writeJSON(w, http.StatusOK, items)
}

func (s *Server) dashboardAgentRuntime(w http.ResponseWriter, r *http.Request) {
	ws, err := s.workspaceFor(r)
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	filter, projectArgs := projectFilter(r, "refs")
	args := []any{ws.ID, dashboardDays(r)}
	args = append(args, projectArgs...)
	rows, err := s.store.db.QueryContext(r.Context(), `WITH refs AS (`+statisticsTaskRefs+`) SELECT refs.agent_id,
		CAST(SUM(MAX(0,(julianday(COALESCE(refs.completed_at,refs.updated_at))-julianday(COALESCE(refs.started_at,refs.created_at)))*86400)) AS INTEGER),
		COUNT(*), SUM(CASE WHEN refs.status='failed' THEN 1 ELSE 0 END), SUM(CASE WHEN refs.status='cancelled' THEN 1 ELSE 0 END)
		FROM refs WHERE refs.workspace_id=? AND refs.status IN ('completed','failed','cancelled')
		AND datetime(COALESCE(refs.completed_at,refs.updated_at))>=datetime('now', '-' || ? || ' days')`+filter+`
		GROUP BY refs.agent_id ORDER BY 2 DESC`, args...)
	if err != nil {
		s.fail(w, err)
		return
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var agentID string
		var seconds, tasks, failed, cancelled int64
		if err := rows.Scan(&agentID, &seconds, &tasks, &failed, &cancelled); err != nil {
			s.fail(w, err)
			return
		}
		items = append(items, map[string]any{"agent_id": agentID, "total_seconds": seconds, "task_count": tasks, "failed_count": failed, "cancelled_count": cancelled})
	}
	writeJSON(w, http.StatusOK, items)
}

func (s *Server) dashboardRuntimeDaily(w http.ResponseWriter, r *http.Request) {
	ws, err := s.workspaceFor(r)
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	filter, projectArgs := projectFilter(r, "refs")
	args := []any{ws.ID, dashboardDays(r)}
	args = append(args, projectArgs...)
	rows, err := s.store.db.QueryContext(r.Context(), `WITH refs AS (`+statisticsTaskRefs+`) SELECT substr(COALESCE(refs.completed_at,refs.updated_at),1,10),
		CAST(SUM(MAX(0,(julianday(COALESCE(refs.completed_at,refs.updated_at))-julianday(COALESCE(refs.started_at,refs.created_at)))*86400)) AS INTEGER),
		COUNT(*), SUM(CASE WHEN refs.status='failed' THEN 1 ELSE 0 END), SUM(CASE WHEN refs.status='cancelled' THEN 1 ELSE 0 END)
		FROM refs WHERE refs.workspace_id=? AND refs.status IN ('completed','failed','cancelled')
		AND datetime(COALESCE(refs.completed_at,refs.updated_at))>=datetime('now', '-' || ? || ' days')`+filter+`
		GROUP BY 1 ORDER BY 1`, args...)
	if err != nil {
		s.fail(w, err)
		return
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var date string
		var seconds, tasks, failed, cancelled int64
		if err := rows.Scan(&date, &seconds, &tasks, &failed, &cancelled); err != nil {
			s.fail(w, err)
			return
		}
		items = append(items, map[string]any{"date": date, "total_seconds": seconds, "task_count": tasks, "failed_count": failed, "cancelled_count": cancelled})
	}
	writeJSON(w, http.StatusOK, items)
}

func (s *Server) dashboardFailures(w http.ResponseWriter, r *http.Request, byAgent bool) {
	ws, err := s.workspaceFor(r)
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	filter, projectArgs := projectFilter(r, "refs")
	group, selectKey := "substr(COALESCE(refs.completed_at,refs.updated_at),1,10)", "date"
	if byAgent {
		group, selectKey = "refs.agent_id", "agent_id"
	}
	args := []any{ws.ID, dashboardDays(r)}
	args = append(args, projectArgs...)
	rows, err := s.store.db.QueryContext(r.Context(), `WITH refs AS (`+statisticsTaskRefs+`) SELECT `+group+`, CASE WHEN refs.status='completed' THEN '' WHEN trim(COALESCE(refs.error,''))='' THEN 'unclassified' ELSE 'execution_error' END, COUNT(*)
		FROM refs WHERE refs.workspace_id=? AND refs.status IN ('completed','failed','cancelled')
		AND datetime(COALESCE(refs.completed_at,refs.updated_at))>=datetime('now', '-' || ? || ' days')`+filter+`
		GROUP BY 1,2 ORDER BY 1,2`, args...)
	if err != nil {
		s.fail(w, err)
		return
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var key, reason string
		var count int64
		if err := rows.Scan(&key, &reason, &count); err != nil {
			s.fail(w, err)
			return
		}
		items = append(items, map[string]any{selectKey: key, "failure_reason": reason, "task_count": count})
	}
	writeJSON(w, http.StatusOK, items)
}

func (s *Server) dashboardFailuresDaily(w http.ResponseWriter, r *http.Request) {
	s.dashboardFailures(w, r, false)
}

func (s *Server) dashboardFailuresByAgent(w http.ResponseWriter, r *http.Request) {
	s.dashboardFailures(w, r, true)
}

func (s *Server) runtimeUsage(w http.ResponseWriter, r *http.Request) {
	ws, err := s.workspaceFor(r)
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	runtimeID := chi.URLParam(r, "id")
	var exists int
	if err := s.store.db.QueryRowContext(r.Context(), `SELECT 1 FROM runtimes WHERE id=? AND workspace_id=?`, runtimeID, ws.ID).Scan(&exists); err != nil {
		s.respondStoreError(w, err)
		return
	}
	rows, err := s.store.db.QueryContext(r.Context(), `WITH refs AS (`+statisticsTaskRefs+`) SELECT substr(u.updated_at,1,10), ?, u.provider, u.model, SUM(u.input_tokens), SUM(u.output_tokens), SUM(u.cache_read_tokens), SUM(u.cache_write_tokens), SUM(COALESCE(u.cost_usd_ticks,0)), COUNT(DISTINCT u.task_id)
		FROM task_run_usage u JOIN refs ON refs.task_id=u.task_id WHERE refs.runtime_id=? AND refs.workspace_id=? GROUP BY 1,3,4 ORDER BY 1`, runtimeID, runtimeID, ws.ID)
	if err != nil {
		s.fail(w, err)
		return
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var date, id, provider, model string
		var input, output, read, write, cost, tasks int64
		if err := rows.Scan(&date, &id, &provider, &model, &input, &output, &read, &write, &cost, &tasks); err != nil {
			s.fail(w, err)
			return
		}
		items = append(items, map[string]any{"date": date, "runtime_id": id, "provider": provider, "model": model, "input_tokens": input, "output_tokens": output, "cache_read_tokens": read, "cache_write_tokens": write, "cost_usd_ticks": cost, "task_count": tasks})
	}
	writeJSON(w, http.StatusOK, items)
}

func (s *Server) runtimeActivity(w http.ResponseWriter, r *http.Request) {
	ws, err := s.workspaceFor(r)
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	runtimeID := chi.URLParam(r, "id")
	rows, err := s.store.db.QueryContext(r.Context(), `SELECT CAST(strftime('%H', COALESCE(completed_at,updated_at)) AS INTEGER), COUNT(*) FROM (
		SELECT tr.completed_at, tr.updated_at FROM task_runs tr JOIN issues i ON i.id=tr.issue_id WHERE tr.runtime=? AND i.workspace_id=? AND tr.status IN ('completed','failed','cancelled')
		UNION ALL
		SELECT ct.completed_at, ct.updated_at FROM chat_tasks ct JOIN chat_sessions cs ON cs.id=ct.chat_session_id WHERE ct.runtime_id=? AND cs.workspace_id=? AND ct.status IN ('completed','failed','cancelled')
	) GROUP BY 1 ORDER BY 1`, runtimeID, ws.ID, runtimeID, ws.ID)
	if err != nil {
		s.fail(w, err)
		return
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var hour, count int
		if err := rows.Scan(&hour, &count); err != nil {
			s.fail(w, err)
			return
		}
		items = append(items, map[string]any{"hour": hour, "count": count})
	}
	writeJSON(w, http.StatusOK, items)
}

func (s *Server) runtimeUsageByAgent(w http.ResponseWriter, r *http.Request) {
	ws, err := s.workspaceFor(r)
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	runtimeID := chi.URLParam(r, "id")
	rows, err := s.store.db.QueryContext(r.Context(), `WITH refs AS (
		SELECT tr.id task_id, CASE WHEN i.assignee_type='squad' THEN COALESCE((SELECT leader_id FROM squads WHERE id=i.assignee_id),i.assignee_id) ELSE COALESCE(i.assignee_id,'') END agent_id
		FROM task_runs tr JOIN issues i ON i.id=tr.issue_id WHERE tr.runtime=? AND i.workspace_id=?
		UNION ALL SELECT ct.id, ct.agent_id FROM chat_tasks ct JOIN chat_sessions cs ON cs.id=ct.chat_session_id WHERE ct.runtime_id=? AND cs.workspace_id=?
	) SELECT refs.agent_id, u.provider, u.model, SUM(u.input_tokens), SUM(u.output_tokens), SUM(u.cache_read_tokens), SUM(u.cache_write_tokens), SUM(COALESCE(u.cost_usd_ticks,0)), COUNT(DISTINCT refs.task_id)
	FROM refs JOIN task_run_usage u ON u.task_id=refs.task_id GROUP BY refs.agent_id,u.provider,u.model ORDER BY 1,2,3`, runtimeID, ws.ID, runtimeID, ws.ID)
	if err != nil {
		s.fail(w, err)
		return
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var agentID, provider, model string
		var input, output, read, write, cost, tasks int64
		if err := rows.Scan(&agentID, &provider, &model, &input, &output, &read, &write, &cost, &tasks); err != nil {
			s.fail(w, err)
			return
		}
		items = append(items, map[string]any{"agent_id": agentID, "provider": provider, "model": model, "input_tokens": input, "output_tokens": output, "cache_read_tokens": read, "cache_write_tokens": write, "cost_usd_ticks": cost, "task_count": tasks})
	}
	writeJSON(w, http.StatusOK, items)
}

func (s *Server) runtimeUsageByHour(w http.ResponseWriter, r *http.Request) {
	ws, err := s.workspaceFor(r)
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	runtimeID := chi.URLParam(r, "id")
	rows, err := s.store.db.QueryContext(r.Context(), `WITH refs AS (
		SELECT tr.id task_id, COALESCE(tr.completed_at,tr.updated_at) at FROM task_runs tr JOIN issues i ON i.id=tr.issue_id WHERE tr.runtime=? AND i.workspace_id=?
		UNION ALL SELECT ct.id, COALESCE(ct.completed_at,ct.updated_at) FROM chat_tasks ct JOIN chat_sessions cs ON cs.id=ct.chat_session_id WHERE ct.runtime_id=? AND cs.workspace_id=?
	) SELECT CAST(strftime('%H',refs.at) AS INTEGER), u.model, SUM(u.input_tokens), SUM(u.output_tokens), SUM(u.cache_read_tokens), SUM(u.cache_write_tokens), SUM(COALESCE(u.cost_usd_ticks,0)), COUNT(DISTINCT refs.task_id)
	FROM refs JOIN task_run_usage u ON u.task_id=refs.task_id GROUP BY 1,u.model ORDER BY 1,u.model`, runtimeID, ws.ID, runtimeID, ws.ID)
	if err != nil {
		s.fail(w, err)
		return
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var hour int
		var model string
		var input, output, read, write, cost, tasks int64
		if err := rows.Scan(&hour, &model, &input, &output, &read, &write, &cost, &tasks); err != nil {
			s.fail(w, err)
			return
		}
		items = append(items, map[string]any{"hour": hour, "model": model, "input_tokens": input, "output_tokens": output, "cache_read_tokens": read, "cache_write_tokens": write, "cost_usd_ticks": cost, "task_count": tasks})
	}
	writeJSON(w, http.StatusOK, items)
}

func (s *Server) agentActivity30d(w http.ResponseWriter, r *http.Request) {
	ws, err := s.workspaceFor(r)
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	rows, err := s.store.db.QueryContext(r.Context(), `WITH refs AS (`+statisticsTaskRefs+`) SELECT refs.agent_id, substr(refs.completed_at,1,10), COUNT(*) FROM refs WHERE refs.workspace_id=? AND refs.status='completed' AND datetime(refs.completed_at)>=datetime('now','-30 days') GROUP BY 1,2 ORDER BY 1,2`, ws.ID)
	if err != nil {
		s.fail(w, err)
		return
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var agentID, date string
		var count int64
		if err := rows.Scan(&agentID, &date, &count); err != nil {
			s.fail(w, err)
			return
		}
		items = append(items, map[string]any{"agent_id": agentID, "date": date, "task_count": count})
	}
	writeJSON(w, http.StatusOK, items)
}

func (s *Server) agentRunCounts(w http.ResponseWriter, r *http.Request) {
	ws, err := s.workspaceFor(r)
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	rows, err := s.store.db.QueryContext(r.Context(), `WITH refs AS (`+statisticsTaskRefs+`) SELECT refs.agent_id, COUNT(*) FROM refs WHERE refs.workspace_id=? AND datetime(COALESCE(refs.completed_at,refs.updated_at))>=datetime('now','-30 days') GROUP BY 1`, ws.ID)
	if err != nil {
		s.fail(w, err)
		return
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var agentID string
		var count int64
		if err := rows.Scan(&agentID, &count); err != nil {
			s.fail(w, err)
			return
		}
		items = append(items, map[string]any{"agent_id": agentID, "run_count": count})
	}
	writeJSON(w, http.StatusOK, items)
}
