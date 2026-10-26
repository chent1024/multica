package localworkbench

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func localWakeupRunCount(t *testing.T, s *Server, id string) int {
	t.Helper()
	var n int
	if err := s.store.db.QueryRow(`SELECT COUNT(*) FROM issue_wakeup_runs WHERE wakeup_id=?`, id).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}
func completeLocalWakeupRun(t *testing.T, s *Server, id string) {
	t.Helper()
	r := localRequest(t, s.Router(), http.MethodPost, "/api/daemon/tasks/"+id+"/start", map[string]any{"thread_id": "fake-thread-" + id, "work_dir": t.TempDir()})
	requireStatus(t, r, http.StatusOK)
	r = localRequest(t, s.Router(), http.MethodPost, "/api/daemon/tasks/"+id+"/complete", map[string]any{"output": "fake harness completed"})
	requireStatus(t, r, http.StatusOK)
}

func TestLocalWakeupScheduleDispatchRestartAndAtomicDedupe(t *testing.T) {
	for _, kind := range []string{"at", "every", "cron"} {
		t.Run(kind, func(t *testing.T) {
			s, issue := localWakeupAPIFixture(t)
			at := time.Now().UTC().Add(10 * time.Minute)
			input := map[string]any{"agent_id": LocalAgentID, "instruction": "Inspect the scheduled issue", "kind": kind}
			switch kind {
			case "at":
				input["at"] = at.Format(time.RFC3339Nano)
			case "every":
				input["interval_seconds"] = 60
			case "cron":
				input["cron_expression"] = "* * * * *"
				input["timezone"] = "Asia/Shanghai"
			}
			row := createLocalAPIRule(t, s, issue, input)
			id := row["id"].(string)
			var due string
			if err := s.store.db.QueryRow(`SELECT next_fire_at FROM issue_wakeups WHERE id=?`, id).Scan(&due); err != nil {
				t.Fatal(err)
			}
			fire, err := time.Parse(time.RFC3339Nano, due)
			if err != nil {
				t.Fatal(err)
			}
			var wg sync.WaitGroup
			errors := make(chan error, 4)
			for range 4 {
				wg.Add(1)
				go func() { defer wg.Done(); errors <- s.tickLocalWakeups(t.Context(), fire) }()
			}
			wg.Wait()
			close(errors)
			for err := range errors {
				if err != nil {
					t.Fatal(err)
				}
			}
			if n := localWakeupRunCount(t, s, id); n != 1 {
				t.Fatalf("duplicate ticks created %d runs", n)
			}
			var taskID string
			if err := s.store.db.QueryRow(`SELECT task_id FROM issue_wakeup_runs WHERE wakeup_id=?`, id).Scan(&taskID); err != nil {
				t.Fatal(err)
			}
			claim := localRequest(t, s.Router(), http.MethodPost, "/api/daemon/tasks/claim", map[string]any{"daemon_id": "test-wakeup-daemon", "runtime_ids": []string{localWakeupRuntimeID(t, s)}, "max_tasks": 1})
			requireStatus(t, claim, http.StatusOK)
			tasks := decodeResponse[map[string]any](t, claim)["tasks"].([]any)
			if len(tasks) != 1 || tasks[0].(map[string]any)["id"] != taskID {
				t.Fatalf("scheduled task not claimable: count=%d", len(tasks))
			}
			completeLocalWakeupRun(t, s, taskID)
			var databasePath string
			if err := s.store.db.QueryRow(`SELECT file FROM pragma_database_list WHERE name='main'`).Scan(&databasePath); err != nil {
				t.Fatal(err)
			}
			restarted, err := NewServer(context.Background(), ServerConfig{DatabasePath: databasePath, FileRoot: filepath.Join(t.TempDir(), "files")})
			if err != nil {
				t.Fatal(err)
			}
			restarted.cancel()
			restarted.wg.Wait()
			t.Cleanup(func() { _ = restarted.Close() })
			if err := restarted.tickLocalWakeups(t.Context(), fire); err != nil {
				t.Fatal(err)
			}
			if n := localWakeupRunCount(t, restarted, id); n != 1 {
				t.Fatalf("restart replay created %d runs", n)
			}
			history := localRequest(t, restarted.Router(), http.MethodGet, "/api/issues/"+issue+"/wakeups/"+id+"/runs", nil)
			requireStatus(t, history, http.StatusOK)
			runs := decodeResponse[[]map[string]any](t, history)
			if len(runs) != 1 || runs[0]["status"] != "completed" {
				t.Fatalf("history=%v", runs)
			}
		})
	}
}

func TestLocalWakeupEventFiltersAndRollback(t *testing.T) {
	s, issue := localWakeupAPIFixture(t)
	requireStatus(t, localRequest(t, s.Router(), http.MethodPost, "/api/issues/"+issue+"/comments", map[string]any{"content": "before subscription"}), http.StatusCreated)
	row := createLocalAPIRule(t, s, issue, map[string]any{"agent_id": LocalAgentID, "instruction": "Inspect matching new comment", "kind": "event", "event_types": []string{"comment.created"}, "filter_actor_type": "member", "filter_actor_id": LocalOperatorID})
	id := row["id"].(string)
	at := time.Now().UTC().Add(time.Second)
	if err := s.tickLocalWakeups(t.Context(), at); err != nil {
		t.Fatal(err)
	}
	if n := localWakeupRunCount(t, s, id); n != 0 {
		t.Fatalf("historical event fired %d runs", n)
	}
	tx, err := s.store.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`INSERT INTO comments(id,issue_id,author_type,author_id,content,type,created_at,updated_at) VALUES('rolled-back',?,'system',?,'not committed','comment',?,?)`, issue, LocalOperatorID, now(), now()); err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if err := s.tickLocalWakeups(t.Context(), at); err != nil {
		t.Fatal(err)
	}
	if n := localWakeupRunCount(t, s, id); n != 0 {
		t.Fatal("rolled-back fact fired")
	}
	requireStatus(t, localRequest(t, s.Router(), http.MethodPost, "/api/issues/"+issue+"/comments", map[string]any{"content": "new committed comment"}), http.StatusCreated)
	if err := s.tickLocalWakeups(t.Context(), at); err != nil {
		t.Fatal(err)
	}
	if n := localWakeupRunCount(t, s, id); n != 1 {
		t.Fatalf("matching comment runs=%d", n)
	}
}

func TestLocalWakeupUnavailableRuntimeRetainsWorkAndExpiry(t *testing.T) {
	s, issue := localWakeupAPIFixture(t)
	at := time.Now().UTC().Add(10 * time.Minute)
	row := createLocalAPIRule(t, s, issue, map[string]any{"agent_id": LocalAgentID, "instruction": "Recover unavailable runtime", "kind": "at", "at": at.Format(time.RFC3339Nano)})
	id := row["id"].(string)
	if _, err := s.store.db.Exec(`UPDATE runtimes SET status='offline'`); err != nil {
		t.Fatal(err)
	}
	if err := s.tickLocalWakeups(t.Context(), at); err != nil {
		t.Fatal(err)
	}
	if n := localWakeupRunCount(t, s, id); n != 0 {
		t.Fatal("offline runtime received a run")
	}
	var message sql.NullString
	if err := s.store.db.QueryRow(`SELECT last_error FROM issue_wakeups WHERE id=?`, id).Scan(&message); err != nil {
		t.Fatal(err)
	}
	if !message.Valid {
		t.Fatal("missing actionable runtime error")
	}
	if _, err := s.store.db.Exec(`UPDATE runtimes SET status='online'`); err != nil {
		t.Fatal(err)
	}
	if err := s.tickLocalWakeups(t.Context(), at); err != nil {
		t.Fatal(err)
	}
	if n := localWakeupRunCount(t, s, id); n != 1 {
		t.Fatalf("recovered runs=%d", n)
	}
	var task string
	if err := s.store.db.QueryRow(`SELECT task_id FROM issue_wakeup_runs WHERE wakeup_id=?`, id).Scan(&task); err != nil {
		t.Fatal(err)
	}
	completeLocalWakeupRun(t, s, task)
	for _, action := range []string{"end", "wake"} {
		row := createLocalAPIRule(t, s, issue, map[string]any{"agent_id": LocalAgentID, "instruction": "Deadline action " + action, "kind": "event", "event_types": []string{"comment.created"}, "expires_in_seconds": 60, "on_timeout": action})
		if err := s.tickLocalWakeups(t.Context(), at.Add(2*time.Minute)); err != nil {
			t.Fatal(err)
		}
		n := localWakeupRunCount(t, s, row["id"].(string))
		want := 0
		if action == "wake" {
			want = 1
		}
		if n != want {
			t.Fatalf("timeout %s runs=%d want=%d", action, n, want)
		}
	}
}

func TestLocalWakeupConditionAndFireLimit(t *testing.T) {
	s, issue := localWakeupAPIFixture(t)
	at := time.Now().UTC().Add(time.Minute)
	row := createLocalAPIRule(t, s, issue, map[string]any{"agent_id": LocalAgentID, "instruction": "Inspect review state", "kind": "event", "mode": "continuous", "max_fires": 1, "condition": map[string]any{"type": "issue_field", "field": "status", "value": "in_review"}})
	id := row["id"].(string)
	if err := s.tickLocalWakeups(t.Context(), at); err != nil {
		t.Fatal(err)
	}
	if localWakeupRunCount(t, s, id) != 0 {
		t.Fatal("unmet condition fired")
	}
	requireStatus(t, localRequest(t, s.Router(), http.MethodPut, "/api/issues/"+issue, map[string]any{"status": "in_review"}), http.StatusOK)
	if err := s.tickLocalWakeups(t.Context(), at); err != nil {
		t.Fatal(err)
	}
	if localWakeupRunCount(t, s, id) != 1 {
		t.Fatal("met condition did not fire")
	}
	var paused string
	var enabled bool
	if err := s.store.db.QueryRow(`SELECT enabled,paused_reason FROM issue_wakeups WHERE id=?`, id).Scan(&enabled, &paused); err != nil {
		t.Fatal(err)
	}
	if enabled || paused != "max_fires" {
		t.Fatalf("cap enabled=%v paused=%s", enabled, paused)
	}
}

func TestLocalSystemWakeupMemberStageDeliveryAndDefaults(t *testing.T) {
	s, parent := localWakeupAPIFixture(t)
	if _, err := s.store.db.Exec(`UPDATE issues SET assignee_type='member',assignee_id=? WHERE id=?`, LocalOperatorID, parent); err != nil {
		t.Fatal(err)
	}
	var project string
	if err := s.store.db.QueryRow(`SELECT project_id FROM issues WHERE id=?`, parent).Scan(&project); err != nil {
		t.Fatal(err)
	}
	children := []string{}
	for stage := 1; stage <= 2; stage++ {
		response := localRequest(t, s.Router(), http.MethodPost, "/api/issues", map[string]any{"title": fmt.Sprintf("Stage %d", stage), "status": "backlog", "project_id": project, "parent_issue_id": parent, "stage": stage})
		requireStatus(t, response, http.StatusCreated)
		children = append(children, decodeResponse[Issue](t, response).ID)
	}
	at := time.Now().UTC().Add(time.Minute)
	if err := s.tickLocalWakeups(t.Context(), at); err != nil {
		t.Fatal(err)
	}
	requireStatus(t, localRequest(t, s.Router(), http.MethodPut, "/api/issues/"+children[0], map[string]any{"status": "done"}), http.StatusOK)
	for range 2 {
		if err := s.tickLocalWakeups(t.Context(), at); err != nil {
			t.Fatal(err)
		}
	}
	var n int
	if err := s.store.db.QueryRow(`SELECT COUNT(*) FROM inbox_items WHERE issue_id=? AND type='children_done'`, parent).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("stage notifications=%d", n)
	}
	response := localRequest(t, s.Router(), http.MethodGet, "/api/issues/"+parent+"/system-wakeups", nil)
	requireStatus(t, response, http.StatusOK)
	rules := decodeResponse[[]map[string]any](t, response)
	if len(rules) != 1 || rules[0]["blocked"] != "member_assignee" {
		t.Fatalf("system rule=%v", rules)
	}
	requireStatus(t, localRequest(t, s.Router(), http.MethodPut, "/api/system-wakeups/child_done", map[string]any{"enabled": false}), http.StatusOK)
	requireStatus(t, localRequest(t, s.Router(), http.MethodPut, "/api/issues/"+children[1], map[string]any{"status": "done"}), http.StatusOK)
	if err := s.tickLocalWakeups(t.Context(), at); err != nil {
		t.Fatal(err)
	}
	if err := s.store.db.QueryRow(`SELECT COUNT(*) FROM inbox_items WHERE issue_id=? AND type='children_done'`, parent).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatal("disabled default delivered")
	}
	requireStatus(t, localRequest(t, s.Router(), http.MethodPut, "/api/issues/"+parent+"/system-wakeups/child_done", map[string]any{"enabled": true, "instruction": "Summarize all child outputs"}), http.StatusOK)
	if err := s.tickLocalWakeups(t.Context(), at); err != nil {
		t.Fatal(err)
	}
	if err := s.store.db.QueryRow(`SELECT COUNT(*) FROM inbox_items WHERE issue_id=? AND type='children_done'`, parent).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("override final notifications=%d", n)
	}
	inventory := localRequest(t, s.Router(), http.MethodGet, "/api/issue-wakeups?scope=all&source=system", nil)
	requireStatus(t, inventory, http.StatusOK)
	items := decodeResponse[map[string]any](t, inventory)["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("system inventory=%v", items)
	}
}

func localWakeupRuntimeID(t *testing.T, s *Server) string {
	t.Helper()
	var id string
	if err := s.store.db.QueryRow(`SELECT id FROM runtimes WHERE provider='codex' LIMIT 1`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestLocalWakeupJoinsQueuedRunAndFencesChangedInput(t *testing.T) {
	for _, changed := range []bool{false, true} {
		t.Run(fmt.Sprint(changed), func(t *testing.T) {
			s, issue := localWakeupAPIFixture(t)
			row := createLocalAPIRule(t, s, issue, nil)
			id := row["id"].(string)
			// An existing assignment of the same agent is waiting for the same operator.
			if _, err := s.store.db.Exec(`UPDATE issues SET assignee_type='agent',assignee_id=? WHERE id=?`, LocalAgentID, issue); err != nil {
				t.Fatal(err)
			}
			if err := s.store.enqueueAssignedIssue(t.Context(), LocalWorkspaceID, issue, "Original assignment"); err != nil {
				t.Fatal(err)
			}
			requireStatus(t, localRequest(t, s.Router(), http.MethodPost, "/api/issues/"+issue+"/comments", map[string]any{"content": "Input while assignment waits"}), http.StatusCreated)
			if err := s.tickLocalWakeups(t.Context(), time.Now().UTC()); err != nil {
				t.Fatal(err)
			}
			if localWakeupRunCount(t, s, id) != 0 {
				t.Fatal("waiting inputs were consumed before claim/start")
			}
			claim := localModernWakeupClaim(t, s, "test-wakeup-daemon", localWakeupRuntimeID(t, s))
			requireStatus(t, claim, http.StatusOK)
			tasks := decodeResponse[map[string]any](t, claim)["tasks"].([]any)
			if len(tasks) != 1 {
				t.Fatalf("claims=%d", len(tasks))
			}
			task := tasks[0].(map[string]any)
			if note, ok := task["wakeup_joined"].(string); !ok || note == "" {
				t.Fatal("joined instruction absent from actual daemon claim")
			}
			taskID := task["id"].(string)
			if localWakeupRunCount(t, s, id) != 0 {
				t.Fatal("claim counted a run that did not start")
			}
			if changed {
				requireStatus(t, localRequest(t, s.Router(), http.MethodPost, "/api/issues/"+issue+"/wakeups/"+id+"/disable", map[string]any{}), http.StatusOK)
			}
			started := localRequest(t, s.Router(), http.MethodPost, "/api/daemon/tasks/"+taskID+"/start", map[string]any{})
			if changed {
				requireStatus(t, started, http.StatusConflict)
				if localWakeupRunCount(t, s, id) != 0 {
					t.Fatal("changed rule consumed old input")
				}
			} else {
				requireStatus(t, started, http.StatusOK)
				if localWakeupRunCount(t, s, id) != 1 {
					t.Fatal("started joined run missing from real history")
				}
			}
		})
	}
}

func TestLocalWakeupCheckinUsesRealRunAndScheduledKind(t *testing.T) {
	for _, kind := range []string{"event", "at", "every"} {
		t.Run(kind, func(t *testing.T) {
			s, issue := localWakeupAPIFixture(t)
			in := map[string]any{"agent_id": LocalAgentID, "instruction": "Check without noise", "kind": kind}
			switch kind {
			case "event":
				in["event_types"] = []string{"comment.created"}
			case "at":
				in["at"] = time.Now().UTC().Add(time.Minute).Format(time.RFC3339Nano)
			case "every":
				in["interval_seconds"] = 60
			}
			row := createLocalAPIRule(t, s, issue, in)
			id := row["id"].(string)
			requireStatus(t, localRequest(t, s.Router(), http.MethodPost, "/api/issues/"+issue+"/wakeups/"+id+"/trigger", map[string]any{}), http.StatusNoContent)
			var taskID string
			if err := s.store.db.QueryRow(`SELECT task_id FROM issue_wakeup_runs WHERE wakeup_id=?`, id).Scan(&taskID); err != nil {
				t.Fatal(err)
			}
			requireStatus(t, localRequest(t, s.Router(), http.MethodPost, "/api/daemon/tasks/"+taskID+"/start", map[string]any{}), http.StatusOK)
			base := "/api/issues/" + issue + "/wakeups/" + id + "/check-in"
			checked := localWakeupHeaderRequest(t, s, http.MethodPost, base, map[string]any{"note": "No changes to report"}, LocalAgentID, taskID)
			if kind == "every" {
				requireStatus(t, checked, http.StatusNoContent)
			} else {
				requireStatus(t, checked, http.StatusBadRequest)
			}
			requireStatus(t, localWakeupHeaderRequest(t, s, http.MethodPost, base, map[string]any{"note": strings.Repeat("字", 501)}, LocalAgentID, taskID), http.StatusBadRequest)
		})
	}
}

func TestLocalWakeupCapturedActorIsEditorAndDoesNotWakeItself(t *testing.T) {
	s, issue := localWakeupAPIFixture(t)
	primary := createLocalAPIRule(t, s, issue, map[string]any{"agent_id": LocalAgentID, "instruction": "Start fake source", "kind": "every", "interval_seconds": 60})
	requireStatus(t, localRequest(t, s.Router(), http.MethodPost, "/api/issues/"+issue+"/wakeups/"+primary["id"].(string)+"/trigger", map[string]any{}), http.StatusNoContent)
	var source string
	if err := s.store.db.QueryRow(`SELECT task_id FROM issue_wakeup_runs WHERE wakeup_id=?`, primary["id"]).Scan(&source); err != nil {
		t.Fatal(err)
	}
	requireStatus(t, localRequest(t, s.Router(), http.MethodPost, "/api/daemon/tasks/"+source+"/start", map[string]any{}), http.StatusOK)
	rule := createLocalAPIRule(t, s, issue, map[string]any{"agent_id": LocalAgentID, "instruction": "Read a genuinely new update", "kind": "event", "event_types": []string{"comment.created", "comment.updated", "issue.updated"}, "mode": "continuous"})
	comment := localWakeupHeaderRequest(t, s, http.MethodPost, "/api/issues/"+issue+"/comments", map[string]any{"content": "Agent authored"}, LocalAgentID, source)
	requireStatus(t, comment, http.StatusCreated)
	cid := decodeResponse[Comment](t, comment).ID
	requireStatus(t, localWakeupHeaderRequest(t, s, http.MethodPut, "/api/issues/"+issue, map[string]any{"title": "Agent changed title"}, LocalAgentID, source), http.StatusOK)
	var actorType, actorID, task string
	if err := s.store.db.QueryRow(`SELECT actor_type,actor_id,source_task_id FROM issue_wakeup_events WHERE issue_id=? AND event_type='issue.updated' ORDER BY id DESC LIMIT 1`, issue).Scan(&actorType, &actorID, &task); err != nil {
		t.Fatal(err)
	}
	if actorType != "agent" || actorID != LocalAgentID || task != source {
		t.Fatalf("actual mutation actor=%s/%s/%s", actorType, actorID, task)
	}
	if err := s.tickLocalWakeups(t.Context(), time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	var pending int
	if err := s.store.db.QueryRow(`SELECT COUNT(*) FROM issue_wakeup_receipts WHERE wakeup_id=? AND task_id IS NULL`, rule["id"]).Scan(&pending); err != nil {
		t.Fatal(err)
	}
	if pending != 0 {
		t.Fatal("agent's own mutations created pending self-wakeup inputs")
	}
	requireStatus(t, localRequest(t, s.Router(), http.MethodPut, "/api/comments/"+cid, map[string]any{"content": "Member edited the old agent comment"}), http.StatusOK)
	if err := s.store.db.QueryRow(`SELECT actor_type,actor_id FROM issue_wakeup_events WHERE issue_id=? AND event_type='comment.updated' ORDER BY id DESC LIMIT 1`, issue).Scan(&actorType, &actorID); err != nil {
		t.Fatal(err)
	}
	if actorType != "member" || actorID != LocalOperatorID {
		t.Fatalf("editor inherited original author: %s/%s", actorType, actorID)
	}
	requireStatus(t, localWakeupHeaderRequest(t, s, http.MethodPut, "/api/issues/"+issue, map[string]any{"title": "Must not apply"}, LocalAgentID, "not-a-running-task"), http.StatusForbidden)
}

func localWakeupHeaderRequest(t *testing.T, s *Server, method, path string, body any, agent, task string) *httptest.ResponseRecorder {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(method, path, bytes.NewReader(raw))
	req.RemoteAddr = "127.0.0.1:21212"
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Agent-ID", agent)
	req.Header.Set("X-Task-ID", task)
	out := httptest.NewRecorder()
	s.Router().ServeHTTP(out, req)
	return out
}

func TestLocalTaskNativeStartClaimReplayAndSupersededFence(t *testing.T) {
	s, issue := localWakeupAPIFixture(t)
	h := s.Router()
	rule := createLocalAPIRule(t, s, issue, nil)
	requireStatus(t, localRequest(t, h, http.MethodPost, "/api/issues/"+issue+"/wakeups/"+rule["id"].(string)+"/trigger", map[string]any{}), http.StatusNoContent)
	claim := localRequest(t, h, http.MethodPost, "/api/daemon/tasks/claim", map[string]any{"daemon_id": "test-wakeup-daemon", "runtime_ids": []string{localWakeupRuntimeID(t, s)}, "max_tasks": 1})
	requireStatus(t, claim, http.StatusOK)
	task := decodeResponse[map[string]any](t, claim)["tasks"].([]any)[0].(map[string]any)
	id := task["id"].(string)
	if task["start_claim_supported"] != true {
		t.Fatal("new daemon did not receive safe replay capability")
	}
	bad := map[string]any{"runtime_id": task["runtime_id"], "dispatched_at": "old-claim"}
	requireStatus(t, localRequest(t, h, http.MethodPost, "/api/daemon/tasks/"+id+"/start", bad), http.StatusConflict)
	body := map[string]any{"runtime_id": task["runtime_id"], "dispatched_at": task["dispatched_at"]}
	requireStatus(t, localRequest(t, h, http.MethodPost, "/api/daemon/tasks/"+id+"/start", body), http.StatusOK)
	var before string
	if err := s.store.db.QueryRow(`SELECT started_at FROM task_runs WHERE id=?`, id).Scan(&before); err != nil {
		t.Fatal(err)
	}
	requireStatus(t, localRequest(t, h, http.MethodPost, "/api/daemon/tasks/"+id+"/start", body), http.StatusOK)
	var after string
	if err := s.store.db.QueryRow(`SELECT started_at FROM task_runs WHERE id=?`, id).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Fatal("same native start claim was replayed as a new start")
	}
}

func TestLocalScheduledCheckinCompletesWithoutCommentOrReviewNoise(t *testing.T) {
	s, issue := localWakeupAPIFixture(t)
	h := s.Router()
	row := createLocalAPIRule(t, s, issue, map[string]any{"agent_id": LocalAgentID, "instruction": "Quiet periodic check", "kind": "every", "interval_seconds": 60})
	id := row["id"].(string)
	requireStatus(t, localRequest(t, h, http.MethodPost, "/api/issues/"+issue+"/wakeups/"+id+"/trigger", map[string]any{}), http.StatusNoContent)
	var task string
	if err := s.store.db.QueryRow(`SELECT task_id FROM issue_wakeup_runs WHERE wakeup_id=?`, id).Scan(&task); err != nil {
		t.Fatal(err)
	}
	requireStatus(t, localRequest(t, h, http.MethodPost, "/api/daemon/tasks/"+task+"/start", map[string]any{}), http.StatusOK)
	requireStatus(t, localWakeupHeaderRequest(t, s, http.MethodPost, "/api/issues/"+issue+"/wakeups/"+id+"/check-in", map[string]any{"note": "No changes"}, LocalAgentID, task), http.StatusNoContent)
	requireStatus(t, localRequest(t, h, http.MethodPost, "/api/daemon/tasks/"+task+"/complete", map[string]any{"output": "No changes, silently checked in"}), http.StatusOK)
	var comments, notifications int
	var status string
	if err := s.store.db.QueryRow(`SELECT COUNT(*) FROM comments WHERE source_task_id=?`, task).Scan(&comments); err != nil {
		t.Fatal(err)
	}
	if err := s.store.db.QueryRow(`SELECT COUNT(*) FROM inbox_items WHERE json_extract(details,'$.task_id')=?`, task).Scan(&notifications); err != nil {
		t.Fatal(err)
	}
	if err := s.store.db.QueryRow(`SELECT status FROM issues WHERE id=?`, issue).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if comments != 0 || notifications != 0 || status != "todo" {
		t.Fatalf("quiet check changed issue/noise: comments=%d notifications=%d status=%s", comments, notifications, status)
	}
}

func localModernWakeupClaim(t *testing.T, s *Server, daemon, runtime string) *httptest.ResponseRecorder {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"daemon_id": daemon, "runtime_ids": []string{runtime}, "max_tasks": 1})
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPost, "/api/daemon/tasks/claim", bytes.NewReader(raw))
	r.RemoteAddr = "127.0.0.1:19191"
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-Client-Capabilities", "joined-wakeups-v1")
	w := httptest.NewRecorder()
	s.Router().ServeHTTP(w, r)
	return w
}

func TestLocalWakeupIssueCloseCancelsClaimBeforeStart(t *testing.T) {
	for _, running := range []bool{false, true} {
		name := "claimed"
		if running {
			name = "running"
		}
		t.Run(name, func(t *testing.T) {
			s, issue := localWakeupAPIFixture(t)
			rule := createLocalAPIRule(t, s, issue, nil)
			taskID, err := s.triggerLocalWakeup(t.Context(), LocalWorkspaceID, issue, rule["id"].(string))
			if err != nil || taskID == "" {
				t.Fatalf("trigger task=%q err=%v", taskID, err)
			}
			claim := localModernWakeupClaim(t, s, "test-wakeup-daemon", localWakeupRuntimeID(t, s))
			requireStatus(t, claim, http.StatusOK)
			tasks := decodeResponse[map[string]any](t, claim)["tasks"].([]any)
			if len(tasks) != 1 {
				t.Fatalf("claims=%d", len(tasks))
			}
			task := tasks[0].(map[string]any)
			start := map[string]any{"runtime_id": task["runtime_id"], "dispatched_at": task["dispatched_at"]}
			if running {
				requireStatus(t, localRequest(t, s.Router(), http.MethodPost, "/api/daemon/tasks/"+taskID+"/start", start), http.StatusOK)
			}
			requireStatus(t, localRequest(t, s.Router(), http.MethodPut, "/api/issues/"+issue, map[string]any{"status": "done"}), http.StatusOK)
			var status string
			if err := s.store.db.QueryRow(`SELECT status FROM task_runs WHERE id=?`, taskID).Scan(&status); err != nil {
				t.Fatal(err)
			}
			want := "cancelled"
			if running {
				want = "running"
			}
			if status != want {
				t.Fatalf("after issue close: task status=%s want=%s", status, want)
			}
			if !running {
				requireStatus(t, localRequest(t, s.Router(), http.MethodPost, "/api/daemon/tasks/"+taskID+"/start", start), http.StatusConflict)
			}
		})
	}
}
