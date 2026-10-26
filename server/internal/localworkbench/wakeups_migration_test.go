package localworkbench

import (
	"bytes"
	"database/sql"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/service"
)

type localWakeupMigrationSnapshot struct {
	columns []string
	rows    [][]any
}

// Read the original column projection after upgrade too: additive columns are
// validated separately, while every pre-existing value must remain identical.
func localWakeupMigrationRows(t *testing.T, db *sql.DB, tables []string, before map[string]localWakeupMigrationSnapshot) map[string]localWakeupMigrationSnapshot {
	t.Helper()
	result := make(map[string]localWakeupMigrationSnapshot, len(tables))
	for _, table := range tables {
		projection := "*"
		if old, exists := before[table]; exists {
			quoted := make([]string, len(old.columns))
			for index, column := range old.columns {
				quoted[index] = `"` + strings.ReplaceAll(column, `"`, `""`) + `"`
			}
			projection = strings.Join(quoted, ",")
		}
		rows, err := db.QueryContext(t.Context(), `SELECT `+projection+` FROM `+table+` ORDER BY 1`)
		if err != nil {
			t.Fatalf("snapshot %s: %v", table, err)
		}
		columns, err := rows.Columns()
		if err != nil {
			_ = rows.Close()
			t.Fatal(err)
		}
		entry := localWakeupMigrationSnapshot{columns: columns, rows: [][]any{}}
		for rows.Next() {
			values := make([]any, len(columns))
			pointers := make([]any, len(values))
			for index := range values {
				pointers[index] = &values[index]
			}
			if err := rows.Scan(pointers...); err != nil {
				_ = rows.Close()
				t.Fatal(err)
			}
			for index, value := range values {
				if raw, ok := value.([]byte); ok {
					values[index] = bytes.Clone(raw)
				}
			}
			entry.rows = append(entry.rows, values)
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			t.Fatal(err)
		}
		if err := rows.Close(); err != nil {
			t.Fatal(err)
		}
		result[table] = entry
	}
	return result
}

func TestLocalWakeupUpgradePreservesSchema22Data(t *testing.T) {
	t.Parallel()
	const oldVersion = 22
	if len(schemaMigrations) != 28 {
		t.Fatalf("expected reviewed migration target 28, got %d", len(schemaMigrations))
	}
	path := filepath.Join(t.TempDir(), "existing-desktop.sqlite")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	})
	if err := configureSQLite(t.Context(), db); err != nil {
		t.Fatal(err)
	}
	if err := applyMigrations(t.Context(), db, schemaMigrations[:oldVersion]); err != nil {
		t.Fatal(err)
	}
	seed := func(query string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(t.Context(), query, args...); err != nil {
			t.Fatal(err)
		}
	}
	stamp := "2026-08-27T06:15:50.389273Z"
	projectID, resourceID, runtimeID, daemonID, agentID := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	issueID, sessionID, stageID, taskID := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	commentID, activityID, notificationID, attachmentID := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	seed(`INSERT INTO projects(id,workspace_id,title,description,icon,status,priority,created_at,updated_at) VALUES(?,?,'Existing Multica','Preserve project context','folder','planned','high',?,?)`, projectID, LocalWorkspaceID, stamp, stamp)
	seed(`INSERT INTO project_resources(id,project_id,workspace_id,resource_type,resource_ref,label,position,created_at,created_by) VALUES(?,?,?,'local_directory',?,'Original checkout',0,?,?)`, resourceID, projectID, LocalWorkspaceID, `{"daemon_id":"`+daemonID+`","execution_mode":"in_place","local_path":"/existing/local/repository"}`, stamp, LocalOperatorID)
	seed(`INSERT INTO runtimes(id,workspace_id,daemon_id,name,provider,version,status,device_name,profile_id,last_seen_at,created_at,updated_at) VALUES(?,?,?,'Original Claude','claude','2.1.79 (Claude Code)','offline','original-machine','default',?,?,?)`, runtimeID, LocalWorkspaceID, daemonID, stamp, stamp, stamp)
	seed(`INSERT INTO agents(id,workspace_id,runtime_id,name,data,created_at,updated_at) VALUES(?,?,?,'Existing reviewer',?, ?,?)`, agentID, LocalWorkspaceID, runtimeID, `{"model":"existing-model","instructions":"Keep my instructions","custom_args":["--existing"]}`, stamp, stamp)
	seed(`INSERT INTO issues(id,workspace_id,number,title,description,status,priority,assignee_type,assignee_id,creator_type,creator_id,project_id,metadata,properties,revision,created_at,updated_at) VALUES(?,?,1,'Existing issue','Do not erase issue context','done','high','agent',?,'member',?,?,?, ?,7,?,?)`, issueID, LocalWorkspaceID, agentID, LocalOperatorID, projectID, `{"retained":true}`, `{"retained":"value"}`, stamp, stamp)
	seed(`INSERT INTO issue_sessions(id,issue_id,workspace_id,active_stage_id,continuity_state,created_at,updated_at) VALUES(?,?,?,?,'fresh',?,?)`, sessionID, issueID, LocalWorkspaceID, stageID, stamp, stamp)
	seed(`INSERT INTO issue_session_stages(id,session_id,kind,status,position,started_at,completed_at) VALUES(?,?,'execution','completed',0,?,?)`, stageID, sessionID, stamp, stamp)
	seed(`INSERT INTO task_runs(id,issue_id,status,thread_id,runtime,session_id,stage_id,work_dir,summary,started_at,completed_at,created_at,updated_at,agent_id) VALUES(?,?,'completed','existing-thread',?,?,?,'/existing/local/repository','Retained execution result',?,?,?,?,?)`, taskID, issueID, runtimeID, sessionID, stageID, stamp, stamp, stamp, stamp, agentID)
	seed(`INSERT INTO task_messages(task_id,issue_id,seq,type,content,created_at) VALUES(?,?,1,'output','Retained transcript',?)`, taskID, issueID, stamp)
	seed(`INSERT INTO task_run_usage(task_id,provider,model,input_tokens,output_tokens,cache_read_tokens,cache_write_tokens,cost_usd_ticks,updated_at) VALUES(?,'claude','existing-model',100,30,20,0,1234,?)`, taskID, stamp)
	seed(`INSERT INTO comments(id,issue_id,author_type,author_id,content,type,source_task_id,created_at,updated_at) VALUES(?,?,'agent',?,'Retained completion note','comment',?,?,?)`, commentID, issueID, agentID, taskID, stamp, stamp)
	seed(`INSERT INTO activities(id,issue_id,actor_type,actor_id,action,details,created_at) VALUES(?,?,'agent',?,'completed',?,?)`, activityID, issueID, agentID, `{"task_id":"`+taskID+`"}`, stamp)
	seed(`INSERT INTO inbox_items(id,workspace_id,recipient_type,recipient_id,type,severity,issue_id,title,body,read,archived,created_at,actor_type,actor_id,details,source_key) VALUES(?,?,'member',?,'runtime_failed','action_required',?,'Retained notification','Retained diagnostic',1,1,?,'agent',?,?,'original-notification')`, notificationID, LocalWorkspaceID, LocalOperatorID, issueID, stamp, agentID, `{"destination":"runtime","runtime_id":"`+runtimeID+`"}`)
	seed(`INSERT INTO local_preferences(workspace_id,owner_id,kind,data,updated_at) VALUES(?,?,'notification_preferences',?,?)`, LocalWorkspaceID, LocalOperatorID, `{"local_delivery":"action_required"}`, stamp)
	seed(`INSERT INTO attachments(id,workspace_id,issue_id,filename,content_type,size_bytes,data,created_at) VALUES(?,?,?,'retained.txt','text/plain',8,?,?)`, attachmentID, LocalWorkspaceID, issueID, []byte("retained"), stamp)
	tables := []string{"users", "workspaces", "local_identity_context", "projects", "project_resources", "runtimes", "agents", "issues", "issue_sessions", "issue_session_stages", "task_runs", "task_messages", "task_run_usage", "comments", "activities", "inbox_items", "local_preferences", "attachments"}
	before := localWakeupMigrationRows(t, db, tables, nil)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if store != nil {
			if err := store.Close(); err != nil {
				t.Error(err)
			}
		}
	})
	after := localWakeupMigrationRows(t, store.db, tables, before)
	if !reflect.DeepEqual(after, before) {
		for table, expected := range before {
			if !reflect.DeepEqual(after[table], expected) {
				t.Errorf("schema 22 data changed in %s: got=%#v want=%#v", table, after[table], expected)
			}
		}
		t.FailNow()
	}
	var metadata, providerVersion string
	if err := store.db.QueryRowContext(t.Context(), `SELECT metadata,version FROM runtimes WHERE id=?`, runtimeID).Scan(&metadata, &providerVersion); err != nil {
		t.Fatal(err)
	}
	if metadata != "{}" || providerVersion != "2.1.79 (Claude Code)" {
		t.Fatalf("legacy runtime changed or invented metadata: %q %q", metadata, providerVersion)
	}
	var events int
	if err := store.db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM issue_wakeup_events`).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if events != 0 {
		t.Fatalf("migration replayed historical business mutations: %d events", events)
	}
	future := time.Now().UTC().Add(time.Hour)
	wakeup := localWakeup{ID: uuid.NewString(), WorkspaceID: LocalWorkspaceID, IssueID: issueID, AgentID: agentID, CreatedBy: LocalOperatorID, Config: service.WakeupInput{AgentID: agentID, Instruction: "Retained database supports new rules", Kind: "at", Mode: "once", Timezone: "UTC", At: &future}, Enabled: true, Revision: 1, NextFireAt: sql.NullString{String: future.Format(time.RFC3339Nano), Valid: true}, CreatedAt: stamp, UpdatedAt: stamp}
	tx, err := store.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := putLocalWakeupTx(t.Context(), tx, wakeup); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.localWakeup(t.Context(), LocalWorkspaceID, issueID, wakeup.ID)
	if err != nil || loaded.Config.Instruction != wakeup.Config.Instruction || loaded.AgentID != agentID || loaded.Revision != 1 {
		t.Fatalf("new wakeup storage unusable after upgrade: %#v, %v", loaded, err)
	}
	ledger := localWakeupMigrationRows(t, store.db, []string{"schema_migrations"}, nil)
	if len(ledger["schema_migrations"].rows) != 28 {
		t.Fatalf("migration ledger=%d, want 28", len(ledger["schema_migrations"].rows))
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	if got := localWakeupMigrationRows(t, store.db, []string{"schema_migrations"}, ledger); !reflect.DeepEqual(got, ledger) {
		t.Fatalf("reopen repeated or rewrote a migration: %#v", got)
	}
	if got := localWakeupMigrationRows(t, store.db, tables, before); !reflect.DeepEqual(got, before) {
		t.Fatal("reopen altered the original schema 22 data")
	}
	if _, err := store.localWakeup(t.Context(), LocalWorkspaceID, issueID, wakeup.ID); err != nil {
		t.Fatalf("reopen lost the new rule: %v", err)
	}
}
