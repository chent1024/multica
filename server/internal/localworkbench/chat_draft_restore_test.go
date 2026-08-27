package localworkbench

import (
	"context"
	"net/http"
	"path/filepath"
	"testing"
)

func TestDispatchedChatCancellationPersistsDraftRestoreAcrossRestart(t *testing.T) {
	root := t.TempDir()
	databasePath := filepath.Join(root, "multica.db")
	fileRoot := filepath.Join(root, "files")
	open := func() *Server {
		server, err := NewServer(context.Background(), ServerConfig{DatabasePath: databasePath, FileRoot: fileRoot, Version: "test"})
		if err != nil {
			t.Fatal(err)
		}
		return server
	}

	server := open()
	handler := server.Router()
	projectResponse := localRequest(t, handler, http.MethodPost, "/api/projects", map[string]any{"title": "Draft restore project"})
	requireStatus(t, projectResponse, http.StatusCreated)
	project := decodeResponse[Project](t, projectResponse)
	register := localRequest(t, handler, http.MethodPost, "/api/daemon/register", map[string]any{
		"workspace_id": LocalWorkspaceID, "daemon_id": "draft-daemon",
		"runtimes": []map[string]any{{"name": "Codex", "type": "codex", "status": "online", "profile_id": "default"}},
	})
	requireStatus(t, register, http.StatusOK)
	runtimeID := decodeResponse[map[string]any](t, register)["runtimes"].([]any)[0].(map[string]any)["id"].(string)
	chatResponse := localRequest(t, handler, http.MethodPost, "/api/chat/sessions", map[string]any{"agent_id": LocalAgentID, "title": "Restore me", "project_id": project.ID})
	requireStatus(t, chatResponse, http.StatusCreated)
	chatID := decodeResponse[map[string]any](t, chatResponse)["id"].(string)
	send := localRequest(t, handler, http.MethodPost, "/api/chat/sessions/"+chatID+"/messages", map[string]any{"content": "keep this prompt"})
	requireStatus(t, send, http.StatusCreated)
	sent := decodeResponse[map[string]any](t, send)
	taskID := sent["task_id"].(string)
	messageID := sent["message_id"].(string)
	attachmentID := "draft-attachment"
	if _, err := server.store.db.Exec(`INSERT INTO attachments(id,workspace_id,filename,content_type,size_bytes,data,created_at,chat_session_id,chat_message_id) VALUES(?,?,?,?,?,x'',?,?,?)`, attachmentID, LocalWorkspaceID, "draft.txt", "text/plain", 0, now(), chatID, messageID); err != nil {
		t.Fatal(err)
	}
	claim := localRequest(t, handler, http.MethodPost, "/api/daemon/tasks/claim", map[string]any{"daemon_id": "draft-daemon", "runtime_ids": []string{runtimeID}, "max_tasks": 1})
	requireStatus(t, claim, http.StatusOK)

	cancel := localRequest(t, handler, http.MethodPost, "/api/tasks/"+taskID+"/cancel", map[string]any{})
	requireStatus(t, cancel, http.StatusOK)
	restoresResponse := localRequest(t, handler, http.MethodGet, "/api/chat/sessions/"+chatID+"/draft-restores", nil)
	requireStatus(t, restoresResponse, http.StatusOK)
	restores := decodeResponse[map[string]any](t, restoresResponse)["restores"].([]any)
	if len(restores) != 1 || restores[0].(map[string]any)["content"] != "keep this prompt" {
		t.Fatalf("draft restores=%v", restores)
	}
	attachments := restores[0].(map[string]any)["attachments"].([]any)
	if len(attachments) != 1 || attachments[0].(map[string]any)["id"] != attachmentID {
		t.Fatalf("draft restore attachments=%v", attachments)
	}
	restoreID := restores[0].(map[string]any)["id"].(string)

	if err := server.Close(); err != nil {
		t.Fatal(err)
	}
	server = open()
	defer server.Close()
	handler = server.Router()
	reopened := localRequest(t, handler, http.MethodGet, "/api/chat/sessions/"+chatID+"/draft-restores", nil)
	requireStatus(t, reopened, http.StatusOK)
	if got := len(decodeResponse[map[string]any](t, reopened)["restores"].([]any)); got != 1 {
		t.Fatalf("draft restore count after restart=%d, want 1", got)
	}
	consume := localRequest(t, handler, http.MethodDelete, "/api/chat/sessions/"+chatID+"/draft-restores/"+restoreID, nil)
	requireStatus(t, consume, http.StatusNoContent)
	consumed := localRequest(t, handler, http.MethodGet, "/api/chat/sessions/"+chatID+"/draft-restores", nil)
	if got := len(decodeResponse[map[string]any](t, consumed)["restores"].([]any)); got != 0 {
		t.Fatalf("draft restore count after consume=%d, want 0", got)
	}
}
