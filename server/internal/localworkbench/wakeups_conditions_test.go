package localworkbench

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/service"
)

func localConditionFixture(t *testing.T) (*Store, Project, Issue) {
	t.Helper()
	store, err := Open(t.Context(), filepath.Join(t.TempDir(), "condition.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	})
	project, err := store.CreateProject(t.Context(), LocalWorkspaceID, CreateProjectInput{Title: "Wakeup conditions"})
	if err != nil {
		t.Fatal(err)
	}
	issue, err := store.CreateIssue(t.Context(), LocalWorkspaceID, CreateIssueInput{Title: "Watch this issue", ProjectID: &project.ID, Status: "todo"})
	if err != nil {
		t.Fatal(err)
	}
	return store, project, issue
}

func localConditionJSON(t *testing.T, value any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func localConditionExec(t *testing.T, store *Store, query string, args ...any) {
	t.Helper()
	if _, err := store.db.ExecContext(t.Context(), query, args...); err != nil {
		t.Fatal(err)
	}
}

func TestLocalWakeupConditionValidationAndScope(t *testing.T) {
	store, project, issue := localConditionFixture(t)
	other, err := store.CreateIssue(t.Context(), LocalWorkspaceID, CreateIssueInput{Title: "Other", ProjectID: &project.ID, Status: "todo"})
	if err != nil {
		t.Fatal(err)
	}
	property, foreignProperty, label, foreignLabel := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	stamp, foreignWorkspace := now(), uuid.NewString()
	for _, item := range []struct{ id, ws string }{{property, LocalWorkspaceID}, {foreignProperty, foreignWorkspace}} {
		localConditionExec(t, store, `INSERT INTO issue_properties(id,workspace_id,name,type,created_at,updated_at) VALUES(?,?,'Ready','boolean',?,?)`, item.id, item.ws, stamp, stamp)
	}
	for _, item := range []struct{ id, ws string }{{label, LocalWorkspaceID}, {foreignLabel, foreignWorkspace}} {
		localConditionExec(t, store, `INSERT INTO labels(id,workspace_id,resource_type,name,description,color,created_at,updated_at) VALUES(?,?,'issue','Ready','','#000000',?,?)`, item.id, item.ws, stamp, stamp)
	}
	cases := []struct {
		name  string
		input any
		bad   bool
	}{
		{"status", map[string]any{"type": "issue_field", "field": "status", "value": "todo"}, false},
		{"unknown status", map[string]any{"type": "issue_field", "field": "status", "value": "not-real"}, true},
		{"member", map[string]any{"type": "issue_field", "field": "assignee", "assignee_type": "member", "assignee_id": LocalOperatorID}, false},
		{"foreign member", map[string]any{"type": "issue_field", "field": "assignee", "assignee_type": "member", "assignee_id": uuid.NewString()}, true},
		{"invalid actor uuid", map[string]any{"type": "issue_field", "field": "assignee", "assignee_type": "agent", "assignee_id": "bad"}, true},
		{"label", map[string]any{"type": "issue_field", "field": "label", "label_id": label}, false},
		{"foreign label", map[string]any{"type": "issue_field", "field": "label", "label_id": foreignLabel}, true},
		{"property", map[string]any{"type": "issue_field", "field": "property", "property_id": property, "value": true}, false},
		{"foreign property", map[string]any{"type": "issue_field", "field": "property", "property_id": foreignProperty, "value": true}, true},
		{"null property", map[string]any{"type": "issue_field", "field": "property", "property_id": property, "value": nil}, true},
		{"large property", map[string]any{"type": "issue_field", "field": "property", "property_id": property, "value": strings.Repeat("x", 1025)}, true},
		{"children", map[string]any{"type": "children_done", "stage": 2}, false},
		{"zero stage", map[string]any{"type": "children_done", "stage": 0}, true},
		{"reserved each stage", map[string]any{"type": "children_done", "each_stage": true}, true},
		{"pull request", map[string]any{"type": "pull_request", "event": "checks_finished"}, false},
		{"invalid pr event", map[string]any{"type": "pull_request", "event": "opened"}, true},
		{"other issue", map[string]any{"type": "other_issue", "issue_id": other.ID, "state": "ended", "identifier": "untrusted"}, false},
		{"self issue", map[string]any{"type": "other_issue", "issue_id": issue.ID, "state": "done"}, true},
		{"foreign issue", map[string]any{"type": "other_issue", "issue_id": uuid.NewString(), "state": "done"}, true},
		{"unknown field", map[string]any{"type": "children_done", "surprise": true}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			normalized, err := validateLocalWakeupCondition(t.Context(), store.db, LocalWorkspaceID, issue.ID, localConditionJSON(t, tc.input))
			if tc.bad {
				if !errors.Is(err, service.ErrWakeupInput) {
					t.Fatalf("error=%v; want Wakeup input rejection", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if tc.name == "other issue" {
				var condition service.WakeupCondition
				if err := json.Unmarshal(normalized, &condition); err != nil {
					t.Fatal(err)
				}
				if condition.Identifier != other.Identifier {
					t.Fatalf("identifier=%q; want authoritative %q", condition.Identifier, other.Identifier)
				}
			}
		})
	}
	if _, err := validateLocalWakeupCondition(t.Context(), store.db, foreignWorkspace, issue.ID, json.RawMessage(`{"type":"children_done"}`)); !errors.Is(err, service.ErrWakeupInput) {
		t.Fatalf("foreign owner issue accepted: %v", err)
	}
	if _, err := validateLocalWakeupCondition(t.Context(), store.db, LocalWorkspaceID, issue.ID, json.RawMessage(`{"type":"children_done"} {}`)); !errors.Is(err, service.ErrWakeupInput) {
		t.Fatalf("trailing JSON accepted: %v", err)
	}
}

func TestLocalWakeupConditionFieldsAndTransaction(t *testing.T) {
	store, project, issue := localConditionFixture(t)
	property, label, largeProperty := uuid.NewString(), uuid.NewString(), uuid.NewString()
	localConditionExec(t, store, `UPDATE issues SET properties=?,assignee_type='member',assignee_id=? WHERE id=?`, `{"`+property+`":{"a":1,"b":2},"`+largeProperty+`":9007199254740992}`, LocalOperatorID, issue.ID)
	localConditionExec(t, store, `INSERT INTO resource_labels(workspace_id,resource_type,resource_id,label_id,created_at) VALUES(?,'issue',?,?,?)`, LocalWorkspaceID, issue.ID, label, now())
	other, err := store.CreateIssue(t.Context(), LocalWorkspaceID, CreateIssueInput{Title: "Ended", ProjectID: &project.ID, Status: "cancelled"})
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name  string
		input any
		want  bool
	}{
		{"status matches", map[string]any{"type": "issue_field", "field": "status", "value": "todo"}, true},
		{"status differs", map[string]any{"type": "issue_field", "field": "status", "value": "done"}, false},
		{"assignee matches", map[string]any{"type": "issue_field", "field": "assignee", "assignee_type": "member", "assignee_id": LocalOperatorID}, true},
		{"assignee type matters", map[string]any{"type": "issue_field", "field": "assignee", "assignee_type": "agent", "assignee_id": LocalOperatorID}, false},
		{"label attached", map[string]any{"type": "issue_field", "field": "label", "label_id": label}, true},
		{"label missing", map[string]any{"type": "issue_field", "field": "label", "label_id": uuid.NewString()}, false},
		{"canonical property", map[string]any{"type": "issue_field", "field": "property", "property_id": property, "value": json.RawMessage(`{"b":2,"a":1}`)}, true},
		{"property differs", map[string]any{"type": "issue_field", "field": "property", "property_id": property, "value": false}, false},
		{"large numbers preserve exact identity", map[string]any{"type": "issue_field", "field": "property", "property_id": largeProperty, "value": json.RawMessage(`9007199254740993`)}, false},
		{"other ended", map[string]any{"type": "other_issue", "issue_id": other.ID, "state": "ended"}, true},
		{"other not done", map[string]any{"type": "other_issue", "issue_id": other.ID, "state": "done"}, false},
		{"other deleted", map[string]any{"type": "other_issue", "issue_id": uuid.NewString(), "state": "done"}, false},
	}
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := tx.Rollback(); err != nil {
			t.Error(err)
		}
	}()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			met, evidence, err := evaluateLocalWakeupCondition(ctx, tx, LocalWorkspaceID, issue.ID, localConditionJSON(t, tc.input))
			if err != nil || met != tc.want || (met && evidence == "") {
				t.Fatalf("met=%v evidence=%q err=%v; want met=%v", met, evidence, err, tc.want)
			}
		})
	}
	if _, _, err := evaluateLocalWakeupCondition(ctx, tx, uuid.NewString(), issue.ID, json.RawMessage(`{"type":"children_done"}`)); err == nil {
		t.Fatal("foreign issue evaluated outside its workspace")
	}
}

func TestLocalWakeupConditionChildStages(t *testing.T) {
	store, project, parent := localConditionFixture(t)
	all := json.RawMessage(`{"type":"children_done"}`)
	check := func(name string, raw json.RawMessage, want bool, expected string) {
		t.Helper()
		met, fingerprint, err := evaluateLocalWakeupCondition(t.Context(), store.db, LocalWorkspaceID, parent.ID, raw)
		if err != nil || met != want || (want && fingerprint != expected) {
			t.Fatalf("%s: met=%v evidence=%q err=%v; want %v %q", name, met, fingerprint, err, want, expected)
		}
	}
	check("empty children do not complete", all, false, "")
	firstStage, secondStage := int64(1), int64(2)
	createChild := func(name, status string, stage *int64) Issue {
		t.Helper()
		issue, err := store.CreateIssue(t.Context(), LocalWorkspaceID, CreateIssueInput{Title: name, ProjectID: &project.ID, ParentIssueID: &parent.ID, Stage: stage, Status: status})
		if err != nil {
			t.Fatal(err)
		}
		return issue
	}
	first := createChild("First", "todo", &firstStage)
	second := createChild("Second", "done", &secondStage)
	unstaged := createChild("Unstaged", "todo", nil)
	check("earlier stage still open", json.RawMessage(`{"type":"children_done","stage":2}`), false, "")
	localConditionExec(t, store, `UPDATE issues SET status='cancelled' WHERE id=?`, first.ID)
	check("closed is finished", json.RawMessage(`{"type":"children_done","stage":2}`), true, "children:2")
	check("stage must exist", json.RawMessage(`{"type":"children_done","stage":3}`), false, "")
	check("unstaged blocks all", all, false, "")
	localConditionExec(t, store, `UPDATE issues SET status='todo' WHERE id=?`, second.ID)
	check("platform closes one stage", json.RawMessage(`{"type":"children_done","each_stage":true}`), true, "stage:1")
	localConditionExec(t, store, `UPDATE issues SET status='done' WHERE id IN (?,?)`, second.ID, unstaged.ID)
	check("platform closes all", json.RawMessage(`{"type":"children_done","each_stage":true}`), true, "all")
	check("all children completed", all, true, "children:3")
}

func TestLocalWakeupConditionPullRequests(t *testing.T) {
	store, _, issue := localConditionFixture(t)
	connection, id := uuid.NewString(), uuid.NewString()
	stamp := now()
	check := func(name, event string, want bool, wantErr error) string {
		t.Helper()
		raw := localConditionJSON(t, map[string]any{"type": "pull_request", "event": event})
		met, evidence, err := evaluateLocalWakeupCondition(t.Context(), store.db, LocalWorkspaceID, issue.ID, raw)
		if met != want || !errors.Is(err, wantErr) {
			t.Fatalf("%s: met=%v evidence=%q err=%v; want met=%v err=%v", name, met, evidence, err, want, wantErr)
		}
		return evidence
	}
	check("unlinked", "checks_finished", false, errLocalWakeupPRNotLinked)
	localConditionExec(t, store, `INSERT INTO vcs_pull_requests(id,workspace_id,connection_id,provider,repo_owner,repo_name,pr_number,title,state,html_url,head_sha,pr_created_at,pr_updated_at) VALUES(?,?,?,'gitlab','owner','repo',1,'Fix','open','https://vcs.invalid/pr/1','head',?,?)`, id, LocalWorkspaceID, connection, stamp, stamp)
	localConditionExec(t, store, `INSERT INTO issue_vcs_pull_requests(issue_id,pull_request_id,created_at) VALUES(?,?,?)`, issue.ID, id, stamp)
	check("no statuses", "checks_finished", false, nil)
	check("self hosted cannot prove conflicts", "conflicts", false, errLocalWakeupPRConflictsUnsupported)
	localConditionExec(t, store, `INSERT INTO vcs_commit_statuses(connection_id,sha,context,state,updated_at) VALUES(?,'head','ci','pending',?)`, connection, stamp)
	check("pending", "checks_finished", false, nil)
	localConditionExec(t, store, `UPDATE vcs_commit_statuses SET state='passed' WHERE connection_id=?`, connection)
	firstEvidence := check("passed", "checks_finished", true, nil)
	if !strings.Contains(firstEvidence, id+"@head:SUCCESS") {
		t.Fatalf("evidence omits PR identity/head/verdict: %q", firstEvidence)
	}
	localConditionExec(t, store, `UPDATE vcs_commit_statuses SET state='failed' WHERE connection_id=?`, connection)
	if check("failed is finished", "checks_finished", true, nil) == firstEvidence {
		t.Fatal("changed check verdict did not change evidence")
	}
	localConditionExec(t, store, `UPDATE vcs_pull_requests SET head_sha='new-head' WHERE id=?`, id)
	check("old head statuses do not prove current checks", "checks_finished", false, nil)
	localConditionExec(t, store, `UPDATE vcs_pull_requests SET provider='github' WHERE id=?`, id)
	check("GitHub snapshot missing", "checks_finished", false, errLocalWakeupPRSnapshotPending)
	localConditionExec(t, store, `UPDATE vcs_pull_requests SET snapshot_head_sha='head',checks_rollup_state='SUCCESS',api_mergeable='CONFLICTING' WHERE id=?`, id)
	check("old snapshot", "conflicts", false, errLocalWakeupPRSnapshotPending)
	localConditionExec(t, store, `UPDATE vcs_pull_requests SET snapshot_head_sha='new-head' WHERE id=?`, id)
	check("GitHub checks", "checks_finished", true, nil)
	conflictEvidence := check("GitHub conflicts", "conflicts", true, nil)
	if !strings.Contains(conflictEvidence, id+"@new-head:conflicting") {
		t.Fatalf("conflict evidence=%q", conflictEvidence)
	}
	localConditionExec(t, store, `UPDATE vcs_pull_requests SET api_mergeable='UNKNOWN' WHERE id=?`, id)
	check("recomputing mergeability retains pending state", "conflicts", false, errLocalWakeupPRMergeabilityPending)
	localConditionExec(t, store, `UPDATE vcs_pull_requests SET api_mergeable='MERGEABLE' WHERE id=?`, id)
	check("mergeable", "conflicts", false, nil)
	localConditionExec(t, store, `UPDATE vcs_pull_requests SET state='merged',api_mergeable='CONFLICTING' WHERE id=?`, id)
	check("merged PR is no longer conflicting", "conflicts", false, nil)
	check("merged", "merged", true, nil)
	otherWS := uuid.NewString()
	localConditionExec(t, store, `UPDATE vcs_pull_requests SET workspace_id=? WHERE id=?`, otherWS, id)
	check("foreign PR does not satisfy local issue", "merged", false, errLocalWakeupPRNotLinked)
}
