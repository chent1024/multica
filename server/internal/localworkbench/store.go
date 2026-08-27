package localworkbench

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

const (
	LocalOperatorID    = "00000000-0000-4000-8000-000000000001"
	LocalWorkspaceID   = "00000000-0000-4000-8000-000000000002"
	LocalWorkspaceSlug = "local"
)

type Store struct {
	db *sql.DB
}

type migration struct {
	version    int
	name       string
	statements []string
}

var schemaMigrations = []migration{
	{
		version: 1,
		name:    "local identity context",
		statements: []string{
			`CREATE TABLE users (
				id TEXT PRIMARY KEY,
				name TEXT NOT NULL,
				email TEXT NOT NULL UNIQUE,
				avatar_url TEXT,
				onboarded_at TEXT,
				onboarding_questionnaire TEXT NOT NULL DEFAULT '{}',
				starter_content_state TEXT,
				language TEXT,
				profile_description TEXT NOT NULL DEFAULT '',
				timezone TEXT,
				created_at TEXT NOT NULL,
				updated_at TEXT NOT NULL
			)`,
			`CREATE TABLE workspaces (
				id TEXT PRIMARY KEY,
				name TEXT NOT NULL,
				slug TEXT NOT NULL UNIQUE,
				description TEXT,
				context TEXT,
				settings TEXT NOT NULL DEFAULT '{}',
				repos TEXT NOT NULL DEFAULT '[]',
				issue_prefix TEXT NOT NULL DEFAULT 'LOCAL',
				avatar_url TEXT,
				created_at TEXT NOT NULL,
				updated_at TEXT NOT NULL
			)`,
			`CREATE TABLE local_identity_context (
				id INTEGER PRIMARY KEY CHECK (id = 1),
				workspace_id TEXT NOT NULL UNIQUE,
				operator_id TEXT NOT NULL UNIQUE
			)`,
			`INSERT INTO users (
				id, name, email, onboarded_at, starter_content_state,
				created_at, updated_at
			) VALUES (
				'00000000-0000-4000-8000-000000000001', 'Local Operator',
				'local@multica.invalid', strftime('%Y-%m-%dT%H:%M:%fZ', 'now'),
				'imported', strftime('%Y-%m-%dT%H:%M:%fZ', 'now'),
				strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
			)`,
			`INSERT INTO workspaces (
				id, name, slug, created_at, updated_at
			) VALUES (
				'00000000-0000-4000-8000-000000000002', 'Local', 'local',
				strftime('%Y-%m-%dT%H:%M:%fZ', 'now'),
				strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
			)`,
			`INSERT INTO local_identity_context (id, workspace_id, operator_id)
			 VALUES (1, '00000000-0000-4000-8000-000000000002',
			 '00000000-0000-4000-8000-000000000001')`,
		},
	},
	{
		version: 2,
		name:    "durable local jobs",
		statements: []string{
			`CREATE TABLE local_jobs (
				id TEXT PRIMARY KEY,
				kind TEXT NOT NULL,
				payload TEXT NOT NULL DEFAULT '{}',
				status TEXT NOT NULL CHECK (status IN ('pending', 'running', 'completed', 'failed')),
				dedupe_key TEXT,
				available_at TEXT NOT NULL,
				lease_owner TEXT,
				lease_expires_at TEXT,
				attempt_count INTEGER NOT NULL DEFAULT 0 CHECK (attempt_count >= 0),
				error TEXT,
				created_at TEXT NOT NULL,
				updated_at TEXT NOT NULL
			)`,
			`CREATE UNIQUE INDEX IF NOT EXISTS local_jobs_dedupe_idx
			 ON local_jobs(kind, dedupe_key) WHERE dedupe_key IS NOT NULL`,
			`CREATE INDEX IF NOT EXISTS local_jobs_claim_idx
			 ON local_jobs(kind, status, available_at, lease_expires_at, created_at)`,
		},
	},
	{
		version: 3,
		name:    "projects and resources",
		statements: []string{
			`CREATE TABLE projects (
				id TEXT PRIMARY KEY,
				workspace_id TEXT NOT NULL,
				title TEXT NOT NULL,
				description TEXT,
				icon TEXT,
				status TEXT NOT NULL DEFAULT 'planned',
				priority TEXT NOT NULL DEFAULT 'none',
				lead_type TEXT,
				lead_id TEXT,
				start_date TEXT,
				due_date TEXT,
				created_at TEXT NOT NULL,
				updated_at TEXT NOT NULL
			)`,
			`CREATE INDEX IF NOT EXISTS projects_workspace_idx
			 ON projects(workspace_id, updated_at DESC, id)`,
			`CREATE TABLE project_resources (
				id TEXT PRIMARY KEY,
				project_id TEXT NOT NULL,
				workspace_id TEXT NOT NULL,
				resource_type TEXT NOT NULL,
				resource_ref TEXT NOT NULL,
				label TEXT,
				position INTEGER NOT NULL DEFAULT 0,
				created_at TEXT NOT NULL,
				created_by TEXT
			)`,
			`CREATE INDEX IF NOT EXISTS project_resources_project_idx
			 ON project_resources(project_id, position, created_at, id)`,
		},
	},
	{
		version: 4,
		name:    "tasks comments runs and runtimes",
		statements: []string{
			`CREATE TABLE issue_statuses (
				id TEXT PRIMARY KEY, workspace_id TEXT NOT NULL, key TEXT NOT NULL,
				name TEXT NOT NULL, description TEXT NOT NULL DEFAULT '', category TEXT NOT NULL,
				color TEXT NOT NULL, is_system INTEGER NOT NULL DEFAULT 0,
				position INTEGER NOT NULL DEFAULT 0, archived_at TEXT,
				created_at TEXT NOT NULL, updated_at TEXT NOT NULL,
				UNIQUE(workspace_id, key)
			)`,
			`CREATE INDEX IF NOT EXISTS issue_statuses_workspace_idx
			 ON issue_statuses(workspace_id, category, position)`,
			`INSERT INTO issue_statuses
			 (id, workspace_id, key, name, category, color, is_system, position, created_at, updated_at)
			 VALUES
			 ('00000000-0000-4000-8100-000000000001', '00000000-0000-4000-8000-000000000002', 'backlog', 'Backlog', 'backlog', '#8b8b8b', 1, 0, strftime('%Y-%m-%dT%H:%M:%fZ', 'now'), strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
			 ('00000000-0000-4000-8100-000000000002', '00000000-0000-4000-8000-000000000002', 'todo', 'Todo', 'unstarted', '#8b8b8b', 1, 1, strftime('%Y-%m-%dT%H:%M:%fZ', 'now'), strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
			 ('00000000-0000-4000-8100-000000000003', '00000000-0000-4000-8000-000000000002', 'in_progress', 'In Progress', 'started', '#f5a623', 1, 2, strftime('%Y-%m-%dT%H:%M:%fZ', 'now'), strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
			 ('00000000-0000-4000-8100-000000000004', '00000000-0000-4000-8000-000000000002', 'done', 'Done', 'completed', '#5eaa5f', 1, 3, strftime('%Y-%m-%dT%H:%M:%fZ', 'now'), strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
			 ('00000000-0000-4000-8100-000000000005', '00000000-0000-4000-8000-000000000002', 'canceled', 'Canceled', 'canceled', '#8b8b8b', 1, 4, strftime('%Y-%m-%dT%H:%M:%fZ', 'now'), strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))`,
			`CREATE TABLE issues (
				id TEXT PRIMARY KEY, workspace_id TEXT NOT NULL, number INTEGER NOT NULL,
				title TEXT NOT NULL, description TEXT, status TEXT NOT NULL DEFAULT 'backlog',
				priority TEXT NOT NULL DEFAULT 'none', assignee_type TEXT, assignee_id TEXT,
				creator_type TEXT NOT NULL DEFAULT 'system', creator_id TEXT NOT NULL,
				parent_issue_id TEXT, project_id TEXT NOT NULL, position REAL NOT NULL DEFAULT 0,
				stage INTEGER, start_date TEXT, due_date TEXT, metadata TEXT NOT NULL DEFAULT '{}',
				properties TEXT NOT NULL DEFAULT '{}', revision INTEGER NOT NULL DEFAULT 1,
				last_activity_at TEXT, created_at TEXT NOT NULL, updated_at TEXT NOT NULL,
				UNIQUE(workspace_id, number)
			)`,
			`CREATE INDEX IF NOT EXISTS issues_workspace_idx ON issues(workspace_id, updated_at DESC)`,
			`CREATE INDEX IF NOT EXISTS issues_project_idx ON issues(workspace_id, project_id, updated_at DESC)`,
			`CREATE TABLE comments (
				id TEXT PRIMARY KEY, issue_id TEXT NOT NULL, author_type TEXT NOT NULL,
				author_id TEXT NOT NULL, content TEXT NOT NULL, type TEXT NOT NULL DEFAULT 'comment',
				parent_id TEXT, revision INTEGER NOT NULL DEFAULT 1, issue_revision INTEGER NOT NULL DEFAULT 1,
				resolved_at TEXT, resolved_by_type TEXT, resolved_by_id TEXT, source_task_id TEXT,
				quick_action_id TEXT, created_at TEXT NOT NULL, updated_at TEXT NOT NULL
			)`,
			`CREATE INDEX IF NOT EXISTS comments_issue_idx ON comments(issue_id, created_at)`,
			`CREATE TABLE activities (
				id TEXT PRIMARY KEY, issue_id TEXT NOT NULL, actor_type TEXT NOT NULL,
				actor_id TEXT NOT NULL, action TEXT NOT NULL, details TEXT NOT NULL DEFAULT '{}',
				created_at TEXT NOT NULL
			)`,
			`CREATE INDEX IF NOT EXISTS activities_issue_idx ON activities(issue_id, created_at)`,
			`CREATE TABLE task_runs (
				id TEXT PRIMARY KEY, issue_id TEXT NOT NULL, status TEXT NOT NULL, thread_id TEXT,
				runtime TEXT, handoff_note TEXT, session_id TEXT, stage_id TEXT, work_dir TEXT,
				prior_task_id TEXT, trigger_comment_id TEXT, request_key TEXT,
				continuity_state TEXT NOT NULL DEFAULT 'fresh', continuity_reason TEXT,
				summary TEXT, error TEXT, started_at TEXT, completed_at TEXT,
				created_at TEXT NOT NULL, updated_at TEXT NOT NULL
			)`,
			`CREATE INDEX IF NOT EXISTS task_runs_issue_idx ON task_runs(issue_id, created_at DESC)`,
			`CREATE UNIQUE INDEX IF NOT EXISTS task_runs_request_key_idx
			 ON task_runs(issue_id, request_key) WHERE request_key IS NOT NULL`,
			`CREATE TABLE task_messages (
				task_id TEXT NOT NULL, issue_id TEXT NOT NULL, seq INTEGER NOT NULL,
				type TEXT NOT NULL, tool TEXT, content TEXT, input TEXT, output TEXT,
				created_at TEXT NOT NULL, PRIMARY KEY(task_id, seq)
			)`,
			`CREATE INDEX IF NOT EXISTS task_messages_issue_idx ON task_messages(issue_id, task_id, seq)`,
			`CREATE TABLE task_run_usage (
				task_id TEXT NOT NULL, provider TEXT NOT NULL, model TEXT NOT NULL,
				input_tokens INTEGER NOT NULL CHECK(input_tokens >= 0),
				output_tokens INTEGER NOT NULL CHECK(output_tokens >= 0),
				cache_read_tokens INTEGER NOT NULL CHECK(cache_read_tokens >= 0),
				cache_write_tokens INTEGER NOT NULL CHECK(cache_write_tokens >= 0),
				cost_usd_ticks INTEGER CHECK(cost_usd_ticks IS NULL OR cost_usd_ticks >= 0),
				updated_at TEXT NOT NULL, PRIMARY KEY(task_id, provider, model)
			)`,
			`CREATE TABLE issue_sessions (
				id TEXT PRIMARY KEY, issue_id TEXT NOT NULL UNIQUE, workspace_id TEXT NOT NULL,
				active_stage_id TEXT NOT NULL, continuity_state TEXT NOT NULL DEFAULT 'fresh',
				continuity_reason TEXT, created_at TEXT NOT NULL, updated_at TEXT NOT NULL
			)`,
			`CREATE INDEX IF NOT EXISTS issue_sessions_workspace_idx ON issue_sessions(workspace_id, updated_at DESC)`,
			`CREATE TABLE issue_session_stages (
				id TEXT PRIMARY KEY, session_id TEXT NOT NULL, kind TEXT NOT NULL, status TEXT NOT NULL,
				position INTEGER NOT NULL, migrated INTEGER NOT NULL DEFAULT 0,
				started_at TEXT NOT NULL, completed_at TEXT
			)`,
			`CREATE INDEX IF NOT EXISTS issue_session_stages_session_idx ON issue_session_stages(session_id, position)`,
			`CREATE TABLE issue_handoffs (
				id TEXT PRIMARY KEY, child_issue_id TEXT NOT NULL UNIQUE, parent_issue_id TEXT NOT NULL,
				parent_session_id TEXT NOT NULL, source_stage_id TEXT NOT NULL, source_run_id TEXT,
				objective TEXT NOT NULL, confirmed_facts TEXT NOT NULL DEFAULT '[]',
				decisions TEXT NOT NULL DEFAULT '[]', constraints TEXT NOT NULL DEFAULT '[]',
				acceptance_criteria TEXT NOT NULL DEFAULT '[]', references_json TEXT NOT NULL DEFAULT '[]',
				open_questions TEXT NOT NULL DEFAULT '[]', created_at TEXT NOT NULL
			)`,
			`CREATE INDEX IF NOT EXISTS issue_handoffs_parent_idx ON issue_handoffs(parent_issue_id, created_at)`,
			`CREATE TABLE attachments (
				id TEXT PRIMARY KEY, workspace_id TEXT NOT NULL, issue_id TEXT, comment_id TEXT,
				filename TEXT NOT NULL, content_type TEXT NOT NULL, size_bytes INTEGER NOT NULL,
				data BLOB NOT NULL, created_at TEXT NOT NULL
			)`,
			`CREATE INDEX IF NOT EXISTS attachments_issue_idx ON attachments(workspace_id, issue_id, created_at)`,
			`CREATE INDEX IF NOT EXISTS attachments_comment_idx ON attachments(comment_id, created_at)`,
			`CREATE TABLE runtimes (
				id TEXT PRIMARY KEY, workspace_id TEXT NOT NULL, daemon_id TEXT NOT NULL,
				name TEXT NOT NULL, provider TEXT NOT NULL, version TEXT, status TEXT NOT NULL DEFAULT 'online',
				device_name TEXT, profile_id TEXT, last_seen_at TEXT, created_at TEXT NOT NULL,
				updated_at TEXT NOT NULL, UNIQUE(workspace_id, daemon_id, provider, profile_id)
			)`,
			`CREATE INDEX IF NOT EXISTS runtimes_workspace_idx ON runtimes(workspace_id, status, provider)`,
		},
	},
	{
		version: 5,
		name:    "agents skills squads automations and integrations",
		statements: []string{
			`CREATE TABLE agents (
				id TEXT PRIMARY KEY, workspace_id TEXT NOT NULL, runtime_id TEXT NOT NULL,
				name TEXT NOT NULL, data TEXT NOT NULL, archived_at TEXT,
				created_at TEXT NOT NULL, updated_at TEXT NOT NULL
			)`,
			`CREATE INDEX IF NOT EXISTS agents_workspace_idx
			 ON agents(workspace_id, archived_at, updated_at DESC, id)`,
			`CREATE TABLE skills (
				id TEXT PRIMARY KEY, workspace_id TEXT NOT NULL, name TEXT NOT NULL,
				data TEXT NOT NULL, created_at TEXT NOT NULL, updated_at TEXT NOT NULL
			)`,
			`CREATE INDEX IF NOT EXISTS skills_workspace_idx
			 ON skills(workspace_id, updated_at DESC, id)`,
			`CREATE TABLE agent_skills (
				agent_id TEXT NOT NULL, skill_id TEXT NOT NULL, enabled INTEGER NOT NULL DEFAULT 1,
				created_at TEXT NOT NULL, PRIMARY KEY(agent_id, skill_id)
			)`,
			`CREATE TABLE squads (
				id TEXT PRIMARY KEY, workspace_id TEXT NOT NULL, name TEXT NOT NULL,
				leader_id TEXT NOT NULL, creator_id TEXT NOT NULL, data TEXT NOT NULL,
				archived_at TEXT, created_at TEXT NOT NULL, updated_at TEXT NOT NULL
			)`,
			`CREATE INDEX IF NOT EXISTS squads_workspace_idx
			 ON squads(workspace_id, archived_at, updated_at DESC, id)`,
			`CREATE TABLE squad_members (
				id TEXT PRIMARY KEY, squad_id TEXT NOT NULL, member_type TEXT NOT NULL,
				member_id TEXT NOT NULL, role TEXT NOT NULL DEFAULT '', created_at TEXT NOT NULL,
				UNIQUE(squad_id, member_type, member_id)
			)`,
			`CREATE INDEX IF NOT EXISTS squad_members_squad_idx
			 ON squad_members(squad_id, created_at, id)`,
			`CREATE TABLE automations (
				id TEXT PRIMARY KEY, workspace_id TEXT NOT NULL, title TEXT NOT NULL,
				status TEXT NOT NULL, assignee_type TEXT NOT NULL, assignee_id TEXT NOT NULL,
				data TEXT NOT NULL, created_at TEXT NOT NULL, updated_at TEXT NOT NULL
			)`,
			`CREATE INDEX IF NOT EXISTS automations_workspace_idx
			 ON automations(workspace_id, status, updated_at DESC, id)`,
			`CREATE TABLE automation_triggers (
				id TEXT PRIMARY KEY, automation_id TEXT NOT NULL, kind TEXT NOT NULL,
				enabled INTEGER NOT NULL DEFAULT 1, occurrence_key TEXT,
				data TEXT NOT NULL, created_at TEXT NOT NULL, updated_at TEXT NOT NULL
			)`,
			`CREATE UNIQUE INDEX IF NOT EXISTS automation_occurrence_idx
			 ON automation_triggers(automation_id, occurrence_key) WHERE occurrence_key IS NOT NULL`,
			`CREATE TABLE automation_runs (
				id TEXT PRIMARY KEY, automation_id TEXT NOT NULL, trigger_id TEXT,
				status TEXT NOT NULL, source TEXT NOT NULL, issue_id TEXT, task_id TEXT,
				data TEXT NOT NULL, triggered_at TEXT NOT NULL, completed_at TEXT
			)`,
			`CREATE INDEX IF NOT EXISTS automation_runs_idx
			 ON automation_runs(automation_id, triggered_at DESC, id)`,
			`CREATE TABLE integration_settings (
				kind TEXT NOT NULL, key TEXT NOT NULL, value TEXT NOT NULL,
				updated_at TEXT NOT NULL, PRIMARY KEY(kind, key)
			)`,
			`CREATE TABLE inbound_deliveries (
				integration TEXT NOT NULL, delivery_id TEXT NOT NULL, payload TEXT NOT NULL,
				status TEXT NOT NULL, error TEXT, received_at TEXT NOT NULL,
				PRIMARY KEY(integration, delivery_id)
			)`,
			`CREATE INDEX IF NOT EXISTS inbound_deliveries_status_idx
			 ON inbound_deliveries(integration, status, received_at DESC)`,
		},
	},
	{
		version: 6,
		name:    "local attachment file identities",
		statements: []string{
			`ALTER TABLE attachments ADD COLUMN storage_path TEXT`,
			`CREATE UNIQUE INDEX IF NOT EXISTS attachments_storage_path_idx
			 ON attachments(storage_path) WHERE storage_path IS NOT NULL`,
		},
	},
	{
		version: 7,
		name:    "local chat sessions and tasks",
		statements: []string{
			`CREATE TABLE chat_sessions (
				id TEXT PRIMARY KEY, workspace_id TEXT NOT NULL, agent_id TEXT NOT NULL,
				creator_id TEXT NOT NULL, project_id TEXT, title TEXT NOT NULL,
				status TEXT NOT NULL DEFAULT 'active', pinned INTEGER NOT NULL DEFAULT 0,
				read_at TEXT, created_at TEXT NOT NULL, updated_at TEXT NOT NULL
			)`,
			`CREATE INDEX IF NOT EXISTS chat_sessions_workspace_idx
			 ON chat_sessions(workspace_id, status, pinned DESC, updated_at DESC, id)`,
			`CREATE TABLE chat_messages (
				id TEXT PRIMARY KEY, chat_session_id TEXT NOT NULL, role TEXT NOT NULL,
				content TEXT NOT NULL, task_id TEXT, failure_reason TEXT, elapsed_ms INTEGER,
				message_kind TEXT NOT NULL DEFAULT 'message', created_at TEXT NOT NULL
			)`,
			`CREATE INDEX IF NOT EXISTS chat_messages_session_idx
			 ON chat_messages(chat_session_id, created_at, id)`,
			`CREATE TABLE chat_tasks (
				id TEXT PRIMARY KEY, chat_session_id TEXT NOT NULL, status TEXT NOT NULL,
				runtime_id TEXT NOT NULL, agent_id TEXT NOT NULL, thread_id TEXT,
				work_dir TEXT, summary TEXT, error TEXT, created_at TEXT NOT NULL,
				started_at TEXT, completed_at TEXT, updated_at TEXT NOT NULL
			)`,
			`CREATE INDEX IF NOT EXISTS chat_tasks_claim_idx
			 ON chat_tasks(runtime_id, status, created_at, id)`,
			`CREATE TABLE chat_pinned_agents (
				agent_id TEXT PRIMARY KEY, workspace_id TEXT NOT NULL,
				created_at TEXT NOT NULL
			)`,
			`ALTER TABLE attachments ADD COLUMN chat_session_id TEXT`,
			`ALTER TABLE attachments ADD COLUMN chat_message_id TEXT`,
			`CREATE INDEX IF NOT EXISTS attachments_chat_message_idx
			 ON attachments(chat_message_id, created_at, id)`,
		},
	},
	{
		version: 8,
		name:    "retained workspace customization and activity",
		statements: []string{
			`UPDATE issue_statuses SET key='cancelled', name='Cancelled', category='cancelled', updated_at=strftime('%Y-%m-%dT%H:%M:%fZ', 'now') WHERE workspace_id='00000000-0000-4000-8000-000000000002' AND key='canceled'`,
			`UPDATE issue_statuses SET category=key, updated_at=strftime('%Y-%m-%dT%H:%M:%fZ', 'now') WHERE workspace_id='00000000-0000-4000-8000-000000000002' AND key IN ('backlog','todo','in_progress','done','cancelled')`,
			`INSERT OR IGNORE INTO issue_statuses(id, workspace_id, key, name, category, color, is_system, position, created_at, updated_at) VALUES
				('00000000-0000-4000-8100-000000000006', '00000000-0000-4000-8000-000000000002', 'in_review', 'In Review', 'in_review', '#8b5cf6', 1, 3, strftime('%Y-%m-%dT%H:%M:%fZ', 'now'), strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
				('00000000-0000-4000-8100-000000000007', '00000000-0000-4000-8000-000000000002', 'blocked', 'Blocked', 'blocked', '#ef4444', 1, 5, strftime('%Y-%m-%dT%H:%M:%fZ', 'now'), strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))`,
			`UPDATE issue_statuses SET position=CASE key WHEN 'backlog' THEN 0 WHEN 'todo' THEN 1 WHEN 'in_progress' THEN 2 WHEN 'in_review' THEN 3 WHEN 'done' THEN 4 WHEN 'blocked' THEN 5 WHEN 'cancelled' THEN 6 ELSE position END WHERE workspace_id='00000000-0000-4000-8000-000000000002'`,
			`CREATE TABLE inbox_items (
				id TEXT PRIMARY KEY, workspace_id TEXT NOT NULL, recipient_type TEXT NOT NULL,
				recipient_id TEXT NOT NULL, type TEXT NOT NULL, severity TEXT NOT NULL,
				issue_id TEXT, title TEXT NOT NULL, body TEXT, read INTEGER NOT NULL DEFAULT 0,
				archived INTEGER NOT NULL DEFAULT 0, created_at TEXT NOT NULL
			)`,
			`CREATE INDEX IF NOT EXISTS inbox_items_workspace_idx ON inbox_items(workspace_id, archived, read, created_at DESC, id)`,
			`CREATE TABLE local_preferences (
				workspace_id TEXT NOT NULL, owner_id TEXT NOT NULL, kind TEXT NOT NULL,
				data TEXT NOT NULL, updated_at TEXT NOT NULL,
				PRIMARY KEY(workspace_id, owner_id, kind)
			)`,
			`CREATE TABLE labels (
				id TEXT PRIMARY KEY, workspace_id TEXT NOT NULL, resource_type TEXT NOT NULL,
				name TEXT NOT NULL, description TEXT NOT NULL DEFAULT '', color TEXT NOT NULL,
				created_at TEXT NOT NULL, updated_at TEXT NOT NULL,
				UNIQUE(workspace_id, resource_type, name)
			)`,
			`CREATE INDEX IF NOT EXISTS labels_workspace_idx ON labels(workspace_id, resource_type, name, id)`,
			`CREATE TABLE resource_labels (
				workspace_id TEXT NOT NULL, resource_type TEXT NOT NULL, resource_id TEXT NOT NULL,
				label_id TEXT NOT NULL, created_at TEXT NOT NULL,
				PRIMARY KEY(resource_type, resource_id, label_id)
			)`,
			`CREATE INDEX IF NOT EXISTS resource_labels_label_idx ON resource_labels(label_id, resource_type, resource_id)`,
			`CREATE TABLE issue_properties (
				id TEXT PRIMARY KEY, workspace_id TEXT NOT NULL, name TEXT NOT NULL, type TEXT NOT NULL,
				description TEXT NOT NULL DEFAULT '', icon TEXT NOT NULL DEFAULT '', config TEXT NOT NULL DEFAULT '{}',
				position INTEGER NOT NULL DEFAULT 0, archived_at TEXT, created_at TEXT NOT NULL, updated_at TEXT NOT NULL
			)`,
			`CREATE INDEX IF NOT EXISTS issue_properties_workspace_idx ON issue_properties(workspace_id, archived_at, position, id)`,
			`CREATE TABLE issue_views (
				id TEXT PRIMARY KEY, workspace_id TEXT NOT NULL, owner_id TEXT NOT NULL, name TEXT NOT NULL,
				scope_type TEXT NOT NULL, scope_id TEXT, scope_variant TEXT, visibility TEXT NOT NULL,
				definition_version INTEGER NOT NULL DEFAULT 1, query TEXT NOT NULL DEFAULT '{}',
				display TEXT NOT NULL DEFAULT '{}', revision INTEGER NOT NULL DEFAULT 1,
				created_at TEXT NOT NULL, updated_at TEXT NOT NULL
			)`,
			`CREATE INDEX IF NOT EXISTS issue_views_workspace_idx ON issue_views(workspace_id, owner_id, scope_type, updated_at DESC, id)`,
			`CREATE TABLE pins (
				workspace_id TEXT NOT NULL, owner_id TEXT NOT NULL, item_type TEXT NOT NULL,
				item_id TEXT NOT NULL, position INTEGER NOT NULL, created_at TEXT NOT NULL,
				PRIMARY KEY(workspace_id, owner_id, item_type, item_id)
			)`,
			`CREATE INDEX IF NOT EXISTS pins_order_idx ON pins(workspace_id, owner_id, position, item_type, item_id)`,
			`CREATE TABLE quick_actions (
				id TEXT PRIMARY KEY, workspace_id TEXT NOT NULL, name TEXT NOT NULL,
				assignee_type TEXT NOT NULL, assignee_id TEXT NOT NULL, data TEXT NOT NULL,
				status TEXT NOT NULL DEFAULT 'active', use_count INTEGER NOT NULL DEFAULT 0,
				last_used_at TEXT, created_at TEXT NOT NULL, updated_at TEXT NOT NULL
			)`,
			`CREATE INDEX IF NOT EXISTS quick_actions_workspace_idx ON quick_actions(workspace_id, status, updated_at DESC, id)`,
			`CREATE TABLE reactions (
				workspace_id TEXT NOT NULL, resource_type TEXT NOT NULL, resource_id TEXT NOT NULL,
				actor_type TEXT NOT NULL, actor_id TEXT NOT NULL, emoji TEXT NOT NULL, created_at TEXT NOT NULL,
				PRIMARY KEY(resource_type, resource_id, actor_type, actor_id, emoji)
			)`,
			`CREATE TABLE issue_subscribers (
				issue_id TEXT NOT NULL, user_type TEXT NOT NULL, user_id TEXT NOT NULL,
				reason TEXT NOT NULL, created_at TEXT NOT NULL,
				PRIMARY KEY(issue_id, user_type, user_id)
			)`,
			`CREATE TABLE agent_builder_sessions (
				id TEXT PRIMARY KEY, workspace_id TEXT NOT NULL, runtime_id TEXT NOT NULL,
				chat_session_id TEXT NOT NULL, draft TEXT NOT NULL DEFAULT '{}', status TEXT NOT NULL DEFAULT 'active',
				created_at TEXT NOT NULL, updated_at TEXT NOT NULL
			)`,
			`CREATE INDEX IF NOT EXISTS agent_builder_workspace_idx ON agent_builder_sessions(workspace_id, status, updated_at DESC, id)`,
			`CREATE TABLE automation_occurrences (
				trigger_id TEXT NOT NULL, occurrence_key TEXT NOT NULL, run_id TEXT,
				status TEXT NOT NULL, error TEXT, created_at TEXT NOT NULL, updated_at TEXT NOT NULL,
				PRIMARY KEY(trigger_id, occurrence_key)
			)`,
		},
	},
	{
		version: 9,
		name:    "chat queue ordering",
		statements: []string{
			`ALTER TABLE chat_tasks ADD COLUMN priority INTEGER NOT NULL DEFAULT 0`,
			`CREATE INDEX IF NOT EXISTS chat_tasks_priority_idx ON chat_tasks(runtime_id, status, priority DESC, created_at, id)`,
		},
	},
	{
		version: 10,
		name:    "runtime profiles and daemon requests",
		statements: []string{
			`CREATE TABLE runtime_profiles (
				id TEXT PRIMARY KEY, workspace_id TEXT NOT NULL, display_name TEXT NOT NULL,
				protocol_family TEXT NOT NULL, command_name TEXT NOT NULL, description TEXT,
				fixed_args TEXT NOT NULL DEFAULT '[]', visibility TEXT NOT NULL DEFAULT 'private',
				created_by TEXT, enabled INTEGER NOT NULL DEFAULT 1, created_at TEXT NOT NULL, updated_at TEXT NOT NULL
			)`,
			`CREATE INDEX IF NOT EXISTS runtime_profiles_workspace_idx ON runtime_profiles(workspace_id, enabled, display_name, id)`,
			`CREATE TABLE runtime_requests (
				id TEXT PRIMARY KEY, workspace_id TEXT NOT NULL, runtime_id TEXT NOT NULL,
				kind TEXT NOT NULL, status TEXT NOT NULL, payload TEXT NOT NULL DEFAULT '{}',
				result TEXT NOT NULL DEFAULT '{}', error TEXT, created_at TEXT NOT NULL, updated_at TEXT NOT NULL
			)`,
			`CREATE INDEX IF NOT EXISTS runtime_requests_claim_idx ON runtime_requests(runtime_id, kind, status, created_at, id)`,
		},
	},
	{
		version: 11,
		name:    "workspace mcp library",
		statements: []string{
			`CREATE TABLE mcp_servers (
				id TEXT PRIMARY KEY, workspace_id TEXT NOT NULL, name TEXT NOT NULL,
				transport TEXT NOT NULL, config TEXT NOT NULL, created_at TEXT NOT NULL, updated_at TEXT NOT NULL,
				UNIQUE(workspace_id, name)
			)`,
			`CREATE INDEX IF NOT EXISTS mcp_servers_workspace_idx ON mcp_servers(workspace_id, name, id)`,
			`CREATE TABLE agent_mcp_servers (
				agent_id TEXT NOT NULL, server_id TEXT NOT NULL, enabled INTEGER NOT NULL DEFAULT 1,
				created_at TEXT NOT NULL, updated_at TEXT NOT NULL, PRIMARY KEY(agent_id, server_id)
			)`,
			`CREATE INDEX IF NOT EXISTS agent_mcp_servers_server_idx ON agent_mcp_servers(server_id, agent_id)`,
		},
	},
	{
		version: 12,
		name:    "durable automation webhook deliveries",
		statements: []string{
			`CREATE TABLE automation_deliveries (
				id TEXT PRIMARY KEY, workspace_id TEXT NOT NULL, automation_id TEXT NOT NULL,
				trigger_id TEXT NOT NULL, provider TEXT NOT NULL, event TEXT NOT NULL,
				dedupe_key TEXT, dedupe_source TEXT, signature_status TEXT NOT NULL,
				status TEXT NOT NULL, attempt_count INTEGER NOT NULL DEFAULT 1,
				dispatch_attempts INTEGER NOT NULL DEFAULT 0, available_at TEXT NOT NULL,
				content_type TEXT, selected_headers TEXT NOT NULL DEFAULT '{}', raw_body BLOB,
				response_status INTEGER, response_body TEXT, automation_run_id TEXT,
				replayed_from_delivery_id TEXT, replay_idempotency_key TEXT,
				error TEXT, reason_code TEXT, received_at TEXT NOT NULL,
				last_attempt_at TEXT NOT NULL, created_at TEXT NOT NULL
			)`,
			`CREATE INDEX IF NOT EXISTS automation_deliveries_list_idx ON automation_deliveries(automation_id, received_at DESC, id)`,
			`CREATE UNIQUE INDEX IF NOT EXISTS automation_deliveries_dedupe_idx ON automation_deliveries(trigger_id, dedupe_key) WHERE dedupe_key IS NOT NULL`,
			`CREATE UNIQUE INDEX IF NOT EXISTS automation_deliveries_replay_idx ON automation_deliveries(replayed_from_delivery_id, replay_idempotency_key) WHERE replay_idempotency_key IS NOT NULL`,
		},
	},
	{
		version: 13,
		name:    "write only webhook signing secrets",
		statements: []string{
			`ALTER TABLE automation_triggers ADD COLUMN signing_secret TEXT`,
		},
	},
	{
		version: 14,
		name:    "local integrations and plugins",
		statements: []string{
			`CREATE TABLE local_integrations (
				id TEXT PRIMARY KEY, workspace_id TEXT NOT NULL, kind TEXT NOT NULL,
				agent_id TEXT, display_name TEXT NOT NULL, public_data TEXT NOT NULL DEFAULT '{}',
				secret_data TEXT NOT NULL DEFAULT '{}', status TEXT NOT NULL DEFAULT 'connected',
				created_at TEXT NOT NULL, updated_at TEXT NOT NULL
			)`,
			`CREATE INDEX IF NOT EXISTS local_integrations_workspace_idx ON local_integrations(workspace_id, kind, updated_at DESC, id)`,
			`CREATE TABLE plugin_packages (
				id TEXT PRIMARY KEY, workspace_id TEXT NOT NULL, plugin_key TEXT NOT NULL,
				name TEXT NOT NULL, data TEXT NOT NULL, bundle_path TEXT,
				created_at TEXT NOT NULL, updated_at TEXT NOT NULL,
				UNIQUE(workspace_id, plugin_key)
			)`,
			`CREATE INDEX IF NOT EXISTS plugin_packages_workspace_idx ON plugin_packages(workspace_id, updated_at DESC, id)`,
			`CREATE TABLE plugin_installations (
				id TEXT PRIMARY KEY, workspace_id TEXT NOT NULL, package_id TEXT NOT NULL,
				plugin_key TEXT NOT NULL, enabled INTEGER NOT NULL DEFAULT 0,
				data TEXT NOT NULL, secret_config TEXT NOT NULL DEFAULT '{}',
				created_at TEXT NOT NULL, updated_at TEXT NOT NULL,
				UNIQUE(workspace_id, plugin_key)
			)`,
			`CREATE INDEX IF NOT EXISTS plugin_installations_workspace_idx ON plugin_installations(workspace_id, enabled, updated_at DESC, id)`,
			`CREATE TABLE plugin_invocations (
				id TEXT PRIMARY KEY, installation_id TEXT NOT NULL, hook_key TEXT NOT NULL,
				trigger_kind TEXT NOT NULL, status TEXT NOT NULL, data TEXT NOT NULL,
				created_at TEXT NOT NULL
			)`,
			`CREATE INDEX IF NOT EXISTS plugin_invocations_install_idx ON plugin_invocations(installation_id, created_at DESC, id)`,
		},
	},
	{
		version: 15,
		name:    "plugin scoped storage",
		statements: []string{
			`CREATE TABLE plugin_storage (
				installation_id TEXT NOT NULL, scope TEXT NOT NULL, owner_id TEXT NOT NULL,
				key TEXT NOT NULL, value TEXT NOT NULL, updated_at TEXT NOT NULL,
				PRIMARY KEY(installation_id, scope, owner_id, key)
			)`,
			`CREATE INDEX IF NOT EXISTS plugin_storage_install_idx ON plugin_storage(installation_id, scope, owner_id, updated_at DESC, key)`,
		},
	},
	{
		version: 16,
		name:    "immutable plugin package versions",
		statements: []string{
			`CREATE TABLE plugin_package_versions (
				id TEXT PRIMARY KEY, package_id TEXT NOT NULL, version TEXT NOT NULL,
				digest TEXT NOT NULL, bundle_path TEXT NOT NULL, data TEXT NOT NULL,
				created_at TEXT NOT NULL, UNIQUE(package_id, version)
			)`,
			`CREATE INDEX IF NOT EXISTS plugin_package_versions_package_idx ON plugin_package_versions(package_id, created_at DESC, id)`,
			`ALTER TABLE plugin_installations ADD COLUMN package_version_id TEXT`,
		},
	},
	{
		version: 17,
		name:    "durable issue source context",
		statements: []string{
			`CREATE TABLE issue_source_contexts (
				id TEXT PRIMARY KEY, issue_id TEXT NOT NULL UNIQUE, source_issue_id TEXT NOT NULL,
				anchor_comment_id TEXT NOT NULL, capture_token TEXT NOT NULL, snapshot TEXT NOT NULL,
				state TEXT NOT NULL DEFAULT 'attached', created_at TEXT NOT NULL, updated_at TEXT NOT NULL
			)`,
			`CREATE INDEX IF NOT EXISTS issue_source_contexts_source_idx ON issue_source_contexts(source_issue_id, created_at DESC, id)`,
		},
	},
	{
		version: 18,
		name:    "durable local integration deliveries",
		statements: []string{
			`CREATE TABLE local_integration_deliveries (
				id TEXT PRIMARY KEY, integration_id TEXT NOT NULL, event TEXT NOT NULL,
				dedupe_key TEXT, signature_status TEXT NOT NULL, status TEXT NOT NULL,
				raw_body BLOB NOT NULL, error TEXT, created_at TEXT NOT NULL
			)`,
			`CREATE UNIQUE INDEX IF NOT EXISTS local_integration_deliveries_dedupe_idx ON local_integration_deliveries(integration_id,dedupe_key) WHERE dedupe_key IS NOT NULL`,
			`CREATE INDEX IF NOT EXISTS local_integration_deliveries_list_idx ON local_integration_deliveries(integration_id,created_at DESC,id)`,
		},
	},
	{
		version: 19,
		name:    "durable issue run targets",
		statements: []string{
			`ALTER TABLE task_runs ADD COLUMN agent_id TEXT`,
			`ALTER TABLE task_runs ADD COLUMN squad_id TEXT`,
			`UPDATE task_runs SET agent_id=CASE
				WHEN (SELECT assignee_type FROM issues WHERE issues.id=task_runs.issue_id)='squad'
				THEN (SELECT leader_id FROM squads WHERE id=(SELECT assignee_id FROM issues WHERE issues.id=task_runs.issue_id))
				ELSE (SELECT assignee_id FROM issues WHERE issues.id=task_runs.issue_id) END`,
			`UPDATE task_runs SET squad_id=(SELECT assignee_id FROM issues WHERE issues.id=task_runs.issue_id)
				WHERE (SELECT assignee_type FROM issues WHERE issues.id=task_runs.issue_id)='squad'`,
			`CREATE INDEX IF NOT EXISTS task_runs_agent_status_idx ON task_runs(agent_id,status,created_at,id)`,
		},
	},
	{
		version: 20,
		name:    "local vcs pull request mirror",
		statements: []string{
			`CREATE TABLE vcs_pull_requests (
				id TEXT PRIMARY KEY, workspace_id TEXT NOT NULL, connection_id TEXT NOT NULL,
				provider TEXT NOT NULL, repo_owner TEXT NOT NULL, repo_name TEXT NOT NULL,
				pr_number INTEGER NOT NULL, title TEXT NOT NULL, state TEXT NOT NULL,
				html_url TEXT NOT NULL, branch TEXT, head_sha TEXT, author_login TEXT,
				author_avatar_url TEXT, merged_at TEXT, closed_at TEXT,
				pr_created_at TEXT NOT NULL, pr_updated_at TEXT NOT NULL,
				additions INTEGER NOT NULL DEFAULT 0, deletions INTEGER NOT NULL DEFAULT 0,
				changed_files INTEGER NOT NULL DEFAULT 0,
				UNIQUE(connection_id,repo_owner,repo_name,pr_number)
			)`,
			`CREATE INDEX IF NOT EXISTS vcs_pull_requests_workspace_idx ON vcs_pull_requests(workspace_id,pr_updated_at DESC,id)`,
			`CREATE TABLE issue_vcs_pull_requests (
				issue_id TEXT NOT NULL, pull_request_id TEXT NOT NULL, close_intent INTEGER NOT NULL DEFAULT 0,
				reference_only INTEGER NOT NULL DEFAULT 0, created_at TEXT NOT NULL,
				PRIMARY KEY(issue_id,pull_request_id)
			)`,
			`CREATE INDEX IF NOT EXISTS issue_vcs_pull_requests_pr_idx ON issue_vcs_pull_requests(pull_request_id,issue_id)`,
			`CREATE TABLE vcs_commit_statuses (
				connection_id TEXT NOT NULL, sha TEXT NOT NULL, context TEXT NOT NULL,
				state TEXT NOT NULL, target_url TEXT, description TEXT, updated_at TEXT NOT NULL,
				PRIMARY KEY(connection_id,sha,context)
			)`,
		},
	},
	{
		version: 21,
		name:    "durable chat draft restores",
		statements: []string{
			`CREATE TABLE chat_draft_restores (
				id TEXT PRIMARY KEY, chat_session_id TEXT NOT NULL, task_id TEXT NOT NULL,
				content TEXT NOT NULL, attachment_ids TEXT NOT NULL DEFAULT '[]',
				created_at TEXT NOT NULL
			)`,
			`CREATE INDEX IF NOT EXISTS chat_draft_restores_session_idx
			 ON chat_draft_restores(chat_session_id, created_at, id)`,
		},
	},
}

func Open(ctx context.Context, path string) (*Store, error) {
	if path == "" {
		return nil, errors.New("sqlite path is empty")
	}
	absolutePath, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve sqlite path: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(absolutePath), 0o700); err != nil {
		return nil, fmt.Errorf("create sqlite directory: %w", err)
	}

	db, err := sql.Open("sqlite", absolutePath)
	if err != nil {
		return nil, fmt.Errorf("open sqlite database: %w", err)
	}
	// One connection keeps connection-local PRAGMAs deterministic and serializes
	// local writes. Claim operations still use explicit transactions.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(0)
	if err := configureSQLite(ctx, db); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := os.Chmod(absolutePath, 0o600); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("protect sqlite database: %w", err)
	}
	if err := applyMigrations(ctx, db, schemaMigrations); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}

func configureSQLite(ctx context.Context, db *sql.DB) error {
	for _, statement := range []string{
		"PRAGMA journal_mode = WAL",
		"PRAGMA synchronous = FULL",
		"PRAGMA foreign_keys = OFF",
		"PRAGMA busy_timeout = 5000",
	} {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("configure sqlite with %q: %w", statement, err)
		}
	}
	return nil
}

func applyMigrations(ctx context.Context, db *sql.DB, migrations []migration) error {
	if err := ensureMigrationLedger(ctx, db); err != nil {
		return err
	}
	for _, item := range migrations {
		applied, err := migrationApplied(ctx, db, item.version)
		if err != nil {
			return err
		}
		if applied {
			continue
		}
		if err := applyMigration(ctx, db, item); err != nil {
			return err
		}
	}
	return nil
}

func ensureMigrationLedger(ctx context.Context, db *sql.DB) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin migration ledger transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version INTEGER PRIMARY KEY,
		name TEXT NOT NULL,
		applied_at TEXT NOT NULL
	)`); err != nil {
		return fmt.Errorf("create migration ledger: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit migration ledger: %w", err)
	}
	return nil
}

func migrationApplied(ctx context.Context, db *sql.DB, version int) (bool, error) {
	var exists int
	err := db.QueryRowContext(ctx,
		"SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE version = ?)",
		version,
	).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("read migration version %d: %w", version, err)
	}
	return exists == 1, nil
}

func applyMigration(ctx context.Context, db *sql.DB, item migration) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin migration %d: %w", item.version, err)
	}
	defer func() { _ = tx.Rollback() }()
	for index, statement := range item.statements {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("apply migration %d statement %d: %w", item.version, index+1, err)
		}
	}
	if _, err := tx.ExecContext(ctx,
		"INSERT INTO schema_migrations(version, name, applied_at) VALUES (?, ?, ?)",
		item.version, item.name, time.Now().UTC().Format(time.RFC3339Nano),
	); err != nil {
		return fmt.Errorf("record migration %d: %w", item.version, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit migration %d: %w", item.version, err)
	}
	return nil
}

func (s *Store) Close() error {
	return s.db.Close()
}

func (s *Store) Identity(ctx context.Context) (IdentityContext, error) {
	var identity IdentityContext
	err := s.db.QueryRowContext(ctx, `SELECT
		w.id, w.name, w.slug, w.description, w.context, w.settings, w.repos,
		w.issue_prefix, w.avatar_url, w.created_at, w.updated_at,
		u.id, u.name, u.email, u.avatar_url, u.onboarded_at,
		u.onboarding_questionnaire, u.starter_content_state, u.language,
		u.profile_description, u.timezone, u.created_at, u.updated_at
		FROM local_identity_context c
		JOIN workspaces w ON w.id = c.workspace_id
		JOIN users u ON u.id = c.operator_id
		WHERE c.id = 1`).Scan(
		&identity.Workspace.ID,
		&identity.Workspace.Name,
		&identity.Workspace.Slug,
		&identity.Workspace.Description,
		&identity.Workspace.Context,
		&identity.Workspace.settingsJSON,
		&identity.Workspace.reposJSON,
		&identity.Workspace.IssuePrefix,
		&identity.Workspace.AvatarURL,
		&identity.Workspace.CreatedAt,
		&identity.Workspace.UpdatedAt,
		&identity.Operator.ID,
		&identity.Operator.Name,
		&identity.Operator.Email,
		&identity.Operator.AvatarURL,
		&identity.Operator.OnboardedAt,
		&identity.Operator.questionnaireJSON,
		&identity.Operator.StarterContentState,
		&identity.Operator.Language,
		&identity.Operator.ProfileDescription,
		&identity.Operator.Timezone,
		&identity.Operator.CreatedAt,
		&identity.Operator.UpdatedAt,
	)
	if err != nil {
		return IdentityContext{}, fmt.Errorf("read local identity context: %w", err)
	}
	if err := identity.decodeJSON(); err != nil {
		return IdentityContext{}, err
	}
	return identity, nil
}
