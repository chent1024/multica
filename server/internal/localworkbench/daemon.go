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

type localRuntime struct {
	ID          string  `json:"id"`
	WorkspaceID string  `json:"workspace_id"`
	DaemonID    string  `json:"daemon_id"`
	Name        string  `json:"name"`
	Provider    string  `json:"provider"`
	Version     *string `json:"version,omitempty"`
	Status      string  `json:"status"`
	DeviceName  *string `json:"device_name,omitempty"`
	ProfileID   *string `json:"profile_id,omitempty"`
	LastSeenAt  *string `json:"last_seen_at"`
	CreatedAt   string  `json:"created_at"`
	UpdatedAt   string  `json:"updated_at"`
}

func scanRuntime(row rowScanner) (localRuntime, error) {
	var runtime localRuntime
	var version, deviceName, profileID, lastSeen sql.NullString
	err := row.Scan(&runtime.ID, &runtime.WorkspaceID, &runtime.DaemonID, &runtime.Name, &runtime.Provider, &version, &runtime.Status, &deviceName, &profileID, &lastSeen, &runtime.CreatedAt, &runtime.UpdatedAt)
	runtime.Version, runtime.DeviceName, runtime.ProfileID, runtime.LastSeenAt = stringPtr(version), stringPtr(deviceName), stringPtr(profileID), stringPtr(lastSeen)
	return runtime, err
}

func (s *Store) listRuntimes(ctx context.Context, workspaceID string) ([]localRuntime, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, workspace_id, daemon_id, name, provider, version, status, device_name, profile_id, last_seen_at, created_at, updated_at FROM runtimes WHERE workspace_id=? ORDER BY provider, id`, workspaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []localRuntime{}
	for rows.Next() {
		item, err := scanRuntime(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) codexRuntime(ctx context.Context, workspaceID string) (localRuntime, error) {
	return scanRuntime(s.db.QueryRowContext(ctx, `SELECT id, workspace_id, daemon_id, name, provider, version, status, device_name, profile_id, last_seen_at, created_at, updated_at FROM runtimes WHERE workspace_id=? AND provider='codex' AND status='online' ORDER BY updated_at DESC LIMIT 1`, workspaceID))
}

func localAgent(workspaceID string, runtime localRuntime) map[string]any {
	return map[string]any{
		"id": LocalAgentID, "workspace_id": workspaceID, "runtime_id": runtime.ID, "runtime_bound": true,
		"name": "Codex", "description": "Local Codex runtime", "instructions": "Work on the assigned local task and report the result.",
		"avatar_url": nil, "runtime_mode": "local", "runtime_config": map[string]any{}, "custom_args": []any{},
		"visibility": "private", "permission_mode": "private", "invocation_targets": []any{}, "status": "idle",
		"max_concurrent_tasks": 1, "model": "", "owner_id": nil, "skills": []any{}, "created_at": runtime.CreatedAt,
		"updated_at": runtime.UpdatedAt, "archived_at": nil, "archived_by": nil,
	}
}

func (s *Store) enqueueAssignedIssue(ctx context.Context, workspaceID, issueID string) error {
	issue, err := s.GetIssue(ctx, workspaceID, issueID)
	if err != nil {
		return err
	}
	if issue.AssigneeType == nil || issue.AssigneeID == nil || *issue.AssigneeType != "agent" || *issue.AssigneeID != LocalAgentID || issue.Status == "done" || issue.Status == "cancelled" {
		return nil
	}
	runtime, err := s.codexRuntime(ctx, workspaceID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	var active int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM task_runs WHERE issue_id=? AND status IN ('queued','dispatched','waiting_local_directory','running')`, issueID).Scan(&active); err != nil {
		return err
	}
	if active > 0 {
		return nil
	}
	stamp := now()
	_, err = s.db.ExecContext(ctx, `INSERT INTO task_runs(id, issue_id, status, runtime, created_at, updated_at) VALUES (?, ?, 'queued', ?, ?, ?)`, uuid.NewString(), issueID, runtime.ID, stamp, stamp)
	return err
}

func (s *Store) enqueuePendingAssignments(ctx context.Context, workspaceID string) error {
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM issues WHERE workspace_id=? AND assignee_type='agent' AND assignee_id=? AND status NOT IN ('done','cancelled')`, workspaceID, LocalAgentID)
	if err != nil {
		return err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, id := range ids {
		if err := s.enqueueAssignedIssue(ctx, workspaceID, id); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) listRuntimes(w http.ResponseWriter, r *http.Request) {
	ws, err := s.workspaceFor(r)
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	items, err := s.store.listRuntimes(r.Context(), ws.ID)
	if err != nil {
		s.fail(w, err)
		return
	}
	result := make([]map[string]any, 0, len(items))
	for _, item := range items {
		result = append(result, map[string]any{
			"id": item.ID, "workspace_id": item.WorkspaceID, "daemon_id": item.DaemonID, "name": item.Name,
			"runtime_mode": "local", "provider": item.Provider, "launch_header": "", "status": item.Status,
			"device_info": "", "metadata": map[string]any{"version": item.Version}, "owner_id": nil, "visibility": "private",
			"profile_id": item.ProfileID, "last_seen_at": item.LastSeenAt, "created_at": item.CreatedAt, "updated_at": item.UpdatedAt,
		})
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) listAgents(w http.ResponseWriter, r *http.Request) {
	ws, err := s.workspaceFor(r)
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	runtime, err := s.store.codexRuntime(r.Context(), ws.ID)
	if errors.Is(err, sql.ErrNoRows) {
		writeJSON(w, http.StatusOK, []any{})
		return
	}
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, []any{localAgent(ws.ID, runtime)})
}

func (s *Server) daemonRegister(w http.ResponseWriter, r *http.Request) {
	ws, err := s.workspaceFor(r)
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	var input struct {
		WorkspaceID string `json:"workspace_id"`
		DaemonID    string `json:"daemon_id"`
		DeviceName  string `json:"device_name"`
		Runtimes    []struct {
			Name      string `json:"name"`
			Type      string `json:"type"`
			Version   string `json:"version"`
			Status    string `json:"status"`
			ProfileID string `json:"profile_id"`
		} `json:"runtimes"`
	}
	if err := decodeBody(r, &input); err != nil || input.WorkspaceID != ws.ID || strings.TrimSpace(input.DaemonID) == "" {
		writeError(w, http.StatusBadRequest, errors.New("invalid local daemon registration"))
		return
	}
	tx, err := s.store.db.BeginTx(r.Context(), nil)
	if err != nil {
		s.fail(w, err)
		return
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(r.Context(), `UPDATE runtimes SET status='offline', updated_at=? WHERE workspace_id=? AND daemon_id=?`, now(), ws.ID, input.DaemonID); err != nil {
		s.fail(w, err)
		return
	}
	stamp := now()
	for _, candidate := range input.Runtimes {
		provider := strings.TrimSpace(candidate.Type)
		if provider == "" {
			continue
		}
		name := strings.TrimSpace(candidate.Name)
		if name == "" {
			name = provider
		}
		profileKey := strings.TrimSpace(candidate.ProfileID)
		var id string
		err := tx.QueryRowContext(r.Context(), `SELECT id FROM runtimes WHERE workspace_id=? AND daemon_id=? AND provider=? AND COALESCE(profile_id,'')=? LIMIT 1`, ws.ID, input.DaemonID, provider, profileKey).Scan(&id)
		if errors.Is(err, sql.ErrNoRows) {
			id = uuid.NewString()
			_, err = tx.ExecContext(r.Context(), `INSERT INTO runtimes(id, workspace_id, daemon_id, name, provider, version, status, device_name, profile_id, last_seen_at, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, 'online', ?, ?, ?, ?, ?)`, id, ws.ID, input.DaemonID, name, provider, nullable(candidate.Version), nullable(input.DeviceName), nullable(profileKey), stamp, stamp, stamp)
		} else if err == nil {
			_, err = tx.ExecContext(r.Context(), `UPDATE runtimes SET name=?, version=?, status='online', device_name=?, last_seen_at=?, updated_at=? WHERE id=?`, name, nullable(candidate.Version), nullable(input.DeviceName), stamp, stamp, id)
		}
		if err != nil {
			s.fail(w, err)
			return
		}
	}
	if err := tx.Commit(); err != nil {
		s.fail(w, err)
		return
	}
	if err := s.store.enqueuePendingAssignments(r.Context(), ws.ID); err != nil {
		s.fail(w, err)
		return
	}
	items, err := s.store.listRuntimes(r.Context(), ws.ID)
	if err != nil {
		s.fail(w, err)
		return
	}
	runtimes := []map[string]any{}
	for _, item := range items {
		if item.DaemonID == input.DaemonID && item.Status == "online" {
			runtimes = append(runtimes, map[string]any{"id": item.ID, "name": item.Name, "provider": item.Provider, "status": item.Status, "profile_id": item.ProfileID})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"runtimes": runtimes, "repos": []any{}, "repos_version": "local"})
}

func (s *Server) daemonDeregister(w http.ResponseWriter, r *http.Request) {
	var input struct {
		RuntimeIDs []string `json:"runtime_ids"`
	}
	if err := decodeBody(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	ws, err := s.workspaceFor(r)
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	for _, id := range input.RuntimeIDs {
		if _, err := s.store.db.ExecContext(r.Context(), `UPDATE runtimes SET status='offline', updated_at=? WHERE workspace_id=? AND id=?`, now(), ws.ID, id); err != nil {
			s.fail(w, err)
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{})
}

func (s *Server) daemonHeartbeat(w http.ResponseWriter, r *http.Request) {
	var input struct {
		RuntimeID string `json:"runtime_id"`
	}
	if err := decodeBody(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	ws, err := s.workspaceFor(r)
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	stamp := now()
	result, err := s.store.db.ExecContext(r.Context(), `UPDATE runtimes SET status='online', last_seen_at=?, updated_at=? WHERE workspace_id=? AND id=?`, stamp, stamp, ws.ID, input.RuntimeID)
	if err != nil {
		s.fail(w, err)
		return
	}
	if count, _ := result.RowsAffected(); count == 0 {
		writeError(w, http.StatusNotFound, errors.New("runtime not found"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{})
}

func (s *Server) daemonClaimTasks(w http.ResponseWriter, r *http.Request) {
	var input struct {
		DaemonID   string   `json:"daemon_id"`
		RuntimeIDs []string `json:"runtime_ids"`
		MaxTasks   int      `json:"max_tasks"`
	}
	if err := decodeBody(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if input.MaxTasks <= 0 {
		input.MaxTasks = 1
	}
	tasks := []any{}
	for _, runtimeID := range input.RuntimeIDs {
		if len(tasks) >= input.MaxTasks {
			break
		}
		task, err := s.claimTask(r.Context(), input.DaemonID, runtimeID)
		if err != nil {
			s.respondStoreError(w, err)
			return
		}
		if task != nil {
			tasks = append(tasks, task)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"tasks": tasks})
}

func (s *Server) daemonClaimTask(w http.ResponseWriter, r *http.Request) {
	var daemonID string
	if err := s.store.db.QueryRowContext(r.Context(), `SELECT daemon_id FROM runtimes WHERE id=?`, chi.URLParam(r, "runtimeID")).Scan(&daemonID); err != nil {
		s.respondStoreError(w, err)
		return
	}
	task, err := s.claimTask(r.Context(), daemonID, chi.URLParam(r, "runtimeID"))
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"task": task})
}

func (s *Server) claimTask(ctx context.Context, daemonID, runtimeID string) (map[string]any, error) {
	workspaces, err := s.store.ListWorkspaces(ctx)
	if err != nil {
		return nil, err
	}
	if len(workspaces) != 1 {
		return nil, sql.ErrNoRows
	}
	localWorkspaceID := workspaces[0].ID
	var workspaceID, provider string
	if err := s.store.db.QueryRowContext(ctx, `SELECT workspace_id, provider FROM runtimes WHERE workspace_id=? AND id=? AND daemon_id=? AND status='online'`, localWorkspaceID, runtimeID, daemonID).Scan(&workspaceID, &provider); err != nil {
		return nil, err
	}
	tx, err := s.store.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	stamp := now()
	var taskID, issueID string
	err = tx.QueryRowContext(ctx, `UPDATE task_runs SET status='dispatched', updated_at=? WHERE id=(SELECT tr.id FROM task_runs tr JOIN issues i ON i.id=tr.issue_id WHERE tr.status='queued' AND tr.runtime=? AND i.workspace_id=? ORDER BY tr.created_at LIMIT 1) RETURNING id, issue_id`, stamp, runtimeID, workspaceID).Scan(&taskID, &issueID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, tx.Commit()
	}
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	issue, err := s.store.GetIssue(ctx, workspaceID, issueID)
	if err != nil {
		return nil, err
	}
	ws, err := s.store.GetWorkspace(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	task := map[string]any{
		"id": taskID, "agent_id": LocalAgentID, "runtime_id": runtimeID, "issue_id": issue.ID, "workspace_id": workspaceID,
		"thread_name": issue.Identifier + " " + issue.Title, "workspace_context": stringValue(ws.Context),
		"agent": map[string]any{"id": LocalAgentID, "name": "Codex", "instructions": "Work on the assigned local task and report the result."},
	}
	if issue.ProjectID != nil {
		project, err := s.store.GetProject(ctx, workspaceID, *issue.ProjectID)
		if err != nil {
			return nil, err
		}
		task["project_id"], task["project_title"] = project.ID, project.Title
		task["project_description"] = stringValue(project.Description)
		rows, err := s.store.db.QueryContext(ctx, `SELECT id, resource_type, resource_ref, COALESCE(label,'') FROM project_resources WHERE workspace_id=? AND project_id=? ORDER BY position`, workspaceID, project.ID)
		if err != nil {
			return nil, err
		}
		resources := []map[string]any{}
		for rows.Next() {
			var id, kind, raw, label string
			if err := rows.Scan(&id, &kind, &raw, &label); err != nil {
				_ = rows.Close()
				return nil, err
			}
			resources = append(resources, map[string]any{"id": id, "resource_type": kind, "resource_ref": json.RawMessage(raw), "label": label})
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
		task["project_resources"] = resources
	}
	_ = provider
	return task, nil
}

func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func (s *Server) daemonTaskState(w http.ResponseWriter, r *http.Request) {
	ws, err := s.workspaceFor(r)
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	action := chi.URLParam(r, "action")
	status := map[string]string{"start": "running", "complete": "completed", "fail": "failed", "wait-local-directory": "waiting_local_directory"}[action]
	if status == "" {
		writeError(w, http.StatusNotFound, errors.New("unknown task action"))
		return
	}
	var body map[string]any
	if err := decodeBody(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	stamp := now()
	set, args := []string{"status=?", "updated_at=?"}, []any{status, stamp}
	if status == "running" {
		set, args = append(set, "started_at=?"), append(args, stamp)
	}
	if status == "completed" || status == "failed" {
		set, args = append(set, "completed_at=?"), append(args, stamp)
	}
	if value, ok := body["session_id"].(string); ok && value != "" {
		set, args = append(set, "thread_id=?"), append(args, value)
	}
	if value, ok := body["output"].(string); ok {
		set, args = append(set, "summary=?"), append(args, value)
	}
	if value, ok := body["error"].(string); ok {
		set, args = append(set, "error=?"), append(args, value)
	}
	taskID := chi.URLParam(r, "taskID")
	args = append(args, taskID, ws.ID)
	tx, err := s.store.db.BeginTx(r.Context(), nil)
	if err != nil {
		s.fail(w, err)
		return
	}
	defer func() { _ = tx.Rollback() }()
	result, err := tx.ExecContext(r.Context(), `UPDATE task_runs SET `+strings.Join(set, ", ")+` WHERE id=? AND status NOT IN ('completed','failed','cancelled') AND issue_id IN (SELECT id FROM issues WHERE workspace_id=?)`, args...)
	if err != nil {
		s.fail(w, err)
		return
	}
	if count, _ := result.RowsAffected(); count == 0 {
		var current string
		queryErr := tx.QueryRowContext(r.Context(), `SELECT tr.status FROM task_runs tr JOIN issues i ON i.id=tr.issue_id WHERE tr.id=? AND i.workspace_id=?`, taskID, ws.ID).Scan(&current)
		if errors.Is(queryErr, sql.ErrNoRows) {
			writeError(w, http.StatusNotFound, errors.New("task not found"))
			return
		}
		if queryErr == nil && current == status {
			if err := tx.Commit(); err != nil {
				s.fail(w, err)
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{})
			return
		}
		writeError(w, http.StatusConflict, errors.New("task is already terminal or missing"))
		return
	}
	var issueID string
	if err := tx.QueryRowContext(r.Context(), `SELECT tr.issue_id FROM task_runs tr JOIN issues i ON i.id=tr.issue_id WHERE tr.id=? AND i.workspace_id=?`, taskID, ws.ID).Scan(&issueID); err != nil {
		s.respondStoreError(w, err)
		return
	}
	issueStatus := map[string]string{"running": "in_progress", "completed": "in_review", "failed": "blocked"}[status]
	if issueStatus != "" {
		if _, err := tx.ExecContext(r.Context(), `UPDATE issues SET status=?, revision=revision+1, last_activity_at=?, updated_at=? WHERE id=?`, issueStatus, stamp, stamp, issueID); err != nil {
			s.fail(w, err)
			return
		}
	}
	if status == "completed" || status == "failed" {
		if output := strings.TrimSpace(stringFromMap(body, "output")); status == "completed" && output != "" {
			var revision int64
			if err := tx.QueryRowContext(r.Context(), `UPDATE issues SET revision=revision+1, last_activity_at=?, updated_at=? WHERE id=? RETURNING revision`, stamp, stamp, issueID).Scan(&revision); err != nil {
				s.fail(w, err)
				return
			}
			if _, err := tx.ExecContext(r.Context(), `INSERT INTO comments(id, issue_id, author_type, author_id, content, type, issue_revision, created_at, updated_at) VALUES (?, ?, 'agent', ?, ?, 'comment', ?, ?, ?)`, uuid.NewString(), issueID, LocalAgentID, output, revision, stamp, stamp); err != nil {
				s.fail(w, err)
				return
			}
		}
		details, err := json.Marshal(map[string]any{"task_id": taskID, "status": status})
		if err != nil {
			s.fail(w, err)
			return
		}
		eventAction := "task_completed"
		if status == "failed" {
			eventAction = "task_failed"
		}
		if _, err := tx.ExecContext(r.Context(), `INSERT INTO activities(id, issue_id, actor_type, actor_id, action, details, created_at) VALUES (?, ?, 'agent', ?, ?, ?, ?)`, uuid.NewString(), issueID, LocalAgentID, eventAction, string(details), stamp); err != nil {
			s.fail(w, err)
			return
		}
	}
	if err := tx.Commit(); err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{})
}

func stringFromMap(values map[string]any, key string) string {
	value, _ := values[key].(string)
	return value
}

func (s *Server) daemonTaskProgress(w http.ResponseWriter, r *http.Request) {
	ws, err := s.workspaceFor(r)
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	var input map[string]any
	if err := decodeBody(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if summary, ok := input["summary"].(string); ok {
		result, err := s.store.db.ExecContext(r.Context(), `UPDATE task_runs SET summary=?, updated_at=? WHERE id=? AND status NOT IN ('completed','failed','cancelled') AND issue_id IN (SELECT id FROM issues WHERE workspace_id=?)`, summary, now(), chi.URLParam(r, "taskID"), ws.ID)
		if err != nil {
			s.fail(w, err)
			return
		}
		if count, _ := result.RowsAffected(); count == 0 {
			writeError(w, http.StatusNotFound, errors.New("task not found"))
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{})
}

func (s *Server) daemonTaskStatus(w http.ResponseWriter, r *http.Request) {
	ws, err := s.workspaceFor(r)
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	var status string
	if err := s.store.db.QueryRowContext(r.Context(), `SELECT tr.status FROM task_runs tr JOIN issues i ON i.id=tr.issue_id WHERE tr.id=? AND i.workspace_id=?`, chi.URLParam(r, "taskID"), ws.ID).Scan(&status); err != nil {
		s.respondStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": status})
}

func (s *Server) daemonTaskSession(w http.ResponseWriter, r *http.Request) {
	ws, err := s.workspaceFor(r)
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	var input struct {
		SessionID string `json:"session_id"`
	}
	if err := decodeBody(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	result, err := s.store.db.ExecContext(r.Context(), `UPDATE task_runs SET thread_id=?, updated_at=? WHERE id=? AND issue_id IN (SELECT id FROM issues WHERE workspace_id=?)`, nullable(input.SessionID), now(), chi.URLParam(r, "taskID"), ws.ID)
	if err != nil {
		s.fail(w, err)
		return
	}
	if count, _ := result.RowsAffected(); count == 0 {
		writeError(w, http.StatusNotFound, errors.New("task not found"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{})
}

func (s *Server) daemonRecoverOrphans(w http.ResponseWriter, r *http.Request) {
	ws, err := s.workspaceFor(r)
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	var exists int
	if err := s.store.db.QueryRowContext(r.Context(), `SELECT 1 FROM runtimes WHERE workspace_id=? AND id=?`, ws.ID, chi.URLParam(r, "runtimeID")).Scan(&exists); err != nil {
		s.respondStoreError(w, err)
		return
	}
	_, err = s.store.db.ExecContext(r.Context(), `UPDATE task_runs SET status='queued', started_at=NULL, updated_at=? WHERE runtime=? AND status IN ('dispatched','waiting_local_directory','running') AND issue_id IN (SELECT id FROM issues WHERE workspace_id=?)`, now(), chi.URLParam(r, "runtimeID"), ws.ID)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{})
}

func (s *Server) daemonTaskNoop(w http.ResponseWriter, r *http.Request) {
	ws, err := s.workspaceFor(r)
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	var exists int
	query := `SELECT 1 FROM task_runs tr JOIN issues i ON i.id=tr.issue_id WHERE tr.id=? AND i.workspace_id=?`
	args := []any{chi.URLParam(r, "taskID"), ws.ID}
	if runtimeID := chi.URLParam(r, "runtimeID"); runtimeID != "" {
		query += ` AND tr.runtime=?`
		args = append(args, runtimeID)
	}
	if err := s.store.db.QueryRowContext(r.Context(), query, args...).Scan(&exists); err != nil {
		s.respondStoreError(w, err)
		return
	}
	var input map[string]any
	if err := decodeBody(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{})
}
