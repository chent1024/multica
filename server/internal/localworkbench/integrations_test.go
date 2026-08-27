package localworkbench

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

func TestVCSWebhookUsesConfiguredCallbackAndPersistsDedupe(t *testing.T) {
	t.Setenv("MULTICA_PUBLIC_CALLBACK_BASE_URL", "https://relay.example.test")
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/user" || r.Header.Get("Authorization") != "token personal-token" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"login":"local-dev"}`))
	}))
	defer provider.Close()
	root := t.TempDir()
	server, err := NewServer(context.Background(), ServerConfig{DatabasePath: filepath.Join(root, "multica.db"), FileRoot: filepath.Join(root, "files")})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	handler := server.Router()
	projectResponse := localRequest(t, handler, http.MethodPost, "/api/projects", map[string]any{"title": "VCS project"})
	requireStatus(t, projectResponse, http.StatusCreated)
	project := decodeResponse[Project](t, projectResponse)
	issueResponse := localRequest(t, handler, http.MethodPost, "/api/issues", map[string]any{"title": "VCS task", "project_id": project.ID})
	requireStatus(t, issueResponse, http.StatusCreated)
	issue := decodeResponse[Issue](t, issueResponse)

	response := localRequest(t, handler, http.MethodPost, "/api/workspaces/"+LocalWorkspaceID+"/vcs/connections", map[string]any{
		"provider": "forgejo", "instance_url": provider.URL, "access_token": "personal-token",
	})
	requireStatus(t, response, http.StatusCreated)
	connection := decodeResponse[map[string]any](t, response)
	id := connection["id"].(string)
	secret := connection["webhook_secret"].(string)
	path := "/api/webhooks/vcs/" + id
	if got := connection["webhook_url"]; got != "https://relay.example.test"+path {
		t.Fatalf("webhook_url=%v", got)
	}

	body := []byte(`{"action":"opened","pull_request":{"number":7,"title":"` + issue.Identifier + ` implement local VCS","body":"Closes ` + issue.Identifier + `","state":"open","html_url":"https://forge.example/pr/7","created_at":"2026-08-27T00:00:00Z","updated_at":"2026-08-27T00:00:00Z","head":{"ref":"` + issue.Identifier + `-work","sha":"abc"}},"repository":{"name":"multica","full_name":"local/multica","owner":{"login":"local"}}}`)
	sign := func(payload []byte) string {
		digest := hmac.New(sha256.New, []byte(secret))
		_, _ = digest.Write(payload)
		return "sha256=" + hex.EncodeToString(digest.Sum(nil))
	}
	send := func(payload []byte, signature, deliveryID string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(payload))
		request.RemoteAddr = "127.0.0.1:40000"
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("X-Gitea-Signature", signature)
		request.Header.Set("X-GitHub-Delivery", deliveryID)
		request.Header.Set("X-Gitea-Event", "pull_request")
		result := httptest.NewRecorder()
		handler.ServeHTTP(result, request)
		return result
	}

	invalid := send(body, "sha256=00", "bad-delivery")
	requireStatus(t, invalid, http.StatusUnauthorized)
	validSignature := sign(body)
	accepted := send(body, validSignature, "delivery-1")
	requireStatus(t, accepted, http.StatusAccepted)
	acceptedBody := decodeResponse[map[string]any](t, accepted)
	if acceptedBody["status"] != "processed" {
		t.Fatalf("accepted=%v", acceptedBody)
	}
	duplicate := send(body, validSignature, "delivery-1")
	requireStatus(t, duplicate, http.StatusOK)
	duplicateBody := decodeResponse[map[string]any](t, duplicate)
	if duplicateBody["status"] != "duplicate" || duplicateBody["delivery_id"] != acceptedBody["delivery_id"] {
		t.Fatalf("duplicate=%v accepted=%v", duplicateBody, acceptedBody)
	}
	brokenBody := []byte(`{"pull_request":`)
	broken := send(brokenBody, sign(brokenBody), "delivery-broken")
	requireStatus(t, broken, http.StatusUnprocessableEntity)
	var integrationNotifications int
	if err := server.store.db.QueryRow(`SELECT COUNT(*) FROM inbox_items WHERE type='integration_failed'`).Scan(&integrationNotifications); err != nil {
		t.Fatal(err)
	}
	if integrationNotifications != 1 {
		t.Fatalf("integration failure notifications=%d want=1", integrationNotifications)
	}

	var acceptedCount, rejectedCount int
	if err := server.store.db.QueryRow(`SELECT COUNT(*) FROM local_integration_deliveries WHERE integration_id=? AND status='processed'`, id).Scan(&acceptedCount); err != nil {
		t.Fatal(err)
	}
	if err := server.store.db.QueryRow(`SELECT COUNT(*) FROM local_integration_deliveries WHERE integration_id=? AND status='rejected'`, id).Scan(&rejectedCount); err != nil {
		t.Fatal(err)
	}
	if acceptedCount != 1 || rejectedCount != 1 {
		t.Fatalf("accepted=%d rejected=%d", acceptedCount, rejectedCount)
	}
	prs := localRequest(t, handler, http.MethodGet, "/api/issues/"+issue.ID+"/pull-requests", nil)
	requireStatus(t, prs, http.StatusOK)
	pullRequests := decodeResponse[map[string]any](t, prs)["pull_requests"].([]any)
	if len(pullRequests) != 1 || pullRequests[0].(map[string]any)["number"] != float64(7) {
		t.Fatalf("pull_requests=%v", pullRequests)
	}
	unlinkedBody := []byte(`{"action":"edited","pull_request":{"number":7,"title":"Unrelated maintenance","body":"No task reference","state":"open","html_url":"https://forge.example/pr/7","created_at":"2026-08-27T00:00:00Z","updated_at":"2026-08-27T00:02:00Z","head":{"ref":"maintenance","sha":"def"}},"repository":{"name":"multica","full_name":"local/multica","owner":{"login":"local"}}}`)
	unlinked := send(unlinkedBody, sign(unlinkedBody), "delivery-2")
	requireStatus(t, unlinked, http.StatusAccepted)
	prs = localRequest(t, handler, http.MethodGet, "/api/issues/"+issue.ID+"/pull-requests", nil)
	requireStatus(t, prs, http.StatusOK)
	if got := len(decodeResponse[map[string]any](t, prs)["pull_requests"].([]any)); got != 0 {
		t.Fatalf("removed PR reference still linked: %d", got)
	}

	staleMergedBody := []byte(`{"action":"merged","pull_request":{"number":7,"title":"` + issue.Identifier + ` stale","body":"Closes ` + issue.Identifier + `","state":"closed","merged":true,"html_url":"https://forge.example/pr/7","created_at":"2026-08-27T00:00:00Z","updated_at":"2026-08-27T00:01:00Z","merged_at":"2026-08-27T00:01:00Z","head":{"ref":"` + issue.Identifier + `-old","sha":"abc"}},"repository":{"name":"multica","full_name":"local/multica","owner":{"login":"local"}}}`)
	staleMerged := send(staleMergedBody, sign(staleMergedBody), "delivery-3")
	requireStatus(t, staleMerged, http.StatusAccepted)
	unchangedIssue := localRequest(t, handler, http.MethodGet, "/api/issues/"+issue.ID, nil)
	requireStatus(t, unchangedIssue, http.StatusOK)
	if decodeResponse[Issue](t, unchangedIssue).Status == "done" {
		t.Fatal("out-of-order stale merge completed the issue")
	}

	mergedBody := []byte(`{"action":"merged","pull_request":{"number":7,"title":"` + issue.Identifier + ` implement local VCS","body":"Closes ` + issue.Identifier + `","state":"closed","merged":true,"html_url":"https://forge.example/pr/7","created_at":"2026-08-27T00:00:00Z","updated_at":"2026-08-27T00:03:00Z","merged_at":"2026-08-27T00:03:00Z","head":{"ref":"` + issue.Identifier + `-work","sha":"ghi"}},"repository":{"name":"multica","full_name":"local/multica","owner":{"login":"local"}}}`)
	merged := send(mergedBody, sign(mergedBody), "delivery-4")
	requireStatus(t, merged, http.StatusAccepted)
	updatedIssue := localRequest(t, handler, http.MethodGet, "/api/issues/"+issue.ID, nil)
	requireStatus(t, updatedIssue, http.StatusOK)
	if decodeResponse[Issue](t, updatedIssue).Status != "done" {
		t.Fatalf("merged close-intent PR did not complete issue")
	}
}

func TestLocalCallbackBaseURLRejectsUnsafeConfiguration(t *testing.T) {
	for _, configured := range []string{"http://relay.example.test", "https://user:pass@relay.example.test", "https://relay.example.test/path", "https://relay.example.test?token=secret"} {
		t.Run(configured, func(t *testing.T) {
			t.Setenv("MULTICA_PUBLIC_CALLBACK_BASE_URL", configured)
			request := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:18080/api/config", nil)
			if _, err := localCallbackBaseURL(request); err == nil {
				t.Fatalf("localCallbackBaseURL(%q) error=nil", configured)
			}
		})
	}
}
