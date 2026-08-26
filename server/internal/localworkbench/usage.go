package localworkbench

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

var errInvalidTaskRunUsage = errors.New("invalid task run usage")

type TaskRunUsage struct {
	Provider         string `json:"provider"`
	Model            string `json:"model"`
	InputTokens      int64  `json:"input_tokens"`
	OutputTokens     int64  `json:"output_tokens"`
	CacheReadTokens  int64  `json:"cache_read_tokens"`
	CacheWriteTokens int64  `json:"cache_write_tokens"`
	CostUSDTicks     *int64 `json:"cost_usd_ticks,omitempty"`
}

type IssueUsageSummary struct {
	TotalInputTokens         int64 `json:"total_input_tokens"`
	TotalOutputTokens        int64 `json:"total_output_tokens"`
	TotalCacheReadTokens     int64 `json:"total_cache_read_tokens"`
	TotalCacheWriteTokens    int64 `json:"total_cache_write_tokens"`
	CostUSDTicks             int64 `json:"cost_usd_ticks"`
	UncostedInputTokens      int64 `json:"uncosted_input_tokens"`
	UncostedOutputTokens     int64 `json:"uncosted_output_tokens"`
	UncostedCacheReadTokens  int64 `json:"uncosted_cache_read_tokens"`
	UncostedCacheWriteTokens int64 `json:"uncosted_cache_write_tokens"`
	TaskCount                int64 `json:"task_count"`
}

func normalizeTaskRunUsage(items []TaskRunUsage) ([]TaskRunUsage, error) {
	normalized := make([]TaskRunUsage, len(items))
	seen := make(map[string]struct{}, len(items))
	for i, item := range items {
		item.Provider = strings.TrimSpace(item.Provider)
		item.Model = strings.TrimSpace(item.Model)
		if item.Provider == "" || item.Model == "" {
			return nil, fmt.Errorf("%w: provider and model are required", errInvalidTaskRunUsage)
		}
		if item.InputTokens < 0 || item.OutputTokens < 0 || item.CacheReadTokens < 0 || item.CacheWriteTokens < 0 || (item.CostUSDTicks != nil && *item.CostUSDTicks < 0) {
			return nil, fmt.Errorf("%w: counters must be non-negative", errInvalidTaskRunUsage)
		}
		key := item.Provider + "\x00" + item.Model
		if _, ok := seen[key]; ok {
			return nil, fmt.Errorf("%w: duplicate provider and model", errInvalidTaskRunUsage)
		}
		seen[key] = struct{}{}
		normalized[i] = item
	}
	return normalized, nil
}

func (s *Store) ReplaceTaskRunUsage(ctx context.Context, workspaceID, taskID string, items []TaskRunUsage) error {
	normalized, err := normalizeTaskRunUsage(items)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var exists int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM task_runs tr JOIN issues i ON i.id=tr.issue_id WHERE tr.id=? AND i.workspace_id=?`, taskID, workspaceID).Scan(&exists); err != nil {
		return err
	}
	if exists == 0 {
		return sql.ErrNoRows
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM task_run_usage WHERE task_id=?`, taskID); err != nil {
		return err
	}
	stamp := now()
	for _, item := range normalized {
		if _, err := tx.ExecContext(ctx, `INSERT INTO task_run_usage(task_id, provider, model, input_tokens, output_tokens, cache_read_tokens, cache_write_tokens, cost_usd_ticks, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, taskID, item.Provider, item.Model, item.InputTokens, item.OutputTokens, item.CacheReadTokens, item.CacheWriteTokens, item.CostUSDTicks, stamp); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func scanTaskRunUsageRows(rows *sql.Rows) (map[string][]TaskRunUsage, error) {
	result := map[string][]TaskRunUsage{}
	for rows.Next() {
		var taskID string
		var item TaskRunUsage
		var cost sql.NullInt64
		if err := rows.Scan(&taskID, &item.Provider, &item.Model, &item.InputTokens, &item.OutputTokens, &item.CacheReadTokens, &item.CacheWriteTokens, &cost); err != nil {
			return nil, err
		}
		item.CostUSDTicks = intPtr(cost)
		result[taskID] = append(result[taskID], item)
	}
	return result, rows.Err()
}

func (s *Store) TaskRunUsageByIssue(ctx context.Context, workspaceID, issueID string) (map[string][]TaskRunUsage, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT u.task_id, u.provider, u.model, u.input_tokens, u.output_tokens, u.cache_read_tokens, u.cache_write_tokens, u.cost_usd_ticks FROM task_run_usage u JOIN task_runs tr ON tr.id=u.task_id JOIN issues i ON i.id=tr.issue_id WHERE tr.issue_id=? AND i.workspace_id=? ORDER BY u.task_id, u.provider, u.model`, issueID, workspaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanTaskRunUsageRows(rows)
}

func (s *Store) TaskRunUsageForTask(ctx context.Context, workspaceID, issueID, taskID string) ([]TaskRunUsage, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT u.task_id, u.provider, u.model, u.input_tokens, u.output_tokens, u.cache_read_tokens, u.cache_write_tokens, u.cost_usd_ticks FROM task_run_usage u JOIN task_runs tr ON tr.id=u.task_id JOIN issues i ON i.id=tr.issue_id WHERE u.task_id=? AND tr.issue_id=? AND i.workspace_id=? ORDER BY u.provider, u.model`, taskID, issueID, workspaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	grouped, err := scanTaskRunUsageRows(rows)
	if err != nil {
		return nil, err
	}
	items := grouped[taskID]
	if items == nil {
		items = []TaskRunUsage{}
	}
	return items, nil
}

func (s *Store) IssueUsage(ctx context.Context, workspaceID, issueID string) (IssueUsageSummary, error) {
	if _, err := s.GetIssue(ctx, workspaceID, issueID); err != nil {
		return IssueUsageSummary{}, err
	}
	var summary IssueUsageSummary
	err := s.db.QueryRowContext(ctx, `SELECT
		COALESCE(SUM(u.input_tokens), 0),
		COALESCE(SUM(u.output_tokens), 0),
		COALESCE(SUM(u.cache_read_tokens), 0),
		COALESCE(SUM(u.cache_write_tokens), 0),
		COALESCE(SUM(u.cost_usd_ticks), 0),
		COALESCE(SUM(CASE WHEN u.cost_usd_ticks IS NULL THEN u.input_tokens ELSE 0 END), 0),
		COALESCE(SUM(CASE WHEN u.cost_usd_ticks IS NULL THEN u.output_tokens ELSE 0 END), 0),
		COALESCE(SUM(CASE WHEN u.cost_usd_ticks IS NULL THEN u.cache_read_tokens ELSE 0 END), 0),
		COALESCE(SUM(CASE WHEN u.cost_usd_ticks IS NULL THEN u.cache_write_tokens ELSE 0 END), 0),
		(SELECT COUNT(*) FROM task_runs WHERE issue_id=?)
	FROM task_runs tr LEFT JOIN task_run_usage u ON u.task_id=tr.id WHERE tr.issue_id=?`, issueID, issueID).Scan(
		&summary.TotalInputTokens,
		&summary.TotalOutputTokens,
		&summary.TotalCacheReadTokens,
		&summary.TotalCacheWriteTokens,
		&summary.CostUSDTicks,
		&summary.UncostedInputTokens,
		&summary.UncostedOutputTokens,
		&summary.UncostedCacheReadTokens,
		&summary.UncostedCacheWriteTokens,
		&summary.TaskCount,
	)
	return summary, err
}
