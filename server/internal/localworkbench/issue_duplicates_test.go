package localworkbench

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

type localDuplicateFixture struct {
	server    *Server
	config    ServerConfig
	projectID string
}

func newLocalDuplicateFixture(t *testing.T) *localDuplicateFixture {
	t.Helper()
	root := t.TempDir()
	f := &localDuplicateFixture{config: ServerConfig{DatabasePath: filepath.Join(root, "db.sqlite"), FileRoot: filepath.Join(root, "files")}}
	f.open(t)
	t.Cleanup(func() {
		if err := f.server.Close(); err != nil {
			t.Fatal(err)
		}
	})
	response := localRequest(t, f.server.Router(), http.MethodPost, "/api/projects", map[string]any{"title": "Duplicate test project"})
	requireStatus(t, response, http.StatusCreated)
	f.projectID = decodeResponse[Project](t, response).ID
	return f
}

func (f *localDuplicateFixture) open(t *testing.T) {
	t.Helper()
	s, err := NewServer(context.Background(), f.config)
	if err != nil {
		t.Fatal(err)
	}
	s.cancel()
	s.wg.Wait()
	f.server = s
}
func (f *localDuplicateFixture) issue(t *testing.T, title, status string) Issue {
	t.Helper()
	response := localRequest(t, f.server.Router(), http.MethodPost, "/api/issues", map[string]any{"title": title, "status": status, "project_id": f.projectID})
	requireStatus(t, response, http.StatusCreated)
	return decodeResponse[Issue](t, response)
}
func (f *localDuplicateFixture) mark(t *testing.T, id, target string) Issue {
	t.Helper()
	response := localRequest(t, f.server.Router(), http.MethodPut, "/api/issues/"+id, map[string]any{"duplicate_of_issue_id": target})
	requireStatus(t, response, http.StatusOK)
	return decodeResponse[Issue](t, response)
}
func (f *localDuplicateFixture) relations(t *testing.T, id string) (*Issue, []Issue) {
	t.Helper()
	response := localRequest(t, f.server.Router(), http.MethodGet, "/api/issues/"+id+"/duplicates", nil)
	requireStatus(t, response, http.StatusOK)
	v := decodeResponse[struct {
		Original   *Issue  `json:"duplicate_of"`
		Duplicates []Issue `json:"duplicates"`
	}](t, response)
	return v.Original, v.Duplicates
}

func TestLocalIssueDuplicateLifecyclePersistsAndClears(t *testing.T) {
	f := newLocalDuplicateFixture(t)
	original := f.issue(t, "Original", "in_progress")
	duplicate := f.issue(t, "Duplicate", "todo")
	marked := f.mark(t, duplicate.ID, original.ID)
	if marked.Status != "cancelled" || marked.DuplicateOf == nil || marked.DuplicateOf.ID != original.ID || marked.DuplicateOf.Title != "Original" || marked.DuplicateOf.Status != "in_progress" {
		t.Fatalf("marked=%+v", marked)
	}
	originalAfter, err := f.server.store.GetIssue(t.Context(), LocalWorkspaceID, original.ID)
	if err != nil {
		t.Fatal(err)
	}
	if originalAfter.Status != original.Status || originalAfter.Revision != original.Revision {
		t.Fatalf("mark mutated original=%+v", originalAfter)
	}
	same := f.mark(t, duplicate.ID, original.ID)
	if same.Revision != marked.Revision {
		t.Fatalf("same mark revision=%d want=%d", same.Revision, marked.Revision)
	}
	if target, _ := f.relations(t, duplicate.ID); target == nil || target.ID != original.ID {
		t.Fatalf("original relation=%+v", target)
	}
	if target, children := f.relations(t, original.ID); target != nil || len(children) != 1 || children[0].ID != duplicate.ID {
		t.Fatalf("reverse relation=%+v %v", target, children)
	}
	if err = f.server.Close(); err != nil {
		t.Fatal(err)
	}
	f.open(t)
	restored, err := f.server.store.GetIssue(t.Context(), LocalWorkspaceID, duplicate.ID)
	if err != nil {
		t.Fatal(err)
	}
	if restored.DuplicateOf == nil || restored.DuplicateOf.ID != original.ID || restored.Status != "cancelled" {
		t.Fatalf("restored=%+v", restored)
	}
	renamed := localRequest(t, f.server.Router(), http.MethodPut, "/api/issues/"+duplicate.ID, map[string]any{"title": "Duplicate renamed"})
	requireStatus(t, renamed, http.StatusOK)
	if issue := decodeResponse[Issue](t, renamed); issue.DuplicateOf == nil {
		t.Fatal("non-status update cleared mark")
	}
	reopened := localRequest(t, f.server.Router(), http.MethodPost, "/api/issues/batch-update", map[string]any{"issue_ids": []string{duplicate.ID}, "updates": map[string]any{"status": "todo"}})
	requireStatus(t, reopened, http.StatusOK)
	var pointer sql.NullString
	if err = f.server.store.db.QueryRow(`SELECT duplicate_of_issue_id FROM issues WHERE id=?`, duplicate.ID).Scan(&pointer); err != nil {
		t.Fatal(err)
	}
	if pointer.Valid {
		t.Fatal("reopening left stored pointer")
	}
	if target, children := f.relations(t, original.ID); target != nil || len(children) != 0 {
		t.Fatalf("original after reopen=%+v %v", target, children)
	}
	cancelled := localRequest(t, f.server.Router(), http.MethodPut, "/api/issues/"+duplicate.ID, map[string]any{"status": "cancelled"})
	requireStatus(t, cancelled, http.StatusOK)
	if value := decodeResponse[Issue](t, cancelled); value.DuplicateOf != nil {
		t.Fatal("cancel again resurrected mark")
	}
	f.mark(t, duplicate.ID, original.ID)
	deleted := localRequest(t, f.server.Router(), http.MethodDelete, "/api/issues/"+original.ID, nil)
	requireStatus(t, deleted, http.StatusNoContent)
	survivor, err := f.server.store.GetIssue(t.Context(), LocalWorkspaceID, duplicate.ID)
	if err != nil {
		t.Fatal(err)
	}
	if survivor.Status != "cancelled" || survivor.DuplicateOf != nil || survivor.DuplicateOfIssueID != nil {
		t.Fatalf("survivor=%+v", survivor)
	}
	if err = f.server.store.db.QueryRow(`SELECT duplicate_of_issue_id FROM issues WHERE id=?`, duplicate.ID).Scan(&pointer); err != nil {
		t.Fatal(err)
	}
	if pointer.Valid {
		t.Fatal("original deletion left stored pointer")
	}
}

func TestLocalIssueDuplicateRejectsInvalidGraphsAndBodies(t *testing.T) {
	f := newLocalDuplicateFixture(t)
	original := f.issue(t, "Original", "todo")
	marked := f.issue(t, "Marked", "todo")
	other := f.issue(t, "Other", "todo")
	f.mark(t, marked.ID, original.ID)
	tests := []struct {
		name   string
		id     string
		body   map[string]any
		status int
		code   string
	}{
		{"self", other.ID, map[string]any{"duplicate_of_issue_id": other.ID}, 400, ""},
		{"target missing", other.ID, map[string]any{"duplicate_of_issue_id": uuid.NewString()}, 400, ""},
		{"target duplicate", other.ID, map[string]any{"duplicate_of_issue_id": marked.ID}, 409, "duplicate_target_is_duplicate"},
		{"source has duplicates", original.ID, map[string]any{"duplicate_of_issue_id": other.ID}, 409, "issue_has_duplicates"},
		{"null", other.ID, map[string]any{"duplicate_of_issue_id": nil}, 400, ""},
		{"invalid UUID", other.ID, map[string]any{"duplicate_of_issue_id": "MUL-1"}, 400, ""},
		{"status conflict", other.ID, map[string]any{"duplicate_of_issue_id": original.ID, "status": "done"}, 400, ""},
		{"description conflict", other.ID, map[string]any{"duplicate_of_issue_id": original.ID, "description": "new"}, 400, ""},
		{"attachment conflict", other.ID, map[string]any{"duplicate_of_issue_id": original.ID, "attachment_ids": []string{uuid.NewString()}}, 400, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			response := localRequest(t, f.server.Router(), http.MethodPut, "/api/issues/"+tc.id, tc.body)
			requireStatus(t, response, tc.status)
			if tc.code != "" && decodeResponse[map[string]any](t, response)["code"] != tc.code {
				t.Fatalf("missing error code=%s", tc.code)
			}
		})
	}
	requireStatus(t, localRequest(t, f.server.Router(), http.MethodPost, "/api/issues/batch-update", map[string]any{"issue_ids": []string{other.ID}, "updates": map[string]any{"duplicate_of_issue_id": original.ID}}), http.StatusBadRequest)
	unchanged, err := f.server.store.GetIssue(t.Context(), LocalWorkspaceID, other.ID)
	if err != nil {
		t.Fatal(err)
	}
	if unchanged.Status != "todo" || unchanged.DuplicateOf != nil || unchanged.Revision != other.Revision {
		t.Fatalf("rejected updates changed source=%+v", unchanged)
	}
}

func TestLocalIssueDuplicateReadsIgnoreStalePointers(t *testing.T) {
	f := newLocalDuplicateFixture(t)
	original := f.issue(t, "Original", "todo")
	duplicate := f.issue(t, "Needle duplicate", "todo")
	other := f.issue(t, "Other", "todo")
	f.mark(t, duplicate.ID, original.ID)
	for _, path := range []string{"/api/issues/" + duplicate.ID, "/api/issues?status=cancelled", "/api/issues/search?q=Needle"} {
		response := localRequest(t, f.server.Router(), http.MethodGet, path, nil)
		requireStatus(t, response, http.StatusOK)
		var raw any
		if err := json.Unmarshal(response.Body.Bytes(), &raw); err != nil {
			t.Fatal(err)
		}
		obj := raw.(map[string]any)
		if items, exists := obj["issues"]; exists {
			obj = items.([]any)[0].(map[string]any)
		}
		ref, ok := obj["duplicate_of"].(map[string]any)
		if !ok || ref["id"] != original.ID || ref["title"] != "Original" {
			t.Fatalf("%s relation=%v", path, obj["duplicate_of"])
		}
	}
	if _, err := f.server.store.db.Exec(`UPDATE issues SET status='todo' WHERE id=?`, duplicate.ID); err != nil {
		t.Fatal(err)
	}
	read, err := f.server.store.GetIssue(t.Context(), LocalWorkspaceID, duplicate.ID)
	if err != nil {
		t.Fatal(err)
	}
	if read.DuplicateOf != nil || read.DuplicateOfIssueID != nil {
		t.Fatalf("stale reopened pointer exposed=%+v", read)
	}
	if _, children := f.relations(t, original.ID); len(children) != 0 {
		t.Fatal("original lists a reopened duplicate")
	}
	f.mark(t, other.ID, duplicate.ID)
	// A missing original left by an older binary is also an invalid mark.
	if _, err = f.server.store.db.Exec(`UPDATE issues SET status='cancelled',duplicate_of_issue_id=? WHERE id=?`, uuid.NewString(), duplicate.ID); err != nil {
		t.Fatal(err)
	}
	read, err = f.server.store.GetIssue(t.Context(), LocalWorkspaceID, duplicate.ID)
	if err != nil {
		t.Fatal(err)
	}
	if read.DuplicateOf != nil || read.DuplicateOfIssueID != nil {
		t.Fatalf("deleted original exposed=%+v", read)
	}
}

func TestLocalIssueDuplicateConcurrentCrossMarksNeverCycle(t *testing.T) {
	f := newLocalDuplicateFixture(t)
	for attempt := 0; attempt < 8; attempt++ {
		a := f.issue(t, "A", "todo")
		b := f.issue(t, "B", "todo")
		start := make(chan struct{})
		codes := make([]int, 2)
		var wait sync.WaitGroup
		for index, pair := range [][2]string{{a.ID, b.ID}, {b.ID, a.ID}} {
			wait.Add(1)
			go func(index int, pair [2]string) {
				defer wait.Done()
				<-start
				codes[index] = localRequest(t, f.server.Router(), http.MethodPut, "/api/issues/"+pair[0], map[string]any{"duplicate_of_issue_id": pair[1]}).Code
			}(index, pair)
		}
		close(start)
		wait.Wait()
		success := 0
		for _, code := range codes {
			if code == 200 {
				success++
			} else if code != 409 {
				t.Fatalf("attempt %d codes=%v", attempt, codes)
			}
		}
		if success != 1 {
			t.Fatalf("attempt %d success=%d codes=%v", attempt, success, codes)
		}
		var pointers int
		if err := f.server.store.db.QueryRow(`SELECT COUNT(*) FROM issues WHERE id IN (?,?) AND duplicate_of_issue_id IS NOT NULL`, a.ID, b.ID).Scan(&pointers); err != nil {
			t.Fatal(err)
		}
		if pointers != 1 {
			t.Fatalf("cycle or missing mark, pointers=%d", pointers)
		}
	}
}

func TestLocalIssueDuplicateClosureWithdrawsQueuedWakeup(t *testing.T) {
	s, issue := localWakeupAPIFixture(t)
	base, err := s.store.GetIssue(t.Context(), LocalWorkspaceID, issue)
	if err != nil {
		t.Fatal(err)
	}
	response := localRequest(t, s.Router(), http.MethodPost, "/api/issues", map[string]any{"title": "Original", "status": "todo", "project_id": base.ProjectID})
	requireStatus(t, response, http.StatusCreated)
	original := decodeResponse[Issue](t, response)
	rule := createLocalAPIRule(t, s, issue, nil)
	wakeupID := rule["id"].(string)
	taskID, err := s.triggerLocalWakeup(t.Context(), LocalWorkspaceID, issue, wakeupID)
	if err != nil {
		t.Fatal(err)
	}
	marked := localRequest(t, s.Router(), http.MethodPut, "/api/issues/"+issue, map[string]any{"duplicate_of_issue_id": original.ID})
	requireStatus(t, marked, http.StatusOK)
	var enabled bool
	var disabled sql.NullString
	if err = s.store.db.QueryRow(`SELECT enabled,disabled_at FROM issue_wakeups WHERE id=?`, wakeupID).Scan(&enabled, &disabled); err != nil {
		t.Fatal(err)
	}
	if enabled || !disabled.Valid {
		t.Fatal("duplicate left user wakeup enabled")
	}
	var status string
	if err = s.store.db.QueryRow(`SELECT status FROM task_runs WHERE id=?`, taskID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "cancelled" {
		t.Fatalf("duplicate left pending wakeup run %s", status)
	}
	if err = s.tickLocalWakeups(t.Context(), time.Now().UTC().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
}

func TestLocalIssueDuplicateDeleteRacingEditDoesNotRestoreMark(t *testing.T) {
	f := newLocalDuplicateFixture(t)
	for attempt := 0; attempt < 5; attempt++ {
		original := f.issue(t, "Delete original", "todo")
		duplicate := f.issue(t, "Edit duplicate", "todo")
		f.mark(t, duplicate.ID, original.ID)
		start := make(chan struct{})
		var wait sync.WaitGroup
		codes := make([]int, 2)
		wait.Add(2)
		go func() {
			defer wait.Done()
			<-start
			codes[0] = localRequest(t, f.server.Router(), http.MethodDelete, "/api/issues/"+original.ID, nil).Code
		}()
		go func() {
			defer wait.Done()
			<-start
			codes[1] = localRequest(t, f.server.Router(), http.MethodPut, "/api/issues/"+duplicate.ID, map[string]any{"priority": "high"}).Code
		}()
		close(start)
		wait.Wait()
		if codes[0] != http.StatusNoContent || codes[1] != http.StatusOK {
			t.Fatalf("attempt %d codes=%v", attempt, codes)
		}
		current, err := f.server.store.GetIssue(t.Context(), LocalWorkspaceID, duplicate.ID)
		if err != nil {
			t.Fatal(err)
		}
		if current.DuplicateOfIssueID != nil || current.DuplicateOf != nil || current.Priority != "high" || current.Status != "cancelled" {
			t.Fatalf("delete/edit restored old state: %+v", current)
		}
	}
}

func TestLocalIssueDuplicateActiveRunCompletionDoesNotReopenMark(t *testing.T) {
	s, issue := localWakeupAPIFixture(t)
	h := s.Router()
	var project string
	if err := s.store.db.QueryRow(`SELECT project_id FROM issues WHERE id=?`, issue).Scan(&project); err != nil {
		t.Fatal(err)
	}
	original := localRequest(t, h, http.MethodPost, "/api/issues", map[string]any{"title": "Original remains", "project_id": project, "status": "todo"})
	requireStatus(t, original, http.StatusCreated)
	target := decodeResponse[Issue](t, original)
	if _, err := s.store.db.Exec(`UPDATE issues SET assignee_type='agent',assignee_id=? WHERE id=?`, LocalAgentID, issue); err != nil {
		t.Fatal(err)
	}
	if err := s.store.enqueueAssignedIssue(t.Context(), LocalWorkspaceID, issue, ""); err != nil {
		t.Fatal(err)
	}
	var task string
	if err := s.store.db.QueryRow(`SELECT id FROM task_runs WHERE issue_id=?`, issue).Scan(&task); err != nil {
		t.Fatal(err)
	}
	requireStatus(t, localRequest(t, h, http.MethodPost, "/api/daemon/tasks/"+task+"/start", map[string]any{}), http.StatusOK)
	requireStatus(t, localRequest(t, h, http.MethodPut, "/api/issues/"+issue, map[string]any{"duplicate_of_issue_id": target.ID}), http.StatusOK)
	requireStatus(t, localRequest(t, h, http.MethodPost, "/api/daemon/tasks/"+task+"/complete", map[string]any{"output": "Existing run completed"}), http.StatusOK)
	current, err := s.store.GetIssue(t.Context(), LocalWorkspaceID, issue)
	if err != nil {
		t.Fatal(err)
	}
	if current.Status != "cancelled" || current.DuplicateOfIssueID == nil || *current.DuplicateOfIssueID != target.ID {
		t.Fatalf("completion reopened duplicate: status=%s target=%v", current.Status, current.DuplicateOfIssueID)
	}
}
