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
	if issue.Metadata == nil {
		t.Fatal("created issue metadata must be an object, got null")
	}
	var listed struct {
		Issues []Issue `json:"issues"`
		Total  int     `json:"total"`
	}
	requestJSON(t, server.URL, http.MethodGet, "/api/issues?status_category=todo&limit=50", nil, http.StatusOK, &listed)
	if listed.Total != 1 || len(listed.Issues) != 1 || listed.Issues[0].ID != issue.ID {
		t.Fatalf("listed issues = %#v", listed)
	}
	var queried struct {
		Issues []Issue `json:"issues"`
		Total  int     `json:"total"`
	}
	requestJSON(t, server.URL, http.MethodPost, "/api/issues/query", map[string]any{"ids": issue.ID}, http.StatusOK, &queried)
	if queried.Total != 1 || len(queried.Issues) != 1 || queried.Issues[0].ID != issue.ID {
		t.Fatalf("queried issues = %#v", queried)
	}
	var issueGroups struct {
		Groups []struct {
			Key string `json:"key"`
		} `json:"groups"`
	}
	tableQuery := map[string]any{
		"query": map[string]any{
			"scope":   map[string]any{"kind": "workspace"},
			"filters": map[string]any{},
		},
		"group": map[string]any{"kind": "status"},
	}
	requestJSON(t, server.URL, http.MethodPost, "/api/issues/table/groups", tableQuery, http.StatusOK, &issueGroups)
	if len(issueGroups.Groups) != 1 || issueGroups.Groups[0].Key != "status:todo" {
		t.Fatalf("issue groups = %#v, want status:todo", issueGroups.Groups)
	}
	var issueRows struct {
		Rows []struct {
			Issue Issue `json:"issue"`
		} `json:"rows"`
	}
	tableQuery["group_key"] = "status:todo"
	tableQuery["hierarchy"] = map[string]any{"enabled": false}
	tableQuery["parent_id"] = nil
	requestJSON(t, server.URL, http.MethodPost, "/api/issues/table/rows", tableQuery, http.StatusOK, &issueRows)
	if len(issueRows.Rows) != 1 || issueRows.Rows[0].Issue.ID != issue.ID {
		t.Fatalf("issue rows = %#v, want created issue", issueRows.Rows)
	}
	for _, test := range []struct {
		name string
		kind string
		key  string
	}{
		{name: "project", kind: "project", key: "project:" + project.ID},
		{name: "status category", kind: "status_category", key: "status_category:todo"},
	} {
		t.Run(test.name+" group key", func(t *testing.T) {
			tableQuery["group"] = map[string]any{"kind": test.kind}
			requestJSON(t, server.URL, http.MethodPost, "/api/issues/table/groups", tableQuery, http.StatusOK, &issueGroups)
			if len(issueGroups.Groups) != 1 || issueGroups.Groups[0].Key != test.key {
				t.Fatalf("issue groups = %#v, want %s", issueGroups.Groups, test.key)
			}
		})
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

	var neighbor Issue
	requestJSON(t, server.URL, http.MethodPost, "/api/issues", map[string]any{
		"title": "Neighbor", "project_id": project.ID, "status": "in_progress",
	}, http.StatusCreated, &neighbor)
	var moved Issue
	requestJSON(t, server.URL, http.MethodPost, "/api/issues/"+issue.ID+"/move", map[string]any{
		"status": "in_progress", "before_id": neighbor.ID, "after_id": nil,
	}, http.StatusOK, &moved)
	if moved.Status != "in_progress" || moved.StatusCategory != "in_progress" || moved.Position != neighbor.Position+1 {
		t.Fatalf("moved issue = %#v, neighbor = %#v", moved, neighbor)
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
	var issue Issue
	requestJSON(t, server.URL, http.MethodPost, "/api/issues", map[string]any{"title": "Dependencies"}, http.StatusCreated, &issue)
	paths := []string{
		"/api/runtimes?workspace_id=" + workspaces[0].ID,
		"/api/properties",
		"/api/issue-views?scope_type=workspace",
		"/api/issue-view-preferences?scope_type=workspace",
		"/api/chat/sessions?status=all",
		"/api/chat/pending-tasks",
		"/api/chat/pending-tasks/has-any",
		"/api/quick-actions",
		"/api/issues/" + issue.ID + "/labels",
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
	requestJSON[any](t, server.URL, http.MethodGet, "/api/tasks/hidden-run/messages", nil, http.StatusNotFound, nil)
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
	var agent map[string]any
	requestJSON(t, server.URL, http.MethodGet, "/api/agents/"+LocalAgentID, nil, http.StatusOK, &agent)
	if agent["name"] != "Codex" {
		t.Fatalf("local agent = %#v", agent)
	}

	var issue Issue
	requestJSON(t, server.URL, http.MethodPost, "/api/issues", map[string]any{
		"title": "Run locally", "status": "todo",
	}, http.StatusCreated, &issue)
	requestJSON(t, server.URL, http.MethodPut, "/api/issues/"+issue.ID, map[string]any{
		"assignee_type": "agent", "assignee_id": LocalAgentID, "handoff_note": "Verify the local execution chain",
	}, http.StatusOK, &issue)
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
	if claim.Tasks[0]["handoff_note"] != "Verify the local execution chain" {
		t.Fatalf("claimed task handoff note = %#v", claim.Tasks[0]["handoff_note"])
	}
	taskID := claim.Tasks[0]["id"].(string)
	requestJSON[any](t, server.URL, http.MethodPost, "/api/daemon/tasks/"+taskID+"/start", map[string]any{}, http.StatusOK, nil)
	requestJSON[any](t, server.URL, http.MethodPost, "/api/daemon/tasks/"+taskID+"/session", map[string]any{"session_id": "codex-thread-1"}, http.StatusOK, nil)
	messages := map[string]any{"messages": []any{
		map[string]any{"seq": 2, "type": "tool_result", "tool": "Read", "output": "ok"},
		map[string]any{"seq": 1, "type": "tool_use", "tool": "Read", "input": map[string]any{"file_path": "README.md"}},
	}}
	requestJSON[any](t, server.URL, http.MethodPost, "/api/daemon/tasks/"+taskID+"/messages", messages, http.StatusOK, nil)
	requestJSON[any](t, server.URL, http.MethodPost, "/api/daemon/tasks/"+taskID+"/messages", messages, http.StatusOK, nil)
	requestJSON[any](t, server.URL, http.MethodPost, "/api/daemon/tasks/"+taskID+"/complete", map[string]any{"output": "Implemented and verified", "session_id": "codex-thread-1"}, http.StatusOK, nil)
	requestJSON[any](t, server.URL, http.MethodPost, "/api/daemon/tasks/"+taskID+"/complete", map[string]any{"output": "Implemented and verified", "session_id": "codex-thread-1"}, http.StatusOK, nil)

	var runs []map[string]any
	requestJSON(t, server.URL, http.MethodGet, "/api/issues/"+issue.ID+"/task-runs", nil, http.StatusOK, &runs)
	if len(runs) != 1 || runs[0]["status"] != "completed" {
		t.Fatalf("task runs = %#v", runs)
	}
	if runs[0]["handoff_note"] != "Verify the local execution chain" {
		t.Fatalf("task run handoff note = %#v", runs[0]["handoff_note"])
	}
	var transcript []map[string]any
	requestJSON(t, server.URL, http.MethodGet, "/api/tasks/"+taskID+"/messages", nil, http.StatusOK, &transcript)
	if len(transcript) != 2 || transcript[0]["seq"] != float64(1) || transcript[1]["seq"] != float64(2) {
		t.Fatalf("task transcript = %#v", transcript)
	}
	if transcript[0]["task_id"] != taskID || transcript[0]["issue_id"] != issue.ID {
		t.Fatalf("task transcript scope = %#v", transcript[0])
	}
	var comments []Comment
	requestJSON(t, server.URL, http.MethodGet, "/api/issues/"+issue.ID+"/comments", nil, http.StatusOK, &comments)
	if len(comments) != 1 || comments[0].Content != "Implemented and verified" {
		t.Fatalf("completion comments = %#v", comments)
	}
	requestJSON(t, server.URL, http.MethodPut, "/api/issues/"+issue.ID, map[string]any{
		"title": "Run locally after edit",
	}, http.StatusOK, &issue)
	claim.Tasks = nil
	requestJSON(t, server.URL, http.MethodPost, "/api/daemon/tasks/claim", map[string]any{
		"daemon_id": "local-daemon", "runtime_ids": []string{registration.Runtimes[0].ID}, "max_tasks": 1,
	}, http.StatusOK, &claim)
	if len(claim.Tasks) != 0 {
		t.Fatalf("non-run issue edit unexpectedly queued tasks = %#v", claim.Tasks)
	}

	var suppressed Issue
	requestJSON(t, server.URL, http.MethodPost, "/api/issues", map[string]any{
		"title": "Assign without starting", "status": "todo",
	}, http.StatusCreated, &suppressed)
	requestJSON(t, server.URL, http.MethodPut, "/api/issues/"+suppressed.ID, map[string]any{
		"assignee_type": "agent", "assignee_id": LocalAgentID, "suppress_run": true,
	}, http.StatusOK, &suppressed)
	requestJSON(t, server.URL, http.MethodPost, "/api/daemon/register", map[string]any{
		"workspace_id": workspaces[0].ID, "daemon_id": "local-daemon", "device_name": "Mac",
		"runtimes": []any{map[string]any{"name": "Codex", "type": "codex", "version": "1.0", "status": "online"}},
	}, http.StatusOK, &registration)
	claim.Tasks = nil
	requestJSON(t, server.URL, http.MethodPost, "/api/daemon/tasks/claim", map[string]any{
		"daemon_id": "local-daemon", "runtime_ids": []string{registration.Runtimes[0].ID}, "max_tasks": 1,
	}, http.StatusOK, &claim)
	if len(claim.Tasks) != 0 {
		t.Fatalf("suppressed assignment unexpectedly queued tasks = %#v", claim.Tasks)
	}
}

func TestSQLiteWorkbenchQuickCreatesWithOnlineLocalCodex(t *testing.T) {
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
		"runtimes": []any{map[string]any{"name": "Codex", "type": "codex", "version": "0.147.0", "status": "online"}},
	}, http.StatusOK, &registration)
	var runtimes []struct {
		Metadata map[string]any `json:"metadata"`
	}
	requestJSON(t, server.URL, http.MethodGet, "/api/runtimes", nil, http.StatusOK, &runtimes)
	if len(runtimes) != 1 || runtimes[0].Metadata["cli_version"] != "0.147.0" {
		t.Fatalf("runtime metadata = %#v", runtimes)
	}

	var issue Issue
	requestJSON(t, server.URL, http.MethodPost, "/api/issues/quick-create", map[string]any{
		"agent_id": LocalAgentID, "prompt": "Inspect the local chain\nand report the result", "priority": "high",
	}, http.StatusCreated, &issue)
	if issue.Title != "Inspect the local chain" || issue.Description == nil || *issue.Description != "Inspect the local chain\nand report the result" || issue.AssigneeID == nil || *issue.AssigneeID != LocalAgentID {
		t.Fatalf("quick-created issue = %#v", issue)
	}
	var queued int
	if err := app.store.db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM task_runs WHERE issue_id=? AND status='queued'`, issue.ID).Scan(&queued); err != nil {
		t.Fatal(err)
	}
	if queued != 1 {
		t.Fatalf("quick-create queued runs = %d, want 1", queued)
	}
}

func TestSQLiteWorkbenchUpgradesTaskRunHandoffNote(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "multica.db")
	store, err := Open(t.Context(), databasePath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(t.Context(), `ALTER TABLE task_runs DROP COLUMN handoff_note`); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(t.Context(), databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	rows, err := reopened.db.QueryContext(t.Context(), `PRAGMA table_info(task_runs)`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	found := false
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, columnType string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			t.Fatal(err)
		}
		found = found || name == "handoff_note"
	}
	if !found {
		t.Fatal("handoff_note column was not restored")
	}
	var version int
	if err := reopened.db.QueryRowContext(t.Context(), `SELECT MAX(version) FROM schema_version`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != 5 {
		t.Fatalf("schema version = %d, want 5", version)
	}
	var taskMessagesTable string
	if err := reopened.db.QueryRowContext(t.Context(), `SELECT name FROM sqlite_master WHERE type='table' AND name='task_messages'`).Scan(&taskMessagesTable); err != nil {
		t.Fatal(err)
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
