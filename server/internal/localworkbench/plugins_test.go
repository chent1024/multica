package localworkbench

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/pkg/plugincontract"
	"github.com/multica-ai/multica/server/pkg/remotemcp"
)

func TestLocalPluginPackageInstallConfigureAndStore(t *testing.T) {
	manifest := map[string]any{"manifest_version": 1, "key": "com.example.local", "name": "Local plugin", "version": "1.0.0", "author": map[string]any{"name": "test"}, "scopes": []string{"issues:read", "storage:user"}, "config": map[string]any{"token": map[string]any{"type": "secret", "label": "Token"}}, "contributes": map[string]any{"resources": []any{map[string]any{"type": "skill", "key": "local-skill", "entry": "skills/local-skill/SKILL.md"}}}}
	manifestRaw, _ := json.Marshal(manifest)
	var archive bytes.Buffer
	zipper := zip.NewWriter(&archive)
	entry, _ := zipper.Create(plugincontract.ManifestFilename)
	_, _ = entry.Write(manifestRaw)
	skillEntry, _ := zipper.Create("skills/local-skill/SKILL.md")
	_, _ = skillEntry.Write([]byte("---\nname: local-skill\n---\n\nUse local data.\n"))
	_ = zipper.Close()
	root := t.TempDir()
	server, err := NewServer(context.Background(), ServerConfig{DatabasePath: filepath.Join(root, "db.sqlite"), FileRoot: filepath.Join(root, "files")})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	handler := server.Router()
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	part, _ := form.CreateFormFile("bundle", "plugin.zip")
	_, _ = part.Write(archive.Bytes())
	_ = form.Close()
	request := httptest.NewRequest(http.MethodPost, "/api/workspaces/"+LocalWorkspaceID+"/plugins/packages", &body)
	request.RemoteAddr = "127.0.0.1:4000"
	request.Header.Set("Content-Type", form.FormDataContentType())
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	requireStatus(t, response, http.StatusCreated)
	pkg := decodeResponse[map[string]any](t, response)
	versionID := pkg["versions"].([]any)[0].(map[string]any)["id"].(string)
	install := localRequest(t, handler, http.MethodPost, "/api/workspaces/"+LocalWorkspaceID+"/plugins", map[string]any{"version_id": versionID, "granted_scopes": []string{"issues:read", "storage:user"}})
	requireStatus(t, install, http.StatusCreated)
	installed := decodeResponse[map[string]any](t, install)
	installationID := installed["id"].(string)
	configured := localRequest(t, handler, http.MethodPut, "/api/workspaces/"+LocalWorkspaceID+"/plugins/"+installationID+"/config", map[string]any{"values": map[string]any{"token": "secret-value"}})
	requireStatus(t, configured, http.StatusOK)
	if bytes.Contains(configured.Body.Bytes(), []byte("secret-value")) {
		t.Fatal("plugin secret exposed")
	}
	enable := localRequest(t, handler, http.MethodPost, "/api/workspaces/"+LocalWorkspaceID+"/plugins/"+installationID+"/enable", map[string]any{})
	requireStatus(t, enable, http.StatusOK)
	storageRequest := httptest.NewRequest(http.MethodPut, "/api/plugin-bridge/v1/storage/user/note", bytes.NewReader([]byte(`{"value":"local"}`)))
	storageRequest.RemoteAddr = "127.0.0.1:4000"
	storageRequest.Header.Set("Content-Type", "application/json")
	storageRequest.Header.Set("X-Multica-Plugin-Installation", installationID)
	storageResponse := httptest.NewRecorder()
	handler.ServeHTTP(storageResponse, storageRequest)
	requireStatus(t, storageResponse, http.StatusNoContent)
}

func TestLocalPluginAgentHooksAndMCPReachDaemonClaim(t *testing.T) {
	var receivedTrigger string
	hookServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		receivedTrigger, _ = body["trigger"].(string)
		writeJSON(w, http.StatusOK, map[string]any{"accepted": true})
	}))
	defer hookServer.Close()

	root := t.TempDir()
	server, err := NewServer(context.Background(), ServerConfig{DatabasePath: filepath.Join(root, "db.sqlite"), FileRoot: filepath.Join(root, "files")})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	handler := server.Router()
	register := localRequest(t, handler, http.MethodPost, "/api/daemon/register", map[string]any{
		"workspace_id": LocalWorkspaceID, "daemon_id": "plugin-daemon",
		"runtimes": []map[string]any{{"name": "Codex", "type": "codex", "status": "online", "profile_id": "default"}},
	})
	requireStatus(t, register, http.StatusOK)
	runtimeID := decodeResponse[map[string]any](t, register)["runtimes"].([]any)[0].(map[string]any)["id"].(string)

	installationID := uuid.NewString()
	schema := json.RawMessage(`{"type":"object","properties":{"value":{"type":"string"}}}`)
	approvedTool := remotemcp.Tool{Name: "lookup", Description: "Lookup local data", InputSchema: schema, SchemaDigest: remotemcp.DigestBytes(schema)}
	manifest := map[string]any{
		"manifest_version": 1, "key": "com.example.agent", "name": "Agent plugin", "version": "1.0.0",
		"scopes": []any{"net:example.com"},
		"config": map[string]any{"remote_credential": map[string]any{"type": "secret", "label": "Credential"}},
		"contributes": map[string]any{"hooks": []any{
			map[string]any{"key": "review", "name": "Review", "description": "Review through the plugin", "triggers": []any{"agent"}, "transport": map[string]any{"type": "http", "url": hookServer.URL}},
			map[string]any{"key": "remote", "name": "Remote", "description": "Remote MCP", "triggers": []any{"agent"}, "transport": map[string]any{"type": "mcp", "url": "https://example.com/mcp"}},
		}},
	}
	item := map[string]any{
		"id": installationID, "workspace_id": LocalWorkspaceID, "plugin_key": "com.example.agent", "enabled": true,
		"manifest": manifest, "hooks": manifest["contributes"].(map[string]any)["hooks"], "granted_scopes": []any{"net:example.com"},
		"approved_mcp_tools": map[string]any{"remote": []any{approvedTool}},
	}
	raw, _ := encodeJSONMap(item)
	stamp := now()
	if _, err := server.store.db.Exec(`INSERT INTO plugin_installations(id,workspace_id,package_id,plugin_key,enabled,data,secret_config,created_at,updated_at) VALUES(?,?,?,?,1,?,?,?,?)`, installationID, LocalWorkspaceID, "package", "com.example.agent", raw, `{"remote_credential":"Bearer local-secret"}`, stamp, stamp); err != nil {
		t.Fatal(err)
	}

	projectResponse := localRequest(t, handler, http.MethodPost, "/api/projects", map[string]any{"title": "Plugin project"})
	requireStatus(t, projectResponse, http.StatusCreated)
	project := decodeResponse[Project](t, projectResponse)
	chatResponse := localRequest(t, handler, http.MethodPost, "/api/chat/sessions", map[string]any{"agent_id": LocalAgentID, "title": "Plugin task", "project_id": project.ID})
	requireStatus(t, chatResponse, http.StatusCreated)
	chatID := decodeResponse[map[string]any](t, chatResponse)["id"].(string)
	send := localRequest(t, handler, http.MethodPost, "/api/chat/sessions/"+chatID+"/messages", map[string]any{"content": "use the plugin"})
	requireStatus(t, send, http.StatusCreated)
	taskID := decodeResponse[map[string]any](t, send)["task_id"].(string)
	claim := localRequest(t, handler, http.MethodPost, "/api/daemon/tasks/claim", map[string]any{"daemon_id": "plugin-daemon", "runtime_ids": []string{runtimeID}, "max_tasks": 1})
	requireStatus(t, claim, http.StatusOK)
	task := decodeResponse[map[string]any](t, claim)["tasks"].([]any)[0].(map[string]any)
	if len(task["plugin_hook_tools"].([]any)) != 1 || len(task["remote_mcp_connections"].([]any)) != 1 {
		t.Fatalf("plugin claim context=%v", task)
	}
	if token, _ := task["remote_mcp_daemon_token"].(string); !strings.HasPrefix(token, "mdt_local_") {
		t.Fatalf("remote MCP daemon token=%q", token)
	}

	credential := localRequest(t, handler, http.MethodGet, "/api/daemon/tasks/"+taskID+"/plugin-mcp/plugin:"+installationID+":remote/credential", nil)
	requireStatus(t, credential, http.StatusOK)
	credentialBody := decodeResponse[map[string]any](t, credential)
	if credentialBody["credential"] != "Bearer local-secret" || credentialBody["credential_header"] != "Authorization" {
		t.Fatalf("plugin MCP credential=%v", credentialBody)
	}
	invoke := localRequest(t, handler, http.MethodPost, "/api/daemon/tasks/"+taskID+"/plugin-hooks", map[string]any{"installation_id": installationID, "hook_key": "review", "input": map[string]any{"value": "check"}})
	requireStatus(t, invoke, http.StatusOK)
	if result := decodeResponse[map[string]any](t, invoke); result["status"] != "ok" || receivedTrigger != plugincontract.TriggerAgent {
		t.Fatalf("plugin hook result=%v trigger=%q", result, receivedTrigger)
	}
	delete(item, "approved_mcp_tools")
	raw, _ = encodeJSONMap(item)
	if _, err := server.store.db.Exec(`UPDATE plugin_installations SET data=? WHERE id=?`, raw, installationID); err != nil {
		t.Fatal(err)
	}
	unapprovedCredential := localRequest(t, handler, http.MethodGet, "/api/daemon/tasks/"+taskID+"/plugin-mcp/plugin:"+installationID+":remote/credential", nil)
	requireStatus(t, unapprovedCredential, http.StatusForbidden)
}
