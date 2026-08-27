package localworkbench

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/robfig/cron/v3"
)

var localCronParser = cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)

func (s *Store) readAutomation(id, workspaceID string) (map[string]any, error) {
	var raw, createdAt, updatedAt string
	if err := s.db.QueryRow(`SELECT data, created_at, updated_at FROM automations WHERE id=? AND workspace_id=?`, id, workspaceID).Scan(&raw, &createdAt, &updatedAt); err != nil {
		return nil, err
	}
	value, err := decodeJSONMap(raw)
	if err != nil {
		return nil, err
	}
	value["id"], value["workspace_id"] = id, workspaceID
	value["created_at"], value["updated_at"] = createdAt, updatedAt
	value["can_write"], value["can_manage_access"] = true, true
	return value, nil
}

func (s *Store) readAutomationTrigger(id, automationID string) (map[string]any, error) {
	var raw, createdAt, updatedAt string
	var enabled bool
	if err := s.db.QueryRow(`SELECT data, enabled, created_at, updated_at FROM automation_triggers WHERE id=? AND automation_id=?`, id, automationID).Scan(&raw, &enabled, &createdAt, &updatedAt); err != nil {
		return nil, err
	}
	value, err := decodeJSONMap(raw)
	if err != nil {
		return nil, err
	}
	value["id"], value["autopilot_id"], value["enabled"] = id, automationID, enabled
	value["created_at"], value["updated_at"] = createdAt, updatedAt
	return value, nil
}

func (s *Server) listAutomations(w http.ResponseWriter, r *http.Request) {
	ws, err := s.workspaceFor(r)
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	query, args := `SELECT id FROM automations WHERE workspace_id=?`, []any{ws.ID}
	if status := strings.TrimSpace(r.URL.Query().Get("status")); status != "" {
		query += ` AND status=?`
		args = append(args, status)
	}
	query += ` ORDER BY updated_at DESC, id`
	rows, err := s.store.db.QueryContext(r.Context(), query, args...)
	if err != nil {
		s.fail(w, err)
		return
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			s.fail(w, err)
			return
		}
		ids = append(ids, id)
	}
	_ = rows.Close()
	items := []map[string]any{}
	for _, id := range ids {
		item, err := s.store.readAutomation(id, ws.ID)
		if err != nil {
			s.fail(w, err)
			return
		}
		var kindsRaw string
		_ = s.store.db.QueryRowContext(r.Context(), `SELECT COALESCE(json_group_array(kind), '[]') FROM automation_triggers WHERE automation_id=? AND enabled=1`, id).Scan(&kindsRaw)
		var kinds []string
		_ = jsonUnmarshalStringArray(kindsRaw, &kinds)
		item["trigger_kinds"] = kinds
		items = append(items, item)
	}
	writeJSON(w, http.StatusOK, map[string]any{"autopilots": items, "total": len(items)})
}

func jsonUnmarshalStringArray(raw string, target *[]string) error {
	value := []any{}
	decoded, err := decodeJSONValue(raw, &value)
	if err != nil {
		return err
	}
	for _, item := range decoded {
		if text, ok := item.(string); ok {
			*target = append(*target, text)
		}
	}
	return nil
}

func decodeJSONValue(raw string, target *[]any) ([]any, error) {
	if err := json.Unmarshal([]byte(raw), target); err != nil {
		return nil, err
	}
	return *target, nil
}

func (s *Server) createAutomation(w http.ResponseWriter, r *http.Request) {
	ws, err := s.workspaceFor(r)
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	input := map[string]any{}
	if err := decodeBody(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	title := strings.TrimSpace(stringFromAny(input["title"]))
	assigneeType, assigneeID := strings.TrimSpace(stringFromAny(input["assignee_type"])), strings.TrimSpace(stringFromAny(input["assignee_id"]))
	if assigneeType == "" {
		assigneeType = "agent"
	}
	if title == "" || assigneeID == "" || (assigneeType != "agent" && assigneeType != "squad") {
		writeError(w, http.StatusBadRequest, errors.New("title and a valid assignee are required"))
		return
	}
	if err := s.requireAutomationAssignee(r, ws.ID, assigneeType, assigneeID); err != nil {
		s.respondStoreError(w, err)
		return
	}
	id, stamp := uuid.NewString(), now()
	value := map[string]any{"id": id, "workspace_id": ws.ID, "title": title, "description": nil, "project_id": nil, "assignee_type": assigneeType, "assignee_id": assigneeID, "status": "active", "pause_reason": nil, "execution_mode": "create_issue", "issue_title_template": nil, "created_by_type": "member", "created_by_id": LocalOperatorID, "last_run_at": nil, "created_at": stamp, "updated_at": stamp, "subscribers": []any{}, "can_write": true, "can_manage_access": true}
	mergeJSONMap(value, input, "id", "workspace_id", "created_by_type", "created_by_id", "created_at", "updated_at", "last_run_at", "can_write", "can_manage_access")
	raw, _ := encodeJSONMap(value)
	if _, err := s.store.db.ExecContext(r.Context(), `INSERT INTO automations(id, workspace_id, title, status, assignee_type, assignee_id, data, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, id, ws.ID, title, stringFromAny(value["status"]), assigneeType, assigneeID, raw, stamp, stamp); err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, value)
}

func (s *Server) requireAutomationAssignee(r *http.Request, workspaceID, kind, id string) error {
	var found string
	if kind == "agent" {
		return s.store.db.QueryRowContext(r.Context(), `SELECT id FROM agents WHERE id=? AND workspace_id=? AND archived_at IS NULL`, id, workspaceID).Scan(&found)
	}
	return s.store.db.QueryRowContext(r.Context(), `SELECT id FROM squads WHERE id=? AND workspace_id=? AND archived_at IS NULL`, id, workspaceID).Scan(&found)
}

func (s *Server) automationItem(w http.ResponseWriter, r *http.Request) {
	ws, err := s.workspaceFor(r)
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	id := chi.URLParam(r, "id")
	item, err := s.store.readAutomation(id, ws.ID)
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	if r.Method == http.MethodGet {
		triggers, err := s.listAutomationTriggers(r, id)
		if err != nil {
			s.fail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"autopilot": item, "triggers": triggers, "collaborators": []any{}})
		return
	}
	if r.Method == http.MethodDelete {
		tx, err := s.store.db.BeginTx(r.Context(), nil)
		if err != nil {
			s.fail(w, err)
			return
		}
		defer func() { _ = tx.Rollback() }()
		_, _ = tx.ExecContext(r.Context(), `DELETE FROM automation_runs WHERE automation_id=?`, id)
		_, _ = tx.ExecContext(r.Context(), `DELETE FROM automation_triggers WHERE automation_id=?`, id)
		if _, err := tx.ExecContext(r.Context(), `DELETE FROM automations WHERE id=? AND workspace_id=?`, id, ws.ID); err != nil {
			s.fail(w, err)
			return
		}
		if err := tx.Commit(); err != nil {
			s.fail(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	input := map[string]any{}
	if err := decodeBody(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	assigneeType, assigneeID := stringFromAny(item["assignee_type"]), stringFromAny(item["assignee_id"])
	if candidate := strings.TrimSpace(stringFromAny(input["assignee_type"])); candidate != "" {
		assigneeType = candidate
	}
	if candidate := strings.TrimSpace(stringFromAny(input["assignee_id"])); candidate != "" {
		assigneeID = candidate
	}
	if err := s.requireAutomationAssignee(r, ws.ID, assigneeType, assigneeID); err != nil {
		s.respondStoreError(w, err)
		return
	}
	mergeJSONMap(item, input, "id", "workspace_id", "created_by_type", "created_by_id", "created_at", "updated_at", "last_run_at", "can_write", "can_manage_access")
	stamp := now()
	item["updated_at"] = stamp
	raw, _ := encodeJSONMap(item)
	if _, err := s.store.db.ExecContext(r.Context(), `UPDATE automations SET title=?, status=?, assignee_type=?, assignee_id=?, data=?, updated_at=? WHERE id=? AND workspace_id=?`, stringFromAny(item["title"]), stringFromAny(item["status"]), assigneeType, assigneeID, raw, stamp, id, ws.ID); err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func (s *Server) listAutomationTriggers(r *http.Request, automationID string) ([]map[string]any, error) {
	rows, err := s.store.db.QueryContext(r.Context(), `SELECT id FROM automation_triggers WHERE automation_id=? ORDER BY created_at, id`, automationID)
	if err != nil {
		return nil, err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	_ = rows.Close()
	result := []map[string]any{}
	for _, id := range ids {
		item, err := s.store.readAutomationTrigger(id, automationID)
		if err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, nil
}

func nextRun(cronExpression, timezone string, after time.Time) (string, error) {
	schedule, err := localCronParser.Parse(cronExpression)
	if err != nil {
		return "", fmt.Errorf("invalid cron expression: %w", err)
	}
	location, err := time.LoadLocation(timezone)
	if err != nil {
		return "", fmt.Errorf("invalid timezone: %w", err)
	}
	next := schedule.Next(after.In(location))
	if next.IsZero() {
		return "", nil
	}
	return next.UTC().Format(time.RFC3339), nil
}

func (s *Server) createAutomationTrigger(w http.ResponseWriter, r *http.Request) {
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
	input := map[string]any{}
	if err := decodeBody(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	kind := stringFromAny(input["kind"])
	if kind != "schedule" && kind != "webhook" && kind != "api" {
		writeError(w, http.StatusBadRequest, errors.New("kind must be schedule, webhook, or api"))
		return
	}
	id, stamp := uuid.NewString(), now()
	value := map[string]any{"id": id, "autopilot_id": automationID, "kind": kind, "enabled": true, "cron_expression": nil, "timezone": nil, "next_run_at": nil, "webhook_token": nil, "webhook_path": nil, "webhook_url": nil, "label": nil, "event_filters": nil, "last_fired_at": nil, "created_at": stamp, "updated_at": stamp}
	mergeJSONMap(value, input, "id", "autopilot_id", "created_at", "updated_at", "last_fired_at", "webhook_path", "webhook_url", "signing_secret", "webhook_secret")
	signingSecret := strings.TrimSpace(stringFromAny(input["signing_secret"]))
	if signingSecret == "" {
		signingSecret = strings.TrimSpace(stringFromAny(input["webhook_secret"]))
	}
	if kind == "webhook" {
		token := uuid.NewString()
		value["webhook_token"], value["webhook_path"] = token, "/api/webhooks/autopilots/"+token
		baseURL, err := localCallbackBaseURL(r)
		if err != nil {
			writeError(w, http.StatusFailedDependency, err)
			return
		}
		value["webhook_url"] = baseURL + stringFromAny(value["webhook_path"])
	}
	if kind == "schedule" {
		expression, timezone := stringFromAny(input["cron_expression"]), stringFromAny(input["timezone"])
		if timezone == "" {
			timezone = "UTC"
			value["timezone"] = timezone
		}
		next, err := nextRun(expression, timezone, time.Now())
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		value["next_run_at"] = next
	}
	raw, _ := encodeJSONMap(value)
	enabled, _ := value["enabled"].(bool)
	if _, err := s.store.db.ExecContext(r.Context(), `INSERT INTO automation_triggers(id, automation_id, kind, enabled, data, signing_secret, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, id, automationID, kind, enabled, raw, nullable(signingSecret), stamp, stamp); err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, value)
}

func (s *Server) automationTriggerItem(w http.ResponseWriter, r *http.Request) {
	ws, err := s.workspaceFor(r)
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	automationID, triggerID := chi.URLParam(r, "id"), chi.URLParam(r, "triggerID")
	if _, err := s.store.readAutomation(automationID, ws.ID); err != nil {
		s.respondStoreError(w, err)
		return
	}
	item, err := s.store.readAutomationTrigger(triggerID, automationID)
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	if r.Method == http.MethodDelete {
		if _, err := s.store.db.ExecContext(r.Context(), `DELETE FROM automation_triggers WHERE id=? AND automation_id=?`, triggerID, automationID); err != nil {
			s.fail(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	input := map[string]any{}
	if err := decodeBody(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	mergeJSONMap(item, input, "id", "autopilot_id", "kind", "created_at", "updated_at", "last_fired_at", "webhook_path", "webhook_url", "signing_secret", "webhook_secret")
	if stringFromAny(item["kind"]) == "schedule" {
		next, err := nextRun(stringFromAny(item["cron_expression"]), stringFromAny(item["timezone"]), time.Now())
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		item["next_run_at"] = next
	}
	stamp := now()
	item["updated_at"] = stamp
	raw, _ := encodeJSONMap(item)
	enabled, _ := item["enabled"].(bool)
	signingSecret := strings.TrimSpace(stringFromAny(input["signing_secret"]))
	if signingSecret == "" {
		signingSecret = strings.TrimSpace(stringFromAny(input["webhook_secret"]))
	}
	query, args := `UPDATE automation_triggers SET enabled=?, data=?, updated_at=?`, []any{enabled, raw, stamp}
	if signingSecret != "" {
		query += `, signing_secret=?`
		args = append(args, signingSecret)
	}
	query += ` WHERE id=? AND automation_id=?`
	args = append(args, triggerID, automationID)
	if _, err := s.store.db.ExecContext(r.Context(), query, args...); err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func (s *Server) rotateAutomationWebhook(w http.ResponseWriter, r *http.Request) {
	ws, err := s.workspaceFor(r)
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	automationID, triggerID := chi.URLParam(r, "id"), chi.URLParam(r, "triggerID")
	if _, err := s.store.readAutomation(automationID, ws.ID); err != nil {
		s.respondStoreError(w, err)
		return
	}
	item, err := s.store.readAutomationTrigger(triggerID, automationID)
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	if stringFromAny(item["kind"]) != "webhook" {
		writeError(w, http.StatusBadRequest, errors.New("trigger is not a webhook"))
		return
	}
	token, stamp := uuid.NewString(), now()
	item["webhook_token"], item["webhook_path"], item["updated_at"] = token, "/api/webhooks/autopilots/"+token, stamp
	baseURL, err := localCallbackBaseURL(r)
	if err != nil {
		writeError(w, http.StatusFailedDependency, err)
		return
	}
	item["webhook_url"] = baseURL + stringFromAny(item["webhook_path"])
	raw, _ := encodeJSONMap(item)
	if _, err := s.store.db.ExecContext(r.Context(), `UPDATE automation_triggers SET data=?, updated_at=? WHERE id=? AND automation_id=?`, raw, stamp, triggerID, automationID); err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func (s *Server) cronPreview(w http.ResponseWriter, r *http.Request) {
	expression, timezone := r.URL.Query().Get("expr"), r.URL.Query().Get("tz")
	if timezone == "" {
		timezone = "UTC"
	}
	cursor := time.Now()
	values := []string{}
	for range 5 {
		next, err := nextRun(expression, timezone, cursor)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		if next == "" {
			break
		}
		values = append(values, next)
		cursor, _ = time.Parse(time.RFC3339, next)
	}
	writeJSON(w, http.StatusOK, map[string]any{"next_runs": values})
}

func (s *Server) triggerAutomation(w http.ResponseWriter, r *http.Request) {
	s.runAutomation(w, r, chi.URLParam(r, "id"), "manual", nil)
}

func (s *Server) inboundAutomationWebhook(w http.ResponseWriter, r *http.Request) {
	s.receiveAutomationWebhook(w, r, chi.URLParam(r, "token"))
}

func (s *Server) runAutomation(w http.ResponseWriter, r *http.Request, automationID, source string, payload map[string]any) {
	ws, err := s.workspaceFor(r)
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	run, status, err := s.executeAutomation(r.Context(), ws.ID, automationID, source, payload, "")
	if err != nil {
		if errors.Is(err, errAutomationInactive) {
			writeError(w, http.StatusConflict, err)
		} else {
			s.respondStoreError(w, err)
		}
		return
	}
	writeJSON(w, status, run)
}

var errAutomationInactive = errors.New("automation is not active")

// executeAutomation owns the durable automation-to-task transition. A supplied
// run ID makes scheduler and webhook retries idempotent across process restarts.
func (s *Server) executeAutomation(ctx context.Context, workspaceID, automationID, source string, payload map[string]any, runID string) (map[string]any, int, error) {
	if runID != "" {
		var raw string
		if err := s.store.db.QueryRowContext(ctx, `SELECT data FROM automation_runs WHERE id=? AND automation_id=?`, runID, automationID).Scan(&raw); err == nil {
			value, decodeErr := decodeJSONMap(raw)
			if decodeErr == nil {
				decodeErr = s.notifyAutomationRun(ctx, workspaceID, automationID, value)
			}
			return value, http.StatusOK, decodeErr
		} else if !errors.Is(err, sql.ErrNoRows) {
			return nil, 0, err
		}
	}
	automation, err := s.store.readAutomation(automationID, workspaceID)
	if err != nil {
		return nil, 0, err
	}
	if stringFromAny(automation["status"]) != "active" {
		return nil, 0, errAutomationInactive
	}
	assigneeType, assigneeID := stringFromAny(automation["assignee_type"]), stringFromAny(automation["assignee_id"])
	executionAgentID := assigneeID
	if assigneeType == "squad" {
		if err := s.store.db.QueryRowContext(ctx, `SELECT leader_id FROM squads WHERE id=? AND workspace_id=? AND archived_at IS NULL`, assigneeID, workspaceID).Scan(&executionAgentID); err != nil {
			return nil, 0, err
		}
	}
	status, reasonCode := "skipped", "runtime_unavailable"
	var runtimeID string
	if err := s.store.db.QueryRowContext(ctx, `SELECT a.runtime_id FROM agents a JOIN runtimes rt ON rt.id=a.runtime_id WHERE a.id=? AND a.workspace_id=? AND a.archived_at IS NULL AND rt.status='online'`, executionAgentID, workspaceID).Scan(&runtimeID); err == nil {
		status, reasonCode = "issue_created", ""
	}
	stamp := now()
	if runID == "" {
		runID = uuid.NewString()
	}
	run := map[string]any{"id": runID, "autopilot_id": automationID, "trigger_id": nil, "source": source, "status": status, "issue_id": nil, "task_id": nil, "triggered_at": stamp, "completed_at": nil, "failure_reason": nil, "reason_code": reasonCode, "trigger_payload": payload, "result": map[string]any{}, "created_at": stamp}
	if payload != nil {
		run["trigger_id"] = payload["trigger_id"]
	}
	if status == "issue_created" {
		projectID := strings.TrimSpace(stringFromAny(automation["project_id"]))
		if projectID == "" {
			status, reasonCode = "failed", "project_required"
			run["status"], run["reason_code"], run["failure_reason"] = status, reasonCode, "automation requires a project"
		} else {
			title := strings.TrimSpace(stringFromAny(automation["issue_title_template"]))
			if title == "" {
				title = stringFromAny(automation["title"])
			}
			var issue Issue
			var existingIssueID string
			lookupErr := s.store.db.QueryRowContext(ctx, `SELECT id FROM issues WHERE workspace_id=? AND json_extract(metadata, '$.automation_run_id')=? LIMIT 1`, workspaceID, runID).Scan(&existingIssueID)
			if lookupErr == nil {
				// A prior process created the task and stopped before recording the run.
				issue, err = s.store.GetIssue(ctx, workspaceID, existingIssueID)
			} else if errors.Is(lookupErr, sql.ErrNoRows) {
				issue, err = s.store.CreateIssue(ctx, workspaceID, CreateIssueInput{Title: title, ProjectID: &projectID, AssigneeType: &assigneeType, AssigneeID: &assigneeID, Metadata: map[string]any{"automation_run_id": runID}})
			} else {
				err = lookupErr
			}
			if err != nil {
				status, reasonCode = "failed", "issue_create_failed"
				run["status"], run["reason_code"], run["failure_reason"] = status, reasonCode, err.Error()
			} else {
				run["issue_id"] = issue.ID
				if err := s.store.enqueueAssignedIssue(ctx, workspaceID, issue.ID, runID); err != nil {
					status, reasonCode = "failed", "task_enqueue_failed"
					run["status"], run["reason_code"], run["failure_reason"] = status, reasonCode, err.Error()
				} else {
					var taskID string
					if err := s.store.db.QueryRowContext(ctx, `SELECT id FROM task_runs WHERE issue_id=? ORDER BY created_at DESC, id DESC LIMIT 1`, issue.ID).Scan(&taskID); err == nil {
						run["task_id"] = taskID
					}
				}
			}
		}
	}
	raw, _ := encodeJSONMap(run)
	if _, err := s.store.db.ExecContext(ctx, `INSERT INTO automation_runs(id, automation_id, trigger_id, source, status, issue_id, task_id, data, triggered_at, completed_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, runID, automationID, run["trigger_id"], source, stringFromAny(run["status"]), run["issue_id"], run["task_id"], raw, stamp, run["completed_at"]); err != nil {
		return nil, 0, err
	}
	updated := automation
	updated["last_run_at"], updated["updated_at"] = stamp, stamp
	automationRaw, _ := encodeJSONMap(updated)
	_, _ = s.store.db.ExecContext(ctx, `UPDATE automations SET data=?, updated_at=? WHERE id=?`, automationRaw, stamp, automationID)
	if err := s.notifyAutomationRun(ctx, workspaceID, automationID, run); err != nil {
		return nil, 0, err
	}
	return run, http.StatusCreated, nil
}

func (s *Server) notifyAutomationRun(ctx context.Context, workspaceID, automationID string, run map[string]any) error {
	status := stringFromAny(run["status"])
	reasonCode := stringFromAny(run["reason_code"])
	if status != "failed" && !(status == "skipped" && reasonCode == "runtime_unavailable") {
		return nil
	}
	var title string
	if err := s.store.db.QueryRowContext(ctx, `SELECT title FROM automations WHERE id=? AND workspace_id=?`, automationID, workspaceID).Scan(&title); err != nil {
		return err
	}
	reason := strings.TrimSpace(stringFromAny(run["failure_reason"]))
	if reason == "" && reasonCode == "runtime_unavailable" {
		reason = "The assigned Agent has no online Runtime."
	}
	runID := stringFromAny(run["id"])
	return s.createLocalNotification(ctx, localNotificationInput{
		WorkspaceID: workspaceID, Type: "automation_failed", Severity: "action_required",
		SourceKey: "automation-run:" + runID + ":failed", IssueID: stringFromAny(run["issue_id"]),
		Title: "Automation needs attention: " + title, Body: reason,
		Details: map[string]string{"destination": "automation", "automation_id": automationID, "run_id": runID, "reason_code": reasonCode},
	})
}

func (s *Server) listAutomationRuns(w http.ResponseWriter, r *http.Request) {
	s.automationRuns(w, r, "")
}

func (s *Server) getAutomationRun(w http.ResponseWriter, r *http.Request) {
	s.automationRuns(w, r, chi.URLParam(r, "runID"))
}

func (s *Server) automationRuns(w http.ResponseWriter, r *http.Request, runID string) {
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
	if runID != "" {
		var raw string
		if err := s.store.db.QueryRowContext(r.Context(), `SELECT data FROM automation_runs WHERE id=? AND automation_id=?`, runID, automationID).Scan(&raw); err != nil {
			s.respondStoreError(w, err)
			return
		}
		value, _ := decodeJSONMap(raw)
		writeJSON(w, http.StatusOK, value)
		return
	}
	rows, err := s.store.db.QueryContext(r.Context(), `SELECT data FROM automation_runs WHERE automation_id=? ORDER BY triggered_at DESC, id LIMIT ? OFFSET ?`, automationID, parseLimit(r, 50), parseOffset(r))
	if err != nil {
		s.fail(w, err)
		return
	}
	items := []map[string]any{}
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			_ = rows.Close()
			s.fail(w, err)
			return
		}
		item, _ := decodeJSONMap(raw)
		delete(item, "trigger_payload")
		items = append(items, item)
	}
	_ = rows.Close()
	var total int
	_ = s.store.db.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM automation_runs WHERE automation_id=?`, automationID).Scan(&total)
	writeJSON(w, http.StatusOK, map[string]any{"runs": items, "total": total})
}

func parseOffset(r *http.Request) int {
	value, err := strconv.Atoi(r.URL.Query().Get("offset"))
	if err != nil || value < 0 {
		return 0
	}
	return value
}

func parseLimit(r *http.Request, fallback int) int {
	value, err := strconv.Atoi(r.URL.Query().Get("limit"))
	if err != nil || value <= 0 {
		return fallback
	}
	if value > 500 {
		return 500
	}
	return value
}

func (s *Server) automationUsage(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"action": "off", "used": nil, "reserved": nil, "limit": nil, "period_start": nil, "period_end": nil, "reset_at": nil, "blocked_counts": nil})
}

func (s *Server) localAutomationCollaborators(w http.ResponseWriter, r *http.Request) {
	ws, err := s.workspaceFor(r)
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	if _, err := s.store.readAutomation(chi.URLParam(r, "id"), ws.ID); err != nil {
		s.respondStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"collaborators": []map[string]any{{"user_id": LocalOperatorID, "name": "Local Operator", "email": "local@multica.invalid", "can_write": true}}})
}
