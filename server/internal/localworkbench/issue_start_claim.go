package localworkbench

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/multica-ai/multica/server/internal/service"
)

func validateLocalStartClaimTx(ctx context.Context, tx *sql.Tx, ws, task string, body map[string]any) (bool, error) {
	runtime, _ := body["runtime_id"].(string)
	dispatched, _ := body["dispatched_at"].(string)
	if runtime == "" && dispatched == "" {
		return false, nil
	}
	if runtime == "" || dispatched == "" {
		return false, fmt.Errorf("%w: runtime_id and dispatched_at are required together", service.ErrWakeupInput)
	}
	var actualRuntime, status string
	var token sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(t.runtime,''),t.status,t.dispatched_at FROM task_runs t JOIN issues i ON i.id=t.issue_id WHERE t.id=? AND i.workspace_id=?`, task, ws).Scan(&actualRuntime, &status, &token); err != nil {
		return false, err
	}
	if actualRuntime != runtime || !token.Valid || token.String != dispatched || (status != "dispatched" && status != "running" && status != "waiting_local_directory") {
		return false, fmt.Errorf("%w: task claim changed before start", service.ErrWakeupConflict)
	}
	return status == "running", nil
}
