package localworkbench

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
)

type Server struct {
	store      *Store
	logger     *slog.Logger
	hub        *localHub
	startedAt  string
	databaseID string
}

func DefaultDatabasePath() (string, error) {
	if path := strings.TrimSpace(os.Getenv("MULTICA_SQLITE_PATH")); path != "" {
		return path, nil
	}
	configDir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("resolving user config directory: %w", err)
	}
	return filepath.Join(configDir, "Multica", "multica.db"), nil
}

func New(ctx context.Context, databasePath string, logger *slog.Logger) (*Server, error) {
	store, err := Open(ctx, databasePath)
	if err != nil {
		return nil, err
	}
	if logger == nil {
		logger = slog.Default()
	}
	absPath, err := filepath.Abs(databasePath)
	if err != nil {
		_ = store.Close()
		return nil, fmt.Errorf("resolving sqlite database path: %w", err)
	}
	return &Server{store: store, logger: logger, hub: newLocalHub(), startedAt: now(), databaseID: fmt.Sprintf("%x", sha256.Sum256([]byte(absPath)))}, nil
}

func (s *Server) Close() error {
	s.hub.close()
	return s.store.Close()
}

func (s *Server) Router() http.Handler {
	r := chi.NewRouter()
	r.Use(localRequestBoundary)
	r.Get("/health", s.health)
	r.Get("/ws", s.websocket)
	r.Route("/api", func(api chi.Router) {
		api.Get("/config", s.config)
		api.Post("/upload-file", s.uploadFile)
		api.Get("/attachments/{id}", s.getAttachment)
		api.Get("/attachments/{id}/content", s.attachmentContent)
		api.Get("/attachments/{id}/download", s.downloadAttachment)
		api.Delete("/attachments/{id}", s.deleteAttachment)
		api.Get("/workspaces", s.listWorkspaces)
		api.Get("/workspaces/{id}", s.getWorkspace)
		api.Patch("/workspaces/{id}", s.updateWorkspace)
		api.Get("/daemon/workspaces", s.listWorkspaces)
		api.Get("/daemon/workspaces/{id}/runtime-profiles", func(w http.ResponseWriter, r *http.Request) {
			ws, err := s.workspaceFor(r)
			if err != nil || chi.URLParam(r, "id") != ws.ID {
				s.respondStoreError(w, sql.ErrNoRows)
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"workspace_id": ws.ID, "runtime_profiles": []any{}})
		})
		api.Get("/daemon/workspaces/{id}/repos", func(w http.ResponseWriter, r *http.Request) {
			ws, err := s.workspaceFor(r)
			if err != nil || chi.URLParam(r, "id") != ws.ID {
				s.respondStoreError(w, sql.ErrNoRows)
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"workspace_id": ws.ID, "repos": []any{}, "repos_version": "local"})
		})
		api.Post("/daemon/register", s.daemonRegister)
		api.Post("/daemon/deregister", s.daemonDeregister)
		api.Post("/daemon/heartbeat", s.daemonHeartbeat)
		api.Post("/daemon/tasks/claim", s.daemonClaimTasks)
		api.Post("/daemon/runtimes/{runtimeID}/tasks/claim", s.daemonClaimTask)
		api.Post("/daemon/runtimes/{runtimeID}/recover-orphans", s.daemonRecoverOrphans)
		api.Post("/daemon/runtimes/{runtimeID}/tasks/{taskID}/prepare-lease", s.daemonTaskNoop)
		api.Post("/daemon/tasks/{taskID}/{action:start|complete|fail|wait-local-directory}", s.daemonTaskState)
		api.Post("/daemon/tasks/{taskID}/progress", s.daemonTaskProgress)
		api.Post("/daemon/tasks/{taskID}/messages", s.daemonTaskMessages)
		api.Post("/daemon/tasks/{taskID}/usage", s.daemonTaskNoop)
		api.Post("/daemon/tasks/{taskID}/session", s.daemonTaskSession)
		api.Get("/daemon/tasks/{taskID}/status", s.daemonTaskStatus)

		api.Get("/projects", s.listProjects)
		api.Post("/projects", s.createProject)
		api.Get("/projects/search", s.searchProjects)
		api.Get("/projects/{id}", s.getProject)
		api.Put("/projects/{id}", s.updateProject)
		api.Delete("/projects/{id}", s.deleteProject)
		api.Get("/projects/{id}/resources", s.listProjectResources)
		api.Post("/projects/{id}/resources", s.createProjectResource)
		api.Put("/projects/{id}/resources/{resourceID}", s.updateProjectResource)
		api.Delete("/projects/{id}/resources/{resourceID}", s.deleteProjectResource)

		api.Get("/issues/grouped", s.listGroupedIssues)
		api.Post("/issues/table/groups", s.listIssueTableGroups)
		api.Post("/issues/table/rows", s.listIssueTableRows)
		api.Post("/issues/table/facets", s.listIssueTableFacets)
		api.Get("/issues/search", s.searchIssues)
		api.Get("/issues/child-progress", s.childProgress)
		api.Get("/issues/children", s.childrenByParents)
		api.Post("/issues/preview-trigger", emptyObject("triggers", []any{}, "total_count", 0))
		api.Get("/issues", s.listIssues)
		api.Post("/issues/query", s.queryIssues)
		api.Post("/issues/quick-create", s.quickCreateIssue)
		api.Post("/issues", s.createIssue)
		api.Post("/issues/batch-update", s.batchUpdateIssues)
		api.Post("/issues/batch-delete", s.batchDeleteIssues)
		api.Get("/issues/{id}", s.getIssue)
		api.Put("/issues/{id}", s.updateIssue)
		api.Post("/issues/{id}/move", s.moveIssue)
		api.Delete("/issues/{id}", s.deleteIssue)
		api.Get("/issues/{id}/children", s.listChildren)
		api.Get("/issues/{id}/comments", s.listComments)
		api.Post("/issues/{id}/comments", s.createComment)
		api.Post("/issues/{id}/comments/trigger-preview", emptyObject("agents", []any{}))
		api.Post("/comments/{id}/resolve", s.resolveComment)
		api.Delete("/comments/{id}/resolve", s.unresolveComment)
		api.Get("/issues/{id}/attachments", s.listIssueAttachments)
		api.Get("/issues/{id}/labels", emptyObject("labels", []any{}))
		api.Get("/issues/{id}/timeline", s.timeline)
		api.Get("/issues/{id}/active-task", s.activeTask)
		api.Get("/issues/{id}/task-runs", s.listTaskRuns)
		api.Post("/issues/{id}/tasks/{taskID}/cancel", s.cancelTask)
		api.Post("/issues/{id}/rerun", s.rerunIssue)
		api.Get("/issues/{id}/usage", emptyObject("total_tokens", 0, "total_cost_usd", 0))

		api.Get("/issue-statuses", s.listIssueStatuses)
		api.Get("/labels", emptyObject("labels", []any{}, "total", 0))
		api.Get("/agents", s.listAgents)
		api.Get("/agents/{id}", s.getAgent)
		api.Get("/tasks/{taskID}/messages", s.listTaskMessages)
		api.Get("/runtimes", s.listRuntimes)
		api.Get("/runtimes/{id}/usage", emptyArray())
		api.Get("/runtimes/{id}/usage/by-agent", emptyArray())
		api.Get("/agent-task-snapshot", emptyArray())
		api.Get("/working-agents", emptyArray())
		api.Get("/assignee-frequency", emptyArray())
		api.Get("/issue-properties", emptyObject("properties", []any{}))
		api.Get("/properties", emptyObject("properties", []any{}, "total", 0))
		api.Get("/quick-actions", emptyObject("quick_actions", []any{}))
		api.Get("/issue-views", emptyArray())
		api.Get("/issue-view-preferences", emptyObject(
			"scope_type", "workspace", "scope_id", nil,
			"prefs", map[string]any{"hidden": []any{}, "order": []any{}}, "updated_at", "",
		))
		api.Get("/chat/sessions", emptyArray())
		api.Get("/chat/pending-tasks", emptyObject("tasks", []any{}))
		api.Get("/chat/pending-tasks/has-any", emptyObject("has_pending", false))
		api.Get("/pins", emptyArray())
	})
	return r
}

func emptyArray() http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, http.StatusOK, []any{}) }
}

func emptyObject(values ...any) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		result := map[string]any{}
		for i := 0; i+1 < len(values); i += 2 {
			result[fmt.Sprint(values[i])] = values[i+1]
		}
		writeJSON(w, http.StatusOK, result)
	}
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]any{"error": err.Error()})
}

func decodeBody(r *http.Request, target any) error {
	decoder := json.NewDecoder(io.LimitReader(r.Body, 2<<20))
	return decoder.Decode(target)
}

func localRequestBoundary(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Native desktop and daemon clients send no Origin. A browser page cannot
		// suppress Origin on cross-origin writes or WebSocket handshakes.
		if strings.TrimSpace(r.Header.Get("Origin")) != "" {
			writeError(w, http.StatusForbidden, errors.New("browser origins are not accepted by the local workbench"))
			return
		}
		if r.Method == http.MethodPost || r.Method == http.MethodPut || r.Method == http.MethodPatch {
			contentType := strings.ToLower(strings.TrimSpace(strings.Split(r.Header.Get("Content-Type"), ";")[0]))
			if r.Method == http.MethodPost && r.URL.Path == "/api/upload-file" {
				if contentType != "multipart/form-data" {
					writeError(w, http.StatusUnsupportedMediaType, errors.New("multipart/form-data is required"))
					return
				}
			} else if contentType != "application/json" {
				writeError(w, http.StatusUnsupportedMediaType, errors.New("application/json is required"))
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"status": "ok", "storage": "sqlite", "mode": "local", "pid": os.Getpid(),
		"commit": strings.TrimSpace(os.Getenv("MULTICA_COMMIT")), "started_at": s.startedAt, "database_id": s.databaseID,
	})
}

func (s *Server) config(w http.ResponseWriter, r *http.Request) {
	host := r.Host
	writeJSON(w, http.StatusOK, map[string]any{
		"cdn_domain": "", "cdn_signed": false, "allow_signup": false,
		"daemon_server_url": "ws://" + host + "/ws/daemon", "daemon_app_url": "http://" + host,
		"workspace_creation_disabled": true, "vcs_integration_available": false,
		"local_worktree_supported": true, "feature_flags": map[string]any{}, "server_version": "local-sqlite",
	})
}

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
	item, err := s.store.CreateProject(r.Context(), ws.ID, input.Title, input.Description, input.Icon, input.Status, input.Priority, input.Resources)
	if err != nil {
		s.fail(w, err)
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
	if _, err := tx.ExecContext(r.Context(), "UPDATE issues SET project_id=NULL WHERE workspace_id=? AND project_id=?", ws.ID, id); err != nil {
		s.fail(w, err)
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
			if item.StatusCategory == category {
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
		return "status_category:" + item.StatusCategory
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
	writeJSON(w, 200, map[string]any{"query_fingerprint": "local", "total": len(items), "facets": []any{}})
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
		if value := values["status_category"]; value != "" && item.StatusCategory != value {
			continue
		}
		if len(statusCategories) > 0 && !statusCategories[item.StatusCategory] {
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
		if values["open_only"] == "true" && (item.StatusCategory == "done" || item.StatusCategory == "cancelled") {
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
		s.fail(w, err)
		return
	}
	if err := s.store.enqueueAssignedIssue(r.Context(), ws.ID, item.ID, ""); err != nil {
		s.fail(w, err)
		return
	}
	s.hub.broadcast("issue:created", map[string]any{"issue": item})
	writeJSON(w, 201, item)
}

func (s *Server) quickCreateIssue(w http.ResponseWriter, r *http.Request) {
	ws, err := s.workspaceFor(r)
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	var input struct {
		AgentID       string   `json:"agent_id"`
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
	if prompt == "" || input.AgentID != LocalAgentID {
		writeError(w, http.StatusBadRequest, errors.New("a local Codex agent and prompt are required"))
		return
	}
	if _, err := s.store.codexRuntime(r.Context(), ws.ID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusConflict, errors.New("local Codex runtime is not online"))
			return
		}
		s.fail(w, err)
		return
	}
	title := strings.TrimSpace(strings.SplitN(prompt, "\n", 2)[0])
	if len([]rune(title)) > 120 {
		title = string([]rune(title)[:120])
	}
	agentType, agentID := "agent", LocalAgentID
	item, err := s.store.CreateIssue(r.Context(), ws.ID, CreateIssueInput{
		Title: title, Description: prompt, Status: "todo", Priority: input.Priority,
		AssigneeType: &agentType, AssigneeID: &agentID,
		ProjectID: input.ProjectID, ParentIssueID: input.ParentID, DueDate: input.DueDate,
		AttachmentIDs: input.AttachmentIDs,
	})
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	if err := s.store.enqueueAssignedIssue(r.Context(), ws.ID, item.ID, ""); err != nil {
		s.fail(w, err)
		return
	}
	s.hub.broadcast("issue:created", map[string]any{"issue": item})
	writeJSON(w, http.StatusCreated, item)
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
	item, err := s.store.UpdateIssue(r.Context(), ws.ID, chi.URLParam(r, "id"), input)
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	if !suppressRun && requestsRun(input) {
		if err := s.store.enqueueAssignedIssue(r.Context(), ws.ID, item.ID, handoffNote); err != nil {
			s.fail(w, err)
			return
		}
	}
	s.hub.broadcast("issue:updated", map[string]any{"issue": item})
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
	if _, err := s.store.GetIssue(r.Context(), ws.ID, issueID); err != nil {
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
			s.fail(w, err)
			return
		}
	}
	s.hub.broadcast("issue:updated", map[string]any{"issue": item})
	writeJSON(w, 200, item)
}

func (s *Server) deleteIssue(w http.ResponseWriter, r *http.Request) {
	ws, err := s.workspaceFor(r)
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	id := chi.URLParam(r, "id")
	if err := s.store.DeleteIssue(r.Context(), ws.ID, id); err != nil {
		s.respondStoreError(w, err)
		return
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
	updated := 0
	for _, id := range input.IssueIDs {
		item, err := s.store.UpdateIssue(r.Context(), ws.ID, id, input.Updates)
		if err != nil {
			s.respondStoreError(w, err)
			return
		}
		if !suppressRun && requestsRun(input.Updates) {
			if err := s.store.enqueueAssignedIssue(r.Context(), ws.ID, item.ID, handoffNote); err != nil {
				s.fail(w, err)
				return
			}
		}
		updated++
		s.hub.broadcast("issue:updated", map[string]any{"issue": item})
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
	if _, err := s.store.GetIssue(r.Context(), ws.ID, chi.URLParam(r, "id")); err != nil {
		s.respondStoreError(w, err)
		return
	}
	var input struct {
		Content       string   `json:"content"`
		Type          string   `json:"type"`
		ParentID      string   `json:"parent_id"`
		AttachmentIDs []string `json:"attachment_ids"`
	}
	if err := decodeBody(r, &input); err != nil || strings.TrimSpace(input.Content) == "" {
		writeError(w, 400, errors.New("content is required"))
		return
	}
	item, err := s.store.CreateCommentWithAttachments(r.Context(), ws.ID, chi.URLParam(r, "id"), input.Content, input.Type, input.ParentID, input.AttachmentIDs)
	if err != nil {
		s.respondStoreError(w, err)
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
	writeJSON(w, 200, map[string]any{"statuses": items, "categories": []string{"backlog", "todo", "in_progress", "in_review", "done", "blocked", "cancelled"}, "total": len(items)})
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
	rows, err := s.store.db.QueryContext(r.Context(), `SELECT id, issue_id, status, thread_id, runtime, handoff_note, summary, error, started_at, completed_at, created_at FROM task_runs WHERE issue_id=? ORDER BY created_at DESC`, chi.URLParam(r, "id"))
	if err != nil {
		s.fail(w, err)
		return
	}
	defer rows.Close()
	items := []any{}
	for rows.Next() {
		var id, issueID, status, createdAt string
		var threadID, runtimeName, handoffNote, summary, errorText, startedAt, completedAt sql.NullString
		if err := rows.Scan(&id, &issueID, &status, &threadID, &runtimeName, &handoffNote, &summary, &errorText, &startedAt, &completedAt, &createdAt); err != nil {
			s.fail(w, err)
			return
		}
		items = append(items, map[string]any{"id": id, "agent_id": LocalAgentID, "runtime_id": nullableString(runtimeName), "issue_id": issueID, "status": status, "priority": 0, "handoff_note": nullableString(handoffNote), "dispatched_at": nil, "started_at": stringPtr(startedAt), "completed_at": stringPtr(completedAt), "result": map[string]any{"thread_id": nullableString(threadID), "summary": nullableString(summary)}, "error": stringPtr(errorText), "created_at": createdAt})
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
	var id, status, runtimeID, createdAt string
	err = s.store.db.QueryRowContext(r.Context(), `SELECT id, status, COALESCE(runtime,''), created_at FROM task_runs WHERE issue_id=? AND status IN ('queued','dispatched','waiting_local_directory','running') ORDER BY created_at DESC LIMIT 1`, issueID).Scan(&id, &status, &runtimeID, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		writeJSON(w, http.StatusOK, map[string]any{"tasks": []any{}})
		return
	}
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"tasks": []any{map[string]any{"id": id, "agent_id": LocalAgentID, "runtime_id": runtimeID, "issue_id": issueID, "status": status, "priority": 0, "created_at": createdAt}}})
}

func (s *Server) cancelTask(w http.ResponseWriter, r *http.Request) {
	ws, err := s.workspaceFor(r)
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	issueID, taskID := chi.URLParam(r, "id"), chi.URLParam(r, "taskID")
	stamp := now()
	result, err := s.store.db.ExecContext(r.Context(), `UPDATE task_runs SET status='cancelled', completed_at=?, updated_at=? WHERE id=? AND issue_id=? AND status IN ('queued','dispatched','waiting_local_directory','running') AND issue_id IN (SELECT id FROM issues WHERE workspace_id=?)`, stamp, stamp, taskID, issueID, ws.ID)
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
	if err := s.store.db.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM task_runs WHERE issue_id=? AND status IN ('queued','dispatched','waiting_local_directory','running')`, issueID).Scan(&active); err != nil {
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
	var id, status, createdAt string
	var runtimeID, handoffNote, threadID, summary, errorText, startedAt, completedAt sql.NullString
	err := s.store.db.QueryRowContext(ctx, `SELECT tr.id, tr.status, tr.runtime, tr.handoff_note, tr.thread_id, tr.summary, tr.error, tr.started_at, tr.completed_at, tr.created_at FROM task_runs tr JOIN issues i ON i.id=tr.issue_id WHERE tr.id=? AND tr.issue_id=? AND i.workspace_id=?`, taskID, issueID, workspaceID).Scan(&id, &status, &runtimeID, &handoffNote, &threadID, &summary, &errorText, &startedAt, &completedAt, &createdAt)
	if err != nil {
		return nil, err
	}
	return map[string]any{"id": id, "agent_id": LocalAgentID, "runtime_id": nullableString(runtimeID), "issue_id": issueID, "status": status, "priority": 0, "handoff_note": nullableString(handoffNote), "started_at": stringPtr(startedAt), "completed_at": stringPtr(completedAt), "result": map[string]any{"thread_id": nullableString(threadID), "summary": nullableString(summary)}, "error": stringPtr(errorText), "created_at": createdAt}, nil
}

func nullableString(v sql.NullString) string {
	if v.Valid {
		return v.String
	}
	return ""
}

func (s *Server) respondStoreError(w http.ResponseWriter, err error) {
	if IsNotFound(err) {
		writeError(w, 404, errors.New("not found"))
		return
	}
	s.fail(w, err)
}
func (s *Server) fail(w http.ResponseWriter, err error) {
	s.logger.Error("local workbench request failed", "error", err)
	writeError(w, 500, errors.New("local database operation failed"))
}

type localHub struct {
	mu      sync.Mutex
	clients map[*websocket.Conn]struct{}
	closed  bool
}

func newLocalHub() *localHub { return &localHub{clients: map[*websocket.Conn]struct{}{}} }
func (h *localHub) add(c *websocket.Conn) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		_ = c.Close()
		return
	}
	h.clients[c] = struct{}{}
}
func (h *localHub) remove(c *websocket.Conn) {
	h.mu.Lock()
	delete(h.clients, c)
	h.mu.Unlock()
	_ = c.Close()
}
func (h *localHub) broadcast(kind string, payload any) {
	raw, _ := json.Marshal(map[string]any{"type": kind, "payload": payload, "actor_id": LocalActorID, "actor_type": "system"})
	h.mu.Lock()
	defer h.mu.Unlock()
	for c := range h.clients {
		_ = c.WriteMessage(websocket.TextMessage, raw)
	}
}
func (h *localHub) close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.closed = true
	for c := range h.clients {
		_ = c.Close()
	}
	h.clients = map[*websocket.Conn]struct{}{}
}

var upgrader = websocket.Upgrader{CheckOrigin: func(r *http.Request) bool {
	return strings.TrimSpace(r.Header.Get("Origin")) == ""
}}

func (s *Server) websocket(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	s.hub.add(conn)
	defer s.hub.remove(conn)
	_ = conn.SetReadDeadline(time.Time{})
	for {
		if _, _, err := conn.ReadMessage(); err != nil {
			return
		}
	}
}

func parseLimit(r *http.Request, fallback int) int {
	value, err := strconv.Atoi(r.URL.Query().Get("limit"))
	if err != nil || value <= 0 {
		return fallback
	}
	if value > 500 {
		return 500
	}
	return value
}
