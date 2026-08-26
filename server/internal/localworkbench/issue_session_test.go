package localworkbench

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
)

func TestIssueSessionMessageIsAtomicIdempotentAndSerial(t *testing.T) {
	app, err := New(t.Context(), filepath.Join(t.TempDir(), "multica.db"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	workspaces, err := app.store.ListWorkspaces(t.Context())
	if err != nil || len(workspaces) != 1 {
		t.Fatalf("workspaces = %#v, err = %v", workspaces, err)
	}
	stamp := now()
	runtimeID := "session-runtime"
	if _, err := app.store.db.ExecContext(t.Context(), `INSERT INTO runtimes(id, workspace_id, daemon_id, name, provider, version, status, created_at, updated_at) VALUES (?, ?, 'session-daemon', 'Codex', 'codex', '1', 'online', ?, ?)`, runtimeID, workspaces[0].ID, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	issue, err := app.store.CreateIssue(t.Context(), workspaces[0].ID, CreateIssueInput{Title: "Conversation"})
	if err != nil {
		t.Fatal(err)
	}

	first, err := app.store.SendIssueSessionMessage(t.Context(), workspaces[0].ID, issue.ID, "request-1", "Implement the feed", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !first.Created || first.Outcome != "queued" || first.Comment.Content != "Implement the feed" || first.TaskID == "" {
		t.Fatalf("first message = %#v", first)
	}
	repeated, err := app.store.SendIssueSessionMessage(t.Context(), workspaces[0].ID, issue.ID, "request-1", "ignored retry body", nil)
	if err != nil {
		t.Fatal(err)
	}
	if repeated.TaskID != first.TaskID || repeated.Comment.ID != first.Comment.ID {
		t.Fatalf("idempotent retry = %#v, want task=%s comment=%s", repeated, first.TaskID, first.Comment.ID)
	}
	if repeated.Created {
		t.Fatal("idempotent retry was reported as newly created")
	}
	second, err := app.store.SendIssueSessionMessage(t.Context(), workspaces[0].ID, issue.ID, "request-2", "Then review it", nil)
	if err != nil {
		t.Fatal(err)
	}
	if second.Outcome != "waiting" {
		t.Fatalf("second message outcome = %q", second.Outcome)
	}
	var comments, runs int
	if err := app.store.db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM comments WHERE issue_id=?`, issue.ID).Scan(&comments); err != nil {
		t.Fatal(err)
	}
	if err := app.store.db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM task_runs WHERE issue_id=?`, issue.ID).Scan(&runs); err != nil {
		t.Fatal(err)
	}
	if comments != 2 || runs != 2 {
		t.Fatalf("idempotent counts comments=%d runs=%d", comments, runs)
	}

	claimed, err := app.claimTask(t.Context(), "session-daemon", runtimeID)
	if err != nil {
		t.Fatal(err)
	}
	if claimed["id"] != first.TaskID || claimed["trigger_comment_id"] != first.Comment.ID || claimed["trigger_comment_content"] != "Implement the feed" {
		t.Fatalf("first claim = %#v", claimed)
	}
	if another, err := app.claimTask(t.Context(), "session-daemon", runtimeID); err != nil || another != nil {
		t.Fatalf("waiting run was claimable: task=%#v err=%v", another, err)
	}

	server := httptest.NewServer(app.Router())
	defer server.Close()
	var empty map[string]any
	requestJSON(t, server.URL, http.MethodPost, "/api/daemon/tasks/"+first.TaskID+"/complete", map[string]any{"session_id": "provider-thread", "work_dir": "/tmp/session-work", "output": "Implemented"}, http.StatusOK, &empty)
	var promotedStatus, promotedPrior string
	if err := app.store.db.QueryRowContext(t.Context(), `SELECT status, prior_task_id FROM task_runs WHERE id=?`, second.TaskID).Scan(&promotedStatus, &promotedPrior); err != nil {
		t.Fatal(err)
	}
	if promotedStatus != "queued" || promotedPrior != first.TaskID {
		t.Fatalf("promoted run status=%q prior=%q", promotedStatus, promotedPrior)
	}
	var sourceTaskID string
	if err := app.store.db.QueryRowContext(t.Context(), `SELECT source_task_id FROM comments WHERE issue_id=? AND author_type='agent'`, issue.ID).Scan(&sourceTaskID); err != nil {
		t.Fatal(err)
	}
	if sourceTaskID != first.TaskID {
		t.Fatalf("agent outcome source = %q", sourceTaskID)
	}
	claimed, err = app.claimTask(t.Context(), "session-daemon", runtimeID)
	if err != nil || claimed["id"] != second.TaskID || claimed["prior_session_id"] != "provider-thread" {
		t.Fatalf("promoted claim = %#v, err=%v", claimed, err)
	}
	body, _ := json.Marshal(map[string]any{"content": "Ship after review"})
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, server.URL+"/api/issues/"+issue.ID+"/session/messages", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", "request-3")
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("session message HTTP status = %d", response.StatusCode)
	}
	var payload struct {
		Outcome string `json:"outcome"`
		Task    struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"task"`
	}
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if payload.Outcome != "waiting" || payload.Task.Status != "waiting_previous_run" {
		t.Fatalf("session message HTTP payload = %#v", payload)
	}
	var cancelled map[string]any
	requestJSON(t, server.URL, http.MethodPost, "/api/issues/"+issue.ID+"/tasks/"+payload.Task.ID+"/cancel", map[string]any{}, http.StatusOK, &cancelled)
	if cancelled["status"] != "cancelled" {
		t.Fatalf("cancelled waiting run = %#v", cancelled)
	}
	requestJSON(t, server.URL, http.MethodPost, "/api/daemon/tasks/"+second.TaskID+"/complete", map[string]any{"session_id": "provider-thread", "work_dir": "/tmp/session-work", "output": "Reviewed"}, http.StatusOK, &empty)
	if another, err := app.claimTask(t.Context(), "session-daemon", runtimeID); err != nil || another != nil {
		t.Fatalf("cancelled waiting run became claimable: task=%#v err=%v", another, err)
	}
}

func TestIssueSessionMessageRollsBackInvalidAttachments(t *testing.T) {
	app, err := New(t.Context(), filepath.Join(t.TempDir(), "multica.db"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	workspaces, _ := app.store.ListWorkspaces(t.Context())
	stamp := now()
	if _, err := app.store.db.ExecContext(t.Context(), `INSERT INTO runtimes(id, workspace_id, daemon_id, name, provider, status, created_at, updated_at) VALUES ('runtime', ?, 'daemon', 'Codex', 'codex', 'online', ?, ?)`, workspaces[0].ID, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	issue, err := app.store.CreateIssue(t.Context(), workspaces[0].ID, CreateIssueInput{Title: "Atomic"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.store.SendIssueSessionMessage(t.Context(), workspaces[0].ID, issue.ID, "invalid-attachment", "Must roll back", []string{"missing"}); err == nil {
		t.Fatal("invalid attachment unexpectedly succeeded")
	}
	var comments, runs int
	_ = app.store.db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM comments WHERE issue_id=?`, issue.ID).Scan(&comments)
	_ = app.store.db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM task_runs WHERE issue_id=?`, issue.ID).Scan(&runs)
	if comments != 0 || runs != 0 {
		t.Fatalf("partial write comments=%d runs=%d", comments, runs)
	}
}

func TestIssueSessionStageAndChildHandoff(t *testing.T) {
	app, err := New(t.Context(), filepath.Join(t.TempDir(), "multica.db"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	server := httptest.NewServer(app.Router())
	defer server.Close()

	var parent Issue
	requestJSON(t, server.URL, http.MethodPost, "/api/issues", map[string]any{"title": "Parent"}, http.StatusCreated, &parent)
	var initial IssueSession
	requestJSON(t, server.URL, http.MethodGet, "/api/issues/"+parent.ID+"/session", nil, http.StatusOK, &initial)
	if len(initial.Stages) != 1 || initial.Stages[0].Kind != "analysis" {
		t.Fatalf("initial session = %#v", initial)
	}

	var implementation IssueSession
	requestJSON(t, server.URL, http.MethodPost, "/api/issues/"+parent.ID+"/session/stages", map[string]any{"kind": "implementation"}, http.StatusOK, &implementation)
	requestJSON(t, server.URL, http.MethodPost, "/api/issues/"+parent.ID+"/session/stages", map[string]any{"kind": "implementation"}, http.StatusOK, &implementation)
	if len(implementation.Stages) != 2 {
		t.Fatalf("idempotent stage activation appended: %#v", implementation.Stages)
	}
	requestJSON(t, server.URL, http.MethodPost, "/api/issues/"+parent.ID+"/session/stages", map[string]any{"kind": "analysis"}, http.StatusOK, &implementation)
	if len(implementation.Stages) != 3 || implementation.Stages[2].Kind != "analysis" {
		t.Fatalf("repeated stage missing: %#v", implementation.Stages)
	}
	var invalidStage map[string]any
	requestJSON(t, server.URL, http.MethodPost, "/api/issues/"+parent.ID+"/session/stages", map[string]any{"kind": "deploy"}, http.StatusBadRequest, &invalidStage)
	var afterInvalid IssueSession
	requestJSON(t, server.URL, http.MethodGet, "/api/issues/"+parent.ID+"/session", nil, http.StatusOK, &afterInvalid)
	if len(afterInvalid.Stages) != 3 || afterInvalid.ActiveStageID != implementation.ActiveStageID {
		t.Fatalf("invalid activation changed session: %#v", afterInvalid)
	}

	var child Issue
	requestJSON(t, server.URL, http.MethodPost, "/api/issues", map[string]any{
		"title": "Child", "parent_issue_id": parent.ID,
		"handoff": map[string]any{"objective": "Ship the bounded slice", "confirmed_facts": []string{"Parent contract is fixed"}},
	}, http.StatusCreated, &child)
	var childSession IssueSession
	requestJSON(t, server.URL, http.MethodGet, "/api/issues/"+child.ID+"/session", nil, http.StatusOK, &childSession)
	if childSession.Handoff == nil || childSession.Handoff.ParentIssueID != parent.ID || childSession.Handoff.SourceStageID != implementation.ActiveStageID || childSession.Handoff.SourceStageKind != "analysis" {
		t.Fatalf("child handoff = %#v", childSession.Handoff)
	}

	var before int
	if err := app.store.db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM issues`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	var errorBody map[string]any
	requestJSON(t, server.URL, http.MethodPost, "/api/issues", map[string]any{
		"title": "Invalid child", "parent_issue_id": parent.ID, "handoff": map[string]any{"objective": ""},
	}, http.StatusBadRequest, &errorBody)
	requestJSON(t, server.URL, http.MethodPost, "/api/issues", map[string]any{
		"title": "Wrong source", "parent_issue_id": parent.ID,
		"handoff": map[string]any{"objective": "Bounded", "source_run_id": "another-session-run"},
	}, http.StatusBadRequest, &errorBody)
	var after int
	if err := app.store.db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM issues`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Fatalf("invalid handoff created partial issue: before=%d after=%d", before, after)
	}
	requestJSON[map[string]any](t, server.URL, http.MethodDelete, "/api/issues/"+child.ID, nil, http.StatusNoContent, nil)
	var sessionRows, handoffRows int
	if err := app.store.db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM issue_sessions WHERE issue_id=?`, child.ID).Scan(&sessionRows); err != nil {
		t.Fatal(err)
	}
	if err := app.store.db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM issue_handoffs WHERE child_issue_id=?`, child.ID).Scan(&handoffRows); err != nil {
		t.Fatal(err)
	}
	if sessionRows != 0 || handoffRows != 0 {
		t.Fatalf("deleted child retained session=%d handoff=%d", sessionRows, handoffRows)
	}
}

func TestPinTaskSessionClassifiesResumeAndGap(t *testing.T) {
	app, err := New(t.Context(), filepath.Join(t.TempDir(), "multica.db"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	workspaces, err := app.store.ListWorkspaces(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	ws := workspaces[0]
	issue, err := app.store.CreateIssue(t.Context(), ws.ID, CreateIssueInput{Title: "Continuity"})
	if err != nil {
		t.Fatal(err)
	}
	session, err := app.store.GetIssueSession(t.Context(), ws.ID, issue.ID)
	if err != nil {
		t.Fatal(err)
	}
	stamp := now()
	if _, err := app.store.db.ExecContext(t.Context(), `INSERT INTO task_runs(id, issue_id, status, session_id, stage_id, continuity_state, created_at, updated_at) VALUES ('run-1', ?, 'running', ?, ?, 'fresh', ?, ?)`, issue.ID, session.ID, session.ActiveStageID, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ id, prior, provider, want string }{
		{"run-1", "", "thread-1", "fresh"},
		{"run-2", "run-1", "thread-1", "resumed"},
		{"run-3", "run-2", "thread-3", "gap"},
		{"run-4", "run-3", "thread-3", "gap"},
	} {
		if tc.id != "run-1" {
			if _, err := app.store.db.ExecContext(t.Context(), `INSERT INTO task_runs(id, issue_id, status, session_id, stage_id, prior_task_id, continuity_state, created_at, updated_at) VALUES (?, ?, 'running', ?, ?, ?, 'pending', ?, ?)`, tc.id, issue.ID, session.ID, session.ActiveStageID, tc.prior, stamp, stamp); err != nil {
				t.Fatal(err)
			}
		}
		tx, err := app.store.db.BeginTx(t.Context(), nil)
		if err != nil {
			t.Fatal(err)
		}
		workDir := "/tmp/work"
		if tc.id == "run-4" {
			workDir = "/tmp/replaced-work"
		}
		if err := pinTaskSessionTx(t.Context(), tx, ws.ID, tc.id, tc.provider, workDir, false); err != nil {
			_ = tx.Rollback()
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
		var got string
		if err := app.store.db.QueryRowContext(t.Context(), `SELECT continuity_state FROM task_runs WHERE id=?`, tc.id).Scan(&got); err != nil && err != sql.ErrNoRows {
			t.Fatal(err)
		}
		if got != tc.want {
			t.Fatalf("%s continuity=%q want %q", tc.id, got, tc.want)
		}
		if _, err := app.store.db.ExecContext(t.Context(), `UPDATE task_runs SET status='completed' WHERE id=?`, tc.id); err != nil {
			t.Fatal(err)
		}
	}
}

func TestHandoffUsesTheSourceRunsExactStage(t *testing.T) {
	app, err := New(t.Context(), filepath.Join(t.TempDir(), "multica.db"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	ws, err := app.store.ListWorkspaces(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	parent, err := app.store.CreateIssue(t.Context(), ws[0].ID, CreateIssueInput{Title: "Parent"})
	if err != nil {
		t.Fatal(err)
	}
	initial, err := app.store.GetIssueSession(t.Context(), ws[0].ID, parent.ID)
	if err != nil {
		t.Fatal(err)
	}
	stamp := now()
	if _, err := app.store.db.ExecContext(t.Context(), `INSERT INTO task_runs(id, issue_id, status, session_id, stage_id, continuity_state, created_at, updated_at) VALUES ('source-run', ?, 'completed', ?, ?, 'fresh', ?, ?)`, parent.ID, initial.ID, initial.ActiveStageID, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	if _, err := app.store.ActivateIssueSessionStage(t.Context(), ws[0].ID, parent.ID, "review"); err != nil {
		t.Fatal(err)
	}
	child, err := app.store.CreateIssue(t.Context(), ws[0].ID, CreateIssueInput{
		Title: "Child", ParentIssueID: &parent.ID,
		Handoff: &IssueHandoffInput{Objective: "Use exact source", SourceRunID: "source-run"},
	})
	if err != nil {
		t.Fatal(err)
	}
	childSession, err := app.store.GetIssueSession(t.Context(), ws[0].ID, child.ID)
	if err != nil {
		t.Fatal(err)
	}
	if childSession.Handoff == nil || childSession.Handoff.SourceStageID != initial.ActiveStageID || childSession.Handoff.SourceStageKind != "analysis" {
		t.Fatalf("handoff source stage = %#v, want %s", childSession.Handoff, initial.ActiveStageID)
	}
}

func TestIssueSessionContinuationCommand(t *testing.T) {
	app, err := New(t.Context(), filepath.Join(t.TempDir(), "multica.db"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	server := httptest.NewServer(app.Router())
	defer server.Close()
	workspaces, err := app.store.ListWorkspaces(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	ws := workspaces[0]
	stamp := now()
	if _, err := app.store.db.ExecContext(t.Context(), `INSERT INTO runtimes(id, workspace_id, daemon_id, name, provider, status, created_at, updated_at) VALUES ('runtime-continue', ?, 'daemon-continue', 'Codex', 'codex', 'online', ?, ?)`, ws.ID, stamp, stamp); err != nil {
		t.Fatal(err)
	}

	var issue Issue
	requestJSON(t, server.URL, http.MethodPost, "/api/issues", map[string]any{"title": "Continue me"}, http.StatusCreated, &issue)
	var sessionBefore IssueSession
	requestJSON(t, server.URL, http.MethodGet, "/api/issues/"+issue.ID+"/session", nil, http.StatusOK, &sessionBefore)
	var first struct {
		Outcome string         `json:"outcome"`
		Issue   Issue          `json:"issue"`
		Task    map[string]any `json:"task"`
	}
	requestJSON(t, server.URL, http.MethodPost, "/api/issues/"+issue.ID+"/session/continue", map[string]any{}, http.StatusCreated, &first)
	if first.Outcome != "queued" || first.Issue.Status != "in_progress" || first.Issue.AssigneeID == nil || *first.Issue.AssigneeID != LocalAgentID {
		t.Fatalf("first continuation = %#v", first)
	}
	firstTaskID, _ := first.Task["id"].(string)
	if firstTaskID == "" || first.Task["stage_id"] != sessionBefore.ActiveStageID || first.Task["stage_kind"] != "analysis" {
		t.Fatalf("first continuation task = %#v", first.Task)
	}

	var active struct {
		Outcome string         `json:"outcome"`
		Issue   Issue          `json:"issue"`
		Task    map[string]any `json:"task"`
	}
	requestJSON(t, server.URL, http.MethodPost, "/api/issues/"+issue.ID+"/session/continue", map[string]any{}, http.StatusOK, &active)
	if active.Outcome != "active" || active.Task["id"] != firstTaskID {
		t.Fatalf("active continuation = %#v", active)
	}
	var count int
	if err := app.store.db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM task_runs WHERE issue_id=?`, issue.ID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("active continuation queued %d runs, want 1", count)
	}

	if _, err := app.store.db.ExecContext(t.Context(), `UPDATE task_runs SET status='completed', thread_id='thread-1', work_dir='/tmp/work-1', completed_at=?, updated_at=? WHERE id=?`, now(), now(), firstTaskID); err != nil {
		t.Fatal(err)
	}
	var implementation IssueSession
	requestJSON(t, server.URL, http.MethodPost, "/api/issues/"+issue.ID+"/session/stages", map[string]any{"kind": "implementation"}, http.StatusOK, &implementation)
	var resumed struct {
		Outcome string         `json:"outcome"`
		Issue   Issue          `json:"issue"`
		Task    map[string]any `json:"task"`
	}
	requestJSON(t, server.URL, http.MethodPost, "/api/issues/"+issue.ID+"/session/continue", map[string]any{}, http.StatusCreated, &resumed)
	if resumed.Task["stage_id"] != implementation.ActiveStageID || resumed.Task["stage_kind"] != "implementation" || resumed.Task["prior_task_id"] != firstTaskID || resumed.Task["continuity_state"] != "pending" {
		t.Fatalf("resumed continuation = %#v", resumed.Task)
	}
	var sessionAfter IssueSession
	requestJSON(t, server.URL, http.MethodGet, "/api/issues/"+issue.ID+"/session", nil, http.StatusOK, &sessionAfter)
	if sessionAfter.ActiveStageID != implementation.ActiveStageID {
		t.Fatalf("continuation changed active stage: got %s want %s", sessionAfter.ActiveStageID, implementation.ActiveStageID)
	}
}

func TestIssueSessionContinuationIsConcurrentIdempotent(t *testing.T) {
	app, err := New(t.Context(), filepath.Join(t.TempDir(), "multica.db"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	workspaces, err := app.store.ListWorkspaces(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	ws := workspaces[0]
	stamp := now()
	if _, err := app.store.db.ExecContext(t.Context(), `INSERT INTO runtimes(id, workspace_id, daemon_id, name, provider, status, created_at, updated_at) VALUES ('runtime-concurrent', ?, 'daemon-concurrent', 'Codex', 'codex', 'online', ?, ?)`, ws.ID, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	issue, err := app.store.CreateIssue(t.Context(), ws.ID, CreateIssueInput{Title: "Continue concurrently"})
	if err != nil {
		t.Fatal(err)
	}

	type result struct {
		taskID  string
		outcome string
		err     error
	}
	results := make(chan result, 2)
	var start sync.WaitGroup
	start.Add(1)
	for range 2 {
		go func() {
			start.Wait()
			_, taskID, outcome, err := app.store.ContinueIssueSession(t.Context(), ws.ID, issue.ID)
			results <- result{taskID: taskID, outcome: outcome, err: err}
		}()
	}
	start.Done()
	first, second := <-results, <-results
	for _, got := range []result{first, second} {
		if got.err != nil {
			t.Fatal(got.err)
		}
	}
	if first.taskID != second.taskID {
		t.Fatalf("concurrent continuation returned tasks %q and %q", first.taskID, second.taskID)
	}
	if !((first.outcome == "queued" && second.outcome == "active") || (first.outcome == "active" && second.outcome == "queued")) {
		t.Fatalf("concurrent outcomes = %q, %q", first.outcome, second.outcome)
	}
	var count int
	if err := app.store.db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM task_runs WHERE issue_id=?`, issue.ID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("concurrent continuation queued %d runs, want 1", count)
	}
}

func TestIssueSessionContinuationRejectsUnrunnableIssueWithoutPartialChange(t *testing.T) {
	for _, tc := range []struct {
		name       string
		input      map[string]any
		addRuntime bool
	}{
		{name: "runtime offline", input: map[string]any{"title": "Offline"}},
		{name: "foreign assignee", input: map[string]any{"title": "Owned", "assignee_type": "member", "assignee_id": "member-1"}, addRuntime: true},
		{name: "unassigned non backlog", input: map[string]any{"title": "Unassigned todo", "status": "todo"}, addRuntime: true},
		{name: "finished", input: map[string]any{"title": "Done", "status": "done"}, addRuntime: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			app, err := New(t.Context(), filepath.Join(t.TempDir(), "multica.db"), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer app.Close()
			server := httptest.NewServer(app.Router())
			defer server.Close()
			workspaces, err := app.store.ListWorkspaces(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if tc.addRuntime {
				if _, err := app.store.db.ExecContext(t.Context(), `INSERT INTO runtimes(id, workspace_id, daemon_id, name, provider, status, created_at, updated_at) VALUES ('runtime-1', ?, 'daemon-1', 'Codex', 'codex', 'online', ?, ?)`, workspaces[0].ID, now(), now()); err != nil {
					t.Fatal(err)
				}
			}
			var issue Issue
			requestJSON(t, server.URL, http.MethodPost, "/api/issues", tc.input, http.StatusCreated, &issue)
			var failure map[string]any
			requestJSON(t, server.URL, http.MethodPost, "/api/issues/"+issue.ID+"/session/continue", map[string]any{}, http.StatusConflict, &failure)
			var after Issue
			requestJSON(t, server.URL, http.MethodGet, "/api/issues/"+issue.ID, nil, http.StatusOK, &after)
			sameOptional := func(left, right *string) bool {
				return left == nil && right == nil || left != nil && right != nil && *left == *right
			}
			if after.Status != issue.Status || !sameOptional(after.AssigneeType, issue.AssigneeType) || !sameOptional(after.AssigneeID, issue.AssigneeID) {
				t.Fatalf("rejected continuation changed issue: before=%#v after=%#v", issue, after)
			}
			var runs int
			if err := app.store.db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM task_runs WHERE issue_id=?`, issue.ID).Scan(&runs); err != nil {
				t.Fatal(err)
			}
			if runs != 0 {
				t.Fatalf("rejected continuation queued %d runs", runs)
			}
		})
	}
}

func TestQueuedRunUsesLatestUsableResumePointer(t *testing.T) {
	app, err := New(t.Context(), filepath.Join(t.TempDir(), "multica.db"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	workspaces, err := app.store.ListWorkspaces(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	agentType, agentID := "agent", LocalAgentID
	issue, err := app.store.CreateIssue(t.Context(), workspaces[0].ID, CreateIssueInput{Title: "Resume fallback", AssigneeType: &agentType, AssigneeID: &agentID})
	if err != nil {
		t.Fatal(err)
	}
	session, err := app.store.GetIssueSession(t.Context(), workspaces[0].ID, issue.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.store.db.ExecContext(t.Context(), `INSERT INTO task_runs(id, issue_id, status, thread_id, work_dir, session_id, stage_id, continuity_state, created_at, updated_at) VALUES ('usable-run', ?, 'completed', 'thread-1', '/tmp/work-1', ?, ?, 'fresh', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z'), ('missing-run', ?, 'failed', NULL, NULL, ?, ?, 'gap', '2026-01-02T00:00:00Z', '2026-01-02T00:00:00Z')`, issue.ID, session.ID, session.ActiveStageID, issue.ID, session.ID, session.ActiveStageID); err != nil {
		t.Fatal(err)
	}
	if _, err := app.store.db.ExecContext(t.Context(), `INSERT INTO runtimes(id, workspace_id, daemon_id, name, provider, status, created_at, updated_at) VALUES ('runtime-1', ?, 'daemon-1', 'Codex', 'codex', 'online', ?, ?)`, workspaces[0].ID, now(), now()); err != nil {
		t.Fatal(err)
	}
	if err := app.store.enqueueAssignedIssue(t.Context(), workspaces[0].ID, issue.ID, ""); err != nil {
		t.Fatal(err)
	}
	var priorTaskID, state string
	if err := app.store.db.QueryRowContext(t.Context(), `SELECT prior_task_id, continuity_state FROM task_runs WHERE status='queued' AND issue_id=?`, issue.ID).Scan(&priorTaskID, &state); err != nil {
		t.Fatal(err)
	}
	if priorTaskID != "usable-run" || state != "pending" {
		t.Fatalf("queued prior=%q state=%q", priorTaskID, state)
	}
}

func TestTerminalRunWithoutReportedSessionBecomesGap(t *testing.T) {
	app, err := New(t.Context(), filepath.Join(t.TempDir(), "multica.db"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	workspaces, err := app.store.ListWorkspaces(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	issue, err := app.store.CreateIssue(t.Context(), workspaces[0].ID, CreateIssueInput{Title: "Terminal gap"})
	if err != nil {
		t.Fatal(err)
	}
	session, err := app.store.GetIssueSession(t.Context(), workspaces[0].ID, issue.ID)
	if err != nil {
		t.Fatal(err)
	}
	stamp := now()
	if _, err := app.store.db.ExecContext(t.Context(), `INSERT INTO task_runs(id, issue_id, status, thread_id, work_dir, session_id, stage_id, prior_task_id, continuity_state, created_at, updated_at) VALUES ('prior', ?, 'completed', 'thread-1', '/tmp/work-1', ?, ?, NULL, 'fresh', ?, ?), ('current', ?, 'running', NULL, NULL, ?, ?, 'prior', 'pending', ?, ?)`, issue.ID, session.ID, session.ActiveStageID, stamp, stamp, issue.ID, session.ID, session.ActiveStageID, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	tx, err := app.store.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := pinTaskSessionTx(t.Context(), tx, workspaces[0].ID, "current", "", "", true); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var state, reason string
	if err := app.store.db.QueryRowContext(t.Context(), `SELECT continuity_state, continuity_reason FROM task_runs WHERE id='current'`).Scan(&state, &reason); err != nil {
		t.Fatal(err)
	}
	if state != "gap" || reason != "provider_session_unreported" {
		t.Fatalf("terminal continuity=%q reason=%q", state, reason)
	}
}

func TestDaemonClaimCarriesIssueSessionContinuityAndHandoff(t *testing.T) {
	app, err := New(t.Context(), filepath.Join(t.TempDir(), "multica.db"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	server := httptest.NewServer(app.Router())
	defer server.Close()
	workspaces, err := app.store.ListWorkspaces(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var registration struct {
		Runtimes []localRuntime `json:"runtimes"`
	}
	requestJSON(t, server.URL, http.MethodPost, "/api/daemon/register", map[string]any{
		"workspace_id": workspaces[0].ID, "daemon_id": "local-daemon", "device_name": "Mac",
		"runtimes": []any{map[string]any{"name": "Codex", "type": "codex", "version": "1.0", "status": "online"}},
	}, http.StatusOK, &registration)
	var parent Issue
	requestJSON(t, server.URL, http.MethodPost, "/api/issues", map[string]any{"title": "Parent"}, http.StatusCreated, &parent)
	var child Issue
	requestJSON(t, server.URL, http.MethodPost, "/api/issues", map[string]any{
		"title": "Child", "parent_issue_id": parent.ID, "assignee_type": "agent", "assignee_id": LocalAgentID,
		"handoff": map[string]any{"objective": "Carry this objective", "constraints": []string{"Stay local"}},
	}, http.StatusCreated, &child)
	claim := struct {
		Tasks []map[string]any `json:"tasks"`
	}{}
	requestJSON(t, server.URL, http.MethodPost, "/api/daemon/tasks/claim", map[string]any{
		"daemon_id": "local-daemon", "runtime_ids": []string{registration.Runtimes[0].ID}, "max_tasks": 1,
	}, http.StatusOK, &claim)
	if len(claim.Tasks) != 1 || claim.Tasks[0]["issue_session_id"] == "" || claim.Tasks[0]["issue_session_stage_id"] == "" || claim.Tasks[0]["issue_session_stage_kind"] != "analysis" {
		t.Fatalf("first claim session context = %#v", claim.Tasks)
	}
	handoff, ok := claim.Tasks[0]["issue_handoff"].(map[string]any)
	if !ok || handoff["objective"] != "Carry this objective" {
		t.Fatalf("first claim handoff = %#v", claim.Tasks[0]["issue_handoff"])
	}
	firstTaskID := claim.Tasks[0]["id"].(string)
	var empty map[string]any
	requestJSON(t, server.URL, http.MethodPost, "/api/daemon/tasks/"+firstTaskID+"/session", map[string]any{"session_id": "provider-thread-1", "work_dir": "/tmp/work-1"}, http.StatusOK, &empty)
	requestJSON(t, server.URL, http.MethodPost, "/api/daemon/tasks/"+firstTaskID+"/complete", map[string]any{"session_id": "provider-thread-1", "work_dir": "/tmp/work-1", "output": "done"}, http.StatusOK, &empty)
	if err := app.store.enqueueAssignedIssue(t.Context(), workspaces[0].ID, child.ID, ""); err != nil {
		t.Fatal(err)
	}
	claim.Tasks = nil
	requestJSON(t, server.URL, http.MethodPost, "/api/daemon/tasks/claim", map[string]any{
		"daemon_id": "local-daemon", "runtime_ids": []string{registration.Runtimes[0].ID}, "max_tasks": 1,
	}, http.StatusOK, &claim)
	if len(claim.Tasks) != 1 || claim.Tasks[0]["prior_session_id"] != "provider-thread-1" || claim.Tasks[0]["prior_work_dir"] != "/tmp/work-1" || claim.Tasks[0]["continuity_state"] != "pending" {
		t.Fatalf("resumed claim = %#v", claim.Tasks)
	}
}
