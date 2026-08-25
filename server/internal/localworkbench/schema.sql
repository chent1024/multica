PRAGMA journal_mode = WAL;
PRAGMA synchronous = NORMAL;
PRAGMA foreign_keys = OFF;
PRAGMA busy_timeout = 5000;

CREATE TABLE IF NOT EXISTS schema_version (
    version INTEGER PRIMARY KEY,
    applied_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS workspaces (
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
);

CREATE TABLE IF NOT EXISTS projects (
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
);
CREATE INDEX IF NOT EXISTS projects_workspace_idx ON projects(workspace_id, updated_at DESC);

CREATE TABLE IF NOT EXISTS project_resources (
    id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL,
    workspace_id TEXT NOT NULL,
    resource_type TEXT NOT NULL,
    resource_ref TEXT NOT NULL DEFAULT '{}',
    label TEXT,
    position INTEGER NOT NULL DEFAULT 0,
    created_at TEXT NOT NULL,
    created_by TEXT
);
CREATE INDEX IF NOT EXISTS project_resources_project_idx ON project_resources(project_id, position);

CREATE TABLE IF NOT EXISTS issue_statuses (
    id TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL,
    key TEXT NOT NULL,
    name TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    category TEXT NOT NULL,
    color TEXT NOT NULL,
    is_system INTEGER NOT NULL DEFAULT 0,
    position INTEGER NOT NULL DEFAULT 0,
    archived_at TEXT,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    UNIQUE(workspace_id, key)
);
CREATE INDEX IF NOT EXISTS issue_statuses_workspace_idx ON issue_statuses(workspace_id, category, position);

CREATE TABLE IF NOT EXISTS issues (
    id TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL,
    number INTEGER NOT NULL,
    title TEXT NOT NULL,
    description TEXT,
    status TEXT NOT NULL DEFAULT 'backlog',
    priority TEXT NOT NULL DEFAULT 'none',
    assignee_type TEXT,
    assignee_id TEXT,
    creator_type TEXT NOT NULL DEFAULT 'system',
    creator_id TEXT NOT NULL,
    parent_issue_id TEXT,
    project_id TEXT,
    position REAL NOT NULL DEFAULT 0,
    stage INTEGER,
    start_date TEXT,
    due_date TEXT,
    metadata TEXT NOT NULL DEFAULT '{}',
    properties TEXT NOT NULL DEFAULT '{}',
    revision INTEGER NOT NULL DEFAULT 1,
    last_activity_at TEXT,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    UNIQUE(workspace_id, number)
);
CREATE INDEX IF NOT EXISTS issues_workspace_idx ON issues(workspace_id, updated_at DESC);
CREATE INDEX IF NOT EXISTS issues_project_idx ON issues(workspace_id, project_id, updated_at DESC);

CREATE TABLE IF NOT EXISTS comments (
    id TEXT PRIMARY KEY,
    issue_id TEXT NOT NULL,
    author_type TEXT NOT NULL,
    author_id TEXT NOT NULL,
    content TEXT NOT NULL,
    type TEXT NOT NULL DEFAULT 'comment',
    parent_id TEXT,
    revision INTEGER NOT NULL DEFAULT 1,
    issue_revision INTEGER NOT NULL DEFAULT 1,
    resolved_at TEXT,
    resolved_by_type TEXT,
    resolved_by_id TEXT,
    source_task_id TEXT,
    quick_action_id TEXT,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS comments_issue_idx ON comments(issue_id, created_at);

CREATE TABLE IF NOT EXISTS activities (
    id TEXT PRIMARY KEY,
    issue_id TEXT NOT NULL,
    actor_type TEXT NOT NULL,
    actor_id TEXT NOT NULL,
    action TEXT NOT NULL,
    details TEXT NOT NULL DEFAULT '{}',
    created_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS activities_issue_idx ON activities(issue_id, created_at);

CREATE TABLE IF NOT EXISTS task_runs (
    id TEXT PRIMARY KEY,
    issue_id TEXT NOT NULL,
    status TEXT NOT NULL,
    thread_id TEXT,
    runtime TEXT,
    handoff_note TEXT,
    summary TEXT,
    error TEXT,
    started_at TEXT,
    completed_at TEXT,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS task_runs_issue_idx ON task_runs(issue_id, created_at DESC);

CREATE TABLE IF NOT EXISTS task_messages (
    task_id TEXT NOT NULL,
    issue_id TEXT NOT NULL,
    seq INTEGER NOT NULL,
    type TEXT NOT NULL,
    tool TEXT,
    content TEXT,
    input TEXT,
    output TEXT,
    created_at TEXT NOT NULL,
    PRIMARY KEY(task_id, seq)
);
CREATE INDEX IF NOT EXISTS task_messages_issue_idx ON task_messages(issue_id, task_id, seq);

CREATE TABLE IF NOT EXISTS runtimes (
    id TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL,
    daemon_id TEXT NOT NULL,
    name TEXT NOT NULL,
    provider TEXT NOT NULL,
    version TEXT,
    status TEXT NOT NULL DEFAULT 'online',
    device_name TEXT,
    profile_id TEXT,
    last_seen_at TEXT,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    UNIQUE(workspace_id, daemon_id, provider, profile_id)
);
CREATE INDEX IF NOT EXISTS runtimes_workspace_idx ON runtimes(workspace_id, status, provider);

INSERT OR IGNORE INTO schema_version(version, applied_at)
VALUES (1, strftime('%Y-%m-%dT%H:%M:%fZ', 'now'));

INSERT OR IGNORE INTO schema_version(version, applied_at)
VALUES (2, strftime('%Y-%m-%dT%H:%M:%fZ', 'now'));

INSERT OR IGNORE INTO schema_version(version, applied_at)
VALUES (3, strftime('%Y-%m-%dT%H:%M:%fZ', 'now'));

INSERT OR IGNORE INTO schema_version(version, applied_at)
VALUES (4, strftime('%Y-%m-%dT%H:%M:%fZ', 'now'));

INSERT OR IGNORE INTO schema_version(version, applied_at)
VALUES (5, strftime('%Y-%m-%dT%H:%M:%fZ', 'now'));
