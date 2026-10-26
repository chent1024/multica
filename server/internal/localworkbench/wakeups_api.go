package localworkbench

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/service"
)

func decodeWakeupBody(w http.ResponseWriter, r *http.Request, target any, max int64) error {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, max))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("%w: invalid wakeup body", service.ErrWakeupInput)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return fmt.Errorf("%w: expected one JSON object", service.ErrWakeupInput)
	}
	return nil
}

func (s *Server) localWakeupError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, service.ErrWakeupInput):
		writeError(w, http.StatusBadRequest, err)
	case errors.Is(err, service.ErrWakeupConflict):
		writeError(w, http.StatusConflict, err)
	case errors.Is(err, service.ErrWakeupForbidden):
		writeError(w, http.StatusForbidden, service.ErrWakeupForbidden)
	case errors.Is(err, sql.ErrNoRows):
		writeError(w, http.StatusNotFound, errors.New("wakeup or issue not found"))
	default:
		s.fail(w, err)
	}
}

func (s *Server) wakeupWorkspace(r *http.Request) (Workspace, error) {
	ws, err := s.workspaceFor(r)
	if err != nil {
		return ws, err
	}
	if requested := r.Header.Get("X-Workspace-ID"); requested != "" && requested != ws.ID {
		return ws, service.ErrWakeupForbidden
	}
	if requested := r.Header.Get("X-Workspace-Slug"); requested != "" && requested != ws.Slug {
		return ws, service.ErrWakeupForbidden
	}
	return ws, nil
}

func (s *Server) wakeupRequestIssue(r *http.Request) (Workspace, Issue, error) {
	ws, err := s.wakeupWorkspace(r)
	if err != nil {
		return ws, Issue{}, err
	}
	issue, err := s.store.GetIssue(r.Context(), ws.ID, chi.URLParam(r, "id"))
	return ws, issue, err
}

func wakeupRuleID(r *http.Request) (string, error) {
	parsed, err := uuid.Parse(chi.URLParam(r, "wakeupID"))
	if err != nil {
		return "", fmt.Errorf("%w: wakeup id must be a UUID", service.ErrWakeupInput)
	}
	return parsed.String(), nil
}

func localWakeupActor(ctx context.Context, q localWakeupQuery, r *http.Request, workspaceID, issueID string) (sql.NullString, error) {
	agentID, taskID := strings.TrimSpace(r.Header.Get("X-Agent-ID")), strings.TrimSpace(r.Header.Get("X-Task-ID"))
	if agentID == "" && taskID == "" {
		return sql.NullString{}, nil
	}
	if _, err := uuid.Parse(agentID); err != nil {
		return sql.NullString{}, service.ErrWakeupForbidden
	}
	if _, err := uuid.Parse(taskID); err != nil {
		return sql.NullString{}, service.ErrWakeupForbidden
	}
	var exists int
	err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM task_runs tr JOIN issues i ON i.id=tr.issue_id JOIN agents a ON a.id=tr.agent_id AND a.workspace_id=i.workspace_id WHERE tr.id=? AND tr.issue_id=? AND i.workspace_id=? AND tr.agent_id=? AND tr.status='running'`, taskID, issueID, workspaceID, agentID).Scan(&exists)
	if err != nil {
		return sql.NullString{}, err
	}
	if exists != 1 {
		return sql.NullString{}, service.ErrWakeupForbidden
	}
	return sql.NullString{String: taskID, Valid: true}, nil
}

func localWakeupIssueOpen(ctx context.Context, q localWakeupQuery, workspaceID, issueID string) error {
	var category string
	if err := q.QueryRowContext(ctx, `SELECT COALESCE(st.category,i.status) FROM issues i LEFT JOIN issue_statuses st ON st.workspace_id=i.workspace_id AND st.key=i.status WHERE i.workspace_id=? AND i.id=?`, workspaceID, issueID).Scan(&category); err != nil {
		return err
	}
	if localStatusCategory(category) == "done" || localStatusCategory(category) == "closed" {
		return fmt.Errorf("%w: issue is closed", service.ErrWakeupInput)
	}
	return nil
}

func validateLocalWakeupReferences(ctx context.Context, q localWakeupQuery, ws, issueID string, in *service.WakeupInput) error {
	exists := func(query string, args ...any) error {
		var count int
		if err := q.QueryRowContext(ctx, query, args...).Scan(&count); err != nil {
			return err
		}
		if count != 1 {
			return service.ErrWakeupForbidden
		}
		return nil
	}
	agent := func(id string) error {
		if _, err := uuid.Parse(id); err != nil {
			return fmt.Errorf("%w: agent id must be a UUID", service.ErrWakeupInput)
		}
		return exists(`SELECT COUNT(*) FROM agents WHERE id=? AND workspace_id=? AND archived_at IS NULL`, id, ws)
	}
	if err := agent(in.AgentID); err != nil {
		return err
	}
	if in.FilterAgentID != "" {
		if err := agent(in.FilterAgentID); err != nil {
			return err
		}
	}
	if in.FilterActorID != "" {
		if _, err := uuid.Parse(in.FilterActorID); err != nil {
			return fmt.Errorf("%w: actor id must be a UUID", service.ErrWakeupInput)
		}
		if in.FilterActorType == "agent" {
			if err := agent(in.FilterActorID); err != nil {
				return err
			}
		} else if in.FilterActorID != LocalOperatorID {
			return service.ErrWakeupForbidden
		}
	}
	if in.FilterTaskID != "" {
		if _, err := uuid.Parse(in.FilterTaskID); err != nil {
			return fmt.Errorf("%w: task id must be a UUID", service.ErrWakeupInput)
		}
		if err := exists(`SELECT COUNT(*) FROM task_runs t JOIN issues i ON i.id=t.issue_id WHERE t.id=? AND t.issue_id=? AND i.workspace_id=?`, in.FilterTaskID, issueID, ws); err != nil {
			return err
		}
	}
	if in.ParentCommentID != "" {
		if _, err := uuid.Parse(in.ParentCommentID); err != nil {
			return fmt.Errorf("%w: comment id must be a UUID", service.ErrWakeupInput)
		}
		if err := exists(`SELECT COUNT(*) FROM comments WHERE id=? AND issue_id=?`, in.ParentCommentID, issueID); err != nil {
			return err
		}
	}
	if in.Kind != "event" && (len(in.EventTypes) > 0 || in.FilterAgentID != "" || in.FilterTaskID != "" || in.FilterActorID != "") {
		return fmt.Errorf("%w: time wakeups cannot contain event filters", service.ErrWakeupInput)
	}
	if len(in.Condition) > 0 && string(in.Condition) != "null" {
		normalized, err := validateLocalWakeupCondition(ctx, q, ws, issueID, in.Condition)
		if err != nil {
			return err
		}
		in.Condition = normalized
	}
	return nil
}

func (s *Server) createIssueWakeup(w http.ResponseWriter, r *http.Request) {
	ws, issue, err := s.wakeupRequestIssue(r)
	if err != nil {
		s.localWakeupError(w, err)
		return
	}
	var input service.WakeupInput
	if err = decodeWakeupBody(w, r, &input, 32768); err != nil {
		s.localWakeupError(w, err)
		return
	}
	tx, err := s.store.db.BeginTx(r.Context(), nil)
	if err != nil {
		s.localWakeupError(w, err)
		return
	}
	defer tx.Rollback()
	source, err := localWakeupActor(r.Context(), tx, r, ws.ID, issue.ID)
	if err != nil {
		s.localWakeupError(w, err)
		return
	}
	if input.AgentID == "" && source.Valid {
		input.AgentID = strings.TrimSpace(r.Header.Get("X-Agent-ID"))
	}
	if err = localWakeupIssueOpen(r.Context(), tx, ws.ID, issue.ID); err != nil {
		s.localWakeupError(w, err)
		return
	}
	stamp := time.Now().UTC()
	validator := service.IssueWakeupService{}
	next, err := validator.Validate(&input, stamp)
	if err != nil {
		s.localWakeupError(w, err)
		return
	}
	expires, _, err := validator.Expiry(&input, stamp)
	if err != nil {
		s.localWakeupError(w, err)
		return
	}
	if err = validateLocalWakeupReferences(r.Context(), tx, ws.ID, issue.ID, &input); err != nil {
		s.localWakeupError(w, err)
		return
	}
	if input.Mode == "continuous" && input.Kind == "event" && input.MaxFires == 0 {
		input.MaxFires = 20
	}
	rule := localWakeup{ID: uuid.NewString(), WorkspaceID: ws.ID, IssueID: issue.ID, AgentID: input.AgentID, CreatedBy: LocalOperatorID, Config: input, Enabled: true, Revision: 1, CreatedAt: stamp.Format(time.RFC3339Nano), UpdatedAt: stamp.Format(time.RFC3339Nano), SourceTaskID: source}
	code := http.StatusCreated
	if r.Method == http.MethodPut {
		id, e := wakeupRuleID(r)
		if e != nil {
			s.localWakeupError(w, e)
			return
		}
		if system, e := isLocalSystemWakeup(r.Context(), tx, id); e != nil {
			s.localWakeupError(w, e)
			return
		} else if system {
			s.localWakeupError(w, service.ErrWakeupForbidden)
			return
		}
		old, e := scanLocalWakeup(tx.QueryRowContext(r.Context(), `SELECT `+localWakeupColumns+` FROM issue_wakeups WHERE workspace_id=? AND issue_id=? AND id=?`, ws.ID, issue.ID, id))
		if e != nil {
			s.localWakeupError(w, e)
			return
		}
		if e = clearLocalWakeupPendingTx(r.Context(), tx, old.ID); e != nil {
			s.localWakeupError(w, e)
			return
		}
		rule.ID, rule.CreatedAt, rule.Revision = old.ID, old.CreatedAt, old.Revision+1
		rule.CreatedBy, rule.SourceTaskID = old.CreatedBy, old.SourceTaskID
		code = http.StatusOK
	}
	if next.Valid {
		rule.NextFireAt = sql.NullString{String: next.Time.UTC().Format(time.RFC3339Nano), Valid: true}
	}
	if len(input.Condition) > 0 && string(input.Condition) != "null" {
		rule.NextFireAt = sql.NullString{String: stamp.Format(time.RFC3339Nano), Valid: true}
	}
	if expires.Valid {
		rule.ExpiresAt = sql.NullString{String: expires.Time.UTC().Format(time.RFC3339Nano), Valid: true}
	}
	if err = checkLocalWakeupCapacity(r.Context(), tx, rule); err != nil {
		s.localWakeupError(w, err)
		return
	}
	if err = putLocalWakeupTx(r.Context(), tx, rule); err != nil {
		s.localWakeupError(w, err)
		return
	}
	if code == http.StatusCreated {
		details, e := json.Marshal(map[string]any{"wakeup_id": rule.ID, "agent_id": rule.AgentID, "wakeup": localWakeupPreview(rule)})
		if e != nil {
			s.localWakeupError(w, e)
			return
		}
		actorType, actorID := "member", LocalOperatorID
		if source.Valid {
			actorType, actorID = "agent", r.Header.Get("X-Agent-ID")
		}
		if _, e = tx.ExecContext(r.Context(), `INSERT INTO activities(id,issue_id,actor_type,actor_id,action,details,created_at) VALUES(?,?,?,?,'wakeup_created',?,?)`, uuid.NewString(), issue.ID, actorType, actorID, string(details), rule.CreatedAt); e != nil {
			s.localWakeupError(w, e)
			return
		}
	}
	if err = tx.Commit(); err != nil {
		s.localWakeupError(w, err)
		return
	}
	payload, err := s.localWakeupPayload(r.Context(), s.store.db, rule)
	if err != nil {
		s.localWakeupError(w, err)
		return
	}
	writeJSON(w, code, payload)
}

func checkLocalWakeupCapacity(ctx context.Context, tx *sql.Tx, rule localWakeup) error {
	var issueCount, workspaceCount int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(SUM(CASE WHEN issue_id=? THEN 1 ELSE 0 END),0) FROM issue_wakeups WHERE workspace_id=? AND enabled=1 AND id<>? AND id NOT IN (SELECT wakeup_id FROM issue_system_wakeups WHERE wakeup_id IS NOT NULL)`, rule.IssueID, rule.WorkspaceID, rule.ID).Scan(&workspaceCount, &issueCount); err != nil {
		return err
	}
	if issueCount >= 32 || workspaceCount >= 1000 {
		return fmt.Errorf("%w: enabled wakeup capacity exceeded", service.ErrWakeupInput)
	}
	return nil
}

func clearLocalWakeupPendingTx(ctx context.Context, tx *sql.Tx, id string) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM issue_wakeup_receipts WHERE wakeup_id=?`, id); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `UPDATE task_runs SET status='cancelled',completed_at=?,updated_at=?,error='Wakeup disabled or updated' WHERE wakeup_id=? AND status IN ('queued','dispatched','waiting_local_directory','waiting_previous_run') AND started_at IS NULL`, now(), now(), id)
	return err
}

func (s *Server) disableIssueWakeup(w http.ResponseWriter, r *http.Request) {
	s.mutateLocalWakeup(w, r, func(tx *sql.Tx, rule *localWakeup) error {
		rule.Enabled = false
		rule.Revision++
		rule.UpdatedAt = now()
		if !rule.DisabledAt.Valid {
			rule.DisabledAt = sql.NullString{String: rule.UpdatedAt, Valid: true}
		}
		return clearLocalWakeupPendingTx(r.Context(), tx, rule.ID)
	}, false)
}

func (s *Server) enableIssueWakeup(w http.ResponseWriter, r *http.Request) {
	var in service.WakeupEnableInput
	if err := decodeWakeupBody(w, r, &in, 1024); err != nil {
		s.localWakeupError(w, err)
		return
	}
	s.mutateLocalWakeup(w, r, func(tx *sql.Tx, rule *localWakeup) error {
		if in.Revision < 1 {
			return fmt.Errorf("%w: revision is required", service.ErrWakeupInput)
		}
		if rule.Revision != in.Revision {
			return service.ErrWakeupConflict
		}
		if err := localWakeupIssueOpen(r.Context(), tx, rule.WorkspaceID, rule.IssueID); err != nil {
			return err
		}
		if rule.Enabled {
			if in.Rearm || in.At != nil {
				return service.ErrWakeupConflict
			}
			return nil
		}
		if rule.Config.Mode == "once" && (!rule.DisabledAt.Valid || rule.LastTaskID.Valid) {
			if !in.Rearm {
				return fmt.Errorf("%w: consumed one-shot requires explicit rearm", service.ErrWakeupInput)
			}
			var active int
			if err := tx.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM task_runs WHERE wakeup_id=? AND status NOT IN ('completed','failed','cancelled')`, rule.ID).Scan(&active); err != nil {
				return err
			}
			if active > 0 {
				return service.ErrWakeupConflict
			}
		}
		config := rule.Config
		stamp := time.Now().UTC()
		if config.Kind == "at" {
			config.AfterSeconds = 0
			config.At = nil
			if rule.NextFireAt.Valid {
				at, err := time.Parse(time.RFC3339Nano, rule.NextFireAt.String)
				if err != nil {
					return err
				}
				config.At = &at
			}
			if in.At != nil {
				config.At = in.At
			}
			if config.At == nil || !config.At.After(stamp) {
				return fmt.Errorf("%w: choose a future time", service.ErrWakeupInput)
			}
		} else if in.At != nil {
			return fmt.Errorf("%w: only single-time wakeups accept a new time", service.ErrWakeupInput)
		}
		validator := service.IssueWakeupService{}
		next, err := validator.Validate(&config, stamp)
		if err != nil {
			return err
		}
		expires, _, err := validator.Expiry(&config, stamp)
		if err != nil {
			return err
		}
		if err = validateLocalWakeupReferences(r.Context(), tx, rule.WorkspaceID, rule.IssueID, &config); err != nil {
			return err
		}
		if err = checkLocalWakeupCapacity(r.Context(), tx, *rule); err != nil {
			return err
		}
		if err = clearLocalWakeupPendingTx(r.Context(), tx, rule.ID); err != nil {
			return err
		}
		rule.Config = config
		rule.Enabled = true
		rule.Revision++
		rule.UpdatedAt = stamp.Format(time.RFC3339Nano)
		rule.DisabledAt, rule.LastError, rule.TimedOutAt, rule.PausedReason = sql.NullString{}, sql.NullString{}, sql.NullString{}, sql.NullString{}
		rule.NextFireAt, rule.ExpiresAt = sql.NullString{}, sql.NullString{}
		if next.Valid {
			rule.NextFireAt = sql.NullString{String: next.Time.UTC().Format(time.RFC3339Nano), Valid: true}
		}
		if len(config.Condition) > 0 && string(config.Condition) != "null" {
			rule.NextFireAt = sql.NullString{String: stamp.Format(time.RFC3339Nano), Valid: true}
		}
		if expires.Valid {
			rule.ExpiresAt = sql.NullString{String: expires.Time.UTC().Format(time.RFC3339Nano), Valid: true}
		}
		if in.Rearm {
			rule.FireCount = 0
			rule.LastTaskID = sql.NullString{}
		}
		return nil
	}, false)
}

func (s *Server) editIssueWakeupInstruction(w http.ResponseWriter, r *http.Request) {
	var in service.WakeupInstructionInput
	if err := decodeWakeupBody(w, r, &in, 160000); err != nil {
		s.localWakeupError(w, err)
		return
	}
	in.Instruction = strings.TrimSpace(in.Instruction)
	if len(in.Instruction) == 0 || len(in.Instruction) > 12000 || in.Revision < 1 {
		s.localWakeupError(w, fmt.Errorf("%w: instruction must be 1–12000 bytes and revision is required", service.ErrWakeupInput))
		return
	}
	s.mutateLocalWakeup(w, r, func(tx *sql.Tx, rule *localWakeup) error {
		if rule.Revision != in.Revision || rule.Config.Instruction != in.ExpectedInstruction {
			return service.ErrWakeupConflict
		}
		rule.Config.Instruction = in.Instruction
		rule.UpdatedAt = now()
		return nil
	}, true)
}

func (s *Server) mutateLocalWakeup(w http.ResponseWriter, r *http.Request, mutate func(*sql.Tx, *localWakeup) error, noContent bool) {
	ws, issue, err := s.wakeupRequestIssue(r)
	if err != nil {
		s.localWakeupError(w, err)
		return
	}
	id, err := wakeupRuleID(r)
	if err != nil {
		s.localWakeupError(w, err)
		return
	}
	tx, err := s.store.db.BeginTx(r.Context(), nil)
	if err != nil {
		s.localWakeupError(w, err)
		return
	}
	defer tx.Rollback()
	if _, err = localWakeupActor(r.Context(), tx, r, ws.ID, issue.ID); err != nil {
		s.localWakeupError(w, err)
		return
	}
	if system, e := isLocalSystemWakeup(r.Context(), tx, id); e != nil {
		s.localWakeupError(w, e)
		return
	} else if system {
		s.localWakeupError(w, service.ErrWakeupForbidden)
		return
	}
	rule, err := scanLocalWakeup(tx.QueryRowContext(r.Context(), `SELECT `+localWakeupColumns+` FROM issue_wakeups WHERE workspace_id=? AND issue_id=? AND id=?`, ws.ID, issue.ID, id))
	if err != nil {
		s.localWakeupError(w, err)
		return
	}
	if err = mutate(tx, &rule); err != nil {
		s.localWakeupError(w, err)
		return
	}
	if err = putLocalWakeupTx(r.Context(), tx, rule); err != nil {
		s.localWakeupError(w, err)
		return
	}
	if err = tx.Commit(); err != nil {
		s.localWakeupError(w, err)
		return
	}
	if noContent {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	payload, err := s.localWakeupPayload(r.Context(), s.store.db, rule)
	if err != nil {
		s.localWakeupError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, payload)
}

func (s *Server) deleteIssueWakeup(w http.ResponseWriter, r *http.Request) {
	ws, issue, err := s.wakeupRequestIssue(r)
	if err != nil {
		s.localWakeupError(w, err)
		return
	}
	id, err := wakeupRuleID(r)
	if err != nil {
		s.localWakeupError(w, err)
		return
	}
	tx, err := s.store.db.BeginTx(r.Context(), nil)
	if err != nil {
		s.localWakeupError(w, err)
		return
	}
	defer tx.Rollback()
	if _, err = localWakeupActor(r.Context(), tx, r, ws.ID, issue.ID); err != nil {
		s.localWakeupError(w, err)
		return
	}
	if system, e := isLocalSystemWakeup(r.Context(), tx, id); e != nil {
		s.localWakeupError(w, e)
		return
	} else if system {
		s.localWakeupError(w, service.ErrWakeupForbidden)
		return
	}
	if _, err = scanLocalWakeup(tx.QueryRowContext(r.Context(), `SELECT `+localWakeupColumns+` FROM issue_wakeups WHERE workspace_id=? AND issue_id=? AND id=?`, ws.ID, issue.ID, id)); err != nil {
		s.localWakeupError(w, err)
		return
	}
	if err = clearLocalWakeupPendingTx(r.Context(), tx, id); err != nil {
		s.localWakeupError(w, err)
		return
	}
	if _, err = tx.ExecContext(r.Context(), `DELETE FROM issue_wakeup_runs WHERE wakeup_id=?`, id); err != nil {
		s.localWakeupError(w, err)
		return
	}
	if err = deleteLocalWakeupOwnedRowsTx(r.Context(), tx, id); err != nil {
		s.localWakeupError(w, err)
		return
	}
	if _, err = tx.ExecContext(r.Context(), `DELETE FROM issue_wakeups WHERE id=? AND workspace_id=? AND issue_id=?`, id, ws.ID, issue.ID); err != nil {
		s.localWakeupError(w, err)
		return
	}
	if err = tx.Commit(); err != nil {
		s.localWakeupError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) triggerIssueWakeup(w http.ResponseWriter, r *http.Request) {
	ws, issue, err := s.wakeupRequestIssue(r)
	if err != nil {
		s.localWakeupError(w, err)
		return
	}
	id, err := wakeupRuleID(r)
	if err != nil {
		s.localWakeupError(w, err)
		return
	}
	if _, err = localWakeupActor(r.Context(), s.store.db, r, ws.ID, issue.ID); err != nil {
		s.localWakeupError(w, err)
		return
	}
	if system, e := isLocalSystemWakeup(r.Context(), s.store.db, id); e != nil {
		s.localWakeupError(w, e)
		return
	} else if system {
		s.localWakeupError(w, service.ErrWakeupForbidden)
		return
	}
	if _, err = s.triggerLocalWakeup(r.Context(), ws.ID, issue.ID, id); err != nil {
		s.localWakeupError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) checkInIssueWakeup(w http.ResponseWriter, r *http.Request) {
	ws, issue, err := s.wakeupRequestIssue(r)
	if err != nil {
		s.localWakeupError(w, err)
		return
	}
	id, err := wakeupRuleID(r)
	if err != nil {
		s.localWakeupError(w, err)
		return
	}
	var in struct {
		Note string `json:"note"`
	}
	if err = decodeWakeupBody(w, r, &in, 8192); err != nil {
		s.localWakeupError(w, err)
		return
	}
	source, err := localWakeupActor(r.Context(), s.store.db, r, ws.ID, issue.ID)
	if err != nil {
		s.localWakeupError(w, err)
		return
	}
	if !source.Valid {
		s.localWakeupError(w, service.ErrWakeupForbidden)
		return
	}
	if err = s.checkInLocalWakeup(r.Context(), ws.ID, issue.ID, id, source.String, in.Note); err != nil {
		s.localWakeupError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) localWakeupPayload(ctx context.Context, q localWakeupQuery, rule localWakeup) (map[string]any, error) {
	c := rule.Config
	nameOf := func(id string) (string, bool, error) {
		if id == "" {
			return "", false, nil
		}
		var name string
		err := q.QueryRowContext(ctx, `SELECT name FROM agents WHERE id=? AND workspace_id=?`, id, rule.WorkspaceID).Scan(&name)
		if errors.Is(err, sql.ErrNoRows) {
			return "", false, nil
		}
		return name, err == nil, err
	}
	agentName, _, err := nameOf(rule.AgentID)
	if err != nil {
		return nil, err
	}
	filterName, filterExists, err := nameOf(c.FilterAgentID)
	if err != nil {
		return nil, err
	}
	var filterID, filterTaskID, actorID, actorName any
	if filterExists {
		filterID = c.FilterAgentID
	}
	if c.FilterTaskID != "" {
		var count int
		if err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM task_runs t JOIN issues i ON i.id=t.issue_id WHERE t.id=? AND t.issue_id=? AND i.workspace_id=?`, c.FilterTaskID, rule.IssueID, rule.WorkspaceID).Scan(&count); err != nil {
			return nil, err
		}
		if count == 1 {
			filterTaskID = c.FilterTaskID
		}
	}
	if c.FilterActorType == "member" && c.FilterActorID == LocalOperatorID {
		actorID, actorName = LocalOperatorID, "Local Operator"
	} else if c.FilterActorType == "agent" {
		name, exists, err := nameOf(c.FilterActorID)
		if err != nil {
			return nil, err
		}
		if exists {
			actorID, actorName = c.FilterActorID, name
		}
	}
	var sourceAgentID, sourceAgentName any
	if rule.SourceTaskID.Valid {
		var id, name string
		err := q.QueryRowContext(ctx, `SELECT a.id,a.name FROM task_runs t JOIN issues i ON i.id=t.issue_id JOIN agents a ON a.id=t.agent_id AND a.workspace_id=i.workspace_id WHERE t.id=? AND t.issue_id=? AND i.workspace_id=?`, rule.SourceTaskID.String, rule.IssueID, rule.WorkspaceID).Scan(&id, &name)
		if err == nil {
			sourceAgentID, sourceAgentName = id, name
		} else if !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
	}
	var lastStatus any
	if rule.LastTaskID.Valid {
		var status string
		err := q.QueryRowContext(ctx, `SELECT status FROM task_runs WHERE id=? AND issue_id=? AND agent_id=?`, rule.LastTaskID.String, rule.IssueID, rule.AgentID).Scan(&status)
		if err == nil {
			lastStatus = status
		} else if !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
	}
	events := c.EventTypes
	if events == nil {
		events = []string{}
	}
	var interval, maxFires, expiry any
	if c.IntervalSeconds > 0 {
		interval = c.IntervalSeconds
	}
	if c.MaxFires > 0 {
		maxFires = c.MaxFires
	}
	if c.ExpiresInSeconds > 0 {
		expiry = c.ExpiresInSeconds
	}
	var condition any
	if len(c.Condition) > 0 && string(c.Condition) != "null" {
		condition = c.Condition
	}
	return map[string]any{
		"id": rule.ID, "issue_id": rule.IssueID, "agent_id": rule.AgentID, "agent_name": agentName, "instruction": c.Instruction,
		"kind": c.Kind, "mode": c.Mode, "event_types": events, "filter_agent_id": filterID, "filter_agent_name": nullable(filterName), "filter_task_id": filterTaskID,
		"filter_actor_type": nullable(c.FilterActorType), "filter_actor_id": actorID, "filter_actor_name": actorName,
		"interval_seconds": interval, "cron_expression": nullable(c.CronExpression), "timezone": c.Timezone,
		"next_fire_at": nullStringPointer(rule.NextFireAt), "enabled": rule.Enabled, "revision": rule.Revision,
		"disabled_at": nullStringPointer(rule.DisabledAt), "last_task_id": nullStringPointer(rule.LastTaskID), "last_task_status": lastStatus, "last_error": nullStringPointer(rule.LastError),
		"expires_at": nullStringPointer(rule.ExpiresAt), "expiry_seconds": expiry, "on_timeout": nullable(c.OnTimeout), "timed_out_at": nullStringPointer(rule.TimedOutAt),
		"created_by_agent": rule.SourceTaskID.Valid, "created_by_name": "Local Operator", "source_agent_id": sourceAgentID, "source_agent_name": sourceAgentName,
		"condition": condition, "max_fires": maxFires, "fire_count": rule.FireCount, "paused_reason": nullStringPointer(rule.PausedReason),
	}, nil
}

func (s *Server) listIssueWakeups(w http.ResponseWriter, r *http.Request) {
	ws, issue, err := s.wakeupRequestIssue(r)
	if err != nil {
		s.localWakeupError(w, err)
		return
	}
	rules, err := s.store.listLocalWakeups(r.Context(), ws.ID, issue.ID)
	if err != nil {
		s.localWakeupError(w, err)
		return
	}
	sort.Slice(rules, func(i, j int) bool {
		a, _ := time.Parse(time.RFC3339Nano, rules[i].CreatedAt)
		b, _ := time.Parse(time.RFC3339Nano, rules[j].CreatedAt)
		if !a.Equal(b) {
			return a.Before(b)
		}
		return rules[i].ID < rules[j].ID
	})
	out := []map[string]any{}
	for _, rule := range rules {
		payload, err := s.localWakeupPayload(r.Context(), s.store.db, rule)
		if err != nil {
			s.localWakeupError(w, err)
			return
		}
		out = append(out, payload)
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) listWorkspaceWakeupSummaries(w http.ResponseWriter, r *http.Request) {
	ws, err := s.wakeupWorkspace(r)
	if err != nil {
		s.localWakeupError(w, err)
		return
	}
	rules, err := s.store.allLocalWakeups(r.Context(), ws.ID)
	if err != nil {
		s.localWakeupError(w, err)
		return
	}
	groups := map[string][]localWakeup{}
	for _, rule := range rules {
		if !rule.Enabled || rule.AgentID == "" {
			continue
		}
		if err := localWakeupIssueOpen(r.Context(), s.store.db, ws.ID, rule.IssueID); errors.Is(err, service.ErrWakeupInput) || errors.Is(err, sql.ErrNoRows) {
			continue
		} else if err != nil {
			s.localWakeupError(w, err)
			return
		}
		groups[rule.IssueID] = append(groups[rule.IssueID], rule)
	}
	issueIDs := make([]string, 0, len(groups))
	for id := range groups {
		issueIDs = append(issueIDs, id)
	}
	sort.Strings(issueIDs)
	out := []map[string]any{}
	for _, id := range issueIDs {
		group := groups[id]
		eventCount := 0
		for _, rule := range group {
			if rule.Config.Kind == "event" {
				eventCount++
			}
		}
		sort.Slice(group, func(i, j int) bool {
			a, b := group[i], group[j]
			if a.NextFireAt.Valid != b.NextFireAt.Valid {
				return a.NextFireAt.Valid
			}
			if a.NextFireAt.Valid && a.NextFireAt.String != b.NextFireAt.String {
				at, _ := time.Parse(time.RFC3339Nano, a.NextFireAt.String)
				bt, _ := time.Parse(time.RFC3339Nano, b.NextFireAt.String)
				if !at.Equal(bt) {
					return at.Before(bt)
				}
			}
			at, _ := time.Parse(time.RFC3339Nano, a.CreatedAt)
			bt, _ := time.Parse(time.RFC3339Nano, b.CreatedAt)
			if !at.Equal(bt) {
				return at.Before(bt)
			}
			return a.ID < b.ID
		})
		for index, rule := range group {
			if index >= 3 {
				break
			}
			payload, err := s.localWakeupPayload(r.Context(), s.store.db, rule)
			if err != nil {
				s.localWakeupError(w, err)
				return
			}
			preview := map[string]any{"active_count": len(group), "event_count": eventCount}
			for _, key := range []string{"id", "issue_id", "agent_id", "agent_name", "kind", "mode", "event_types", "filter_agent_id", "filter_agent_name", "filter_task_id", "filter_actor_type", "filter_actor_id", "filter_actor_name", "interval_seconds", "cron_expression", "timezone", "next_fire_at", "condition"} {
				preview[key] = payload[key]
			}
			out = append(out, preview)
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) listPausedWakeups(w http.ResponseWriter, r *http.Request) {
	ws, err := s.wakeupWorkspace(r)
	if err != nil {
		s.localWakeupError(w, err)
		return
	}
	rules, err := s.store.allLocalWakeups(r.Context(), ws.ID)
	if err != nil {
		s.localWakeupError(w, err)
		return
	}
	out := []map[string]any{}
	for _, rule := range rules {
		if !rule.PausedReason.Valid {
			continue
		}
		if err := localWakeupIssueOpen(r.Context(), s.store.db, ws.ID, rule.IssueID); errors.Is(err, service.ErrWakeupInput) || errors.Is(err, sql.ErrNoRows) {
			continue
		} else if err != nil {
			s.localWakeupError(w, err)
			return
		}
		out = append(out, map[string]any{"issue_id": rule.IssueID, "id": rule.ID, "agent_id": rule.AgentID, "paused_reason": rule.PausedReason.String})
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) listIssueWakeupRuns(w http.ResponseWriter, r *http.Request) {
	ws, issue, err := s.wakeupRequestIssue(r)
	if err != nil {
		s.localWakeupError(w, err)
		return
	}
	id, err := wakeupRuleID(r)
	if err != nil {
		s.localWakeupError(w, err)
		return
	}
	if _, err = s.store.localWakeup(r.Context(), ws.ID, issue.ID, id); err != nil {
		s.localWakeupError(w, err)
		return
	}
	rows, err := s.store.db.QueryContext(r.Context(), `SELECT t.id,t.status,t.created_at,t.started_at,t.completed_at,wr.checkin_note,wr.triggers,EXISTS(SELECT 1 FROM comments c WHERE c.source_task_id=t.id AND c.issue_id=t.issue_id) FROM issue_wakeup_runs wr JOIN task_runs t ON t.id=wr.task_id WHERE wr.wakeup_id=? AND t.issue_id=? ORDER BY wr.created_at DESC,t.id DESC LIMIT 50`, id, issue.ID)
	if err != nil {
		s.localWakeupError(w, err)
		return
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var taskID, status, created, note, raw string
		var started, completed sql.NullString
		var commented bool
		if err = rows.Scan(&taskID, &status, &created, &started, &completed, &note, &raw, &commented); err != nil {
			s.localWakeupError(w, err)
			return
		}
		triggers := []string{}
		if err = json.Unmarshal([]byte(raw), &triggers); err != nil {
			s.localWakeupError(w, err)
			return
		}
		if triggers == nil {
			triggers = []string{}
		}
		out = append(out, map[string]any{"id": taskID, "status": status, "created_at": created, "started_at": nullStringPointer(started), "completed_at": nullStringPointer(completed), "checkin_note": note, "triggers": triggers, "commented": commented})
	}
	if err = rows.Err(); err != nil {
		s.localWakeupError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) localWakeupInventoryRow(ctx context.Context, rule localWakeup) (map[string]any, error) {
	payload, err := s.localWakeupPayload(ctx, s.store.db, rule)
	if err != nil {
		return nil, err
	}
	delete(payload, "instruction")
	var title, identifier, category string
	err = s.store.db.QueryRowContext(ctx, `SELECT i.title,w.issue_prefix||'-'||i.number,COALESCE(st.category,i.status) FROM issues i JOIN workspaces w ON w.id=i.workspace_id LEFT JOIN issue_statuses st ON st.workspace_id=i.workspace_id AND st.key=i.status WHERE i.id=? AND i.workspace_id=?`, rule.IssueID, rule.WorkspaceID).Scan(&title, &identifier, &category)
	if err != nil {
		return nil, err
	}
	closed := localStatusCategory(category) == "done" || localStatusCategory(category) == "closed"
	var active, runs7d int
	if err = s.store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM task_runs WHERE wakeup_id=? AND issue_id=? AND status NOT IN ('completed','failed','cancelled')`, rule.ID, rule.IssueID).Scan(&active); err != nil {
		return nil, err
	}
	since := time.Now().UTC().Add(-7 * 24 * time.Hour).Format(time.RFC3339Nano)
	if err = s.store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM issue_wakeup_runs WHERE wakeup_id=? AND julianday(created_at)>julianday(?)`, rule.ID, since).Scan(&runs7d); err != nil {
		return nil, err
	}
	source := "member"
	if rule.SourceTaskID.Valid {
		source = "agent"
	}
	scope := "ended"
	switch {
	case (rule.Enabled && !closed) || active > 0:
		scope = "active"
	case !closed && rule.PausedReason.Valid:
		scope = "paused"
	case !closed && rule.DisabledAt.Valid:
		scope = "disabled"
	}
	var task any
	var id, status, runtime, created string
	var started, completed sql.NullString
	err = s.store.db.QueryRowContext(ctx, `SELECT id,status,runtime,created_at,started_at,completed_at FROM task_runs WHERE wakeup_id=? AND issue_id=? ORDER BY CASE WHEN status NOT IN ('completed','failed','cancelled') THEN 0 ELSE 1 END,created_at DESC,id DESC LIMIT 1`, rule.ID, rule.IssueID).Scan(&id, &status, &runtime, &created, &started, &completed)
	if err == nil {
		task = map[string]any{"id": id, "agent_id": rule.AgentID, "runtime_id": runtime, "issue_id": rule.IssueID, "wakeup_id": rule.ID, "status": status, "created_at": created, "started_at": nullStringPointer(started), "completed_at": nullStringPointer(completed)}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	for key, value := range map[string]any{"issue_title": title, "issue_identifier": identifier, "issue_closed": closed, "can_manage": true, "active_runs": active, "task": task, "source": source, "runs_7d": runs7d, "rule": nil, "system_stage": nil, "system_remaining": nil, "target_type": nil, "created_at": rule.CreatedAt, "scope": scope} {
		payload[key] = value
	}
	return payload, nil
}

func (s *Server) listWorkspaceWakeups(w http.ResponseWriter, r *http.Request) {
	ws, err := s.wakeupWorkspace(r)
	if err != nil {
		s.localWakeupError(w, err)
		return
	}
	q := r.URL.Query()
	scope, kind, source, agentID, search := q.Get("scope"), q.Get("kind"), q.Get("source"), q.Get("agent_id"), strings.TrimSpace(q.Get("search"))
	if scope == "" {
		scope = "active"
	}
	if kind == "" {
		kind = "all"
	}
	if !slices.Contains([]string{"active", "all", "paused", "disabled", "ended"}, scope) || !slices.Contains([]string{"all", "event", "at", "recurring"}, kind) || !slices.Contains([]string{"", "member", "agent", "system"}, source) || len(search) > 256 {
		s.localWakeupError(w, fmt.Errorf("%w: invalid wakeup filters", service.ErrWakeupInput))
		return
	}
	if agentID != "" {
		parsed, err := uuid.Parse(agentID)
		if err != nil {
			s.localWakeupError(w, fmt.Errorf("%w: invalid agent id", service.ErrWakeupInput))
			return
		}
		agentID = parsed.String()
	}
	offset, limit := 0, 50
	for key, target := range map[string]*int{"offset": &offset, "limit": &limit} {
		if value := q.Get(key); value != "" {
			number, err := strconv.Atoi(value)
			if err != nil || number < 0 || (key == "limit" && (number < 1 || number > 100)) || (key == "offset" && number > 1000000) {
				s.localWakeupError(w, fmt.Errorf("%w: invalid pagination", service.ErrWakeupInput))
				return
			}
			*target = number
		}
	}
	rules, err := s.store.listLocalWakeups(r.Context(), ws.ID, "")
	if err != nil {
		s.localWakeupError(w, err)
		return
	}
	base := []map[string]any{}
	for _, rule := range rules {
		row, err := s.localWakeupInventoryRow(r.Context(), rule)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			s.localWakeupError(w, err)
			return
		}
		base = append(base, row)
	}
	systems, err := s.localSystemWakeupInventory(r.Context(), ws.ID)
	if err != nil {
		s.localWakeupError(w, err)
		return
	}
	base = append(base, systems...)
	counts := map[string]int{"all": len(base), "active": 0, "paused": 0, "disabled": 0, "ended": 0}
	choices := map[string]string{}
	filtered := []map[string]any{}
	for _, row := range base {
		rowScope := stringFromAny(row["scope"])
		counts[rowScope]++
		if id := stringFromAny(row["agent_id"]); id != "" {
			choices[id] = stringFromAny(row["agent_name"])
		}
		rowKind := stringFromAny(row["kind"])
		if scope != "all" && rowScope != scope {
			continue
		}
		if source != "" && stringFromAny(row["source"]) != source {
			continue
		}
		if agentID != "" && stringFromAny(row["agent_id"]) != agentID {
			continue
		}
		if kind == "recurring" && (rowKind != "every" && rowKind != "cron") {
			continue
		}
		if kind != "all" && kind != "recurring" && rowKind != kind {
			continue
		}
		if search != "" && !strings.Contains(strings.ToLower(stringFromAny(row["issue_title"])+" "+stringFromAny(row["issue_identifier"])+" "+stringFromAny(row["agent_name"])), strings.ToLower(search)) {
			continue
		}
		filtered = append(filtered, row)
	}
	sort.Slice(filtered, func(i, j int) bool {
		at, _ := time.Parse(time.RFC3339Nano, stringFromAny(filtered[i]["created_at"]))
		bt, _ := time.Parse(time.RFC3339Nano, stringFromAny(filtered[j]["created_at"]))
		if !at.Equal(bt) {
			return at.After(bt)
		}
		return stringFromAny(filtered[i]["id"]) > stringFromAny(filtered[j]["id"])
	})
	total := len(filtered)
	if offset > total {
		offset = total
	}
	end := offset + limit
	if end > total {
		end = total
	}
	items := filtered[offset:end]
	if items == nil {
		items = []map[string]any{}
	}
	for _, row := range items {
		delete(row, "scope")
		delete(row, "created_at")
	}
	agents := []map[string]string{}
	for id, name := range choices {
		agents = append(agents, map[string]string{"id": id, "name": name})
	}
	sort.Slice(agents, func(i, j int) bool {
		if agents[i]["name"] != agents[j]["name"] {
			return agents[i]["name"] < agents[j]["name"]
		}
		return agents[i]["id"] < agents[j]["id"]
	})
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "total": total, "counts": counts, "agents": agents})
}

func isLocalSystemWakeup(ctx context.Context, q localWakeupQuery, id string) (bool, error) {
	var found bool
	err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM issue_system_wakeups WHERE wakeup_id=?)`, id).Scan(&found)
	return found, err
}

func localWakeupPreview(rule localWakeup) map[string]any {
	c := rule.Config
	events := c.EventTypes
	if events == nil {
		events = []string{}
	}
	preview := map[string]any{"id": rule.ID, "agent_id": rule.AgentID, "kind": c.Kind, "mode": c.Mode, "event_types": events, "timezone": c.Timezone}
	if len(c.Condition) > 0 && string(c.Condition) != "null" {
		preview["condition"] = c.Condition
	}
	if c.FilterActorType != "" {
		preview["filter_actor_type"], preview["filter_actor_id"] = c.FilterActorType, c.FilterActorID
	}
	if c.FilterAgentID != "" {
		preview["filter_agent_id"] = c.FilterAgentID
	}
	if c.FilterTaskID != "" {
		preview["filter_task_id"] = c.FilterTaskID
	}
	if c.IntervalSeconds > 0 {
		preview["interval_seconds"] = c.IntervalSeconds
	}
	if c.CronExpression != "" {
		preview["cron_expression"] = c.CronExpression
	}
	if rule.NextFireAt.Valid && len(c.Condition) == 0 {
		preview["next_fire_at"] = rule.NextFireAt.String
	}
	if rule.ExpiresAt.Valid {
		preview["expires_at"] = rule.ExpiresAt.String
	}
	return preview
}
