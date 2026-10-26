package localworkbench

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/service"
)

func (s *Server) workspaceFor(r *http.Request) (Workspace, error) {
	workspaces, err := s.store.ListWorkspaces(r.Context())
	if err != nil {
		return Workspace{}, err
	}
	if len(workspaces) == 0 {
		return Workspace{}, sql.ErrNoRows
	}
	return workspaces[0], nil
}

func (s *Server) listWorkspaces(w http.ResponseWriter, r *http.Request) {
	items, err := s.store.ListWorkspaces(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, items)
}

func (s *Server) getWorkspace(w http.ResponseWriter, r *http.Request) {
	item, err := s.workspaceFor(r)
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	requested := chi.URLParam(r, "id")
	if requested != item.ID && requested != item.Slug {
		writeError(w, http.StatusNotFound, sql.ErrNoRows)
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func (s *Server) updateWorkspace(w http.ResponseWriter, r *http.Request) {
	item, err := s.workspaceFor(r)
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	requested := chi.URLParam(r, "id")
	if requested != item.ID && requested != item.Slug {
		writeError(w, http.StatusNotFound, sql.ErrNoRows)
		return
	}
	var input map[string]any
	if err := decodeBody(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	set, args := []string{"updated_at=?"}, []any{now()}
	for _, key := range []string{"name", "description", "context", "issue_prefix", "avatar_url"} {
		if value, ok := input[key]; ok {
			set = append(set, key+"=?")
			args = append(args, value)
		}
	}
	for _, key := range []string{"settings", "repos"} {
		if value, ok := input[key]; ok {
			raw, _ := json.Marshal(value)
			set = append(set, key+"=?")
			args = append(args, string(raw))
		}
	}
	args = append(args, item.ID)
	if _, err := s.store.db.ExecContext(r.Context(), "UPDATE workspaces SET "+strings.Join(set, ", ")+" WHERE id=?", args...); err != nil {
		s.fail(w, err)
		return
	}
	updated, err := s.store.GetWorkspace(r.Context(), item.ID)
	if err != nil {
		s.fail(w, err)
		return
	}
	s.hub.broadcast("workspace:updated", map[string]any{"workspace": updated})
	writeJSON(w, http.StatusOK, updated)
}

func (s *Server) listProjects(w http.ResponseWriter, r *http.Request) {
	ws, err := s.workspaceFor(r)
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	items, err := s.store.ListProjects(r.Context(), ws.ID)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"projects": items, "total": len(items)})
}

func (s *Server) searchProjects(w http.ResponseWriter, r *http.Request) {
	ws, err := s.workspaceFor(r)
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	items, err := s.store.ListProjects(r.Context(), ws.ID)
	if err != nil {
		s.fail(w, err)
		return
	}
	q := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))
	result := []map[string]any{}
	for _, item := range items {
		if q == "" || strings.Contains(strings.ToLower(item.Title), q) {
			raw, _ := json.Marshal(item)
			var value map[string]any
			_ = json.Unmarshal(raw, &value)
			value["match_source"] = "title"
			result = append(result, value)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"projects": result, "total": len(result)})
}

func (s *Server) createProject(w http.ResponseWriter, r *http.Request) {
	ws, err := s.workspaceFor(r)
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	var input struct {
		WorkflowID  *string                `json:"workflow_id"`
		Title       string                 `json:"title"`
		Description string                 `json:"description"`
		Icon        string                 `json:"icon"`
		Status      string                 `json:"status"`
		Priority    string                 `json:"priority"`
		Resources   []ProjectResourceInput `json:"resources"`
	}
	if err := decodeBody(r, &input); err != nil || strings.TrimSpace(input.Title) == "" {
		writeError(w, http.StatusBadRequest, errors.New("title is required"))
		return
	}
	item, err := s.store.CreateProject(r.Context(), ws.ID, CreateProjectInput{WorkflowID: input.WorkflowID,
		Title: input.Title, Description: &input.Description, Icon: &input.Icon,
		Status: input.Status, Priority: input.Priority, Resources: input.Resources,
	})
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	s.hub.broadcast("project:created", map[string]any{"project": item})
	writeJSON(w, http.StatusCreated, item)
}

func (s *Server) getProject(w http.ResponseWriter, r *http.Request) {
	ws, err := s.workspaceFor(r)
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	item, err := s.store.GetProject(r.Context(), ws.ID, chi.URLParam(r, "id"))
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func (s *Server) updateProject(w http.ResponseWriter, r *http.Request) {
	ws, err := s.workspaceFor(r)
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	id := chi.URLParam(r, "id")
	if _, err := s.store.GetProject(r.Context(), ws.ID, id); err != nil {
		s.respondStoreError(w, err)
		return
	}
	var input map[string]any
	if err := decodeBody(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	set, args := []string{"updated_at=?"}, []any{now()}
	for _, key := range []string{"title", "description", "icon", "status", "priority", "lead_type", "lead_id", "start_date", "due_date"} {
		if value, ok := input[key]; ok {
			set = append(set, key+"=?")
			args = append(args, value)
		}
	}
	args = append(args, id)
	if _, err := s.store.db.ExecContext(r.Context(), "UPDATE projects SET "+strings.Join(set, ", ")+" WHERE id=?", args...); err != nil {
		s.fail(w, err)
		return
	}
	item, err := s.store.GetProject(r.Context(), ws.ID, id)
	if err != nil {
		s.fail(w, err)
		return
	}
	s.hub.broadcast("project:updated", map[string]any{"project": item})
	writeJSON(w, http.StatusOK, item)
}

func (s *Server) deleteProject(w http.ResponseWriter, r *http.Request) {
	ws, err := s.workspaceFor(r)
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	id := chi.URLParam(r, "id")
	if _, err := s.store.GetProject(r.Context(), ws.ID, id); err != nil {
		s.respondStoreError(w, err)
		return
	}
	tx, err := s.store.db.BeginTx(r.Context(), nil)
	if err != nil {
		s.fail(w, err)
		return
	}
	defer func() { _ = tx.Rollback() }()
	var issueCount int
	if err := tx.QueryRowContext(r.Context(), "SELECT COUNT(*) FROM issues WHERE workspace_id=? AND project_id=?", ws.ID, id).Scan(&issueCount); err != nil {
		s.fail(w, err)
		return
	}
	if issueCount > 0 {
		s.respondStoreError(w, ErrProjectHasIssues)
		return
	}
	if _, err := tx.ExecContext(r.Context(), "DELETE FROM project_resources WHERE workspace_id=? AND project_id=?", ws.ID, id); err != nil {
		s.fail(w, err)
		return
	}
	if _, err := tx.ExecContext(r.Context(), "DELETE FROM projects WHERE workspace_id=? AND id=?", ws.ID, id); err != nil {
		s.fail(w, err)
		return
	}
	if err := tx.Commit(); err != nil {
		s.fail(w, err)
		return
	}
	s.hub.broadcast("project:deleted", map[string]any{"project_id": id})
	w.WriteHeader(http.StatusNoContent)
}

type projectResource struct {
	ID, ProjectID, WorkspaceID, ResourceType string
	ResourceRef                              map[string]any
	Label                                    *string
	Position                                 int
	CreatedAt                                string
	CreatedBy                                *string
}

func (p projectResource) MarshalJSON() ([]byte, error) {
	return json.Marshal(map[string]any{"id": p.ID, "project_id": p.ProjectID, "workspace_id": p.WorkspaceID, "resource_type": p.ResourceType, "resource_ref": p.ResourceRef, "label": p.Label, "position": p.Position, "created_at": p.CreatedAt, "created_by": p.CreatedBy})
}

func (s *Server) getProjectResource(ctx context.Context, projectID, resourceID string) (projectResource, error) {
	var item projectResource
	var raw string
	var label, createdBy sql.NullString
	err := s.store.db.QueryRowContext(ctx, `SELECT id, project_id, workspace_id, resource_type, resource_ref, label, position, created_at, created_by FROM project_resources WHERE id=? AND project_id=?`, resourceID, projectID).Scan(&item.ID, &item.ProjectID, &item.WorkspaceID, &item.ResourceType, &raw, &label, &item.Position, &item.CreatedAt, &createdBy)
	item.ResourceRef = decodeObject(raw)
	item.Label = stringPtr(label)
	item.CreatedBy = stringPtr(createdBy)
	return item, err
}

func (s *Server) listProjectResources(w http.ResponseWriter, r *http.Request) {
	ws, err := s.workspaceFor(r)
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	projectID := chi.URLParam(r, "id")
	if _, err := s.store.GetProject(r.Context(), ws.ID, projectID); err != nil {
		s.respondStoreError(w, err)
		return
	}
	rows, err := s.store.db.QueryContext(r.Context(), `SELECT id, project_id, workspace_id, resource_type, resource_ref, label, position, created_at, created_by FROM project_resources WHERE workspace_id=? AND project_id=? ORDER BY position`, ws.ID, projectID)
	if err != nil {
		s.fail(w, err)
		return
	}
	defer rows.Close()
	items := []projectResource{}
	for rows.Next() {
		var item projectResource
		var raw string
		var label, createdBy sql.NullString
		if err := rows.Scan(&item.ID, &item.ProjectID, &item.WorkspaceID, &item.ResourceType, &raw, &label, &item.Position, &item.CreatedAt, &createdBy); err != nil {
			s.fail(w, err)
			return
		}
		item.ResourceRef = decodeObject(raw)
		item.Label = stringPtr(label)
		item.CreatedBy = stringPtr(createdBy)
		items = append(items, item)
	}
	writeJSON(w, http.StatusOK, map[string]any{"resources": items, "total": len(items)})
}

func (s *Server) createProjectResource(w http.ResponseWriter, r *http.Request) {
	ws, err := s.workspaceFor(r)
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	project, err := s.store.GetProject(r.Context(), ws.ID, chi.URLParam(r, "id"))
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	var input struct {
		ResourceType string         `json:"resource_type"`
		ResourceRef  map[string]any `json:"resource_ref"`
		Label        string         `json:"label"`
		Position     int            `json:"position"`
	}
	if err := decodeBody(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	raw, _ := json.Marshal(input.ResourceRef)
	id, stamp := uuid.NewString(), now()
	_, err = s.store.db.ExecContext(r.Context(), `INSERT INTO project_resources(id, project_id, workspace_id, resource_type, resource_ref, label, position, created_at, created_by) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, id, project.ID, project.WorkspaceID, input.ResourceType, string(raw), nullable(input.Label), input.Position, stamp, LocalActorID)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"id": id, "project_id": project.ID, "workspace_id": project.WorkspaceID, "resource_type": input.ResourceType, "resource_ref": input.ResourceRef, "label": nullableJSON(input.Label), "position": input.Position, "created_at": stamp, "created_by": LocalActorID})
}

func nullableJSON(value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return value
}

func (s *Server) updateProjectResource(w http.ResponseWriter, r *http.Request) {
	ws, err := s.workspaceFor(r)
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	projectID := chi.URLParam(r, "id")
	if _, err := s.store.GetProject(r.Context(), ws.ID, projectID); err != nil {
		s.respondStoreError(w, err)
		return
	}
	var input map[string]any
	if err := decodeBody(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	set, args := []string{}, []any{}
	for _, key := range []string{"label", "position"} {
		if value, ok := input[key]; ok {
			set = append(set, key+"=?")
			args = append(args, value)
		}
	}
	if value, ok := input["resource_ref"]; ok {
		raw, _ := json.Marshal(value)
		set = append(set, "resource_ref=?")
		args = append(args, string(raw))
	}
	if len(set) == 0 {
		writeError(w, http.StatusBadRequest, errors.New("no fields to update"))
		return
	}
	args = append(args, chi.URLParam(r, "resourceID"), chi.URLParam(r, "id"))
	if _, err := s.store.db.ExecContext(r.Context(), "UPDATE project_resources SET "+strings.Join(set, ", ")+" WHERE id=? AND project_id=? AND workspace_id=?", append(args, ws.ID)...); err != nil {
		s.fail(w, err)
		return
	}
	item, err := s.getProjectResource(r.Context(), chi.URLParam(r, "id"), chi.URLParam(r, "resourceID"))
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func (s *Server) deleteProjectResource(w http.ResponseWriter, r *http.Request) {
	ws, err := s.workspaceFor(r)
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	projectID := chi.URLParam(r, "id")
	if _, err := s.store.GetProject(r.Context(), ws.ID, projectID); err != nil {
		s.respondStoreError(w, err)
		return
	}
	_, err = s.store.db.ExecContext(r.Context(), "DELETE FROM project_resources WHERE id=? AND project_id=? AND workspace_id=?", chi.URLParam(r, "resourceID"), projectID, ws.ID)
	if err != nil {
		s.fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func projectIDFromRequest(r *http.Request) string {
	if value := r.URL.Query().Get("project_ids"); value != "" {
		return strings.Split(value, ",")[0]
	}
	return ""
}

func (s *Server) listGroupedIssues(w http.ResponseWriter, r *http.Request) {
	ws, err := s.workspaceFor(r)
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	items, err := s.store.ListIssues(r.Context(), ws.ID, projectIDFromRequest(r), "")
	if err != nil {
		s.fail(w, err)
		return
	}
	if category := r.URL.Query().Get("status_category"); category != "" {
		filtered := items[:0]
		for _, item := range items {
			if localStatusCategoryMatches(item.StatusCategory, category) {
				filtered = append(filtered, item)
			}
		}
		items = filtered
	}
	writeJSON(w, http.StatusOK, map[string]any{"groups": []any{map[string]any{"id": "unassigned", "assignee_type": nil, "assignee_id": nil, "issues": items, "total": len(items)}}})
}

type tableRequest struct {
	Query struct {
		Scope struct {
			Kind      string `json:"kind"`
			ProjectID string `json:"project_id"`
		} `json:"scope"`
		Filters struct {
			Statuses   []string `json:"statuses"`
			Priorities []string `json:"priorities"`
			ProjectIDs []string `json:"project_ids"`
		} `json:"filters"`
		Search string `json:"search"`
	} `json:"query"`
	Group struct {
		Kind string `json:"kind"`
	} `json:"group"`
	Facets []struct {
		Kind string `json:"kind"`
	} `json:"facets"`
	GroupKey *string `json:"group_key"`
	ParentID *string `json:"parent_id"`
}

func contains(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}

func issueTableGroupKey(kind string, item Issue) string {
	switch kind {
	case "project":
		if item.ProjectID == nil {
			return "project:none"
		}
		return "project:" + *item.ProjectID
	case "status_category":
		return "status_category:" + localStatusCategory(item.StatusCategory)
	default:
		return "status:" + item.Status
	}
}

func (s *Server) tableIssues(r *http.Request, input tableRequest) ([]Issue, error) {
	ws, err := s.workspaceFor(r)
	if err != nil {
		return nil, err
	}
	projectID := input.Query.Scope.ProjectID
	if projectID == "" && len(input.Query.Filters.ProjectIDs) > 0 {
		projectID = input.Query.Filters.ProjectIDs[0]
	}
	items, err := s.store.ListIssues(r.Context(), ws.ID, projectID, "")
	if err != nil {
		return nil, err
	}
	result := items[:0]
	for _, item := range items {
		if len(input.Query.Filters.Statuses) > 0 && !contains(input.Query.Filters.Statuses, item.Status) {
			continue
		}
		if len(input.Query.Filters.Priorities) > 0 && !contains(input.Query.Filters.Priorities, item.Priority) {
			continue
		}
		if input.Query.Search != "" && !strings.Contains(strings.ToLower(item.Title), strings.ToLower(input.Query.Search)) {
			continue
		}
		result = append(result, item)
	}
	return result, nil
}

func (s *Server) listIssueTableGroups(w http.ResponseWriter, r *http.Request) {
	var input tableRequest
	if err := decodeBody(r, &input); err != nil {
		writeError(w, 400, err)
		return
	}
	items, err := s.tableIssues(r, input)
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	counts := map[string]int{}
	for _, item := range items {
		key := issueTableGroupKey(input.Group.Kind, item)
		counts[key]++
	}
	groups := []any{}
	for key, count := range counts {
		status := strings.TrimPrefix(key, "status:")
		if input.Group.Kind == "status_category" {
			status = strings.TrimPrefix(key, "status_category:")
		}
		value := map[string]any{"kind": "status", "status": status}
		if input.Group.Kind == "project" {
			var project any = nil
			projectID := strings.TrimPrefix(key, "project:")
			if projectID != "none" {
				project = projectID
			}
			value = map[string]any{"kind": "project", "project_id": project}
		}
		groups = append(groups, map[string]any{"key": key, "value": value, "count": count})
	}
	writeJSON(w, 200, map[string]any{"query_fingerprint": "local", "total": len(items), "groups": groups, "next_cursor": nil})
}

func (s *Server) listIssueTableRows(w http.ResponseWriter, r *http.Request) {
	var input tableRequest
	if err := decodeBody(r, &input); err != nil {
		writeError(w, 400, err)
		return
	}
	items, err := s.tableIssues(r, input)
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	rows := []any{}
	for _, item := range items {
		if input.GroupKey != nil && *input.GroupKey != "" {
			key := issueTableGroupKey(input.Group.Kind, item)
			if key != *input.GroupKey {
				continue
			}
		}
		rows = append(rows, map[string]any{"issue": item, "direct_child_count": 0})
	}
	writeJSON(w, 200, map[string]any{"query_fingerprint": "local", "group_key": input.GroupKey, "parent_id": input.ParentID, "total": len(rows), "rows": rows, "branch_total": len(rows), "next_cursor": nil})
}

func (s *Server) listIssueTableFacets(w http.ResponseWriter, r *http.Request) {
	var input tableRequest
	if err := decodeBody(r, &input); err != nil {
		writeError(w, 400, err)
		return
	}
	items, err := s.tableIssues(r, input)
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	facets := []any{}
	for _, facet := range input.Facets {
		if facet.Kind != "status" {
			continue
		}
		// Status facets are disjunctive: keep every other filter but drop the
		// selected statuses so hidden categories and unselected filter options
		// still report the count they would reveal.
		facetInput := input
		facetInput.Query.Filters.Statuses = nil
		facetItems, err := s.tableIssues(r, facetInput)
		if err != nil {
			s.respondStoreError(w, err)
			return
		}
		counts := map[string]int{}
		for _, item := range facetItems {
			counts[item.Status]++
		}
		keys := make([]string, 0, len(counts))
		for key := range counts {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		values := make([]any, 0, len(keys))
		for _, key := range keys {
			values = append(values, map[string]any{"key": key, "count": counts[key]})
		}
		facets = append(facets, map[string]any{"kind": "status", "values": values})
	}
	writeJSON(w, 200, map[string]any{"query_fingerprint": "local", "total": len(items), "facets": facets})
}

func (s *Server) searchIssues(w http.ResponseWriter, r *http.Request) {
	ws, err := s.workspaceFor(r)
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	items, err := s.store.ListIssues(r.Context(), ws.ID, "", "")
	if err != nil {
		s.fail(w, err)
		return
	}
	q := strings.ToLower(r.URL.Query().Get("q"))
	result := []any{}
	for _, item := range items {
		if q == "" || strings.Contains(strings.ToLower(item.Title), q) || strings.Contains(strings.ToLower(item.Identifier), q) {
			raw, _ := json.Marshal(item)
			var v map[string]any
			_ = json.Unmarshal(raw, &v)
			v["match_source"] = "title"
			result = append(result, v)
		}
	}
	writeJSON(w, 200, map[string]any{"issues": result, "total": len(result)})
}

func issueListValues(r *http.Request) map[string]string {
	values := map[string]string{}
	for key, list := range r.URL.Query() {
		if len(list) > 0 {
			values[key] = list[0]
		}
	}
	return values
}

func commaValues(raw string) map[string]bool {
	result := map[string]bool{}
	for _, value := range strings.Split(raw, ",") {
		if value = strings.TrimSpace(value); value != "" {
			result[value] = true
		}
	}
	return result
}

func requestsRun(updates map[string]any) bool {
	for _, key := range []string{"status", "assignee_type", "assignee_id"} {
		if _, exists := updates[key]; exists {
			return true
		}
	}
	return false
}

func issueTransitionRequestsRun(before, after Issue, updates map[string]any) bool {
	_, assigneeTypeRequested := updates["assignee_type"]
	_, assigneeIDRequested := updates["assignee_id"]
	if assigneeTypeRequested || assigneeIDRequested {
		beforeType, beforeID := "", ""
		afterType, afterID := "", ""
		if before.AssigneeType != nil {
			beforeType = *before.AssigneeType
		}
		if before.AssigneeID != nil {
			beforeID = *before.AssigneeID
		}
		if after.AssigneeType != nil {
			afterType = *after.AssigneeType
		}
		if after.AssigneeID != nil {
			afterID = *after.AssigneeID
		}
		if (beforeType != afterType || beforeID != afterID) && localIssueCategoryRunnable(after.StatusCategory) {
			return true
		}
	}
	_, statusRequested := updates["status"]
	return statusRequested && before.StatusCategory == "backlog" && localIssueCategoryRunnable(after.StatusCategory)
}

func (s *Server) issueListResponse(ctx context.Context, ws Workspace, values map[string]string) (map[string]any, error) {
	items, err := s.store.ListIssues(ctx, ws.ID, "", "")
	if err != nil {
		return nil, err
	}
	statuses, statusCategories := commaValues(values["statuses"]), commaValues(values["status_categories"])
	priorities := commaValues(values["priorities"])
	projectIDs, assigneeIDs := commaValues(values["project_ids"]), commaValues(values["assignee_ids"])
	ids, idsPresent := commaValues(values["ids"]), false
	if _, ok := values["ids"]; ok {
		idsPresent = true
	}
	q := strings.ToLower(strings.TrimSpace(values["q"]))
	filtered := make([]Issue, 0, len(items))
	for _, item := range items {
		if idsPresent && !ids[item.ID] {
			continue
		}
		if value := values["status_category"]; value != "" && !localStatusCategoryMatches(item.StatusCategory, value) {
			continue
		}
		if len(statusCategories) > 0 && !statusCategories[item.StatusCategory] && !statusCategories[localStatusCategory(item.StatusCategory)] {
			continue
		}
		if value := values["status"]; value != "" && item.Status != value {
			continue
		}
		if len(statuses) > 0 && !statuses[item.Status] {
			continue
		}
		if value := values["priority"]; value != "" && item.Priority != value {
			continue
		}
		if len(priorities) > 0 && !priorities[item.Priority] {
			continue
		}
		if value := values["project_id"]; value != "" && (item.ProjectID == nil || *item.ProjectID != value) {
			continue
		}
		if len(projectIDs) > 0 && (item.ProjectID == nil || !projectIDs[*item.ProjectID]) {
			continue
		}
		if value := values["assignee_id"]; value != "" && (item.AssigneeID == nil || *item.AssigneeID != value) {
			continue
		}
		if len(assigneeIDs) > 0 && (item.AssigneeID == nil || !assigneeIDs[*item.AssigneeID]) {
			continue
		}
		if values["top_level_only"] == "true" && item.ParentIssueID != nil {
			continue
		}
		if values["open_only"] == "true" && (item.StatusCategory == "done" || localStatusCategory(item.StatusCategory) == "closed") {
			continue
		}
		if q != "" && !strings.Contains(strings.ToLower(item.Title), q) && !strings.Contains(strings.ToLower(item.Identifier), q) {
			continue
		}
		filtered = append(filtered, item)
	}
	total := len(filtered)
	offset, _ := strconv.Atoi(values["offset"])
	limit, _ := strconv.Atoi(values["limit"])
	if offset < 0 {
		offset = 0
	}
	if offset > len(filtered) {
		offset = len(filtered)
	}
	end := len(filtered)
	if limit > 0 && offset+limit < end {
		end = offset + limit
	}
	return map[string]any{"issues": filtered[offset:end], "total": total}, nil
}

func (s *Server) listIssues(w http.ResponseWriter, r *http.Request) {
	ws, err := s.workspaceFor(r)
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	result, err := s.issueListResponse(r.Context(), ws, issueListValues(r))
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) queryIssues(w http.ResponseWriter, r *http.Request) {
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
	values := map[string]string{}
	for key, value := range input {
		if value != nil {
			values[key] = fmt.Sprint(value)
		}
	}
	result, err := s.issueListResponse(r.Context(), ws, values)
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) createIssue(w http.ResponseWriter, r *http.Request) {
	ws, err := s.workspaceFor(r)
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	var input CreateIssueInput
	if err := decodeBody(r, &input); err != nil || strings.TrimSpace(input.Title) == "" {
		writeError(w, 400, errors.New("title is required"))
		return
	}
	item, err := s.store.CreateIssue(r.Context(), ws.ID, input)
	if err != nil {
		if errors.Is(err, ErrInvalidIssueHandoff) {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		s.respondStoreError(w, err)
		return
	}
	if !input.SuppressRun {
		if err := s.store.enqueueAssignedIssue(r.Context(), ws.ID, item.ID, ""); err != nil {
			s.respondStoreError(w, err)
			return
		}
	}
	s.hub.broadcast("issue:created", map[string]any{"issue": item})
	if item.workflowTaskID != "" {
		s.hub.broadcast("daemon:pending_work", map[string]any{"issue_id": item.ID, "task_id": item.workflowTaskID})
	}
	writeJSON(w, 201, item)
}

func (s *Server) getIssueSession(w http.ResponseWriter, r *http.Request) {
	ws, err := s.workspaceFor(r)
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	session, err := s.store.GetIssueSession(r.Context(), ws.ID, chi.URLParam(r, "id"))
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, session)
}

func (s *Server) activateIssueSessionStage(w http.ResponseWriter, r *http.Request) {
	ws, err := s.workspaceFor(r)
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	var input struct {
		Kind string `json:"kind"`
	}
	if err := decodeBody(r, &input); err != nil || !isSessionStageKind(input.Kind) {
		writeError(w, http.StatusBadRequest, errors.New("invalid issue session stage kind"))
		return
	}
	session, err := s.store.ActivateIssueSessionStage(r.Context(), ws.ID, chi.URLParam(r, "id"), input.Kind)
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, session)
}

func (s *Server) continueIssueSession(w http.ResponseWriter, r *http.Request) {
	ws, err := s.workspaceFor(r)
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	issue, taskID, outcome, err := s.store.ContinueIssueSession(r.Context(), ws.ID, chi.URLParam(r, "id"))
	if err != nil {
		if errors.Is(err, ErrIssueSessionNotRunnable) || errors.Is(err, ErrIssueSessionRuntimeUnavailable) {
			writeError(w, http.StatusConflict, err)
			return
		}
		s.respondStoreError(w, err)
		return
	}
	task, err := s.taskRun(r.Context(), ws.ID, issue.ID, taskID)
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	status := http.StatusOK
	if outcome == "queued" {
		status = http.StatusCreated
		s.hub.broadcast("issue:updated", map[string]any{"issue": issue})
		s.hub.broadcast("task:created", map[string]any{"task": task})
	}
	writeJSON(w, status, map[string]any{"outcome": outcome, "issue": issue, "task": task})
}

func (s *Server) sendIssueSessionMessage(w http.ResponseWriter, r *http.Request) {
	ws, err := s.workspaceFor(r)
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	var input struct {
		Content       string   `json:"content"`
		AttachmentIDs []string `json:"attachment_ids"`
	}
	if err := decodeBody(r, &input); err != nil || strings.TrimSpace(input.Content) == "" {
		writeError(w, http.StatusBadRequest, errors.New("content is required"))
		return
	}
	requestKey := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if requestKey == "" || len(requestKey) > maxSessionRequestKey {
		writeError(w, http.StatusBadRequest, errors.New("valid Idempotency-Key is required"))
		return
	}
	result, err := s.store.SendIssueSessionMessage(r.Context(), ws.ID, chi.URLParam(r, "id"), requestKey, input.Content, input.AttachmentIDs)
	if err != nil {
		if errors.Is(err, ErrIssueSessionNotRunnable) || errors.Is(err, ErrIssueSessionRuntimeUnavailable) {
			writeError(w, http.StatusConflict, err)
			return
		}
		s.respondStoreError(w, err)
		return
	}
	task, err := s.taskRun(r.Context(), ws.ID, result.Issue.ID, result.TaskID)
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	status := http.StatusOK
	if result.Created {
		status = http.StatusCreated
		s.hub.broadcast("issue:updated", map[string]any{"issue": result.Issue})
		s.hub.broadcast("comment:created", map[string]any{"comment": result.Comment})
		s.hub.broadcast("task:created", map[string]any{"task": task})
	}
	writeJSON(w, status, map[string]any{"outcome": result.Outcome, "issue": result.Issue, "comment": result.Comment, "task": task})
}

func (s *Server) quickCreateIssue(w http.ResponseWriter, r *http.Request) {
	ws, err := s.workspaceFor(r)
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	var input struct {
		AgentID       string   `json:"agent_id"`
		SquadID       string   `json:"squad_id"`
		Prompt        string   `json:"prompt"`
		ProjectID     *string  `json:"project_id"`
		ParentID      *string  `json:"parent_issue_id"`
		Priority      string   `json:"priority"`
		DueDate       *string  `json:"due_date"`
		AttachmentIDs []string `json:"attachment_ids"`
	}
	if err := decodeBody(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	prompt := strings.TrimSpace(input.Prompt)
	agentID, squadID := strings.TrimSpace(input.AgentID), strings.TrimSpace(input.SquadID)
	if prompt == "" || (agentID == "") == (squadID == "") {
		writeError(w, http.StatusBadRequest, errors.New("exactly one local agent or squad and a prompt are required"))
		return
	}
	assigneeType, assigneeID := "agent", agentID
	runtimeQuery := `SELECT r.id FROM agents a JOIN runtimes r ON r.id=a.runtime_id WHERE a.id=? AND a.workspace_id=? AND a.archived_at IS NULL AND r.status='online'`
	if squadID != "" {
		assigneeType, assigneeID = "squad", squadID
		runtimeQuery = `SELECT r.id FROM squads s JOIN agents a ON a.id=s.leader_id JOIN runtimes r ON r.id=a.runtime_id WHERE s.id=? AND s.workspace_id=? AND s.archived_at IS NULL AND a.archived_at IS NULL AND r.status='online'`
	}
	var runtimeID string
	if err := s.store.db.QueryRowContext(r.Context(), runtimeQuery, assigneeID, ws.ID).Scan(&runtimeID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusConflict, errors.New("the selected local assignee has no online runtime"))
			return
		}
		s.fail(w, err)
		return
	}
	title := strings.TrimSpace(strings.SplitN(prompt, "\n", 2)[0])
	if len([]rune(title)) > 120 {
		title = string([]rune(title)[:120])
	}
	item, err := s.store.CreateIssue(r.Context(), ws.ID, CreateIssueInput{
		Title: title, Description: prompt, Status: "todo", Priority: input.Priority,
		AssigneeType: &assigneeType, AssigneeID: &assigneeID,
		ProjectID: input.ProjectID, ParentIssueID: input.ParentID, DueDate: input.DueDate,
		AttachmentIDs: input.AttachmentIDs, Metadata: map[string]any{"origin": "quick_create"},
	})
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	if err := s.store.enqueueAssignedIssue(r.Context(), ws.ID, item.ID, ""); err != nil {
		s.respondStoreError(w, err)
		return
	}
	var taskID string
	if err := s.store.db.QueryRowContext(r.Context(), `SELECT id FROM task_runs WHERE issue_id=? AND status='queued' ORDER BY created_at DESC,id DESC LIMIT 1`, item.ID).Scan(&taskID); err != nil {
		writeError(w, http.StatusConflict, errors.New("the issue was created but its local task could not be queued"))
		return
	}
	s.hub.broadcast("issue:created", map[string]any{"issue": item})
	writeJSON(w, http.StatusCreated, map[string]any{"task_id": taskID})
}

func (s *Server) getIssue(w http.ResponseWriter, r *http.Request) {
	ws, err := s.workspaceFor(r)
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	item, err := s.store.GetIssue(r.Context(), ws.ID, chi.URLParam(r, "id"))
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	item.SourceContext, err = s.localSourceContextDetail(r, item)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, 200, item)
}

func (s *Server) updateIssue(w http.ResponseWriter, r *http.Request) {
	ws, err := s.workspaceFor(r)
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	var input map[string]any
	if err := decodeBody(r, &input); err != nil {
		writeError(w, 400, err)
		return
	}
	handoffNote, _ := input["handoff_note"].(string)
	suppressRun, _ := input["suppress_run"].(bool)
	issueID := chi.URLParam(r, "id")
	before, err := s.store.GetIssue(r.Context(), ws.ID, issueID)
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	item, err := s.store.UpdateIssue(r.Context(), ws.ID, issueID, input)
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	if !suppressRun && issueTransitionRequestsRun(before, item, input) {
		if err := s.store.enqueueAssignedIssue(r.Context(), ws.ID, item.ID, handoffNote); err != nil {
			s.respondStoreError(w, err)
			return
		}
	}
	s.publishLocalIssueUpdate(before, item)
	writeJSON(w, 200, item)
}

func (s *Server) moveIssue(w http.ResponseWriter, r *http.Request) {
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
	issueID := chi.URLParam(r, "id")
	previous, err := s.store.GetIssue(r.Context(), ws.ID, issueID)
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	position := 0.0
	var before, after *Issue
	beforeID, _ := input["before_id"].(string)
	afterID, _ := input["after_id"].(string)
	if beforeID != "" {
		if beforeID == issueID {
			writeError(w, http.StatusBadRequest, errors.New("move neighbor cannot be the issue itself"))
			return
		}
		item, err := s.store.GetIssue(r.Context(), ws.ID, beforeID)
		if err != nil {
			s.respondStoreError(w, err)
			return
		}
		before = &item
	}
	if afterID != "" {
		if afterID == issueID {
			writeError(w, http.StatusBadRequest, errors.New("move neighbor cannot be the issue itself"))
			return
		}
		item, err := s.store.GetIssue(r.Context(), ws.ID, afterID)
		if err != nil {
			s.respondStoreError(w, err)
			return
		}
		after = &item
	}
	switch {
	case before != nil && after != nil:
		position = (before.Position + after.Position) / 2
	case before != nil:
		position = before.Position + 1
	case after != nil:
		position = after.Position - 1
	}
	updates := map[string]any{"position": position}
	for _, key := range []string{"status", "assignee_type", "assignee_id", "parent_issue_id", "project_id"} {
		if value, exists := input[key]; exists {
			updates[key] = value
		}
	}
	item, err := s.store.UpdateIssue(r.Context(), ws.ID, issueID, updates)
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	if requestsRun(updates) {
		if err := s.store.enqueueAssignedIssue(r.Context(), ws.ID, item.ID, ""); err != nil {
			s.respondStoreError(w, err)
			return
		}
	}
	s.publishLocalIssueUpdate(previous, item)
	writeJSON(w, 200, item)
}

func (s *Server) deleteIssue(w http.ResponseWriter, r *http.Request) {
	ws, err := s.workspaceFor(r)
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	id := chi.URLParam(r, "id")
	issue, err := s.store.GetIssue(r.Context(), ws.ID, id)
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	attachments, err := s.store.listAttachments(r.Context(), ws.ID, issue.ID, "")
	if err != nil {
		s.fail(w, err)
		return
	}
	type stagedFile struct{ original, staged string }
	staged := make([]stagedFile, 0, len(attachments))
	for _, attachment := range attachments {
		if attachment.storagePath == "" {
			continue
		}
		entry := stagedFile{original: attachment.storagePath, staged: filepath.Join("trash", uuid.NewString())}
		if err := s.files.Rename(entry.original, entry.staged); err != nil {
			for index := len(staged) - 1; index >= 0; index-- {
				_ = s.files.Rename(staged[index].staged, staged[index].original)
			}
			s.fail(w, err)
			return
		}
		staged = append(staged, entry)
	}
	if err := s.store.DeleteIssue(r.Context(), ws.ID, id); err != nil {
		for index := len(staged) - 1; index >= 0; index-- {
			_ = s.files.Rename(staged[index].staged, staged[index].original)
		}
		s.respondStoreError(w, err)
		return
	}
	for _, entry := range staged {
		if err := s.files.Remove(entry.staged); err != nil {
			s.logger.Warn("remove staged attachment after task deletion", "path", entry.staged, "error", err)
		}
	}
	s.hub.broadcast("issue:deleted", map[string]any{"issue_id": id})
	w.WriteHeader(204)
}

func (s *Server) listChildren(w http.ResponseWriter, r *http.Request) {
	ws, err := s.workspaceFor(r)
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	items, err := s.store.ListIssues(r.Context(), ws.ID, "", chi.URLParam(r, "id"))
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"issues": items})
}
func (s *Server) childrenByParents(w http.ResponseWriter, r *http.Request) {
	ws, err := s.workspaceFor(r)
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	ids := strings.Split(r.URL.Query().Get("parent_ids"), ",")
	all := []Issue{}
	for _, id := range ids {
		items, e := s.store.ListIssues(r.Context(), ws.ID, "", id)
		if e != nil {
			s.fail(w, e)
			return
		}
		all = append(all, items...)
	}
	writeJSON(w, 200, map[string]any{"issues": all})
}
func (s *Server) childProgress(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, 200, map[string]any{"progress": []any{}})
}

func (s *Server) batchUpdateIssues(w http.ResponseWriter, r *http.Request) {
	ws, err := s.workspaceFor(r)
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	var input struct {
		IssueIDs []string       `json:"issue_ids"`
		Updates  map[string]any `json:"updates"`
	}
	if err := decodeBody(r, &input); err != nil {
		writeError(w, 400, err)
		return
	}
	handoffNote, _ := input.Updates["handoff_note"].(string)
	suppressRun, _ := input.Updates["suppress_run"].(bool)
	if _, present := input.Updates["duplicate_of_issue_id"]; present {
		writeError(w, http.StatusBadRequest, errors.New("duplicate marks require an individual issue update"))
		return
	}
	updated := 0
	for _, id := range input.IssueIDs {
		before, err := s.store.GetIssue(r.Context(), ws.ID, id)
		if err != nil {
			s.respondStoreError(w, err)
			return
		}
		item, err := s.store.UpdateIssue(r.Context(), ws.ID, id, input.Updates)
		if err != nil {
			s.respondStoreError(w, err)
			return
		}
		if !suppressRun && requestsRun(input.Updates) {
			if err := s.store.enqueueAssignedIssue(r.Context(), ws.ID, item.ID, handoffNote); err != nil {
				s.respondStoreError(w, err)
				return
			}
		}
		updated++
		s.publishLocalIssueUpdate(before, item)
	}
	writeJSON(w, 200, map[string]any{"updated": updated})
}
func (s *Server) batchDeleteIssues(w http.ResponseWriter, r *http.Request) {
	ws, err := s.workspaceFor(r)
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	var input struct {
		IssueIDs []string `json:"issue_ids"`
	}
	if err := decodeBody(r, &input); err != nil {
		writeError(w, 400, err)
		return
	}
	deleted := 0
	for _, id := range input.IssueIDs {
		if s.store.DeleteIssue(r.Context(), ws.ID, id) == nil {
			deleted++
		}
	}
	writeJSON(w, 200, map[string]any{"deleted": deleted})
}

func (s *Server) listComments(w http.ResponseWriter, r *http.Request) {
	ws, err := s.workspaceFor(r)
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	if _, err := s.store.GetIssue(r.Context(), ws.ID, chi.URLParam(r, "id")); err != nil {
		s.respondStoreError(w, err)
		return
	}
	items, err := s.store.ListComments(r.Context(), ws.ID, chi.URLParam(r, "id"))
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, 200, items)
}
func (s *Server) createComment(w http.ResponseWriter, r *http.Request) {
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
		Content          string   `json:"content"`
		Type             string   `json:"type"`
		ParentID         string   `json:"parent_id"`
		AttachmentIDs    []string `json:"attachment_ids"`
		SuppressAgentIDs []string `json:"suppress_agent_ids"`
	}
	if err := decodeBody(r, &input); err != nil || strings.TrimSpace(input.Content) == "" {
		writeError(w, 400, errors.New("content is required"))
		return
	}
	agentID := strings.TrimSpace(r.Header.Get("X-Agent-ID"))
	taskID := strings.TrimSpace(r.Header.Get("X-Task-ID"))
	var item Comment
	if agentID == "" && taskID == "" {
		item, err = s.store.CreateCommentWithAttachments(r.Context(), ws.ID, issueID, input.Content, input.Type, input.ParentID, input.AttachmentIDs)
	} else {
		if agentID == "" || taskID == "" {
			writeError(w, http.StatusForbidden, errors.New("invalid agent task identity"))
			return
		}
		var taskExists int
		if err := s.store.db.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM task_runs t JOIN issues i ON i.id=t.issue_id WHERE t.id=? AND t.issue_id=? AND t.agent_id=? AND t.status='running' AND i.workspace_id=?`, taskID, issueID, agentID, ws.ID).Scan(&taskExists); err != nil {
			s.fail(w, err)
			return
		}
		if taskExists == 0 {
			writeError(w, http.StatusForbidden, errors.New("agent task is not active for issue"))
			return
		}
		item, err = s.store.createComment(r.Context(), ws.ID, issueID, input.Content, input.Type, input.ParentID, "agent", agentID, taskID, input.AttachmentIDs)
	}
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	item.TriggerOutcomes, err = s.dispatchCommentMentions(r.Context(), ws.ID, issueID, item.ID, input.Content, input.SuppressAgentIDs)
	if err != nil {
		s.fail(w, err)
		return
	}
	s.hub.broadcast("comment:created", map[string]any{"comment": item})
	writeJSON(w, 201, item)
}

func (s *Server) resolveComment(w http.ResponseWriter, r *http.Request) {
	s.setCommentResolution(w, r, true)
}

func (s *Server) unresolveComment(w http.ResponseWriter, r *http.Request) {
	s.setCommentResolution(w, r, false)
}

func (s *Server) setCommentResolution(w http.ResponseWriter, r *http.Request, resolved bool) {
	ws, err := s.workspaceFor(r)
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	item, err := s.store.setCommentResolved(r.Context(), ws.ID, chi.URLParam(r, "id"), resolved)
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	event := "comment:unresolved"
	if resolved {
		event = "comment:resolved"
	}
	s.hub.broadcast(event, map[string]any{"comment": item})
	writeJSON(w, http.StatusOK, item)
}

func (s *Server) timeline(w http.ResponseWriter, r *http.Request) {
	ws, err := s.workspaceFor(r)
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	if _, err := s.store.GetIssue(r.Context(), ws.ID, chi.URLParam(r, "id")); err != nil {
		s.respondStoreError(w, err)
		return
	}
	items, err := s.store.Timeline(r.Context(), ws.ID, chi.URLParam(r, "id"))
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, 200, items)
}

func (s *Server) listIssueStatuses(w http.ResponseWriter, r *http.Request) {
	ws, err := s.workspaceFor(r)
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	items, err := s.store.ListStatuses(r.Context(), ws.ID)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"statuses": items, "categories": []string{"unstarted", "started", "done", "closed"}, "total": len(items)})
}

func (s *Server) listTaskRuns(w http.ResponseWriter, r *http.Request) {
	ws, err := s.workspaceFor(r)
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	if _, err := s.store.GetIssue(r.Context(), ws.ID, chi.URLParam(r, "id")); err != nil {
		s.respondStoreError(w, err)
		return
	}
	usageByTask, err := s.store.TaskRunUsageByIssue(r.Context(), ws.ID, chi.URLParam(r, "id"))
	if err != nil {
		s.fail(w, err)
		return
	}
	rows, err := s.store.db.QueryContext(r.Context(), `SELECT tr.id, tr.issue_id, tr.status, tr.thread_id, tr.runtime, tr.handoff_note, tr.summary, tr.error, tr.started_at, tr.completed_at, tr.created_at, tr.session_id, tr.stage_id, tr.prior_task_id, tr.trigger_comment_id, tr.continuity_state, tr.continuity_reason, st.kind, COALESCE(tr.agent_id,?) FROM task_runs tr LEFT JOIN issue_session_stages st ON st.id=tr.stage_id WHERE tr.issue_id=? ORDER BY tr.created_at DESC`, LocalAgentID, chi.URLParam(r, "id"))
	if err != nil {
		s.fail(w, err)
		return
	}
	defer rows.Close()
	items := []any{}
	for rows.Next() {
		var id, issueID, status, createdAt, agentID string
		var threadID, runtimeName, handoffNote, summary, errorText, startedAt, completedAt, sessionID, stageID, priorTaskID, triggerCommentID, continuityReason, stageKind sql.NullString
		var continuityState string
		if err := rows.Scan(&id, &issueID, &status, &threadID, &runtimeName, &handoffNote, &summary, &errorText, &startedAt, &completedAt, &createdAt, &sessionID, &stageID, &priorTaskID, &triggerCommentID, &continuityState, &continuityReason, &stageKind, &agentID); err != nil {
			s.fail(w, err)
			return
		}
		usage := usageByTask[id]
		if usage == nil {
			usage = []TaskRunUsage{}
		}
		items = append(items, map[string]any{"id": id, "agent_id": LocalAgentID, "runtime_id": nullableString(runtimeName), "issue_id": issueID, "status": status, "priority": 0, "handoff_note": nullableString(handoffNote), "dispatched_at": nil, "started_at": stringPtr(startedAt), "completed_at": stringPtr(completedAt), "result": map[string]any{"thread_id": nullableString(threadID), "summary": nullableString(summary)}, "error": stringPtr(errorText), "created_at": createdAt, "session_id": nullableString(sessionID), "stage_id": nullableString(stageID), "stage_kind": nullableString(stageKind), "prior_task_id": nullableString(priorTaskID), "trigger_comment_id": nullableString(triggerCommentID), "continuity_state": continuityState, "continuity_reason": nullableString(continuityReason), "usage": usage})
		items[len(items)-1].(map[string]any)["agent_id"] = agentID
	}
	writeJSON(w, 200, items)
}

func (s *Server) activeTask(w http.ResponseWriter, r *http.Request) {
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
	var id, status, runtimeID, createdAt, sessionID, stageID, stageKind, continuityState, agentID string
	err = s.store.db.QueryRowContext(r.Context(), `SELECT tr.id, tr.status, COALESCE(tr.runtime,''), tr.created_at, COALESCE(tr.session_id,''), COALESCE(tr.stage_id,''), COALESCE(st.kind,''), tr.continuity_state, COALESCE(tr.agent_id,?) FROM task_runs tr LEFT JOIN issue_session_stages st ON st.id=tr.stage_id WHERE tr.issue_id=? AND tr.status IN ('queued','dispatched','waiting_local_directory','running','waiting_previous_run') ORDER BY CASE WHEN tr.status='waiting_previous_run' THEN 1 ELSE 0 END, tr.created_at DESC LIMIT 1`, LocalAgentID, issueID).Scan(&id, &status, &runtimeID, &createdAt, &sessionID, &stageID, &stageKind, &continuityState, &agentID)
	if errors.Is(err, sql.ErrNoRows) {
		writeJSON(w, http.StatusOK, map[string]any{"tasks": []any{}})
		return
	}
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"tasks": []any{map[string]any{"id": id, "agent_id": agentID, "runtime_id": runtimeID, "issue_id": issueID, "status": status, "priority": 0, "created_at": createdAt, "session_id": sessionID, "stage_id": stageID, "stage_kind": stageKind, "continuity_state": continuityState}}})
}

func (s *Server) cancelTask(w http.ResponseWriter, r *http.Request) {
	ws, err := s.workspaceFor(r)
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	issueID, taskID := chi.URLParam(r, "id"), chi.URLParam(r, "taskID")
	stamp := now()
	tx, err := s.store.db.BeginTx(r.Context(), nil)
	if err != nil {
		s.fail(w, err)
		return
	}
	defer func() { _ = tx.Rollback() }()
	result, err := tx.ExecContext(r.Context(), `UPDATE task_runs SET status='cancelled', completed_at=?, updated_at=? WHERE id=? AND issue_id=? AND status IN ('queued','dispatched','waiting_local_directory','running','waiting_previous_run') AND issue_id IN (SELECT id FROM issues WHERE workspace_id=?)`, stamp, stamp, taskID, issueID, ws.ID)
	if err != nil {
		s.fail(w, err)
		return
	}
	changed, err := result.RowsAffected()
	if err != nil {
		s.fail(w, err)
		return
	}
	if changed == 0 {
		writeError(w, http.StatusConflict, errors.New("task is not active"))
		return
	}
	if _, err := promoteWaitingIssueRunTx(r.Context(), tx, issueID, stamp); err != nil {
		s.fail(w, err)
		return
	}
	if err := tx.Commit(); err != nil {
		s.fail(w, err)
		return
	}
	item, err := s.taskRun(r.Context(), ws.ID, issueID, taskID)
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	s.hub.broadcast("task:cancelled", map[string]any{"task": item})
	writeJSON(w, http.StatusOK, item)
}

func (s *Server) rerunIssue(w http.ResponseWriter, r *http.Request) {
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
		TaskID string `json:"task_id"`
	}
	if err := decodeBody(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if input.TaskID != "" {
		var exists int
		if err := s.store.db.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM task_runs tr JOIN issues i ON i.id=tr.issue_id WHERE tr.id=? AND tr.issue_id=? AND i.workspace_id=?`, input.TaskID, issueID, ws.ID).Scan(&exists); err != nil {
			s.fail(w, err)
			return
		}
		if exists == 0 {
			writeError(w, http.StatusNotFound, errors.New("task not found"))
			return
		}
	}
	var active int
	if err := s.store.db.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM task_runs WHERE issue_id=? AND status IN ('queued','dispatched','waiting_local_directory','running','waiting_previous_run')`, issueID).Scan(&active); err != nil {
		s.fail(w, err)
		return
	}
	if active > 0 {
		writeError(w, http.StatusConflict, errors.New("issue already has an active task"))
		return
	}
	if err := s.store.enqueueAssignedIssue(r.Context(), ws.ID, issueID, ""); err != nil {
		s.respondStoreError(w, err)
		return
	}
	var taskID string
	if err := s.store.db.QueryRowContext(r.Context(), `SELECT id FROM task_runs WHERE issue_id=? AND status='queued' ORDER BY created_at DESC LIMIT 1`, issueID).Scan(&taskID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusConflict, errors.New("issue is not ready to run"))
			return
		}
		s.fail(w, err)
		return
	}
	item, err := s.taskRun(r.Context(), ws.ID, issueID, taskID)
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	s.hub.broadcast("task:created", map[string]any{"task": item})
	writeJSON(w, http.StatusCreated, item)
}

func (s *Server) taskRun(ctx context.Context, workspaceID, issueID, taskID string) (map[string]any, error) {
	var id, status, createdAt, agentID string
	var runtimeID, handoffNote, threadID, summary, errorText, startedAt, completedAt, sessionID, stageID, stageKind, priorTaskID, triggerCommentID, continuityReason sql.NullString
	var continuityState string
	err := s.store.db.QueryRowContext(ctx, `SELECT tr.id, tr.status, tr.runtime, tr.handoff_note, tr.thread_id, tr.summary, tr.error, tr.started_at, tr.completed_at, tr.created_at, tr.session_id, tr.stage_id, st.kind, tr.prior_task_id, tr.trigger_comment_id, tr.continuity_state, tr.continuity_reason, COALESCE(tr.agent_id,?) FROM task_runs tr JOIN issues i ON i.id=tr.issue_id LEFT JOIN issue_session_stages st ON st.id=tr.stage_id WHERE tr.id=? AND tr.issue_id=? AND i.workspace_id=?`, LocalAgentID, taskID, issueID, workspaceID).Scan(&id, &status, &runtimeID, &handoffNote, &threadID, &summary, &errorText, &startedAt, &completedAt, &createdAt, &sessionID, &stageID, &stageKind, &priorTaskID, &triggerCommentID, &continuityState, &continuityReason, &agentID)
	if err != nil {
		return nil, err
	}
	usage, err := s.store.TaskRunUsageForTask(ctx, workspaceID, issueID, taskID)
	if err != nil {
		return nil, err
	}
	return map[string]any{"id": id, "agent_id": agentID, "runtime_id": nullableString(runtimeID), "issue_id": issueID, "status": status, "priority": 0, "handoff_note": nullableString(handoffNote), "started_at": stringPtr(startedAt), "completed_at": stringPtr(completedAt), "result": map[string]any{"thread_id": nullableString(threadID), "summary": nullableString(summary)}, "error": stringPtr(errorText), "created_at": createdAt, "session_id": nullableString(sessionID), "stage_id": nullableString(stageID), "stage_kind": nullableString(stageKind), "prior_task_id": nullableString(priorTaskID), "trigger_comment_id": nullableString(triggerCommentID), "continuity_state": continuityState, "continuity_reason": nullableString(continuityReason), "usage": usage}, nil
}

func (s *Server) issueUsage(w http.ResponseWriter, r *http.Request) {
	ws, err := s.workspaceFor(r)
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	summary, err := s.store.IssueUsage(r.Context(), ws.ID, chi.URLParam(r, "id"))
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, summary)
}

func nullableString(v sql.NullString) string {
	if v.Valid {
		return v.String
	}
	return ""
}

func (s *Server) respondStoreError(w http.ResponseWriter, err error) {
	if errors.Is(err, ErrIssueRevisionConflict) {
		writeJSON(w, http.StatusConflict, map[string]any{"error": err.Error(), "code": "revision_conflict"})
		return
	}
	if errors.Is(err, ErrInvalidIssueRevision) {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if writeLocalDuplicateError(w, err) {
		return
	}
	if writeLocalIssueWorkflowError(w, err) {
		return
	}
	if errors.Is(err, service.ErrWakeupForbidden) {
		writeError(w, http.StatusForbidden, service.ErrWakeupForbidden)
		return
	}
	if IsNotFound(err) {
		writeError(w, 404, errors.New("not found"))
		return
	}
	if errors.Is(err, ErrIssueProjectRequired) || errors.Is(err, ErrIssueProjectMismatch) {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if errors.Is(err, ErrIssueProjectImmutable) || errors.Is(err, ErrProjectHasIssues) {
		writeError(w, http.StatusConflict, err)
		return
	}
	s.fail(w, err)
}
func (s *Server) fail(w http.ResponseWriter, err error) {
	s.logger.Error("local workbench request failed", "error", err)
	writeError(w, 500, errors.New("local database operation failed"))
}

func (s *Server) publishLocalIssueUpdate(before, after Issue) {
	s.hub.broadcast("issue:updated", map[string]any{"issue": after, "status_changed": before.Status != after.Status, "duplicate_of_issue_id": after.DuplicateOfIssueID, "prev_duplicate_of_issue_id": before.DuplicateOfIssueID})
	for _, id := range after.workflowCancelled {
		s.hub.broadcast("task:cancelled", map[string]any{"task": map[string]any{"id": id, "issue_id": after.ID, "status": "cancelled"}})
	}
	if after.workflowTaskID != "" {
		s.hub.broadcast("daemon:pending_work", map[string]any{"issue_id": after.ID, "task_id": after.workflowTaskID})
	}
}
