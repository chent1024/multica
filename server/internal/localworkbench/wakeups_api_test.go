package localworkbench

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/testutil"
)

func localWakeupAPIFixture(t *testing.T) (*Server, string) {
	t.Helper()
	root := t.TempDir()
	s, err := NewServer(context.Background(), ServerConfig{DatabasePath: filepath.Join(root, "db.sqlite"), FileRoot: filepath.Join(root, "files")})
	if err != nil {
		t.Fatal(err)
	}
	// API tests own the clock through explicit trigger calls; scheduler behavior
	// is covered by the scheduler suite, without racing HTTP state assertions.
	s.cancel()
	s.wg.Wait()
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
	})
	h := s.Router()
	p := localRequest(t, h, http.MethodPost, "/api/projects", map[string]any{"title": "Wakeup project"})
	requireStatus(t, p, http.StatusCreated)
	project := decodeResponse[Project](t, p)
	registered := localRequest(t, h, http.MethodPost, "/api/daemon/register", map[string]any{"workspace_id": LocalWorkspaceID, "daemon_id": "test-wakeup-daemon", "runtimes": []map[string]any{{"name": "Codex test", "type": "codex", "status": "online", "profile_id": "default", "version": "fake"}}})
	requireStatus(t, registered, http.StatusOK)
	created := localRequest(t, h, http.MethodPost, "/api/issues", map[string]any{"title": "Wakeup issue", "status": "todo", "project_id": project.ID})
	requireStatus(t, created, http.StatusCreated)
	return s, decodeResponse[Issue](t, created).ID
}

func createLocalAPIRule(t *testing.T, s *Server, issue string, body map[string]any) map[string]any {
	t.Helper()
	if body == nil {
		body = map[string]any{"agent_id": LocalAgentID, "instruction": "Inspect local results", "kind": "event", "event_types": []string{"comment.created"}}
	}
	response := localRequest(t, s.Router(), http.MethodPost, "/api/issues/"+issue+"/wakeups", body)
	requireStatus(t, response, http.StatusCreated)
	return decodeResponse[map[string]any](t, response)
}

func TestLocalWakeupAPIManagementAndRevisions(t *testing.T) {
	s, issue := localWakeupAPIFixture(t)
	h := s.Router()
	rule := createLocalAPIRule(t, s, issue, nil)
	id := rule["id"].(string)
	base := "/api/issues/" + issue + "/wakeups/" + id
	if rule["kind"] != "event" || rule["mode"] != "once" || rule["revision"] != float64(1) || rule["enabled"] != true {
		t.Fatalf("created rule=%v", rule)
	}
	for _, key := range []string{"filter_agent_id", "filter_task_id", "interval_seconds", "cron_expression", "next_fire_at", "disabled_at", "last_task_id", "last_error"} {
		if value, exists := rule[key]; !exists || value != nil {
			t.Fatalf("nullable field %s=%v exists=%v", key, value, exists)
		}
	}
	listed := localRequest(t, h, http.MethodGet, "/api/issues/"+issue+"/wakeups", nil)
	requireStatus(t, listed, http.StatusOK)
	if values := decodeResponse[[]map[string]any](t, listed); len(values) != 1 || values[0]["id"] != id {
		t.Fatalf("rules=%v", values)
	}
	edit := map[string]any{"instruction": "Updated instruction", "expected_instruction": "Inspect local results", "revision": 1}
	requireStatus(t, localRequest(t, h, http.MethodPatch, base+"/instruction", edit), http.StatusNoContent)
	requireStatus(t, localRequest(t, h, http.MethodPatch, base+"/instruction", edit), http.StatusConflict)
	current, err := s.store.localWakeup(t.Context(), LocalWorkspaceID, issue, id)
	if err != nil {
		t.Fatal(err)
	}
	if current.Config.Instruction != "Updated instruction" || current.Revision != 1 || !current.Enabled {
		t.Fatalf("instruction changed lifecycle: %+v", current)
	}
	disabled := localRequest(t, h, http.MethodPost, base+"/disable", map[string]any{})
	requireStatus(t, disabled, http.StatusOK)
	off := decodeResponse[map[string]any](t, disabled)
	if off["enabled"] != false || off["revision"] != float64(2) || off["disabled_at"] == nil {
		t.Fatalf("disabled=%v", off)
	}
	requireStatus(t, localRequest(t, h, http.MethodPost, base+"/enable", map[string]any{"revision": 1}), http.StatusConflict)
	enabled := localRequest(t, h, http.MethodPost, base+"/enable", map[string]any{"revision": 2})
	requireStatus(t, enabled, http.StatusOK)
	if on := decodeResponse[map[string]any](t, enabled); on["enabled"] != true || on["revision"] != float64(3) || on["disabled_at"] != nil {
		t.Fatalf("enabled=%v", on)
	}
	requireStatus(t, localRequest(t, h, http.MethodDelete, base, nil), http.StatusNoContent)
	requireStatus(t, localRequest(t, h, http.MethodGet, base+"/runs", nil), http.StatusNotFound)
	var count int
	if err = s.store.db.QueryRow(`SELECT COUNT(*) FROM issue_wakeups WHERE id=?`, id).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("deleted rule remained durable")
	}
}

func TestLocalWakeupAPIValidationAndScope(t *testing.T) {
	s, issue := localWakeupAPIFixture(t)
	h := s.Router()
	tests := []struct {
		name   string
		body   map[string]any
		status int
	}{
		{"unknown kind", map[string]any{"agent_id": LocalAgentID, "instruction": "x", "kind": "unknown"}, 400},
		{"empty instruction", map[string]any{"agent_id": LocalAgentID, "instruction": " ", "kind": "event", "event_types": []string{"comment.created"}}, 400},
		{"short interval", map[string]any{"agent_id": LocalAgentID, "instruction": "x", "kind": "every", "interval_seconds": 59}, 400},
		{"invalid timezone", map[string]any{"agent_id": LocalAgentID, "instruction": "x", "kind": "every", "interval_seconds": 60, "timezone": "Invalid/Zone"}, 400},
		{"unknown field", map[string]any{"agent_id": LocalAgentID, "instruction": "x", "kind": "at", "at": time.Now().Add(time.Hour).Format(time.RFC3339), "enabled": true}, 400},
		{"invalid actor", map[string]any{"agent_id": LocalAgentID, "instruction": "x", "kind": "event", "event_types": []string{"comment.created"}, "filter_actor_type": "robot", "filter_actor_id": LocalOperatorID}, 400},
		{"foreign agent", map[string]any{"agent_id": uuid.NewString(), "instruction": "x", "kind": "event", "event_types": []string{"comment.created"}}, 403},
		{"foreign run", map[string]any{"agent_id": LocalAgentID, "instruction": "x", "kind": "event", "event_types": []string{"task.completed"}, "filter_task_id": uuid.NewString()}, 403},
		{"schedule event filter", map[string]any{"agent_id": LocalAgentID, "instruction": "x", "kind": "at", "at": time.Now().Add(time.Hour).Format(time.RFC3339), "event_types": []string{"comment.created"}}, 400},
		{"invalid condition", map[string]any{"agent_id": LocalAgentID, "instruction": "x", "kind": "event", "condition": map[string]any{"type": "unknown"}}, 400},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			requireStatus(t, localRequest(t, h, http.MethodPost, "/api/issues/"+issue+"/wakeups", tc.body), tc.status)
		})
	}
	bad := httptest.NewRequest(http.MethodGet, "/api/issue-wakeup-summaries", nil)
	bad.RemoteAddr = "127.0.0.1:41414"
	bad.Header.Set("X-Workspace-ID", uuid.NewString())
	testutil.Call(t, h.ServeHTTP, bad).Want(http.StatusForbidden)
	requireStatus(t, localRequest(t, h, http.MethodGet, "/api/issues/"+uuid.NewString()+"/wakeups", nil), http.StatusNotFound)
	var count int
	if err := s.store.db.QueryRow(`SELECT COUNT(*) FROM issue_wakeups`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("rejected creates persisted %d rules", count)
	}
}

func TestLocalWakeupAPIInventorySummaryAndPagination(t *testing.T) {
	s, issue := localWakeupAPIFixture(t)
	h := s.Router()
	ids := []string{}
	for i := 0; i < 5; i++ {
		rule := createLocalAPIRule(t, s, issue, nil)
		ids = append(ids, rule["id"].(string))
	}
	summary := localRequest(t, h, http.MethodGet, "/api/issue-wakeup-summaries", nil)
	requireStatus(t, summary, http.StatusOK)
	previews := decodeResponse[[]map[string]any](t, summary)
	if len(previews) != 3 {
		t.Fatalf("previews=%d want 3", len(previews))
	}
	for _, row := range previews {
		if row["active_count"] != float64(5) || row["event_count"] != float64(5) {
			t.Fatalf("summary=%v", row)
		}
		if _, exists := row["instruction"]; exists {
			t.Fatal("summary leaked instructions")
		}
	}
	page := localRequest(t, h, http.MethodGet, "/api/issue-wakeups?scope=all&limit=2&offset=1", nil)
	requireStatus(t, page, http.StatusOK)
	inventory := decodeResponse[map[string]any](t, page)
	if inventory["total"] != float64(5) || len(inventory["items"].([]any)) != 2 || inventory["counts"].(map[string]any)["active"] != float64(5) {
		t.Fatalf("inventory=%v", inventory)
	}
	for _, item := range inventory["items"].([]any) {
		row := item.(map[string]any)
		if row["issue_title"] != "Wakeup issue" || row["source"] != "member" || row["task"] != nil || row["active_runs"] != float64(0) {
			t.Fatalf("inventory row=%v", row)
		}
		if _, exists := row["instruction"]; exists {
			t.Fatal("inventory leaked instructions")
		}
	}
	requireStatus(t, localRequest(t, h, http.MethodPost, "/api/issues/"+issue+"/wakeups/"+ids[0]+"/disable", map[string]any{}), http.StatusOK)
	filtered := localRequest(t, h, http.MethodGet, "/api/issue-wakeups?scope=disabled&kind=event&source=member&search=wakeup", nil)
	requireStatus(t, filtered, http.StatusOK)
	result := decodeResponse[map[string]any](t, filtered)
	if result["total"] != float64(1) || result["counts"].(map[string]any)["all"] != float64(5) {
		t.Fatalf("filtered=%v", result)
	}
	empty := localRequest(t, h, http.MethodGet, "/api/issue-wakeups?scope=all&offset=100&kind=at", nil)
	requireStatus(t, empty, http.StatusOK)
	if result := decodeResponse[map[string]any](t, empty); result["total"] != float64(0) || len(result["items"].([]any)) != 0 || result["counts"].(map[string]any)["all"] != float64(5) {
		t.Fatalf("empty page=%v", result)
	}
	for _, query := range []string{"scope=unknown", "kind=unknown", "source=unknown", "limit=0", "limit=101", "offset=-1", "offset=1000001", "agent_id=not-a-uuid"} {
		requireStatus(t, localRequest(t, h, http.MethodGet, "/api/issue-wakeups?"+query, nil), http.StatusBadRequest)
	}
	if _, err := s.store.db.Exec(`UPDATE issue_wakeups SET enabled=0,paused_reason='max_fires' WHERE id=?`, ids[1]); err != nil {
		t.Fatal(err)
	}
	paused := localRequest(t, h, http.MethodGet, "/api/issue-wakeup-paused", nil)
	requireStatus(t, paused, http.StatusOK)
	rows := decodeResponse[[]map[string]any](t, paused)
	if len(rows) != 1 || rows[0]["id"] != ids[1] || rows[0]["paused_reason"] != "max_fires" {
		t.Fatalf("paused=%v", rows)
	}
}

func TestLocalWakeupAPISchedulesAndRearm(t *testing.T) {
	s, issue := localWakeupAPIFixture(t)
	h := s.Router()
	future := time.Now().UTC().Add(time.Hour)
	scheduled := createLocalAPIRule(t, s, issue, map[string]any{"agent_id": LocalAgentID, "instruction": "Scheduled check", "kind": "at", "at": future.Format(time.RFC3339Nano)})
	id := scheduled["id"].(string)
	base := "/api/issues/" + issue + "/wakeups/" + id
	if scheduled["next_fire_at"] != future.Format(time.RFC3339Nano) {
		t.Fatalf("scheduled=%v", scheduled)
	}
	if _, err := s.store.db.Exec(`UPDATE issue_wakeups SET enabled=0,last_task_id=?,next_fire_at=NULL,disabled_at=NULL WHERE id=?`, uuid.NewString(), id); err != nil {
		t.Fatal(err)
	}
	requireStatus(t, localRequest(t, h, http.MethodPost, base+"/enable", map[string]any{"revision": 1}), http.StatusBadRequest)
	requireStatus(t, localRequest(t, h, http.MethodPost, base+"/enable", map[string]any{"revision": 1, "rearm": true}), http.StatusBadRequest)
	rearmed := localRequest(t, h, http.MethodPost, base+"/enable", map[string]any{"revision": 1, "rearm": true, "at": future.Add(time.Hour).Format(time.RFC3339Nano)})
	requireStatus(t, rearmed, http.StatusOK)
	if row := decodeResponse[map[string]any](t, rearmed); row["last_task_id"] != nil || row["enabled"] != true || row["revision"] != float64(2) {
		t.Fatalf("rearmed=%v", row)
	}
	every := createLocalAPIRule(t, s, issue, map[string]any{"agent_id": LocalAgentID, "instruction": "Repeat check", "kind": "every", "interval_seconds": 60, "expires_in_seconds": 120})
	if every["expiry_seconds"] != float64(120) || every["expires_at"] == nil || every["mode"] != "continuous" || every["on_timeout"] != "end" {
		t.Fatalf("recurring=%v", every)
	}
	cron := createLocalAPIRule(t, s, issue, map[string]any{"agent_id": LocalAgentID, "instruction": "Cron check", "kind": "cron", "cron_expression": "0 * * * *", "timezone": "Asia/Shanghai"})
	if cron["cron_expression"] != "0 * * * *" || cron["next_fire_at"] == nil {
		t.Fatalf("cron=%v", cron)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/issues/"+issue+"/wakeups", strings.NewReader(`{"agent_id":"`+LocalAgentID+`","instruction":"x","kind":"event","event_types":["comment.created"]} {}`))
	request.RemoteAddr = "127.0.0.1:41414"
	request.Header.Set("Content-Type", "application/json")
	testutil.Call(t, h.ServeHTTP, request).Want(http.StatusBadRequest)
}

func TestLocalWakeupAPIManualHistoryAndDisableCleanup(t *testing.T) {
	s, issue := localWakeupAPIFixture(t)
	h := s.Router()
	rule := createLocalAPIRule(t, s, issue, nil)
	id := rule["id"].(string)
	base := "/api/issues/" + issue + "/wakeups/" + id
	requireStatus(t, localRequest(t, h, http.MethodPost, base+"/trigger", map[string]any{}), http.StatusNoContent)
	history := localRequest(t, h, http.MethodGet, base+"/runs", nil)
	requireStatus(t, history, http.StatusOK)
	runs := decodeResponse[[]map[string]any](t, history)
	if len(runs) != 1 || runs[0]["status"] != "queued" || runs[0]["checkin_note"] != "" || runs[0]["commented"] != false {
		t.Fatalf("history=%v", runs)
	}
	taskID := runs[0]["id"].(string)
	requireStatus(t, localRequest(t, h, http.MethodPost, base+"/disable", map[string]any{}), http.StatusOK)
	var status string
	if err := s.store.db.QueryRow(`SELECT status FROM task_runs WHERE id=?`, taskID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "cancelled" {
		t.Fatalf("unstarted task status=%s", status)
	}
	requireStatus(t, localRequest(t, h, http.MethodPost, base+"/check-in", map[string]any{"note": "No change"}), http.StatusForbidden)
}

func TestLocalWakeupAPIAgentOriginAndOwnerScope(t *testing.T) {
	s, issue := localWakeupAPIFixture(t)
	rule := createLocalAPIRule(t, s, issue, nil)
	id := rule["id"].(string)
	taskID, err := s.triggerLocalWakeup(t.Context(), LocalWorkspaceID, issue, id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.store.db.Exec(`UPDATE task_runs SET status='running',started_at=? WHERE id=?`, now(), taskID); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]any{"instruction": "Agent-created wait", "kind": "event", "event_types": []string{"issue.updated"}})
	request := httptest.NewRequest(http.MethodPost, "/api/issues/"+issue+"/wakeups", bytes.NewReader(body))
	request.RemoteAddr = "127.0.0.1:41414"
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Agent-ID", LocalAgentID)
	request.Header.Set("X-Task-ID", taskID)
	var response map[string]any
	testutil.Call(t, s.Router().ServeHTTP, request).Want(http.StatusCreated).JSON(&response)
	if response["created_by_agent"] != true || response["source_agent_id"] != LocalAgentID || response["agent_id"] != LocalAgentID {
		t.Fatalf("agent origin=%v", response)
	}
	request = httptest.NewRequest(http.MethodPost, "/api/issues/"+issue+"/wakeups", bytes.NewReader(body))
	request.RemoteAddr = "127.0.0.1:41414"
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Agent-ID", uuid.NewString())
	request.Header.Set("X-Task-ID", taskID)
	testutil.Call(t, s.Router().ServeHTTP, request).Want(http.StatusForbidden)
}

func TestLocalWakeupAPISystemProjectionAndManagementBoundary(t *testing.T) {
	s, issue := localWakeupAPIFixture(t)
	h := s.Router()
	parent, err := s.store.GetIssue(t.Context(), LocalWorkspaceID, issue)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.store.db.Exec(`UPDATE issues SET assignee_type='agent',assignee_id=? WHERE id=?`, LocalAgentID, issue); err != nil {
		t.Fatal(err)
	}
	child := localRequest(t, h, http.MethodPost, "/api/issues", map[string]any{"title": "Waiting child", "status": "todo", "project_id": parent.ProjectID, "parent_issue_id": issue})
	requireStatus(t, child, http.StatusCreated)
	if err = s.tickLocalSystemWakeups(t.Context(), time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	var id string
	if err = s.store.db.QueryRow(`SELECT wakeup_id FROM issue_system_wakeups WHERE issue_id=?`, issue).Scan(&id); err != nil {
		t.Fatal(err)
	}
	listed := localRequest(t, h, http.MethodGet, "/api/issues/"+issue+"/wakeups", nil)
	requireStatus(t, listed, http.StatusOK)
	if rows := decodeResponse[[]map[string]any](t, listed); len(rows) != 0 {
		t.Fatalf("system rule leaked into user controls: %v", rows)
	}
	summary := localRequest(t, h, http.MethodGet, "/api/issue-wakeup-summaries", nil)
	requireStatus(t, summary, http.StatusOK)
	if rows := decodeResponse[[]map[string]any](t, summary); len(rows) != 1 || rows[0]["id"] != id || rows[0]["active_count"] != float64(1) {
		t.Fatalf("system summary=%v", rows)
	}
	base := "/api/issues/" + issue + "/wakeups/" + id
	for _, action := range []string{"disable", "enable", "trigger"} {
		requireStatus(t, localRequest(t, h, http.MethodPost, base+"/"+action, map[string]any{"revision": 1}), http.StatusForbidden)
	}
	requireStatus(t, localRequest(t, h, http.MethodDelete, base, nil), http.StatusForbidden)
	requireStatus(t, localRequest(t, h, http.MethodPatch, base+"/instruction", map[string]any{"instruction": "x", "expected_instruction": "old", "revision": 1}), http.StatusForbidden)
	requireStatus(t, localRequest(t, h, http.MethodPut, base, map[string]any{"instruction": "x", "agent_id": LocalAgentID, "kind": "event", "event_types": []string{"comment.created"}}), http.StatusForbidden)
	if _, err = s.store.db.Exec(`UPDATE issue_wakeups SET enabled=0,paused_reason='max_fires' WHERE id=?`, id); err != nil {
		t.Fatal(err)
	}
	paused := localRequest(t, h, http.MethodGet, "/api/issue-wakeup-paused", nil)
	requireStatus(t, paused, http.StatusOK)
	if rows := decodeResponse[[]map[string]any](t, paused); len(rows) != 1 || rows[0]["id"] != id {
		t.Fatalf("system pause cue=%v", rows)
	}
}
