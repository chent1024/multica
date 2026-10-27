package localworkbench

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

func (s *Server) runLocalWakeupScheduler(ctx context.Context) {
	defer s.wg.Done()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		if err := s.tickLocalWakeups(ctx, time.Now().UTC()); err != nil && ctx.Err() == nil {
			s.logger.Error("evaluate local wakeups", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (s *Server) tickLocalWakeups(ctx context.Context, at time.Time) error {
	tx, err := s.store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := cancelStaleLocalWorkflowTasksTx(ctx, tx, LocalWorkspaceID, ""); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}

	rows, err := s.store.db.QueryContext(ctx, `SELECT id,workspace_id,issue_id FROM issue_wakeups WHERE enabled=1 AND id NOT IN (SELECT wakeup_id FROM issue_system_wakeups WHERE wakeup_id IS NOT NULL) ORDER BY updated_at,id LIMIT 1000`)
	if err != nil {
		return err
	}
	type key struct{ id, ws, issue string }
	keys := []key{}
	for rows.Next() {
		var k key
		if err := rows.Scan(&k.id, &k.ws, &k.issue); err != nil {
			_ = rows.Close()
			return err
		}
		keys = append(keys, k)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	var failures []error
	for _, k := range keys {
		if _, err := s.processLocalWakeup(ctx, k.ws, k.issue, k.id, at, false); err != nil && !errors.Is(err, sql.ErrNoRows) {
			failures = append(failures, fmt.Errorf("wakeup %s: %w", k.id, err))
		}
	}
	if err := s.tickLocalSystemWakeups(ctx, at); err != nil {
		failures = append(failures, err)
	}
	return errors.Join(failures...)
}

func (s *Server) triggerLocalWakeup(ctx context.Context, workspaceID, issueID, id string) (string, error) {
	return s.processLocalWakeup(ctx, workspaceID, issueID, id, time.Now().UTC(), true)
}

func (s *Server) processLocalWakeup(ctx context.Context, workspaceID, issueID, id string, at time.Time, manual bool) (string, error) {
	tx, err := s.store.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback() }()
	w, err := scanLocalWakeup(tx.QueryRowContext(ctx, `SELECT `+localWakeupColumns+` FROM issue_wakeups WHERE workspace_id=? AND issue_id=? AND id=?`, workspaceID, issueID, id))
	if err != nil {
		return "", err
	}
	if !w.Enabled {
		if manual {
			return "", fmt.Errorf("%w: wakeup is disabled", service.ErrWakeupInput)
		}
		return "", nil
	}
	if err := localWakeupIssueOpen(ctx, tx, workspaceID, issueID); err != nil {
		if !errors.Is(err, service.ErrWakeupInput) && !errors.Is(err, sql.ErrNoRows) {
			return "", err
		}
		w.Enabled = false
		w.DisabledAt = localWakeupTime(at)
		w.LastError = sql.NullString{String: "issue is closed or unavailable", Valid: true}
		if err := clearLocalWakeupPendingTx(ctx, tx, w.ID); err != nil {
			return "", err
		}
		if err := putLocalWakeupTx(ctx, tx, w); err != nil {
			return "", err
		}
		return "", tx.Commit()
	}
	expired := false
	if w.ExpiresAt.Valid {
		deadline, err := time.Parse(time.RFC3339Nano, w.ExpiresAt.String)
		if err != nil {
			return "", err
		}
		expired = !deadline.After(at)
	}
	if expired && !w.TimedOutAt.Valid {
		w.TimedOutAt = localWakeupTime(at)
		if w.Config.OnTimeout != "wake" || w.Config.Kind != "event" {
			w.Enabled = false
			w.DisabledAt = sql.NullString{}
			if err := clearLocalWakeupPendingTx(ctx, tx, w.ID); err != nil {
				return "", err
			}
			if err := putLocalWakeupTx(ctx, tx, w); err != nil {
				return "", err
			}
			return "", tx.Commit()
		}
		if err := captureLocalWakeupReceipt(ctx, tx, w, "timeout:"+fmt.Sprint(w.Revision), "wakeup.timeout", "", map[string]any{"expires_at": w.ExpiresAt.String, "on_timeout": w.Config.OnTimeout}, at); err != nil {
			return "", err
		}
	}
	if manual {
		origin, _ := ctx.Value(localWakeupOriginKey{}).(localWakeupOrigin)
		actorType, actorID := "member", LocalOperatorID
		if origin.AgentID != "" {
			actorType, actorID = "agent", origin.AgentID
		}
		if err := captureLocalWakeupReceipt(ctx, tx, w, "manual:"+uuid.NewString(), "wakeup.manual", origin.TaskID, map[string]any{"requested_at": at.Format(time.RFC3339Nano), "actor_type": actorType, "actor_id": actorID}, at); err != nil {
			return "", err
		}
	} else if !w.TimedOutAt.Valid {
		if len(w.Config.Condition) > 0 && string(w.Config.Condition) != "null" {
			if err := s.captureLocalWakeupCondition(ctx, tx, w, at); err != nil {
				w.LastError = sql.NullString{String: err.Error(), Valid: true}
				w.UpdatedAt = at.Format(time.RFC3339Nano)
				if saveErr := putLocalWakeupTx(ctx, tx, w); saveErr != nil {
					return "", saveErr
				}
				return "", tx.Commit()
			}
		} else if w.Config.Kind == "event" {
			acknowledged, err := captureLocalWakeupEvents(ctx, tx, w, at)
			if err != nil {
				return "", err
			}
			if acknowledged && w.Config.Mode == "once" {
				var pending int
				if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM issue_wakeup_receipts WHERE wakeup_id=? AND task_id IS NULL`, w.ID).Scan(&pending); err != nil {
					return "", err
				}
				if pending == 0 {
					w.Enabled = false
					w.DisabledAt = sql.NullString{}
					w.UpdatedAt = at.Format(time.RFC3339Nano)
					if err := putLocalWakeupTx(ctx, tx, w); err != nil {
						return "", err
					}
					return "", tx.Commit()
				}
			}
		} else if w.NextFireAt.Valid {
			next, err := time.Parse(time.RFC3339Nano, w.NextFireAt.String)
			if err != nil {
				return "", err
			}
			if !next.After(at) {
				if err := captureLocalWakeupReceipt(ctx, tx, w, "schedule:"+w.NextFireAt.String, "time.due", "", map[string]any{"scheduled_for": w.NextFireAt.String, "timezone": w.Config.Timezone}, at); err != nil {
					return "", err
				}
			}
		}
	}
	receiptRows, err := tx.QueryContext(ctx, `SELECT id,event_type,COALESCE(source_task_id,'') FROM issue_wakeup_receipts WHERE wakeup_id=? AND task_id IS NULL ORDER BY created_at,id LIMIT 100`, w.ID)
	if err != nil {
		return "", err
	}
	receiptIDs, causes, sources := []string{}, []string{}, []string{}
	for receiptRows.Next() {
		var rid, cause, source string
		if err := receiptRows.Scan(&rid, &cause, &source); err != nil {
			_ = receiptRows.Close()
			return "", err
		}
		receiptIDs = append(receiptIDs, rid)
		if !slices.Contains(causes, cause) {
			causes = append(causes, cause)
		}
		if source != "" {
			sources = append(sources, source)
		}
	}
	if err := receiptRows.Err(); err != nil {
		_ = receiptRows.Close()
		return "", err
	}
	if err := receiptRows.Close(); err != nil {
		return "", err
	}
	if len(receiptIDs) == 0 {
		w.LastError = sql.NullString{}
		if err := putLocalWakeupTx(ctx, tx, w); err != nil {
			return "", err
		}
		return "", tx.Commit()
	}
	maxFires := int(w.Config.MaxFires)
	if maxFires == 0 && w.Config.Kind == "event" && w.Config.Mode == "continuous" {
		maxFires = 20
	}
	if maxFires > 0 && w.FireCount >= maxFires {
		return "", pauseLocalWakeupTx(ctx, tx, w, "max_fires", at)
	}
	var recent int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM issue_wakeup_runs WHERE wakeup_id=? AND julianday(created_at)>julianday(?)`, w.ID, at.Add(-time.Hour).Format(time.RFC3339Nano)).Scan(&recent); err != nil {
		return "", err
	}
	if recent >= 12 {
		return "", pauseLocalWakeupTx(ctx, tx, w, "rate", at)
	}
	for _, source := range sources {
		loop, err := localWakeupLoop(ctx, tx, w.ID, source)
		if err != nil {
			return "", err
		}
		if loop {
			return "", pauseLocalWakeupTx(ctx, tx, w, "loop", at)
		}
	}
	var active string
	err = tx.QueryRowContext(ctx, `SELECT id FROM task_runs WHERE issue_id=? AND status IN ('queued','dispatched','running','waiting_local_directory','waiting_previous_run') ORDER BY created_at LIMIT 1`, issueID).Scan(&active)
	if err == nil {
		if err := putLocalWakeupTx(ctx, tx, w); err != nil {
			return "", err
		}
		return active, tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	taskID, err := enqueueLocalWakeupTaskTx(ctx, tx, w, at)
	if err != nil {
		w.LastError = sql.NullString{String: err.Error(), Valid: true}
		w.UpdatedAt = at.Format(time.RFC3339Nano)
		if err := putLocalWakeupTx(ctx, tx, w); err != nil {
			return "", err
		}
		if err := tx.Commit(); err != nil {
			return "", err
		}
		if manual {
			return "", fmt.Errorf("%w: %s", service.ErrWakeupInput, w.LastError.String)
		}
		return "", nil
	}
	if err := finishLocalWakeupDeliveryTx(ctx, tx, w, taskID, receiptIDs, causes, at); err != nil {
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	s.hub.broadcast(protocol.EventDaemonTaskAvailable, map[string]any{"issue_id": issueID, "task_id": taskID})
	s.hub.broadcast("task:queued", map[string]any{"issue_id": issueID, "task_id": taskID, "agent_id": w.AgentID})
	return taskID, nil
}

func localWakeupTime(at time.Time) sql.NullString {
	return sql.NullString{String: at.UTC().Format(time.RFC3339Nano), Valid: true}
}

func captureLocalWakeupReceipt(ctx context.Context, tx *sql.Tx, w localWakeup, key, kind, source string, payload any, at time.Time) error {
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO issue_wakeup_receipts(id,wakeup_id,event_key,event_type,payload,source_task_id,created_at) VALUES(?,?,?,?,?,?,?)`, uuid.NewString(), w.ID, fmt.Sprintf("r%d:%s", w.Revision, key), kind, string(raw), nullable(source), at.Format(time.RFC3339Nano))
	return err
}

func captureLocalWakeupEvents(ctx context.Context, tx *sql.Tx, w localWakeup, at time.Time) (bool, error) {
	acknowledged := false
	var cursor int64
	if err := tx.QueryRowContext(ctx, `SELECT event_cursor FROM issue_wakeup_activations WHERE wakeup_id=?`, w.ID).Scan(&cursor); err != nil {
		return false, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT id,event_type,COALESCE(actor_type,''),COALESCE(actor_id,''),COALESCE(source_task_id,''),COALESCE(source_agent_id,''),payload FROM issue_wakeup_events WHERE workspace_id=? AND issue_id=? AND id>? ORDER BY id LIMIT 200`, w.WorkspaceID, w.IssueID, cursor)
	if err != nil {
		return false, err
	}
	type event struct {
		id                                             int64
		kind, actorType, actorID, task, agent, payload string
	}
	events := []event{}
	for rows.Next() {
		var e event
		if err := rows.Scan(&e.id, &e.kind, &e.actorType, &e.actorID, &e.task, &e.agent, &e.payload); err != nil {
			_ = rows.Close()
			return false, err
		}
		events = append(events, e)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return false, err
	}
	if err := rows.Close(); err != nil {
		return false, err
	}
	for _, e := range events {
		cursor = e.id
		if !slices.Contains(w.Config.EventTypes, e.kind) || w.Config.FilterTaskID != "" && w.Config.FilterTaskID != e.task || w.Config.FilterAgentID != "" && w.Config.FilterAgentID != e.agent || w.Config.FilterActorType != "" && (w.Config.FilterActorType != e.actorType || w.Config.FilterActorID != e.actorID) {
			continue
		}
		if e.actorType == "agent" && e.actorID == w.AgentID && !strings.HasPrefix(e.kind, "task.") {
			acknowledged = true
			continue
		}
		payload := map[string]any{}
		if err := json.Unmarshal([]byte(e.payload), &payload); err != nil {
			return false, err
		}
		payload["actor_type"], payload["actor_id"], payload["source_task_id"] = e.actorType, e.actorID, e.task
		if err := captureLocalWakeupReceipt(ctx, tx, w, fmt.Sprint(e.id), e.kind, e.task, payload, at); err != nil {
			return false, err
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE issue_wakeup_activations SET event_cursor=? WHERE wakeup_id=?`, cursor, w.ID); err != nil {
		return false, err
	}
	if w.Config.FilterTaskID != "" && w.FireCount == 0 {
		var state, agent string
		err := tx.QueryRowContext(ctx, `SELECT status,COALESCE(agent_id,'') FROM task_runs WHERE id=? AND issue_id=?`, w.Config.FilterTaskID, w.IssueID).Scan(&state, &agent)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return false, err
		}
		kind := "task." + state
		if err == nil && (state == "completed" || state == "failed" || state == "cancelled") && slices.Contains(w.Config.EventTypes, kind) && (w.Config.FilterAgentID == "" || w.Config.FilterAgentID == agent) {
			err := captureLocalWakeupReceipt(ctx, tx, w, "registration_snapshot:"+w.Config.FilterTaskID, kind, w.Config.FilterTaskID, map[string]any{"task_id": w.Config.FilterTaskID}, at)
			return acknowledged, err
		}
	}
	if acknowledged {
		if err := recordLocalWakeupActivityTx(ctx, tx, w, "wakeup_triggered", map[string]any{"outcome": "acknowledged", "triggers": w.Config.EventTypes}, at); err != nil {
			return false, err
		}
	}
	return acknowledged, nil
}

func (s *Server) captureLocalWakeupCondition(ctx context.Context, tx *sql.Tx, w localWakeup, at time.Time) error {
	met, evidence, err := evaluateLocalWakeupCondition(ctx, tx, w.WorkspaceID, w.IssueID, w.Config.Condition)
	if err != nil {
		return err
	}
	var previous string
	err = tx.QueryRowContext(ctx, `SELECT evidence FROM issue_wakeup_condition_state WHERE wakeup_id=?`, w.ID).Scan(&previous)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if met && evidence != previous {
		if err := captureLocalWakeupReceipt(ctx, tx, w, "condition:"+uuid.NewString(), "condition.met", "", map[string]any{"condition": json.RawMessage(w.Config.Condition), "evidence": evidence}, at); err != nil {
			return err
		}
	}
	if !met {
		evidence = ""
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO issue_wakeup_condition_state(wakeup_id,evidence,updated_at) VALUES(?,?,?) ON CONFLICT(wakeup_id) DO UPDATE SET evidence=excluded.evidence,updated_at=excluded.updated_at`, w.ID, evidence, at.Format(time.RFC3339Nano))
	return err
}

func pauseLocalWakeupTx(ctx context.Context, tx *sql.Tx, w localWakeup, reason string, at time.Time) error {
	w.Enabled = false
	w.PausedReason = sql.NullString{String: reason, Valid: true}
	w.DisabledAt = localWakeupTime(at)
	w.UpdatedAt = at.Format(time.RFC3339Nano)
	if err := putLocalWakeupTx(ctx, tx, w); err != nil {
		return err
	}
	if err := recordLocalWakeupActivityTx(ctx, tx, w, "wakeup_paused", map[string]any{"reason": reason}, at); err != nil {
		return err
	}
	return tx.Commit()
}

func localWakeupLoop(ctx context.Context, tx *sql.Tx, wakeupID, taskID string) (bool, error) {
	repeats := 0
	seen := map[string]bool{}
	for depth := 0; taskID != "" && depth < 32; depth++ {
		if seen[taskID] {
			return true, nil
		}
		seen[taskID] = true
		var source, rule sql.NullString
		err := tx.QueryRowContext(ctx, `SELECT t.wakeup_id,w.source_task_id FROM task_runs t LEFT JOIN issue_wakeups w ON w.id=t.wakeup_id WHERE t.id=?`, taskID).Scan(&rule, &source)
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		if rule.String == wakeupID {
			repeats++
			if repeats >= 2 {
				return true, nil
			}
		}
		if !rule.Valid {
			return false, nil
		}
		// Follow the event that started this particular run, not just rule creation.
		var causalTask sql.NullString
		err = tx.QueryRowContext(ctx, `SELECT source_task_id FROM issue_wakeup_receipts WHERE task_id=? AND source_task_id IS NOT NULL ORDER BY created_at,id LIMIT 1`, taskID).Scan(&causalTask)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return false, err
		}
		taskID = causalTask.String
		if taskID == "" {
			taskID = source.String
		}
	}
	return taskID != "", nil
}

func enqueueLocalWakeupTaskTx(ctx context.Context, tx *sql.Tx, w localWakeup, at time.Time) (string, error) {
	note, err := localWakeupPromptNoteTx(ctx, tx, w)
	if err != nil {
		return "", err
	}
	id, err := enqueueLocalIssueAgentTaskTx(ctx, tx, w.WorkspaceID, w.IssueID, w.AgentID, note, w.ID, at)
	if err != nil {
		return "", err
	}
	if w.Config.ParentCommentID != "" {
		if _, err := tx.ExecContext(ctx, `UPDATE task_runs SET trigger_comment_id=? WHERE id=?`, w.Config.ParentCommentID, id); err != nil {
			return "", err
		}
	}
	return id, nil
}

func recordLocalWakeupActivityTx(ctx context.Context, tx *sql.Tx, w localWakeup, action string, details map[string]any, at time.Time) error {
	details["wakeup"] = localWakeupPreview(w)
	details["wakeup_id"] = w.ID
	details["agent_id"] = w.AgentID
	raw, err := json.Marshal(details)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO activities(id,issue_id,actor_type,actor_id,action,details,created_at) VALUES(?,?,'system',?,?,?,?)`, uuid.NewString(), w.IssueID, LocalOperatorID, action, string(raw), at.Format(time.RFC3339Nano))
	return err
}

func (s *Server) checkInLocalWakeup(ctx context.Context, workspaceID, issueID, id, taskID, note string) error {
	note = strings.TrimSpace(note)
	if note == "" || utf8.RuneCountInString(note) > 500 {
		return fmt.Errorf("%w: note must contain 1–500 characters", service.ErrWakeupInput)
	}
	tx, err := s.store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	w, err := scanLocalWakeup(tx.QueryRowContext(ctx, `SELECT `+localWakeupColumns+` FROM issue_wakeups WHERE workspace_id=? AND issue_id=? AND id=?`, workspaceID, issueID, id))
	if err != nil {
		return err
	}
	var state string
	if err := tx.QueryRowContext(ctx, `SELECT status FROM task_runs WHERE id=? AND issue_id=? AND agent_id=? AND wakeup_id=?`, taskID, issueID, w.AgentID, id).Scan(&state); err != nil {
		return err
	}
	if state != "running" {
		return fmt.Errorf("%w: check-in requires the current wakeup run", service.ErrWakeupConflict)
	}
	if w.Config.Kind != "every" && w.Config.Kind != "cron" {
		return fmt.Errorf("%w: check-ins are for scheduled checks; post a comment instead", service.ErrWakeupInput)
	}
	result, err := tx.ExecContext(ctx, `UPDATE issue_wakeup_runs SET checkin_note=? WHERE task_id=? AND wakeup_id=?`, note, taskID, id)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return sql.ErrNoRows
	}
	if err := recordLocalWakeupActivityTx(ctx, tx, w, "wakeup_checkin", map[string]any{"task_id": taskID, "note": note}, time.Now().UTC()); err != nil {
		return err
	}
	return tx.Commit()
}

func finishLocalWakeupDeliveryTx(ctx context.Context, tx *sql.Tx, w localWakeup, taskID string, receiptIDs, causes []string, at time.Time) error {
	maxFires := int(w.Config.MaxFires)
	if maxFires == 0 && w.Config.Kind == "event" && w.Config.Mode == "continuous" {
		maxFires = 20
	}
	occurrence := receiptIDs[0]
	encodedCauses, err := json.Marshal(causes)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO issue_wakeup_runs(task_id,wakeup_id,occurrence_key,triggers,created_at) VALUES(?,?,?,?,?)`, taskID, w.ID, occurrence, string(encodedCauses), at.Format(time.RFC3339Nano)); err != nil {
		return err
	}
	for _, rid := range receiptIDs {
		if _, err := tx.ExecContext(ctx, `UPDATE issue_wakeup_receipts SET task_id=? WHERE id=? AND task_id IS NULL`, taskID, rid); err != nil {
			return err
		}
	}
	w.FireCount++
	w.LastTaskID = sql.NullString{String: taskID, Valid: true}
	w.LastError = sql.NullString{}
	w.UpdatedAt = at.Format(time.RFC3339Nano)
	if w.Config.Mode == "once" || w.TimedOutAt.Valid {
		w.Enabled = false
		w.DisabledAt = sql.NullString{}
	} else {
		switch w.Config.Kind {
		case "every":
			interval := time.Duration(w.Config.IntervalSeconds) * time.Second
			base := at
			if w.NextFireAt.Valid {
				parsed, err := time.Parse(time.RFC3339Nano, w.NextFireAt.String)
				if err != nil {
					return err
				}
				base = parsed
			}
			steps := int64(at.Sub(base)/interval) + 1
			if steps < 1 {
				steps = 1
			}
			w.NextFireAt = localWakeupTime(base.Add(time.Duration(steps) * interval))
		case "cron":
			next, err := service.NextOccurrenceAfterUTC(w.Config.CronExpression, w.Config.Timezone, at)
			if err != nil {
				return err
			}
			w.NextFireAt = localWakeupTime(next)
		}
		if maxFires > 0 && w.FireCount >= maxFires {
			w.Enabled = false
			w.DisabledAt = localWakeupTime(at)
			w.PausedReason = sql.NullString{String: "max_fires", Valid: true}
		}
	}
	if err := putLocalWakeupTx(ctx, tx, w); err != nil {
		return err
	}
	if err := recordLocalWakeupActivityTx(ctx, tx, w, "wakeup_triggered", map[string]any{"task_id": taskID, "triggers": causes}, at); err != nil {
		return err
	}

	return nil
}

func localWakeupPromptNoteTx(ctx context.Context, tx *sql.Tx, w localWakeup) (string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT event_type,payload FROM issue_wakeup_receipts WHERE wakeup_id=? AND task_id IS NULL ORDER BY created_at,id LIMIT 100`, w.ID)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	var note strings.Builder
	note.WriteString(w.Config.Instruction)
	note.WriteString("\n\n[Wakeup trigger facts]\n")
	for rows.Next() {
		var kind, raw string
		if err := rows.Scan(&kind, &raw); err != nil {
			return "", err
		}
		if note.Len()+len(raw)+len(kind) > 24000 {
			note.WriteString("Additional captured facts omitted from this prompt.\n")
			break
		}
		note.WriteString(kind + " " + raw + "\n")
	}
	return note.String(), rows.Err()
}
