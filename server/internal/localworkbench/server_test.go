package localworkbench

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gorilla/websocket"
)

func TestSQLiteWorkbenchPersistsCoreLifecycle(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "multica.db")
	app, err := New(context.Background(), databasePath, nil)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(app.Router())

	var workspaces []Workspace
	requestJSON(t, server.URL, http.MethodGet, "/api/workspaces", nil, http.StatusOK, &workspaces)
	if len(workspaces) != 1 || workspaces[0].Slug != "local" {
		t.Fatalf("default workspaces = %#v", workspaces)
	}

	var project Project
	requestJSON(t, server.URL, http.MethodPost, "/api/projects", map[string]any{
		"title": "Multica", "status": "in_progress", "priority": "high",
		"resources": []any{map[string]any{
			"resource_type": "local_directory",
			"resource_ref":  map[string]any{"local_path": "/tmp/multica", "daemon_id": "local"},
		}},
	}, http.StatusCreated, &project)
	if project.Title != "Multica" || project.WorkspaceID != workspaces[0].ID || project.ResourceCount != 1 {
		t.Fatalf("created project = %#v", project)
	}

	var issue Issue
	requestJSON(t, server.URL, http.MethodPost, "/api/issues", map[string]any{
		"title": "Replace PostgreSQL", "project_id": project.ID, "status": "todo",
	}, http.StatusCreated, &issue)
	if issue.Identifier != "LOCAL-1" || issue.ProjectID == nil || *issue.ProjectID != project.ID {
		t.Fatalf("created issue = %#v", issue)
	}

	var comment Comment
	requestJSON(t, server.URL, http.MethodPost, "/api/issues/"+issue.ID+"/comments", map[string]any{
		"content": "SQLite lifecycle verified",
	}, http.StatusCreated, &comment)
	if comment.IssueID != issue.ID || comment.Content != "SQLite lifecycle verified" {
		t.Fatalf("created comment = %#v", comment)
	}

	var timeline []map[string]any
	requestJSON(t, server.URL, http.MethodGet, "/api/issues/"+issue.ID+"/timeline", nil, http.StatusOK, &timeline)
	if len(timeline) != 2 || timeline[0]["type"] != "activity" || timeline[1]["type"] != "comment" {
		t.Fatalf("timeline = %#v", timeline)
	}

	server.Close()
	if err := app.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := New(context.Background(), databasePath, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	persisted, err := reopened.store.GetIssue(context.Background(), workspaces[0].ID, issue.ID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Title != issue.Title {
		t.Fatalf("persisted issue title = %q, want %q", persisted.Title, issue.Title)
	}
}

func TestSQLiteWorkbenchHasNoAuthOrCloudRoutes(t *testing.T) {
	app, err := New(context.Background(), filepath.Join(t.TempDir(), "multica.db"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	server := httptest.NewServer(app.Router())
	defer server.Close()

	for _, path := range []string{
		"/auth/send-code", "/auth/verify-code", "/api/me", "/api/tokens", "/api/invitations", "/api/billing/subscription",
		"/api/squads", "/api/workspaces/local/members",
	} {
		requestJSON[map[string]any](t, server.URL, http.MethodGet, path, nil, http.StatusNotFound, nil)
	}

	var health map[string]any
	requestJSON(t, server.URL, http.MethodGet, "/health", nil, http.StatusOK, &health)
	if health["storage"] != "sqlite" || health["mode"] != "local" {
		t.Fatalf("health = %#v", health)
	}
	requestJSON[map[string]any](t, server.URL, http.MethodPost, "/api/workspaces", map[string]any{"name": "Second"}, http.StatusMethodNotAllowed, nil)
}

func TestSQLiteWorkbenchExposesOnlyItsExistingLocalWorkspace(t *testing.T) {
	app, err := New(context.Background(), filepath.Join(t.TempDir(), "multica.db"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()

	first, err := app.store.ListWorkspaces(t.Context())
	if err != nil || len(first) != 1 {
		t.Fatalf("initial local workspace = %#v, err = %v", first, err)
	}
	if _, err := app.store.CreateWorkspace(t.Context(), "Legacy", "legacy-cloud", "", "OLD"); err != nil {
		t.Fatal(err)
	}

	server := httptest.NewServer(app.Router())
	defer server.Close()
	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, server.URL+"/api/workspaces", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("X-Workspace-Slug", "legacy-cloud")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var exposed []Workspace
	if err := json.NewDecoder(response.Body).Decode(&exposed); err != nil {
		t.Fatal(err)
	}
	if len(exposed) != 1 || exposed[0].ID != first[0].ID {
		t.Fatalf("exposed workspaces = %#v, want only %#v", exposed, first[0])
	}
}

func TestSQLiteWorkbenchServesLocalIssuePageDependencies(t *testing.T) {
	app, err := New(context.Background(), filepath.Join(t.TempDir(), "multica.db"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	server := httptest.NewServer(app.Router())
	defer server.Close()

	var workspaces []Workspace
	requestJSON(t, server.URL, http.MethodGet, "/api/workspaces", nil, http.StatusOK, &workspaces)
	paths := []string{
		"/api/runtimes?workspace_id=" + workspaces[0].ID,
		"/api/properties",
		"/api/issue-views?scope_type=workspace",
		"/api/issue-view-preferences?scope_type=workspace",
		"/api/chat/sessions?status=all",
		"/api/chat/pending-tasks",
		"/api/chat/pending-tasks/has-any",
	}
	for _, path := range paths {
		requestJSON[any](t, server.URL, http.MethodGet, path, nil, http.StatusOK, nil)
	}
}

func TestSQLiteWorkbenchRejectsBrowserOriginWritesAndWebSockets(t *testing.T) {
	app, err := New(context.Background(), filepath.Join(t.TempDir(), "multica.db"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	server := httptest.NewServer(app.Router())
	defer server.Close()

	request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, server.URL+"/api/projects", bytes.NewBufferString(`{"title":"drive-by"}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "text/plain")
	request.Header.Set("Origin", "https://evil.example")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("browser-origin write = %d, want 403", response.StatusCode)
	}

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/ws"
	header := http.Header{"Origin": []string{"https://evil.example"}}
	conn, response, err := websocket.DefaultDialer.Dial(wsURL, header)
	if conn != nil {
		_ = conn.Close()
	}
	if err == nil || response == nil || response.StatusCode != http.StatusForbidden {
		t.Fatalf("browser-origin websocket response = %#v, err = %v; want 403", response, err)
	}
}

func TestSQLiteWorkbenchScopesNestedDataToOnlyLocalWorkspace(t *testing.T) {
	app, err := New(context.Background(), filepath.Join(t.TempDir(), "multica.db"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	local, err := app.store.ListWorkspaces(t.Context())
	if err != nil || len(local) != 1 {
		t.Fatalf("local workspace = %#v, err = %v", local, err)
	}
	legacy, err := app.store.CreateWorkspace(t.Context(), "Legacy", "legacy", "", "OLD")
	if err != nil {
		t.Fatal(err)
	}
	project, err := app.store.CreateProject(t.Context(), legacy.ID, "Hidden", "", "", "planned", "none", nil)
	if err != nil {
		t.Fatal(err)
	}
	issue, err := app.store.CreateIssue(t.Context(), legacy.ID, CreateIssueInput{Title: "Hidden issue", ProjectID: &project.ID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.store.CreateComment(t.Context(), issue.ID, "hidden", "comment", ""); err != nil {
		t.Fatal(err)
	}
	stamp := now()
	if _, err := app.store.db.ExecContext(t.Context(), `INSERT INTO task_runs(id, issue_id, status, created_at, updated_at) VALUES (?, ?, 'completed', ?, ?)`, "hidden-run", issue.ID, stamp, stamp); err != nil {
		t.Fatal(err)
	}

	server := httptest.NewServer(app.Router())
	defer server.Close()
	for _, path := range []string{
		"/api/projects/" + project.ID,
		"/api/projects/" + project.ID + "/resources",
		"/api/issues/" + issue.ID + "/comments",
		"/api/issues/" + issue.ID + "/timeline",
		"/api/issues/" + issue.ID + "/task-runs",
	} {
		requestJSON[any](t, server.URL, http.MethodGet, path, nil, http.StatusNotFound, nil)
	}
	requestJSON[any](t, server.URL, http.MethodGet, "/api/daemon/tasks/hidden-run/status", nil, http.StatusNotFound, nil)
	for _, path := range []string{
		"/api/daemon/tasks/hidden-run/start",
		"/api/daemon/tasks/hidden-run/progress",
		"/api/daemon/tasks/hidden-run/session",
		"/api/daemon/tasks/hidden-run/messages",
	} {
		requestJSON[any](t, server.URL, http.MethodPost, path, map[string]any{"summary": "must not mutate"}, http.StatusNotFound, nil)
	}
}

func TestSQLiteWorkbenchRunsAssignedIssueThroughDaemonProtocol(t *testing.T) {
	app, err := New(context.Background(), filepath.Join(t.TempDir(), "multica.db"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	server := httptest.NewServer(app.Router())
	defer server.Close()

	var workspaces []Workspace
	requestJSON(t, server.URL, http.MethodGet, "/api/workspaces", nil, http.StatusOK, &workspaces)
	var registration struct {
		Runtimes []localRuntime `json:"runtimes"`
	}
	requestJSON(t, server.URL, http.MethodPost, "/api/daemon/register", map[string]any{
		"workspace_id": workspaces[0].ID, "daemon_id": "local-daemon", "device_name": "Mac",
		"runtimes": []any{map[string]any{"name": "Codex", "type": "codex", "version": "1.0", "status": "online"}},
	}, http.StatusOK, &registration)
	if len(registration.Runtimes) != 1 {
		t.Fatalf("registered runtimes = %#v", registration.Runtimes)
	}
	var agents []map[string]any
	requestJSON(t, server.URL, http.MethodGet, "/api/agents", nil, http.StatusOK, &agents)
	if len(agents) != 1 || agents[0]["id"] != LocalAgentID {
		t.Fatalf("local agents = %#v", agents)
	}

	var issue Issue
	requestJSON(t, server.URL, http.MethodPost, "/api/issues", map[string]any{
		"title": "Run locally", "status": "todo", "assignee_type": "agent", "assignee_id": LocalAgentID,
	}, http.StatusCreated, &issue)
	var claim struct {
		Tasks []map[string]any `json:"tasks"`
	}
	requestJSON(t, server.URL, http.MethodPost, "/api/daemon/tasks/claim", map[string]any{
		"daemon_id": "local-daemon", "runtime_ids": []string{registration.Runtimes[0].ID}, "max_tasks": 1,
	}, http.StatusOK, &claim)
	if len(claim.Tasks) != 1 || claim.Tasks[0]["issue_id"] != issue.ID {
		t.Fatalf("claimed tasks = %#v", claim.Tasks)
	}
	if _, exists := claim.Tasks[0]["auth_token"]; exists {
		t.Fatalf("local task must not carry auth_token: %#v", claim.Tasks[0])
	}
	taskID := claim.Tasks[0]["id"].(string)
	requestJSON[any](t, server.URL, http.MethodPost, "/api/daemon/tasks/"+taskID+"/start", map[string]any{}, http.StatusOK, nil)
	requestJSON[any](t, server.URL, http.MethodPost, "/api/daemon/tasks/"+taskID+"/session", map[string]any{"session_id": "codex-thread-1"}, http.StatusOK, nil)
	requestJSON[any](t, server.URL, http.MethodPost, "/api/daemon/tasks/"+taskID+"/complete", map[string]any{"output": "Implemented and verified", "session_id": "codex-thread-1"}, http.StatusOK, nil)
	requestJSON[any](t, server.URL, http.MethodPost, "/api/daemon/tasks/"+taskID+"/complete", map[string]any{"output": "Implemented and verified", "session_id": "codex-thread-1"}, http.StatusOK, nil)

	var runs []map[string]any
	requestJSON(t, server.URL, http.MethodGet, "/api/issues/"+issue.ID+"/task-runs", nil, http.StatusOK, &runs)
	if len(runs) != 1 || runs[0]["status"] != "completed" {
		t.Fatalf("task runs = %#v", runs)
	}
	var comments []Comment
	requestJSON(t, server.URL, http.MethodGet, "/api/issues/"+issue.ID+"/comments", nil, http.StatusOK, &comments)
	if len(comments) != 1 || comments[0].Content != "Implemented and verified" {
		t.Fatalf("completion comments = %#v", comments)
	}
}

func requestJSON[T any](t *testing.T, baseURL, method, path string, body any, wantStatus int, output *T) {
	t.Helper()
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		reader = bytes.NewReader(raw)
	}
	request, err := http.NewRequestWithContext(t.Context(), method, baseURL+path, reader)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Workspace-Slug", "local")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != wantStatus {
		raw, _ := io.ReadAll(response.Body)
		t.Fatalf("%s %s status = %d, want %d: %s", method, path, response.StatusCode, wantStatus, raw)
	}
	if output != nil {
		if err := json.NewDecoder(response.Body).Decode(output); err != nil {
			t.Fatal(err)
		}
	}
}
