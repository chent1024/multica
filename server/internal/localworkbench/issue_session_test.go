package localworkbench

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

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
	if childSession.Handoff == nil || childSession.Handoff.ParentIssueID != parent.ID || childSession.Handoff.SourceStageID != implementation.ActiveStageID {
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
	if childSession.Handoff == nil || childSession.Handoff.SourceStageID != initial.ActiveStageID {
		t.Fatalf("handoff source stage = %#v, want %s", childSession.Handoff, initial.ActiveStageID)
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
