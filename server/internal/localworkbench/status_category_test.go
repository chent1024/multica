package localworkbench

import (
	"context"
	"net/http"
	"path/filepath"
	"testing"
)

func TestLocalStatusLifecycleCategories(t *testing.T) {
	root := t.TempDir()
	s, err := NewServer(context.Background(), ServerConfig{DatabasePath: filepath.Join(root, "db.sqlite"), FileRoot: filepath.Join(root, "files")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	h := s.Router()
	for _, category := range []string{"unstarted", "started", "done", "closed"} {
		created := localRequest(t, h, http.MethodPost, "/api/issue-statuses", map[string]any{"name": "Custom " + category, "category": category})
		requireStatus(t, created, http.StatusCreated)
		entry := decodeResponse[map[string]any](t, created)
		if entry["category"] != category {
			t.Fatalf("category=%v want %s", entry["category"], category)
		}
	}
	requireStatus(t, localRequest(t, h, http.MethodPost, "/api/issue-statuses", map[string]any{"name": "Invalid", "category": "unknown"}), http.StatusBadRequest)
	listed := localRequest(t, h, http.MethodGet, "/api/issue-statuses", nil)
	requireStatus(t, listed, http.StatusOK)
	catalog := decodeResponse[struct {
		Categories []string `json:"categories"`
		Statuses   []struct {
			ID       string `json:"id"`
			Category string `json:"category"`
		} `json:"statuses"`
	}](t, listed)
	if len(catalog.Categories) != 4 {
		t.Fatalf("categories=%v", catalog.Categories)
	}
	ids := []string{}
	for _, entry := range catalog.Statuses {
		if localStatusCategory(entry.Category) == "started" {
			ids = append([]string{entry.ID}, ids...)
		}
	}
	reordered := localRequest(t, h, http.MethodPatch, "/api/issue-statuses/reorder", map[string]any{"category": "started", "ids": ids, "include_system": true})
	requireStatus(t, reordered, http.StatusOK)
	for position, id := range ids {
		var actual int
		if err := s.store.db.QueryRow(`SELECT position FROM issue_statuses WHERE id=?`, id).Scan(&actual); err != nil {
			t.Fatal(err)
		}
		if actual != position {
			t.Fatalf("position=%d want=%d", actual, position)
		}
	}
	invalid := localRequest(t, h, http.MethodPatch, "/api/issue-statuses/reorder", map[string]any{"category": "started", "ids": append(ids, ids[0]), "include_system": true})
	requireStatus(t, invalid, http.StatusConflict)
	projectResponse := localRequest(t, h, http.MethodPost, "/api/projects", map[string]any{"title": "Category test"})
	requireStatus(t, projectResponse, http.StatusCreated)
	project := decodeResponse[Project](t, projectResponse)
	for _, status := range []string{"todo", "in_review", "cancelled"} {
		requireStatus(t, localRequest(t, h, http.MethodPost, "/api/issues", map[string]any{"title": status, "status": status, "project_id": project.ID}), http.StatusCreated)
	}
	for category, want := range map[string]string{"unstarted": "todo", "started": "in_review", "closed": "cancelled"} {
		response := localRequest(t, h, http.MethodGet, "/api/issues?status_category="+category, nil)
		requireStatus(t, response, http.StatusOK)
		result := decodeResponse[struct {
			Issues []Issue `json:"issues"`
		}](t, response)
		if len(result.Issues) != 1 || result.Issues[0].Status != want {
			t.Fatalf("%s issues=%v", category, result.Issues)
		}
	}
	if localIssueCategoryRunnable("backlog") || localIssueCategoryRunnable("closed") || !localIssueCategoryRunnable("todo") {
		t.Fatal("lifecycle projection changed status scheduling")
	}
	var version int
	if err := s.store.db.QueryRow(`SELECT MAX(version) FROM schema_migrations`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != schemaMigrations[len(schemaMigrations)-1].version {
		t.Fatalf("schema version changed: %d", version)
	}
}
