package localworkbench

import (
	"context"
	"database/sql"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	_ "modernc.org/sqlite"
)

const (
	LocalActorID = "00000000-0000-4000-8000-000000000001"
	LocalAgentID = "00000000-0000-4000-8000-000000000002"
)

//go:embed schema.sql
var schemaSQL string

type Store struct {
	db *sql.DB
}

type Workspace struct {
	ID          string         `json:"id"`
	Name        string         `json:"name"`
	Slug        string         `json:"slug"`
	Description *string        `json:"description"`
	Context     *string        `json:"context"`
	Settings    map[string]any `json:"settings"`
	Repos       []any          `json:"repos"`
	IssuePrefix string         `json:"issue_prefix"`
	AvatarURL   *string        `json:"avatar_url"`
	CreatedAt   string         `json:"created_at"`
	UpdatedAt   string         `json:"updated_at"`
}

type Project struct {
	ID            string  `json:"id"`
	WorkspaceID   string  `json:"workspace_id"`
	Title         string  `json:"title"`
	Description   *string `json:"description"`
	Icon          *string `json:"icon"`
	Status        string  `json:"status"`
	Priority      string  `json:"priority"`
	LeadType      *string `json:"lead_type"`
	LeadID        *string `json:"lead_id"`
	StartDate     *string `json:"start_date"`
	DueDate       *string `json:"due_date"`
	CreatedAt     string  `json:"created_at"`
	UpdatedAt     string  `json:"updated_at"`
	IssueCount    int64   `json:"issue_count"`
	DoneCount     int64   `json:"done_count"`
	ResourceCount int64   `json:"resource_count"`
}

type ProjectResourceInput struct {
	ResourceType string         `json:"resource_type"`
	ResourceRef  map[string]any `json:"resource_ref"`
	Label        string         `json:"label"`
	Position     int            `json:"position"`
}

type Issue struct {
	ID             string         `json:"id"`
	WorkspaceID    string         `json:"workspace_id"`
	Number         int64          `json:"number"`
	Identifier     string         `json:"identifier"`
	Title          string         `json:"title"`
	Description    *string        `json:"description"`
	Status         string         `json:"status"`
	StatusCategory string         `json:"status_category,omitempty"`
	Priority       string         `json:"priority"`
	AssigneeType   *string        `json:"assignee_type"`
	AssigneeID     *string        `json:"assignee_id"`
	CreatorType    string         `json:"creator_type"`
	CreatorID      string         `json:"creator_id"`
	ParentIssueID  *string        `json:"parent_issue_id"`
	ProjectID      *string        `json:"project_id"`
	Position       float64        `json:"position"`
	Stage          *int64         `json:"stage"`
	StartDate      *string        `json:"start_date"`
	DueDate        *string        `json:"due_date"`
	Metadata       map[string]any `json:"metadata"`
	Properties     map[string]any `json:"properties"`
	Reactions      []any          `json:"reactions"`
	Labels         []any          `json:"labels"`
	CreatedAt      string         `json:"created_at"`
	UpdatedAt      string         `json:"updated_at"`
	Revision       int64          `json:"revision"`
	LastActivityAt *string        `json:"last_activity_at"`
}

type Comment struct {
	ID             string  `json:"id"`
	IssueID        string  `json:"issue_id"`
	AuthorType     string  `json:"author_type"`
	AuthorID       string  `json:"author_id"`
	Content        string  `json:"content"`
	Type           string  `json:"type"`
	ParentID       *string `json:"parent_id"`
	Reactions      []any   `json:"reactions"`
	Attachments    []any   `json:"attachments"`
	CreatedAt      string  `json:"created_at"`
	UpdatedAt      string  `json:"updated_at"`
	Revision       int64   `json:"revision"`
	IssueRevision  int64   `json:"issue_revision"`
	ResolvedAt     *string `json:"resolved_at"`
	ResolvedByType *string `json:"resolved_by_type"`
	ResolvedByID   *string `json:"resolved_by_id"`
	SourceTaskID   *string `json:"source_task_id"`
	QuickActionID  *string `json:"quick_action_id"`
}

func Open(ctx context.Context, path string) (*Store, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("sqlite path is empty")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("creating sqlite directory: %w", err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("opening sqlite database: %w", err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.ExecContext(ctx, schemaSQL); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("initializing sqlite schema: %w", err)
	}
	s := &Store{db: db}
	if err := s.ensureDefaultWorkspace(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

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

func (s *Store) ensureDefaultWorkspace(ctx context.Context) error {
	workspaces, err := s.ListWorkspaces(ctx)
	if err != nil {
		return fmt.Errorf("listing local workspace: %w", err)
	}
	if len(workspaces) != 0 {
		return s.ensureBuiltinStatuses(ctx, workspaces[0].ID)
	}
	ws, err := s.CreateWorkspace(ctx, "Local", "local", "Local Codex workbench", "LOCAL")
	if err != nil {
		return fmt.Errorf("creating default local workspace: %w", err)
	}
	return s.ensureBuiltinStatuses(ctx, ws.ID)
}

func (s *Store) ensureBuiltinStatuses(ctx context.Context, workspaceID string) error {
	type status struct{ key, name, description, color string }
	statuses := []status{
		{"backlog", "Backlog", "Parked", "#6b7280"},
		{"todo", "Todo", "Queued for work", "#6b7280"},
		{"in_progress", "In Progress", "Actively being worked on", "#f59e0b"},
		{"in_review", "In Review", "Waiting for review", "#22c55e"},
		{"done", "Done", "Completed", "#3b82f6"},
		{"blocked", "Blocked", "Blocked by a dependency", "#ef4444"},
		{"cancelled", "Cancelled", "Cancelled", "#6b7280"},
	}
	stamp := now()
	for position, item := range statuses {
		_, err := s.db.ExecContext(ctx, `INSERT OR IGNORE INTO issue_statuses
			(id, workspace_id, key, name, description, category, color, is_system, position, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, 1, ?, ?, ?)`, uuid.NewString(), workspaceID, item.key, item.name, item.description, item.key, item.color, position, stamp, stamp)
		if err != nil {
			return fmt.Errorf("seeding issue status %s: %w", item.key, err)
		}
	}
	return nil
}

func (s *Store) ListWorkspaces(ctx context.Context) ([]Workspace, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, slug, description, context, settings, repos, issue_prefix, avatar_url, created_at, updated_at FROM workspaces ORDER BY created_at, id LIMIT 1`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []Workspace{}
	for rows.Next() {
		ws, err := scanWorkspace(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, ws)
	}
	return result, rows.Err()
}

func (s *Store) GetWorkspace(ctx context.Context, value string) (Workspace, error) {
	return scanWorkspace(s.db.QueryRowContext(ctx, `SELECT id, name, slug, description, context, settings, repos, issue_prefix, avatar_url, created_at, updated_at FROM workspaces WHERE id = ? OR slug = ? LIMIT 1`, value, value))
}

type rowScanner interface{ Scan(...any) error }

func scanWorkspace(row rowScanner) (Workspace, error) {
	var ws Workspace
	var description, contextValue, avatar sql.NullString
	var settings, repos string
	err := row.Scan(&ws.ID, &ws.Name, &ws.Slug, &description, &contextValue, &settings, &repos, &ws.IssuePrefix, &avatar, &ws.CreatedAt, &ws.UpdatedAt)
	ws.Description, ws.Context, ws.AvatarURL = stringPtr(description), stringPtr(contextValue), stringPtr(avatar)
	ws.Settings, ws.Repos = decodeObject(settings), decodeArray(repos)
	return ws, err
}

func (s *Store) CreateWorkspace(ctx context.Context, name, slug, description, prefix string) (Workspace, error) {
	stamp := now()
	if prefix == "" {
		prefix = "LOCAL"
	}
	if slug == "" {
		slug = "local"
	}
	id := uuid.NewString()
	_, err := s.db.ExecContext(ctx, `INSERT INTO workspaces(id, name, slug, description, issue_prefix, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)`, id, name, slug, nullable(description), strings.ToUpper(prefix), stamp, stamp)
	if err != nil {
		return Workspace{}, err
	}
	if err := s.ensureBuiltinStatuses(ctx, id); err != nil {
		return Workspace{}, err
	}
	return s.GetWorkspace(ctx, id)
}

func (s *Store) ListProjects(ctx context.Context, workspaceID string) ([]Project, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT p.id, p.workspace_id, p.title, p.description, p.icon, p.status, p.priority, p.lead_type, p.lead_id, p.start_date, p.due_date, p.created_at, p.updated_at,
		(SELECT COUNT(*) FROM issues i WHERE i.project_id=p.id),
		(SELECT COUNT(*) FROM issues i WHERE i.project_id=p.id AND i.status='done'),
		(SELECT COUNT(*) FROM project_resources r WHERE r.project_id=p.id)
		FROM projects p WHERE p.workspace_id=? ORDER BY p.updated_at DESC`, workspaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []Project{}
	for rows.Next() {
		project, err := scanProject(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, project)
	}
	return result, rows.Err()
}

func (s *Store) GetProject(ctx context.Context, workspaceID, id string) (Project, error) {
	return scanProject(s.db.QueryRowContext(ctx, `SELECT p.id, p.workspace_id, p.title, p.description, p.icon, p.status, p.priority, p.lead_type, p.lead_id, p.start_date, p.due_date, p.created_at, p.updated_at,
		(SELECT COUNT(*) FROM issues i WHERE i.project_id=p.id),
		(SELECT COUNT(*) FROM issues i WHERE i.project_id=p.id AND i.status='done'),
		(SELECT COUNT(*) FROM project_resources r WHERE r.project_id=p.id)
		FROM projects p WHERE p.workspace_id=? AND p.id=?`, workspaceID, id))
}

func scanProject(row rowScanner) (Project, error) {
	var p Project
	var description, icon, leadType, leadID, startDate, dueDate sql.NullString
	err := row.Scan(&p.ID, &p.WorkspaceID, &p.Title, &description, &icon, &p.Status, &p.Priority, &leadType, &leadID, &startDate, &dueDate, &p.CreatedAt, &p.UpdatedAt, &p.IssueCount, &p.DoneCount, &p.ResourceCount)
	p.Description, p.Icon, p.LeadType, p.LeadID = stringPtr(description), stringPtr(icon), stringPtr(leadType), stringPtr(leadID)
	p.StartDate, p.DueDate = stringPtr(startDate), stringPtr(dueDate)
	return p, err
}

func (s *Store) CreateProject(ctx context.Context, workspaceID, title, description, icon, status, priority string, resources []ProjectResourceInput) (Project, error) {
	if status == "" {
		status = "planned"
	}
	if priority == "" {
		priority = "none"
	}
	stamp, id := now(), uuid.NewString()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Project{}, err
	}
	defer func() { _ = tx.Rollback() }()
	_, err = tx.ExecContext(ctx, `INSERT INTO projects(id, workspace_id, title, description, icon, status, priority, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, id, workspaceID, title, nullable(description), nullable(icon), status, priority, stamp, stamp)
	if err != nil {
		return Project{}, err
	}
	for position, resource := range resources {
		if resource.Position == 0 {
			resource.Position = position
		}
		raw, err := json.Marshal(resource.ResourceRef)
		if err != nil {
			return Project{}, fmt.Errorf("encoding project resource: %w", err)
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO project_resources(id, project_id, workspace_id, resource_type, resource_ref, label, position, created_at, created_by) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, uuid.NewString(), id, workspaceID, resource.ResourceType, string(raw), nullable(resource.Label), resource.Position, stamp, LocalActorID)
		if err != nil {
			return Project{}, fmt.Errorf("creating project resource: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return Project{}, err
	}
	return s.GetProject(ctx, workspaceID, id)
}

func (s *Store) ListIssues(ctx context.Context, workspaceID, projectID, parentID string) ([]Issue, error) {
	query := `SELECT i.id, i.workspace_id, i.number, w.issue_prefix, i.title, i.description, i.status,
		COALESCE(s.category, i.status), i.priority, i.assignee_type, i.assignee_id, i.creator_type, i.creator_id,
		i.parent_issue_id, i.project_id, i.position, i.stage, i.start_date, i.due_date, i.metadata, i.properties,
		i.created_at, i.updated_at, i.revision, i.last_activity_at
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
	return result, rows.Err()
}

func (s *Store) GetIssue(ctx context.Context, workspaceID, value string) (Issue, error) {
	query := `SELECT i.id, i.workspace_id, i.number, w.issue_prefix, i.title, i.description, i.status,
		COALESCE(s.category, i.status), i.priority, i.assignee_type, i.assignee_id, i.creator_type, i.creator_id,
		i.parent_issue_id, i.project_id, i.position, i.stage, i.start_date, i.due_date, i.metadata, i.properties,
		i.created_at, i.updated_at, i.revision, i.last_activity_at
		FROM issues i JOIN workspaces w ON w.id=i.workspace_id
		LEFT JOIN issue_statuses s ON s.workspace_id=i.workspace_id AND s.key=i.status
		WHERE i.workspace_id=? AND (i.id=? OR upper(w.issue_prefix || '-' || i.number)=upper(?)) LIMIT 1`
	return scanIssue(s.db.QueryRowContext(ctx, query, workspaceID, value, value))
}

func scanIssue(row rowScanner) (Issue, error) {
	var i Issue
	var prefix, metadata, properties string
	var description, assigneeType, assigneeID, parentID, projectID, startDate, dueDate, lastActivity sql.NullString
	var stage sql.NullInt64
	err := row.Scan(&i.ID, &i.WorkspaceID, &i.Number, &prefix, &i.Title, &description, &i.Status, &i.StatusCategory, &i.Priority, &assigneeType, &assigneeID, &i.CreatorType, &i.CreatorID, &parentID, &projectID, &i.Position, &stage, &startDate, &dueDate, &metadata, &properties, &i.CreatedAt, &i.UpdatedAt, &i.Revision, &lastActivity)
	i.Identifier = fmt.Sprintf("%s-%d", prefix, i.Number)
	i.Description, i.AssigneeType, i.AssigneeID = stringPtr(description), stringPtr(assigneeType), stringPtr(assigneeID)
	i.ParentIssueID, i.ProjectID, i.StartDate, i.DueDate = stringPtr(parentID), stringPtr(projectID), stringPtr(startDate), stringPtr(dueDate)
	i.Stage, i.LastActivityAt = intPtr(stage), stringPtr(lastActivity)
	i.Metadata, i.Properties, i.Reactions, i.Labels = decodeObject(metadata), decodeObject(properties), []any{}, []any{}
	return i, err
}

type CreateIssueInput struct {
	Title         string         `json:"title"`
	Description   string         `json:"description"`
	Status        string         `json:"status"`
	Priority      string         `json:"priority"`
	AssigneeType  *string        `json:"assignee_type"`
	AssigneeID    *string        `json:"assignee_id"`
	ProjectID     *string        `json:"project_id"`
	ParentIssueID *string        `json:"parent_issue_id"`
	StartDate     *string        `json:"start_date"`
	DueDate       *string        `json:"due_date"`
	Metadata      map[string]any `json:"metadata"`
}

func (s *Store) CreateIssue(ctx context.Context, workspaceID string, input CreateIssueInput) (Issue, error) {
	if input.ProjectID != nil {
		if _, err := s.GetProject(ctx, workspaceID, *input.ProjectID); err != nil {
			return Issue{}, err
		}
	}
	if input.ParentIssueID != nil {
		if _, err := s.GetIssue(ctx, workspaceID, *input.ParentIssueID); err != nil {
			return Issue{}, err
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Issue{}, err
	}
	defer func() { _ = tx.Rollback() }()
	var number int64
	if err := tx.QueryRowContext(ctx, "SELECT COALESCE(MAX(number), 0)+1 FROM issues WHERE workspace_id=?", workspaceID).Scan(&number); err != nil {
		return Issue{}, err
	}
	if input.Status == "" {
		input.Status = "backlog"
	}
	if input.Priority == "" {
		input.Priority = "none"
	}
	metadata, _ := json.Marshal(input.Metadata)
	stamp, id := now(), uuid.NewString()
	_, err = tx.ExecContext(ctx, `INSERT INTO issues(id, workspace_id, number, title, description, status, priority, assignee_type, assignee_id, creator_type, creator_id, parent_issue_id, project_id, start_date, due_date, metadata, last_activity_at, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 'system', ?, ?, ?, ?, ?, ?, ?, ?, ?)`, id, workspaceID, number, input.Title, nullable(input.Description), input.Status, input.Priority, input.AssigneeType, input.AssigneeID, LocalActorID, input.ParentIssueID, input.ProjectID, input.StartDate, input.DueDate, string(metadata), stamp, stamp, stamp)
	if err != nil {
		return Issue{}, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO activities(id, issue_id, actor_type, actor_id, action, details, created_at) VALUES (?, ?, 'system', ?, 'created', '{}', ?)`, uuid.NewString(), id, LocalActorID, stamp)
	if err != nil {
		return Issue{}, err
	}
	if err := tx.Commit(); err != nil {
		return Issue{}, err
	}
	return s.GetIssue(ctx, workspaceID, id)
}

func (s *Store) UpdateIssue(ctx context.Context, workspaceID, id string, updates map[string]any) (Issue, error) {
	current, err := s.GetIssue(ctx, workspaceID, id)
	if err != nil {
		return Issue{}, err
	}
	set := []string{"updated_at=?", "last_activity_at=?", "revision=revision+1"}
	stamp := now()
	args := []any{stamp, stamp}
	if value, ok := updates["project_id"].(string); ok && value != "" {
		if _, err := s.GetProject(ctx, workspaceID, value); err != nil {
			return Issue{}, err
		}
	}
	if value, ok := updates["parent_issue_id"].(string); ok && value != "" {
		if value == current.ID {
			return Issue{}, errors.New("issue cannot be its own parent")
		}
		if _, err := s.GetIssue(ctx, workspaceID, value); err != nil {
			return Issue{}, err
		}
	}
	allowed := map[string]bool{"title": true, "description": true, "status": true, "priority": true, "assignee_type": true, "assignee_id": true, "project_id": true, "parent_issue_id": true, "start_date": true, "due_date": true, "position": true, "stage": true, "metadata": true, "properties": true}
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
	args = append(args, current.ID)
	if _, err := s.db.ExecContext(ctx, "UPDATE issues SET "+strings.Join(set, ", ")+" WHERE id=?", args...); err != nil {
		return Issue{}, err
	}
	return s.GetIssue(ctx, workspaceID, current.ID)
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
	for _, query := range []string{"DELETE FROM comments WHERE issue_id=?", "DELETE FROM activities WHERE issue_id=?", "DELETE FROM task_runs WHERE issue_id=?", "DELETE FROM issues WHERE id=?"} {
		if _, err := tx.ExecContext(ctx, query, issue.ID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) ListComments(ctx context.Context, issueID string) ([]Comment, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, issue_id, author_type, author_id, content, type, parent_id, created_at, updated_at, revision, issue_revision, resolved_at, resolved_by_type, resolved_by_id, source_task_id, quick_action_id FROM comments WHERE issue_id=? ORDER BY created_at`, issueID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []Comment{}
	for rows.Next() {
		c, err := scanComment(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, c)
	}
	return result, rows.Err()
}

func scanComment(row rowScanner) (Comment, error) {
	var c Comment
	var parent, resolvedAt, resolvedType, resolvedID, sourceTask, quickAction sql.NullString
	err := row.Scan(&c.ID, &c.IssueID, &c.AuthorType, &c.AuthorID, &c.Content, &c.Type, &parent, &c.CreatedAt, &c.UpdatedAt, &c.Revision, &c.IssueRevision, &resolvedAt, &resolvedType, &resolvedID, &sourceTask, &quickAction)
	c.ParentID, c.ResolvedAt, c.ResolvedByType, c.ResolvedByID = stringPtr(parent), stringPtr(resolvedAt), stringPtr(resolvedType), stringPtr(resolvedID)
	c.SourceTaskID, c.QuickActionID, c.Reactions, c.Attachments = stringPtr(sourceTask), stringPtr(quickAction), []any{}, []any{}
	return c, err
}

func (s *Store) CreateComment(ctx context.Context, issueID, content, kind, parentID string) (Comment, error) {
	return s.createComment(ctx, issueID, content, kind, parentID, "system", LocalActorID)
}

func (s *Store) createComment(ctx context.Context, issueID, content, kind, parentID, authorType, authorID string) (Comment, error) {
	if kind == "" {
		kind = "comment"
	}
	stamp, id := now(), uuid.NewString()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Comment{}, err
	}
	defer func() { _ = tx.Rollback() }()
	var revision int64
	if err := tx.QueryRowContext(ctx, "UPDATE issues SET revision=revision+1, updated_at=?, last_activity_at=? WHERE id=? RETURNING revision", stamp, stamp, issueID).Scan(&revision); err != nil {
		return Comment{}, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO comments(id, issue_id, author_type, author_id, content, type, parent_id, issue_revision, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, id, issueID, authorType, authorID, content, kind, nullable(parentID), revision, stamp, stamp)
	if err != nil {
		return Comment{}, err
	}
	if err := tx.Commit(); err != nil {
		return Comment{}, err
	}
	return scanComment(s.db.QueryRowContext(ctx, `SELECT id, issue_id, author_type, author_id, content, type, parent_id, created_at, updated_at, revision, issue_revision, resolved_at, resolved_by_type, resolved_by_id, source_task_id, quick_action_id FROM comments WHERE id=?`, id))
}

func (s *Store) Timeline(ctx context.Context, issueID string) ([]map[string]any, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT 'activity', id, actor_type, actor_id, created_at, action, details, NULL, NULL, NULL, NULL FROM activities WHERE issue_id=?
		UNION ALL SELECT 'comment', id, author_type, author_id, created_at, NULL, '{}', content, parent_id, updated_at, type FROM comments WHERE issue_id=? ORDER BY created_at`, issueID, issueID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
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
			entry["attachments"] = []any{}
		}
		result = append(result, entry)
	}
	return result, rows.Err()
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
