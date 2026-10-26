package localworkbench

import (
	"context"
	"database/sql"
)

func deleteLocalWakeupOwnedRowsTx(ctx context.Context, tx *sql.Tx, id string) error {
	for _, table := range []string{"issue_wakeup_activations", "issue_wakeup_condition_state", "issue_wakeup_receipts", "issue_wakeup_runs"} {
		if _, err := tx.ExecContext(ctx, `DELETE FROM `+table+` WHERE wakeup_id=?`, id); err != nil {
			return err
		}
	}
	// A dispatched claim keeps its reservation until start validation. Removing
	// it here would let a daemon execute the deleted rule's cached instruction.
	return nil
}

func deleteLocalWakeupsForIssueTx(ctx context.Context, tx *sql.Tx, ws, issue string) error {
	rows, err := tx.QueryContext(ctx, `SELECT id FROM issue_wakeups WHERE workspace_id=? AND issue_id=?`, ws, issue)
	if err != nil {
		return err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, id := range ids {
		if err := deleteLocalWakeupOwnedRowsTx(ctx, tx, id); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM issue_wakeup_joins WHERE task_id IN (SELECT id FROM task_runs WHERE issue_id=?)`, issue); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM issue_system_wakeups WHERE workspace_id=? AND issue_id=?`, ws, issue); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM issue_wakeups WHERE workspace_id=? AND issue_id=?`, ws, issue); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `DELETE FROM issue_wakeup_events WHERE workspace_id=? AND issue_id=?`, ws, issue)
	return err
}
