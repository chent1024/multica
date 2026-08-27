package localworkbench

import (
	"context"
	"net/http"
	"path/filepath"
	"testing"
)

func TestLocalNotificationIsDurableIdempotentAndFilterable(t *testing.T) {
	root := t.TempDir()
	server, err := NewServer(context.Background(), ServerConfig{DatabasePath: filepath.Join(root, "db.sqlite"), FileRoot: filepath.Join(root, "files")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close() })

	insert := func(input localNotificationInput) bool {
		t.Helper()
		tx, err := server.store.db.BeginTx(t.Context(), nil)
		if err != nil {
			t.Fatal(err)
		}
		_, inserted, err := insertLocalNotificationTx(t.Context(), tx, input)
		if err != nil {
			_ = tx.Rollback()
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
		return inserted
	}

	review := localNotificationInput{Type: "review_requested", Severity: "info", SourceKey: "task:one:review", ActorType: "agent", ActorID: LocalAgentID, Title: "Ready", Details: map[string]string{"task_id": "one"}}
	if !insert(review) || insert(review) {
		t.Fatal("same source outcome must insert exactly once")
	}
	if !insert(localNotificationInput{Type: "runtime_failed", Severity: "action_required", SourceKey: "runtime-request:two:failed", Title: "Runtime failed", Body: "credential missing", Details: map[string]string{"destination": "runtime", "runtime_id": "runtime-1"}}) {
		t.Fatal("distinct runtime failure was not inserted")
	}

	handler := server.Router()
	all := localRequest(t, handler, http.MethodGet, "/api/inbox", nil)
	requireStatus(t, all, http.StatusOK)
	items := decodeResponse[[]map[string]any](t, all)
	if len(items) != 2 || items[1]["actor_type"] != "agent" || items[1]["details"].(map[string]any)["task_id"] != "one" {
		t.Fatalf("all notifications=%v", items)
	}

	preference := localRequest(t, handler, http.MethodPatch, "/api/notification-preferences", map[string]any{"preferences": map[string]any{"local_delivery": "action_required"}})
	requireStatus(t, preference, http.StatusOK)
	filtered := localRequest(t, handler, http.MethodGet, "/api/inbox", nil)
	requireStatus(t, filtered, http.StatusOK)
	filteredItems := decodeResponse[[]map[string]any](t, filtered)
	if len(filteredItems) != 1 || filteredItems[0]["type"] != "runtime_failed" {
		t.Fatalf("filtered notifications=%v", filteredItems)
	}
	unread := localRequest(t, handler, http.MethodGet, "/api/inbox/unread-count", nil)
	requireStatus(t, unread, http.StatusOK)
	if decodeResponse[map[string]any](t, unread)["count"] != float64(1) {
		t.Fatalf("filtered unread=%s", unread.Body.String())
	}
}

func TestFailedRuntimeRequestCreatesOneActionableNotification(t *testing.T) {
	root := t.TempDir()
	server, err := NewServer(context.Background(), ServerConfig{DatabasePath: filepath.Join(root, "db.sqlite"), FileRoot: filepath.Join(root, "files")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close() })
	handler := server.Router()
	register := localRequest(t, handler, http.MethodPost, "/api/daemon/register", map[string]any{
		"workspace_id": LocalWorkspaceID, "daemon_id": "notification-daemon",
		"runtimes": []map[string]any{{"name": "Codex", "type": "codex", "status": "online", "profile_id": "default"}},
	})
	requireStatus(t, register, http.StatusOK)
	runtimeID := decodeResponse[map[string]any](t, register)["runtimes"].([]any)[0].(map[string]any)["id"].(string)
	created := localRequest(t, handler, http.MethodPost, "/api/runtimes/"+runtimeID+"/models", map[string]any{})
	requireStatus(t, created, http.StatusOK)
	requestID := decodeResponse[map[string]any](t, created)["id"].(string)
	path := "/api/daemon/runtimes/" + runtimeID + "/models/" + requestID + "/result"
	failed := localRequest(t, handler, http.MethodPost, path, map[string]any{"status": "failed", "error": "credential missing"})
	requireStatus(t, failed, http.StatusOK)
	replayed := localRequest(t, handler, http.MethodPost, path, map[string]any{"status": "failed", "error": "credential missing"})
	requireStatus(t, replayed, http.StatusOK)
	var count int
	if err := server.store.db.QueryRow(`SELECT COUNT(*) FROM inbox_items WHERE type='runtime_failed' AND source_key=? AND severity='action_required' AND body='credential missing'`, "runtime-request:"+requestID+":failed").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("runtime failure notifications=%d want=1", count)
	}
}

func TestQuickCreateTerminalOutcomeUsesLocalNotificationContract(t *testing.T) {
	root := t.TempDir()
	server, err := NewServer(context.Background(), ServerConfig{DatabasePath: filepath.Join(root, "db.sqlite"), FileRoot: filepath.Join(root, "files")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close() })
	handler := server.Router()
	projectResponse := localRequest(t, handler, http.MethodPost, "/api/projects", map[string]any{"title": "Notification project"})
	requireStatus(t, projectResponse, http.StatusCreated)
	project := decodeResponse[Project](t, projectResponse)
	register := localRequest(t, handler, http.MethodPost, "/api/daemon/register", map[string]any{
		"workspace_id": LocalWorkspaceID, "daemon_id": "quick-create-daemon",
		"runtimes": []map[string]any{{"name": "Codex", "type": "codex", "status": "online", "profile_id": "default"}},
	})
	requireStatus(t, register, http.StatusOK)
	created := localRequest(t, handler, http.MethodPost, "/api/issues/quick-create", map[string]any{"agent_id": LocalAgentID, "prompt": "Inspect the local build", "project_id": project.ID})
	requireStatus(t, created, http.StatusCreated)
	taskID := decodeResponse[map[string]any](t, created)["task_id"].(string)
	completePath := "/api/daemon/tasks/" + taskID + "/complete"
	completed := localRequest(t, handler, http.MethodPost, completePath, map[string]any{"output": "Build is ready"})
	requireStatus(t, completed, http.StatusOK)
	replayed := localRequest(t, handler, http.MethodPost, completePath, map[string]any{"output": "Build is ready"})
	requireStatus(t, replayed, http.StatusOK)
	var count int
	var details string
	if err := server.store.db.QueryRow(`SELECT COUNT(*),MAX(details) FROM inbox_items WHERE type='quick_create_done' AND source_key=?`, "task:"+taskID+":quick_create_done").Scan(&count, &details); err != nil {
		t.Fatal(err)
	}
	if count != 1 || details == "" {
		t.Fatalf("quick-create notifications=%d details=%q", count, details)
	}
}
