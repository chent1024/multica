package localworkbench

import (
	"context"
	"database/sql"
)

// A queued step is an instruction snapshot. Once its issue or workflow moves,
// retire that snapshot so it cannot poison the oldest-task claim or run late.
func cancelStaleLocalWorkflowTasksTx(ctx context.Context, tx *sql.Tx, ws, runtime string) error {
	query := `UPDATE task_runs AS t SET status='cancelled',completed_at=?,updated_at=?
 WHERE t.status IN ('queued','dispatched','waiting_local_directory','waiting_previous_run') AND t.started_at IS NULL AND t.workflow_id IS NOT NULL
 AND EXISTS(SELECT 1 FROM issues i JOIN projects p ON p.id=i.project_id AND p.workspace_id=i.workspace_id WHERE i.id=t.issue_id AND i.workspace_id=?
 AND ((t.workflow_step IS NOT NULL AND t.workflow_step<>i.status) OR t.workflow_id<>COALESCE(p.workflow_id,(SELECT id FROM issue_workflows WHERE workspace_id=i.workspace_id AND is_default=1))))`
	stamp := now()
	args := []any{stamp, stamp, ws}
	if runtime != "" {
		query += ` AND t.runtime=?`
		args = append(args, runtime)
	}
	_, err := tx.ExecContext(ctx, query, args...)
	return err
}
