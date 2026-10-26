package localworkbench

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

func TestLocalWakeupVCSSignedCurrentHeadEvidence(t *testing.T) {
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/user" || r.Header.Get("Authorization") != "token fixture-only" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"login":"fixture"}`))
	}))
	defer provider.Close()
	root := t.TempDir()
	server, err := NewServer(context.Background(), ServerConfig{DatabasePath: filepath.Join(root, "db.sqlite"), FileRoot: filepath.Join(root, "files")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close() })
	handler := server.Router()
	response := localRequest(t, handler, http.MethodPost, "/api/projects", map[string]any{"title": "VCS wakeup fixture"})
	requireStatus(t, response, http.StatusCreated)
	project := decodeResponse[Project](t, response)
	response = localRequest(t, handler, http.MethodPost, "/api/issues", map[string]any{"title": "Watch pull request", "project_id": project.ID})
	requireStatus(t, response, http.StatusCreated)
	issue := decodeResponse[Issue](t, response)
	response = localRequest(t, handler, http.MethodPost, "/api/workspaces/"+LocalWorkspaceID+"/vcs/connections", map[string]any{"provider": "forgejo", "instance_url": provider.URL, "access_token": "fixture-only"})
	requireStatus(t, response, http.StatusCreated)
	connection := decodeResponse[map[string]any](t, response)
	connectionID := connection["id"].(string)
	secret := connection["webhook_secret"].(string)
	delivery := 0
	send := func(kind string, payload []byte, valid bool) *httptest.ResponseRecorder {
		t.Helper()
		delivery++
		mac := hmac.New(sha256.New, []byte(secret))
		_, _ = mac.Write(payload)
		signature := hex.EncodeToString(mac.Sum(nil))
		if !valid {
			signature = "00"
		}
		request := httptest.NewRequest(http.MethodPost, "/api/webhooks/vcs/"+connectionID, bytes.NewReader(payload))
		request.RemoteAddr = "127.0.0.1:40000"
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("X-Gitea-Signature", signature)
		request.Header.Set("X-Gitea-Event", kind)
		request.Header.Set("X-GitHub-Delivery", fmt.Sprintf("wakeup-vcs-%d", delivery))
		result := httptest.NewRecorder()
		handler.ServeHTTP(result, request)
		return result
	}
	pr := func(head, stamp string) []byte {
		t.Helper()
		payload, err := json.Marshal(map[string]any{"action": "synchronize", "pull_request": map[string]any{"number": 7, "title": issue.Identifier + " fix", "body": "References " + issue.Identifier, "state": "open", "html_url": "https://vcs.example.test/pr/7", "created_at": "2026-10-26T00:00:00Z", "updated_at": stamp, "head": map[string]any{"ref": issue.Identifier + "-branch", "sha": head}}, "repository": map[string]any{"name": "repo", "full_name": "owner/repo", "owner": map[string]any{"login": "owner"}}})
		if err != nil {
			t.Fatal(err)
		}
		return payload
	}
	status := func(head, state, stamp string) []byte {
		payload, err := json.Marshal(map[string]any{"sha": head, "state": state, "context": "ci/test", "updated_at": stamp})
		if err != nil {
			t.Fatal(err)
		}
		return payload
	}
	check := func(want bool) string {
		t.Helper()
		met, evidence, err := evaluateLocalWakeupPullRequests(t.Context(), server.store.db, LocalWorkspaceID, issue.ID, "checks_finished")
		if err != nil || met != want {
			t.Fatalf("condition met=%v want=%v evidence=%s err=%v", met, want, evidence, err)
		}
		return evidence
	}
	requireStatus(t, send("pull_request", pr("head-one", "2026-10-26T00:00:00Z"), true), http.StatusAccepted)
	check(false)
	requireStatus(t, send("status", status("head-one", "success", "2026-10-26T00:00:01Z"), false), http.StatusUnauthorized)
	check(false)
	requireStatus(t, send("status", status("head-one", "pending", "2026-10-26T00:00:02Z"), true), http.StatusAccepted)
	check(false)
	requireStatus(t, send("status", status("head-one", "success", "2026-10-26T00:00:03Z"), true), http.StatusAccepted)
	firstEvidence := check(true)
	if _, err := server.store.db.Exec(`UPDATE vcs_pull_requests SET snapshot_head_sha='head-one',checks_rollup_state='SUCCESS',api_mergeable='MERGEABLE' WHERE connection_id=?`, connectionID); err != nil {
		t.Fatal(err)
	}
	requireStatus(t, send("pull_request", pr("head-one", "2026-10-26T00:00:03Z"), true), http.StatusAccepted)
	var snapshot, checks, mergeability string
	if err := server.store.db.QueryRow(`SELECT COALESCE(snapshot_head_sha,''),COALESCE(checks_rollup_state,''),COALESCE(api_mergeable,'') FROM vcs_pull_requests WHERE connection_id=?`, connectionID).Scan(&snapshot, &checks, &mergeability); err != nil {
		t.Fatal(err)
	}
	if snapshot != "head-one" || checks != "SUCCESS" || mergeability != "MERGEABLE" {
		t.Fatal("same head discarded its existing snapshot")
	}
	requireStatus(t, send("pull_request", pr("head-two", "2026-10-26T00:00:04Z"), false), http.StatusUnauthorized)
	if err := server.store.db.QueryRow(`SELECT COALESCE(snapshot_head_sha,'') FROM vcs_pull_requests WHERE connection_id=?`, connectionID).Scan(&snapshot); err != nil {
		t.Fatal(err)
	}
	if snapshot != "head-one" {
		t.Fatal("unsigned webhook changed the trusted snapshot")
	}
	requireStatus(t, send("pull_request", pr("head-two", "2026-10-26T00:00:04Z"), true), http.StatusAccepted)
	check(false)
	if err := server.store.db.QueryRow(`SELECT COALESCE(snapshot_head_sha,''),COALESCE(checks_rollup_state,''),COALESCE(api_mergeable,'') FROM vcs_pull_requests WHERE connection_id=?`, connectionID).Scan(&snapshot, &checks, &mergeability); err != nil {
		t.Fatal(err)
	}
	if snapshot != "" || checks != "" || mergeability != "" {
		t.Fatal("new head retained an old snapshot")
	}
	// Neither an old PR delivery nor an old head's checks can satisfy the new head.
	requireStatus(t, send("pull_request", pr("head-one", "2026-10-26T00:00:01Z"), true), http.StatusAccepted)
	requireStatus(t, send("status", status("head-one", "success", "2026-10-26T00:00:05Z"), true), http.StatusAccepted)
	check(false)
	if err := server.store.db.QueryRow(`SELECT COALESCE(snapshot_head_sha,''),COALESCE(checks_rollup_state,''),COALESCE(api_mergeable,'') FROM vcs_pull_requests WHERE connection_id=?`, connectionID).Scan(&snapshot, &checks, &mergeability); err != nil {
		t.Fatal(err)
	}
	if snapshot != "" || checks != "" || mergeability != "" {
		t.Fatal("stale delivery resurrected an old snapshot")
	}
	requireStatus(t, send("status", status("head-two", "failure", "2026-10-26T00:00:06Z"), true), http.StatusAccepted)
	if check(true) == firstEvidence {
		t.Fatal("new head reused old condition evidence")
	}
	requireStatus(t, send("status", status("head-two", "pending", "2026-10-26T00:00:02Z"), true), http.StatusAccepted)
	check(true)
	_, _, err = evaluateLocalWakeupPullRequests(t.Context(), server.store.db, LocalWorkspaceID, issue.ID, "conflicts")
	if !errors.Is(err, errLocalWakeupPRConflictsUnsupported) {
		t.Fatalf("self-hosted mergeability must remain unsupported, got %v", err)
	}
}
