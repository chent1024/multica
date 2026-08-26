package localworkbench

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func mustTestProject(t *testing.T, app *Server, workspaceID string) Project {
	t.Helper()
	projects, err := app.store.ListProjects(t.Context(), workspaceID)
	if err != nil {
		t.Fatal(err)
	}
	if len(projects) > 0 {
		return projects[0]
	}
	project, err := app.store.CreateProject(t.Context(), workspaceID, "Test project", "", "", "planned", "none", nil)
	if err != nil {
		t.Fatal(err)
	}
	return project
}

func TestIssueProjectOwnershipIsRequiredImmutableAndInherited(t *testing.T) {
	app, err := New(t.Context(), filepath.Join(t.TempDir(), "multica.db"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	workspaces, _ := app.store.ListWorkspaces(t.Context())
	first := mustTestProject(t, app, workspaces[0].ID)
	second, err := app.store.CreateProject(t.Context(), workspaces[0].ID, "Second", "", "", "planned", "none", nil)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := app.store.CreateIssue(t.Context(), workspaces[0].ID, CreateIssueInput{Title: "Missing"}); !errors.Is(err, ErrIssueProjectRequired) {
		t.Fatalf("missing project error = %v", err)
	}
	unknownProjectID := "missing-project"
	if _, err := app.store.CreateIssue(t.Context(), workspaces[0].ID, CreateIssueInput{Title: "Unknown", ProjectID: &unknownProjectID}); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("unknown project error = %v", err)
	}
	parent, err := app.store.CreateIssue(t.Context(), workspaces[0].ID, CreateIssueInput{Title: "Parent", ProjectID: &first.ID})
	if err != nil {
		t.Fatal(err)
	}
	child, err := app.store.CreateIssue(t.Context(), workspaces[0].ID, CreateIssueInput{Title: "Child", ParentIssueID: &parent.ID})
	if err != nil {
		t.Fatal(err)
	}
	if child.ProjectID == nil || *child.ProjectID != first.ID {
		t.Fatalf("child project = %#v, want %s", child.ProjectID, first.ID)
	}
	if _, err := app.store.CreateIssue(t.Context(), workspaces[0].ID, CreateIssueInput{Title: "Wrong child", ParentIssueID: &parent.ID, ProjectID: &second.ID}); !errors.Is(err, ErrIssueProjectMismatch) {
		t.Fatalf("cross-project child error = %v", err)
	}
	if _, err := app.store.UpdateIssue(t.Context(), workspaces[0].ID, parent.ID, map[string]any{"project_id": second.ID}); !errors.Is(err, ErrIssueProjectImmutable) {
		t.Fatalf("project move error = %v", err)
	}
	if _, err := app.store.UpdateIssue(t.Context(), workspaces[0].ID, parent.ID, map[string]any{"project_id": nil}); !errors.Is(err, ErrIssueProjectRequired) {
		t.Fatalf("project clear error = %v", err)
	}
	unchanged, err := app.store.UpdateIssue(t.Context(), workspaces[0].ID, parent.ID, map[string]any{"project_id": " " + first.ID + " "})
	if err != nil || unchanged.ProjectID == nil || *unchanged.ProjectID != first.ID {
		t.Fatalf("canonical project update = %#v, err = %v", unchanged.ProjectID, err)
	}

	server := httptest.NewServer(app.Router())
	defer server.Close()
	requestJSON[any](t, server.URL, http.MethodDelete, "/api/projects/"+first.ID, nil, http.StatusConflict, nil)
	if _, err := app.store.GetProject(t.Context(), workspaces[0].ID, first.ID); err != nil {
		t.Fatalf("project was deleted: %v", err)
	}
}

func TestLegacyProjectlessIssueBackfillAndRepair(t *testing.T) {
	for _, tc := range []struct {
		name         string
		projectCount int
		wantAssigned bool
	}{
		{name: "one project is unambiguous", projectCount: 1, wantAssigned: true},
		{name: "multiple projects require repair", projectCount: 2, wantAssigned: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "legacy.db")
			db, err := sql.Open("sqlite", path)
			if err != nil {
				t.Fatal(err)
			}
			legacySchema := strings.Replace(schemaSQL, "parent_issue_id TEXT,\n    project_id TEXT NOT NULL,\n    position", "parent_issue_id TEXT,\n    project_id TEXT,\n    position", 1)
			if _, err := db.ExecContext(t.Context(), legacySchema); err != nil {
				t.Fatal(err)
			}
			stamp := now()
			if _, err := db.ExecContext(t.Context(), `INSERT INTO workspaces(id,name,slug,settings,repos,issue_prefix,created_at,updated_at) VALUES ('ws','Local','local','{}','[]','LOCAL',?,?)`, stamp, stamp); err != nil {
				t.Fatal(err)
			}
			for index := range tc.projectCount {
				if _, err := db.ExecContext(t.Context(), `INSERT INTO projects(id,workspace_id,title,status,priority,created_at,updated_at) VALUES (?,?,?,'planned','none',?,?)`, fmt.Sprintf("project-%d", index+1), "ws", fmt.Sprintf("Project %d", index+1), stamp, stamp); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := db.ExecContext(t.Context(), `INSERT INTO issues(id,workspace_id,number,title,status,priority,creator_type,creator_id,position,metadata,properties,revision,created_at,updated_at) VALUES ('legacy-issue','ws',1,'Legacy','backlog','none','system','local',0,'{}','{}',1,?,?)`, stamp, stamp); err != nil {
				t.Fatal(err)
			}
			if tc.projectCount == 2 {
				if _, err := db.ExecContext(t.Context(), `INSERT INTO issues(id,workspace_id,number,title,status,priority,creator_type,creator_id,project_id,position,metadata,properties,revision,created_at,updated_at) VALUES ('legacy-parent','ws',2,'Parent','backlog','none','system','local','project-1',0,'{}','{}',1,?,?)`, stamp, stamp); err != nil {
					t.Fatal(err)
				}
				if _, err := db.ExecContext(t.Context(), `UPDATE issues SET parent_issue_id='legacy-parent' WHERE id='legacy-issue'`); err != nil {
					t.Fatal(err)
				}
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}

			app, err := New(t.Context(), path, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer app.Close()
			issue, err := app.store.GetIssue(t.Context(), "ws", "legacy-issue")
			if err != nil {
				t.Fatal(err)
			}
			if tc.wantAssigned {
				if issue.ProjectID == nil || *issue.ProjectID != "project-1" {
					t.Fatalf("backfilled project = %#v", issue.ProjectID)
				}
				return
			}
			if issue.ProjectID != nil {
				t.Fatalf("ambiguous project was guessed: %q", *issue.ProjectID)
			}
			server := httptest.NewServer(app.Router())
			defer server.Close()
			requestJSON[any](t, server.URL, http.MethodPut, "/api/issues/"+issue.ID, map[string]any{"status": "in_progress"}, http.StatusBadRequest, nil)
			unchanged, err := app.store.GetIssue(t.Context(), "ws", issue.ID)
			if err != nil {
				t.Fatal(err)
			}
			if unchanged.Status != "backlog" || unchanged.Revision != issue.Revision {
				t.Fatalf("failed run request changed issue: status=%q revision=%d", unchanged.Status, unchanged.Revision)
			}
			var runCount int
			if err := app.store.db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM task_runs WHERE issue_id=?`, issue.ID).Scan(&runCount); err != nil {
				t.Fatal(err)
			}
			if runCount != 0 {
				t.Fatalf("failed run request persisted %d runs", runCount)
			}
			if _, _, _, err := app.store.ContinueIssueSession(t.Context(), "ws", issue.ID); !errors.Is(err, ErrIssueProjectRequired) {
				t.Fatalf("projectless continuation error = %v", err)
			}
			if err := app.store.enqueueAssignedIssue(t.Context(), "ws", issue.ID, ""); !errors.Is(err, ErrIssueProjectRequired) {
				t.Fatalf("projectless run error = %v", err)
			}
			if _, err := app.store.UpdateIssue(t.Context(), "ws", issue.ID, map[string]any{"project_id": "project-2"}); !errors.Is(err, ErrIssueProjectMismatch) {
				t.Fatalf("cross-project legacy repair error = %v", err)
			}
			repaired, err := app.store.UpdateIssue(t.Context(), "ws", issue.ID, map[string]any{"project_id": "project-1"})
			if err != nil || repaired.ProjectID == nil || *repaired.ProjectID != "project-1" {
				t.Fatalf("legacy repair = %#v, err = %v", repaired.ProjectID, err)
			}
			if _, err := app.store.UpdateIssue(t.Context(), "ws", issue.ID, map[string]any{"project_id": "project-2"}); !errors.Is(err, ErrIssueProjectImmutable) {
				t.Fatalf("second legacy repair error = %v", err)
			}
		})
	}
}

func TestLocalAttachmentUploadAndCommentBinding(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "multica.db")
	app, err := New(t.Context(), databasePath, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	server := httptest.NewServer(app.Router())
	defer server.Close()

	var workspaces []Workspace
	requestJSON(t, server.URL, http.MethodGet, "/api/workspaces", nil, http.StatusOK, &workspaces)
	project := mustTestProject(t, app, workspaces[0].ID)
	var issue Issue
	requestJSON(t, server.URL, http.MethodPost, "/api/issues", map[string]any{"title": "Paste screenshot", "project_id": project.ID}, http.StatusCreated, &issue)

	png := []byte("\x89PNG\r\n\x1a\nlocal screenshot")
	var form bytes.Buffer
	writer := multipart.NewWriter(&form)
	part, err := writer.CreateFormFile("file", "image.png")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(png); err != nil {
		t.Fatal(err)
	}
	if err := writer.WriteField("issue_id", issue.ID); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, server.URL+"/api/upload-file", &form)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", writer.FormDataContentType())
	request.Header.Set("X-Workspace-Slug", "local")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusCreated {
		raw, _ := io.ReadAll(response.Body)
		t.Fatalf("upload status = %d: %s", response.StatusCode, raw)
	}
	var attachment Attachment
	if err := json.NewDecoder(response.Body).Decode(&attachment); err != nil {
		t.Fatal(err)
	}
	if attachment.Filename != "image.png" || attachment.ContentType != "image/png" || attachment.IssueID == nil || *attachment.IssueID != issue.ID {
		t.Fatalf("attachment = %#v", attachment)
	}
	if attachment.MarkdownURL != "/api/attachments/"+attachment.ID+"/download" {
		t.Fatalf("markdown URL = %q", attachment.MarkdownURL)
	}

	download, err := http.Get(server.URL + attachment.DownloadURL)
	if err != nil {
		t.Fatal(err)
	}
	defer download.Body.Close()
	downloaded, err := io.ReadAll(download.Body)
	if err != nil {
		t.Fatal(err)
	}
	if download.StatusCode != http.StatusOK || download.Header.Get("Content-Type") != "image/png" || !bytes.Equal(downloaded, png) {
		t.Fatalf("download status=%d content-type=%q body=%q", download.StatusCode, download.Header.Get("Content-Type"), downloaded)
	}

	var comment Comment
	requestJSON(t, server.URL, http.MethodPost, "/api/issues/"+issue.ID+"/comments", map[string]any{
		"content": "Screenshot attached", "attachment_ids": []string{attachment.ID},
	}, http.StatusCreated, &comment)
	if comment.AuthorType != "system" || comment.AuthorID != LocalActorID || len(comment.Attachments) != 1 || comment.Attachments[0].ID != attachment.ID {
		t.Fatalf("system comment = %#v", comment)
	}
	requestJSON(t, server.URL, http.MethodPost, "/api/comments/"+comment.ID+"/resolve", map[string]any{}, http.StatusOK, &comment)
	if comment.ResolvedAt == nil || comment.ResolvedByID == nil || *comment.ResolvedByID != LocalActorID {
		t.Fatalf("resolved comment = %#v", comment)
	}
	var resolvedTimeline []map[string]any
	requestJSON(t, server.URL, http.MethodGet, "/api/issues/"+issue.ID+"/timeline", nil, http.StatusOK, &resolvedTimeline)
	if len(resolvedTimeline) != 2 || resolvedTimeline[1]["resolved_at"] == nil || resolvedTimeline[1]["resolved_by_id"] != LocalActorID {
		t.Fatalf("resolved timeline = %#v", resolvedTimeline)
	}
	requestJSON(t, server.URL, http.MethodDelete, "/api/comments/"+comment.ID+"/resolve", nil, http.StatusOK, &comment)
	if comment.ResolvedAt != nil || comment.ResolvedByID != nil {
		t.Fatalf("unresolved comment = %#v", comment)
	}
	var comments []Comment
	requestJSON(t, server.URL, http.MethodGet, "/api/issues/"+issue.ID+"/comments", nil, http.StatusOK, &comments)
	if len(comments) != 1 || len(comments[0].Attachments) != 1 || comments[0].Attachments[0].ID != attachment.ID {
		t.Fatalf("listed comments = %#v", comments)
	}
	var timeline []map[string]any
	requestJSON(t, server.URL, http.MethodGet, "/api/issues/"+issue.ID+"/timeline", nil, http.StatusOK, &timeline)
	if len(timeline) != 2 {
		t.Fatalf("timeline = %#v", timeline)
	}
	attachments, ok := timeline[1]["attachments"].([]any)
	if !ok || len(attachments) != 1 {
		t.Fatalf("timeline attachments = %#v", timeline[1]["attachments"])
	}
	var issueAttachments []Attachment
	requestJSON(t, server.URL, http.MethodGet, "/api/issues/"+issue.ID+"/attachments", nil, http.StatusOK, &issueAttachments)
	if len(issueAttachments) != 1 || issueAttachments[0].ID != attachment.ID {
		t.Fatalf("issue attachments = %#v", issueAttachments)
	}
	unbound, err := app.store.createAttachment(t.Context(), workspaces[0].ID, "", "description.png", "image/png", png)
	if err != nil {
		t.Fatal(err)
	}
	requestJSON(t, server.URL, http.MethodPut, "/api/issues/"+issue.ID, map[string]any{
		"description":    "![description](/api/attachments/" + unbound.ID + "/download)",
		"attachment_ids": []string{unbound.ID},
	}, http.StatusOK, &issue)
	requestJSON(t, server.URL, http.MethodGet, "/api/issues/"+issue.ID+"/attachments", nil, http.StatusOK, &issueAttachments)
	if len(issueAttachments) != 2 {
		t.Fatalf("description attachment was not bound: %#v", issueAttachments)
	}

	requestJSON[map[string]any](t, server.URL, http.MethodPost, "/api/upload-file", map[string]any{"file": "not multipart"}, http.StatusUnsupportedMediaType, nil)
	if len(workspaces) != 1 {
		t.Fatalf("workspaces = %#v", workspaces)
	}
}

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

func TestIssueTableStatusFacetIncludesHiddenAndUnselectedStatuses(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "multica.db")
	app, err := New(t.Context(), databasePath, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	server := httptest.NewServer(app.Router())
	defer server.Close()
	workspaces, _ := app.store.ListWorkspaces(t.Context())
	project := mustTestProject(t, app, workspaces[0].ID)

	for _, input := range []map[string]any{
		{"title": "Todo", "status": "todo"},
		{"title": "Blocked", "status": "blocked"},
		{"title": "Cancelled", "status": "cancelled"},
	} {
		input["project_id"] = project.ID
		requestJSON[Issue](t, server.URL, http.MethodPost, "/api/issues", input, http.StatusCreated, nil)
	}

	var response struct {
		Total  int `json:"total"`
		Facets []struct {
			Kind   string `json:"kind"`
			Values []struct {
				Key   string `json:"key"`
				Count int    `json:"count"`
			} `json:"values"`
		} `json:"facets"`
	}
	requestJSON(t, server.URL, http.MethodPost, "/api/issues/table/facets", map[string]any{
		"query": map[string]any{
			"scope":   map[string]any{"kind": "workspace"},
			"filters": map[string]any{"statuses": []string{"todo"}},
		},
		"facets":        []map[string]any{{"kind": "status"}},
		"include_total": true,
	}, http.StatusOK, &response)

	if response.Total != 1 || len(response.Facets) != 1 || response.Facets[0].Kind != "status" {
		t.Fatalf("facet response = %#v", response)
	}
	counts := map[string]int{}
	for _, value := range response.Facets[0].Values {
		counts[value.Key] = value.Count
	}
	if counts["todo"] != 1 || counts["blocked"] != 1 || counts["cancelled"] != 1 {
		t.Fatalf("status facet counts = %#v", counts)
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
	project := mustTestProject(t, app, workspaces[0].ID)
	var issue Issue
	requestJSON(t, server.URL, http.MethodPost, "/api/issues", map[string]any{"title": "Dependencies", "project_id": project.ID}, http.StatusCreated, &issue)
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
	project := mustTestProject(t, app, workspaces[0].ID)
	requestJSON(t, server.URL, http.MethodPost, "/api/issues", map[string]any{
		"title": "Run locally", "status": "todo", "project_id": project.ID,
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
	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/ws"
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	requestJSON[any](t, server.URL, http.MethodPost, "/api/daemon/tasks/"+taskID+"/start", map[string]any{}, http.StatusOK, nil)
	readWSEventKinds(t, conn, "issue:updated", "task:running")
	requestJSON[any](t, server.URL, http.MethodPost, "/api/daemon/tasks/"+taskID+"/session", map[string]any{"session_id": "codex-thread-1"}, http.StatusOK, nil)
	var agentComment Comment
	requestJSONWithHeaders(t, server.URL, http.MethodPost, "/api/issues/"+issue.ID+"/comments", map[string]any{
		"content": "Agent-authored result",
	}, map[string]string{
		"X-Agent-ID": LocalAgentID,
		"X-Task-ID":  taskID,
	}, http.StatusCreated, &agentComment)
	if agentComment.AuthorType != "agent" || agentComment.AuthorID != LocalAgentID || agentComment.SourceTaskID == nil || *agentComment.SourceTaskID != taskID {
		t.Fatalf("agent comment attribution = %#v", agentComment)
	}
	requestJSONWithHeaders[any](t, server.URL, http.MethodPost, "/api/issues/"+issue.ID+"/comments", map[string]any{
		"content": "Spoofed agent result",
	}, map[string]string{
		"X-Agent-ID": LocalAgentID,
		"X-Task-ID":  "other-task",
	}, http.StatusForbidden, nil)
	messages := map[string]any{"messages": []any{
		map[string]any{"seq": 2, "type": "tool_result", "tool": "Read", "output": "ok"},
		map[string]any{"seq": 1, "type": "tool_use", "tool": "Read", "input": map[string]any{"file_path": "README.md"}},
	}}
	requestJSON[any](t, server.URL, http.MethodPost, "/api/daemon/tasks/"+taskID+"/messages", messages, http.StatusOK, nil)
	requestJSON[any](t, server.URL, http.MethodPost, "/api/daemon/tasks/"+taskID+"/messages", messages, http.StatusOK, nil)
	requestJSON[any](t, server.URL, http.MethodPost, "/api/daemon/tasks/"+taskID+"/complete", map[string]any{"output": "Implemented and verified", "session_id": "codex-thread-1"}, http.StatusOK, nil)
	readWSEventKinds(t, conn, "issue:updated", "comment:created", "task:completed")
	requestJSON[any](t, server.URL, http.MethodPost, "/api/daemon/tasks/"+taskID+"/complete", map[string]any{"output": "Implemented and verified", "session_id": "codex-thread-1"}, http.StatusOK, nil)
	requestJSONWithHeaders[any](t, server.URL, http.MethodPost, "/api/issues/"+issue.ID+"/comments", map[string]any{
		"content": "Late agent result",
	}, map[string]string{
		"X-Agent-ID": LocalAgentID,
		"X-Task-ID":  taskID,
	}, http.StatusForbidden, nil)

	var runs []map[string]any
	requestJSON(t, server.URL, http.MethodGet, "/api/issues/"+issue.ID+"/task-runs", nil, http.StatusOK, &runs)
	if len(runs) != 1 || runs[0]["status"] != "completed" {
		t.Fatalf("task runs = %#v", runs)
	}
	if runs[0]["handoff_note"] != "Verify the local execution chain" {
		t.Fatalf("task run handoff note = %#v", runs[0]["handoff_note"])
	}
	var rerun map[string]any
	requestJSON(t, server.URL, http.MethodPost, "/api/issues/"+issue.ID+"/rerun", map[string]any{"task_id": taskID}, http.StatusCreated, &rerun)
	if rerun["status"] != "queued" || rerun["issue_id"] != issue.ID {
		t.Fatalf("rerun = %#v", rerun)
	}
	requestJSON(t, server.URL, http.MethodPost, "/api/issues/"+issue.ID+"/tasks/"+rerun["id"].(string)+"/cancel", map[string]any{}, http.StatusOK, &rerun)
	if rerun["status"] != "cancelled" {
		t.Fatalf("cancelled rerun = %#v", rerun)
	}
	var usage []map[string]any
	requestJSON(t, server.URL, http.MethodGet, "/api/runtimes/"+registration.Runtimes[0].ID+"/usage?days=180", nil, http.StatusOK, &usage)
	requestJSON(t, server.URL, http.MethodGet, "/api/runtimes/"+registration.Runtimes[0].ID+"/usage/by-agent?days=30", nil, http.StatusOK, &usage)
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
	if len(comments) != 2 || comments[0].Content != "Agent-authored result" || comments[1].Content != "Implemented and verified" || comments[1].SourceTaskID == nil || *comments[1].SourceTaskID != taskID {
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
		"title": "Assign without starting", "status": "todo", "project_id": project.ID,
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
	project := mustTestProject(t, app, workspaces[0].ID)
	requestJSON(t, server.URL, http.MethodPost, "/api/issues/quick-create", map[string]any{
		"agent_id": LocalAgentID, "prompt": "Inspect the local chain\nand report the result", "priority": "high", "project_id": project.ID,
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
	if _, err := store.db.ExecContext(t.Context(), `DROP INDEX task_runs_request_key_idx`); err != nil {
		t.Fatal(err)
	}
	for _, column := range []string{"handoff_note", "session_id", "stage_id", "work_dir", "prior_task_id", "trigger_comment_id", "request_key", "continuity_state", "continuity_reason"} {
		if _, err := store.db.ExecContext(t.Context(), `ALTER TABLE task_runs DROP COLUMN `+column); err != nil {
			t.Fatal(err)
		}
	}
	workspaces, err := store.ListWorkspaces(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	project, err := store.CreateProject(t.Context(), workspaces[0].ID, "Legacy project", "", "", "planned", "none", nil)
	if err != nil {
		t.Fatal(err)
	}
	legacyIssue, err := store.CreateIssue(t.Context(), workspaces[0].ID, CreateIssueInput{Title: "Legacy issue", ProjectID: &project.ID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(t.Context(), `DELETE FROM issue_session_stages WHERE session_id IN (SELECT id FROM issue_sessions WHERE issue_id=?); DELETE FROM issue_sessions WHERE issue_id=?`, legacyIssue.ID, legacyIssue.ID); err != nil {
		t.Fatal(err)
	}
	stamp := now()
	if _, err := store.db.ExecContext(t.Context(), `INSERT INTO task_runs(id, issue_id, status, created_at, updated_at) VALUES ('legacy-run', ?, 'completed', ?, ?)`, legacyIssue.ID, stamp, stamp); err != nil {
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
	found := map[string]bool{}
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, columnType string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			t.Fatal(err)
		}
		found[name] = true
	}
	for _, name := range []string{"handoff_note", "trigger_comment_id", "request_key"} {
		if !found[name] {
			t.Fatalf("%s column was not restored", name)
		}
	}
	var version int
	if err := reopened.db.QueryRowContext(t.Context(), `SELECT MAX(version) FROM schema_version`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != 9 {
		t.Fatalf("schema version = %d, want 9", version)
	}
	var sessionID, stageID, stageKind string
	var migrated int
	if err := reopened.db.QueryRowContext(t.Context(), `SELECT s.id, s.active_stage_id, st.kind, st.migrated FROM issue_sessions s JOIN issue_session_stages st ON st.id=s.active_stage_id WHERE s.issue_id=?`, legacyIssue.ID).Scan(&sessionID, &stageID, &stageKind, &migrated); err != nil {
		t.Fatal(err)
	}
	if stageKind != "implementation" || migrated != 1 {
		t.Fatalf("backfilled stage kind=%q migrated=%d", stageKind, migrated)
	}
	var runSessionID, runStageID string
	if err := reopened.db.QueryRowContext(t.Context(), `SELECT session_id, stage_id FROM task_runs WHERE id='legacy-run'`).Scan(&runSessionID, &runStageID); err != nil {
		t.Fatal(err)
	}
	if runSessionID != sessionID || runStageID != stageID {
		t.Fatalf("legacy run binding session=%q stage=%q", runSessionID, runStageID)
	}
	var taskMessagesTable string
	if err := reopened.db.QueryRowContext(t.Context(), `SELECT name FROM sqlite_master WHERE type='table' AND name='task_messages'`).Scan(&taskMessagesTable); err != nil {
		t.Fatal(err)
	}
	var attachmentsTable string
	if err := reopened.db.QueryRowContext(t.Context(), `SELECT name FROM sqlite_master WHERE type='table' AND name='attachments'`).Scan(&attachmentsTable); err != nil {
		t.Fatal(err)
	}
}

func readWSEventKinds(t *testing.T, conn *websocket.Conn, kinds ...string) {
	t.Helper()
	wanted := make(map[string]struct{}, len(kinds))
	for _, kind := range kinds {
		wanted[kind] = struct{}{}
	}
	if err := conn.SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}
	for len(wanted) > 0 {
		var event struct {
			Type string `json:"type"`
		}
		if err := conn.ReadJSON(&event); err != nil {
			t.Fatalf("reading websocket events %v: %v", kinds, err)
		}
		delete(wanted, event.Type)
	}
}

func requestJSON[T any](t *testing.T, baseURL, method, path string, body any, wantStatus int, output *T) {
	t.Helper()
	requestJSONWithHeaders(t, baseURL, method, path, body, nil, wantStatus, output)
}

func requestJSONWithHeaders[T any](t *testing.T, baseURL, method, path string, body any, headers map[string]string, wantStatus int, output *T) {
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
	for key, value := range headers {
		request.Header.Set(key, value)
	}
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
