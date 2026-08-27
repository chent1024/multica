package localworkbench

import (
	"context"
	"fmt"
	"net/http"
	"path/filepath"
	"testing"
)

func TestLocalCommentMentionsPreviewAndQueueExactTargets(t *testing.T) {
	root := t.TempDir()
	server, err := NewServer(context.Background(), ServerConfig{DatabasePath: filepath.Join(root, "multica.db"), FileRoot: filepath.Join(root, "files")})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	handler := server.Router()

	projectResponse := localRequest(t, handler, http.MethodPost, "/api/projects", map[string]any{"title": "Mentions"})
	requireStatus(t, projectResponse, http.StatusCreated)
	project := decodeResponse[Project](t, projectResponse)
	register := localRequest(t, handler, http.MethodPost, "/api/daemon/register", map[string]any{
		"workspace_id": LocalWorkspaceID, "daemon_id": "mentions-daemon",
		"runtimes": []map[string]any{{"name": "Codex", "type": "codex", "status": "online", "profile_id": "default"}},
	})
	requireStatus(t, register, http.StatusOK)
	runtimeID := decodeResponse[map[string]any](t, register)["runtimes"].([]any)[0].(map[string]any)["id"].(string)
	agentResponse := localRequest(t, handler, http.MethodPost, "/api/agents", map[string]any{"name": "Reviewer", "runtime_id": runtimeID})
	requireStatus(t, agentResponse, http.StatusCreated)
	agent := decodeResponse[map[string]any](t, agentResponse)
	squadResponse := localRequest(t, handler, http.MethodPost, "/api/squads", map[string]any{"name": "Review squad", "leader_id": agent["id"]})
	requireStatus(t, squadResponse, http.StatusCreated)
	squad := decodeResponse[map[string]any](t, squadResponse)
	issueResponse := localRequest(t, handler, http.MethodPost, "/api/issues", map[string]any{"title": "Mention targets", "project_id": project.ID})
	requireStatus(t, issueResponse, http.StatusCreated)
	issue := decodeResponse[Issue](t, issueResponse)
	content := fmt.Sprintf("[@Codex](mention://agent/%s) [@Review](mention://squad/%s) please inspect", LocalAgentID, squad["id"])

	preview := localRequest(t, handler, http.MethodPost, "/api/issues/"+issue.ID+"/comments/trigger-preview", map[string]any{"content": content})
	requireStatus(t, preview, http.StatusOK)
	previewBody := decodeResponse[map[string]any](t, preview)
	if got := len(previewBody["agents"].([]any)); got != 2 {
		t.Fatalf("preview agents=%d body=%v", got, previewBody)
	}

	created := localRequest(t, handler, http.MethodPost, "/api/issues/"+issue.ID+"/comments", map[string]any{"content": content})
	requireStatus(t, created, http.StatusCreated)
	comment := decodeResponse[map[string]any](t, created)
	if got := len(comment["trigger_outcomes"].([]any)); got != 2 {
		t.Fatalf("outcomes=%d body=%v", got, comment)
	}
	var runs, targets int
	if err := server.store.db.QueryRow(`SELECT COUNT(*),COUNT(DISTINCT agent_id) FROM task_runs WHERE issue_id=? AND trigger_comment_id=? AND status='queued'`, issue.ID, comment["id"]).Scan(&runs, &targets); err != nil {
		t.Fatal(err)
	}
	if runs != 2 || targets != 2 {
		t.Fatalf("runs=%d targets=%d", runs, targets)
	}
	var reviewerTaskID string
	for range 2 {
		claim := localRequest(t, handler, http.MethodPost, "/api/daemon/tasks/claim", map[string]any{"daemon_id": "mentions-daemon", "runtime_ids": []string{runtimeID}, "max_tasks": 1})
		requireStatus(t, claim, http.StatusOK)
		for _, raw := range decodeResponse[map[string]any](t, claim)["tasks"].([]any) {
			task := raw.(map[string]any)
			if task["agent_id"] == agent["id"] {
				reviewerTaskID = task["id"].(string)
			}
		}
	}
	if reviewerTaskID == "" {
		t.Fatal("reviewer mention task was not claimed for the exact agent")
	}
	completed := localRequest(t, handler, http.MethodPost, "/api/daemon/tasks/"+reviewerTaskID+"/complete", map[string]any{"output": "Reviewed by the mentioned agent"})
	requireStatus(t, completed, http.StatusOK)
	var authorID string
	if err := server.store.db.QueryRow(`SELECT author_id FROM comments WHERE source_task_id=?`, reviewerTaskID).Scan(&authorID); err != nil {
		t.Fatal(err)
	}
	if authorID != agent["id"] {
		t.Fatalf("mentioned task result author=%s, want %s", authorID, agent["id"])
	}
}

func TestLocalIssueTriggerPreviewMatchesRunnableAssignment(t *testing.T) {
	root := t.TempDir()
	server, err := NewServer(context.Background(), ServerConfig{DatabasePath: filepath.Join(root, "multica.db"), FileRoot: filepath.Join(root, "files")})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	handler := server.Router()
	register := localRequest(t, handler, http.MethodPost, "/api/daemon/register", map[string]any{
		"workspace_id": LocalWorkspaceID, "daemon_id": "preview-daemon",
		"runtimes": []map[string]any{{"name": "Codex", "type": "codex", "status": "online", "profile_id": "default"}},
	})
	requireStatus(t, register, http.StatusOK)
	preview := localRequest(t, handler, http.MethodPost, "/api/issues/preview-trigger", map[string]any{"is_create": true, "assignee_type": "agent", "assignee_id": LocalAgentID, "status": "todo"})
	requireStatus(t, preview, http.StatusOK)
	body := decodeResponse[map[string]any](t, preview)
	if body["total_count"] != float64(1) {
		t.Fatalf("preview=%v", body)
	}
	backlog := localRequest(t, handler, http.MethodPost, "/api/issues/preview-trigger", map[string]any{"is_create": true, "assignee_type": "agent", "assignee_id": LocalAgentID, "status": "backlog"})
	requireStatus(t, backlog, http.StatusOK)
	if decodeResponse[map[string]any](t, backlog)["total_count"] != float64(0) {
		t.Fatalf("backlog should park")
	}
	projectResponse := localRequest(t, handler, http.MethodPost, "/api/projects", map[string]any{"title": "Trigger state"})
	requireStatus(t, projectResponse, http.StatusCreated)
	project := decodeResponse[Project](t, projectResponse)
	issueResponse := localRequest(t, handler, http.MethodPost, "/api/issues", map[string]any{"title": "Parked assignment", "project_id": project.ID, "status": "backlog", "assignee_type": "agent", "assignee_id": LocalAgentID})
	requireStatus(t, issueResponse, http.StatusCreated)
	issue := decodeResponse[Issue](t, issueResponse)
	var runs int
	if err := server.store.db.QueryRow(`SELECT COUNT(*) FROM task_runs WHERE issue_id=?`, issue.ID).Scan(&runs); err != nil {
		t.Fatal(err)
	}
	if runs != 0 {
		t.Fatalf("assigned backlog queued %d runs, want 0", runs)
	}
	sameAssignment := localRequest(t, handler, http.MethodPost, "/api/issues/preview-trigger", map[string]any{"issue_ids": []string{issue.ID}, "assignee_type": "agent", "assignee_id": LocalAgentID})
	requireStatus(t, sameAssignment, http.StatusOK)
	if decodeResponse[map[string]any](t, sameAssignment)["total_count"] != float64(0) {
		t.Fatal("unchanged assignee previewed a new run")
	}
	unchanged := localRequest(t, handler, http.MethodPut, "/api/issues/"+issue.ID, map[string]any{"assignee_type": "agent", "assignee_id": LocalAgentID})
	requireStatus(t, unchanged, http.StatusOK)
	if err := server.store.db.QueryRow(`SELECT COUNT(*) FROM task_runs WHERE issue_id=?`, issue.ID).Scan(&runs); err != nil {
		t.Fatal(err)
	}
	if runs != 0 {
		t.Fatalf("unchanged assignee queued %d runs, want 0", runs)
	}
	activated := localRequest(t, handler, http.MethodPut, "/api/issues/"+issue.ID, map[string]any{"status": "todo"})
	requireStatus(t, activated, http.StatusOK)
	if err := server.store.db.QueryRow(`SELECT COUNT(*) FROM task_runs WHERE issue_id=? AND status='queued'`, issue.ID).Scan(&runs); err != nil {
		t.Fatal(err)
	}
	if runs != 1 {
		t.Fatalf("backlog activation queued %d runs, want 1", runs)
	}
}

func TestLocalCommentMentionWhileAgentActivePersistsDeferredFollowUp(t *testing.T) {
	root := t.TempDir()
	server, err := NewServer(context.Background(), ServerConfig{DatabasePath: filepath.Join(root, "multica.db"), FileRoot: filepath.Join(root, "files")})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	handler := server.Router()
	register := localRequest(t, handler, http.MethodPost, "/api/daemon/register", map[string]any{
		"workspace_id": LocalWorkspaceID, "daemon_id": "trigger-follow-up-daemon",
		"runtimes": []map[string]any{{"name": "Codex", "type": "codex", "status": "online", "profile_id": "default"}},
	})
	requireStatus(t, register, http.StatusOK)
	projectResponse := localRequest(t, handler, http.MethodPost, "/api/projects", map[string]any{"title": "Mention follow-up"})
	requireStatus(t, projectResponse, http.StatusCreated)
	project := decodeResponse[Project](t, projectResponse)
	issueResponse := localRequest(t, handler, http.MethodPost, "/api/issues", map[string]any{"title": "Persist every mention", "project_id": project.ID})
	requireStatus(t, issueResponse, http.StatusCreated)
	issue := decodeResponse[Issue](t, issueResponse)
	content := fmt.Sprintf("[@Codex](mention://agent/%s)", LocalAgentID)

	first := localRequest(t, handler, http.MethodPost, "/api/issues/"+issue.ID+"/comments", map[string]any{"content": content})
	requireStatus(t, first, http.StatusCreated)
	second := localRequest(t, handler, http.MethodPost, "/api/issues/"+issue.ID+"/comments", map[string]any{"content": content})
	requireStatus(t, second, http.StatusCreated)
	response := decodeResponse[map[string]any](t, second)
	outcomes := response["trigger_outcomes"].([]any)
	if got := outcomes[0].(map[string]any)["status"]; got != "deferred" {
		t.Fatalf("second mention status = %v, want deferred", got)
	}
	var queued, waiting int
	if err := server.store.db.QueryRow(`SELECT COUNT(*) FILTER (WHERE status='queued'), COUNT(*) FILTER (WHERE status='waiting_previous_run') FROM task_runs WHERE issue_id=? AND agent_id=?`, issue.ID, LocalAgentID).Scan(&queued, &waiting); err != nil {
		t.Fatal(err)
	}
	if queued != 1 || waiting != 1 {
		t.Fatalf("run states = queued:%d waiting:%d, want 1/1", queued, waiting)
	}
}
