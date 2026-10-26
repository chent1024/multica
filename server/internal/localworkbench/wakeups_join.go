package localworkbench

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/multica-ai/multica/server/internal/service"
)

// Claiming reserves inputs for the same local operator/agent. Starting the run
// consumes them; a failed claim leaves receipts owned by the rule for recovery.
func joinLocalWakeupsTx(ctx context.Context, tx *sql.Tx, ws, issue, agent, task string, at time.Time) (string, error) {
	if _, err := tx.ExecContext(ctx, `DELETE FROM issue_wakeup_joins WHERE task_id=?`, task); err != nil {
		return "", err
	}
	rows, err := tx.QueryContext(ctx, `SELECT `+localWakeupColumns+` FROM issue_wakeups WHERE workspace_id=? AND issue_id=? AND agent_id=? AND enabled=1 ORDER BY created_at,id`, ws, issue, agent)
	if err != nil {
		return "", err
	}
	rules := []localWakeup{}
	for rows.Next() {
		w, err := scanLocalWakeup(rows)
		if err != nil {
			_ = rows.Close()
			return "", err
		}
		rules = append(rules, w)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return "", err
	}
	if err := rows.Close(); err != nil {
		return "", err
	}
	notes := []string{}
	for _, w := range rules {
		if w.ExpiresAt.Valid {
			deadline, err := time.Parse(time.RFC3339Nano, w.ExpiresAt.String)
			if err != nil {
				return "", err
			}
			if !deadline.After(at) {
				continue
			}
		}
		receipts, causes, err := pendingLocalWakeupReceiptsTx(ctx, tx, w.ID)
		if err != nil {
			return "", err
		}
		if len(receipts) == 0 {
			continue
		}
		prompt, err := localWakeupPromptNoteTx(ctx, tx, w)
		if err != nil {
			return "", err
		}
		note := fmt.Sprintf("Wakeup %s\n%s\nTriggers: %s", w.ID, prompt, strings.Join(causes, ", "))
		if len(strings.Join(notes, "\n\n"))+len(note) > 12000 {
			continue
		}
		encoded, err := json.Marshal(receipts)
		if err != nil {
			return "", err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO issue_wakeup_joins(task_id,wakeup_id,revision,receipt_ids,created_at) VALUES(?,?,?,?,?)`, task, w.ID, w.Revision, string(encoded), at.Format(time.RFC3339Nano)); err != nil {
			return "", err
		}
		notes = append(notes, note)
	}
	note := strings.Join(notes, "\n\n")
	_, err = tx.ExecContext(ctx, `UPDATE task_runs SET wakeup_joined=? WHERE id=?`, note, task)
	return note, err
}

func pendingLocalWakeupReceiptsTx(ctx context.Context, tx *sql.Tx, id string) ([]string, []string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id,event_type FROM issue_wakeup_receipts WHERE wakeup_id=? AND task_id IS NULL ORDER BY created_at,id LIMIT 100`, id)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	ids, causes := []string{}, []string{}
	seen := map[string]bool{}
	for rows.Next() {
		var rid, kind string
		if err := rows.Scan(&rid, &kind); err != nil {
			return nil, nil, err
		}
		ids = append(ids, rid)
		if !seen[kind] {
			causes = append(causes, kind)
			seen[kind] = true
		}
	}
	return ids, causes, rows.Err()
}

func takeLocalJoinedWakeupsTx(ctx context.Context, tx *sql.Tx, ws, issue, task string, at time.Time) error {
	rows, err := tx.QueryContext(ctx, `SELECT wakeup_id,revision,receipt_ids FROM issue_wakeup_joins WHERE task_id=? ORDER BY wakeup_id`, task)
	if err != nil {
		return err
	}
	type reservation struct {
		id       string
		revision int64
		raw      string
	}
	all := []reservation{}
	for rows.Next() {
		var item reservation
		if err := rows.Scan(&item.id, &item.revision, &item.raw); err != nil {
			_ = rows.Close()
			return err
		}
		all = append(all, item)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, item := range all {
		w, err := scanLocalWakeup(tx.QueryRowContext(ctx, `SELECT `+localWakeupColumns+` FROM issue_wakeups WHERE id=? AND workspace_id=? AND issue_id=?`, item.id, ws, issue))
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("%w: joined wakeup was deleted before start", service.ErrWakeupConflict)
		}
		if err != nil {
			return err
		}
		if !w.Enabled || w.Revision != item.revision {
			return fmt.Errorf("%w: joined wakeup changed before start; claim the run again", service.ErrWakeupConflict)
		}
		var ids []string
		if err := json.Unmarshal([]byte(item.raw), &ids); err != nil {
			return err
		}
		causes := []string{}
		for _, id := range ids {
			var kind string
			err := tx.QueryRowContext(ctx, `SELECT event_type FROM issue_wakeup_receipts WHERE id=? AND wakeup_id=? AND task_id IS NULL`, id, w.ID).Scan(&kind)
			if errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("%w: joined wakeup inputs changed before start", service.ErrWakeupConflict)
			}
			if err != nil {
				return err
			}
			causes = append(causes, kind)
		}
		if len(ids) > 0 {
			if err := finishLocalWakeupDeliveryTx(ctx, tx, w, task, ids, causes, at); err != nil {
				return err
			}
		}
	}
	_, err = tx.ExecContext(ctx, `DELETE FROM issue_wakeup_joins WHERE task_id=?`, task)
	return err
}
