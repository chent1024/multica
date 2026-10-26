package localworkbench

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

var (
	errLocalDuplicateInput             = errors.New("invalid duplicate mark")
	errLocalDuplicateTargetIsDuplicate = errors.New("duplicate target is itself a duplicate")
	errLocalIssueHasDuplicates         = errors.New("issue has duplicates")
)

type localIssueDuplicateRef struct {
	ID         string `json:"id"`
	Identifier string `json:"identifier"`
	Title      string `json:"title"`
	Status     string `json:"status"`
}

// Every query applies the same validity guard; old clients may have left a
// pointer behind after reopening an issue or removing its original.
const localIssueDuplicateSelect = `CASE WHEN i.status='cancelled' AND EXISTS (
 SELECT 1 FROM issues original WHERE original.id=i.duplicate_of_issue_id
 AND original.workspace_id=i.workspace_id) THEN i.duplicate_of_issue_id ELSE NULL END`

func writeLocalDuplicateError(w http.ResponseWriter, err error) bool {
	switch {
	case errors.Is(err, errLocalDuplicateInput):
		writeError(w, http.StatusBadRequest, err)
	case errors.Is(err, errLocalDuplicateTargetIsDuplicate):
		writeJSON(w, http.StatusConflict, map[string]any{"error": "the target issue is itself a duplicate; use its original instead", "code": "duplicate_target_is_duplicate"})
	case errors.Is(err, errLocalIssueHasDuplicates):
		writeJSON(w, http.StatusConflict, map[string]any{"error": "other issues are marked as duplicates of this issue; remove those marks first", "code": "issue_has_duplicates"})
	default:
		return false
	}
	return true
}

func prepareLocalDuplicateMark(ctx context.Context, tx *sql.Tx, workspaceID, issueID, currentStatus string, currentPointer sql.NullString, updates map[string]any) (bool, bool, error) {
	raw, present := updates["duplicate_of_issue_id"]
	if !present {
		return false, false, nil
	}
	value, ok := raw.(string)
	if !ok {
		return false, false, fmt.Errorf("%w: duplicate_of_issue_id must be a UUID; change status to remove a mark", errLocalDuplicateInput)
	}
	target, err := uuid.Parse(strings.TrimSpace(value))
	if err != nil {
		return false, false, fmt.Errorf("%w: duplicate_of_issue_id must be a UUID", errLocalDuplicateInput)
	}
	targetID := target.String()
	if targetID == issueID {
		return false, false, fmt.Errorf("%w: an issue cannot duplicate itself", errLocalDuplicateInput)
	}
	if status, present := updates["status"]; present && status != "cancelled" {
		return false, false, fmt.Errorf("%w: a duplicate is cancelled; status must be cancelled or omitted", errLocalDuplicateInput)
	}
	for _, key := range []string{"description", "title_base", "description_base"} {
		if _, present := updates[key]; present {
			return false, false, fmt.Errorf("%w: duplicate marks cannot be combined with description or attachment changes", errLocalDuplicateInput)
		}
	}
	if rawIDs, present := updates["attachment_ids"]; present {
		ids, err := attachmentIDs(rawIDs)
		if err != nil {
			return false, false, fmt.Errorf("%w: %v", errLocalDuplicateInput, err)
		}
		if len(ids) > 0 {
			return false, false, fmt.Errorf("%w: duplicate marks cannot be combined with description or attachment changes", errLocalDuplicateInput)
		}
	}
	var targetStatus string
	var liveTargetPointer sql.NullString
	err = tx.QueryRowContext(ctx, `SELECT i.status,`+localIssueDuplicateSelect+` FROM issues i WHERE i.id=? AND i.workspace_id=?`, targetID, workspaceID).Scan(&targetStatus, &liveTargetPointer)
	if errors.Is(err, sql.ErrNoRows) {
		return false, false, fmt.Errorf("%w: duplicate target not found in this workspace", errLocalDuplicateInput)
	}
	if err != nil {
		return false, false, err
	}
	if liveTargetPointer.Valid {
		return false, false, errLocalDuplicateTargetIsDuplicate
	}
	var incoming int
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM issues WHERE workspace_id=? AND duplicate_of_issue_id=? AND status='cancelled'`, workspaceID, issueID).Scan(&incoming); err != nil {
		return false, false, err
	}
	if incoming > 0 {
		return false, false, errLocalIssueHasDuplicates
	}
	updates["duplicate_of_issue_id"], updates["status"] = targetID, "cancelled"
	sameMark := currentStatus == "cancelled" && currentPointer.Valid && currentPointer.String == targetID
	for key := range updates {
		switch key {
		case "duplicate_of_issue_id", "status", "suppress_run", "handoff_note", "attachment_ids":
		default:
			sameMark = false
		}
	}
	return true, sameMark, nil
}

func endLocalIssueWakeupsTx(ctx context.Context, tx *sql.Tx, workspaceID, issueID, stamp string) error {
	if _, err := tx.ExecContext(ctx, `UPDATE issue_wakeups SET enabled=0,disabled_at=COALESCE(disabled_at,?),updated_at=? WHERE workspace_id=? AND issue_id=? AND id NOT IN (SELECT wakeup_id FROM issue_system_wakeups WHERE wakeup_id IS NOT NULL)`, stamp, stamp, workspaceID, issueID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE task_runs SET status='cancelled',completed_at=?,updated_at=?,error='Issue closed; wakeup disabled' WHERE issue_id=? AND wakeup_id IS NOT NULL AND status IN ('queued','dispatched','waiting_local_directory','waiting_previous_run') AND started_at IS NULL`, stamp, stamp, issueID); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `DELETE FROM issue_wakeup_receipts WHERE wakeup_id IN (SELECT id FROM issue_wakeups WHERE workspace_id=? AND issue_id=?)`, workspaceID, issueID)
	return err
}

func (s *Server) listIssueDuplicates(w http.ResponseWriter, r *http.Request) {
	ws, err := s.workspaceFor(r)
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	issue, err := s.store.GetIssue(r.Context(), ws.ID, chi.URLParam(r, "id"))
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	var original *Issue
	if issue.DuplicateOfIssueID != nil {
		target, err := s.store.GetIssue(r.Context(), ws.ID, *issue.DuplicateOfIssueID)
		if err == nil {
			original = &target
		} else if !errors.Is(err, sql.ErrNoRows) {
			s.respondStoreError(w, err)
			return
		}
	}
	rows, err := s.store.db.QueryContext(r.Context(), `SELECT id FROM issues WHERE workspace_id=? AND duplicate_of_issue_id=? AND status='cancelled'`, ws.ID, issue.ID)
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			_ = rows.Close()
			s.respondStoreError(w, err)
			return
		}
		ids = append(ids, id)
	}
	if err = rows.Err(); err != nil {
		_ = rows.Close()
		s.respondStoreError(w, err)
		return
	}
	if err = rows.Close(); err != nil {
		s.respondStoreError(w, err)
		return
	}
	duplicates := []Issue{}
	for _, id := range ids {
		value, err := s.store.GetIssue(r.Context(), ws.ID, id)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			s.respondStoreError(w, err)
			return
		}
		if value.DuplicateOfIssueID != nil && *value.DuplicateOfIssueID == issue.ID {
			duplicates = append(duplicates, value)
		}
	}
	sort.Slice(duplicates, func(i, j int) bool {
		a, _ := time.Parse(time.RFC3339Nano, duplicates[i].CreatedAt)
		b, _ := time.Parse(time.RFC3339Nano, duplicates[j].CreatedAt)
		if !a.Equal(b) {
			return a.Before(b)
		}
		return duplicates[i].ID < duplicates[j].ID
	})
	writeJSON(w, http.StatusOK, map[string]any{"duplicate_of": original, "duplicates": duplicates})
}
