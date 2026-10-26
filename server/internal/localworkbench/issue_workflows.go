package localworkbench

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/issueworkflow"
)

var regexpWorkflowColor = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)

type localWorkflowRecord struct {
	ID          string               `json:"id"`
	WorkspaceID string               `json:"workspace_id"`
	Name        string               `json:"name"`
	Description string               `json:"description"`
	IsDefault   bool                 `json:"is_default"`
	OwnerID     *string              `json:"owner_id"`
	Steps       []issueworkflow.Step `json:"steps"`
	ProjectIDs  []string             `json:"project_ids"`
	CreatedAt   string               `json:"created_at"`
	UpdatedAt   string               `json:"updated_at"`
}

func (r localWorkflowRecord) definition() issueworkflow.Definition {
	return issueworkflow.Definition{Name: r.Name, IsDefault: r.IsDefault, Steps: r.Steps}
}

const localWorkflowSelect = `SELECT id,workspace_id,name,description,is_default,owner_id,steps,created_at,updated_at FROM issue_workflows`

func scanLocalWorkflow(row rowScanner) (localWorkflowRecord, error) {
	var r localWorkflowRecord
	var owner sql.NullString
	var steps string
	if err := row.Scan(&r.ID, &r.WorkspaceID, &r.Name, &r.Description, &r.IsDefault, &owner, &steps, &r.CreatedAt, &r.UpdatedAt); err != nil {
		return r, err
	}
	var err error
	r.Steps, err = issueworkflow.DecodeSteps([]byte(steps))
	r.OwnerID = stringPtr(owner)
	r.ProjectIDs = []string{}
	return r, err
}
func loadLocalWorkflow(ctx context.Context, q localWakeupQuery, ws, id string) (localWorkflowRecord, error) {
	return scanLocalWorkflow(q.QueryRowContext(ctx, localWorkflowSelect+` WHERE workspace_id=? AND id=?`, ws, id))
}
func localWorkflowCatalog(ctx context.Context, q localWakeupQuery, ws string) (map[string]issueworkflow.CatalogEntry, []string, error) {
	rows, err := q.QueryContext(ctx, `SELECT key,name,category,archived_at FROM issue_statuses WHERE workspace_id=? ORDER BY position,id`, ws)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	out := map[string]issueworkflow.CatalogEntry{}
	keys := []string{}
	for rows.Next() {
		var key, name, category string
		var archived sql.NullString
		if err := rows.Scan(&key, &name, &category, &archived); err != nil {
			return nil, nil, err
		}
		out[key] = issueworkflow.CatalogEntry{Key: key, Name: name, Category: localStatusCategory(category), Archived: archived.Valid}
		if !archived.Valid {
			keys = append(keys, key)
		}
	}
	return out, keys, rows.Err()
}
func ensureLocalDefaultWorkflowTx(ctx context.Context, tx *sql.Tx, ws string) (localWorkflowRecord, error) {
	r, err := scanLocalWorkflow(tx.QueryRowContext(ctx, localWorkflowSelect+` WHERE workspace_id=? AND is_default=1`, ws))
	if err == nil {
		return r, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return r, err
	}
	_, keys, err := localWorkflowCatalog(ctx, tx, ws)
	if err != nil {
		return r, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT steps FROM issue_workflows WHERE workspace_id=? AND is_default=0`, ws)
	if err != nil {
		return r, err
	}
	claimed := map[string]bool{}
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			_ = rows.Close()
			return r, err
		}
		steps, e := issueworkflow.DecodeKeys([]byte(raw))
		if e != nil {
			_ = rows.Close()
			return r, e
		}
		for _, k := range steps {
			claimed[k] = true
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return r, err
	}
	if err := rows.Close(); err != nil {
		return r, err
	}
	builtin := map[string]bool{}
	for _, key := range issueworkflow.BuiltInDefaultKeys() {
		builtin[key] = true
	}
	steps := []issueworkflow.Step{}
	for _, key := range keys {
		if !claimed[key] || builtin[key] {
			steps = append(steps, issueworkflow.Step{StatusKey: key, Handler: issueworkflow.Handler{Type: "none"}})
		}
	}
	steps = issueworkflow.Arrange(steps)
	raw, err := issueworkflow.EncodeSteps(steps)
	if err != nil {
		return r, err
	}
	id, stamp := uuid.NewString(), now()
	if _, err := tx.ExecContext(ctx, `INSERT INTO issue_workflows(id,workspace_id,name,description,is_default,owner_id,steps,created_at,updated_at) VALUES(?,?,'Default','',1,NULL,?,?,?)`, id, ws, string(raw), stamp, stamp); err != nil {
		return r, err
	}
	return loadLocalWorkflow(ctx, tx, ws, id)
}
func localWorkflowProjects(ctx context.Context, q localWakeupQuery, ws string, r localWorkflowRecord) ([]string, error) {
	query := `SELECT id FROM projects WHERE workspace_id=? AND workflow_id=? ORDER BY id`
	args := []any{ws, r.ID}
	if r.IsDefault {
		query = `SELECT id FROM projects WHERE workspace_id=? AND (workflow_id IS NULL OR workflow_id=? OR NOT EXISTS(SELECT 1 FROM issue_workflows w WHERE w.id=projects.workflow_id AND w.workspace_id=projects.workspace_id)) ORDER BY id`
	}
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
func (s *Server) localWorkflowResponse(ctx context.Context, r localWorkflowRecord) (localWorkflowRecord, error) {
	ids, err := localWorkflowProjects(ctx, s.store.db, r.WorkspaceID, r)
	r.ProjectIDs = ids
	return r, err
}

type localWorkflowError struct {
	Status        int
	Code, Message string
	Plan          any
}

func (e *localWorkflowError) Error() string { return e.Message }
func workflowError(status int, code, message string) error {
	return &localWorkflowError{Status: status, Code: code, Message: message}
}
func writeLocalIssueWorkflowError(w http.ResponseWriter, err error) bool {
	var e *localWorkflowError
	if errors.As(err, &e) {
		out := map[string]any{"error": e.Message, "code": e.Code}
		if e.Plan != nil {
			out["plan"] = e.Plan
		}
		writeJSON(w, e.Status, out)
		return true
	}
	var n *issueworkflow.NotInWorkflowError
	if errors.As(err, &n) {
		writeJSON(w, 400, map[string]any{"error": n.Error(), "code": "status_not_in_workflow", "workflow_name": n.WorkflowName, "allowed_statuses": n.Allowed})
		return true
	}
	var m *issueworkflow.StepMovedError
	if errors.As(err, &m) {
		writeJSON(w, 409, map[string]any{"error": m.Error(), "code": "workflow_step_moved", "step": m.Step, "current_status": m.Current})
		return true
	}
	return false
}
func (s *Server) respondLocalWorkflowError(w http.ResponseWriter, err error) {
	if !writeLocalIssueWorkflowError(w, err) {
		s.respondStoreError(w, err)
	}
}
func (s *Server) listLocalIssueWorkflows(w http.ResponseWriter, r *http.Request) {
	ws, err := s.workspaceFor(r)
	if err != nil {
		s.respondLocalWorkflowError(w, err)
		return
	}
	tx, err := s.store.db.BeginTx(r.Context(), nil)
	if err != nil {
		s.fail(w, err)
		return
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := ensureLocalDefaultWorkflowTx(r.Context(), tx, ws.ID); err != nil {
		s.fail(w, err)
		return
	}
	rows, err := tx.QueryContext(r.Context(), localWorkflowSelect+` WHERE workspace_id=? ORDER BY is_default DESC,name,id`, ws.ID)
	if err != nil {
		s.fail(w, err)
		return
	}
	records := []localWorkflowRecord{}
	for rows.Next() {
		rec, e := scanLocalWorkflow(rows)
		if e != nil {
			_ = rows.Close()
			s.fail(w, e)
			return
		}
		records = append(records, rec)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		s.fail(w, err)
		return
	}
	if err := rows.Close(); err != nil {
		s.fail(w, err)
		return
	}
	for i := range records {
		ids, e := localWorkflowProjects(r.Context(), tx, ws.ID, records[i])
		if e != nil {
			s.fail(w, e)
			return
		}
		records[i].ProjectIDs = ids
	}
	if err := tx.Commit(); err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"workflows": records, "total": len(records)})
}

type localWorkflowStepInput struct {
	StatusKey     string                                  `json:"status_key"`
	NewStatus     *struct{ Name, Category, Color string } `json:"new_status"`
	Handler       *issueworkflow.Handler                  `json:"handler"`
	Instructions  string                                  `json:"instructions"`
	NextStatusKey string                                  `json:"next_status_key"`
	BackStatusKey string                                  `json:"back_status_key"`
}
type localWorkflowWrite struct {
	Name          *string                   `json:"name"`
	Description   *string                   `json:"description"`
	Steps         *[]localWorkflowStepInput `json:"steps"`
	StatusMapping map[string]string         `json:"status_mapping"`
	DryRun        bool                      `json:"dry_run"`
}

func localWorkflowResolveSteps(ctx context.Context, tx *sql.Tx, ws string, inputs []localWorkflowStepInput) ([]issueworkflow.Step, error) {
	steps := []issueworkflow.Step{}
	for _, in := range inputs {
		step := issueworkflow.Step{StatusKey: in.StatusKey, Instructions: in.Instructions, NextStatusKey: in.NextStatusKey, BackStatusKey: in.BackStatusKey, Handler: issueworkflow.Handler{Type: "none"}}
		if in.Handler != nil {
			step.Handler = *in.Handler
		}
		if in.NewStatus != nil {
			name := strings.TrimSpace(in.NewStatus.Name)
			if name == "" || utf8.RuneCountInString(name) > 64 {
				return nil, workflowError(400, "invalid_workflow", "status name must be 1-64 characters")
			}
			var key string
			err := tx.QueryRowContext(ctx, `SELECT key FROM issue_statuses WHERE workspace_id=? AND lower(name)=lower(?) AND archived_at IS NULL LIMIT 1`, ws, name).Scan(&key)
			if errors.Is(err, sql.ErrNoRows) {
				category := localStatusCategory(in.NewStatus.Category)
				if category != "unstarted" && category != "started" && category != "done" && category != "closed" {
					category = "started"
				}
				color := in.NewStatus.Color
				if color == "" {
					color = map[string]string{"unstarted": "#94a3b8", "started": "#3b82f6", "done": "#22c55e", "closed": "#64748b"}[category]
				}
				if !regexpWorkflowColor.MatchString(color) {
					return nil, workflowError(400, "invalid_workflow", "color must be #rrggbb")
				}
				id, stamp := uuid.NewString(), now()
				key = "custom_" + strings.ReplaceAll(id, "-", "")
				var pos int
				if e := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(position),-1)+1 FROM issue_statuses WHERE workspace_id=?`, ws).Scan(&pos); e != nil {
					return nil, e
				}
				if _, e := tx.ExecContext(ctx, `INSERT INTO issue_statuses(id,workspace_id,key,name,description,category,color,is_system,position,created_at,updated_at) VALUES(?,?,?,?,'',?,?,0,?,?,?)`, id, ws, key, name, category, color, pos, stamp, stamp); e != nil {
					return nil, e
				}
			} else if err != nil {
				return nil, err
			}
			step.StatusKey = key
		}
		steps = append(steps, step)
	}
	return steps, nil
}
func validateLocalWorkflowHandler(ctx context.Context, q localWakeupQuery, ws string, handler issueworkflow.Handler) error {
	var count int
	var err error
	switch handler.Type {
	case "none", "project_lead", "creator":
		return nil
	case "agent":
		err = q.QueryRowContext(ctx, `SELECT COUNT(*) FROM agents WHERE workspace_id=? AND id=? AND archived_at IS NULL`, ws, handler.ID).Scan(&count)
	case "squad":
		err = q.QueryRowContext(ctx, `SELECT COUNT(*) FROM squads WHERE workspace_id=? AND id=? AND archived_at IS NULL`, ws, handler.ID).Scan(&count)
	case "member":
		if handler.ID == LocalOperatorID {
			count = 1
		}
	default:
		return workflowError(400, "invalid_handoff", "unknown handler")
	}
	if err != nil {
		return err
	}
	if count != 1 {
		return workflowError(400, "invalid_handoff", "handler does not exist in this workspace")
	}
	return nil
}
func validateLocalWorkflow(ctx context.Context, tx *sql.Tx, ws, name, description string, steps []issueworkflow.Step, isDefault bool, previous ...[]issueworkflow.Step) error {
	catalog, _, err := localWorkflowCatalog(ctx, tx, ws)
	if err != nil {
		return err
	}
	if err := issueworkflow.Validate(name, description, steps, isDefault, catalog); err != nil {
		return workflowError(400, "invalid_workflow", err.Error())
	}
	known := map[issueworkflow.Handler]bool{}
	for _, prior := range previous {
		for _, step := range prior {
			known[step.Handler] = true
		}
	}
	for _, step := range steps {
		if known[step.Handler] {
			continue
		}
		if err := validateLocalWorkflowHandler(ctx, tx, ws, step.Handler); err != nil {
			return err
		}
	}
	return nil
}
func (s *Server) createLocalIssueWorkflow(w http.ResponseWriter, r *http.Request) {
	ws, err := s.workspaceFor(r)
	if err != nil {
		s.respondLocalWorkflowError(w, err)
		return
	}
	var input localWorkflowWrite
	if err := decodeBody(r, &input); err != nil {
		writeError(w, 400, err)
		return
	}
	tx, err := s.store.db.BeginTx(r.Context(), nil)
	if err != nil {
		s.fail(w, err)
		return
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := ensureLocalDefaultWorkflowTx(r.Context(), tx, ws.ID); err != nil {
		s.respondLocalWorkflowError(w, err)
		return
	}
	steps := []issueworkflow.Step{}
	if input.Steps != nil {
		steps, err = localWorkflowResolveSteps(r.Context(), tx, ws.ID, *input.Steps)
		if err != nil {
			s.respondLocalWorkflowError(w, err)
			return
		}
	}
	name, description := "", ""
	if input.Name != nil {
		name = *input.Name
	}
	if input.Description != nil {
		description = *input.Description
	}
	name, description, steps = issueworkflow.Normalize(name, description, steps)
	if err := validateLocalWorkflow(r.Context(), tx, ws.ID, name, description, steps, false); err != nil {
		s.respondLocalWorkflowError(w, err)
		return
	}
	raw, err := issueworkflow.EncodeSteps(steps)
	if err != nil {
		s.fail(w, err)
		return
	}
	id, stamp := uuid.NewString(), now()
	if _, err := tx.ExecContext(r.Context(), `INSERT INTO issue_workflows(id,workspace_id,name,description,is_default,owner_id,steps,created_at,updated_at) VALUES(?,?,?,?,0,?,?,?,?)`, id, ws.ID, name, description, LocalOperatorID, string(raw), stamp, stamp); err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			s.respondLocalWorkflowError(w, workflowError(409, "workflow_name_taken", "a workflow with this name already exists"))
		} else {
			s.fail(w, err)
		}
		return
	}
	if err := tx.Commit(); err != nil {
		s.fail(w, err)
		return
	}
	s.hub.broadcast("issue_workflow:changed", map[string]any{"action": "created"})
	s.hub.broadcast("issue_status:changed", map[string]any{"action": "created"})
	rec, err := loadLocalWorkflow(r.Context(), s.store.db, ws.ID, id)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, 201, rec)
}

type localWorkflowMappingRequirement struct {
	StatusKey          string `json:"status_key"`
	IssueCount         int    `json:"issue_count"`
	SuggestedStatusKey string `json:"suggested_status_key"`
}
type localWorkflowStatusCount struct {
	StatusKey  string `json:"status_key"`
	IssueCount int    `json:"issue_count"`
}
type localWorkflowMappingPlan struct {
	Required       []localWorkflowMappingRequirement `json:"required"`
	Unchanged      []localWorkflowStatusCount        `json:"unchanged"`
	TotalIssues    int                               `json:"total_issues"`
	AffectedIssues int                               `json:"affected_issues"`
}

func localWorkflowPlan(ctx context.Context, tx *sql.Tx, ws string, projectIDs []string, target localWorkflowRecord) (localWorkflowMappingPlan, error) {
	plan := localWorkflowMappingPlan{Required: []localWorkflowMappingRequirement{}, Unchanged: []localWorkflowStatusCount{}}
	counts := map[string]int{}
	for _, projectID := range projectIDs {
		rows, err := tx.QueryContext(ctx, `SELECT status,COUNT(*) FROM issues WHERE workspace_id=? AND project_id=? GROUP BY status`, ws, projectID)
		if err != nil {
			return plan, err
		}
		for rows.Next() {
			var key string
			var n int
			if err := rows.Scan(&key, &n); err != nil {
				_ = rows.Close()
				return plan, err
			}
			counts[key] += n
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return plan, err
		}
		if err := rows.Close(); err != nil {
			return plan, err
		}
	}
	catalog, _, err := localWorkflowCatalog(ctx, tx, ws)
	if err != nil {
		return plan, err
	}
	keys := []string{}
	for key := range counts {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	def := target.definition()
	for _, key := range keys {
		n := counts[key]
		plan.TotalIssues += n
		if target.IsDefault || def.Has(key) {
			plan.Unchanged = append(plan.Unchanged, localWorkflowStatusCount{key, n})
		} else {
			plan.AffectedIssues += n
			plan.Required = append(plan.Required, localWorkflowMappingRequirement{key, n, issueworkflow.MoveTarget(def, key, func(k string) string { return catalog[k].Category })})
		}
	}
	return plan, nil
}
func localWorkflowValidateMapping(plan localWorkflowMappingPlan, mapping map[string]string, target localWorkflowRecord) error {
	def := target.definition()
	for _, req := range plan.Required {
		dest := mapping[req.StatusKey]
		if dest == "" || !def.Has(dest) {
			return &localWorkflowError{Status: 409, Code: "workflow_status_mapping_required", Message: fmt.Sprintf("choose a destination status for the %d issue(s) on %q", req.IssueCount, req.StatusKey), Plan: plan}
		}
	}
	return nil
}
func localWorkflowRemapTx(ctx context.Context, tx *sql.Tx, ws string, projects []string, plan localWorkflowMappingPlan, mapping map[string]string) ([]string, error) {
	ids := []string{}
	stamp := now()
	for _, req := range plan.Required {
		for _, project := range projects {
			rows, err := tx.QueryContext(ctx, `UPDATE issues SET status=?,duplicate_of_issue_id=CASE WHEN ?='cancelled' AND status='cancelled' THEN duplicate_of_issue_id ELSE NULL END,revision=revision+1,updated_at=?,last_activity_at=? WHERE workspace_id=? AND project_id=? AND status=? RETURNING id`, mapping[req.StatusKey], mapping[req.StatusKey], stamp, stamp, ws, project, req.StatusKey)
			if err != nil {
				return nil, err
			}
			for rows.Next() {
				var id string
				if err := rows.Scan(&id); err != nil {
					_ = rows.Close()
					return nil, err
				}
				ids = append(ids, id)
			}
			if err := rows.Err(); err != nil {
				_ = rows.Close()
				return nil, err
			}
			if err := rows.Close(); err != nil {
				return nil, err
			}
		}
	}
	return ids, nil
}
func (s *Server) localIssueWorkflowItem(w http.ResponseWriter, r *http.Request) {
	ws, err := s.workspaceFor(r)
	if err != nil {
		s.respondLocalWorkflowError(w, err)
		return
	}
	id := chi.URLParam(r, "id")
	if r.Method == http.MethodGet {
		rec, e := loadLocalWorkflow(r.Context(), s.store.db, ws.ID, id)
		if e != nil {
			s.respondLocalWorkflowError(w, e)
			return
		}
		rec, e = s.localWorkflowResponse(r.Context(), rec)
		if e != nil {
			s.fail(w, e)
			return
		}
		writeJSON(w, 200, rec)
		return
	}
	tx, err := s.store.db.BeginTx(r.Context(), nil)
	if err != nil {
		s.fail(w, err)
		return
	}
	defer func() { _ = tx.Rollback() }()
	rec, err := loadLocalWorkflow(r.Context(), tx, ws.ID, id)
	if err != nil {
		s.respondLocalWorkflowError(w, err)
		return
	}
	projects, err := localWorkflowProjects(r.Context(), tx, ws.ID, rec)
	if err != nil {
		s.fail(w, err)
		return
	}
	if r.Method == http.MethodDelete {
		if rec.IsDefault {
			s.respondLocalWorkflowError(w, workflowError(400, "workflow_is_default", "the Default workflow cannot be deleted"))
			return
		}
		if len(projects) > 0 {
			writeJSON(w, 409, map[string]any{"error": "projects still use this workflow", "code": "workflow_in_use", "project_count": len(projects)})
			return
		}
		if _, err := tx.ExecContext(r.Context(), `DELETE FROM issue_workflows WHERE workspace_id=? AND id=?`, ws.ID, id); err != nil {
			s.fail(w, err)
			return
		}
		if err := tx.Commit(); err != nil {
			s.fail(w, err)
			return
		}
		s.hub.broadcast("issue_workflow:changed", map[string]any{"action": "deleted"})
		w.WriteHeader(204)
		return
	}
	previousSteps := append([]issueworkflow.Step(nil), rec.Steps...)
	var input localWorkflowWrite
	if err := decodeBody(r, &input); err != nil {
		writeError(w, 400, err)
		return
	}
	if input.Name != nil && !rec.IsDefault {
		rec.Name = *input.Name
	}
	if input.Description != nil {
		rec.Description = *input.Description
	}
	if input.Steps != nil {
		rec.Steps, err = localWorkflowResolveSteps(r.Context(), tx, ws.ID, *input.Steps)
		if err != nil {
			s.respondLocalWorkflowError(w, err)
			return
		}
	}
	rec.Name, rec.Description, rec.Steps = issueworkflow.Normalize(rec.Name, rec.Description, rec.Steps)
	if err := validateLocalWorkflow(r.Context(), tx, ws.ID, rec.Name, rec.Description, rec.Steps, rec.IsDefault, previousSteps); err != nil {
		s.respondLocalWorkflowError(w, err)
		return
	}
	plan, err := localWorkflowPlan(r.Context(), tx, ws.ID, projects, rec)
	if err != nil {
		s.fail(w, err)
		return
	}
	if input.DryRun {
		writeJSON(w, 200, map[string]any{"dry_run": true, "plan": plan})
		return
	}
	if err := localWorkflowValidateMapping(plan, input.StatusMapping, rec); err != nil {
		s.respondLocalWorkflowError(w, err)
		return
	}
	raw, err := issueworkflow.EncodeSteps(rec.Steps)
	if err != nil {
		s.fail(w, err)
		return
	}
	if _, err := tx.ExecContext(r.Context(), `UPDATE issue_workflows SET name=?,description=?,steps=?,updated_at=? WHERE workspace_id=? AND id=?`, rec.Name, rec.Description, string(raw), now(), ws.ID, id); err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			s.respondLocalWorkflowError(w, workflowError(409, "workflow_name_taken", "a workflow with this name already exists"))
		} else {
			s.fail(w, err)
		}
		return
	}
	changed, err := localWorkflowRemapTx(r.Context(), tx, ws.ID, projects, plan, input.StatusMapping)
	if err != nil {
		s.fail(w, err)
		return
	}
	if err := tx.Commit(); err != nil {
		s.fail(w, err)
		return
	}
	for _, issue := range changed {
		s.hub.broadcast("issue:updated", map[string]any{"issue_id": issue})
	}
	s.hub.broadcast("issue_workflow:changed", map[string]any{"action": "updated"})
	s.hub.broadcast("issue_status:changed", map[string]any{"action": "updated"})
	rec, err = loadLocalWorkflow(r.Context(), s.store.db, ws.ID, id)
	if err != nil {
		s.fail(w, err)
		return
	}
	rec.ProjectIDs = projects
	writeJSON(w, 200, rec)
}
func (s *Server) setLocalProjectWorkflow(w http.ResponseWriter, r *http.Request) {
	ws, err := s.workspaceFor(r)
	if err != nil {
		s.respondLocalWorkflowError(w, err)
		return
	}
	var input struct {
		WorkflowID    *string           `json:"workflow_id"`
		StatusMapping map[string]string `json:"status_mapping"`
		DryRun        bool              `json:"dry_run"`
	}
	if err := decodeBody(r, &input); err != nil {
		writeError(w, 400, err)
		return
	}
	tx, err := s.store.db.BeginTx(r.Context(), nil)
	if err != nil {
		s.fail(w, err)
		return
	}
	defer func() { _ = tx.Rollback() }()
	projectID := chi.URLParam(r, "id")
	if _, err := requireProjectTx(r.Context(), tx, ws.ID, projectID); err != nil {
		s.respondLocalWorkflowError(w, err)
		return
	}
	var target localWorkflowRecord
	target, err = localProjectWorkflowTargetTx(r.Context(), tx, ws.ID, input.WorkflowID)
	if err != nil {
		s.respondLocalWorkflowError(w, err)
		return
	}
	plan, err := localWorkflowPlan(r.Context(), tx, ws.ID, []string{projectID}, target)
	if err != nil {
		s.fail(w, err)
		return
	}
	if input.DryRun {
		writeJSON(w, 200, map[string]any{"dry_run": true, "plan": plan})
		return
	}
	if err := localWorkflowValidateMapping(plan, input.StatusMapping, target); err != nil {
		s.respondLocalWorkflowError(w, err)
		return
	}
	var workflowID any
	if !target.IsDefault {
		workflowID = target.ID
	}
	if _, err := tx.ExecContext(r.Context(), `UPDATE projects SET workflow_id=?,updated_at=? WHERE workspace_id=? AND id=?`, workflowID, now(), ws.ID, projectID); err != nil {
		s.fail(w, err)
		return
	}
	changed, err := localWorkflowRemapTx(r.Context(), tx, ws.ID, []string{projectID}, plan, input.StatusMapping)
	if err != nil {
		s.fail(w, err)
		return
	}
	if err := tx.Commit(); err != nil {
		s.fail(w, err)
		return
	}
	for _, id := range changed {
		s.hub.broadcast("issue:updated", map[string]any{"issue_id": id})
	}
	project, err := s.store.GetProject(r.Context(), ws.ID, projectID)
	if err != nil {
		s.fail(w, err)
		return
	}
	s.hub.broadcast("project:updated", map[string]any{"project": project})
	s.hub.broadcast("issue_workflow:changed", map[string]any{"action": "project_updated"})
	writeJSON(w, 200, map[string]any{"project": project, "updated_issues": len(changed), "plan": plan})
}
func localProjectWorkflowTargetTx(ctx context.Context, tx *sql.Tx, ws string, id *string) (localWorkflowRecord, error) {
	if id == nil || strings.TrimSpace(*id) == "" {
		return ensureLocalDefaultWorkflowTx(ctx, tx, ws)
	}
	r, err := loadLocalWorkflow(ctx, tx, ws, *id)
	if errors.Is(err, sql.ErrNoRows) {
		return r, workflowError(400, "invalid_workflow", "workflow not found in this workspace")
	}
	return r, err
}

func (s *Server) setLocalIssueWorkflowHandoff(w http.ResponseWriter, r *http.Request) {
	ws, err := s.workspaceFor(r)
	if err != nil {
		s.respondLocalWorkflowError(w, err)
		return
	}
	var input struct {
		issueworkflow.Step
		CopyForProjectID string `json:"copy_for_project_id"`
	}
	if err := decodeBody(r, &input); err != nil {
		writeError(w, 400, err)
		return
	}
	input.StatusKey = chi.URLParam(r, "status")
	step := issueworkflow.NormalizeHandoff(input.Step)
	tx, err := s.store.db.BeginTx(r.Context(), nil)
	if err != nil {
		s.fail(w, err)
		return
	}
	defer func() { _ = tx.Rollback() }()
	rec, err := loadLocalWorkflow(r.Context(), tx, ws.ID, chi.URLParam(r, "id"))
	if err != nil {
		s.respondLocalWorkflowError(w, err)
		return
	}
	if err := issueworkflow.ValidateHandoff(step, rec.definition()); err != nil {
		s.respondLocalWorkflowError(w, workflowError(400, "invalid_handoff", err.Error()))
		return
	}
	if err := validateLocalWorkflowHandler(r.Context(), tx, ws.ID, step.Handler); err != nil {
		s.respondLocalWorkflowError(w, err)
		return
	}
	for i := range rec.Steps {
		if rec.Steps[i].StatusKey == step.StatusKey {
			rec.Steps[i] = step
		}
	}
	raw, err := issueworkflow.EncodeSteps(rec.Steps)
	if err != nil {
		s.fail(w, err)
		return
	}
	status := 200
	if input.CopyForProjectID != "" {
		if rec.IsDefault {
			s.respondLocalWorkflowError(w, workflowError(400, "invalid_handoff", "the Default workflow cannot be copied for one project"))
			return
		}
		project, err := scanProject(tx.QueryRowContext(r.Context(), projectSelect+` WHERE p.workspace_id=? AND p.id=?`, ws.ID, input.CopyForProjectID))
		if err != nil {
			s.respondLocalWorkflowError(w, err)
			return
		}
		if project.WorkflowID == nil || *project.WorkflowID != rec.ID {
			s.respondLocalWorkflowError(w, workflowError(409, "invalid_handoff", "the project does not use this workflow"))
			return
		}
		var candidate string
		for n := 1; ; n++ {
			suffix := " (" + project.Title + ")"
			if n > 1 {
				suffix = fmt.Sprintf(" (%s %d)", project.Title, n)
			}
			nameRunes := []rune(rec.Name)
			suffixRunes := []rune(suffix)
			if len(suffixRunes) > 48 {
				suffixRunes = append(suffixRunes[:40], []rune(fmt.Sprintf(" %d)", n))...)
			}
			if len(nameRunes)+len(suffixRunes) > 64 {
				nameRunes = nameRunes[:64-len(suffixRunes)]
			}
			candidate = string(nameRunes) + string(suffixRunes)
			var count int
			if err := tx.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM issue_workflows WHERE workspace_id=? AND name=? COLLATE NOCASE`, ws.ID, candidate).Scan(&count); err != nil {
				s.fail(w, err)
				return
			}
			if count == 0 {
				break
			}
			if n > 1000 {
				s.respondLocalWorkflowError(w, workflowError(409, "workflow_name_taken", "cannot create a unique project workflow name"))
				return
			}
		}
		rec.ID, rec.Name, rec.OwnerID, rec.CreatedAt, rec.UpdatedAt = uuid.NewString(), candidate, localWorkflowStringPointer(LocalOperatorID), now(), now()
		if _, err := tx.ExecContext(r.Context(), `INSERT INTO issue_workflows(id,workspace_id,name,description,is_default,owner_id,steps,created_at,updated_at) VALUES(?,?,?,?,0,?,?,?,?)`, rec.ID, ws.ID, rec.Name, rec.Description, LocalOperatorID, string(raw), rec.CreatedAt, rec.UpdatedAt); err != nil {
			s.fail(w, err)
			return
		}
		if _, err := tx.ExecContext(r.Context(), `UPDATE projects SET workflow_id=?,updated_at=? WHERE id=? AND workspace_id=?`, rec.ID, now(), project.ID, ws.ID); err != nil {
			s.fail(w, err)
			return
		}
		status = 201
	} else {
		if _, err := tx.ExecContext(r.Context(), `UPDATE issue_workflows SET steps=?,updated_at=? WHERE workspace_id=? AND id=?`, string(raw), now(), ws.ID, rec.ID); err != nil {
			s.fail(w, err)
			return
		}
	}
	if err := tx.Commit(); err != nil {
		s.fail(w, err)
		return
	}
	s.hub.broadcast("issue_workflow:changed", map[string]any{"action": "updated"})
	if input.CopyForProjectID != "" {
		project, e := s.store.GetProject(r.Context(), ws.ID, input.CopyForProjectID)
		if e != nil {
			s.fail(w, e)
			return
		}
		s.hub.broadcast("project:updated", map[string]any{"project": project})
	}
	rec, err = loadLocalWorkflow(r.Context(), s.store.db, ws.ID, rec.ID)
	if err != nil {
		s.fail(w, err)
		return
	}
	rec, err = s.localWorkflowResponse(r.Context(), rec)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, status, rec)
}
func localWorkflowStringPointer(s string) *string { return &s }

type localWorkflowIssue struct {
	ID, WorkspaceID, Status, CreatorType, CreatorID, Identifier string
	ProjectID, AssigneeType, AssigneeID                         sql.NullString
}

func localWorkflowIssueFor(ctx context.Context, q localWakeupQuery, ws, id string) (localWorkflowIssue, error) {
	var i localWorkflowIssue
	var prefix string
	var n int64
	err := q.QueryRowContext(ctx, `SELECT i.id,i.workspace_id,i.status,i.project_id,i.assignee_type,i.assignee_id,i.creator_type,i.creator_id,w.issue_prefix,i.number FROM issues i JOIN workspaces w ON w.id=i.workspace_id WHERE i.workspace_id=? AND (i.id=? OR upper(w.issue_prefix || '-' || i.number)=upper(?)) LIMIT 1`, ws, id, id).Scan(&i.ID, &i.WorkspaceID, &i.Status, &i.ProjectID, &i.AssigneeType, &i.AssigneeID, &i.CreatorType, &i.CreatorID, &prefix, &n)
	i.Identifier = fmt.Sprintf("%s-%d", prefix, n)
	return i, err
}
func localWorkflowForProjectTx(ctx context.Context, tx *sql.Tx, ws, projectID string) (localWorkflowRecord, Project, error) {
	p, err := scanProject(tx.QueryRowContext(ctx, projectSelect+` WHERE p.workspace_id=? AND p.id=?`, ws, projectID))
	if err != nil {
		return localWorkflowRecord{}, p, err
	}
	if p.WorkflowID != nil {
		rec, err := loadLocalWorkflow(ctx, tx, ws, *p.WorkflowID)
		if err == nil {
			return rec, p, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return rec, p, err
		}
	}
	rec, err := ensureLocalDefaultWorkflowTx(ctx, tx, ws)
	return rec, p, err
}
func localWorkflowResolveHandler(ctx context.Context, q localWakeupQuery, ws string, step issueworkflow.Step, p Project, i localWorkflowIssue) (string, string, error) {
	kind, id := step.Handler.Type, step.Handler.ID
	switch kind {
	case "none":
		return "", "", nil
	case "project_lead":
		if p.LeadType == nil || p.LeadID == nil {
			return "", "", nil
		}
		kind, id = *p.LeadType, *p.LeadID
	case "creator":
		kind, id = i.CreatorType, i.CreatorID
	}
	if err := validateLocalWorkflowHandler(ctx, q, ws, issueworkflow.Handler{Type: kind, ID: id}); err != nil {
		return "", "", err
	}
	return kind, id, nil
}
func localWorkflowActorName(ctx context.Context, q localWakeupQuery, ws, kind, id string) string {
	var name string
	var err error
	switch kind {
	case "agent":
		err = q.QueryRowContext(ctx, `SELECT name FROM agents WHERE workspace_id=? AND id=?`, ws, id).Scan(&name)
	case "squad":
		err = q.QueryRowContext(ctx, `SELECT name FROM squads WHERE workspace_id=? AND id=?`, ws, id).Scan(&name)
	case "member":
		if id == LocalOperatorID {
			err = q.QueryRowContext(ctx, `SELECT name FROM users WHERE id=?`, id).Scan(&name)
		}
	}
	if err != nil || name == "" {
		return kind
	}
	return name
}
func localWorkflowBrief(ctx context.Context, q localWakeupQuery, ws string, rec localWorkflowRecord, p Project, i localWorkflowIssue, observer bool) (string, error) {
	catalog, _, err := localWorkflowCatalog(ctx, q, ws)
	if err != nil {
		return "", err
	}
	nameOf := func(step issueworkflow.Step) string {
		kind, id, e := localWorkflowResolveHandler(ctx, q, ws, step, p, i)
		if e != nil {
			return ""
		}
		if kind == "" {
			if step.Handler.Type == "project_lead" {
				return "the project lead (none set)"
			}
			return ""
		}
		return localWorkflowActorName(ctx, q, ws, kind, id)
	}
	assignee := ""
	if i.AssigneeType.Valid && i.AssigneeID.Valid {
		assignee = localWorkflowActorName(ctx, q, ws, i.AssigneeType.String, i.AssigneeID.String)
	}
	return issueworkflow.Brief(issueworkflow.BriefInput{Workflow: rec.definition(), ProjectName: p.Title, StatusKey: i.Status, IssueIdentifier: i.Identifier, StatusName: func(key string) string {
		if e, ok := catalog[key]; ok {
			return e.Name
		}
		return key
	}, HandlerName: nameOf, Observer: observer, AssigneeName: assignee}), nil
}

// The issue writer owns enqueueing after its row update in the same transaction.
type localWorkflowEntry struct {
	AgentID, Brief, WorkflowID, StatusKey string
	CancelledTaskIDs                      []string
}

func applyLocalIssueWorkflowEntryTx(ctx context.Context, tx *sql.Tx, ws, issueID string, updates map[string]any) (*localWorkflowEntry, error) {
	prev, err := localWorkflowIssueFor(ctx, tx, ws, issueID)
	if err != nil {
		return nil, err
	}
	projectID := prev.ProjectID.String
	if value, exists := updates["project_id"]; exists {
		next, ok := value.(string)
		if !ok || strings.TrimSpace(next) == "" {
			return nil, ErrIssueProjectRequired
		}
		next = strings.TrimSpace(next)
		if prev.ProjectID.Valid && next != projectID {
			return nil, ErrIssueProjectImmutable
		}
		projectID = next
	}
	if projectID == "" {
		return nil, ErrIssueProjectRequired
	}
	rec, p, err := localWorkflowForProjectTx(ctx, tx, ws, projectID)
	if err != nil {
		return nil, err
	}
	def := rec.definition()
	statusExplicit := false
	status := prev.Status
	if value, exists := updates["status"]; exists {
		next, ok := value.(string)
		if !ok || strings.TrimSpace(next) == "" {
			return nil, workflowError(400, "invalid_workflow", "status is required")
		}
		statusExplicit = true
		status = strings.TrimSpace(next)
	}
	moving := projectID != prev.ProjectID.String
	if moving && !statusExplicit && !def.IsDefault && !def.Has(status) {
		catalog, _, e := localWorkflowCatalog(ctx, tx, ws)
		if e != nil {
			return nil, e
		}
		status = issueworkflow.MoveTarget(def, status, func(key string) string { return catalog[key].Category })
		updates["status"] = status
	}
	if statusExplicit || moving {
		catalog, _, err := localWorkflowCatalog(ctx, tx, ws)
		if err != nil {
			return nil, err
		}
		statusEntry, exists := catalog[status]
		if !exists || (statusEntry.Archived && status != prev.Status) {
			return nil, workflowError(400, "invalid_workflow", "status does not exist or is archived")
		}
		if err := issueworkflow.CheckStatus(&def, status); err != nil {
			return nil, err
		}
	}
	sourceTaskID, _ := updates["_workflow_source_task_id"].(string)
	if sourceTaskID != "" && statusExplicit && status != prev.Status && (!def.IsDefault || def.HandsOffAnywhere()) {
		var step, workflow string
		err := tx.QueryRowContext(ctx, `SELECT COALESCE(workflow_step,''),COALESCE(workflow_id,'') FROM task_runs WHERE id=? AND issue_id=? AND status='running'`, sourceTaskID, prev.ID).Scan(&step, &workflow)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		if err == nil && step != "" && (step != prev.Status || (workflow != "" && workflow != rec.ID)) {
			return nil, &issueworkflow.StepMovedError{IssueIdentifier: prev.Identifier, Step: step, Current: prev.Status}
		}
	}
	_, typeTouched := updates["assignee_type"]
	_, idTouched := updates["assignee_id"]
	if typeTouched || idTouched {
		return nil, nil
	}
	enterMove, _ := updates["enter_workflow_step"].(bool)
	if (!moving && (!statusExplicit || status == prev.Status)) || (moving && !enterMove) {
		return nil, nil
	}
	step, ok := def.Step(status)
	if !ok || !step.HandsOff() || !issueworkflow.AcceptsHandoff(status) {
		return nil, nil
	}
	kind, id, err := localWorkflowResolveHandler(ctx, tx, ws, step, p, prev)
	if err != nil {
		return nil, err
	}
	if kind == "" {
		return nil, nil
	}
	updates["assignee_type"], updates["assignee_id"] = kind, id
	entry := &localWorkflowEntry{WorkflowID: rec.ID, StatusKey: status, CancelledTaskIDs: []string{}}
	switch kind {
	case "agent":
		entry.AgentID = id
	case "squad":
		if err := tx.QueryRowContext(ctx, `SELECT leader_id FROM squads WHERE workspace_id=? AND id=? AND archived_at IS NULL`, ws, id).Scan(&entry.AgentID); err != nil {
			return nil, err
		}
	}
	next := prev
	next.Status = status
	next.ProjectID = sql.NullString{String: projectID, Valid: true}
	next.AssigneeType = sql.NullString{String: kind, Valid: true}
	next.AssigneeID = sql.NullString{String: id, Valid: true}
	if entry.AgentID != "" {
		entry.Brief, err = localWorkflowBrief(ctx, tx, ws, rec, p, next, false)
		if err != nil {
			return nil, err
		}
	}
	stop, _ := updates["stop_previous_assignee_runs"].(bool)
	if stop && (kind != prev.AssigneeType.String || id != prev.AssigneeID.String) && (prev.AssigneeType.String == "agent" || prev.AssigneeType.String == "squad") {
		column := "agent_id"
		if prev.AssigneeType.String == "squad" {
			column = "squad_id"
		}
		rows, e := tx.QueryContext(ctx, `UPDATE task_runs SET status='cancelled',completed_at=?,updated_at=? WHERE issue_id=? AND `+column+`=? AND status NOT IN ('completed','failed','cancelled') AND id<>? RETURNING id`, now(), now(), prev.ID, prev.AssigneeID.String, sourceTaskID)
		if e != nil {
			return nil, e
		}
		for rows.Next() {
			var id string
			if e := rows.Scan(&id); e != nil {
				_ = rows.Close()
				return nil, e
			}
			entry.CancelledTaskIDs = append(entry.CancelledTaskIDs, id)
		}
		if e := rows.Err(); e != nil {
			_ = rows.Close()
			return nil, e
		}
		if e := rows.Close(); e != nil {
			return nil, e
		}
	}
	return entry, nil
}
func prepareLocalIssueWorkflowCreateTx(ctx context.Context, tx *sql.Tx, ws string, input *CreateIssueInput) (*localWorkflowEntry, error) {
	if input.ProjectID == nil || strings.TrimSpace(*input.ProjectID) == "" {
		return nil, ErrIssueProjectRequired
	}
	rec, p, err := localWorkflowForProjectTx(ctx, tx, ws, *input.ProjectID)
	if err != nil {
		return nil, err
	}
	if input.Status == "" {
		input.Status = "backlog"
		if !rec.IsDefault {
			input.Status = issueworkflow.StartStatus
		}
	}
	def := rec.definition()
	catalog, _, err := localWorkflowCatalog(ctx, tx, ws)
	if err != nil {
		return nil, err
	}
	statusEntry, exists := catalog[input.Status]
	if !exists || statusEntry.Archived {
		return nil, workflowError(400, "invalid_workflow", "status does not exist or is archived")
	}
	if err := issueworkflow.CheckStatus(&def, input.Status); err != nil {
		return nil, err
	}
	if input.AssigneeType != nil || input.AssigneeID != nil {
		return nil, nil
	}
	step, ok := def.Step(input.Status)
	if !ok || !step.HandsOff() || !issueworkflow.AcceptsHandoff(input.Status) {
		return nil, nil
	}
	var n int64
	var prefix string
	if err := tx.QueryRowContext(ctx, `SELECT w.issue_prefix,(SELECT COALESCE(MAX(number),0)+1 FROM issues WHERE workspace_id=w.id) FROM workspaces w WHERE w.id=?`, ws).Scan(&prefix, &n); err != nil {
		return nil, err
	}
	issue := localWorkflowIssue{WorkspaceID: ws, Status: input.Status, CreatorType: "member", CreatorID: LocalOperatorID, Identifier: fmt.Sprintf("%s-%d", prefix, n), ProjectID: sql.NullString{String: *input.ProjectID, Valid: true}}
	kind, id, err := localWorkflowResolveHandler(ctx, tx, ws, step, p, issue)
	if err != nil {
		return nil, err
	}
	if kind == "" {
		return nil, nil
	}
	input.AssigneeType, input.AssigneeID = localWorkflowStringPointer(kind), localWorkflowStringPointer(id)
	issue.AssigneeType = sql.NullString{String: kind, Valid: true}
	issue.AssigneeID = sql.NullString{String: id, Valid: true}
	entry := &localWorkflowEntry{WorkflowID: rec.ID, StatusKey: input.Status, CancelledTaskIDs: []string{}}
	if kind == "agent" {
		entry.AgentID = id
	} else if kind == "squad" {
		if err := tx.QueryRowContext(ctx, `SELECT leader_id FROM squads WHERE workspace_id=? AND id=? AND archived_at IS NULL`, ws, id).Scan(&entry.AgentID); err != nil {
			return nil, err
		}
	}
	if entry.AgentID != "" {
		entry.Brief, err = localWorkflowBrief(ctx, tx, ws, rec, p, issue, false)
	}
	return entry, err
}
func (s *Server) previewLocalIssueWorkflowBrief(w http.ResponseWriter, r *http.Request) {
	ws, err := s.workspaceFor(r)
	if err != nil {
		s.respondLocalWorkflowError(w, err)
		return
	}
	var input struct {
		issueworkflow.Step
		ProjectID string `json:"project_id"`
	}
	if err := decodeBody(r, &input); err != nil {
		writeError(w, 400, err)
		return
	}
	tx, err := s.store.db.BeginTx(r.Context(), nil)
	if err != nil {
		s.fail(w, err)
		return
	}
	defer func() { _ = tx.Rollback() }()
	rec, err := loadLocalWorkflow(r.Context(), tx, ws.ID, chi.URLParam(r, "id"))
	if err != nil {
		s.respondLocalWorkflowError(w, err)
		return
	}
	step := issueworkflow.NormalizeHandoff(input.Step)
	if err := issueworkflow.ValidateHandoff(step, rec.definition()); err != nil {
		s.respondLocalWorkflowError(w, workflowError(400, "invalid_handoff", err.Error()))
		return
	}
	if err := validateLocalWorkflowHandler(r.Context(), tx, ws.ID, step.Handler); err != nil {
		s.respondLocalWorkflowError(w, err)
		return
	}
	for i := range rec.Steps {
		if rec.Steps[i].StatusKey == step.StatusKey {
			rec.Steps[i] = step
		}
	}
	p := Project{}
	if input.ProjectID != "" {
		p, err = scanProject(tx.QueryRowContext(r.Context(), projectSelect+` WHERE p.workspace_id=? AND p.id=?`, ws.ID, input.ProjectID))
		if err != nil {
			s.respondLocalWorkflowError(w, err)
			return
		}
	}
	issue := localWorkflowIssue{Status: step.StatusKey, Identifier: ws.IssuePrefix + "-123", CreatorType: "member", CreatorID: LocalOperatorID}
	brief, err := localWorkflowBrief(r.Context(), tx, ws.ID, rec, p, issue, false)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"brief": brief})
}
func (s *Server) previewLocalWorkflowHandoff(w http.ResponseWriter, r *http.Request) {
	ws, err := s.workspaceFor(r)
	if err != nil {
		s.respondLocalWorkflowError(w, err)
		return
	}
	status, projectParam := r.URL.Query().Get("status"), r.URL.Query().Get("project_id")
	if status == "" && projectParam == "" {
		writeError(w, 400, errors.New("status is required"))
		return
	}
	tx, err := s.store.db.BeginTx(r.Context(), nil)
	if err != nil {
		s.fail(w, err)
		return
	}
	defer func() { _ = tx.Rollback() }()
	prev, err := localWorkflowIssueFor(r.Context(), tx, ws.ID, chi.URLParam(r, "id"))
	if err != nil {
		s.respondLocalWorkflowError(w, err)
		return
	}
	updates := map[string]any{}
	if status != "" {
		updates["status"] = status
	}
	if projectParam != "" {
		updates["project_id"], updates["enter_workflow_step"] = projectParam, true
	}
	entry, err := applyLocalIssueWorkflowEntryTx(r.Context(), tx, ws.ID, prev.ID, updates)
	if err != nil {
		s.respondLocalWorkflowError(w, err)
		return
	}
	target := prev.Status
	if v, ok := updates["status"].(string); ok {
		target = v
	}
	out := map[string]any{"handoff": entry != nil, "workflow_name": "", "from_status": prev.Status, "to_status": target, "handler_type": nil, "handler_id": nil, "previous_assignee_type": stringPtr(prev.AssigneeType), "previous_assignee_id": stringPtr(prev.AssigneeID), "previous_runs": []any{}, "brief": ""}
	if entry != nil {
		rec, err := loadLocalWorkflow(r.Context(), tx, ws.ID, entry.WorkflowID)
		if err != nil {
			s.fail(w, err)
			return
		}
		out["workflow_name"], out["handler_type"], out["handler_id"], out["brief"] = rec.Name, updates["assignee_type"], updates["assignee_id"], entry.Brief
		if updates["assignee_type"] != prev.AssigneeType.String || updates["assignee_id"] != prev.AssigneeID.String {
			column := "agent_id"
			if prev.AssigneeType.String == "squad" {
				column = "squad_id"
			}
			if prev.AssigneeType.String == "agent" || prev.AssigneeType.String == "squad" {
				rows, e := tx.QueryContext(r.Context(), `SELECT id,COALESCE(agent_id,''),status,started_at FROM task_runs WHERE issue_id=? AND `+column+`=? AND status NOT IN ('completed','failed','cancelled') ORDER BY created_at,id`, prev.ID, prev.AssigneeID.String)
				if e != nil {
					s.fail(w, e)
					return
				}
				runs := []any{}
				for rows.Next() {
					var id, agent, state string
					var started sql.NullString
					if e := rows.Scan(&id, &agent, &state, &started); e != nil {
						_ = rows.Close()
						s.fail(w, e)
						return
					}
					runs = append(runs, map[string]any{"task_id": id, "agent_id": agent, "status": state, "started_at": stringPtr(started)})
				}
				if e := rows.Err(); e != nil {
					_ = rows.Close()
					s.fail(w, e)
					return
				}
				if e := rows.Close(); e != nil {
					s.fail(w, e)
					return
				}
				out["previous_runs"] = runs
			}
		}
	}
	writeJSON(w, 200, out)
}

// Claims preserve the step/brief of a queued handoff. Ordinary runs acquire
// their current project's workflow context once, without duplicating prompts.
func ensureLocalTaskWorkflowContextTx(ctx context.Context, tx *sql.Tx, ws, issueID, taskID string) error {
	issue, err := localWorkflowIssueFor(ctx, tx, ws, issueID)
	if err != nil {
		return err
	}
	if !issue.ProjectID.Valid {
		return nil
	}
	rec, p, err := localWorkflowForProjectTx(ctx, tx, ws, issue.ProjectID.String)
	if err != nil {
		return err
	}
	def := rec.definition()
	var agent, squad, note, workflow, step sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT agent_id,squad_id,handoff_note,workflow_id,workflow_step FROM task_runs WHERE id=? AND issue_id=?`, taskID, issue.ID).Scan(&agent, &squad, &note, &workflow, &step); err != nil {
		return err
	}
	if workflow.Valid {
		if workflow.String != rec.ID || (step.Valid && step.String != issue.Status) {
			return &issueworkflow.StepMovedError{IssueIdentifier: issue.Identifier, Step: step.String, Current: issue.Status}
		}
		return nil
	}
	if def.IsDefault && !def.HandsOffAnywhere() {
		return nil
	}
	assigned := (issue.AssigneeType.String == "agent" && issue.AssigneeID.String == agent.String) || (issue.AssigneeType.String == "squad" && issue.AssigneeID.String == squad.String)
	brief, err := localWorkflowBrief(ctx, tx, ws, rec, p, issue, !assigned)
	if err != nil {
		return err
	}
	if note.Valid && strings.TrimSpace(note.String) != "" {
		brief = note.String + "\n\n" + brief
	}
	var status any
	if assigned {
		status = issue.Status
	}
	_, err = tx.ExecContext(ctx, `UPDATE task_runs SET workflow_id=?,workflow_step=?,handoff_note=? WHERE id=? AND issue_id=? AND workflow_id IS NULL`, rec.ID, status, brief, taskID, issue.ID)
	return err
}
