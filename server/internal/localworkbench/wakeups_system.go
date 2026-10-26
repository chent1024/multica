package localworkbench

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/service"
)

type localSystemSettings struct {
	Enabled, Customized    bool
	Instruction            string
	Revision               int64
	WakeupID, LastEvidence string
}

func localSystemSettingsFor(ctx context.Context, q localWakeupQuery, ws, issue string) (localSystemSettings, error) {
	out := localSystemSettings{Enabled: true}
	err := q.QueryRowContext(ctx, `SELECT enabled,instruction,revision,customized,COALESCE(wakeup_id,''),last_evidence FROM issue_system_wakeups WHERE workspace_id=? AND issue_id=?`, ws, issue).Scan(&out.Enabled, &out.Instruction, &out.Revision, &out.Customized, &out.WakeupID, &out.LastEvidence)
	if errors.Is(err, sql.ErrNoRows) {
		return out, nil
	}
	return out, err
}

func (s *Server) localSystemWakeupDescription(ctx context.Context, q localWakeupQuery, ws, issueID string) (map[string]any, error) {
	var status, category string
	var assigneeType, assigneeID sql.NullString
	if err := q.QueryRowContext(ctx, `SELECT i.status,COALESCE(st.category,i.status),i.assignee_type,i.assignee_id FROM issues i LEFT JOIN issue_statuses st ON st.workspace_id=i.workspace_id AND st.key=i.status WHERE i.workspace_id=? AND i.id=?`, ws, issueID).Scan(&status, &category, &assigneeType, &assigneeID); err != nil {
		return nil, err
	}
	if localStatusCategory(category) == "closed" || category == "done" {
		return nil, nil
	}
	defaults, err := localSystemSettingsFor(ctx, q, ws, "")
	if err != nil {
		return nil, err
	}
	settings, err := localSystemSettingsFor(ctx, q, ws, issueID)
	if err != nil {
		return nil, err
	}
	enabled := defaults.Enabled
	instruction := ""
	revision := int64(0)
	if settings.Customized {
		enabled = settings.Enabled
		instruction = settings.Instruction
	}
	if settings.Revision > 0 {
		revision = settings.Revision
	}
	defaultInstruction := defaults.Instruction
	if defaultInstruction == "" {
		defaultInstruction = service.ChildDoneDefaultInstruction
	}
	rows, err := q.QueryContext(ctx, `SELECT i.number,COALESCE(st.category,i.status),i.stage,w.issue_prefix FROM issues i JOIN workspaces w ON w.id=i.workspace_id LEFT JOIN issue_statuses st ON st.workspace_id=i.workspace_id AND st.key=i.status WHERE i.workspace_id=? AND i.parent_issue_id=? ORDER BY i.stage,i.number`, ws, issueID)
	if err != nil {
		return nil, err
	}
	type child struct {
		number           int
		category, prefix string
		stage            sql.NullInt64
	}
	children := []child{}
	for rows.Next() {
		var c child
		if err := rows.Scan(&c.number, &c.category, &c.stage, &c.prefix); err != nil {
			_ = rows.Close()
			return nil, err
		}
		children = append(children, c)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if len(children) == 0 {
		return nil, nil
	}
	var stage *int64
	for _, c := range children {
		closed := localStatusCategory(c.category) == "closed" || c.category == "done"
		if !closed && c.stage.Valid && (stage == nil || c.stage.Int64 < *stage) {
			n := c.stage.Int64
			stage = &n
		}
	}
	total, remaining := 0, 0
	waiting := []string{}
	for _, c := range children {
		if stage != nil && (!c.stage.Valid || c.stage.Int64 > *stage) {
			continue
		}
		total++
		if localStatusCategory(c.category) != "closed" && c.category != "done" {
			remaining++
			if len(waiting) < 5 {
				waiting = append(waiting, fmt.Sprintf("%s-%d", c.prefix, c.number))
			}
		}
	}
	var target any
	blocked := ""
	switch assigneeType.String {
	case "agent":
		var name string
		err := q.QueryRowContext(ctx, `SELECT name FROM agents WHERE workspace_id=? AND id=? AND archived_at IS NULL`, ws, assigneeID.String).Scan(&name)
		if err == nil {
			target = map[string]any{"type": "agent", "id": assigneeID.String, "name": name}
		} else if !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
	case "squad":
		var name string
		err := q.QueryRowContext(ctx, `SELECT name FROM squads WHERE workspace_id=? AND id=? AND archived_at IS NULL`, ws, assigneeID.String).Scan(&name)
		if err == nil {
			target = map[string]any{"type": "squad", "id": assigneeID.String, "name": name}
		} else if !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
	case "member":
		if assigneeID.String == LocalOperatorID {
			target = map[string]any{"type": "member", "id": LocalOperatorID, "name": "Local Operator"}
			blocked = "member_assignee"
		}
	}
	if target == nil {
		blocked = "no_assignee"
	}
	if status == "backlog" {
		blocked = "backlog"
	}
	var paused any
	if settings.WakeupID != "" {
		var reason sql.NullString
		err := q.QueryRowContext(ctx, `SELECT enabled,paused_reason,revision FROM issue_wakeups WHERE id=?`, settings.WakeupID).Scan(&enabled, &reason, &revision)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		if reason.Valid {
			paused = reason.String
		}
	}
	return map[string]any{"id": settings.WakeupID, "revision": revision, "rule": "child_done", "enabled": enabled, "instruction": instruction, "default_instruction": defaultInstruction, "customized": settings.Customized, "paused_reason": paused, "staged": stage != nil, "stage": stage, "total": total, "remaining": remaining, "waiting": waiting, "target": target, "blocked": blocked, "workspace_default": defaults.Enabled}, nil
}

func (s *Server) listIssueSystemWakeups(w http.ResponseWriter, r *http.Request) {
	ws, issue, err := s.wakeupRequestIssue(r)
	if err != nil {
		s.localWakeupError(w, err)
		return
	}
	row, err := s.localSystemWakeupDescription(r.Context(), s.store.db, ws.ID, issue.ID)
	if err != nil {
		s.localWakeupError(w, err)
		return
	}
	out := []any{}
	if row != nil && row["remaining"].(int) > 0 {
		out = append(out, row)
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) updateIssueSystemWakeup(w http.ResponseWriter, r *http.Request) {
	ws, issue, err := s.wakeupRequestIssue(r)
	if err != nil {
		s.localWakeupError(w, err)
		return
	}
	if chi.URLParam(r, "rule") != "child_done" {
		s.localWakeupError(w, sql.ErrNoRows)
		return
	}
	if r.Header.Get("X-Agent-ID") != "" {
		s.localWakeupError(w, service.ErrWakeupForbidden)
		return
	}
	if err := s.updateLocalSystemSettings(r.Context(), ws.ID, issue.ID, w, r); err != nil {
		s.localWakeupError(w, err)
		return
	}
	s.listIssueSystemWakeups(w, r)
}

func (s *Server) listWorkspaceSystemWakeups(w http.ResponseWriter, r *http.Request) {
	ws, err := s.wakeupWorkspace(r)
	if err != nil {
		s.localWakeupError(w, err)
		return
	}
	d, err := localSystemSettingsFor(r.Context(), s.store.db, ws.ID, "")
	if err != nil {
		s.localWakeupError(w, err)
		return
	}
	var count int
	if err := s.store.db.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM issue_system_wakeups WHERE workspace_id=? AND issue_id<>'' AND customized=1`, ws.ID).Scan(&count); err != nil {
		s.localWakeupError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, []any{map[string]any{"rule": "child_done", "enabled": d.Enabled, "instruction": d.Instruction, "builtin_instruction": service.ChildDoneDefaultInstruction, "customized": count}})
}

func (s *Server) updateWorkspaceSystemWakeup(w http.ResponseWriter, r *http.Request) {
	ws, err := s.wakeupWorkspace(r)
	if err != nil {
		s.localWakeupError(w, err)
		return
	}
	if chi.URLParam(r, "rule") != "child_done" {
		s.localWakeupError(w, sql.ErrNoRows)
		return
	}
	if r.Header.Get("X-Agent-ID") != "" {
		s.localWakeupError(w, service.ErrWakeupForbidden)
		return
	}
	if err := s.updateLocalSystemSettings(r.Context(), ws.ID, "", w, r); err != nil {
		s.localWakeupError(w, err)
		return
	}
	s.listWorkspaceSystemWakeups(w, r)
}

func (s *Server) updateLocalSystemSettings(ctx context.Context, ws, issue string, w http.ResponseWriter, r *http.Request) error {
	var input struct {
		Enabled     *bool   `json:"enabled"`
		Instruction *string `json:"instruction"`
	}
	if err := decodeWakeupBody(w, r, &input, 16384); err != nil {
		return err
	}
	if input.Instruction != nil && len(*input.Instruction) > service.MaxSystemWakeupInstruction {
		return fmt.Errorf("%w: system wakeup instruction is too long", service.ErrWakeupInput)
	}
	tx, err := s.store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	current, err := localSystemSettingsFor(ctx, tx, ws, issue)
	if err != nil {
		return err
	}
	if issue != "" && !current.Customized {
		d, err := localSystemSettingsFor(ctx, tx, ws, "")
		if err != nil {
			return err
		}
		current.Enabled = d.Enabled
		current.Instruction = d.Instruction
	}
	if input.Enabled != nil {
		current.Enabled = *input.Enabled
	}
	if input.Instruction != nil {
		current.Instruction = strings.TrimSpace(*input.Instruction)
	}
	stamp := now()
	customized := issue != ""
	_, err = tx.ExecContext(ctx, `INSERT INTO issue_system_wakeups(workspace_id,issue_id,enabled,instruction,revision,customized,created_at,updated_at) VALUES(?,?,?,?,1,?,?,?) ON CONFLICT(workspace_id,issue_id) DO UPDATE SET enabled=excluded.enabled,instruction=excluded.instruction,customized=excluded.customized,revision=issue_system_wakeups.revision+1,updated_at=excluded.updated_at`, ws, issue, current.Enabled, current.Instruction, customized, stamp, stamp)
	if err != nil {
		return err
	}
	if issue != "" {
		if _, err := syncLocalSystemWakeupTx(ctx, tx, ws, issue, time.Now().UTC(), true); err != nil {
			return err
		}
	} else {
		rows, err := tx.QueryContext(ctx, `SELECT issue_id FROM issue_system_wakeups WHERE workspace_id=? AND issue_id<>'' AND customized=0`, ws)
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
			if _, err := syncLocalSystemWakeupTx(ctx, tx, ws, id, time.Now().UTC(), true); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

func syncLocalSystemWakeupTx(ctx context.Context, tx *sql.Tx, ws, issue string, at time.Time, reset bool) (localWakeup, error) {
	settings, err := localSystemSettingsFor(ctx, tx, ws, issue)
	if err != nil {
		return localWakeup{}, err
	}
	defaults, err := localSystemSettingsFor(ctx, tx, ws, "")
	if err != nil {
		return localWakeup{}, err
	}
	enabled, instruction := defaults.Enabled, defaults.Instruction
	if settings.Customized {
		enabled, instruction = settings.Enabled, settings.Instruction
	}
	if instruction == "" {
		instruction = service.ChildDoneDefaultInstruction
	}
	id := settings.WakeupID
	if id == "" {
		id = uuid.NewSHA1(uuid.NameSpaceOID, []byte("local-child-done:"+issue)).String()
	}
	w, err := scanLocalWakeup(tx.QueryRowContext(ctx, `SELECT `+localWakeupColumns+` FROM issue_wakeups WHERE id=? AND workspace_id=? AND issue_id=?`, id, ws, issue))
	if errors.Is(err, sql.ErrNoRows) {
		w = localWakeup{ID: id, WorkspaceID: ws, IssueID: issue, CreatedBy: LocalOperatorID, Enabled: enabled, Revision: 1, CreatedAt: at.Format(time.RFC3339Nano)}
	} else if err != nil {
		return w, err
	}
	var targetType, targetID sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT assignee_type,assignee_id FROM issues WHERE workspace_id=? AND id=?`, ws, issue).Scan(&targetType, &targetID); err != nil {
		return w, err
	}
	agentID := ""
	switch targetType.String {
	case "agent":
		agentID = targetID.String
	case "squad":
		err := tx.QueryRowContext(ctx, `SELECT leader_id FROM squads WHERE workspace_id=? AND id=? AND archived_at IS NULL`, ws, targetID.String).Scan(&agentID)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return w, err
		}
	}
	w.AgentID = agentID
	w.Config = service.WakeupInput{AgentID: agentID, Instruction: instruction, Kind: "event", Mode: "continuous", Timezone: "UTC", Condition: json.RawMessage(`{"type":"children_done","each_stage":true}`)}
	if reset {
		w.Revision++
		w.PausedReason = sql.NullString{}
		w.LastError = sql.NullString{}
		w.DisabledAt = sql.NullString{}
	}
	if !w.PausedReason.Valid {
		w.Enabled = enabled
	}
	w.UpdatedAt = at.Format(time.RFC3339Nano)
	if !w.Enabled {
		w.DisabledAt = localWakeupTime(at)
		if err := clearLocalWakeupPendingTx(ctx, tx, w.ID); err != nil {
			return w, err
		}
	}
	if err := putLocalWakeupTx(ctx, tx, w); err != nil {
		return w, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO issue_system_wakeups(workspace_id,issue_id,enabled,instruction,revision,created_at,updated_at,wakeup_id) VALUES(?,?,?,'',1,?,?,?) ON CONFLICT(workspace_id,issue_id) DO UPDATE SET wakeup_id=excluded.wakeup_id,updated_at=excluded.updated_at`, ws, issue, enabled, at.Format(time.RFC3339Nano), at.Format(time.RFC3339Nano), id)
	return w, err
}

func (s *Server) tickLocalSystemWakeups(ctx context.Context, at time.Time) error {
	rows, err := s.store.db.QueryContext(ctx, `SELECT DISTINCT p.workspace_id,p.id FROM issues p JOIN issues c ON c.parent_issue_id=p.id AND c.workspace_id=p.workspace_id ORDER BY p.id LIMIT 1000`)
	if err != nil {
		return err
	}
	type parent struct{ ws, id string }
	parents := []parent{}
	for rows.Next() {
		var p parent
		if err := rows.Scan(&p.ws, &p.id); err != nil {
			_ = rows.Close()
			return err
		}
		parents = append(parents, p)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, p := range parents {
		tx, err := s.store.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		rule, err := syncLocalSystemWakeupTx(ctx, tx, p.ws, p.id, at, false)
		if err == nil {
			var status, category string
			var targetType, targetID sql.NullString
			err = tx.QueryRowContext(ctx, `SELECT i.status,COALESCE(st.category,i.status),i.assignee_type,i.assignee_id FROM issues i LEFT JOIN issue_statuses st ON st.workspace_id=i.workspace_id AND st.key=i.status WHERE i.workspace_id=? AND i.id=?`, p.ws, p.id).Scan(&status, &category, &targetType, &targetID)
			if err == nil && (localStatusCategory(category) == "closed" || category == "done") {
				rule.Enabled = false
				rule.DisabledAt = localWakeupTime(at)
				err = clearLocalWakeupPendingTx(ctx, tx, rule.ID)
				if err == nil {
					err = putLocalWakeupTx(ctx, tx, rule)
				}
			} else if err == nil && rule.Enabled && status != "backlog" && targetType.String == "member" && targetID.String == LocalOperatorID {
				var met bool
				var evidence string
				met, evidence, err = evaluateLocalWakeupCondition(ctx, tx, p.ws, p.id, rule.Config.Condition)
				settings, getErr := localSystemSettingsFor(ctx, tx, p.ws, p.id)
				if err == nil {
					err = getErr
				}
				if err == nil && !met && settings.LastEvidence != "" {
					_, err = tx.ExecContext(ctx, `UPDATE issue_system_wakeups SET last_evidence='',updated_at=? WHERE workspace_id=? AND issue_id=?`, at.Format(time.RFC3339Nano), p.ws, p.id)
				}
				if err == nil && met && settings.LastEvidence != evidence {
					var sequence string
					sequence, err = localChildEvidenceSequence(ctx, tx, p.id)
					if err != nil {
						_ = tx.Rollback()
						return err
					}
					_, _, err = insertLocalNotificationTx(ctx, tx, localNotificationInput{WorkspaceID: p.ws, Type: "children_done", Severity: "info", IssueID: p.id, ActorType: "system", Title: "Sub-issues finished", Body: rule.Config.Instruction, SourceKey: "child_done:" + p.id + ":" + evidence + ":" + sequence, Details: map[string]string{"rule": "child_done", "evidence": evidence}})
					if err == nil {
						_, err = tx.ExecContext(ctx, `UPDATE issue_system_wakeups SET last_evidence=?,updated_at=? WHERE workspace_id=? AND issue_id=?`, evidence, at.Format(time.RFC3339Nano), p.ws, p.id)
					}
				}
			}
		}
		if err != nil {
			_ = tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
		// The generic transactional dispatcher owns run dedupe, receipts and limits.
		if rule.Enabled && rule.AgentID != "" {
			var status string
			if err := s.store.db.QueryRowContext(ctx, `SELECT status FROM issues WHERE id=? AND workspace_id=?`, p.id, p.ws).Scan(&status); err != nil {
				return err
			}
			if status != "backlog" {
				if _, err := s.processLocalWakeup(ctx, p.ws, p.id, rule.ID, at, false); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func (s *Server) localSystemWakeupInventory(ctx context.Context, workspaceID string) ([]map[string]any, error) {
	rows, err := s.store.db.QueryContext(ctx, `SELECT w.id,w.issue_id FROM issue_system_wakeups settings JOIN issue_wakeups w ON w.id=settings.wakeup_id WHERE settings.workspace_id=? AND settings.issue_id<>'' ORDER BY w.created_at DESC,w.id`, workspaceID)
	if err != nil {
		return nil, err
	}
	type key struct{ id, issue string }
	keys := []key{}
	for rows.Next() {
		var k key
		if err := rows.Scan(&k.id, &k.issue); err != nil {
			_ = rows.Close()
			return nil, err
		}
		keys = append(keys, k)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	out := []map[string]any{}
	for _, k := range keys {
		w, err := s.store.localWakeup(ctx, workspaceID, k.issue, k.id)
		if err != nil {
			return nil, err
		}
		row, err := s.localWakeupInventoryRow(ctx, w)
		if err != nil {
			return nil, err
		}
		row["source"], row["rule"], row["can_manage"] = "system", "child_done", true
		desc, err := s.localSystemWakeupDescription(ctx, s.store.db, workspaceID, k.issue)
		if err != nil {
			return nil, err
		}
		row["target_type"], row["system_stage"], row["system_remaining"] = nil, nil, 0
		if desc != nil {
			row["system_stage"] = desc["stage"]
			row["system_remaining"] = desc["remaining"]
			if target, ok := desc["target"].(map[string]any); ok {
				row["target_type"] = target["type"]
			}
		}
		out = append(out, row)
	}
	return out, nil
}

func localChildEvidenceSequence(ctx context.Context, q localWakeupQuery, issue string) (string, error) {
	var id int64
	if err := q.QueryRowContext(ctx, `SELECT COALESCE(MAX(id),0) FROM issue_wakeup_events WHERE issue_id=? OR issue_id IN (SELECT id FROM issues WHERE parent_issue_id=?)`, issue, issue).Scan(&id); err != nil {
		return "", err
	}
	return fmt.Sprint(id), nil
}
