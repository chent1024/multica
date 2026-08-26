package localworkbench

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
)

const (
	initialSessionStage  = "analysis"
	migratedRunStage     = "implementation"
	maxHandoffItems      = 20
	maxHandoffItemRunes  = 1000
	maxHandoffObjective  = 4000
	maxSessionRequestKey = 200
)

var sessionStageKinds = map[string]struct{}{
	"analysis": {}, "implementation": {}, "review": {}, "test": {},
	"commit": {}, "push": {}, "release": {},
}

var ErrInvalidIssueHandoff = errors.New("invalid issue handoff")

var (
	ErrIssueSessionNotRunnable        = errors.New("issue session is not runnable")
	ErrIssueSessionRuntimeUnavailable = errors.New("local Codex runtime is not online")
)

type IssueSessionMessageResult struct {
	Issue   Issue
	Comment Comment
	TaskID  string
	Outcome string
	Created bool
}

// SendIssueSessionMessage atomically records one user-visible turn and its
// corresponding run. A second call with the same request key reads the first
// result back instead of creating either half twice.
func (s *Store) SendIssueSessionMessage(ctx context.Context, workspaceID, issueID, requestKey, content string, attachmentIDs []string) (IssueSessionMessageResult, error) {
	requestKey, content = strings.TrimSpace(requestKey), strings.TrimSpace(content)
	if requestKey == "" || len(requestKey) > maxSessionRequestKey {
		return IssueSessionMessageResult{}, fmt.Errorf("%w: request key is required and bounded", ErrIssueSessionNotRunnable)
	}
	if content == "" {
		return IssueSessionMessageResult{}, fmt.Errorf("%w: content is required", ErrIssueSessionNotRunnable)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return IssueSessionMessageResult{}, fmt.Errorf("beginning issue session message: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var existingTaskID, existingCommentID, existingStatus string
	err = tx.QueryRowContext(ctx, `SELECT tr.id, tr.trigger_comment_id, tr.status FROM task_runs tr JOIN issues i ON i.id=tr.issue_id WHERE tr.request_key=? AND tr.issue_id=? AND i.workspace_id=?`, requestKey, issueID, workspaceID).Scan(&existingTaskID, &existingCommentID, &existingStatus)
	if err == nil {
		if err := tx.Commit(); err != nil {
			return IssueSessionMessageResult{}, err
		}
		return s.readIssueSessionMessageResult(ctx, workspaceID, issueID, existingTaskID, existingCommentID, sessionMessageOutcome(existingStatus), false)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return IssueSessionMessageResult{}, fmt.Errorf("checking issue session request identity: %w", err)
	}

	var statusCategory string
	var assigneeType, assigneeID, projectID sql.NullString
	err = tx.QueryRowContext(ctx, `SELECT COALESCE(st.category, i.status), i.assignee_type, i.assignee_id, i.project_id
		FROM issues i LEFT JOIN issue_statuses st ON st.workspace_id=i.workspace_id AND st.key=i.status
		WHERE i.workspace_id=? AND i.id=?`, workspaceID, issueID).Scan(&statusCategory, &assigneeType, &assigneeID, &projectID)
	if err != nil {
		return IssueSessionMessageResult{}, fmt.Errorf("reading issue for session message: %w", err)
	}
	if !projectID.Valid || strings.TrimSpace(projectID.String) == "" {
		return IssueSessionMessageResult{}, ErrIssueProjectRequired
	}
	if statusCategory == "done" || statusCategory == "cancelled" {
		return IssueSessionMessageResult{}, fmt.Errorf("%w: finished issue", ErrIssueSessionNotRunnable)
	}
	if assigneeType.Valid || assigneeID.Valid {
		if !assigneeType.Valid || !assigneeID.Valid || assigneeType.String != "agent" || assigneeID.String != LocalAgentID {
			return IssueSessionMessageResult{}, fmt.Errorf("%w: issue is assigned to another actor", ErrIssueSessionNotRunnable)
		}
	} else if statusCategory != "backlog" {
		return IssueSessionMessageResult{}, fmt.Errorf("%w: only an unassigned backlog issue can be claimed", ErrIssueSessionNotRunnable)
	}

	var runtimeID string
	if err := tx.QueryRowContext(ctx, `SELECT id FROM runtimes WHERE workspace_id=? AND provider='codex' AND status='online' ORDER BY updated_at DESC LIMIT 1`, workspaceID).Scan(&runtimeID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return IssueSessionMessageResult{}, ErrIssueSessionRuntimeUnavailable
		}
		return IssueSessionMessageResult{}, fmt.Errorf("reading Codex runtime for session message: %w", err)
	}

	var sessionID, stageID string
	if err := tx.QueryRowContext(ctx, `SELECT id, active_stage_id FROM issue_sessions WHERE workspace_id=? AND issue_id=?`, workspaceID, issueID).Scan(&sessionID, &stageID); err != nil {
		return IssueSessionMessageResult{}, fmt.Errorf("reading issue session for message: %w", err)
	}
	stamp := now()
	if !assigneeType.Valid && !assigneeID.Valid {
		if _, err := tx.ExecContext(ctx, `UPDATE issues SET status='in_progress', assignee_type='agent', assignee_id=?, revision=revision+1, updated_at=?, last_activity_at=? WHERE workspace_id=? AND id=?`, LocalAgentID, stamp, stamp, workspaceID, issueID); err != nil {
			return IssueSessionMessageResult{}, fmt.Errorf("aligning parked issue for session message: %w", err)
		}
	}

	// Recover a waiting turn left behind by a process interruption before
	// accepting another one, then decide whether this turn can be claimed now.
	if _, err := promoteWaitingIssueRunTx(ctx, tx, issueID, stamp); err != nil {
		return IssueSessionMessageResult{}, fmt.Errorf("recovering waiting issue run: %w", err)
	}
	var activeTaskID string
	err = tx.QueryRowContext(ctx, `SELECT id FROM task_runs WHERE issue_id=? AND status IN ('queued','dispatched','waiting_local_directory','running') ORDER BY created_at LIMIT 1`, issueID).Scan(&activeTaskID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return IssueSessionMessageResult{}, fmt.Errorf("checking active issue run: %w", err)
	}

	var revision int64
	if err := tx.QueryRowContext(ctx, `UPDATE issues SET revision=revision+1, updated_at=?, last_activity_at=? WHERE id=? RETURNING revision`, stamp, stamp, issueID).Scan(&revision); err != nil {
		return IssueSessionMessageResult{}, fmt.Errorf("updating issue for session message: %w", err)
	}
	commentID := uuid.NewString()
	if _, err := tx.ExecContext(ctx, `INSERT INTO comments(id, issue_id, author_type, author_id, content, type, issue_revision, created_at, updated_at) VALUES (?, ?, 'system', ?, ?, 'comment', ?, ?, ?)`, commentID, issueID, LocalActorID, content, revision, stamp, stamp); err != nil {
		return IssueSessionMessageResult{}, fmt.Errorf("recording issue session message: %w", err)
	}
	if len(attachmentIDs) > 0 {
		if err := bindAttachmentsTx(ctx, tx, workspaceID, issueID, commentID, attachmentIDs); err != nil {
			return IssueSessionMessageResult{}, fmt.Errorf("binding issue session message attachments: %w", err)
		}
	}

	taskID, runStatus := uuid.NewString(), "queued"
	var priorTaskID sql.NullString
	continuityState, continuityReason := "fresh", ""
	if activeTaskID != "" {
		runStatus = "waiting_previous_run"
		continuityState = "pending"
	} else {
		continuityState, continuityReason, err = latestRunContinuityTx(ctx, tx, issueID, taskID, &priorTaskID)
		if err != nil {
			return IssueSessionMessageResult{}, err
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO task_runs(id, issue_id, status, runtime, session_id, stage_id, prior_task_id, trigger_comment_id, request_key, continuity_state, continuity_reason, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, taskID, issueID, runStatus, runtimeID, sessionID, stageID, nullable(priorTaskID.String), commentID, requestKey, continuityState, nullable(continuityReason), stamp, stamp); err != nil {
		return IssueSessionMessageResult{}, fmt.Errorf("queueing issue session message run: %w", err)
	}
	if runStatus == "queued" {
		if _, err := tx.ExecContext(ctx, `UPDATE issue_sessions SET continuity_state=?, continuity_reason=?, updated_at=? WHERE id=?`, continuityState, nullable(continuityReason), stamp, sessionID); err != nil {
			return IssueSessionMessageResult{}, fmt.Errorf("updating issue session continuity: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return IssueSessionMessageResult{}, fmt.Errorf("committing issue session message: %w", err)
	}
	return s.readIssueSessionMessageResult(ctx, workspaceID, issueID, taskID, commentID, sessionMessageOutcome(runStatus), true)
}

func sessionMessageOutcome(status string) string {
	if status == "waiting_previous_run" {
		return "waiting"
	}
	return "queued"
}

func (s *Store) readIssueSessionMessageResult(ctx context.Context, workspaceID, issueID, taskID, commentID, outcome string, created bool) (IssueSessionMessageResult, error) {
	issue, err := s.GetIssue(ctx, workspaceID, issueID)
	if err != nil {
		return IssueSessionMessageResult{}, err
	}
	comment, err := scanComment(s.db.QueryRowContext(ctx, `SELECT id, issue_id, author_type, author_id, content, type, parent_id, created_at, updated_at, revision, issue_revision, resolved_at, resolved_by_type, resolved_by_id, source_task_id, quick_action_id FROM comments WHERE id=?`, commentID))
	if err != nil {
		return IssueSessionMessageResult{}, err
	}
	attachments, err := s.listAttachments(ctx, workspaceID, "", commentID)
	if err != nil {
		return IssueSessionMessageResult{}, err
	}
	for index := range attachments {
		attachments[index] = attachmentWithURLs(attachments[index])
	}
	comment.Attachments = attachments
	return IssueSessionMessageResult{Issue: issue, Comment: comment, TaskID: taskID, Outcome: outcome, Created: created}, nil
}

func latestRunContinuityTx(ctx context.Context, tx *sql.Tx, issueID, excludeTaskID string, priorTaskID *sql.NullString) (string, string, error) {
	var historyCount int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM task_runs WHERE issue_id=? AND id<>?`, issueID, excludeTaskID).Scan(&historyCount); err != nil {
		return "", "", fmt.Errorf("counting prior issue runs: %w", err)
	}
	err := tx.QueryRowContext(ctx, `SELECT id FROM task_runs WHERE issue_id=? AND id<>? AND NULLIF(TRIM(thread_id),'') IS NOT NULL AND NULLIF(TRIM(work_dir),'') IS NOT NULL ORDER BY created_at DESC LIMIT 1`, issueID, excludeTaskID).Scan(priorTaskID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", "", fmt.Errorf("reading prior issue continuation pointer: %w", err)
	}
	if priorTaskID.Valid {
		return "pending", "", nil
	}
	if historyCount > 0 {
		return "gap", "missing_resume_pointer", nil
	}
	return "fresh", "", nil
}

func promoteWaitingIssueRunTx(ctx context.Context, tx *sql.Tx, issueID, stamp string) (string, error) {
	var active int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM task_runs WHERE issue_id=? AND status IN ('queued','dispatched','waiting_local_directory','running')`, issueID).Scan(&active); err != nil || active > 0 {
		return "", err
	}
	var taskID, sessionID string
	err := tx.QueryRowContext(ctx, `SELECT id, session_id FROM task_runs WHERE issue_id=? AND status='waiting_previous_run' ORDER BY created_at LIMIT 1`, issueID).Scan(&taskID, &sessionID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	var priorTaskID sql.NullString
	state, reason, err := latestRunContinuityTx(ctx, tx, issueID, taskID, &priorTaskID)
	if err != nil {
		return "", err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE task_runs SET status='queued', prior_task_id=?, continuity_state=?, continuity_reason=?, updated_at=? WHERE id=? AND status='waiting_previous_run'`, nullable(priorTaskID.String), state, nullable(reason), stamp, taskID); err != nil {
		return "", err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE issue_sessions SET continuity_state=?, continuity_reason=?, updated_at=? WHERE id=?`, state, nullable(reason), stamp, sessionID); err != nil {
		return "", err
	}
	return taskID, nil
}

func isSessionStageKind(kind string) bool {
	_, ok := sessionStageKinds[kind]
	return ok
}

type IssueSessionStage struct {
	ID          string  `json:"id"`
	SessionID   string  `json:"session_id"`
	Kind        string  `json:"kind"`
	Status      string  `json:"status"`
	Position    int64   `json:"position"`
	Migrated    bool    `json:"migrated"`
	StartedAt   string  `json:"started_at"`
	CompletedAt *string `json:"completed_at"`
}

type IssueHandoffInput struct {
	Objective          string   `json:"objective"`
	ConfirmedFacts     []string `json:"confirmed_facts"`
	Decisions          []string `json:"decisions"`
	Constraints        []string `json:"constraints"`
	AcceptanceCriteria []string `json:"acceptance_criteria"`
	References         []string `json:"references"`
	OpenQuestions      []string `json:"open_questions"`
	SourceRunID        string   `json:"source_run_id,omitempty"`
}

type IssueHandoff struct {
	ID                 string   `json:"id"`
	ChildIssueID       string   `json:"child_issue_id"`
	ParentIssueID      string   `json:"parent_issue_id"`
	ParentSessionID    string   `json:"parent_session_id"`
	SourceStageID      string   `json:"source_stage_id"`
	SourceStageKind    string   `json:"source_stage_kind"`
	SourceRunID        *string  `json:"source_run_id"`
	Objective          string   `json:"objective"`
	ConfirmedFacts     []string `json:"confirmed_facts"`
	Decisions          []string `json:"decisions"`
	Constraints        []string `json:"constraints"`
	AcceptanceCriteria []string `json:"acceptance_criteria"`
	References         []string `json:"references"`
	OpenQuestions      []string `json:"open_questions"`
	CreatedAt          string   `json:"created_at"`
}

// ContinueIssueSession returns the active run when one already exists or
// atomically aligns an unassigned parked issue and queues one new run against
// the current session stage. It deliberately never activates a stage.
func (s *Store) ContinueIssueSession(ctx context.Context, workspaceID, issueID string) (Issue, string, string, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Issue{}, "", "", fmt.Errorf("beginning issue session continuation: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var status, statusCategory string
	var assigneeType, assigneeID, projectID sql.NullString
	err = tx.QueryRowContext(ctx, `SELECT i.status, COALESCE(st.category, i.status), i.assignee_type, i.assignee_id, i.project_id
		FROM issues i LEFT JOIN issue_statuses st ON st.workspace_id=i.workspace_id AND st.key=i.status
		WHERE i.workspace_id=? AND i.id=?`, workspaceID, issueID).Scan(&status, &statusCategory, &assigneeType, &assigneeID, &projectID)
	if err != nil {
		return Issue{}, "", "", fmt.Errorf("reading issue for session continuation: %w", err)
	}
	if !projectID.Valid || strings.TrimSpace(projectID.String) == "" {
		return Issue{}, "", "", ErrIssueProjectRequired
	}
	if statusCategory == "done" || statusCategory == "cancelled" {
		return Issue{}, "", "", fmt.Errorf("%w: finished issue", ErrIssueSessionNotRunnable)
	}
	if assigneeType.Valid || assigneeID.Valid {
		if !assigneeType.Valid || !assigneeID.Valid || assigneeType.String != "agent" || assigneeID.String != LocalAgentID {
			return Issue{}, "", "", fmt.Errorf("%w: issue is assigned to another actor", ErrIssueSessionNotRunnable)
		}
	} else if statusCategory != "backlog" {
		return Issue{}, "", "", fmt.Errorf("%w: only an unassigned backlog issue can be claimed", ErrIssueSessionNotRunnable)
	}

	var activeTaskID string
	err = tx.QueryRowContext(ctx, `SELECT id FROM task_runs WHERE issue_id=? AND status IN ('queued','dispatched','waiting_local_directory','running','waiting_previous_run') ORDER BY CASE WHEN status='waiting_previous_run' THEN 1 ELSE 0 END, created_at DESC LIMIT 1`, issueID).Scan(&activeTaskID)
	if err == nil {
		if err := tx.Commit(); err != nil {
			return Issue{}, "", "", fmt.Errorf("committing active issue session continuation: %w", err)
		}
		issue, err := s.GetIssue(ctx, workspaceID, issueID)
		if err != nil {
			return Issue{}, "", "", fmt.Errorf("reading continued issue: %w", err)
		}
		return issue, activeTaskID, "active", nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Issue{}, "", "", fmt.Errorf("checking active issue run: %w", err)
	}

	var runtimeID string
	if err := tx.QueryRowContext(ctx, `SELECT id FROM runtimes WHERE workspace_id=? AND provider='codex' AND status='online' ORDER BY updated_at DESC LIMIT 1`, workspaceID).Scan(&runtimeID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Issue{}, "", "", ErrIssueSessionRuntimeUnavailable
		}
		return Issue{}, "", "", fmt.Errorf("reading Codex runtime for session continuation: %w", err)
	}

	stamp := now()
	if !assigneeType.Valid && !assigneeID.Valid {
		status = "in_progress"
		if _, err := tx.ExecContext(ctx, `UPDATE issues SET status=?, assignee_type='agent', assignee_id=?, revision=revision+1, updated_at=?, last_activity_at=? WHERE workspace_id=? AND id=?`, status, LocalAgentID, stamp, stamp, workspaceID, issueID); err != nil {
			return Issue{}, "", "", fmt.Errorf("aligning parked issue for session continuation: %w", err)
		}
	}

	var sessionID, stageID string
	if err := tx.QueryRowContext(ctx, `SELECT s.id, s.active_stage_id FROM issue_sessions s JOIN issue_session_stages st ON st.id=s.active_stage_id WHERE s.workspace_id=? AND s.issue_id=?`, workspaceID, issueID).Scan(&sessionID, &stageID); err != nil {
		return Issue{}, "", "", fmt.Errorf("reading issue session for continuation: %w", err)
	}
	var historyCount int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM task_runs WHERE issue_id=?`, issueID).Scan(&historyCount); err != nil {
		return Issue{}, "", "", fmt.Errorf("counting prior issue runs: %w", err)
	}
	var priorTaskID sql.NullString
	err = tx.QueryRowContext(ctx, `SELECT id FROM task_runs WHERE issue_id=? AND NULLIF(TRIM(thread_id),'') IS NOT NULL AND NULLIF(TRIM(work_dir),'') IS NOT NULL ORDER BY created_at DESC LIMIT 1`, issueID).Scan(&priorTaskID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return Issue{}, "", "", fmt.Errorf("reading prior issue continuation pointer: %w", err)
	}
	continuityState, continuityReason := "fresh", ""
	if priorTaskID.Valid {
		continuityState = "pending"
	} else if historyCount > 0 {
		continuityState = "gap"
		continuityReason = "missing_resume_pointer"
	}
	taskID := uuid.NewString()
	if _, err := tx.ExecContext(ctx, `INSERT INTO task_runs(id, issue_id, status, runtime, session_id, stage_id, prior_task_id, continuity_state, continuity_reason, created_at, updated_at) VALUES (?, ?, 'queued', ?, ?, ?, ?, ?, ?, ?, ?)`, taskID, issueID, runtimeID, sessionID, stageID, nullable(priorTaskID.String), continuityState, nullable(continuityReason), stamp, stamp); err != nil {
		return Issue{}, "", "", fmt.Errorf("queueing issue session run: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE issue_sessions SET continuity_state=?, continuity_reason=?, updated_at=? WHERE id=?`, continuityState, nullable(continuityReason), stamp, sessionID); err != nil {
		return Issue{}, "", "", fmt.Errorf("updating issue session continuity: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Issue{}, "", "", fmt.Errorf("committing issue session continuation: %w", err)
	}
	issue, err := s.GetIssue(ctx, workspaceID, issueID)
	if err != nil {
		return Issue{}, "", "", fmt.Errorf("reading continued issue: %w", err)
	}
	return issue, taskID, "queued", nil
}

type IssueSession struct {
	ID               string              `json:"id"`
	IssueID          string              `json:"issue_id"`
	WorkspaceID      string              `json:"workspace_id"`
	ActiveStageID    string              `json:"active_stage_id"`
	ContinuityState  string              `json:"continuity_state"`
	ContinuityReason *string             `json:"continuity_reason"`
	CreatedAt        string              `json:"created_at"`
	UpdatedAt        string              `json:"updated_at"`
	Stages           []IssueSessionStage `json:"stages"`
	Handoff          *IssueHandoff       `json:"handoff"`
}

func createIssueSessionTx(ctx context.Context, tx *sql.Tx, workspaceID, issueID, kind string, migrated bool) (string, string, error) {
	if _, ok := sessionStageKinds[kind]; !ok {
		return "", "", fmt.Errorf("invalid session stage kind: %s", kind)
	}
	stamp := now()
	sessionID, stageID := uuid.NewString(), uuid.NewString()
	if _, err := tx.ExecContext(ctx, `INSERT INTO issue_sessions(id, issue_id, workspace_id, active_stage_id, continuity_state, created_at, updated_at) VALUES (?, ?, ?, ?, 'fresh', ?, ?)`, sessionID, issueID, workspaceID, stageID, stamp, stamp); err != nil {
		return "", "", fmt.Errorf("creating issue session: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO issue_session_stages(id, session_id, kind, status, position, migrated, started_at) VALUES (?, ?, ?, 'active', 0, ?, ?)`, stageID, sessionID, kind, migrated, stamp); err != nil {
		return "", "", fmt.Errorf("creating initial issue session stage: %w", err)
	}
	return sessionID, stageID, nil
}

func validateHandoff(input IssueHandoffInput) error {
	if strings.TrimSpace(input.Objective) == "" {
		return fmt.Errorf("%w: objective is required", ErrInvalidIssueHandoff)
	}
	if len([]rune(input.Objective)) > maxHandoffObjective {
		return fmt.Errorf("%w: objective is too long", ErrInvalidIssueHandoff)
	}
	sections := [][]string{input.ConfirmedFacts, input.Decisions, input.Constraints, input.AcceptanceCriteria, input.References, input.OpenQuestions}
	for _, items := range sections {
		if len(items) > maxHandoffItems {
			return fmt.Errorf("%w: section has too many items", ErrInvalidIssueHandoff)
		}
		for _, item := range items {
			if strings.TrimSpace(item) == "" || len([]rune(item)) > maxHandoffItemRunes {
				return fmt.Errorf("%w: items must be non-empty and bounded", ErrInvalidIssueHandoff)
			}
		}
	}
	return nil
}

func createIssueHandoffTx(ctx context.Context, tx *sql.Tx, workspaceID, childIssueID, parentIssueID string, input IssueHandoffInput) error {
	if err := validateHandoff(input); err != nil {
		return err
	}
	var parentSessionID, sourceStageID string
	if err := tx.QueryRowContext(ctx, `SELECT id, active_stage_id FROM issue_sessions WHERE workspace_id=? AND issue_id=?`, workspaceID, parentIssueID).Scan(&parentSessionID, &sourceStageID); err != nil {
		return fmt.Errorf("reading parent issue session: %w", err)
	}
	if input.SourceRunID != "" {
		var runStageID sql.NullString
		if err := tx.QueryRowContext(ctx, `SELECT stage_id FROM task_runs WHERE id=? AND issue_id=? AND session_id=?`, input.SourceRunID, parentIssueID, parentSessionID).Scan(&runStageID); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("%w: source run does not belong to parent session", ErrInvalidIssueHandoff)
			}
			return fmt.Errorf("checking handoff source run: %w", err)
		}
		if !runStageID.Valid || strings.TrimSpace(runStageID.String) == "" {
			return fmt.Errorf("%w: source run has no session stage", ErrInvalidIssueHandoff)
		}
		sourceStageID = runStageID.String
	}
	encode := func(items []string) (string, error) {
		if items == nil {
			items = []string{}
		}
		raw, err := json.Marshal(items)
		return string(raw), err
	}
	facts, err := encode(input.ConfirmedFacts)
	if err != nil {
		return err
	}
	decisions, err := encode(input.Decisions)
	if err != nil {
		return err
	}
	constraints, err := encode(input.Constraints)
	if err != nil {
		return err
	}
	criteria, err := encode(input.AcceptanceCriteria)
	if err != nil {
		return err
	}
	references, err := encode(input.References)
	if err != nil {
		return err
	}
	questions, err := encode(input.OpenQuestions)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO issue_handoffs(id, child_issue_id, parent_issue_id, parent_session_id, source_stage_id, source_run_id, objective, confirmed_facts, decisions, constraints, acceptance_criteria, references_json, open_questions, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, uuid.NewString(), childIssueID, parentIssueID, parentSessionID, sourceStageID, nullable(input.SourceRunID), strings.TrimSpace(input.Objective), facts, decisions, constraints, criteria, references, questions, now())
	if err != nil {
		return fmt.Errorf("creating issue handoff: %w", err)
	}
	return nil
}

func (s *Store) GetIssueSession(ctx context.Context, workspaceID, issueID string) (IssueSession, error) {
	var session IssueSession
	var continuityReason sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT id, issue_id, workspace_id, active_stage_id, continuity_state, continuity_reason, created_at, updated_at FROM issue_sessions WHERE workspace_id=? AND issue_id=?`, workspaceID, issueID).Scan(&session.ID, &session.IssueID, &session.WorkspaceID, &session.ActiveStageID, &session.ContinuityState, &continuityReason, &session.CreatedAt, &session.UpdatedAt)
	if err != nil {
		return IssueSession{}, err
	}
	session.ContinuityReason = stringPtr(continuityReason)
	rows, err := s.db.QueryContext(ctx, `SELECT id, session_id, kind, status, position, migrated, started_at, completed_at FROM issue_session_stages WHERE session_id=? ORDER BY position`, session.ID)
	if err != nil {
		return IssueSession{}, err
	}
	defer rows.Close()
	session.Stages = []IssueSessionStage{}
	for rows.Next() {
		var stage IssueSessionStage
		var migrated int
		var completedAt sql.NullString
		if err := rows.Scan(&stage.ID, &stage.SessionID, &stage.Kind, &stage.Status, &stage.Position, &migrated, &stage.StartedAt, &completedAt); err != nil {
			return IssueSession{}, err
		}
		stage.Migrated = migrated != 0
		stage.CompletedAt = stringPtr(completedAt)
		session.Stages = append(session.Stages, stage)
	}
	if err := rows.Err(); err != nil {
		return IssueSession{}, err
	}
	handoff, err := s.getIssueHandoff(ctx, issueID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return IssueSession{}, err
	}
	if err == nil {
		session.Handoff = &handoff
	}
	return session, nil
}

func (s *Store) ActivateIssueSessionStage(ctx context.Context, workspaceID, issueID, kind string) (IssueSession, error) {
	if _, ok := sessionStageKinds[kind]; !ok {
		return IssueSession{}, fmt.Errorf("invalid session stage kind: %s", kind)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return IssueSession{}, err
	}
	defer func() { _ = tx.Rollback() }()
	var sessionID, activeStageID, activeKind string
	if err := tx.QueryRowContext(ctx, `SELECT s.id, s.active_stage_id, st.kind FROM issue_sessions s JOIN issue_session_stages st ON st.id=s.active_stage_id WHERE s.workspace_id=? AND s.issue_id=?`, workspaceID, issueID).Scan(&sessionID, &activeStageID, &activeKind); err != nil {
		return IssueSession{}, err
	}
	if activeKind != kind {
		stamp := now()
		if _, err := tx.ExecContext(ctx, `UPDATE issue_session_stages SET status='completed', completed_at=? WHERE id=? AND session_id=? AND status='active'`, stamp, activeStageID, sessionID); err != nil {
			return IssueSession{}, err
		}
		var position int64
		if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(position), -1)+1 FROM issue_session_stages WHERE session_id=?`, sessionID).Scan(&position); err != nil {
			return IssueSession{}, err
		}
		stageID := uuid.NewString()
		if _, err := tx.ExecContext(ctx, `INSERT INTO issue_session_stages(id, session_id, kind, status, position, migrated, started_at) VALUES (?, ?, ?, 'active', ?, 0, ?)`, stageID, sessionID, kind, position, stamp); err != nil {
			return IssueSession{}, err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE issue_sessions SET active_stage_id=?, updated_at=? WHERE id=?`, stageID, stamp, sessionID); err != nil {
			return IssueSession{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return IssueSession{}, err
	}
	return s.GetIssueSession(ctx, workspaceID, issueID)
}

func (s *Store) getIssueHandoff(ctx context.Context, childIssueID string) (IssueHandoff, error) {
	var handoff IssueHandoff
	var sourceRunID sql.NullString
	var facts, decisions, constraints, criteria, references, questions string
	err := s.db.QueryRowContext(ctx, `SELECT h.id, h.child_issue_id, h.parent_issue_id, h.parent_session_id, h.source_stage_id, st.kind, h.source_run_id, h.objective, h.confirmed_facts, h.decisions, h.constraints, h.acceptance_criteria, h.references_json, h.open_questions, h.created_at FROM issue_handoffs h JOIN issue_session_stages st ON st.id=h.source_stage_id WHERE h.child_issue_id=?`, childIssueID).Scan(&handoff.ID, &handoff.ChildIssueID, &handoff.ParentIssueID, &handoff.ParentSessionID, &handoff.SourceStageID, &handoff.SourceStageKind, &sourceRunID, &handoff.Objective, &facts, &decisions, &constraints, &criteria, &references, &questions, &handoff.CreatedAt)
	if err != nil {
		return IssueHandoff{}, err
	}
	handoff.SourceRunID = stringPtr(sourceRunID)
	decode := func(raw string, target *[]string) error { return json.Unmarshal([]byte(raw), target) }
	if err := decode(facts, &handoff.ConfirmedFacts); err != nil {
		return IssueHandoff{}, err
	}
	if err := decode(decisions, &handoff.Decisions); err != nil {
		return IssueHandoff{}, err
	}
	if err := decode(constraints, &handoff.Constraints); err != nil {
		return IssueHandoff{}, err
	}
	if err := decode(criteria, &handoff.AcceptanceCriteria); err != nil {
		return IssueHandoff{}, err
	}
	if err := decode(references, &handoff.References); err != nil {
		return IssueHandoff{}, err
	}
	if err := decode(questions, &handoff.OpenQuestions); err != nil {
		return IssueHandoff{}, err
	}
	return handoff, nil
}
