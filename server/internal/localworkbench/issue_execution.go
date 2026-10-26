package localworkbench

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/google/uuid"
)

var ErrLocalAgentRuntimeUnavailable = errors.New("target agent runtime is unavailable")

// All new local automation entry points share the existing issue session and
// task queue. Provider sessions are resumed only for the same executing agent.
func enqueueLocalIssueAgentTaskTx(ctx context.Context, tx *sql.Tx, workspaceID, issueID, agentID, note, wakeupID string, at time.Time) (string, error) {
	var runtimeID, projectID, category, status string
	err := tx.QueryRowContext(ctx, `SELECT a.runtime_id,i.project_id,COALESCE(st.category,i.status),i.status FROM agents a JOIN runtimes r ON r.id=a.runtime_id JOIN issues i ON i.id=? AND i.workspace_id=a.workspace_id LEFT JOIN issue_statuses st ON st.workspace_id=i.workspace_id AND st.key=i.status WHERE a.workspace_id=? AND a.id=? AND a.archived_at IS NULL AND r.status='online'`, issueID, workspaceID, agentID).Scan(&runtimeID, &projectID, &category, &status)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrLocalAgentRuntimeUnavailable
	}
	if err != nil {
		return "", err
	}
	if projectID == "" {
		return "", ErrIssueProjectRequired
	}
	if localStatusCategory(category) == "closed" || category == "done" {
		return "", errors.New("issue is terminal")
	}
	var sessionID, stageID string
	err = tx.QueryRowContext(ctx, `SELECT id,active_stage_id FROM issue_sessions WHERE workspace_id=? AND issue_id=?`, workspaceID, issueID).Scan(&sessionID, &stageID)
	if errors.Is(err, sql.ErrNoRows) {
		sessionID, stageID, err = createIssueSessionTx(ctx, tx, workspaceID, issueID, initialSessionStage, false)
	}
	if err != nil {
		return "", err
	}
	prior := sql.NullString{}
	var history int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM task_runs WHERE issue_id=? AND agent_id=?`, issueID, agentID).Scan(&history); err != nil {
		return "", err
	}
	err = tx.QueryRowContext(ctx, `SELECT id FROM task_runs WHERE issue_id=? AND agent_id=? AND NULLIF(TRIM(thread_id),'') IS NOT NULL AND NULLIF(TRIM(work_dir),'') IS NOT NULL ORDER BY created_at DESC,id DESC LIMIT 1`, issueID, agentID).Scan(&prior)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	continuity, reason := "fresh", ""
	if prior.Valid {
		continuity = "pending"
	} else if history > 0 {
		continuity, reason = "gap", "missing_resume_pointer"
	}
	var squadID sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT CASE WHEN i.assignee_type='squad' AND EXISTS(SELECT 1 FROM squads s WHERE s.id=i.assignee_id AND s.workspace_id=i.workspace_id AND s.leader_id=?) THEN i.assignee_id END FROM issues i WHERE i.id=? AND i.workspace_id=?`, agentID, issueID, workspaceID).Scan(&squadID); err != nil {
		return "", err
	}
	id, stamp := uuid.NewString(), at.Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, `INSERT INTO task_runs(id,issue_id,status,runtime,handoff_note,session_id,stage_id,prior_task_id,continuity_state,continuity_reason,agent_id,wakeup_id,squad_id,created_at,updated_at) VALUES(?,?,'queued',?,?,?,?,?,?,?,?,?,?,?,?)`, id, issueID, runtimeID, note, sessionID, stageID, prior, continuity, nullable(reason), agentID, nullable(wakeupID), squadID, stamp, stamp); err != nil {
		return "", err
	}
	return id, nil
}
