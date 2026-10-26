package localworkbench

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"sort"
	"strings"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/service"
)

// The same queries run inside management/dispatch transactions and read-only
// projections. Callers must pass their transaction instead of opening a second
// query against the store's single SQLite connection.
type localWakeupQuery interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

var (
	errLocalWakeupPRNotLinked            = errors.New("no pull request is linked to this issue; link its pull request or wait for the linking webhook")
	errLocalWakeupPRSnapshotPending      = errors.New("pull request snapshot for the current head is not available; refresh the linked pull request integration snapshot")
	errLocalWakeupPRConflictsUnsupported = errors.New("merge conflicts require a linked GitHub pull request; the linked self-hosted provider does not report mergeability")
	errLocalWakeupPRMergeabilityPending  = errors.New("GitHub is recomputing mergeability for the current head; retain the previous condition evidence and wait for its refreshed snapshot")
)

func localWakeupConditionInputError(message string) error {
	return fmt.Errorf("%w: %s", service.ErrWakeupInput, message)
}

func decodeLocalWakeupCondition(raw json.RawMessage) (service.WakeupCondition, error) {
	var condition service.WakeupCondition
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&condition); err != nil {
		return condition, localWakeupConditionInputError("unreadable condition")
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return condition, localWakeupConditionInputError("condition must be one JSON object")
	}
	return condition, nil
}

func localWakeupConditionExists(ctx context.Context, q localWakeupQuery, statement string, args ...any) (bool, error) {
	var exists bool
	if err := q.QueryRowContext(ctx, "SELECT EXISTS("+statement+")", args...).Scan(&exists); err != nil {
		return false, fmt.Errorf("validate local wakeup condition reference: %w", err)
	}
	return exists, nil
}

func validateLocalWakeupCondition(ctx context.Context, q localWakeupQuery, workspaceID, issueID string, raw json.RawMessage) (json.RawMessage, error) {
	c, err := decodeLocalWakeupCondition(raw)
	if err != nil {
		return nil, err
	}
	issueExists, err := localWakeupConditionExists(ctx, q, "SELECT 1 FROM issues WHERE id=? AND workspace_id=?", issueID, workspaceID)
	if err != nil {
		return nil, err
	}
	if !issueExists {
		return nil, localWakeupConditionInputError("unknown issue")
	}
	requireUUID := func(value, field string) (string, error) {
		parsed, err := uuid.Parse(value)
		if err != nil || parsed == uuid.Nil {
			return "", localWakeupConditionInputError(field + " must be a UUID")
		}
		return parsed.String(), nil
	}
	requireReference := func(statement, message string, args ...any) error {
		exists, err := localWakeupConditionExists(ctx, q, statement, args...)
		if err != nil {
			return err
		}
		if !exists {
			return localWakeupConditionInputError(message)
		}
		return nil
	}
	var out service.WakeupCondition
	switch c.Type {
	case "issue_field":
		out = service.WakeupCondition{Type: c.Type, Field: c.Field}
		switch c.Field {
		case "status":
			var value string
			if json.Unmarshal(c.Value, &value) != nil || value == "" {
				return nil, localWakeupConditionInputError("status value must be a status key")
			}
			if err := requireReference("SELECT 1 FROM issue_statuses WHERE workspace_id=? AND key=?", "unknown status", workspaceID, value); err != nil {
				return nil, err
			}
			out.Value, err = json.Marshal(value)
		case "assignee":
			id, parseErr := requireUUID(c.AssigneeID, "assignee_id")
			if parseErr != nil {
				return nil, parseErr
			}
			var statement string
			switch c.AssigneeType {
			case "member":
				statement = "SELECT 1 FROM local_identity_context WHERE workspace_id=? AND operator_id=?"
			case "agent":
				statement = "SELECT 1 FROM agents WHERE workspace_id=? AND id=? AND archived_at IS NULL"
			case "squad":
				statement = "SELECT 1 FROM squads WHERE workspace_id=? AND id=? AND archived_at IS NULL"
			default:
				return nil, localWakeupConditionInputError("assignee_type must be member, agent or squad")
			}
			if err := requireReference(statement, "unknown assignee", workspaceID, id); err != nil {
				return nil, err
			}
			out.AssigneeType, out.AssigneeID = c.AssigneeType, id
		case "label":
			id, parseErr := requireUUID(c.LabelID, "label_id")
			if parseErr != nil {
				return nil, parseErr
			}
			if err := requireReference("SELECT 1 FROM labels WHERE workspace_id=? AND id=? AND resource_type='issue'", "unknown label", workspaceID, id); err != nil {
				return nil, err
			}
			out.LabelID = id
		case "property":
			id, parseErr := requireUUID(c.PropertyID, "property_id")
			if parseErr != nil {
				return nil, parseErr
			}
			if err := requireReference("SELECT 1 FROM issue_properties WHERE workspace_id=? AND id=? AND archived_at IS NULL", "unknown property", workspaceID, id); err != nil {
				return nil, err
			}
			value, valueErr := canonicalLocalWakeupJSON(c.Value)
			if valueErr != nil || string(value) == "null" || len(value) > 1024 {
				return nil, localWakeupConditionInputError("property value must be a JSON value within 1 KB")
			}
			out.PropertyID, out.Value = id, value
		default:
			return nil, localWakeupConditionInputError("field must be status, assignee, label or property")
		}
	case "children_done":
		if c.EachStage {
			return nil, localWakeupConditionInputError("each_stage is reserved for the platform's sub-issue rule")
		}
		if c.Stage != nil && (*c.Stage < 1 || *c.Stage > 1000) {
			return nil, localWakeupConditionInputError("stage must be a positive number")
		}
		out = service.WakeupCondition{Type: c.Type, Stage: c.Stage}
	case "pull_request":
		if !slices.Contains([]string{"checks_finished", "merged", "conflicts"}, c.Event) {
			return nil, localWakeupConditionInputError("event must be checks_finished, merged or conflicts")
		}
		out = service.WakeupCondition{Type: c.Type, Event: c.Event}
	case "other_issue":
		id, parseErr := requireUUID(c.IssueID, "issue_id")
		if parseErr != nil {
			return nil, parseErr
		}
		if id == issueID {
			return nil, localWakeupConditionInputError("watch another issue, not this one")
		}
		if !slices.Contains([]string{"done", "ended", "in_review"}, c.State) {
			return nil, localWakeupConditionInputError("state must be done, ended or in_review")
		}
		var identifier string
		if err := q.QueryRowContext(ctx, `SELECT w.issue_prefix||'-'||i.number FROM issues i JOIN workspaces w ON w.id=i.workspace_id WHERE i.id=? AND i.workspace_id=?`, id, workspaceID).Scan(&identifier); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil, localWakeupConditionInputError("unknown issue")
			}
			return nil, fmt.Errorf("read local watched issue: %w", err)
		}
		out = service.WakeupCondition{Type: c.Type, IssueID: id, State: c.State, Identifier: identifier}
	default:
		return nil, localWakeupConditionInputError("type must be issue_field, children_done, pull_request or other_issue")
	}
	if err != nil {
		return nil, fmt.Errorf("encode local condition value: %w", err)
	}
	normalized, err := json.Marshal(out)
	if err != nil {
		return nil, fmt.Errorf("encode local wakeup condition: %w", err)
	}
	return normalized, nil
}

func canonicalLocalWakeupJSON(raw json.RawMessage) (json.RawMessage, error) {
	var value any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return nil, errors.New("JSON value has trailing data")
	}
	return json.Marshal(value)
}

func evaluateLocalWakeupCondition(ctx context.Context, q localWakeupQuery, workspaceID, issueID string, raw json.RawMessage) (bool, string, error) {
	c, err := decodeLocalWakeupCondition(raw)
	if err != nil {
		return false, "", err
	}
	var status, category, assigneeType, assigneeID, properties string
	err = q.QueryRowContext(ctx, `SELECT i.status,COALESCE(s.category,i.status),COALESCE(i.assignee_type,''),COALESCE(i.assignee_id,''),i.properties FROM issues i LEFT JOIN issue_statuses s ON s.workspace_id=i.workspace_id AND s.key=i.status WHERE i.id=? AND i.workspace_id=?`, issueID, workspaceID).Scan(&status, &category, &assigneeType, &assigneeID, &properties)
	if err != nil {
		return false, "", fmt.Errorf("read local condition issue: %w", err)
	}
	switch c.Type {
	case "issue_field":
		switch c.Field {
		case "status":
			var expected string
			if err := json.Unmarshal(c.Value, &expected); err != nil {
				return false, "", localWakeupConditionInputError("status value must be a string")
			}
			return status == expected, "status:" + status, nil
		case "assignee":
			return assigneeType == c.AssigneeType && assigneeID == c.AssigneeID, "assignee:" + assigneeType + ":" + assigneeID, nil
		case "label":
			attached, err := localWakeupConditionExists(ctx, q, "SELECT 1 FROM resource_labels WHERE workspace_id=? AND resource_type='issue' AND resource_id=? AND label_id=?", workspaceID, issueID, c.LabelID)
			return attached, "label:" + c.LabelID, err
		case "property":
			var values map[string]json.RawMessage
			if err := json.Unmarshal([]byte(properties), &values); err != nil {
				return false, "", fmt.Errorf("read local condition properties: %w", err)
			}
			actual := values[c.PropertyID]
			if len(actual) == 0 {
				actual = json.RawMessage("null")
			}
			actual, err = canonicalLocalWakeupJSON(actual)
			if err != nil {
				return false, "", fmt.Errorf("read local condition property: %w", err)
			}
			expected, err := canonicalLocalWakeupJSON(c.Value)
			if err != nil {
				return false, "", localWakeupConditionInputError("invalid property value")
			}
			return bytes.Equal(actual, expected), "property:" + string(actual), nil
		}
	case "children_done":
		return evaluateLocalWakeupChildren(ctx, q, workspaceID, issueID, c)
	case "pull_request":
		return evaluateLocalWakeupPullRequests(ctx, q, workspaceID, issueID, c.Event)
	case "other_issue":
		err := q.QueryRowContext(ctx, `SELECT i.status,COALESCE(s.category,i.status) FROM issues i LEFT JOIN issue_statuses s ON s.workspace_id=i.workspace_id AND s.key=i.status WHERE i.id=? AND i.workspace_id=?`, c.IssueID, workspaceID).Scan(&status, &category)
		if errors.Is(err, sql.ErrNoRows) {
			return false, "", nil
		}
		if err != nil {
			return false, "", fmt.Errorf("read local watched issue state: %w", err)
		}
		category = localStatusCategory(category)
		switch c.State {
		case "done":
			return category == "done", "issue:" + c.IssueID + ":" + status, nil
		case "ended":
			return category == "done" || category == "closed", "issue:" + c.IssueID + ":" + status, nil
		case "in_review":
			return status == "in_review", "issue:" + c.IssueID + ":" + status, nil
		}
	}
	return false, "", localWakeupConditionInputError("unknown condition")
}

func evaluateLocalWakeupChildren(ctx context.Context, q localWakeupQuery, workspaceID, issueID string, c service.WakeupCondition) (bool, string, error) {
	rows, err := q.QueryContext(ctx, `SELECT i.id,i.stage,COALESCE(s.category,i.status) FROM issues i LEFT JOIN issue_statuses s ON s.workspace_id=i.workspace_id AND s.key=i.status WHERE i.parent_issue_id=? AND i.workspace_id=? ORDER BY i.id`, issueID, workspaceID)
	if err != nil {
		return false, "", fmt.Errorf("read local condition children: %w", err)
	}
	defer rows.Close()
	type stageCounts struct{ total, closed int }
	counts := map[int32]stageCounts{}
	total, closed, inStage := 0, 0, 0
	for rows.Next() {
		var id, category string
		var stage sql.NullInt32
		if err := rows.Scan(&id, &stage, &category); err != nil {
			return false, "", fmt.Errorf("scan local condition child: %w", err)
		}
		if c.Stage != nil && (!stage.Valid || stage.Int32 > *c.Stage) {
			continue
		}
		if c.Stage != nil && stage.Valid && stage.Int32 == *c.Stage {
			inStage++
		}
		total++
		category = localStatusCategory(category)
		isClosed := category == "done" || category == "closed"
		if isClosed {
			closed++
		}
		if stage.Valid {
			count := counts[stage.Int32]
			count.total++
			if isClosed {
				count.closed++
			}
			counts[stage.Int32] = count
		}
	}
	if err := rows.Err(); err != nil {
		return false, "", fmt.Errorf("iterate local condition children: %w", err)
	}
	if c.EachStage {
		if total > 0 && total == closed {
			return true, "all", nil
		}
		stages := make([]int32, 0, len(counts))
		for stage := range counts {
			stages = append(stages, stage)
		}
		slices.Sort(stages)
		var frontier int32
		for _, stage := range stages {
			count := counts[stage]
			if count.closed < count.total {
				if frontier > 0 {
					return true, fmt.Sprintf("stage:%d", frontier), nil
				}
				break
			}
			frontier = stage
		}
		return false, "", nil
	}
	met := total > 0 && total == closed && (c.Stage == nil || inStage > 0)
	return met, fmt.Sprintf("children:%d", total), nil
}

func evaluateLocalWakeupPullRequests(ctx context.Context, q localWakeupQuery, workspaceID, issueID, event string) (bool, string, error) {
	rows, err := q.QueryContext(ctx, `SELECT p.id,p.provider,p.state,COALESCE(p.head_sha,''),COALESCE(p.snapshot_head_sha,''),COALESCE(p.checks_rollup_state,''),COALESCE(p.api_mergeable,''),
		COALESCE(SUM(CASE WHEN cs.state IN ('failed','failure','error') THEN 1 ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN cs.state NOT IN ('passed','success','failed','failure','error') THEN 1 ELSE 0 END),0),COUNT(cs.state)
		FROM vcs_pull_requests p JOIN issue_vcs_pull_requests l ON l.pull_request_id=p.id
		LEFT JOIN vcs_commit_statuses cs ON cs.connection_id=p.connection_id AND cs.sha=p.head_sha AND p.head_sha<>''
		WHERE p.workspace_id=? AND l.issue_id=? GROUP BY p.id ORDER BY p.id`, workspaceID, issueID)
	if err != nil {
		return false, "", fmt.Errorf("read local pull request condition: %w", err)
	}
	defer rows.Close()
	linked, github, missingSnapshot, unknownMergeability := 0, 0, false, false
	keys := []string{}
	for rows.Next() {
		var id, provider, state, head, snapshotHead, checks, mergeable string
		var failed, pending, total int
		if err := rows.Scan(&id, &provider, &state, &head, &snapshotHead, &checks, &mergeable, &failed, &pending, &total); err != nil {
			return false, "", fmt.Errorf("scan local pull request condition: %w", err)
		}
		linked++
		isGitHub := provider == "github"
		if isGitHub {
			github++
		}
		switch event {
		case "merged":
			if state == "merged" {
				keys = append(keys, "vcs:"+id)
			}
		case "checks_finished":
			verdict := ""
			if isGitHub {
				if snapshotHead == "" || snapshotHead != head {
					missingSnapshot = true
					continue
				}
				if slices.Contains([]string{"SUCCESS", "FAILURE", "ERROR"}, strings.ToUpper(checks)) {
					verdict = strings.ToUpper(checks)
				}
			} else if failed > 0 {
				verdict = "FAILURE"
			} else if total > 0 && pending == 0 {
				verdict = "SUCCESS"
			}
			if head != "" && verdict != "" {
				keys = append(keys, "vcs:"+id+"@"+head+":"+verdict)
			}
		case "conflicts":
			if !isGitHub || (state != "open" && state != "draft") {
				continue
			}
			if snapshotHead == "" || snapshotHead != head {
				missingSnapshot = true
				continue
			}
			if strings.EqualFold(mergeable, "CONFLICTING") {
				keys = append(keys, id+"@"+head+":conflicting")
			} else if mergeable == "" || strings.EqualFold(mergeable, "UNKNOWN") {
				unknownMergeability = true
			}
		default:
			return false, "", localWakeupConditionInputError("unknown pull request event")
		}
	}
	if err := rows.Err(); err != nil {
		return false, "", fmt.Errorf("iterate local pull request condition: %w", err)
	}
	if len(keys) == 0 {
		if linked == 0 {
			return false, "", errLocalWakeupPRNotLinked
		}
		if event == "conflicts" && github == 0 {
			return false, "", errLocalWakeupPRConflictsUnsupported
		}
		if missingSnapshot {
			return false, "", errLocalWakeupPRSnapshotPending
		}
		if unknownMergeability {
			return false, "", errLocalWakeupPRMergeabilityPending
		}
	}
	sort.Strings(keys)
	return len(keys) > 0, "pr:" + strings.Join(keys, ","), nil
}
