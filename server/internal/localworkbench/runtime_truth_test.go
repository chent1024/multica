package localworkbench

import (
	"context"
	"net/http"
	"path/filepath"
	"testing"
)

func TestLocalRuntimeRegistrationPreservesAuthoritativeVersionsAndAvailability(t *testing.T) {
	root := t.TempDir()
	databasePath := filepath.Join(root, "db.sqlite")
	fileRoot := filepath.Join(root, "files")
	server, err := NewServer(context.Background(), ServerConfig{DatabasePath: databasePath, FileRoot: fileRoot})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if server != nil {
			_ = server.Close()
		}
	})
	handler := server.Router()
	register := localRequest(t, handler, http.MethodPost, "/api/daemon/register", map[string]any{
		"workspace_id": LocalWorkspaceID, "daemon_id": "truth-daemon", "device_name": "test-host",
		"cli_version": "0.7.0-local-test", "launched_by": "desktop",
		"runtimes": []map[string]any{
			{"name": "Claude", "type": "claude", "version": "2.1.79 (Claude Code)", "status": "offline"},
			{"name": "Codex", "type": "codex", "version": "codex-cli 0.164.0", "status": "online"},
			{"name": "Legacy", "type": "legacy", "version": "1.0.0"},
		},
	})
	requireStatus(t, register, http.StatusOK)
	listed := localRequest(t, handler, http.MethodGet, "/api/runtimes", nil)
	requireStatus(t, listed, http.StatusOK)
	rows := decodeResponse[[]map[string]any](t, listed)
	if len(rows) != 3 {
		t.Fatalf("runtime count=%d", len(rows))
	}
	ids := map[string]string{}
	for _, row := range rows {
		provider := row["provider"].(string)
		ids[provider] = row["id"].(string)
		wantStatus := "online"
		if provider == "claude" {
			wantStatus = "offline"
		}
		if row["status"] != wantStatus {
			t.Fatalf("%s status=%v want=%s", provider, row["status"], wantStatus)
		}
		metadata := row["metadata"].(map[string]any)
		if metadata["cli_version"] != "0.7.0-local-test" || metadata["launched_by"] != "desktop" {
			t.Fatalf("%s daemon metadata=%v", provider, metadata)
		}
		if provider == "claude" && metadata["version"] != "2.1.79 (Claude Code)" {
			t.Fatalf("provider version lost: %v", metadata)
		}
	}
	renamed := localRequest(t, handler, http.MethodPatch, "/api/runtimes/"+ids["codex"], map[string]any{"custom_name": "Local Codex"})
	requireStatus(t, renamed, http.StatusOK)
	if got := decodeResponse[map[string]any](t, renamed)["metadata"].(map[string]any)["cli_version"]; got != "0.7.0-local-test" {
		t.Fatalf("detail CLI version=%v", got)
	}
	if err := server.Close(); err != nil {
		t.Fatal(err)
	}
	server = nil
	server, err = NewServer(context.Background(), ServerConfig{DatabasePath: databasePath, FileRoot: fileRoot})
	if err != nil {
		t.Fatal(err)
	}
	persisted := localRequest(t, server.Router(), http.MethodGet, "/api/runtimes", nil)
	requireStatus(t, persisted, http.StatusOK)
	for _, row := range decodeResponse[[]map[string]any](t, persisted) {
		if row["id"] != ids[row["provider"].(string)] || row["metadata"].(map[string]any)["cli_version"] != "0.7.0-local-test" {
			t.Fatalf("restart changed runtime provenance: %v", row)
		}
	}
	// A later authoritative registration omitting a provider keeps that row but
	// removes its availability; process liveness cannot resurrect it.
	requireStatus(t, localRequest(t, server.Router(), http.MethodPost, "/api/daemon/register", map[string]any{
		"workspace_id": LocalWorkspaceID, "daemon_id": "truth-daemon", "cli_version": "0.7.1-local-test", "launched_by": "desktop",
		"runtimes": []map[string]any{{"name": "Codex", "type": "codex", "version": "codex-cli 0.165.0", "status": "online"}},
	}), http.StatusOK)
	persisted = localRequest(t, server.Router(), http.MethodGet, "/api/runtimes", nil)
	requireStatus(t, persisted, http.StatusOK)
	for _, row := range decodeResponse[[]map[string]any](t, persisted) {
		if row["provider"] != "codex" && row["status"] != "offline" {
			t.Fatalf("omitted provider remains online: %v", row)
		}
		if row["metadata"].(map[string]any)["cli_version"] != "0.7.1-local-test" {
			t.Fatalf("CLI metadata did not refresh: %v", row)
		}
	}
}

func TestLocalRuntimeLegacyProviderVersionIsNotCLIProvenance(t *testing.T) {
	root := t.TempDir()
	server, err := NewServer(context.Background(), ServerConfig{DatabasePath: filepath.Join(root, "db.sqlite"), FileRoot: filepath.Join(root, "files")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close() })
	if _, err := server.store.db.Exec(`INSERT INTO runtimes(id, workspace_id, daemon_id, name, provider, version, status, created_at, updated_at) VALUES (?, ?, 'old-daemon', 'Claude', 'claude', '2.1.79 (Claude Code)', 'offline', ?, ?)`, "legacy-runtime", LocalWorkspaceID, now(), now()); err != nil {
		t.Fatal(err)
	}
	response := localRequest(t, server.Router(), http.MethodGet, "/api/runtimes", nil)
	requireStatus(t, response, http.StatusOK)
	rows := decodeResponse[[]map[string]any](t, response)
	if len(rows) != 1 {
		t.Fatalf("runtime count=%d", len(rows))
	}
	metadata := rows[0]["metadata"].(map[string]any)
	if _, exists := metadata["cli_version"]; exists {
		t.Fatalf("legacy provider version inferred as CLI: %v", metadata)
	}
	if metadata["version"] != "2.1.79 (Claude Code)" {
		t.Fatalf("legacy provider version lost: %v", metadata)
	}
}
