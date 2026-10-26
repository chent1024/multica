package localworkbench

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

var ErrProjectHasIssues = errors.New("project still has tasks")

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
	WorkflowID    *string `json:"workflow_id"`
}

type ProjectResource struct {
	ID           string         `json:"id"`
	ProjectID    string         `json:"project_id"`
	WorkspaceID  string         `json:"workspace_id"`
	ResourceType string         `json:"resource_type"`
	ResourceRef  map[string]any `json:"resource_ref"`
	Label        *string        `json:"label"`
	Position     int            `json:"position"`
	CreatedAt    string         `json:"created_at"`
	CreatedBy    *string        `json:"created_by"`
	resourceJSON string
}

type CreateProjectInput struct {
	Title       string                 `json:"title"`
	Description *string                `json:"description"`
	Icon        *string                `json:"icon"`
	Status      string                 `json:"status"`
	Priority    string                 `json:"priority"`
	LeadType    *string                `json:"lead_type"`
	LeadID      *string                `json:"lead_id"`
	StartDate   *string                `json:"start_date"`
	DueDate     *string                `json:"due_date"`
	Resources   []ProjectResourceInput `json:"resources"`
	WorkflowID  *string                `json:"workflow_id,omitempty"`
}

type ProjectResourceInput struct {
	ResourceType string         `json:"resource_type"`
	ResourceRef  map[string]any `json:"resource_ref"`
	Label        *string        `json:"label"`
	Position     int            `json:"position"`
}

func (s *Store) ListProjects(ctx context.Context, workspaceID string) ([]Project, error) {
	rows, err := s.db.QueryContext(ctx, projectSelect+`
		WHERE p.workspace_id = ? ORDER BY p.updated_at DESC, p.id`, workspaceID)
	if err != nil {
		return nil, fmt.Errorf("list projects: %w", err)
	}
	defer rows.Close()
	projects := []Project{}
	for rows.Next() {
		project, err := scanProject(rows)
		if err != nil {
			return nil, fmt.Errorf("scan project: %w", err)
		}
		projects = append(projects, project)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate projects: %w", err)
	}
	return projects, nil
}

func (s *Store) GetProject(ctx context.Context, workspaceID, id string) (Project, error) {
	project, err := scanProject(s.db.QueryRowContext(ctx, projectSelect+`
		WHERE p.workspace_id = ? AND p.id = ?`, workspaceID, id))
	if err != nil {
		return Project{}, fmt.Errorf("get project: %w", err)
	}
	return project, nil
}

const projectSelect = `SELECT
	p.id, p.workspace_id, p.title, p.description, p.icon, p.status, p.priority,
	p.lead_type, p.lead_id, p.start_date, p.due_date, p.created_at, p.updated_at,
	(SELECT COUNT(*) FROM issues i WHERE i.project_id = p.id),
	(SELECT COUNT(*) FROM issues i WHERE i.project_id = p.id AND i.status = 'done'),
	(SELECT COUNT(*) FROM project_resources r WHERE r.project_id = p.id), p.workflow_id
	FROM projects p `

type rowScanner interface {
	Scan(dest ...any) error
}

func scanProject(row rowScanner) (Project, error) {
	var project Project
	var description, icon, leadType, leadID, startDate, dueDate, workflowID sql.NullString
	err := row.Scan(
		&project.ID, &project.WorkspaceID, &project.Title, &description, &icon,
		&project.Status, &project.Priority, &leadType, &leadID, &startDate, &dueDate,
		&project.CreatedAt, &project.UpdatedAt, &project.IssueCount,
		&project.DoneCount, &project.ResourceCount, &workflowID,
	)
	if err != nil {
		return Project{}, err
	}
	project.Description = nullStringPointer(description)
	project.Icon = nullStringPointer(icon)
	project.LeadType = nullStringPointer(leadType)
	project.LeadID = nullStringPointer(leadID)
	project.StartDate = nullStringPointer(startDate)
	project.DueDate = nullStringPointer(dueDate)
	project.WorkflowID = nullStringPointer(workflowID)
	return project, nil
}

func (s *Store) CreateProject(ctx context.Context, workspaceID string, input CreateProjectInput) (Project, error) {
	input.Title = strings.TrimSpace(input.Title)
	if input.Title == "" {
		return Project{}, errors.New("project title is required")
	}
	if input.Status == "" {
		input.Status = "planned"
	}
	if input.Priority == "" {
		input.Priority = "none"
	}
	stamp := time.Now().UTC().Format(time.RFC3339Nano)
	id := uuid.NewString()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Project{}, fmt.Errorf("begin create project: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if input.WorkflowID != nil && strings.TrimSpace(*input.WorkflowID) != "" {
		target, err := localProjectWorkflowTargetTx(ctx, tx, workspaceID, input.WorkflowID)
		if err != nil {
			return Project{}, err
		}
		if target.IsDefault {
			input.WorkflowID = nil
		} else {
			input.WorkflowID = &target.ID
		}
	} else {
		input.WorkflowID = nil
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO projects (
		id, workspace_id, title, description, icon, status, priority,
		lead_type, lead_id, start_date, due_date, created_at, updated_at, workflow_id
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, workspaceID, input.Title, input.Description, input.Icon,
		input.Status, input.Priority, input.LeadType, input.LeadID,
		input.StartDate, input.DueDate, stamp, stamp, input.WorkflowID,
	); err != nil {
		return Project{}, fmt.Errorf("insert project: %w", err)
	}
	for index, resource := range input.Resources {
		if resource.Position == 0 {
			resource.Position = index
		}
		if _, err := createProjectResourceTx(ctx, tx, workspaceID, id, resource, stamp); err != nil {
			return Project{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return Project{}, fmt.Errorf("commit project: %w", err)
	}
	return s.GetProject(ctx, workspaceID, id)
}

func (s *Store) UpdateProject(ctx context.Context, workspaceID, id string, updates map[string]any) (Project, error) {
	allowed := map[string]bool{
		"title": true, "description": true, "icon": true, "status": true,
		"priority": true, "lead_type": true, "lead_id": true,
		"start_date": true, "due_date": true,
	}
	set := []string{"updated_at = ?"}
	args := []any{time.Now().UTC().Format(time.RFC3339Nano)}
	for key, value := range updates {
		if !allowed[key] {
			continue
		}
		if key == "title" {
			title, ok := value.(string)
			if !ok || strings.TrimSpace(title) == "" {
				return Project{}, errors.New("project title is required")
			}
			value = strings.TrimSpace(title)
		}
		set = append(set, key+" = ?")
		args = append(args, value)
	}
	args = append(args, workspaceID, id)
	result, err := s.db.ExecContext(ctx,
		"UPDATE projects SET "+strings.Join(set, ", ")+" WHERE workspace_id = ? AND id = ?",
		args...,
	)
	if err != nil {
		return Project{}, fmt.Errorf("update project: %w", err)
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return Project{}, fmt.Errorf("read project update result: %w", err)
	}
	if updated != 1 {
		return Project{}, sql.ErrNoRows
	}
	return s.GetProject(ctx, workspaceID, id)
}

func (s *Store) DeleteProject(ctx context.Context, workspaceID, id string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin delete project: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var issueCount int
	if err := tx.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM issues WHERE workspace_id = ? AND project_id = ?",
		workspaceID, id,
	).Scan(&issueCount); err != nil {
		return fmt.Errorf("count project tasks: %w", err)
	}
	if issueCount != 0 {
		return ErrProjectHasIssues
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM project_resources WHERE workspace_id = ? AND project_id = ?", workspaceID, id); err != nil {
		return fmt.Errorf("delete project resources: %w", err)
	}
	result, err := tx.ExecContext(ctx, "DELETE FROM projects WHERE workspace_id = ? AND id = ?", workspaceID, id)
	if err != nil {
		return fmt.Errorf("delete project: %w", err)
	}
	deleted, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read project deletion result: %w", err)
	}
	if deleted != 1 {
		return sql.ErrNoRows
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit project deletion: %w", err)
	}
	return nil
}

func (s *Store) ListProjectResources(ctx context.Context, workspaceID, projectID string) ([]ProjectResource, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, project_id, workspace_id,
		resource_type, resource_ref, label, position, created_at, created_by
		FROM project_resources WHERE workspace_id = ? AND project_id = ?
		ORDER BY position, created_at, id`, workspaceID, projectID)
	if err != nil {
		return nil, fmt.Errorf("list project resources: %w", err)
	}
	defer rows.Close()
	resources := []ProjectResource{}
	for rows.Next() {
		resource, err := scanProjectResource(rows)
		if err != nil {
			return nil, fmt.Errorf("scan project resource: %w", err)
		}
		resources = append(resources, resource)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate project resources: %w", err)
	}
	return resources, nil
}

func (s *Store) CreateProjectResource(ctx context.Context, workspaceID, projectID string, input ProjectResourceInput) (ProjectResource, error) {
	stamp := time.Now().UTC().Format(time.RFC3339Nano)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ProjectResource{}, fmt.Errorf("begin create project resource: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := requireProjectTx(ctx, tx, workspaceID, projectID); err != nil {
		return ProjectResource{}, err
	}
	resource, err := createProjectResourceTx(ctx, tx, workspaceID, projectID, input, stamp)
	if err != nil {
		return ProjectResource{}, err
	}
	if err := tx.Commit(); err != nil {
		return ProjectResource{}, fmt.Errorf("commit project resource: %w", err)
	}
	return resource, nil
}

func createProjectResourceTx(ctx context.Context, tx *sql.Tx, workspaceID, projectID string, input ProjectResourceInput, stamp string) (ProjectResource, error) {
	if input.ResourceType != "github_repo" && input.ResourceType != "local_directory" {
		return ProjectResource{}, errors.New("unsupported project resource type")
	}
	if len(input.ResourceRef) == 0 {
		return ProjectResource{}, errors.New("project resource reference is required")
	}
	raw, err := json.Marshal(input.ResourceRef)
	if err != nil {
		return ProjectResource{}, fmt.Errorf("encode project resource: %w", err)
	}
	resource := ProjectResource{
		ID: uuid.NewString(), ProjectID: projectID, WorkspaceID: workspaceID,
		ResourceType: input.ResourceType, ResourceRef: input.ResourceRef,
		Label: input.Label, Position: input.Position, CreatedAt: stamp,
	}
	createdBy := LocalOperatorID
	resource.CreatedBy = &createdBy
	if _, err := tx.ExecContext(ctx, `INSERT INTO project_resources (
		id, project_id, workspace_id, resource_type, resource_ref,
		label, position, created_at, created_by
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		resource.ID, projectID, workspaceID, input.ResourceType, string(raw),
		input.Label, input.Position, stamp, createdBy,
	); err != nil {
		return ProjectResource{}, fmt.Errorf("insert project resource: %w", err)
	}
	return resource, nil
}

func requireProjectTx(ctx context.Context, tx *sql.Tx, workspaceID, projectID string) (string, error) {
	var id string
	if err := tx.QueryRowContext(ctx,
		"SELECT id FROM projects WHERE workspace_id = ? AND id = ?",
		workspaceID, projectID,
	).Scan(&id); err != nil {
		return "", fmt.Errorf("require project: %w", err)
	}
	return id, nil
}

func scanProjectResource(row rowScanner) (ProjectResource, error) {
	var resource ProjectResource
	var label, createdBy sql.NullString
	if err := row.Scan(
		&resource.ID, &resource.ProjectID, &resource.WorkspaceID,
		&resource.ResourceType, &resource.resourceJSON, &label,
		&resource.Position, &resource.CreatedAt, &createdBy,
	); err != nil {
		return ProjectResource{}, err
	}
	if err := json.Unmarshal([]byte(resource.resourceJSON), &resource.ResourceRef); err != nil {
		return ProjectResource{}, fmt.Errorf("decode project resource: %w", err)
	}
	resource.Label = nullStringPointer(label)
	resource.CreatedBy = nullStringPointer(createdBy)
	return resource, nil
}
