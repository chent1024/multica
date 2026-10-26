package localworkbench

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func localWorkflowFixture(t *testing.T) *Server {
	t.Helper()
	root := t.TempDir()
	s, err := NewServer(context.Background(), ServerConfig{DatabasePath: filepath.Join(root, "db.sqlite"), FileRoot: filepath.Join(root, "files")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}
func localWorkflowCreateFixture(t *testing.T, s *Server, name string, steps []map[string]any) localWorkflowRecord {
	t.Helper()
	r := localRequest(t, s.Router(), http.MethodPost, "/api/issue-workflows", map[string]any{"name": name, "steps": steps})
	requireStatus(t, r, http.StatusCreated)
	return decodeResponse[localWorkflowRecord](t, r)
}
func TestLocalIssueWorkflowCRUDMappingCopyAndPreservation(t *testing.T) {
	s := localWorkflowFixture(t)
	h := s.Router()
	r := localRequest(t, h, http.MethodPost, "/api/issue-statuses", map[string]any{"name": "Existing custom", "category": "started"})
	requireStatus(t, r, 201)
	existingKey := decodeResponse[map[string]any](t, r)["key"].(string)
	r = localRequest(t, h, http.MethodGet, "/api/issue-workflows", nil)
	requireStatus(t, r, 200)
	initial := decodeResponse[struct {
		Workflows []localWorkflowRecord `json:"workflows"`
	}](t, r)
	if len(initial.Workflows) != 1 || !initial.Workflows[0].IsDefault {
		t.Fatalf("default=%s", r.Body.String())
	}
	defaultWorkflow := initial.Workflows[0]
	if !defaultWorkflow.definition().Has(existingKey) {
		t.Fatal("Default lost an existing custom status")
	}
	named := localWorkflowCreateFixture(t, s, "Build pipeline", []map[string]any{{"new_status": map[string]any{"name": "Build", "category": "started", "color": "#aabbcc"}}})
	var buildKey string
	for _, step := range named.Steps {
		if strings.HasPrefix(step.StatusKey, "custom_") {
			buildKey = step.StatusKey
		}
	}
	if buildKey == "" {
		t.Fatal("new status was not created")
	}
	r = localRequest(t, h, http.MethodGet, "/api/issue-workflows/"+defaultWorkflow.ID, nil)
	requireStatus(t, r, 200)
	if decodeResponse[localWorkflowRecord](t, r).definition().Has(buildKey) {
		t.Fatal("a named workflow's new status entered Default")
	}
	r = localRequest(t, h, http.MethodPost, "/api/projects", map[string]any{"title": "Workflow project", "workflow_id": named.ID})
	requireStatus(t, r, 201)
	project := decodeResponse[Project](t, r)
	if project.WorkflowID == nil || *project.WorkflowID != named.ID {
		t.Fatalf("workflow binding=%v", project.WorkflowID)
	}
	r = localRequest(t, h, http.MethodPost, "/api/issues", map[string]any{"title": "Keep this issue", "project_id": project.ID, "status": buildKey})
	requireStatus(t, r, 201)
	issue := decodeResponse[Issue](t, r)
	proposed := []map[string]any{{"new_status": map[string]any{"name": "Transient", "category": "started"}}}
	r = localRequest(t, h, http.MethodPatch, "/api/issue-workflows/"+named.ID, map[string]any{"steps": proposed, "dry_run": true})
	requireStatus(t, r, 200)
	preview := decodeResponse[struct {
		Plan localWorkflowMappingPlan `json:"plan"`
	}](t, r)
	if preview.Plan.AffectedIssues != 1 || len(preview.Plan.Required) != 1 || preview.Plan.Required[0].StatusKey != buildKey {
		t.Fatalf("plan=%s", r.Body.String())
	}
	r = localRequest(t, h, http.MethodPatch, "/api/issue-workflows/"+named.ID, map[string]any{"steps": proposed})
	requireStatus(t, r, 409)
	if decodeResponse[map[string]any](t, r)["code"] != "workflow_status_mapping_required" {
		t.Fatalf("mapping rejection=%s", r.Body.String())
	}
	var transient int
	if err := s.store.db.QueryRow(`SELECT COUNT(*) FROM issue_statuses WHERE name='Transient'`).Scan(&transient); err != nil {
		t.Fatal(err)
	}
	if transient != 0 {
		t.Fatal("dry run/rejected edit persisted a status")
	}
	r = localRequest(t, h, http.MethodPatch, "/api/issue-workflows/"+named.ID, map[string]any{"steps": []any{}, "status_mapping": map[string]string{buildKey: "todo"}})
	requireStatus(t, r, 200)
	r = localRequest(t, h, http.MethodGet, "/api/issues/"+issue.ID, nil)
	requireStatus(t, r, 200)
	mapped := decodeResponse[Issue](t, r)
	if mapped.ID != issue.ID || mapped.Status != "todo" || mapped.Title != "Keep this issue" {
		t.Fatalf("mapping lost issue content: %s", r.Body.String())
	}
	requireStatus(t, localRequest(t, h, http.MethodDelete, "/api/issue-workflows/"+named.ID, nil), 409)
	requireStatus(t, localRequest(t, h, http.MethodDelete, "/api/issue-workflows/"+defaultWorkflow.ID, nil), 400)
	r = localRequest(t, h, http.MethodPut, "/api/issue-workflows/"+named.ID+"/handoffs/todo", map[string]any{"handler": map[string]string{"type": "member", "id": LocalOperatorID}, "instructions": "Review", "copy_for_project_id": project.ID})
	requireStatus(t, r, 201)
	copied := decodeResponse[localWorkflowRecord](t, r)
	if copied.ID == named.ID {
		t.Fatal("project copy overwrote the shared workflow")
	}
	r = localRequest(t, h, http.MethodGet, "/api/projects/"+project.ID, nil)
	requireStatus(t, r, 200)
	updated := decodeResponse[Project](t, r)
	if updated.WorkflowID == nil || *updated.WorkflowID != copied.ID {
		t.Fatal("copy did not atomically bind the project")
	}
	r = localRequest(t, h, http.MethodGet, "/api/issue-workflows/"+named.ID, nil)
	requireStatus(t, r, 200)
	original := decodeResponse[localWorkflowRecord](t, r)
	step, _ := original.definition().Step("todo")
	if step.Handler.Type != "none" {
		t.Fatal("copy changed the source handoff")
	}
	requireStatus(t, localRequest(t, h, http.MethodDelete, "/api/issue-workflows/"+named.ID, nil), 204)
	r = localRequest(t, h, http.MethodPost, "/api/projects/"+project.ID+"/workflow", map[string]any{"workflow_id": nil})
	requireStatus(t, r, 200)
	r = localRequest(t, h, http.MethodGet, "/api/projects/"+project.ID, nil)
	requireStatus(t, r, 200)
	if decodeResponse[Project](t, r).WorkflowID != nil {
		t.Fatal("switching Default retained a named binding")
	}
	requireStatus(t, localRequest(t, h, http.MethodDelete, "/api/issue-workflows/"+copied.ID, nil), 204)
	var issues int
	if err := s.store.db.QueryRow(`SELECT COUNT(*) FROM issues`).Scan(&issues); err != nil {
		t.Fatal(err)
	}
	if issues != 1 {
		t.Fatalf("workflow operations changed issue count=%d", issues)
	}
}

func TestLocalIssueWorkflowHandoffPreviewAndEntry(t *testing.T) {
	s := localWorkflowFixture(t)
	h := s.Router()
	requireStatus(t, localRequest(t, h, http.MethodPost, "/api/daemon/register", map[string]any{"workspace_id": LocalWorkspaceID, "daemon_id": "workflow-fixture", "cli_version": "fixture", "runtimes": []map[string]any{{"name": "Fake Codex", "type": "codex", "status": "online"}}}), 200)
	named := localWorkflowCreateFixture(t, s, "Agent workflow", []map[string]any{{"status_key": "in_progress", "handler": map[string]string{"type": "agent", "id": LocalAgentID}, "instructions": "Build without publishing", "next_status_key": "in_review"}, {"status_key": "in_review", "handler": map[string]string{"type": "member", "id": LocalOperatorID}}})
	r := localRequest(t, h, http.MethodPost, "/api/projects", map[string]any{"title": "Execution fixture", "workflow_id": named.ID})
	requireStatus(t, r, 201)
	project := decodeResponse[Project](t, r)
	r = localRequest(t, h, http.MethodPost, "/api/issues", map[string]any{"title": "Handoff fixture", "project_id": project.ID, "status": "todo"})
	requireStatus(t, r, 201)
	issue := decodeResponse[Issue](t, r)
	r = localRequest(t, h, http.MethodGet, "/api/issues/"+issue.ID+"/workflow-handoff?status=in_progress", nil)
	requireStatus(t, r, 200)
	preview := decodeResponse[map[string]any](t, r)
	if preview["handoff"] != true || preview["handler_id"] != LocalAgentID || !strings.Contains(preview["brief"].(string), "Build without publishing") {
		t.Fatalf("handoff preview=%s", r.Body.String())
	}
	requireStatus(t, localRequest(t, h, http.MethodGet, "/api/issues/"+issue.ID+"/workflow-handoff?status=blocked", nil), 400)
	r = localRequest(t, h, http.MethodPost, "/api/projects", map[string]any{"title": "Other execution root"})
	requireStatus(t, r, 201)
	other := decodeResponse[Project](t, r)
	requireStatus(t, localRequest(t, h, http.MethodGet, "/api/issues/"+issue.ID+"/workflow-handoff?project_id="+other.ID, nil), 409)
	tx, err := s.store.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	updates := map[string]any{"status": "in_progress"}
	entry, err := applyLocalIssueWorkflowEntryTx(t.Context(), tx, LocalWorkspaceID, issue.ID, updates)
	if err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if entry == nil || entry.AgentID != LocalAgentID || entry.WorkflowID != named.ID || entry.StatusKey != "in_progress" || !strings.Contains(entry.Brief, "Build without publishing") || updates["assignee_id"] != LocalAgentID {
		_ = tx.Rollback()
		t.Fatalf("entry=%+v updates=%v", entry, updates)
	}
	var current string
	if err := tx.QueryRow(`SELECT status FROM issues WHERE id=?`, issue.ID).Scan(&current); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if current != "todo" {
		_ = tx.Rollback()
		t.Fatal("entry helper changed the issue before its owner")
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	r = localRequest(t, h, http.MethodPost, "/api/issue-workflows/"+named.ID+"/brief", map[string]any{"status_key": "in_progress", "handler": map[string]string{"type": "agent", "id": LocalAgentID}, "instructions": "Draft-only instructions", "project_id": project.ID})
	requireStatus(t, r, 200)
	if !strings.Contains(decodeResponse[map[string]string](t, r)["brief"], "Draft-only instructions") {
		t.Fatal("brief preview omitted the unsaved draft")
	}
	r = localRequest(t, h, http.MethodGet, "/api/issue-workflows/"+named.ID, nil)
	requireStatus(t, r, 200)
	step, _ := decodeResponse[localWorkflowRecord](t, r).definition().Step("in_progress")
	if step.Instructions != "Build without publishing" {
		t.Fatal("brief preview persisted a draft")
	}
}

func TestLocalIssueWorkflowQueuesRealRunsAndProtectsOldSteps(t *testing.T) {
	s := localWorkflowFixture(t)
	h := s.Router()
	requireStatus(t, localRequest(t, h, http.MethodPost, "/api/daemon/register", map[string]any{"workspace_id": LocalWorkspaceID, "daemon_id": "workflow-queue-fixture", "runtimes": []map[string]any{{"name": "Fake Codex", "type": "codex", "status": "online"}}}), 200)
	named := localWorkflowCreateFixture(t, s, "Custom execution", []map[string]any{{"new_status": map[string]any{"name": "Implement custom", "category": "started"}, "handler": map[string]string{"type": "agent", "id": LocalAgentID}, "instructions": "Implement this custom step", "next_status_key": "done"}})
	var key string
	for _, step := range named.Steps {
		if strings.HasPrefix(step.StatusKey, "custom_") {
			key = step.StatusKey
		}
	}
	if key == "" {
		t.Fatal("custom execution column missing")
	}
	r := localRequest(t, h, http.MethodPost, "/api/projects", map[string]any{"title": "Native handoff fixture", "workflow_id": named.ID})
	requireStatus(t, r, 201)
	project := decodeResponse[Project](t, r)
	r = localRequest(t, h, http.MethodPost, "/api/issues", map[string]any{"title": "Execute workflow step", "project_id": project.ID, "status": key})
	requireStatus(t, r, 201)
	issue := decodeResponse[Issue](t, r)
	if issue.AssigneeID == nil || *issue.AssigneeID != LocalAgentID {
		t.Fatalf("create did not apply handler: %s", r.Body.String())
	}
	var taskID, workflowID, workflowStep, note string
	var count int
	if err := s.store.db.QueryRow(`SELECT COUNT(*) FROM task_runs WHERE issue_id=?`, issue.ID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("initial handoff queued %d runs", count)
	}
	if err := s.store.db.QueryRow(`SELECT id,workflow_id,workflow_step,handoff_note FROM task_runs WHERE issue_id=?`, issue.ID).Scan(&taskID, &workflowID, &workflowStep, &note); err != nil {
		t.Fatal(err)
	}
	if workflowID != named.ID || workflowStep != key || !strings.Contains(note, "Implement this custom step") {
		t.Fatalf("queued handoff provenance=%s %s %q", workflowID, workflowStep, note)
	}
	requireStatus(t, localRequest(t, h, http.MethodPost, "/api/daemon/tasks/"+taskID+"/start", map[string]any{}), 200)
	requireStatus(t, localRequest(t, h, http.MethodPost, "/api/daemon/tasks/"+taskID+"/complete", map[string]any{"output": "fixture completed"}), 200)
	r = localRequest(t, h, http.MethodGet, "/api/issues/"+issue.ID, nil)
	requireStatus(t, r, 200)
	current := decodeResponse[Issue](t, r)
	if current.Status != key {
		t.Fatalf("daemon lifecycle overwrote the custom step: %s", r.Body.String())
	}
	requireStatus(t, localRequest(t, h, http.MethodPut, "/api/issues/"+issue.ID, map[string]any{"status": key}), 200)
	if err := s.store.db.QueryRow(`SELECT COUNT(*) FROM task_runs WHERE issue_id=?`, issue.ID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("reselecting a step duplicated its run: %d", count)
	}
	requireStatus(t, localRequest(t, h, http.MethodPut, "/api/issues/"+issue.ID, map[string]any{"status": "done", "suppress_run": true}), 200)
	if _, err := s.store.db.Exec(`UPDATE task_runs SET status='running' WHERE id=?`, taskID); err != nil {
		t.Fatal(err)
	}
	body := strings.NewReader(`{"status":"todo"}`)
	request := httptest.NewRequest(http.MethodPut, "/api/issues/"+issue.ID, body)
	request.RemoteAddr = "127.0.0.1:40000"
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Agent-ID", LocalAgentID)
	request.Header.Set("X-Task-ID", taskID)
	response := httptest.NewRecorder()
	h.ServeHTTP(response, request)
	requireStatus(t, response, 409)
	if decodeResponse[map[string]any](t, response)["code"] != "workflow_step_moved" {
		t.Fatalf("stale step rejection=%s", response.Body.String())
	}
	r = localRequest(t, h, http.MethodPut, "/api/issues/"+issue.ID, map[string]any{"status": "todo", "expected_revision": issue.Revision})
	requireStatus(t, r, 409)
}

func TestLocalWorkflowStatusArchiveProtectsNamedUse(t *testing.T) {
	s := localWorkflowFixture(t)
	h := s.Router()
	made := localRequest(t, h, http.MethodPost, "/api/issue-statuses", map[string]any{"name": "Archive protected", "category": "started", "color": "#123456"})
	requireStatus(t, made, http.StatusCreated)
	status := decodeResponse[map[string]any](t, made)
	key, id := status["key"].(string), status["id"].(string)
	requireStatus(t, localRequest(t, h, http.MethodGet, "/api/issue-workflows", nil), http.StatusOK)
	flow := localWorkflowCreateFixture(t, s, "Uses protected status", []map[string]any{{"status_key": key}})
	refused := localRequest(t, h, http.MethodDelete, "/api/issue-statuses/"+id, nil)
	requireStatus(t, refused, http.StatusConflict)
	if code := decodeResponse[map[string]any](t, refused)["code"]; code != "issue_status_in_workflow" {
		t.Fatalf("code=%v", code)
	}
	requireStatus(t, localRequest(t, h, http.MethodDelete, "/api/issue-workflows/"+flow.ID, nil), http.StatusNoContent)
	requireStatus(t, localRequest(t, h, http.MethodDelete, "/api/issue-statuses/"+id, nil), http.StatusOK)
	var raw string
	if err := s.store.db.QueryRow(`SELECT steps FROM issue_workflows WHERE is_default=1`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(raw, key) {
		t.Fatal("archived status remained in Default workflow")
	}
}

func TestLocalWorkflowStaleQueueDoesNotPoisonOtherClaims(t *testing.T) {
	s, issue := localWakeupAPIFixture(t)
	h := s.Router()
	flow := localWorkflowCreateFixture(t, s, "Queue fence", []map[string]any{{"new_status": map[string]any{"name": "Queue step", "category": "started"}, "handler": map[string]any{"type": "agent", "id": LocalAgentID}, "instructions": "Execute this workflow step"}})
	var key string
	for _, step := range flow.Steps {
		if step.StatusKey != "todo" && step.StatusKey != "done" && step.StatusKey != "cancelled" {
			key = step.StatusKey
		}
	}
	var project string
	if err := s.store.db.QueryRow(`SELECT project_id FROM issues WHERE id=?`, issue).Scan(&project); err != nil {
		t.Fatal(err)
	}
	requireStatus(t, localRequest(t, h, http.MethodPost, "/api/projects/"+project+"/workflow", map[string]any{"workflow_id": flow.ID}), http.StatusOK)
	requireStatus(t, localRequest(t, h, http.MethodPut, "/api/issues/"+issue, map[string]any{"status": key}), http.StatusOK)
	var old string
	if err := s.store.db.QueryRow(`SELECT id FROM task_runs WHERE issue_id=?`, issue).Scan(&old); err != nil {
		t.Fatal(err)
	}
	requireStatus(t, localRequest(t, h, http.MethodPut, "/api/issues/"+issue, map[string]any{"status": "todo", "suppress_run": true}), http.StatusOK)
	second := localRequest(t, h, http.MethodPost, "/api/issues", map[string]any{"title": "Valid later task", "project_id": project, "status": key})
	requireStatus(t, second, http.StatusCreated)
	later := decodeResponse[Issue](t, second)
	claimed := localRequest(t, h, http.MethodPost, "/api/daemon/tasks/claim", map[string]any{"daemon_id": "test-wakeup-daemon", "runtime_ids": []string{localWakeupRuntimeID(t, s)}, "max_tasks": 1})
	requireStatus(t, claimed, http.StatusOK)
	tasks := decodeResponse[map[string]any](t, claimed)["tasks"].([]any)
	if len(tasks) != 1 || tasks[0].(map[string]any)["issue_id"] != later.ID {
		t.Fatalf("stale queue blocked valid task: count=%d", len(tasks))
	}
	var status string
	if err := s.store.db.QueryRow(`SELECT status FROM task_runs WHERE id=?`, old).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "cancelled" {
		t.Fatalf("stale queued status=%s", status)
	}
}

func TestLocalWorkflowProjectLeadSquadKeepsExecutionIdentity(t *testing.T) {
	s, issue := localWakeupAPIFixture(t)
	h := s.Router()
	made := localRequest(t, h, http.MethodPost, "/api/squads", map[string]any{"name": "Local lead squad", "leader_id": LocalAgentID})
	requireStatus(t, made, http.StatusCreated)
	squad := decodeResponse[map[string]any](t, made)["id"].(string)
	var project string
	if err := s.store.db.QueryRow(`SELECT project_id FROM issues WHERE id=?`, issue).Scan(&project); err != nil {
		t.Fatal(err)
	}
	if _, err := s.store.db.Exec(`UPDATE projects SET lead_type='squad',lead_id=? WHERE id=?`, squad, project); err != nil {
		t.Fatal(err)
	}
	flow := localWorkflowCreateFixture(t, s, "Squad lead handoff", []map[string]any{{"new_status": map[string]any{"name": "Lead review", "category": "started"}, "handler": map[string]any{"type": "project_lead"}, "instructions": "Coordinate this workflow step"}})
	key := ""
	for _, step := range flow.Steps {
		if step.Handler.Type == "project_lead" {
			key = step.StatusKey
		}
	}
	requireStatus(t, localRequest(t, h, http.MethodPost, "/api/projects/"+project+"/workflow", map[string]any{"workflow_id": flow.ID}), http.StatusOK)
	requireStatus(t, localRequest(t, h, http.MethodPut, "/api/issues/"+issue, map[string]any{"status": key}), http.StatusOK)
	claim := localRequest(t, h, http.MethodPost, "/api/daemon/tasks/claim", map[string]any{"daemon_id": "test-wakeup-daemon", "runtime_ids": []string{localWakeupRuntimeID(t, s)}, "max_tasks": 1})
	requireStatus(t, claim, http.StatusOK)
	tasks := decodeResponse[map[string]any](t, claim)["tasks"].([]any)
	if len(tasks) != 1 {
		t.Fatalf("claims=%d", len(tasks))
	}
	task := tasks[0].(map[string]any)
	if task["squad_id"] != squad || task["agent_id"] != LocalAgentID || task["is_leader_task"] != true || task["leader_role_resolved"] != true {
		t.Fatal("project-lead handoff lost authoritative squad/agent identity")
	}
}
