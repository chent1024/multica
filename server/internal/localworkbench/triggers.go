package localworkbench

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/util"
)

type localTriggerTarget struct {
	TargetType string
	TargetID   string
	AgentID    string
	SquadID    string
	Name       string
	AvatarURL  any
	RuntimeID  string
	Ready      bool
}

func (s *Store) resolveMentionTarget(ctx context.Context, workspaceID string, mention util.Mention) (localTriggerTarget, error) {
	target := localTriggerTarget{TargetType: mention.Type, TargetID: mention.ID}
	switch mention.Type {
	case "agent":
		target.AgentID = mention.ID
	case "squad":
		target.SquadID = mention.ID
		if err := s.db.QueryRowContext(ctx, `SELECT leader_id FROM squads WHERE id=? AND workspace_id=? AND archived_at IS NULL`, mention.ID, workspaceID).Scan(&target.AgentID); err != nil {
			return target, err
		}
	default:
		return target, sql.ErrNoRows
	}
	var raw, archivedAt sql.NullString
	var runtimeStatus string
	err := s.db.QueryRowContext(ctx, `SELECT a.name,a.data,a.archived_at,a.runtime_id,COALESCE(r.status,'offline')
		FROM agents a LEFT JOIN runtimes r ON r.id=a.runtime_id AND r.workspace_id=a.workspace_id
		WHERE a.id=? AND a.workspace_id=?`, target.AgentID, workspaceID).
		Scan(&target.Name, &raw, &archivedAt, &target.RuntimeID, &runtimeStatus)
	if err != nil {
		return target, err
	}
	if raw.Valid {
		data := decodeObject(raw.String)
		target.AvatarURL = data["avatar_url"]
	}
	target.Ready = !archivedAt.Valid && target.RuntimeID != "" && runtimeStatus == "online"
	return target, nil
}

func triggerBlocked(mention util.Mention, reason string) map[string]any {
	return map[string]any{"target_type": mention.Type, "target_id": mention.ID, "status": "blocked", "reason_code": reason}
}

func (s *Server) previewCommentTriggers(w http.ResponseWriter, r *http.Request) {
	ws, err := s.workspaceFor(r)
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	issueID := chi.URLParam(r, "id")
	if _, err := s.store.GetIssue(r.Context(), ws.ID, issueID); err != nil {
		s.respondStoreError(w, err)
		return
	}
	var input struct {
		Content string `json:"content"`
	}
	if err := decodeBody(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	agents := []map[string]any{}
	blocked := []map[string]any{}
	seenAgents := map[string]bool{}
	for _, mention := range util.ParseMentions(input.Content) {
		if mention.Type != "agent" && mention.Type != "squad" {
			continue
		}
		target, err := s.store.resolveMentionTarget(r.Context(), ws.ID, mention)
		if err != nil {
			blocked = append(blocked, triggerBlocked(mention, "target_unavailable"))
			continue
		}
		if !target.Ready {
			blocked = append(blocked, triggerBlocked(mention, "runtime_offline"))
			continue
		}
		if seenAgents[target.AgentID] {
			continue
		}
		seenAgents[target.AgentID] = true
		source, reason := "mention_agent", "This mention will trigger this agent."
		if mention.Type == "squad" {
			source, reason = "mention_squad_leader", "This mention will trigger the squad leader."
		}
		agents = append(agents, map[string]any{"id": target.AgentID, "name": target.Name, "avatar_url": target.AvatarURL, "source": source, "reason": reason})
	}
	writeJSON(w, http.StatusOK, map[string]any{"agents": agents, "blocked": blocked})
}

func (s *Store) enqueueMentionRun(ctx context.Context, workspaceID, issueID string, target localTriggerTarget, commentID string) (string, error) {
	if !target.Ready {
		return "blocked", nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback() }()
	var active int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM task_runs WHERE issue_id=? AND agent_id=? AND status IN ('queued','dispatched','waiting_local_directory','running','waiting_previous_run')`, issueID, target.AgentID).Scan(&active); err != nil {
		return "", err
	}
	var sessionID, stageID string
	if err := tx.QueryRowContext(ctx, `SELECT id,active_stage_id FROM issue_sessions WHERE workspace_id=? AND issue_id=?`, workspaceID, issueID).Scan(&sessionID, &stageID); err != nil {
		return "", err
	}
	var priorTaskID sql.NullString
	err = tx.QueryRowContext(ctx, `SELECT id FROM task_runs WHERE issue_id=? AND agent_id=? AND NULLIF(TRIM(thread_id),'') IS NOT NULL AND NULLIF(TRIM(work_dir),'') IS NOT NULL ORDER BY created_at DESC,id DESC LIMIT 1`, issueID, target.AgentID).Scan(&priorTaskID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	continuity, runStatus := "fresh", "queued"
	if priorTaskID.Valid {
		continuity = "pending"
	}
	if active > 0 {
		// A claimed task cannot absorb a newly posted comment. Persist a
		// follow-up instead of reporting a false coalesced success; terminal task
		// reconciliation promotes waiting runs one at a time.
		runStatus = "waiting_previous_run"
		continuity = "pending"
	}
	stamp := now()
	_, err = tx.ExecContext(ctx, `INSERT INTO task_runs(id,issue_id,status,runtime,session_id,stage_id,prior_task_id,trigger_comment_id,continuity_state,agent_id,squad_id,created_at,updated_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`, uuid.NewString(), issueID, runStatus, target.RuntimeID, sessionID, stageID, nullable(priorTaskID.String), commentID, continuity, target.AgentID, nullable(target.SquadID), stamp, stamp)
	if err != nil {
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	if runStatus == "waiting_previous_run" {
		return "deferred", nil
	}
	return "queued", nil
}

func (s *Server) dispatchCommentMentions(ctx context.Context, workspaceID, issueID, commentID, content string, suppressed []string) ([]map[string]any, error) {
	skip := map[string]bool{}
	for _, id := range suppressed {
		skip[strings.TrimSpace(id)] = true
	}
	outcomes := []map[string]any{}
	seenTargets := map[string]bool{}
	resultsByAgent := map[string]map[string]any{}
	for _, mention := range util.ParseMentions(content) {
		if mention.Type != "agent" && mention.Type != "squad" {
			continue
		}
		key := mention.Type + ":" + mention.ID
		if seenTargets[key] {
			continue
		}
		seenTargets[key] = true
		target, err := s.store.resolveMentionTarget(ctx, workspaceID, mention)
		if err != nil {
			outcomes = append(outcomes, triggerBlocked(mention, "target_unavailable"))
			continue
		}
		if skip[target.AgentID] {
			outcomes = append(outcomes, triggerBlocked(mention, "suppressed"))
			continue
		}
		if !target.Ready {
			outcomes = append(outcomes, triggerBlocked(mention, "runtime_offline"))
			continue
		}
		if prior, exists := resultsByAgent[target.AgentID]; exists {
			outcomes = append(outcomes, map[string]any{"target_type": mention.Type, "target_id": mention.ID, "status": "coalesced", "reason_code": "same_execution_target", "task_status": prior["status"]})
			continue
		}
		status, err := s.store.enqueueMentionRun(ctx, workspaceID, issueID, target, commentID)
		if err != nil {
			outcomes = append(outcomes, triggerBlocked(mention, "internal_error"))
			continue
		}
		reason := "mention"
		if status == "deferred" {
			reason = "already_pending"
		}
		outcome := map[string]any{"target_type": mention.Type, "target_id": mention.ID, "status": status, "reason_code": reason}
		resultsByAgent[target.AgentID] = outcome
		outcomes = append(outcomes, outcome)
	}
	return outcomes, nil
}

func (s *Server) previewIssueTrigger(w http.ResponseWriter, r *http.Request) {
	ws, err := s.workspaceFor(r)
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	var input struct {
		IssueIDs     []string `json:"issue_ids"`
		IsCreate     bool     `json:"is_create"`
		AssigneeType *string  `json:"assignee_type"`
		AssigneeID   *string  `json:"assignee_id"`
		Status       *string  `json:"status"`
	}
	if err := decodeBody(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	triggers := []map[string]any{}
	evaluate := func(issueID string, oldStatus, oldAssigneeType, oldAssigneeID, assigneeType, assigneeID, status string, isCreate bool) {
		category := status
		_ = s.store.db.QueryRowContext(r.Context(), `SELECT category FROM issue_statuses WHERE workspace_id=? AND key=?`, ws.ID, status).Scan(&category)
		oldCategory := oldStatus
		_ = s.store.db.QueryRowContext(r.Context(), `SELECT category FROM issue_statuses WHERE workspace_id=? AND key=?`, ws.ID, oldStatus).Scan(&oldCategory)
		source := ""
		assignmentChanged := isCreate || (input.AssigneeType != nil && input.AssigneeID != nil && (oldAssigneeType != assigneeType || oldAssigneeID != assigneeID))
		if assignmentChanged {
			if category == "backlog" || category == "done" || category == "cancelled" {
				return
			}
			source = "assign"
		} else if input.Status != nil && oldCategory == "backlog" && category != "backlog" && category != "done" && category != "cancelled" {
			source = "status"
		} else {
			return
		}
		target, err := s.store.resolveMentionTarget(r.Context(), ws.ID, util.Mention{Type: assigneeType, ID: assigneeID})
		if err != nil || !target.Ready {
			return
		}
		if source == "status" && issueID != "" {
			var active int
			if err := s.store.db.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM task_runs WHERE issue_id=? AND agent_id=? AND status IN ('queued','dispatched','waiting_local_directory','running','waiting_previous_run')`, issueID, target.AgentID).Scan(&active); err != nil || active > 0 {
				return
			}
		}
		triggers = append(triggers, map[string]any{"issue_id": issueID, "agent_id": target.AgentID, "source": source, "handoff_supported": true})
	}
	if input.IsCreate {
		status := "todo"
		if input.Status != nil && *input.Status != "" {
			status = *input.Status
		}
		if input.AssigneeType != nil && input.AssigneeID != nil {
			evaluate("", "", "", "", *input.AssigneeType, *input.AssigneeID, status, true)
		}
	} else {
		for _, id := range input.IssueIDs {
			issue, err := s.store.GetIssue(r.Context(), ws.ID, id)
			if err != nil {
				continue
			}
			assigneeType, assigneeID, status := "", "", issue.Status
			if issue.AssigneeType != nil {
				assigneeType = *issue.AssigneeType
			}
			if issue.AssigneeID != nil {
				assigneeID = *issue.AssigneeID
			}
			oldAssigneeType, oldAssigneeID := assigneeType, assigneeID
			if input.AssigneeType != nil {
				assigneeType = *input.AssigneeType
			}
			if input.AssigneeID != nil {
				assigneeID = *input.AssigneeID
			}
			if input.Status != nil {
				status = *input.Status
			}
			evaluate(issue.ID, issue.Status, oldAssigneeType, oldAssigneeID, assigneeType, assigneeID, status, false)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"triggers": triggers, "total_count": len(triggers)})
}
