package localworkbench

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"
)

type localAutomationFixture struct {
	server       *Server
	handler      http.Handler
	automationID string
}

func newLocalAutomationFixture(t *testing.T) localAutomationFixture {
	t.Helper()
	root := t.TempDir()
	server, err := NewServer(context.Background(), ServerConfig{DatabasePath: filepath.Join(root, "multica.db"), FileRoot: filepath.Join(root, "files"), Version: "test"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close() })
	handler := server.Router()
	projectResponse := localRequest(t, handler, http.MethodPost, "/api/projects", map[string]any{"title": "Automation project"})
	requireStatus(t, projectResponse, http.StatusCreated)
	project := decodeResponse[Project](t, projectResponse)
	register := localRequest(t, handler, http.MethodPost, "/api/daemon/register", map[string]any{"workspace_id": LocalWorkspaceID, "daemon_id": "automation-daemon", "runtimes": []map[string]any{{"name": "Codex", "type": "codex", "status": "online", "profile_id": "default"}}})
	requireStatus(t, register, http.StatusOK)
	automationResponse := localRequest(t, handler, http.MethodPost, "/api/autopilots", map[string]any{"title": "Local automation", "project_id": project.ID, "assignee_type": "agent", "assignee_id": LocalAgentID})
	requireStatus(t, automationResponse, http.StatusCreated)
	automation := decodeResponse[map[string]any](t, automationResponse)
	return localAutomationFixture{server: server, handler: handler, automationID: automation["id"].(string)}
}

func TestLocalAutomationWebhookDedupeSignatureReplayAndPersistence(t *testing.T) {
	fixture := newLocalAutomationFixture(t)
	triggerResponse := localRequest(t, fixture.handler, http.MethodPost, "/api/autopilots/"+fixture.automationID+"/triggers", map[string]any{"kind": "webhook", "provider": "github", "signing_secret": "local-secret"})
	requireStatus(t, triggerResponse, http.StatusCreated)
	trigger := decodeResponse[map[string]any](t, triggerResponse)
	if _, exposed := trigger["signing_secret"]; exposed {
		t.Fatal("webhook signing secret was exposed")
	}
	path := trigger["webhook_path"].(string)
	body := []byte(`{"action":"opened"}`)
	hash := hmac.New(sha256.New, []byte("local-secret"))
	_, _ = hash.Write(body)
	send := func(signature, deliveryID string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
		req.RemoteAddr = "127.0.0.1:43434"
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Hub-Signature-256", signature)
		req.Header.Set("X-GitHub-Delivery", deliveryID)
		req.Header.Set("X-GitHub-Event", "issues")
		response := httptest.NewRecorder()
		fixture.handler.ServeHTTP(response, req)
		return response
	}
	first := send("sha256="+hex.EncodeToString(hash.Sum(nil)), "delivery-1")
	requireStatus(t, first, http.StatusOK)
	accepted := decodeResponse[map[string]any](t, first)
	second := send("sha256="+hex.EncodeToString(hash.Sum(nil)), "delivery-1")
	requireStatus(t, second, http.StatusOK)
	duplicate := decodeResponse[map[string]any](t, second)
	if duplicate["status"] != "duplicate" || duplicate["delivery_id"] != accepted["delivery_id"] || duplicate["run_id"] != accepted["run_id"] {
		t.Fatalf("duplicate=%v accepted=%v", duplicate, accepted)
	}

	listResponse := localRequest(t, fixture.handler, http.MethodGet, "/api/autopilots/"+fixture.automationID+"/deliveries", nil)
	requireStatus(t, listResponse, http.StatusOK)
	list := decodeResponse[map[string]any](t, listResponse)
	if list["total"].(float64) != 1 {
		t.Fatalf("deliveries=%v", list)
	}
	delivery := list["deliveries"].([]any)[0].(map[string]any)
	if delivery["attempt_count"].(float64) != 2 || delivery["signature_status"] != "valid" {
		t.Fatalf("delivery=%v", delivery)
	}

	replayPath := "/api/autopilots/" + fixture.automationID + "/deliveries/" + accepted["delivery_id"].(string) + "/replay"
	replayRequest := httptest.NewRequest(http.MethodPost, replayPath, bytes.NewReader([]byte(`{}`)))
	replayRequest.RemoteAddr = "127.0.0.1:43434"
	replayRequest.Header.Set("Content-Type", "application/json")
	replayRequest.Header.Set("Idempotency-Key", "replay-1")
	replayResponse := httptest.NewRecorder()
	fixture.handler.ServeHTTP(replayResponse, replayRequest)
	requireStatus(t, replayResponse, http.StatusAccepted)
	replay := decodeResponse[map[string]any](t, replayResponse)
	if replay["replayed_from_delivery_id"] != accepted["delivery_id"] || replay["autopilot_run_id"] == nil {
		t.Fatalf("replay=%v", replay)
	}

	invalid := send("sha256=00", "delivery-2")
	requireStatus(t, invalid, http.StatusUnauthorized)
	var webhookNotifications int
	if err := fixture.server.store.db.QueryRow(`SELECT COUNT(*) FROM inbox_items WHERE type='webhook_failed' AND source_key LIKE 'automation-delivery:%:rejected'`).Scan(&webhookNotifications); err != nil {
		t.Fatal(err)
	}
	if webhookNotifications != 1 {
		t.Fatalf("webhook failure notifications=%d want=1", webhookNotifications)
	}
	retried := send("sha256="+hex.EncodeToString(hash.Sum(nil)), "delivery-2")
	requireStatus(t, retried, http.StatusOK)
	if got := decodeResponse[map[string]any](t, retried)["status"]; got != "accepted" {
		t.Fatalf("valid retry after rejected signature status=%v", got)
	}
	var runCount int
	if err := fixture.server.store.db.QueryRow(`SELECT COUNT(*) FROM automation_runs WHERE automation_id=?`, fixture.automationID).Scan(&runCount); err != nil {
		t.Fatal(err)
	}
	if runCount != 3 {
		t.Fatalf("run count=%d want=3", runCount)
	}
}

func TestFinishAutomationDeliveryDoesNotCreateNotificationWithoutSourceDelivery(t *testing.T) {
	fixture := newLocalAutomationFixture(t)
	request := httptest.NewRequest(http.MethodPost, "/", nil)
	err := fixture.server.finishAutomationDelivery(request, "missing-delivery", "failed", http.StatusInternalServerError, map[string]any{
		"status": "failed",
		"reason": "dispatch failed",
	}, "", "dispatch_failed")
	if !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("finish error=%v want=%v", err, sql.ErrNoRows)
	}
	var count int
	if err := fixture.server.store.db.QueryRow(`SELECT COUNT(*) FROM inbox_items WHERE source_key='automation-delivery:missing-delivery:failed'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("orphan notifications=%d want=0", count)
	}
}

func TestFinishAutomationDeliveryRollsBackOutcomeWhenNotificationInsertFails(t *testing.T) {
	fixture := newLocalAutomationFixture(t)
	stamp := now()
	if _, err := fixture.server.store.db.Exec(`INSERT INTO automation_deliveries(
		id, workspace_id, automation_id, trigger_id, provider, event, signature_status,
		status, available_at, received_at, last_attempt_at, created_at)
		VALUES ('delivery-with-notification-failure', ?, ?, 'trigger-1', 'github', 'issues',
		'valid', 'queued', ?, ?, ?, ?)`, LocalWorkspaceID, fixture.automationID, stamp, stamp, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.server.store.db.Exec(`CREATE TRIGGER reject_test_notification
		BEFORE INSERT ON inbox_items BEGIN SELECT RAISE(ABORT, 'notification insert failed'); END`); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/", nil)
	err := fixture.server.finishAutomationDelivery(request, "delivery-with-notification-failure", "failed", http.StatusInternalServerError, map[string]any{
		"status": "failed",
		"reason": "dispatch failed",
	}, "", "dispatch_failed")
	if err == nil {
		t.Fatal("finish succeeded despite notification insert failure")
	}
	var status string
	var dispatchAttempts int
	if err := fixture.server.store.db.QueryRow(`SELECT status, dispatch_attempts FROM automation_deliveries WHERE id='delivery-with-notification-failure'`).Scan(&status, &dispatchAttempts); err != nil {
		t.Fatal(err)
	}
	if status != "queued" || dispatchAttempts != 0 {
		t.Fatalf("delivery status=%q dispatch_attempts=%d want queued,0", status, dispatchAttempts)
	}
	var count int
	if err := fixture.server.store.db.QueryRow(`SELECT COUNT(*) FROM inbox_items WHERE source_key='automation-delivery:delivery-with-notification-failure:failed'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("notifications=%d want=0", count)
	}
}

func TestLocalAutomationRuntimeFailureCreatesOneNotification(t *testing.T) {
	fixture := newLocalAutomationFixture(t)
	if _, err := fixture.server.store.db.Exec(`UPDATE runtimes SET status='offline' WHERE workspace_id=?`, LocalWorkspaceID); err != nil {
		t.Fatal(err)
	}
	first := localRequest(t, fixture.handler, http.MethodPost, "/api/autopilots/"+fixture.automationID+"/trigger", map[string]any{})
	requireStatus(t, first, http.StatusCreated)
	run := decodeResponse[map[string]any](t, first)
	if run["status"] != "skipped" || run["reason_code"] != "runtime_unavailable" {
		t.Fatalf("run=%v", run)
	}
	if err := fixture.server.notifyAutomationRun(t.Context(), LocalWorkspaceID, fixture.automationID, run); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := fixture.server.store.db.QueryRow(`SELECT COUNT(*) FROM inbox_items WHERE type='automation_failed' AND source_key=?`, "automation-run:"+run["id"].(string)+":failed").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("automation failure notifications=%d want=1", count)
	}
}

func TestLocalAutomationSchedulerIsRestartIdempotent(t *testing.T) {
	fixture := newLocalAutomationFixture(t)
	triggerResponse := localRequest(t, fixture.handler, http.MethodPost, "/api/autopilots/"+fixture.automationID+"/triggers", map[string]any{"kind": "schedule", "cron_expression": "0 0 * * *", "timezone": "UTC"})
	requireStatus(t, triggerResponse, http.StatusCreated)
	trigger := decodeResponse[map[string]any](t, triggerResponse)
	due := time.Now().UTC().Add(-10 * time.Second).Truncate(time.Second).Format(time.RFC3339)
	trigger["next_run_at"] = due
	raw, _ := json.Marshal(trigger)
	if _, err := fixture.server.store.db.Exec(`UPDATE automation_triggers SET data=? WHERE id=?`, string(raw), trigger["id"]); err != nil {
		t.Fatal(err)
	}
	fixture.server.processDueAutomations(context.Background())
	fixture.server.processDueAutomations(context.Background())
	var runCount, occurrenceCount int
	if err := fixture.server.store.db.QueryRow(`SELECT COUNT(*) FROM automation_runs WHERE automation_id=?`, fixture.automationID).Scan(&runCount); err != nil {
		t.Fatal(err)
	}
	if err := fixture.server.store.db.QueryRow(`SELECT COUNT(*) FROM automation_occurrences WHERE trigger_id=?`, trigger["id"]).Scan(&occurrenceCount); err != nil {
		t.Fatal(err)
	}
	if runCount != 1 || occurrenceCount != 1 {
		t.Fatalf("runs=%d occurrences=%d", runCount, occurrenceCount)
	}
}
