package localworkbench

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

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

func (s *Store) latestCodexRuntime(ctx context.Context, workspaceID string) (localRuntime, error) {
	return scanRuntime(s.db.QueryRowContext(ctx, `SELECT id, workspace_id, daemon_id, name, provider, version, status, device_name, profile_id, last_seen_at, created_at, updated_at FROM runtimes WHERE workspace_id=? AND provider='codex' ORDER BY updated_at DESC LIMIT 1`, workspaceID))
}

func localAgent(workspaceID string, runtime localRuntime) map[string]any {
	status := "offline"
	if runtime.Status == "online" {
		status = "idle"
	}
	return map[string]any{
		"id": LocalAgentID, "workspace_id": workspaceID, "runtime_id": runtime.ID, "runtime_bound": true,
		"name": "Codex", "description": "Local Codex runtime", "instructions": "Work on the assigned local task and report the result.",
		"avatar_url": nil, "runtime_mode": "local", "runtime_config": map[string]any{}, "custom_args": []any{},
		"visibility": "private", "permission_mode": "private", "invocation_targets": []any{}, "status": status,
		"max_concurrent_tasks": 1, "model": "", "owner_id": nil, "skills": []any{}, "created_at": runtime.CreatedAt,
		"updated_at": runtime.UpdatedAt, "archived_at": nil, "archived_by": nil,
	}
}

func (s *Store) enqueueAssignedIssue(ctx context.Context, workspaceID, issueID, handoffNote string) error {
	issue, err := s.GetIssue(ctx, workspaceID, issueID)
	if err != nil {
		return err
	}
	if issue.ProjectID == nil || strings.TrimSpace(*issue.ProjectID) == "" {
		return ErrIssueProjectRequired
	}
	if issue.AssigneeType == nil || issue.AssigneeID == nil || !localIssueCategoryRunnable(issue.StatusCategory) {
		return nil
	}
	agentID := *issue.AssigneeID
	if *issue.AssigneeType == "squad" {
		if err := s.db.QueryRowContext(ctx, `SELECT leader_id FROM squads WHERE id=? AND workspace_id=? AND archived_at IS NULL`, agentID, workspaceID).Scan(&agentID); err != nil {
			return err
		}
	} else if *issue.AssigneeType != "agent" {
		return nil
	}
	var runtime localRuntime
	err = s.db.QueryRowContext(ctx, `SELECT r.id, r.workspace_id, r.daemon_id, r.name, r.provider,
		r.version, r.status, r.device_name, r.profile_id, r.last_seen_at, r.created_at, r.updated_at
		FROM agents a JOIN runtimes r ON r.id=a.runtime_id
		WHERE a.id=? AND a.workspace_id=? AND a.archived_at IS NULL AND r.status='online'`,
		agentID, workspaceID).Scan(&runtime.ID, &runtime.WorkspaceID, &runtime.DaemonID,
		&runtime.Name, &runtime.Provider, &runtime.Version, &runtime.Status, &runtime.DeviceName,
		&runtime.ProfileID, &runtime.LastSeenAt, &runtime.CreatedAt, &runtime.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var sessionID, stageID string
	if err := tx.QueryRowContext(ctx, `SELECT id, active_stage_id FROM issue_sessions WHERE workspace_id=? AND issue_id=?`, workspaceID, issueID).Scan(&sessionID, &stageID); err != nil {
		return fmt.Errorf("reading issue session for queued run: %w", err)
	}
	var active int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM task_runs WHERE issue_id=? AND status IN ('queued','dispatched','waiting_local_directory','running','waiting_previous_run')`, issueID).Scan(&active); err != nil {
		return err
	}
	if active > 0 {
		return tx.Commit()
	}
	var historyCount int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM task_runs WHERE issue_id=?`, issueID).Scan(&historyCount); err != nil {
		return err
	}
	var priorTaskID, priorThreadID, priorWorkDir sql.NullString
	err = tx.QueryRowContext(ctx, `SELECT id, thread_id, work_dir FROM task_runs WHERE issue_id=? AND NULLIF(TRIM(thread_id),'') IS NOT NULL AND NULLIF(TRIM(work_dir),'') IS NOT NULL ORDER BY created_at DESC LIMIT 1`, issueID).Scan(&priorTaskID, &priorThreadID, &priorWorkDir)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	continuityState, continuityReason := "fresh", ""
	if priorTaskID.Valid {
		continuityState = "pending"
	} else if historyCount > 0 {
		continuityState = "gap"
		continuityReason = "missing_resume_pointer"
	}
	stamp := now()
	var squadID string
	if *issue.AssigneeType == "squad" {
		squadID = *issue.AssigneeID
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO task_runs(id, issue_id, status, runtime, handoff_note, session_id, stage_id, prior_task_id, continuity_state, continuity_reason, agent_id, squad_id, created_at, updated_at) VALUES (?, ?, 'queued', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, uuid.NewString(), issueID, runtime.ID, nullable(handoffNote), sessionID, stageID, nullable(priorTaskID.String), continuityState, nullable(continuityReason), agentID, nullable(squadID), stamp, stamp)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE issue_sessions SET continuity_state=?, continuity_reason=?, updated_at=? WHERE id=?`, continuityState, nullable(continuityReason), stamp, sessionID); err != nil {
		return err
	}
	return tx.Commit()
}

func localIssueCategoryRunnable(category string) bool {
	switch category {
	case "backlog", "done", "cancelled", "canceled":
		return false
	default:
		return true
	}
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
			"device_info": "", "metadata": map[string]any{"cli_version": item.Version}, "owner_id": nil, "visibility": "private",
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
	runtime, err := s.store.latestCodexRuntime(r.Context(), ws.ID)
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

func (s *Server) getAgent(w http.ResponseWriter, r *http.Request) {
	ws, err := s.workspaceFor(r)
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	if chi.URLParam(r, "id") != LocalAgentID {
		s.respondStoreError(w, sql.ErrNoRows)
		return
	}
	runtime, err := s.store.codexRuntime(r.Context(), ws.ID)
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, localAgent(ws.ID, runtime))
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
		if provider == "codex" {
			if err := ensureDefaultAgentTx(r.Context(), tx, ws.ID, id); err != nil {
				s.fail(w, err)
				return
			}
		}
	}
	if err := tx.Commit(); err != nil {
		s.fail(w, err)
		return
	}
	items, err := s.store.listRuntimes(r.Context(), ws.ID)
	if err != nil {
		s.fail(w, err)
		return
	}
	runtimes := []map[string]any{}
	runtimeIDs := []string{}
	for _, item := range items {
		if item.DaemonID == input.DaemonID && item.Status == "online" {
			runtimes = append(runtimes, map[string]any{"id": item.ID, "name": item.Name, "provider": item.Provider, "status": item.Status, "profile_id": item.ProfileID})
			runtimeIDs = append(runtimeIDs, item.ID)
		}
	}
	s.hub.broadcast("daemon:register", map[string]any{"daemon_id": input.DaemonID, "runtime_ids": runtimeIDs})
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
		RuntimeID           string `json:"runtime_id"`
		SupportsBatchImport bool   `json:"supports_batch_import"`
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
	pending, err := s.claimRuntimeRequests(r.Context(), ws.ID, input.RuntimeID, input.SupportsBatchImport)
	if err != nil {
		s.fail(w, err)
		return
	}
	pending["runtime_id"], pending["status"], pending["server_capabilities"] = input.RuntimeID, "ok", []string{"batch_local_skill_import"}
	writeJSON(w, http.StatusOK, pending)
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
	var taskID, issueID, sessionID, stageID, continuityState, agentID string
	var handoffNote, priorTaskID, triggerCommentID sql.NullString
	err = tx.QueryRowContext(ctx, `UPDATE task_runs SET status='dispatched', updated_at=? WHERE id=(SELECT tr.id FROM task_runs tr JOIN issues i ON i.id=tr.issue_id WHERE tr.status='queued' AND tr.runtime=? AND i.workspace_id=? ORDER BY tr.created_at LIMIT 1) RETURNING id, issue_id, handoff_note, session_id, stage_id, prior_task_id, trigger_comment_id, continuity_state, COALESCE(agent_id,'')`, stamp, runtimeID, workspaceID).Scan(&taskID, &issueID, &handoffNote, &sessionID, &stageID, &priorTaskID, &triggerCommentID, &continuityState, &agentID)
	if errors.Is(err, sql.ErrNoRows) {
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		return s.claimChatTask(ctx, workspaceID, runtimeID)
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
	var stageKind string
	if err := s.store.db.QueryRowContext(ctx, `SELECT kind FROM issue_session_stages WHERE id=? AND session_id=?`, stageID, sessionID).Scan(&stageKind); err != nil {
		return nil, err
	}
	ws, err := s.store.GetWorkspace(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	if agentID == "" {
		if issue.AssigneeID != nil {
			agentID = *issue.AssigneeID
		}
		if issue.AssigneeType != nil && *issue.AssigneeType == "squad" {
			if err := s.store.db.QueryRowContext(ctx, `SELECT leader_id FROM squads WHERE id=? AND workspace_id=?`, agentID, workspaceID).Scan(&agentID); err != nil {
				return nil, err
			}
		}
	}
	agent, err := s.executionAgent(ctx, workspaceID, agentID)
	if err != nil {
		return nil, err
	}
	task := map[string]any{
		"id": taskID, "agent_id": agentID, "runtime_id": runtimeID, "issue_id": issue.ID, "workspace_id": workspaceID,
		"thread_name": issue.Identifier + " " + issue.Title, "workspace_context": stringValue(ws.Context),
		"agent": agent, "auth_token": localTaskToken(),
		"issue_session_id": sessionID, "issue_session_stage_id": stageID, "issue_session_stage_kind": stageKind, "continuity_state": continuityState,
	}
	if handoffNote.Valid && strings.TrimSpace(handoffNote.String) != "" {
		task["handoff_note"] = handoffNote.String
	}
	if triggerCommentID.Valid && strings.TrimSpace(triggerCommentID.String) != "" {
		var triggerContent, triggerAuthorType, triggerAuthorID string
		if err := s.store.db.QueryRowContext(ctx, `SELECT content, author_type, author_id FROM comments WHERE id=? AND issue_id=?`, triggerCommentID.String, issueID).Scan(&triggerContent, &triggerAuthorType, &triggerAuthorID); err != nil {
			return nil, fmt.Errorf("reading issue session trigger comment: %w", err)
		}
		task["trigger_comment_id"] = triggerCommentID.String
		task["trigger_thread_id"] = triggerCommentID.String
		task["trigger_comment_content"] = triggerContent
		task["trigger_author_type"] = triggerAuthorType
		task["trigger_author_name"] = triggerAuthorID
	}
	if priorTaskID.Valid && strings.TrimSpace(priorTaskID.String) != "" {
		var priorSessionID, priorWorkDir sql.NullString
		if err := s.store.db.QueryRowContext(ctx, `SELECT thread_id, work_dir FROM task_runs WHERE id=? AND issue_id=?`, priorTaskID.String, issueID).Scan(&priorSessionID, &priorWorkDir); err != nil {
			return nil, fmt.Errorf("reading prior issue session run: %w", err)
		}
		if priorSessionID.Valid && priorWorkDir.Valid && strings.TrimSpace(priorSessionID.String) != "" && strings.TrimSpace(priorWorkDir.String) != "" {
			task["prior_session_id"] = priorSessionID.String
			task["prior_work_dir"] = priorWorkDir.String
		}
	}
	if inherited, err := s.store.getIssueHandoff(ctx, issueID); err == nil {
		task["issue_handoff"] = inherited
	} else if !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("reading inherited issue handoff: %w", err)
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
	task["runtime_provider"] = provider
	if err := s.addLocalPluginClaimContext(ctx, workspaceID, task); err != nil {
		return nil, err
	}
	return task, nil
}

func (s *Server) claimChatTask(ctx context.Context, workspaceID, runtimeID string) (map[string]any, error) {
	tx, err := s.store.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	stamp := now()
	var taskID, sessionID, agentID string
	err = tx.QueryRowContext(ctx, `UPDATE chat_tasks SET status='dispatched', updated_at=?
		WHERE id=(SELECT t.id FROM chat_tasks t JOIN chat_sessions s ON s.id=t.chat_session_id
		WHERE t.runtime_id=? AND t.status='queued' AND s.workspace_id=? ORDER BY t.priority DESC, t.created_at, t.id LIMIT 1)
		RETURNING id, chat_session_id, agent_id`, stamp, runtimeID, workspaceID).Scan(&taskID, &sessionID, &agentID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, tx.Commit()
	}
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	var title, projectID string
	var project sql.NullString
	if err := s.store.db.QueryRowContext(ctx, `SELECT title, project_id FROM chat_sessions WHERE id=? AND workspace_id=?`, sessionID, workspaceID).Scan(&title, &project); err != nil {
		return nil, err
	}
	if project.Valid {
		projectID = project.String
	}
	var message string
	if err := s.store.db.QueryRowContext(ctx, `SELECT content FROM chat_messages WHERE chat_session_id=? AND task_id=? AND role='user'`, sessionID, taskID).Scan(&message); err != nil {
		return nil, err
	}
	agent, err := s.executionAgent(ctx, workspaceID, agentID)
	if err != nil {
		return nil, err
	}
	ws, err := s.store.GetWorkspace(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	task := map[string]any{
		"id": taskID, "agent_id": agentID, "runtime_id": runtimeID,
		"workspace_id": workspaceID, "workspace_slug": ws.Slug,
		"workspace_context": stringValue(ws.Context), "thread_name": title,
		"chat_session_id": sessionID, "chat_message": message,
		"project_id": projectID, "agent": agent, "auth_token": localTaskToken(),
	}
	if err := s.addLocalPluginClaimContext(ctx, workspaceID, task); err != nil {
		return nil, err
	}
	return task, nil
}

func localTaskToken() string {
	return "mat_local_" + strings.ReplaceAll(uuid.NewString(), "-", "")
}

func (s *Server) addLocalPluginClaimContext(ctx context.Context, workspaceID string, task map[string]any) error {
	tools, connections, err := s.localPluginClaimContext(ctx, workspaceID)
	if err != nil {
		return err
	}
	if len(tools) > 0 {
		task["plugin_hook_tools"] = tools
	}
	if len(connections) > 0 {
		task["remote_mcp_connections"] = connections
	}
	if len(tools) > 0 || len(connections) > 0 {
		task["remote_mcp_daemon_token"] = "mdt_local_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	}
	return nil
}

func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func pinTaskSessionTx(ctx context.Context, tx *sql.Tx, workspaceID, taskID, sessionID, workDir string, terminal bool) error {
	var issueSessionID string
	var priorTaskID sql.NullString
	var taskStatus string
	if err := tx.QueryRowContext(ctx, `SELECT tr.session_id, tr.prior_task_id, tr.status FROM task_runs tr JOIN issues i ON i.id=tr.issue_id WHERE tr.id=? AND i.workspace_id=?`, taskID, workspaceID).Scan(&issueSessionID, &priorTaskID, &taskStatus); err != nil {
		return err
	}
	if taskStatus == "completed" || taskStatus == "failed" || taskStatus == "cancelled" {
		return nil
	}
	if strings.TrimSpace(sessionID) == "" && strings.TrimSpace(workDir) == "" && !terminal {
		return nil
	}
	state, reason := "fresh", ""
	if priorTaskID.Valid {
		var priorThreadID, priorWorkDir sql.NullString
		if err := tx.QueryRowContext(ctx, `SELECT thread_id, work_dir FROM task_runs WHERE id=?`, priorTaskID.String).Scan(&priorThreadID, &priorWorkDir); err != nil {
			return err
		}
		if strings.TrimSpace(sessionID) == "" && strings.TrimSpace(workDir) == "" {
			state, reason = "gap", "provider_session_unreported"
		} else if priorThreadID.Valid && priorWorkDir.Valid && strings.TrimSpace(priorThreadID.String) != "" && strings.TrimSpace(priorWorkDir.String) != "" && sessionID == priorThreadID.String && workDir == priorWorkDir.String {
			state = "resumed"
		} else {
			state, reason = "gap", "provider_thread_replaced"
			if !priorThreadID.Valid || !priorWorkDir.Valid || strings.TrimSpace(priorThreadID.String) == "" || strings.TrimSpace(priorWorkDir.String) == "" {
				reason = "missing_resume_pointer"
			} else if sessionID == priorThreadID.String && workDir != priorWorkDir.String {
				reason = "work_dir_replaced"
			}
		}
	}
	stamp := now()
	if _, err := tx.ExecContext(ctx, `UPDATE task_runs SET thread_id=COALESCE(NULLIF(?,''),thread_id), work_dir=COALESCE(NULLIF(?,''),work_dir), continuity_state=?, continuity_reason=?, updated_at=? WHERE id=?`, sessionID, workDir, state, nullable(reason), stamp, taskID); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `UPDATE issue_sessions SET continuity_state=?, continuity_reason=?, updated_at=? WHERE id=?`, state, nullable(reason), stamp, issueSessionID)
	return err
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
	if value, ok := body["output"].(string); ok {
		set, args = append(set, "summary=?"), append(args, value)
	}
	if value, ok := body["error"].(string); ok {
		set, args = append(set, "error=?"), append(args, value)
	}
	taskID := chi.URLParam(r, "taskID")
	var isChatTask int
	if err := s.store.db.QueryRowContext(r.Context(), `SELECT EXISTS(
		SELECT 1 FROM chat_tasks t JOIN chat_sessions cs ON cs.id=t.chat_session_id
		WHERE t.id=? AND cs.workspace_id=?)`, taskID, ws.ID).Scan(&isChatTask); err != nil {
		s.fail(w, err)
		return
	}
	if isChatTask == 1 {
		s.daemonChatTaskState(w, r, ws.ID, taskID, status, body)
		return
	}
	args = append(args, taskID, ws.ID)
	tx, err := s.store.db.BeginTx(r.Context(), nil)
	if err != nil {
		s.fail(w, err)
		return
	}
	defer func() { _ = tx.Rollback() }()
	sessionID, _ := body["session_id"].(string)
	workDir, _ := body["work_dir"].(string)
	terminal := status == "completed" || status == "failed"
	createdCommentID := ""
	if err := pinTaskSessionTx(r.Context(), tx, ws.ID, chi.URLParam(r, "taskID"), sessionID, workDir, terminal); err != nil && !errors.Is(err, sql.ErrNoRows) {
		s.fail(w, err)
		return
	}
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
	var persistedAgentID, assigneeType, assigneeID sql.NullString
	if err := tx.QueryRowContext(r.Context(), `SELECT tr.issue_id,tr.agent_id,i.assignee_type,i.assignee_id FROM task_runs tr JOIN issues i ON i.id=tr.issue_id WHERE tr.id=? AND i.workspace_id=?`, taskID, ws.ID).Scan(&issueID, &persistedAgentID, &assigneeType, &assigneeID); err != nil {
		s.respondStoreError(w, err)
		return
	}
	agentID := LocalAgentID
	if persistedAgentID.Valid && strings.TrimSpace(persistedAgentID.String) != "" {
		agentID = persistedAgentID.String
	} else if assigneeID.Valid && strings.TrimSpace(assigneeID.String) != "" {
		agentID = assigneeID.String
	}
	if !persistedAgentID.Valid && assigneeType.Valid && assigneeType.String == "squad" {
		if err := tx.QueryRowContext(r.Context(), `SELECT leader_id FROM squads WHERE id=?`, agentID).Scan(&agentID); err != nil {
			s.fail(w, err)
			return
		}
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
			createdCommentID = uuid.NewString()
			if _, err := tx.ExecContext(r.Context(), `INSERT INTO comments(id, issue_id, author_type, author_id, content, type, issue_revision, source_task_id, created_at, updated_at) VALUES (?, ?, 'agent', ?, ?, 'comment', ?, ?, ?, ?)`, createdCommentID, issueID, agentID, output, revision, taskID, stamp, stamp); err != nil {
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
		if _, err := tx.ExecContext(r.Context(), `INSERT INTO activities(id, issue_id, actor_type, actor_id, action, details, created_at) VALUES (?, ?, 'agent', ?, ?, ?, ?)`, uuid.NewString(), issueID, agentID, eventAction, string(details), stamp); err != nil {
			s.fail(w, err)
			return
		}
		if _, err := promoteWaitingIssueRunTx(r.Context(), tx, issueID, stamp); err != nil {
			s.fail(w, err)
			return
		}
		severity := "info"
		title := "Agent task completed"
		if status == "failed" {
			severity, title = "error", "Agent task failed"
		}
		if _, err := insertInboxTx(tx, ws.ID, "agent_task_"+status, severity, issueID, title, stringFromMap(body, "error")); err != nil {
			s.fail(w, err)
			return
		}
	}
	if err := tx.Commit(); err != nil {
		s.fail(w, err)
		return
	}
	if issue, err := s.store.GetIssue(r.Context(), ws.ID, issueID); err == nil {
		s.hub.broadcast("issue:updated", map[string]any{"issue": issue})
	}
	if createdCommentID != "" {
		if comment, err := scanComment(s.store.db.QueryRowContext(r.Context(), `SELECT id, issue_id, author_type, author_id, content, type, parent_id, created_at, updated_at, revision, issue_revision, resolved_at, resolved_by_type, resolved_by_id, source_task_id, quick_action_id FROM comments WHERE id=?`, createdCommentID)); err == nil {
			s.hub.broadcast("comment:created", map[string]any{"comment": comment})
		}
	}
	s.hub.broadcast("task:"+status, map[string]any{
		"task_id":  taskID,
		"agent_id": agentID,
		"issue_id": issueID,
		"status":   status,
	})
	writeJSON(w, http.StatusOK, map[string]any{})
}

func (s *Server) daemonChatTaskState(w http.ResponseWriter, r *http.Request, workspaceID, taskID, status string, body map[string]any) {
	tx, err := s.store.db.BeginTx(r.Context(), nil)
	if err != nil {
		s.fail(w, err)
		return
	}
	defer func() { _ = tx.Rollback() }()
	stamp := now()
	set, args := []string{"status=?", "updated_at=?"}, []any{status, stamp}
	if status == "running" {
		set, args = append(set, "started_at=?"), append(args, stamp)
	}
	if status == "completed" || status == "failed" {
		set, args = append(set, "completed_at=?"), append(args, stamp)
	}
	if value, ok := body["output"].(string); ok {
		set, args = append(set, "summary=?"), append(args, value)
	}
	if value, ok := body["error"].(string); ok {
		set, args = append(set, "error=?"), append(args, value)
	}
	if sessionID, ok := body["session_id"].(string); ok && strings.TrimSpace(sessionID) != "" {
		set, args = append(set, "thread_id=?"), append(args, sessionID)
	}
	if workDir, ok := body["work_dir"].(string); ok && strings.TrimSpace(workDir) != "" {
		set, args = append(set, "work_dir=?"), append(args, workDir)
	}
	args = append(args, taskID, workspaceID)
	result, err := tx.ExecContext(r.Context(), `UPDATE chat_tasks SET `+strings.Join(set, ",")+`
		WHERE id=? AND chat_session_id IN (SELECT id FROM chat_sessions WHERE workspace_id=?)
		AND status NOT IN ('completed','failed','cancelled')`, args...)
	if err != nil {
		s.fail(w, err)
		return
	}
	if count, _ := result.RowsAffected(); count == 0 {
		var current string
		if err := tx.QueryRowContext(r.Context(), `SELECT status FROM chat_tasks WHERE id=?`, taskID).Scan(&current); err != nil {
			s.respondStoreError(w, err)
			return
		}
		if current != status {
			writeError(w, http.StatusConflict, errors.New("task is already terminal"))
			return
		}
		_ = tx.Commit()
		writeJSON(w, http.StatusOK, map[string]any{})
		return
	}
	var chatSessionID, agentID, createdAt string
	if err := tx.QueryRowContext(r.Context(), `SELECT chat_session_id, agent_id, created_at FROM chat_tasks WHERE id=?`, taskID).Scan(&chatSessionID, &agentID, &createdAt); err != nil {
		s.fail(w, err)
		return
	}
	messageID := ""
	if status == "completed" || status == "failed" {
		content := strings.TrimSpace(stringFromMap(body, "output"))
		failureReason := any(nil)
		messageKind := "message"
		if status == "failed" {
			content = strings.TrimSpace(stringFromMap(body, "error"))
			if content == "" {
				content = "Agent task failed"
			}
			failureReason = "agent_error"
		} else if content == "" {
			messageKind = "no_response"
		}
		start, _ := time.Parse(time.RFC3339Nano, createdAt)
		end, _ := time.Parse(time.RFC3339Nano, stamp)
		elapsed := end.Sub(start).Milliseconds()
		messageID = uuid.NewString()
		if _, err := tx.ExecContext(r.Context(), `INSERT INTO chat_messages(id, chat_session_id, role, content, task_id, failure_reason, elapsed_ms, message_kind, created_at) VALUES (?, ?, 'assistant', ?, ?, ?, ?, ?, ?)`, messageID, chatSessionID, content, taskID, failureReason, elapsed, messageKind, stamp); err != nil {
			s.fail(w, err)
			return
		}
		if _, err := tx.ExecContext(r.Context(), `UPDATE chat_sessions SET updated_at=? WHERE id=?`, stamp, chatSessionID); err != nil {
			s.fail(w, err)
			return
		}
	}
	if err := tx.Commit(); err != nil {
		s.fail(w, err)
		return
	}
	s.hub.broadcast("task:"+status, map[string]any{"task_id": taskID, "agent_id": agentID, "chat_session_id": chatSessionID, "status": status})
	if messageID != "" {
		s.hub.broadcast("chat:message_created", map[string]any{"message_id": messageID, "chat_session_id": chatSessionID, "task_id": taskID})
	}
	writeJSON(w, http.StatusOK, map[string]any{})
}

func stringFromMap(values map[string]any, key string) string {
	value, _ := values[key].(string)
	return value
}

type daemonTaskRef struct {
	ContainerID string
	Kind        string
}

func (s *Server) daemonTaskRef(ctx context.Context, workspaceID, taskID string) (daemonTaskRef, error) {
	var ref daemonTaskRef
	err := s.store.db.QueryRowContext(ctx, `SELECT container_id, kind FROM (
		SELECT tr.issue_id AS container_id, 'issue' AS kind
		FROM task_runs tr JOIN issues i ON i.id=tr.issue_id
		WHERE tr.id=? AND i.workspace_id=?
		UNION ALL
		SELECT ct.chat_session_id AS container_id, 'chat' AS kind
		FROM chat_tasks ct JOIN chat_sessions cs ON cs.id=ct.chat_session_id
		WHERE ct.id=? AND cs.workspace_id=?
	) LIMIT 1`, taskID, workspaceID, taskID, workspaceID).Scan(&ref.ContainerID, &ref.Kind)
	return ref, err
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
		taskID := chi.URLParam(r, "taskID")
		ref, err := s.daemonTaskRef(r.Context(), ws.ID, taskID)
		if err != nil {
			s.respondStoreError(w, err)
			return
		}
		table, containerColumn := "task_runs", "issue_id"
		if ref.Kind == "chat" {
			table, containerColumn = "chat_tasks", "chat_session_id"
		}
		result, err := s.store.db.ExecContext(r.Context(), `UPDATE `+table+` SET summary=?, updated_at=? WHERE id=? AND `+containerColumn+`=? AND status NOT IN ('completed','failed','cancelled')`, summary, now(), taskID, ref.ContainerID)
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

type localTaskMessageInput struct {
	Seq     int            `json:"seq"`
	Type    string         `json:"type"`
	Tool    string         `json:"tool"`
	Content string         `json:"content"`
	Input   map[string]any `json:"input"`
	Output  string         `json:"output"`
}

func (s *Server) daemonTaskMessages(w http.ResponseWriter, r *http.Request) {
	ws, err := s.workspaceFor(r)
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	var input struct {
		Messages []localTaskMessageInput `json:"messages"`
	}
	if err := decodeBody(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if len(input.Messages) > 1000 {
		writeError(w, http.StatusBadRequest, errors.New("too many task messages"))
		return
	}
	taskID := chi.URLParam(r, "taskID")
	ref, err := s.daemonTaskRef(r.Context(), ws.ID, taskID)
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	tx, err := s.store.db.BeginTx(r.Context(), nil)
	if err != nil {
		s.fail(w, err)
		return
	}
	defer func() { _ = tx.Rollback() }()
	payloads := make([]map[string]any, 0, len(input.Messages))
	for _, message := range input.Messages {
		if message.Seq <= 0 || !validTaskMessageType(message.Type) {
			writeError(w, http.StatusBadRequest, errors.New("invalid task message"))
			return
		}
		encodedInput, err := json.Marshal(message.Input)
		if err != nil {
			writeError(w, http.StatusBadRequest, errors.New("invalid task message input"))
			return
		}
		stamp := now()
		result, err := tx.ExecContext(r.Context(), `INSERT INTO task_messages(task_id, issue_id, seq, type, tool, content, input, output, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(task_id, seq) DO NOTHING`,
			taskID, ref.ContainerID, message.Seq, message.Type, nullable(message.Tool), nullable(message.Content), nullable(string(encodedInput)), nullable(message.Output), stamp)
		if err != nil {
			s.fail(w, err)
			return
		}
		if inserted, _ := result.RowsAffected(); inserted != 0 {
			payload := taskMessagePayload(taskID, ref.ContainerID, message.Seq, message.Type, message.Tool, message.Content, message.Input, message.Output, stamp)
			if ref.Kind == "chat" {
				payload["chat_session_id"] = ref.ContainerID
				delete(payload, "issue_id")
			}
			payloads = append(payloads, payload)
		}
	}
	if err := tx.Commit(); err != nil {
		s.fail(w, err)
		return
	}
	for _, payload := range payloads {
		s.hub.broadcast("task:message", payload)
	}
	writeJSON(w, http.StatusOK, map[string]any{})
}

func validTaskMessageType(value string) bool {
	switch value {
	case "text", "thinking", "tool_use", "tool_result", "error":
		return true
	default:
		return false
	}
}

func taskMessagePayload(taskID, issueID string, seq int, messageType, tool, content string, input map[string]any, output, createdAt string) map[string]any {
	payload := map[string]any{"task_id": taskID, "issue_id": issueID, "seq": seq, "type": messageType, "created_at": createdAt}
	if tool != "" {
		payload["tool"] = tool
	}
	if content != "" {
		payload["content"] = content
	}
	if input != nil {
		payload["input"] = input
	}
	if output != "" {
		payload["output"] = output
	}
	return payload
}

func (s *Server) listTaskMessages(w http.ResponseWriter, r *http.Request) {
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
	rows, err := s.store.db.QueryContext(r.Context(), `SELECT seq, type, COALESCE(tool,''), COALESCE(content,''), COALESCE(input,''), COALESCE(output,''), created_at FROM task_messages WHERE task_id=? AND issue_id=? ORDER BY seq`, taskID, ref.ContainerID)
	if err != nil {
		s.fail(w, err)
		return
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var seq int
		var messageType, tool, content, encodedInput, output, createdAt string
		if err := rows.Scan(&seq, &messageType, &tool, &content, &encodedInput, &output, &createdAt); err != nil {
			s.fail(w, err)
			return
		}
		var messageInput map[string]any
		if encodedInput != "" {
			if err := json.Unmarshal([]byte(encodedInput), &messageInput); err != nil {
				s.fail(w, err)
				return
			}
		}
		payload := taskMessagePayload(taskID, ref.ContainerID, seq, messageType, tool, content, messageInput, output, createdAt)
		if ref.Kind == "chat" {
			payload["chat_session_id"] = ref.ContainerID
			delete(payload, "issue_id")
		}
		items = append(items, payload)
	}
	if err := rows.Err(); err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, items)
}

func (s *Server) daemonTaskStatus(w http.ResponseWriter, r *http.Request) {
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
	query := `SELECT status FROM task_runs WHERE id=? AND issue_id=?`
	if ref.Kind == "chat" {
		query = `SELECT status FROM chat_tasks WHERE id=? AND chat_session_id=?`
	}
	var status string
	if err := s.store.db.QueryRowContext(r.Context(), query, taskID, ref.ContainerID).Scan(&status); err != nil {
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
		WorkDir   string `json:"work_dir"`
	}
	if err := decodeBody(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	taskID := chi.URLParam(r, "taskID")
	ref, err := s.daemonTaskRef(r.Context(), ws.ID, taskID)
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	tx, err := s.store.db.BeginTx(r.Context(), nil)
	if err != nil {
		s.fail(w, err)
		return
	}
	defer func() { _ = tx.Rollback() }()
	if ref.Kind == "chat" {
		result, err := tx.ExecContext(r.Context(), `UPDATE chat_tasks SET thread_id=COALESCE(NULLIF(?,''),thread_id), work_dir=COALESCE(NULLIF(?,''),work_dir), updated_at=? WHERE id=? AND chat_session_id=? AND status NOT IN ('completed','failed','cancelled')`, input.SessionID, input.WorkDir, now(), taskID, ref.ContainerID)
		if err != nil {
			s.fail(w, err)
			return
		}
		if count, _ := result.RowsAffected(); count == 0 {
			writeError(w, http.StatusConflict, errors.New("task is already terminal"))
			return
		}
	} else if err := pinTaskSessionTx(r.Context(), tx, ws.ID, taskID, input.SessionID, input.WorkDir, false); err != nil {
		s.fail(w, err)
		return
	}
	if err := tx.Commit(); err != nil {
		s.fail(w, err)
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
	if _, err := s.store.db.ExecContext(r.Context(), `UPDATE chat_tasks SET status='queued', started_at=NULL, updated_at=? WHERE runtime_id=? AND status IN ('dispatched','waiting_local_directory','running') AND chat_session_id IN (SELECT id FROM chat_sessions WHERE workspace_id=?)`, now(), chi.URLParam(r, "runtimeID"), ws.ID); err != nil {
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
	taskID := chi.URLParam(r, "taskID")
	ref, err := s.daemonTaskRef(r.Context(), ws.ID, taskID)
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	var exists int
	query := `SELECT 1 FROM task_runs WHERE id=? AND issue_id=?`
	args := []any{taskID, ref.ContainerID}
	if ref.Kind == "chat" {
		query = `SELECT 1 FROM chat_tasks WHERE id=? AND chat_session_id=?`
	}
	if runtimeID := chi.URLParam(r, "runtimeID"); runtimeID != "" {
		if ref.Kind == "chat" {
			query += ` AND runtime_id=?`
		} else {
			query += ` AND runtime=?`
		}
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

func (s *Server) daemonTaskUsage(w http.ResponseWriter, r *http.Request) {
	ws, err := s.workspaceFor(r)
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	var body struct {
		Usage []TaskRunUsage `json:"usage"`
	}
	if err := decodeBody(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if body.Usage == nil {
		writeError(w, http.StatusBadRequest, errors.New("usage is required"))
		return
	}
	if err := s.store.ReplaceTaskRunUsage(r.Context(), ws.ID, chi.URLParam(r, "taskID"), body.Usage); err != nil {
		if errors.Is(err, errInvalidTaskRunUsage) {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		s.respondStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{})
}
