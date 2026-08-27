package localworkbench

import (
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

const maxLocalWebhookBody = 256 << 10

func localCallbackBaseURL(r *http.Request) (string, error) {
	configured := strings.TrimRight(strings.TrimSpace(os.Getenv("MULTICA_PUBLIC_CALLBACK_BASE_URL")), "/")
	if configured == "" {
		scheme := "http"
		if r.TLS != nil {
			scheme = "https"
		}
		return scheme + "://" + r.Host, nil
	}
	parsed, err := url.Parse(configured)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/") {
		return "", errors.New("MULTICA_PUBLIC_CALLBACK_BASE_URL must be an HTTPS origin without credentials, path, query, or fragment")
	}
	return configured, nil
}

func (s *Server) receiveAutomationWebhook(w http.ResponseWriter, r *http.Request, token string) {
	var automationID, triggerID, rawTrigger string
	var signingSecret sql.NullString
	err := s.store.db.QueryRowContext(r.Context(), `SELECT automation_id, id, data, signing_secret FROM automation_triggers WHERE kind='webhook' AND json_extract(data, '$.webhook_token')=? LIMIT 1`, token).Scan(&automationID, &triggerID, &rawTrigger, &signingSecret)
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	trigger, err := decodeJSONMap(rawTrigger)
	if err != nil {
		s.fail(w, err)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxLocalWebhookBody+1))
	if err != nil {
		writeError(w, http.StatusBadRequest, errors.New("unable to read webhook payload"))
		return
	}
	if len(body) > maxLocalWebhookBody {
		writeError(w, http.StatusRequestEntityTooLarge, errors.New("webhook payload exceeds 256 KiB"))
		return
	}
	payload := map[string]any{}
	if err := json.Unmarshal(body, &payload); err != nil {
		writeError(w, http.StatusBadRequest, errors.New("webhook payload must be a JSON object"))
		return
	}
	payload["trigger_id"] = triggerID
	provider, event := webhookProviderAndEvent(r.Header, trigger)
	dedupeKey, dedupeSource := webhookDedupe(r.Header)
	storageDedupeKey := dedupeKey
	signatureStatus := "not_required"
	if signingSecret.Valid && signingSecret.String != "" {
		signatureStatus = verifyLocalWebhookSignature(signingSecret.String, r.Header, body)
	}
	selectedHeaders, _ := json.Marshal(map[string]any{
		"x-github-event": r.Header.Get("X-GitHub-Event"),
		"x-gitlab-event": r.Header.Get("X-Gitlab-Event"),
		"x-request-id":   r.Header.Get("X-Request-ID"),
	})
	id, stamp := uuid.NewString(), now()
	status, reasonCode := "queued", any(nil)
	responseStatus := http.StatusOK
	if signatureStatus == "invalid" || signatureStatus == "missing" {
		status, reasonCode, responseStatus = "rejected", "invalid_signature", http.StatusUnauthorized
		if storageDedupeKey != "" {
			// A rejected request must not consume the provider's valid retry key.
			storageDedupeKey = "rejected:" + storageDedupeKey
		}
	}
	result, err := s.store.db.ExecContext(r.Context(), `INSERT OR IGNORE INTO automation_deliveries(
		id, workspace_id, automation_id, trigger_id, provider, event, dedupe_key, dedupe_source,
		signature_status, status, available_at, content_type, selected_headers, raw_body,
		reason_code, received_at, last_attempt_at, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, LocalWorkspaceID, automationID, triggerID, provider, event, nullable(storageDedupeKey), nullable(dedupeSource),
		signatureStatus, status, stamp, nullable(r.Header.Get("Content-Type")), string(selectedHeaders), body,
		reasonCode, stamp, stamp, stamp)
	if err != nil {
		s.fail(w, err)
		return
	}
	inserted, _ := result.RowsAffected()
	if inserted == 0 {
		var existingID, existingRunID string
		_ = s.store.db.QueryRowContext(r.Context(), `SELECT id, COALESCE(automation_run_id, '') FROM automation_deliveries WHERE trigger_id=? AND dedupe_key=?`, triggerID, storageDedupeKey).Scan(&existingID, &existingRunID)
		_, _ = s.store.db.ExecContext(r.Context(), `UPDATE automation_deliveries SET attempt_count=attempt_count+1, last_attempt_at=? WHERE id=?`, stamp, existingID)
		response := map[string]any{"status": "duplicate", "delivery_id": existingID}
		if existingRunID != "" {
			response["run_id"] = existingRunID
		}
		writeJSON(w, http.StatusOK, response)
		return
	}
	if status == "rejected" {
		response := map[string]any{"status": status, "delivery_id": id, "reason": reasonCode}
		s.finishAutomationDelivery(r, id, status, responseStatus, response, "", reasonCode)
		writeJSON(w, responseStatus, response)
		return
	}
	if enabled, _ := trigger["enabled"].(bool); !enabled {
		response := map[string]any{"status": "ignored", "delivery_id": id, "reason": "trigger_disabled"}
		s.finishAutomationDelivery(r, id, "ignored", http.StatusOK, response, "", "trigger_disabled")
		writeJSON(w, http.StatusOK, response)
		return
	}
	runID := uuid.NewString()
	run, _, executeErr := s.executeAutomation(r.Context(), LocalWorkspaceID, automationID, "webhook", payload, runID)
	if executeErr != nil {
		response := map[string]any{"status": "failed", "delivery_id": id, "reason": executeErr.Error()}
		s.finishAutomationDelivery(r, id, "failed", http.StatusInternalServerError, response, "", "dispatch_failed")
		writeJSON(w, http.StatusInternalServerError, response)
		return
	}
	response := map[string]any{"status": "accepted", "delivery_id": id, "run_id": runID, "autopilot_id": automationID, "trigger_id": triggerID}
	if stringFromAny(run["status"]) == "skipped" {
		response["status"], response["reason"] = "skipped", run["failure_reason"]
	}
	s.finishAutomationDelivery(r, id, "dispatched", http.StatusOK, response, runID, nil)
	writeJSON(w, http.StatusOK, response)
}

func webhookProviderAndEvent(headers http.Header, trigger map[string]any) (string, string) {
	provider := strings.TrimSpace(stringFromAny(trigger["provider"]))
	if provider == "" {
		provider = "generic"
	}
	event := strings.TrimSpace(headers.Get("X-GitHub-Event"))
	if event == "" {
		event = strings.TrimSpace(headers.Get("X-Gitlab-Event"))
	}
	if event == "" {
		event = "generic"
	}
	return provider, event
}

func webhookDedupe(headers http.Header) (string, string) {
	for _, name := range []string{"X-GitHub-Delivery", "X-Gitlab-Event-UUID", "X-Request-ID", "Idempotency-Key"} {
		if value := strings.TrimSpace(headers.Get(name)); value != "" {
			return value, strings.ToLower(name)
		}
	}
	return "", ""
}

func verifyLocalWebhookSignature(secret string, headers http.Header, body []byte) string {
	provided := strings.TrimSpace(headers.Get("X-Hub-Signature-256"))
	if provided == "" {
		provided = strings.TrimSpace(headers.Get("X-Multica-Signature"))
	}
	if provided == "" {
		return "missing"
	}
	provided = strings.TrimPrefix(provided, "sha256=")
	want, err := hex.DecodeString(provided)
	if err != nil {
		return "invalid"
	}
	hash := hmac.New(sha256.New, []byte(secret))
	_, _ = hash.Write(body)
	if hmac.Equal(hash.Sum(nil), want) {
		return "valid"
	}
	return "invalid"
}

func (s *Server) finishAutomationDelivery(r *http.Request, id, status string, responseStatus int, response map[string]any, runID string, reason any) {
	raw, _ := json.Marshal(response)
	_, _ = s.store.db.ExecContext(r.Context(), `UPDATE automation_deliveries SET status=?, dispatch_attempts=dispatch_attempts+1, response_status=?, response_body=?, automation_run_id=?, reason_code=?, last_attempt_at=? WHERE id=?`, status, responseStatus, string(raw), nullable(runID), reason, now(), id)
}

func scanAutomationDelivery(row rowScanner, detail bool) (map[string]any, error) {
	var id, workspaceID, automationID, triggerID, provider, event, signatureStatus, status, availableAt, receivedAt, lastAttemptAt, createdAt string
	var dedupeKey, dedupeSource, contentType, responseBody, runID, replayedFrom, replayKey, deliveryError, reasonCode sql.NullString
	var attemptCount, dispatchAttempts int
	var responseStatus sql.NullInt64
	var selectedHeaders string
	var rawBody []byte
	err := row.Scan(&id, &workspaceID, &automationID, &triggerID, &provider, &event, &dedupeKey, &dedupeSource, &signatureStatus, &status, &attemptCount, &dispatchAttempts, &availableAt, &contentType, &responseStatus, &runID, &replayedFrom, &deliveryError, &reasonCode, &replayKey, &receivedAt, &lastAttemptAt, &createdAt, &selectedHeaders, &rawBody, &responseBody)
	if err != nil {
		return nil, err
	}
	result := map[string]any{"id": id, "workspace_id": workspaceID, "autopilot_id": automationID, "trigger_id": triggerID, "provider": provider, "event": event, "dedupe_key": nullString(dedupeKey), "dedupe_source": nullString(dedupeSource), "signature_status": signatureStatus, "status": status, "attempt_count": attemptCount, "dispatch_attempts": dispatchAttempts, "available_at": availableAt, "content_type": nullString(contentType), "response_status": nullInt(responseStatus), "autopilot_run_id": nullString(runID), "replayed_from_delivery_id": nullString(replayedFrom), "error": nullString(deliveryError), "reason_code": nullString(reasonCode), "replay_idempotency_key": nullString(replayKey), "received_at": receivedAt, "last_attempt_at": lastAttemptAt, "created_at": createdAt}
	if detail {
		result["selected_headers"] = decodeObject(selectedHeaders)
		result["raw_body"] = string(rawBody)
		result["response_body"] = nullString(responseBody)
	}
	return result, nil
}

const automationDeliverySelect = `SELECT id, workspace_id, automation_id, trigger_id, provider, event, dedupe_key, dedupe_source, signature_status, status, attempt_count, dispatch_attempts, available_at, content_type, response_status, automation_run_id, replayed_from_delivery_id, error, reason_code, replay_idempotency_key, received_at, last_attempt_at, created_at, selected_headers, raw_body, response_body FROM automation_deliveries`

func (s *Server) listAutomationDeliveries(w http.ResponseWriter, r *http.Request) {
	ws, err := s.workspaceFor(r)
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	automationID := chi.URLParam(r, "id")
	if _, err := s.store.readAutomation(automationID, ws.ID); err != nil {
		s.respondStoreError(w, err)
		return
	}
	rows, err := s.store.db.QueryContext(r.Context(), automationDeliverySelect+` WHERE automation_id=? AND workspace_id=? ORDER BY received_at DESC, id LIMIT ? OFFSET ?`, automationID, ws.ID, parseLimit(r, 20), parseOffset(r))
	if err != nil {
		s.fail(w, err)
		return
	}
	items := []map[string]any{}
	for rows.Next() {
		item, scanErr := scanAutomationDelivery(rows, false)
		if scanErr != nil {
			_ = rows.Close()
			s.fail(w, scanErr)
			return
		}
		items = append(items, item)
	}
	_ = rows.Close()
	var total int
	_ = s.store.db.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM automation_deliveries WHERE automation_id=? AND workspace_id=?`, automationID, ws.ID).Scan(&total)
	writeJSON(w, http.StatusOK, map[string]any{"deliveries": items, "total": total})
}

func (s *Server) getAutomationDelivery(w http.ResponseWriter, r *http.Request) {
	ws, err := s.workspaceFor(r)
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	item, err := scanAutomationDelivery(s.store.db.QueryRowContext(r.Context(), automationDeliverySelect+` WHERE id=? AND automation_id=? AND workspace_id=?`, chi.URLParam(r, "deliveryID"), chi.URLParam(r, "id"), ws.ID), true)
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func (s *Server) replayAutomationDelivery(w http.ResponseWriter, r *http.Request) {
	ws, err := s.workspaceFor(r)
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	original, err := scanAutomationDelivery(s.store.db.QueryRowContext(r.Context(), automationDeliverySelect+` WHERE id=? AND automation_id=? AND workspace_id=?`, chi.URLParam(r, "deliveryID"), chi.URLParam(r, "id"), ws.ID), true)
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	if original["status"] == "rejected" || original["signature_status"] == "invalid" {
		writeError(w, http.StatusBadRequest, errors.New("cannot replay a delivery that failed signature verification"))
		return
	}
	key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if key == "" {
		key = uuid.NewString()
	}
	var existingID string
	if err := s.store.db.QueryRowContext(r.Context(), `SELECT id FROM automation_deliveries WHERE replayed_from_delivery_id=? AND replay_idempotency_key=?`, original["id"], key).Scan(&existingID); err == nil {
		item, _ := scanAutomationDelivery(s.store.db.QueryRowContext(r.Context(), automationDeliverySelect+` WHERE id=?`, existingID), true)
		writeJSON(w, http.StatusAccepted, item)
		return
	}
	id, runID, stamp := uuid.NewString(), uuid.NewString(), now()
	selected, _ := json.Marshal(original["selected_headers"])
	_, err = s.store.db.ExecContext(r.Context(), `INSERT INTO automation_deliveries(id, workspace_id, automation_id, trigger_id, provider, event, signature_status, status, available_at, content_type, selected_headers, raw_body, replayed_from_delivery_id, replay_idempotency_key, received_at, last_attempt_at, created_at) VALUES (?, ?, ?, ?, ?, ?, 'not_required', 'queued', ?, ?, ?, ?, ?, ?, ?, ?, ?)`, id, ws.ID, original["autopilot_id"], original["trigger_id"], original["provider"], original["event"], stamp, original["content_type"], string(selected), original["raw_body"], original["id"], key, stamp, stamp, stamp)
	if err != nil {
		s.fail(w, err)
		return
	}
	payload := map[string]any{}
	if err := json.Unmarshal([]byte(stringFromAny(original["raw_body"])), &payload); err != nil {
		writeError(w, http.StatusBadRequest, errors.New("stored body no longer parses"))
		return
	}
	payload["trigger_id"] = original["trigger_id"]
	_, _, executeErr := s.executeAutomation(r.Context(), ws.ID, stringFromAny(original["autopilot_id"]), "webhook_replay", payload, runID)
	if executeErr != nil {
		s.finishAutomationDelivery(r, id, "failed", http.StatusInternalServerError, map[string]any{"status": "failed", "reason": executeErr.Error()}, "", "dispatch_failed")
	} else {
		s.finishAutomationDelivery(r, id, "dispatched", http.StatusAccepted, map[string]any{"status": "accepted", "run_id": runID}, runID, nil)
	}
	item, _ := scanAutomationDelivery(s.store.db.QueryRowContext(r.Context(), automationDeliverySelect+` WHERE id=?`, id), true)
	writeJSON(w, http.StatusAccepted, item)
}

func nullString(value sql.NullString) any {
	if value.Valid {
		return value.String
	}
	return nil
}
func nullInt(value sql.NullInt64) any {
	if value.Valid {
		return value.Int64
	}
	return nil
}
