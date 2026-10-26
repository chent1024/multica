package localworkbench

import "fmt"

var localRuntimeMetadataMigration = migration{
	version: 23, name: "authoritative daemon runtime metadata",
	statements: []string{`ALTER TABLE runtimes ADD COLUMN metadata TEXT NOT NULL DEFAULT '{}'`},
}

var localWakeupsMigration = migration{
	version: 24, name: "durable local issue wakeups",
	statements: localWakeupSchema(),
}

var localVCSWakeupSnapshotMigration = migration{
	version: 25, name: "local pull request condition snapshots",
	statements: []string{
		`ALTER TABLE vcs_pull_requests ADD COLUMN snapshot_head_sha TEXT`,
		`ALTER TABLE vcs_pull_requests ADD COLUMN checks_rollup_state TEXT`,
		`ALTER TABLE vcs_pull_requests ADD COLUMN api_mergeable TEXT`,
	},
}

func localWakeupSchema() []string {
	statements := []string{
		`CREATE TABLE issue_wakeups (
   id TEXT PRIMARY KEY, workspace_id TEXT NOT NULL, issue_id TEXT NOT NULL,
   agent_id TEXT NOT NULL, created_by TEXT NOT NULL, config TEXT NOT NULL,
   enabled INTEGER NOT NULL DEFAULT 1, revision INTEGER NOT NULL DEFAULT 1,
   next_fire_at TEXT, expires_at TEXT, disabled_at TEXT, last_task_id TEXT,
   last_error TEXT, timed_out_at TEXT, paused_reason TEXT,
   fire_count INTEGER NOT NULL DEFAULT 0, created_at TEXT NOT NULL, updated_at TEXT NOT NULL,
   source_task_id TEXT
  )`,
		`CREATE INDEX IF NOT EXISTS issue_wakeups_scope_idx ON issue_wakeups(workspace_id,issue_id,enabled,id)`,
		`CREATE INDEX IF NOT EXISTS issue_wakeups_due_idx ON issue_wakeups(enabled,next_fire_at,id)`,
		`CREATE TABLE issue_wakeup_events (
   id INTEGER PRIMARY KEY AUTOINCREMENT, workspace_id TEXT NOT NULL, issue_id TEXT NOT NULL,
   event_type TEXT NOT NULL, actor_type TEXT, actor_id TEXT, source_task_id TEXT, source_agent_id TEXT,
   payload TEXT NOT NULL DEFAULT '{}', created_at TEXT NOT NULL
  )`,
		`CREATE INDEX IF NOT EXISTS issue_wakeup_events_issue_idx ON issue_wakeup_events(workspace_id,issue_id,id)`,
		`CREATE TABLE issue_wakeup_activations (wakeup_id TEXT PRIMARY KEY,event_cursor INTEGER NOT NULL DEFAULT 0)`,
		`CREATE TABLE issue_wakeup_receipts (
   id TEXT PRIMARY KEY, wakeup_id TEXT NOT NULL,
   event_key TEXT NOT NULL, event_type TEXT NOT NULL, payload TEXT NOT NULL DEFAULT '{}',
   source_task_id TEXT, task_id TEXT, created_at TEXT NOT NULL,
   UNIQUE(wakeup_id,event_key)
  )`,
		`CREATE INDEX IF NOT EXISTS issue_wakeup_receipts_pending_idx ON issue_wakeup_receipts(wakeup_id,task_id,created_at,id)`,
		`CREATE TABLE issue_wakeup_runs (
   task_id TEXT NOT NULL,wakeup_id TEXT NOT NULL,occurrence_key TEXT NOT NULL,
   checkin_note TEXT NOT NULL DEFAULT '',triggers TEXT NOT NULL DEFAULT '[]',created_at TEXT NOT NULL,
   PRIMARY KEY(wakeup_id,task_id),UNIQUE(wakeup_id,occurrence_key)
  )`,
		`CREATE INDEX IF NOT EXISTS issue_wakeup_runs_rule_idx ON issue_wakeup_runs(wakeup_id,created_at DESC,task_id)`,
		`CREATE TABLE issue_wakeup_condition_state (
   wakeup_id TEXT PRIMARY KEY,evidence TEXT NOT NULL DEFAULT '',
   updated_at TEXT NOT NULL
  )`,
		`CREATE TABLE issue_system_wakeups (
   workspace_id TEXT NOT NULL,issue_id TEXT NOT NULL DEFAULT '',
   enabled INTEGER NOT NULL DEFAULT 1,instruction TEXT NOT NULL DEFAULT '',revision INTEGER NOT NULL DEFAULT 1,
   last_evidence TEXT NOT NULL DEFAULT '',created_at TEXT NOT NULL,updated_at TEXT NOT NULL,
   wakeup_id TEXT,customized INTEGER NOT NULL DEFAULT 0,
   PRIMARY KEY(workspace_id,issue_id)
  )`,
		`ALTER TABLE task_runs ADD COLUMN wakeup_id TEXT`,
		`ALTER TABLE task_runs ADD COLUMN wakeup_joined TEXT NOT NULL DEFAULT ''`,
		`CREATE TABLE issue_wakeup_joins(task_id TEXT NOT NULL,wakeup_id TEXT NOT NULL,revision INTEGER NOT NULL,receipt_ids TEXT NOT NULL,created_at TEXT NOT NULL,PRIMARY KEY(task_id,wakeup_id))`,
	}
	// Capture in the same SQLite transaction as the business mutation. A rollback
	// removes the fact as well; the scheduler never depends on an in-memory bus.
	stamp := `strftime('%Y-%m-%dT%H:%M:%fZ','now')`
	eventSQL := func(ws, issue, kind, actorType, actorID, taskID, agentID, payload string) string {
		return fmt.Sprintf(`INSERT INTO issue_wakeup_events(workspace_id,issue_id,event_type,actor_type,actor_id,source_task_id,source_agent_id,payload,created_at) SELECT %s,%s,%s,%s,%s,%s,%s,%s,%s WHERE %s IS NOT NULL;`, ws, issue, kind, actorType, actorID, taskID, agentID, payload, stamp, issue)
	}
	for _, field := range []struct{ column, event string }{
		{"status", "issue.status_changed"}, {"assignee_type", "issue.assignee_changed"}, {"assignee_id", "issue.assignee_changed"},
		{"parent_issue_id", "issue.parent_changed"}, {"project_id", "issue.project_changed"},
		{"properties", "issue.properties_changed"}, {"metadata", "issue.metadata_changed"},
		{"stage", "issue.updated"}, {"title", "issue.updated"}, {"description", "issue.updated"}, {"priority", "issue.updated"}, {"start_date", "issue.updated"}, {"due_date", "issue.updated"},
	} {
		body := eventSQL("NEW.workspace_id", "NEW.id", "'"+field.event+"'", "'member'", "'"+LocalOperatorID+"'", "NULL", "NULL", `json_object('field','`+field.column+`','before',OLD.`+field.column+`,'after',NEW.`+field.column+`)`)
		statements = append(statements, fmt.Sprintf(`CREATE TRIGGER issue_wakeup_issue_%s AFTER UPDATE OF %s ON issues WHEN NEW.%s IS NOT OLD.%s BEGIN %s END`, field.column, field.column, field.column, field.column, body))
	}
	for _, operation := range []struct{ op, kind, row string }{{"INSERT", "comment.created", "NEW"}, {"UPDATE", "comment.updated", "NEW"}, {"DELETE", "comment.deleted", "OLD"}} {
		row := operation.row
		actorType := `CASE WHEN ` + row + `.author_type='agent' THEN 'agent' ELSE 'member' END`
		actorID := `CASE WHEN ` + row + `.author_type='agent' THEN COALESCE((SELECT agent_id FROM task_runs WHERE id=` + row + `.source_task_id),` + row + `.author_id) ELSE '` + LocalOperatorID + `' END`
		body := eventSQL(`(SELECT workspace_id FROM issues WHERE id=`+row+`.issue_id)`, row+`.issue_id`, `'`+operation.kind+`'`, actorType, actorID, row+`.source_task_id`, `(SELECT agent_id FROM task_runs WHERE id=`+row+`.source_task_id)`, `json_object('comment_id',`+row+`.id)`)
		when := ""
		if operation.op == "UPDATE" {
			when = ` WHEN NEW.content IS NOT OLD.content OR NEW.type IS NOT OLD.type`
		}
		statements = append(statements, `CREATE TRIGGER issue_wakeup_comment_`+operation.op+` AFTER `+operation.op+` ON comments`+when+` BEGIN `+body+` END`)
	}
	for _, state := range []struct{ when, event string }{{"NEW.resolved_at IS NOT NULL AND OLD.resolved_at IS NULL", "comment.resolved"}, {"NEW.resolved_at IS NULL AND OLD.resolved_at IS NOT NULL", "comment.unresolved"}} {
		statements = append(statements, `CREATE TRIGGER issue_wakeup_`+state.event[len("comment."):]+` AFTER UPDATE OF resolved_at ON comments WHEN `+state.when+` BEGIN `+eventSQL(`(SELECT workspace_id FROM issues WHERE id=NEW.issue_id)`, `NEW.issue_id`, `'`+state.event+`'`, `COALESCE(NEW.resolved_by_type,'member')`, `COALESCE(NEW.resolved_by_id,'`+LocalOperatorID+`')`, `NEW.source_task_id`, `(SELECT agent_id FROM task_runs WHERE id=NEW.source_task_id)`, `json_object('comment_id',NEW.id)`)+` END`)
	}
	for _, operation := range []string{"INSERT", "UPDATE"} {
		when := ""
		if operation == "UPDATE" {
			when = ` WHEN NEW.status<>OLD.status`
		}
		kind := `'task.' || CASE NEW.status WHEN 'running' THEN 'started' ELSE NEW.status END`
		statements = append(statements, `CREATE TRIGGER issue_wakeup_task_`+operation+` AFTER `+operation+` ON task_runs`+when+` BEGIN `+eventSQL(`(SELECT workspace_id FROM issues WHERE id=NEW.issue_id)`, `NEW.issue_id`, kind, `'agent'`, `NEW.agent_id`, `NEW.id`, `NEW.agent_id`, `json_object('task_id',NEW.id,'status',NEW.status,'wakeup_id',NEW.wakeup_id)`)+` END`)
	}
	for _, operation := range []struct{ op, row, kind string }{{"INSERT", "NEW", "issue.labels_changed"}, {"DELETE", "OLD", "issue.labels_changed"}} {
		row := operation.row
		statements = append(statements, `CREATE TRIGGER issue_wakeup_labels_`+operation.op+` AFTER `+operation.op+` ON resource_labels WHEN `+row+`.resource_type='issue' BEGIN `+eventSQL(row+`.workspace_id`, row+`.resource_id`, `'`+operation.kind+`'`, `'member'`, `'`+LocalOperatorID+`'`, `NULL`, `NULL`, `json_object('label_id',`+row+`.label_id)`)+` END`)
	}
	for _, operation := range []struct{ op, row, kind string }{{"INSERT", "NEW", "reaction.added"}, {"DELETE", "OLD", "reaction.removed"}} {
		row := operation.row
		issue := `CASE WHEN ` + row + `.resource_type='issue' THEN ` + row + `.resource_id ELSE (SELECT issue_id FROM comments WHERE id=` + row + `.resource_id) END`
		statements = append(statements, `CREATE TRIGGER issue_wakeup_reaction_`+operation.op+` AFTER `+operation.op+` ON reactions BEGIN `+eventSQL(row+`.workspace_id`, issue, `'`+operation.kind+`'`, row+`.actor_type`, row+`.actor_id`, `NULL`, `NULL`, `json_object('emoji',`+row+`.emoji)`)+` END`)
	}
	for _, operation := range []struct{ name, op, issue, kind, when string }{
		{"attach", "INSERT", "NEW.issue_id", "attachment.attached", "NEW.issue_id IS NOT NULL"},
		{"bind", "UPDATE", "NEW.issue_id", "attachment.attached", "NEW.issue_id IS NOT NULL AND OLD.issue_id IS NOT NEW.issue_id"},
		{"detach", "DELETE", "OLD.issue_id", "attachment.detached", "OLD.issue_id IS NOT NULL"},
	} {
		row := "NEW"
		if operation.op == "DELETE" {
			row = "OLD"
		}
		statements = append(statements, `CREATE TRIGGER issue_wakeup_attachment_`+operation.name+` AFTER `+operation.op+` ON attachments WHEN `+operation.when+` BEGIN `+eventSQL(`(SELECT workspace_id FROM issues WHERE id=`+operation.issue+`)`, operation.issue, `'`+operation.kind+`'`, `'member'`, `'`+LocalOperatorID+`'`, `NULL`, `NULL`, `json_object('attachment_id',`+row+`.id)`)+` END`)
	}

	return statements
}

var localIssueDuplicatesMigration = migration{
	version: 26, name: "local issue duplicate relationships",
	statements: []string{`ALTER TABLE issues ADD COLUMN duplicate_of_issue_id TEXT`, `CREATE INDEX IF NOT EXISTS issues_duplicate_target_idx ON issues(workspace_id,duplicate_of_issue_id)`},
}

var localProjectWorkflowsMigration = migration{
	version: 27, name: "local project workflows and handoff provenance",
	statements: []string{
		`CREATE TABLE issue_workflows(id TEXT PRIMARY KEY,workspace_id TEXT NOT NULL,name TEXT NOT NULL COLLATE NOCASE,description TEXT NOT NULL DEFAULT '',is_default INTEGER NOT NULL DEFAULT 0,owner_id TEXT,steps TEXT NOT NULL,created_at TEXT NOT NULL,updated_at TEXT NOT NULL,UNIQUE(workspace_id,name))`,
		`CREATE UNIQUE INDEX IF NOT EXISTS issue_workflows_default_idx ON issue_workflows(workspace_id) WHERE is_default=1`,
		`ALTER TABLE projects ADD COLUMN workflow_id TEXT`,
		`ALTER TABLE task_runs ADD COLUMN workflow_step TEXT`,
		`ALTER TABLE task_runs ADD COLUMN workflow_id TEXT`,
	},
}

var localTaskStartClaimMigration = migration{
	version: 28, name: "local task start claim fencing",
	statements: []string{`ALTER TABLE task_runs ADD COLUMN dispatched_at TEXT`},
}
