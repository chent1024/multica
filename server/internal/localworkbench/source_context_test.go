package localworkbench

import (
	"context"
	"net/http"
	"path/filepath"
	"testing"
)

func TestLocalSourceContextRejectsStaleCaptureAndPersistsChild(t *testing.T) {
	root := t.TempDir()
	server, err := NewServer(context.Background(), ServerConfig{DatabasePath: filepath.Join(root, "db.sqlite"), FileRoot: filepath.Join(root, "files")})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	handler := server.Router()
	projectResponse := localRequest(t, handler, http.MethodPost, "/api/projects", map[string]any{"title": "Source project"})
	requireStatus(t, projectResponse, http.StatusCreated)
	project := decodeResponse[Project](t, projectResponse)
	parentResponse := localRequest(t, handler, http.MethodPost, "/api/issues", map[string]any{"title": "Parent", "description": "source", "project_id": project.ID})
	requireStatus(t, parentResponse, http.StatusCreated)
	parent := decodeResponse[Issue](t, parentResponse)
	commentResponse := localRequest(t, handler, http.MethodPost, "/api/issues/"+parent.ID+"/comments", map[string]any{"content": "selected context"})
	requireStatus(t, commentResponse, http.StatusCreated)
	comment := decodeResponse[Comment](t, commentResponse)

	previewResponse := localRequest(t, handler, http.MethodGet, "/api/comments/"+comment.ID+"/sub-issue-preview", nil)
	requireStatus(t, previewResponse, http.StatusOK)
	preview := decodeResponse[map[string]any](t, previewResponse)
	token := preview["capture_token"].(string)
	update := localRequest(t, handler, http.MethodPut, "/api/comments/"+comment.ID, map[string]any{"content": "changed"})
	requireStatus(t, update, http.StatusOK)
	stale := localRequest(t, handler, http.MethodPost, "/api/comments/"+comment.ID+"/sub-issues", map[string]any{"mode": "manual", "capture_token": token, "issue": map[string]any{"title": "Child", "stage": 2}})
	requireStatus(t, stale, http.StatusConflict)
	if body := decodeResponse[map[string]any](t, stale); body["code"] != "source_context_changed" {
		t.Fatalf("stale response=%v", body)
	}

	previewResponse = localRequest(t, handler, http.MethodGet, "/api/comments/"+comment.ID+"/sub-issue-preview", nil)
	preview = decodeResponse[map[string]any](t, previewResponse)
	createdResponse := localRequest(t, handler, http.MethodPost, "/api/comments/"+comment.ID+"/sub-issues", map[string]any{"mode": "manual", "capture_token": preview["capture_token"], "issue": map[string]any{"title": "Child", "stage": 2}})
	requireStatus(t, createdResponse, http.StatusCreated)
	child := decodeResponse[Issue](t, createdResponse)
	if child.ParentIssueID == nil || *child.ParentIssueID != parent.ID || child.Stage == nil || *child.Stage != 2 {
		t.Fatalf("child relation=%v stage=%v", child.ParentIssueID, child.Stage)
	}
	var storedToken string
	if err := server.store.db.QueryRow(`SELECT capture_token FROM issue_source_contexts WHERE issue_id=?`, child.ID).Scan(&storedToken); err != nil {
		t.Fatal(err)
	}
	if storedToken != preview["capture_token"] {
		t.Fatalf("stored token=%q preview=%q", storedToken, preview["capture_token"])
	}
	detailResponse := localRequest(t, handler, http.MethodGet, "/api/issues/"+child.ID, nil)
	requireStatus(t, detailResponse, http.StatusOK)
	detail := decodeResponse[map[string]any](t, detailResponse)
	source := detail["source_context"].(map[string]any)
	if source["display_state"] != "unchanged" || source["usage"] != "read_only_historical_background" || source["can_open_current_source"] != true {
		t.Fatalf("source context detail=%v", source)
	}
	changed := localRequest(t, handler, http.MethodPut, "/api/comments/"+comment.ID, map[string]any{"content": "changed after capture"})
	requireStatus(t, changed, http.StatusOK)
	detailResponse = localRequest(t, handler, http.MethodGet, "/api/issues/"+child.ID, nil)
	requireStatus(t, detailResponse, http.StatusOK)
	detail = decodeResponse[map[string]any](t, detailResponse)
	source = detail["source_context"].(map[string]any)
	if source["display_state"] != "changed" || source["comment_thread_state"] != "changed" {
		t.Fatalf("changed source context detail=%v", source)
	}
}
