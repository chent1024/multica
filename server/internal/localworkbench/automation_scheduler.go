package localworkbench

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
)

type dueAutomationTrigger struct {
	id           string
	automationID string
	nextRunAt    string
	data         map[string]any
}

func (s *Server) runAutomationScheduler(ctx context.Context) {
	defer s.wg.Done()
	s.processDueAutomations(ctx)
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.processDueAutomations(ctx)
		}
	}
}

func (s *Server) processDueAutomations(ctx context.Context) {
	triggers, err := s.dueAutomationTriggers(ctx)
	if err != nil {
		if !errors.Is(err, context.Canceled) {
			s.logger.Error("list due local automations", "error", err)
		}
		return
	}
	for _, trigger := range triggers {
		runID, err := s.reserveAutomationOccurrence(ctx, trigger)
		if err != nil {
			if !errors.Is(err, context.Canceled) {
				s.logger.Error("reserve local automation occurrence", "trigger_id", trigger.id, "error", err)
			}
			continue
		}
		if runID == "" {
			continue
		}
		payload := map[string]any{"trigger_id": trigger.id, "scheduled_for": trigger.nextRunAt}
		_, _, executeErr := s.executeAutomation(ctx, LocalWorkspaceID, trigger.automationID, "schedule", payload, runID)
		stamp := now()
		status, failure := "completed", any(nil)
		if executeErr != nil {
			status, failure = "failed", executeErr.Error()
			s.logger.Error("execute local scheduled automation", "trigger_id", trigger.id, "run_id", runID, "error", executeErr)
		}
		_, _ = s.store.db.ExecContext(ctx, `UPDATE automation_occurrences SET status=?, error=?, updated_at=? WHERE trigger_id=? AND occurrence_key=?`, status, failure, stamp, trigger.id, trigger.nextRunAt)
	}
}

func (s *Server) dueAutomationTriggers(ctx context.Context) ([]dueAutomationTrigger, error) {
	rows, err := s.store.db.QueryContext(ctx, `SELECT t.id, t.automation_id, json_extract(t.data, '$.next_run_at'), t.data
		FROM automation_triggers t JOIN automations a ON a.id=t.automation_id
		WHERE t.kind='schedule' AND t.enabled=1 AND a.status='active'
		AND json_extract(t.data, '$.next_run_at') IS NOT NULL
		AND json_extract(t.data, '$.next_run_at') <= ?
		ORDER BY json_extract(t.data, '$.next_run_at'), t.id LIMIT 100`, now())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []dueAutomationTrigger{}
	for rows.Next() {
		var item dueAutomationTrigger
		var raw string
		if err := rows.Scan(&item.id, &item.automationID, &item.nextRunAt, &raw); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(raw), &item.data); err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func (s *Server) reserveAutomationOccurrence(ctx context.Context, trigger dueAutomationTrigger) (string, error) {
	tx, err := s.store.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback() }()
	var runID, occurrenceStatus string
	err = tx.QueryRowContext(ctx, `SELECT COALESCE(run_id, ''), status FROM automation_occurrences WHERE trigger_id=? AND occurrence_key=?`, trigger.id, trigger.nextRunAt).Scan(&runID, &occurrenceStatus)
	if errors.Is(err, sql.ErrNoRows) {
		runID = uuid.NewString()
		stamp := now()
		if _, err := tx.ExecContext(ctx, `INSERT INTO automation_occurrences(trigger_id, occurrence_key, run_id, status, created_at, updated_at) VALUES (?, ?, ?, 'running', ?, ?)`, trigger.id, trigger.nextRunAt, runID, stamp, stamp); err != nil {
			return "", err
		}
	} else if err != nil {
		return "", err
	} else if occurrenceStatus == "completed" {
		runID = ""
	}

	expression, timezone := stringFromAny(trigger.data["cron_expression"]), stringFromAny(trigger.data["timezone"])
	scheduledFor, err := time.Parse(time.RFC3339, trigger.nextRunAt)
	if err != nil {
		return "", err
	}
	next, err := nextRun(expression, timezone, scheduledFor)
	if err != nil {
		return "", err
	}
	trigger.data["last_fired_at"], trigger.data["next_run_at"], trigger.data["updated_at"] = trigger.nextRunAt, next, now()
	raw, err := json.Marshal(trigger.data)
	if err != nil {
		return "", err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE automation_triggers SET data=?, updated_at=? WHERE id=? AND json_extract(data, '$.next_run_at')=?`, string(raw), trigger.data["updated_at"], trigger.id, trigger.nextRunAt); err != nil {
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	return runID, nil
}
