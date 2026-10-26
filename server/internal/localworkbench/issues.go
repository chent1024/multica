package localworkbench

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"strings"
	"time"

	"github.com/google/uuid"
)

const (
	LocalActorID = LocalOperatorID
	LocalAgentID = "00000000-0000-4000-8000-000000000003"
)

type Issue struct {
	workflowTaskID     string
	workflowCancelled  []string
	ID                 string                  `json:"id"`
	WorkspaceID        string                  `json:"workspace_id"`
	Number             int64                   `json:"number"`
	Identifier         string                  `json:"identifier"`
	Title              string                  `json:"title"`
	Description        *string                 `json:"description"`
	Status             string                  `json:"status"`
	StatusCategory     string                  `json:"status_category,omitempty"`
	Priority           string                  `json:"priority"`
	AssigneeType       *string                 `json:"assignee_type"`
	AssigneeID         *string                 `json:"assignee_id"`
	CreatorType        string                  `json:"creator_type"`
	CreatorID          string                  `json:"creator_id"`
	ParentIssueID      *string                 `json:"parent_issue_id"`
	DuplicateOfIssueID *string                 `json:"duplicate_of_issue_id,omitempty"`
	DuplicateOf        *localIssueDuplicateRef `json:"duplicate_of"`
	ProjectID          *string                 `json:"project_id"`
	Position           float64                 `json:"position"`
	Stage              *int64                  `json:"stage"`
	StartDate          *string                 `json:"start_date"`
	DueDate            *string                 `json:"due_date"`
	Metadata           map[string]any          `json:"metadata"`
	Properties         map[string]any          `json:"properties"`
	Reactions          []any                   `json:"reactions"`
	Labels             []any                   `json:"labels"`
	CreatedAt          string                  `json:"created_at"`
	UpdatedAt          string                  `json:"updated_at"`
	Revision           int64                   `json:"revision"`
	LastActivityAt     *string                 `json:"last_activity_at"`
	SourceContext      map[string]any          `json:"source_context,omitempty"`
}

type Comment struct {
	ID              string           `json:"id"`
	IssueID         string           `json:"issue_id"`
	AuthorType      string           `json:"author_type"`
	AuthorID        string           `json:"author_id"`
	Content         string           `json:"content"`
	Type            string           `json:"type"`
	ParentID        *string          `json:"parent_id"`
	Reactions       []any            `json:"reactions"`
	Attachments     []Attachment     `json:"attachments"`
	CreatedAt       string           `json:"created_at"`
	UpdatedAt       string           `json:"updated_at"`
	Revision        int64            `json:"revision"`
	IssueRevision   int64            `json:"issue_revision"`
	ResolvedAt      *string          `json:"resolved_at"`
	ResolvedByType  *string          `json:"resolved_by_type"`
	ResolvedByID    *string          `json:"resolved_by_id"`
	SourceTaskID    *string          `json:"source_task_id"`
	QuickActionID   *string          `json:"quick_action_id"`
	TriggerOutcomes []map[string]any `json:"trigger_outcomes,omitempty"`
}

func now() string { return time.Now().UTC().Format(time.RFC3339Nano) }

func nullable(v string) any {
	if strings.TrimSpace(v) == "" {
		return nil
	}
	return v
}

func decodeObject(raw string) map[string]any {
	result := map[string]any{}
	_ = json.Unmarshal([]byte(raw), &result)
	if result == nil {
		return map[string]any{}
	}
	return result
}

func decodeArray(raw string) []any {
	result := []any{}
	_ = json.Unmarshal([]byte(raw), &result)
	return result
}

func stringPtr(v sql.NullString) *string {
	if !v.Valid {
		return nil
	}
	return &v.String
}

func intPtr(v sql.NullInt64) *int64 {
	if !v.Valid {
		return nil
	}
	return &v.Int64
}

func (s *Store) ListIssues(ctx context.Context, workspaceID, projectID, parentID string) ([]Issue, error) {
	query := `SELECT i.id, i.workspace_id, i.number, w.issue_prefix, i.title, i.description, i.status,
		COALESCE(s.category, i.status), i.priority, i.assignee_type, i.assignee_id, i.creator_type, i.creator_id,
		i.parent_issue_id, i.project_id, i.position, i.stage, i.start_date, i.due_date, i.metadata, i.properties,
		i.created_at, i.updated_at, i.revision, i.last_activity_at, ` + localIssueDuplicateSelect + `
		FROM issues i JOIN workspaces w ON w.id=i.workspace_id
		LEFT JOIN issue_statuses s ON s.workspace_id=i.workspace_id AND s.key=i.status
		WHERE i.workspace_id=?`
	args := []any{workspaceID}
	if projectID != "" {
		query += " AND i.project_id=?"
		args = append(args, projectID)
	}
	if parentID != "" {
		query += " AND i.parent_issue_id=?"
		args = append(args, parentID)
	}
	query += " ORDER BY i.position, i.updated_at DESC"
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []Issue{}
	for rows.Next() {
		issue, err := scanIssue(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, issue)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := s.hydrateIssueDecorations(ctx, workspaceID, result); err != nil {
		return nil, err
	}
	return result, nil
}

func (s *Store) GetIssue(ctx context.Context, workspaceID, value string) (Issue, error) {
	query := `SELECT i.id, i.workspace_id, i.number, w.issue_prefix, i.title, i.description, i.status,
		COALESCE(s.category, i.status), i.priority, i.assignee_type, i.assignee_id, i.creator_type, i.creator_id,
		i.parent_issue_id, i.project_id, i.position, i.stage, i.start_date, i.due_date, i.metadata, i.properties,
		i.created_at, i.updated_at, i.revision, i.last_activity_at, ` + localIssueDuplicateSelect + `
		FROM issues i JOIN workspaces w ON w.id=i.workspace_id
		LEFT JOIN issue_statuses s ON s.workspace_id=i.workspace_id AND s.key=i.status
		WHERE i.workspace_id=? AND (i.id=? OR upper(w.issue_prefix || '-' || i.number)=upper(?)) LIMIT 1`
	issue, err := scanIssue(s.db.QueryRowContext(ctx, query, workspaceID, value, value))
	if err != nil {
		return Issue{}, err
	}
	items := []Issue{issue}
	if err := s.hydrateIssueDecorations(ctx, workspaceID, items); err != nil {
		return Issue{}, err
	}
	return items[0], nil
}

func (s *Store) hydrateIssueDecorations(ctx context.Context, workspaceID string, issues []Issue) error {
	for index := range issues {
		labels, err := s.resourceLabelsFor(ctx, workspaceID, "issue", issues[index].ID)
		if err != nil {
			return err
		}
		reactions, err := s.reactionsFor(ctx, workspaceID, "issue", issues[index].ID)
		if err != nil {
			return err
		}
		issues[index].Labels, issues[index].Reactions = labels, reactions
		if pointer := issues[index].DuplicateOfIssueID; pointer != nil {
			var ref localIssueDuplicateRef
			err := s.db.QueryRowContext(ctx, `SELECT i.id,w.issue_prefix||'-'||i.number,i.title,i.status FROM issues i JOIN workspaces w ON w.id=i.workspace_id WHERE i.id=? AND i.workspace_id=?`, *pointer, workspaceID).Scan(&ref.ID, &ref.Identifier, &ref.Title, &ref.Status)
			if errors.Is(err, sql.ErrNoRows) {
				issues[index].DuplicateOfIssueID = nil
			} else if err != nil {
				return err
			} else {
				issues[index].DuplicateOf = &ref
			}
		}
	}
	return nil
}

func (s *Store) resourceLabelsFor(ctx context.Context, workspaceID, resourceType, resourceID string) ([]any, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT l.id,l.workspace_id,l.resource_type,l.name,l.description,l.color,l.created_at,l.updated_at,(SELECT COUNT(*) FROM resource_labels x WHERE x.label_id=l.id) FROM resource_labels rl JOIN labels l ON l.id=rl.label_id WHERE rl.workspace_id=? AND rl.resource_type=? AND rl.resource_id=? ORDER BY l.name,l.id`, workspaceID, resourceType, resourceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []any{}
	for rows.Next() {
		item, err := scanLabel(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func (s *Store) reactionsFor(ctx context.Context, workspaceID, resourceType, resourceID string) ([]any, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT actor_type,actor_id,emoji,created_at FROM reactions WHERE workspace_id=? AND resource_type=? AND resource_id=? ORDER BY created_at,actor_type,actor_id,emoji`, workspaceID, resourceType, resourceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []any{}
	for rows.Next() {
		var actorType, actorID, emoji, createdAt string
		if err := rows.Scan(&actorType, &actorID, &emoji, &createdAt); err != nil {
			return nil, err
		}
		item := map[string]any{"id": resourceType + ":" + resourceID + ":" + actorType + ":" + actorID + ":" + emoji, "actor_type": actorType, "actor_id": actorID, "emoji": emoji, "created_at": createdAt}
		item[resourceType+"_id"] = resourceID
		result = append(result, item)
	}
	return result, rows.Err()
}

func scanIssue(row rowScanner) (Issue, error) {
	var i Issue
	var prefix, metadata, properties string
	var description, assigneeType, assigneeID, parentID, projectID, startDate, dueDate, lastActivity, duplicateOf sql.NullString
	var stage sql.NullInt64
	err := row.Scan(&i.ID, &i.WorkspaceID, &i.Number, &prefix, &i.Title, &description, &i.Status, &i.StatusCategory, &i.Priority, &assigneeType, &assigneeID, &i.CreatorType, &i.CreatorID, &parentID, &projectID, &i.Position, &stage, &startDate, &dueDate, &metadata, &properties, &i.CreatedAt, &i.UpdatedAt, &i.Revision, &lastActivity, &duplicateOf)
	i.Identifier = fmt.Sprintf("%s-%d", prefix, i.Number)
	i.Description, i.AssigneeType, i.AssigneeID = stringPtr(description), stringPtr(assigneeType), stringPtr(assigneeID)
	i.ParentIssueID, i.ProjectID, i.StartDate, i.DueDate = stringPtr(parentID), stringPtr(projectID), stringPtr(startDate), stringPtr(dueDate)
	i.Stage, i.LastActivityAt = intPtr(stage), stringPtr(lastActivity)
	i.DuplicateOfIssueID = stringPtr(duplicateOf)
	i.Metadata, i.Properties, i.Reactions, i.Labels = decodeObject(metadata), decodeObject(properties), []any{}, []any{}
	return i, err
}

type CreateIssueInput struct {
	SuppressRun   bool                `json:"suppress_run,omitempty"`
	Title         string              `json:"title"`
	Description   string              `json:"description"`
	Status        string              `json:"status"`
	Priority      string              `json:"priority"`
	AssigneeType  *string             `json:"assignee_type"`
	AssigneeID    *string             `json:"assignee_id"`
	ProjectID     *string             `json:"project_id"`
	ParentIssueID *string             `json:"parent_issue_id"`
	Stage         *int64              `json:"stage"`
	StartDate     *string             `json:"start_date"`
	DueDate       *string             `json:"due_date"`
	Metadata      map[string]any      `json:"metadata"`
	AttachmentIDs []string            `json:"attachment_ids"`
	LabelIDs      []string            `json:"label_ids"`
	Handoff       *IssueHandoffInput  `json:"handoff,omitempty"`
	SourceContext *IssueSourceContext `json:"-"`
}

type IssueSourceContext struct {
	ID              string
	SourceIssueID   string
	AnchorCommentID string
	CaptureToken    string
	Snapshot        string
}

var (
	ErrIssueProjectRequired  = errors.New("issue project is required")
	ErrIssueProjectImmutable = errors.New("issue project cannot be changed")
	ErrIssueProjectMismatch  = errors.New("child issue project must match parent")
	ErrIssueRevisionConflict = errors.New("issue was updated elsewhere")
	ErrInvalidIssueRevision  = errors.New("expected_revision must be a positive integer")
)

func (s *Store) CreateIssue(ctx context.Context, workspaceID string, input CreateIssueInput) (Issue, error) {
	if input.Handoff != nil {
		if input.ParentIssueID == nil || strings.TrimSpace(*input.ParentIssueID) == "" {
			return Issue{}, fmt.Errorf("%w: parent issue is required", ErrInvalidIssueHandoff)
		}
		if err := validateHandoff(*input.Handoff); err != nil {
			return Issue{}, err
		}
	}
	if input.Stage != nil && *input.Stage < 1 {
		return Issue{}, errors.New("issue stage must be at least 1")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Issue{}, err
	}
	defer func() { _ = tx.Rollback() }()

	if input.ParentIssueID != nil {
		var parentProjectID sql.NullString
		if err := tx.QueryRowContext(ctx, `SELECT i.project_id
			FROM issues i JOIN workspaces w ON w.id=i.workspace_id
			WHERE i.workspace_id=? AND (i.id=? OR upper(w.issue_prefix || '-' || i.number)=upper(?)) LIMIT 1`, workspaceID, *input.ParentIssueID, *input.ParentIssueID).Scan(&parentProjectID); err != nil {
			return Issue{}, err
		}
		if !parentProjectID.Valid || strings.TrimSpace(parentProjectID.String) == "" {
			return Issue{}, fmt.Errorf("%w: parent task has no project", ErrIssueProjectRequired)
		}
		if input.ProjectID == nil || strings.TrimSpace(*input.ProjectID) == "" {
			projectID := parentProjectID.String
			input.ProjectID = &projectID
		} else if strings.TrimSpace(*input.ProjectID) != parentProjectID.String {
			return Issue{}, ErrIssueProjectMismatch
		}
	}
	if input.ProjectID == nil || strings.TrimSpace(*input.ProjectID) == "" {
		return Issue{}, ErrIssueProjectRequired
	}
	projectID := strings.TrimSpace(*input.ProjectID)
	input.ProjectID = &projectID
	var projectExists int
	if err := tx.QueryRowContext(ctx, `SELECT 1 FROM projects WHERE workspace_id=? AND id=?`, workspaceID, projectID).Scan(&projectExists); err != nil {
		return Issue{}, err
	}
	var number int64
	if err := tx.QueryRowContext(ctx, "SELECT COALESCE(MAX(number), 0)+1 FROM issues WHERE workspace_id=?", workspaceID).Scan(&number); err != nil {
		return Issue{}, err
	}
	var position float64
	if err := tx.QueryRowContext(ctx, "SELECT COALESCE(MAX(position), -1)+1 FROM issues WHERE workspace_id=?", workspaceID).Scan(&position); err != nil {
		return Issue{}, err
	}
	workflowEntry, err := prepareLocalIssueWorkflowCreateTx(ctx, tx, workspaceID, &input)
	if err != nil {
		return Issue{}, err
	}
	if input.Status == "" {
		input.Status = "backlog"
	}
	if input.Priority == "" {
		input.Priority = "none"
	}
	if input.Metadata == nil {
		input.Metadata = map[string]any{}
	}
	metadata, _ := json.Marshal(input.Metadata)
	stamp, id := now(), uuid.NewString()
	_, err = tx.ExecContext(ctx, `INSERT INTO issues(id, workspace_id, number, title, description, status, priority, assignee_type, assignee_id, creator_type, creator_id, parent_issue_id, project_id, position, stage, start_date, due_date, metadata, last_activity_at, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 'system', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, id, workspaceID, number, input.Title, nullable(input.Description), input.Status, input.Priority, input.AssigneeType, input.AssigneeID, LocalActorID, input.ParentIssueID, input.ProjectID, position, input.Stage, input.StartDate, input.DueDate, string(metadata), stamp, stamp, stamp)
	if err != nil {
		return Issue{}, err
	}
	if _, _, err := createIssueSessionTx(ctx, tx, workspaceID, id, initialSessionStage, false); err != nil {
		return Issue{}, err
	}
	if input.Handoff != nil {
		if err := createIssueHandoffTx(ctx, tx, workspaceID, id, *input.ParentIssueID, *input.Handoff); err != nil {
			return Issue{}, err
		}
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO activities(id, issue_id, actor_type, actor_id, action, details, created_at) VALUES (?, ?, 'system', ?, 'created', '{}', ?)`, uuid.NewString(), id, LocalActorID, stamp)
	if err != nil {
		return Issue{}, err
	}
	if err := bindAttachmentsTx(ctx, tx, workspaceID, id, "", input.AttachmentIDs); err != nil {
		return Issue{}, err
	}
	for _, labelID := range input.LabelIDs {
		var valid int
		if err := tx.QueryRowContext(ctx, `SELECT 1 FROM labels WHERE id=? AND workspace_id=? AND resource_type='issue'`, labelID, workspaceID).Scan(&valid); err != nil {
			return Issue{}, err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO resource_labels(workspace_id,resource_type,resource_id,label_id,created_at) VALUES(?,'issue',?,?,?) ON CONFLICT DO NOTHING`, workspaceID, id, labelID, stamp); err != nil {
			return Issue{}, err
		}
	}
	if input.SourceContext != nil {
		contextID := input.SourceContext.ID
		if contextID == "" {
			contextID = uuid.NewString()
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO issue_source_contexts(id,issue_id,source_issue_id,anchor_comment_id,capture_token,snapshot,state,created_at,updated_at) VALUES(?,?,?,?,?,?,'attached',?,?)`, contextID, id, input.SourceContext.SourceIssueID, input.SourceContext.AnchorCommentID, input.SourceContext.CaptureToken, input.SourceContext.Snapshot, stamp, stamp); err != nil {
			return Issue{}, err
		}
	}
	workflowTaskID := ""
	if workflowEntry != nil && workflowEntry.AgentID != "" && !input.SuppressRun {
		workflowTaskID, err = enqueueLocalIssueAgentTaskTx(ctx, tx, workspaceID, id, workflowEntry.AgentID, workflowEntry.Brief, "", time.Now().UTC())
		if err != nil {
			if errors.Is(err, ErrLocalAgentRuntimeUnavailable) {
				return Issue{}, workflowError(409, "workflow_runtime_unavailable", err.Error())
			}
			return Issue{}, err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE task_runs SET workflow_id=?,workflow_step=? WHERE id=?`, workflowEntry.WorkflowID, workflowEntry.StatusKey, workflowTaskID); err != nil {
			return Issue{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return Issue{}, err
	}
	record, err := s.GetIssue(ctx, workspaceID, id)
	record.workflowTaskID = workflowTaskID
	return record, err
}

func (s *Store) UpdateIssue(ctx context.Context, workspaceID, id string, updates map[string]any) (Issue, error) {
	updates = maps.Clone(updates)
	if updates == nil {
		updates = map[string]any{}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Issue{}, err
	}
	defer func() { _ = tx.Rollback() }()
	cursor, origin, err := beginLocalWakeupMutation(ctx, tx, workspaceID)
	if err != nil {
		return Issue{}, err
	}

	var currentID, currentStatus string
	var currentRevision int64
	var currentProjectID, currentParentIssueID, currentDuplicateOf sql.NullString
	err = tx.QueryRowContext(ctx, `SELECT i.id, i.project_id, i.parent_issue_id, i.status, i.duplicate_of_issue_id,i.revision
		FROM issues i JOIN workspaces w ON w.id=i.workspace_id
		WHERE i.workspace_id=? AND (i.id=? OR upper(w.issue_prefix || '-' || i.number)=upper(?)) LIMIT 1`, workspaceID, id, id).Scan(&currentID, &currentProjectID, &currentParentIssueID, &currentStatus, &currentDuplicateOf, &currentRevision)
	if err != nil {
		return Issue{}, err
	}

	if raw, present := updates["expected_revision"]; present {
		var revision int64
		valid := true
		switch v := raw.(type) {
		case float64:
			revision = int64(v)
			valid = v == float64(revision)
		case int:
			revision = int64(v)
		case int64:
			revision = v
		case json.Number:
			var parseErr error
			revision, parseErr = v.Int64()
			valid = parseErr == nil
		default:
			valid = false
		}
		if !valid || revision < 1 {
			return Issue{}, ErrInvalidIssueRevision
		}
		if revision != currentRevision {
			return Issue{}, ErrIssueRevisionConflict
		}
	}
	markingDuplicate, duplicateNoop, err := prepareLocalDuplicateMark(ctx, tx, workspaceID, currentID, currentStatus, currentDuplicateOf, updates)
	if err != nil {
		return Issue{}, err
	}
	if duplicateNoop {
		if err := commitLocalWakeupMutation(ctx, tx, workspaceID, cursor, origin); err != nil {
			return Issue{}, err
		}
		return s.GetIssue(ctx, workspaceID, currentID)
	}
	finalProjectID := strings.TrimSpace(currentProjectID.String)
	repairingProject := false
	set := []string{"updated_at=?", "last_activity_at=?", "revision=revision+1"}
	stamp := now()
	args := []any{stamp, stamp}
	if value, exists := updates["project_id"]; exists {
		projectID, ok := value.(string)
		projectID = strings.TrimSpace(projectID)
		if !ok || projectID == "" {
			return Issue{}, ErrIssueProjectRequired
		}
		updates["project_id"] = projectID
		if currentProjectID.Valid {
			if finalProjectID != projectID {
				return Issue{}, ErrIssueProjectImmutable
			}
		} else {
			var projectExists int
			if err := tx.QueryRowContext(ctx, `SELECT 1 FROM projects WHERE workspace_id=? AND id=?`, workspaceID, projectID).Scan(&projectExists); err != nil {
				return Issue{}, err
			}
			if currentParentIssueID.Valid {
				var parentProjectID sql.NullString
				if err := tx.QueryRowContext(ctx, `SELECT project_id FROM issues WHERE workspace_id=? AND id=?`, workspaceID, currentParentIssueID.String).Scan(&parentProjectID); err != nil {
					return Issue{}, err
				}
				if !parentProjectID.Valid || strings.TrimSpace(parentProjectID.String) != projectID {
					return Issue{}, ErrIssueProjectMismatch
				}
			}
			repairingProject = true
			finalProjectID = projectID
		}
	}
	if requestsRun(updates) && !markingDuplicate && finalProjectID == "" {
		return Issue{}, ErrIssueProjectRequired
	}
	if value, ok := updates["parent_issue_id"].(string); ok && value != "" {
		if value == currentID {
			return Issue{}, errors.New("issue cannot be its own parent")
		}
		var parentProjectID sql.NullString
		if err := tx.QueryRowContext(ctx, `SELECT i.project_id
			FROM issues i JOIN workspaces w ON w.id=i.workspace_id
			WHERE i.workspace_id=? AND (i.id=? OR upper(w.issue_prefix || '-' || i.number)=upper(?)) LIMIT 1`, workspaceID, value, value).Scan(&parentProjectID); err != nil {
			return Issue{}, err
		}
		if finalProjectID == "" || !parentProjectID.Valid || finalProjectID != strings.TrimSpace(parentProjectID.String) {
			return Issue{}, ErrIssueProjectMismatch
		}
	}
	updates["_workflow_source_task_id"] = origin.TaskID
	workflowEntry, err := applyLocalIssueWorkflowEntryTx(ctx, tx, workspaceID, currentID, updates)
	if err != nil {
		return Issue{}, err
	}
	allowed := map[string]bool{"title": true, "description": true, "status": true, "priority": true, "assignee_type": true, "assignee_id": true, "project_id": true, "parent_issue_id": true, "start_date": true, "due_date": true, "position": true, "stage": true, "metadata": true, "properties": true, "duplicate_of_issue_id": true}
	for key, value := range updates {
		if !allowed[key] {
			continue
		}
		if key == "metadata" || key == "properties" {
			encoded, _ := json.Marshal(value)
			value = string(encoded)
		}
		set = append(set, key+"=?")
		args = append(args, value)
	}

	if status, requested := updates["status"]; requested && !markingDuplicate {
		if status != "cancelled" || currentStatus != "cancelled" {
			set = append(set, "duplicate_of_issue_id=NULL")
		}
	}
	args = append(args, currentID)
	where := "id=?"
	if repairingProject {
		where += " AND project_id IS NULL"
	}
	result, err := tx.ExecContext(ctx, "UPDATE issues SET "+strings.Join(set, ", ")+" WHERE "+where, args...)
	if err != nil {
		return Issue{}, err
	}
	if repairingProject {
		updated, err := result.RowsAffected()
		if err != nil {
			return Issue{}, err
		}
		if updated != 1 {
			return Issue{}, ErrIssueProjectImmutable
		}
	}

	if status, requested := updates["status"].(string); requested {
		var category string
		if err := tx.QueryRowContext(ctx, `SELECT category FROM issue_statuses WHERE workspace_id=? AND key=?`, workspaceID, status).Scan(&category); err != nil && !errors.Is(err, sql.ErrNoRows) {
			return Issue{}, err
		}
		if category == "" {
			category = status
		}
		if category == "done" || localStatusCategory(category) == "closed" {
			if err := endLocalIssueWakeupsTx(ctx, tx, workspaceID, currentID, stamp); err != nil {
				return Issue{}, err
			}
		}
	}
	if rawIDs, ok := updates["attachment_ids"]; ok {
		ids, err := attachmentIDs(rawIDs)
		if err != nil {
			return Issue{}, err
		}
		if err := bindAttachmentsTx(ctx, tx, workspaceID, currentID, "", ids); err != nil {
			return Issue{}, err
		}
	}
	workflowTaskID := ""
	suppressRun, _ := updates["suppress_run"].(bool)
	if workflowEntry != nil && workflowEntry.AgentID != "" && !suppressRun {
		workflowTaskID, err = enqueueLocalIssueAgentTaskTx(ctx, tx, workspaceID, currentID, workflowEntry.AgentID, workflowEntry.Brief, "", time.Now().UTC())
		if err != nil {
			if errors.Is(err, ErrLocalAgentRuntimeUnavailable) {
				return Issue{}, workflowError(409, "workflow_runtime_unavailable", err.Error())
			}
			return Issue{}, err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE task_runs SET workflow_id=?,workflow_step=? WHERE id=?`, workflowEntry.WorkflowID, workflowEntry.StatusKey, workflowTaskID); err != nil {
			return Issue{}, err
		}
	}
	if origin.TaskID != "" {
		if status, ok := updates["status"].(string); ok {
			if _, err := tx.ExecContext(ctx, `UPDATE task_runs SET workflow_step=? WHERE id=? AND workflow_id IS NOT NULL`, status, origin.TaskID); err != nil {
				return Issue{}, err
			}
		}
	}
	if err := commitLocalWakeupMutation(ctx, tx, workspaceID, cursor, origin); err != nil {
		return Issue{}, err
	}
	record, err := s.GetIssue(ctx, workspaceID, currentID)
	record.workflowTaskID = workflowTaskID
	if workflowEntry != nil {
		record.workflowCancelled = workflowEntry.CancelledTaskIDs
	}
	return record, err
}

func attachmentIDs(value any) ([]string, error) {
	values, ok := value.([]any)
	if !ok {
		if strings, ok := value.([]string); ok {
			return strings, nil
		}
		return nil, errors.New("attachment_ids must be an array")
	}
	ids := make([]string, 0, len(values))
	for _, value := range values {
		id, ok := value.(string)
		if !ok || strings.TrimSpace(id) == "" {
			return nil, errors.New("attachment_ids must contain non-empty strings")
		}
		ids = append(ids, id)
	}
	return ids, nil
}

func (s *Store) DeleteIssue(ctx context.Context, workspaceID, id string) error {
	issue, err := s.GetIssue(ctx, workspaceID, id)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := deleteLocalWakeupsForIssueTx(ctx, tx, workspaceID, issue.ID); err != nil {
		return err
	}

	if _, err := tx.ExecContext(ctx, `UPDATE issues SET duplicate_of_issue_id=NULL,updated_at=?,last_activity_at=?,revision=revision+1 WHERE workspace_id=? AND duplicate_of_issue_id=?`, now(), now(), workspaceID, issue.ID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM issue_handoffs WHERE child_issue_id=? OR parent_issue_id=?`, issue.ID, issue.ID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM issue_source_contexts WHERE issue_id=?`, issue.ID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM resource_labels WHERE workspace_id=? AND resource_type='issue' AND resource_id=?`, workspaceID, issue.ID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM reactions WHERE workspace_id=? AND ((resource_type='issue' AND resource_id=?) OR (resource_type='comment' AND resource_id IN (SELECT id FROM comments WHERE issue_id=?)))`, workspaceID, issue.ID, issue.ID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM issue_subscribers WHERE issue_id=?`, issue.ID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM issue_vcs_pull_requests WHERE issue_id=?`, issue.ID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM inbox_items WHERE workspace_id=? AND issue_id=?`, workspaceID, issue.ID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE issues SET parent_issue_id=NULL, updated_at=?, revision=revision+1 WHERE workspace_id=? AND parent_issue_id=?`, now(), workspaceID, issue.ID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM issue_session_stages WHERE session_id IN (SELECT id FROM issue_sessions WHERE issue_id=?)`, issue.ID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM issue_sessions WHERE issue_id=?`, issue.ID); err != nil {
		return err
	}
	for _, query := range []string{"DELETE FROM attachments WHERE issue_id=?", "DELETE FROM comments WHERE issue_id=?", "DELETE FROM activities WHERE issue_id=?", "DELETE FROM task_messages WHERE issue_id=?", "DELETE FROM task_run_usage WHERE task_id IN (SELECT id FROM task_runs WHERE issue_id=?)", "DELETE FROM task_runs WHERE issue_id=?", "DELETE FROM issues WHERE id=?"} {
		if _, err := tx.ExecContext(ctx, query, issue.ID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) ListComments(ctx context.Context, workspaceID, issueID string) ([]Comment, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, issue_id, author_type, author_id, content, type, parent_id, created_at, updated_at, revision, issue_revision, resolved_at, resolved_by_type, resolved_by_id, source_task_id, quick_action_id FROM comments WHERE issue_id=? ORDER BY created_at`, issueID)
	if err != nil {
		return nil, err
	}
	result := []Comment{}
	for rows.Next() {
		c, err := scanComment(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, c)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := s.hydrateCommentAttachments(ctx, workspaceID, result); err != nil {
		return nil, err
	}
	for index := range result {
		reactions, err := s.reactionsFor(ctx, workspaceID, "comment", result[index].ID)
		if err != nil {
			return nil, err
		}
		result[index].Reactions = reactions
	}
	return result, nil
}

func scanComment(row rowScanner) (Comment, error) {
	var c Comment
	var parent, resolvedAt, resolvedType, resolvedID, sourceTask, quickAction sql.NullString
	err := row.Scan(&c.ID, &c.IssueID, &c.AuthorType, &c.AuthorID, &c.Content, &c.Type, &parent, &c.CreatedAt, &c.UpdatedAt, &c.Revision, &c.IssueRevision, &resolvedAt, &resolvedType, &resolvedID, &sourceTask, &quickAction)
	c.ParentID, c.ResolvedAt, c.ResolvedByType, c.ResolvedByID = stringPtr(parent), stringPtr(resolvedAt), stringPtr(resolvedType), stringPtr(resolvedID)
	c.SourceTaskID, c.QuickActionID, c.Reactions, c.Attachments = stringPtr(sourceTask), stringPtr(quickAction), []any{}, []Attachment{}
	return c, err
}

func (s *Store) CreateComment(ctx context.Context, issueID, content, kind, parentID string) (Comment, error) {
	return s.createComment(ctx, "", issueID, content, kind, parentID, "system", LocalActorID, "", nil)
}

func (s *Store) CreateCommentWithAttachments(ctx context.Context, workspaceID, issueID, content, kind, parentID string, attachmentIDs []string) (Comment, error) {
	return s.createComment(ctx, workspaceID, issueID, content, kind, parentID, "system", LocalActorID, "", attachmentIDs)
}

func (s *Store) createAgentCommentWithAttachments(ctx context.Context, workspaceID, issueID, content, kind, parentID, taskID string, attachmentIDs []string) (Comment, error) {
	return s.createComment(ctx, workspaceID, issueID, content, kind, parentID, "agent", LocalAgentID, taskID, attachmentIDs)
}

func (s *Store) createComment(ctx context.Context, workspaceID, issueID, content, kind, parentID, authorType, authorID, sourceTaskID string, attachmentIDs []string) (Comment, error) {
	if kind == "" {
		kind = "comment"
	}
	stamp, id := now(), uuid.NewString()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Comment{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if workspaceID == "" {
		if err := tx.QueryRowContext(ctx, `SELECT workspace_id FROM issues WHERE id=?`, issueID).Scan(&workspaceID); err != nil {
			return Comment{}, err
		}
	}

	cursor, origin, err := beginLocalWakeupMutation(ctx, tx, workspaceID)
	if err != nil {
		return Comment{}, err
	}

	if authorType == "agent" {
		origin = localWakeupOrigin{AgentID: authorID, TaskID: sourceTaskID}
	}

	var revision int64
	if err := tx.QueryRowContext(ctx, "UPDATE issues SET revision=revision+1, updated_at=?, last_activity_at=? WHERE id=? RETURNING revision", stamp, stamp, issueID).Scan(&revision); err != nil {
		return Comment{}, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO comments(id, issue_id, author_type, author_id, content, type, parent_id, issue_revision, source_task_id, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, id, issueID, authorType, authorID, content, kind, nullable(parentID), revision, nullable(sourceTaskID), stamp, stamp)
	if err != nil {
		return Comment{}, err
	}
	if len(attachmentIDs) > 0 {
		if workspaceID == "" {
			return Comment{}, errors.New("workspace is required to bind attachments")
		}
		if err := bindAttachmentsTx(ctx, tx, workspaceID, issueID, id, attachmentIDs); err != nil {
			return Comment{}, err
		}
	}
	if err := commitLocalWakeupMutation(ctx, tx, workspaceID, cursor, origin); err != nil {
		return Comment{}, err
	}
	comment, err := scanComment(s.db.QueryRowContext(ctx, `SELECT id, issue_id, author_type, author_id, content, type, parent_id, created_at, updated_at, revision, issue_revision, resolved_at, resolved_by_type, resolved_by_id, source_task_id, quick_action_id FROM comments WHERE id=?`, id))
	if err != nil {
		return Comment{}, err
	}
	if workspaceID != "" {
		attachments, err := s.listAttachments(ctx, workspaceID, "", id)
		if err != nil {
			return Comment{}, err
		}
		for index := range attachments {
			attachments[index] = attachmentWithURLs(attachments[index])
		}
		comment.Attachments = attachments
	}
	return comment, nil
}

func (s *Store) hydrateCommentAttachments(ctx context.Context, workspaceID string, comments []Comment) error {
	for index := range comments {
		attachments, err := s.listAttachments(ctx, workspaceID, "", comments[index].ID)
		if err != nil {
			return err
		}
		for attachmentIndex := range attachments {
			attachments[attachmentIndex] = attachmentWithURLs(attachments[attachmentIndex])
		}
		comments[index].Attachments = attachments
	}
	return nil
}

func (s *Store) setCommentResolved(ctx context.Context, workspaceID, commentID string, resolved bool) (Comment, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Comment{}, err
	}
	defer func() { _ = tx.Rollback() }()
	cursor, origin, err := beginLocalWakeupMutation(ctx, tx, workspaceID)
	if err != nil {
		return Comment{}, err
	}

	stamp := now()
	var issueID string
	if err := tx.QueryRowContext(ctx, `SELECT c.issue_id FROM comments c JOIN issues i ON i.id=c.issue_id WHERE c.id=? AND i.workspace_id=?`, commentID, workspaceID).Scan(&issueID); err != nil {
		return Comment{}, err
	}
	var revision int64
	if err := tx.QueryRowContext(ctx, `UPDATE issues SET revision=revision+1, updated_at=?, last_activity_at=? WHERE id=? RETURNING revision`, stamp, stamp, issueID).Scan(&revision); err != nil {
		return Comment{}, err
	}
	if resolved {
		_, err = tx.ExecContext(ctx, `UPDATE comments SET resolved_at=?, resolved_by_type='system', resolved_by_id=?, revision=revision+1, issue_revision=?, updated_at=? WHERE id=?`, stamp, LocalActorID, revision, stamp, commentID)
	} else {
		_, err = tx.ExecContext(ctx, `UPDATE comments SET resolved_at=NULL, resolved_by_type=NULL, resolved_by_id=NULL, revision=revision+1, issue_revision=?, updated_at=? WHERE id=?`, revision, stamp, commentID)
	}
	if err != nil {
		return Comment{}, err
	}
	if err := commitLocalWakeupMutation(ctx, tx, workspaceID, cursor, origin); err != nil {
		return Comment{}, err
	}
	comment, err := scanComment(s.db.QueryRowContext(ctx, `SELECT id, issue_id, author_type, author_id, content, type, parent_id, created_at, updated_at, revision, issue_revision, resolved_at, resolved_by_type, resolved_by_id, source_task_id, quick_action_id FROM comments WHERE id=?`, commentID))
	if err != nil {
		return Comment{}, err
	}
	attachments, err := s.listAttachments(ctx, workspaceID, "", commentID)
	if err != nil {
		return Comment{}, err
	}
	for index := range attachments {
		attachments[index] = attachmentWithURLs(attachments[index])
	}
	comment.Attachments = attachments
	return comment, nil
}

func (s *Store) Timeline(ctx context.Context, workspaceID, issueID string) ([]map[string]any, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT 'activity', id, actor_type, actor_id, created_at, action, details, NULL, NULL, NULL, NULL FROM activities WHERE issue_id=?
		UNION ALL SELECT 'comment', id, author_type, author_id, created_at, NULL, '{}', content, parent_id, updated_at, type FROM comments WHERE issue_id=? ORDER BY created_at`, issueID, issueID)
	if err != nil {
		return nil, err
	}
	result := []map[string]any{}
	for rows.Next() {
		var kind, id, actorType, actorID, createdAt, details string
		var action, content, parentID, updatedAt, commentType sql.NullString
		if err := rows.Scan(&kind, &id, &actorType, &actorID, &createdAt, &action, &details, &content, &parentID, &updatedAt, &commentType); err != nil {
			return nil, err
		}
		entry := map[string]any{"type": kind, "id": id, "actor_type": actorType, "actor_id": actorID, "created_at": createdAt}
		if action.Valid {
			entry["action"] = action.String
			entry["details"] = decodeObject(details)
		}
		if content.Valid {
			entry["content"] = content.String
			entry["parent_id"] = stringPtr(parentID)
			entry["updated_at"] = updatedAt.String
			entry["comment_type"] = commentType.String
			entry["reactions"] = []any{}
			entry["attachments"] = []Attachment{}
		}
		result = append(result, entry)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	comments, err := s.ListComments(ctx, workspaceID, issueID)
	if err != nil {
		return nil, err
	}
	commentsByID := make(map[string]Comment, len(comments))
	for _, comment := range comments {
		commentsByID[comment.ID] = comment
	}
	for _, entry := range result {
		comment, ok := commentsByID[fmt.Sprint(entry["id"])]
		if !ok {
			continue
		}
		entry["revision"] = comment.Revision
		entry["issue_revision"] = comment.IssueRevision
		entry["resolved_at"] = comment.ResolvedAt
		entry["resolved_by_type"] = comment.ResolvedByType
		entry["resolved_by_id"] = comment.ResolvedByID
		entry["source_task_id"] = comment.SourceTaskID
		entry["quick_action_id"] = comment.QuickActionID
		entry["attachments"] = comment.Attachments
	}
	return result, nil
}

func (s *Store) ListStatuses(ctx context.Context, workspaceID string) ([]map[string]any, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, workspace_id, key, name, description, category, color, is_system, position, archived_at, created_at, updated_at FROM issue_statuses WHERE workspace_id=? ORDER BY position`, workspaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []map[string]any{}
	for rows.Next() {
		var id, wsID, key, name, description, category, color, createdAt, updatedAt string
		var system bool
		var position int
		var archived sql.NullString
		if err := rows.Scan(&id, &wsID, &key, &name, &description, &category, &color, &system, &position, &archived, &createdAt, &updatedAt); err != nil {
			return nil, err
		}
		result = append(result, map[string]any{"id": id, "workspace_id": wsID, "key": key, "name": name, "description": description, "category": category, "color": color, "is_system": system, "position": position, "archived_at": stringPtr(archived), "created_at": createdAt, "updated_at": updatedAt})
	}
	return result, rows.Err()
}

func IsNotFound(err error) bool { return errors.Is(err, sql.ErrNoRows) }
