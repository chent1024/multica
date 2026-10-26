package localworkbench

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
)

const localWakeupColumns = `id, workspace_id, issue_id, agent_id, created_by, config, enabled, revision, next_fire_at, expires_at, disabled_at, last_task_id, last_error, timed_out_at, paused_reason, fire_count, created_at, updated_at, source_task_id`

func scanLocalWakeup(row rowScanner) (localWakeup, error) {
	var w localWakeup
	var config string
	if err := row.Scan(&w.ID, &w.WorkspaceID, &w.IssueID, &w.AgentID, &w.CreatedBy, &config, &w.Enabled, &w.Revision, &w.NextFireAt, &w.ExpiresAt, &w.DisabledAt, &w.LastTaskID, &w.LastError, &w.TimedOutAt, &w.PausedReason, &w.FireCount, &w.CreatedAt, &w.UpdatedAt, &w.SourceTaskID); err != nil {
		return w, err
	}
	if err := json.Unmarshal([]byte(config), &w.Config); err != nil {
		return w, fmt.Errorf("decode wakeup configuration: %w", err)
	}
	return w, nil
}

func (s *Store) localWakeup(ctx context.Context, workspaceID, issueID, id string) (localWakeup, error) {
	return scanLocalWakeup(s.db.QueryRowContext(ctx, `SELECT `+localWakeupColumns+` FROM issue_wakeups WHERE workspace_id=? AND issue_id=? AND id=?`, workspaceID, issueID, id))
}

func (s *Store) listLocalWakeups(ctx context.Context, workspaceID, issueID string) ([]localWakeup, error) {
	return s.queryLocalWakeups(ctx, workspaceID, issueID, false)
}

func (s *Store) allLocalWakeups(ctx context.Context, workspaceID string) ([]localWakeup, error) {
	return s.queryLocalWakeups(ctx, workspaceID, "", true)
}

func (s *Store) queryLocalWakeups(ctx context.Context, workspaceID, issueID string, includeSystem bool) ([]localWakeup, error) {
	query, args := `SELECT `+localWakeupColumns+` FROM issue_wakeups WHERE workspace_id=?`, []any{workspaceID}
	if !includeSystem {
		query += ` AND id NOT IN (SELECT wakeup_id FROM issue_system_wakeups WHERE wakeup_id IS NOT NULL)`
	}
	if issueID != "" {
		query += ` AND issue_id=?`
		args = append(args, issueID)
	}
	query += ` ORDER BY created_at DESC,id DESC`
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []localWakeup{}
	for rows.Next() {
		w, err := scanLocalWakeup(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

func putLocalWakeupTx(ctx context.Context, tx *sql.Tx, w localWakeup) error {
	var oldEnabled bool
	var oldRevision int64
	oldErr := tx.QueryRowContext(ctx, `SELECT enabled,revision FROM issue_wakeups WHERE id=? AND workspace_id=? AND issue_id=?`, w.ID, w.WorkspaceID, w.IssueID).Scan(&oldEnabled, &oldRevision)
	if oldErr != nil && !errors.Is(oldErr, sql.ErrNoRows) {
		return oldErr
	}
	config, err := json.Marshal(w.Config)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO issue_wakeups (`+localWakeupColumns+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
 ON CONFLICT(id) DO UPDATE SET config=excluded.config,agent_id=excluded.agent_id,enabled=excluded.enabled,revision=excluded.revision,next_fire_at=excluded.next_fire_at,expires_at=excluded.expires_at,disabled_at=excluded.disabled_at,last_task_id=excluded.last_task_id,last_error=excluded.last_error,timed_out_at=excluded.timed_out_at,paused_reason=excluded.paused_reason,fire_count=excluded.fire_count,updated_at=excluded.updated_at,source_task_id=excluded.source_task_id
 WHERE issue_wakeups.workspace_id=excluded.workspace_id AND issue_wakeups.issue_id=excluded.issue_id`, w.ID, w.WorkspaceID, w.IssueID, w.AgentID, w.CreatedBy, string(config), w.Enabled, w.Revision, w.NextFireAt, w.ExpiresAt, w.DisabledAt, w.LastTaskID, w.LastError, w.TimedOutAt, w.PausedReason, w.FireCount, w.CreatedAt, w.UpdatedAt, w.SourceTaskID)
	if err != nil {
		return err
	}
	if errors.Is(oldErr, sql.ErrNoRows) || (w.Enabled && (!oldEnabled || oldRevision != w.Revision)) {
		if _, err := tx.ExecContext(ctx, `INSERT INTO issue_wakeup_activations(wakeup_id,event_cursor) VALUES(?,COALESCE((SELECT MAX(id) FROM issue_wakeup_events),0)) ON CONFLICT(wakeup_id) DO UPDATE SET event_cursor=excluded.event_cursor`, w.ID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM issue_wakeup_condition_state WHERE wakeup_id=?`, w.ID); err != nil {
			return err
		}
	}
	return nil
}
