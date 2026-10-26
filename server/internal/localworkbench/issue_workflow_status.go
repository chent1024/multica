package localworkbench

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/multica-ai/multica/server/internal/issueworkflow"
)

func archiveLocalWorkflowStatusTx(ctx context.Context, tx *sql.Tx, ws, key string) error {
	rows, err := tx.QueryContext(ctx, `SELECT id,name,is_default,steps FROM issue_workflows WHERE workspace_id=? ORDER BY name,id`, ws)
	if err != nil {
		return err
	}
	type row struct {
		id, name, raw string
		isDefault     bool
	}
	all := []row{}
	for rows.Next() {
		var w row
		if err := rows.Scan(&w.id, &w.name, &w.isDefault, &w.raw); err != nil {
			_ = rows.Close()
			return err
		}
		all = append(all, w)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, w := range all {
		steps, err := issueworkflow.DecodeSteps([]byte(w.raw))
		if err != nil {
			return err
		}
		next := []issueworkflow.Step{}
		found := false
		for _, step := range steps {
			if step.StatusKey == key {
				found = true
			} else {
				next = append(next, step)
			}
		}
		if !found {
			continue
		}
		if !w.isDefault {
			return workflowError(409, "issue_status_in_workflow", fmt.Sprintf("cannot archive status: workflow %q still uses it; remove it from the workflow first", w.name))
		}
		encoded, err := issueworkflow.EncodeSteps(next)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE issue_workflows SET steps=?,updated_at=? WHERE id=? AND workspace_id=?`, string(encoded), now(), w.id, ws); err != nil {
			return err
		}
	}
	return nil
}
