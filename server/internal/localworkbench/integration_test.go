package localworkbench

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func localRequest(t *testing.T, handler http.Handler, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		reader = bytes.NewReader(raw)
	}
	request := httptest.NewRequest(method, path, reader)
	request.RemoteAddr = "127.0.0.1:41414"
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func requireStatus(t *testing.T, response *httptest.ResponseRecorder, status int) {
	t.Helper()
	if response.Code != status {
		t.Fatalf("status=%d want=%d body=%s", response.Code, status, response.Body.String())
	}
}

func decodeResponse[T any](t *testing.T, response *httptest.ResponseRecorder) T {
	t.Helper()
	var value T
	if err := json.Unmarshal(response.Body.Bytes(), &value); err != nil {
		t.Fatalf("decode response: %v body=%s", err, response.Body.String())
	}
	return value
}

func TestLocalWorkbenchRetainedFeatureLifecyclePersists(t *testing.T) {
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

	me := localRequest(t, handler, http.MethodGet, "/api/me", nil)
	requireStatus(t, me, http.StatusOK)
	if user := decodeResponse[User](t, me); user.ID != LocalOperatorID {
		t.Fatalf("operator id=%s", user.ID)
	}
	cliTokenResponse := localRequest(t, handler, http.MethodPost, "/api/cli-token", map[string]any{})
	requireStatus(t, cliTokenResponse, http.StatusOK)
	if token := decodeResponse[map[string]any](t, cliTokenResponse)["token"]; token != "mul_local_desktop" {
		t.Fatalf("local CLI token=%v", token)
	}

	projectResponse := localRequest(t, handler, http.MethodPost, "/api/projects", map[string]any{"title": "Local project"})
	requireStatus(t, projectResponse, http.StatusCreated)
	project := decodeResponse[Project](t, projectResponse)

	register := localRequest(t, handler, http.MethodPost, "/api/daemon/register", map[string]any{
		"workspace_id": LocalWorkspaceID, "daemon_id": "daemon-test", "device_name": "test-mac",
		"runtimes": []map[string]any{{"name": "Codex", "type": "codex", "version": "1.0", "status": "online", "profile_id": "default"}},
	})
	requireStatus(t, register, http.StatusOK)
	registered := decodeResponse[map[string]any](t, register)
	runtimeID := registered["runtimes"].([]any)[0].(map[string]any)["id"].(string)
	agents := localRequest(t, handler, http.MethodGet, "/api/agents", nil)
	requireStatus(t, agents, http.StatusOK)
	agentList := decodeResponse[[]map[string]any](t, agents)
	if len(agentList) != 1 || agentList[0]["id"] != LocalAgentID {
		t.Fatalf("agents=%v", agentList)
	}
	setEnv := localRequest(t, handler, http.MethodPut, "/api/agents/"+LocalAgentID+"/env", map[string]any{"custom_env": map[string]any{"LOCAL_TEST_KEY": "secret"}})
	requireStatus(t, setEnv, http.StatusOK)
	agentDetail := localRequest(t, handler, http.MethodGet, "/api/agents/"+LocalAgentID, nil)
	requireStatus(t, agentDetail, http.StatusOK)
	redactedAgent := decodeResponse[map[string]any](t, agentDetail)
	if redactedAgent["custom_env_key_count"] != float64(1) || redactedAgent["has_custom_env"] != true {
		t.Fatalf("redacted agent env metadata=%v", redactedAgent)
	}
	if _, exposed := redactedAgent["custom_env"]; exposed {
		t.Fatal("agent detail exposed custom_env values")
	}

	skillResponse := localRequest(t, handler, http.MethodPost, "/api/skills", map[string]any{"name": "Review", "description": "Review changes", "content": "# Review"})
	requireStatus(t, skillResponse, http.StatusCreated)
	skill := decodeResponse[map[string]any](t, skillResponse)
	skillID := skill["id"].(string)
	attachSkill := localRequest(t, handler, http.MethodPost, "/api/agents/"+LocalAgentID+"/skills/add", map[string]any{"skill_ids": []string{skillID}})
	requireStatus(t, attachSkill, http.StatusNoContent)

	squadResponse := localRequest(t, handler, http.MethodPost, "/api/squads", map[string]any{"name": "Local squad", "leader_id": LocalAgentID})
	requireStatus(t, squadResponse, http.StatusCreated)
	squad := decodeResponse[map[string]any](t, squadResponse)
	if squad["member_count"].(float64) != 1 {
		t.Fatalf("squad=%v", squad)
	}

	automationResponse := localRequest(t, handler, http.MethodPost, "/api/autopilots", map[string]any{"title": "Daily local check", "project_id": project.ID, "assignee_type": "agent", "assignee_id": LocalAgentID, "execution_mode": "create_issue"})
	requireStatus(t, automationResponse, http.StatusCreated)
	automation := decodeResponse[map[string]any](t, automationResponse)
	automationID := automation["id"].(string)
	triggerResponse := localRequest(t, handler, http.MethodPost, "/api/autopilots/"+automationID+"/triggers", map[string]any{"kind": "schedule", "cron_expression": "0 9 * * *", "timezone": "UTC"})
	requireStatus(t, triggerResponse, http.StatusCreated)
	runResponse := localRequest(t, handler, http.MethodPost, "/api/autopilots/"+automationID+"/trigger", map[string]any{})
	requireStatus(t, runResponse, http.StatusCreated)
	run := decodeResponse[map[string]any](t, runResponse)
	if run["status"] != "issue_created" || run["issue_id"] == nil {
		t.Fatalf("automation run=%v", run)
	}
	if taskID, ok := run["task_id"].(string); ok && taskID != "" {
		response := localRequest(t, handler, http.MethodPost, "/api/daemon/tasks/"+taskID+"/complete", map[string]any{"output": "Automation complete"})
		requireStatus(t, response, http.StatusOK)
	}
	queuedAutomation := localRequest(t, handler, http.MethodPost, "/api/daemon/tasks/claim", map[string]any{"daemon_id": "daemon-test", "runtime_ids": []string{runtimeID}, "max_tasks": 1})
	requireStatus(t, queuedAutomation, http.StatusOK)
	queuedTasks := decodeResponse[map[string]any](t, queuedAutomation)["tasks"].([]any)
	if len(queuedTasks) == 1 {
		taskID := queuedTasks[0].(map[string]any)["id"].(string)
		response := localRequest(t, handler, http.MethodPost, "/api/daemon/tasks/"+taskID+"/complete", map[string]any{"output": "Automation complete"})
		requireStatus(t, response, http.StatusOK)
	}

	stats := localRequest(t, handler, http.MethodGet, "/api/dashboard/runtime/daily?days=30", nil)
	requireStatus(t, stats, http.StatusOK)

	chatResponse := localRequest(t, handler, http.MethodPost, "/api/chat/sessions", map[string]any{"agent_id": LocalAgentID, "title": "Local chat", "project_id": project.ID})
	requireStatus(t, chatResponse, http.StatusCreated)
	chat := decodeResponse[map[string]any](t, chatResponse)
	chatID := chat["id"].(string)
	sendResponse := localRequest(t, handler, http.MethodPost, "/api/chat/sessions/"+chatID+"/messages", map[string]any{"content": "Check the local task"})
	requireStatus(t, sendResponse, http.StatusCreated)
	sent := decodeResponse[map[string]any](t, sendResponse)
	chatTaskID := sent["task_id"].(string)
	claimResponse := localRequest(t, handler, http.MethodPost, "/api/daemon/tasks/claim", map[string]any{"daemon_id": "daemon-test", "runtime_ids": []string{runtimeID}, "max_tasks": 1})
	requireStatus(t, claimResponse, http.StatusOK)
	claimed := decodeResponse[map[string]any](t, claimResponse)
	claimedTasks := claimed["tasks"].([]any)
	if len(claimedTasks) != 1 || claimedTasks[0].(map[string]any)["id"] != chatTaskID {
		t.Fatalf("claimed chat tasks=%v", claimedTasks)
	}
	if token, _ := claimedTasks[0].(map[string]any)["auth_token"].(string); !strings.HasPrefix(token, "mat_") {
		t.Fatalf("claimed chat task auth_token=%q, want task-scoped mat_ token", token)
	}
	for path, body := range map[string]any{
		"/api/daemon/tasks/" + chatTaskID + "/session":  map[string]any{"session_id": "thread-local", "work_dir": root},
		"/api/daemon/tasks/" + chatTaskID + "/start":    map[string]any{},
		"/api/daemon/tasks/" + chatTaskID + "/progress": map[string]any{"summary": "working"},
		"/api/daemon/tasks/" + chatTaskID + "/messages": map[string]any{"messages": []map[string]any{{"seq": 1, "type": "text", "content": "working"}}},
		"/api/daemon/tasks/" + chatTaskID + "/usage":    map[string]any{"usage": []map[string]any{{"provider": "openai", "model": "codex", "input_tokens": 2, "output_tokens": 3, "cache_read_tokens": 0, "cache_write_tokens": 0}}},
	} {
		response := localRequest(t, handler, http.MethodPost, path, body)
		requireStatus(t, response, http.StatusOK)
	}
	completeResponse := localRequest(t, handler, http.MethodPost, "/api/daemon/tasks/"+chatTaskID+"/complete", map[string]any{"output": "Local task complete", "session_id": "thread-local", "work_dir": root})
	requireStatus(t, completeResponse, http.StatusOK)
	statusResponse := localRequest(t, handler, http.MethodGet, "/api/daemon/tasks/"+chatTaskID+"/status", nil)
	requireStatus(t, statusResponse, http.StatusOK)
	if status := decodeResponse[map[string]any](t, statusResponse)["status"]; status != "completed" {
		t.Fatalf("chat task status=%v", status)
	}

	var upload bytes.Buffer
	writer := multipart.NewWriter(&upload)
	part, err := writer.CreateFormFile("file", "proof.txt")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = part.Write([]byte("local file proof"))
	_ = writer.Close()
	uploadRequest := httptest.NewRequest(http.MethodPost, "/api/upload-file", &upload)
	uploadRequest.RemoteAddr = "127.0.0.1:41414"
	uploadRequest.Header.Set("Content-Type", writer.FormDataContentType())
	uploadResponse := httptest.NewRecorder()
	handler.ServeHTTP(uploadResponse, uploadRequest)
	requireStatus(t, uploadResponse, http.StatusCreated)
	attachment := decodeResponse[Attachment](t, uploadResponse)

	if err := server.Close(); err != nil {
		t.Fatal(err)
	}
	server = open()
	defer server.Close()
	handler = server.Router()

	for _, path := range []string{"/api/projects", "/api/agents", "/api/skills", "/api/squads", "/api/autopilots", "/api/chat/sessions", "/api/chat/sessions/" + chatID + "/messages"} {
		response := localRequest(t, handler, http.MethodGet, path, nil)
		requireStatus(t, response, http.StatusOK)
		if response.Body.Len() == 0 {
			t.Fatalf("empty response after restart: %s", path)
		}
	}
	messagesResponse := localRequest(t, handler, http.MethodGet, "/api/chat/sessions/"+chatID+"/messages", nil)
	messages := decodeResponse[[]map[string]any](t, messagesResponse)
	if len(messages) != 2 || messages[1]["content"] != "Local task complete" {
		t.Fatalf("persisted chat messages=%v", messages)
	}
	download := localRequest(t, handler, http.MethodGet, "/api/attachments/"+attachment.ID+"/download", nil)
	requireStatus(t, download, http.StatusOK)
	if download.Body.String() != "local file proof" {
		t.Fatalf("attachment body=%q", download.Body.String())
	}
	if _, err := os.Stat(filepath.Join(fileRoot, "attachments", attachment.ID)); err != nil {
		t.Fatalf("local attachment missing: %v", err)
	}
}

func TestLocalWorkbenchRejectsNonLoopbackAndBrowserOrigin(t *testing.T) {
	server, err := NewServer(context.Background(), ServerConfig{DatabasePath: filepath.Join(t.TempDir(), "db.sqlite"), FileRoot: filepath.Join(t.TempDir(), "files")})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()

	nonLoopback := httptest.NewRequest(http.MethodGet, "/api/me", nil)
	nonLoopback.RemoteAddr = "192.0.2.10:1234"
	response := httptest.NewRecorder()
	server.Router().ServeHTTP(response, nonLoopback)
	requireStatus(t, response, http.StatusForbidden)

	browser := httptest.NewRequest(http.MethodGet, "/api/me", nil)
	browser.RemoteAddr = "127.0.0.1:1234"
	browser.Header.Set("Origin", "https://evil.example")
	response = httptest.NewRecorder()
	server.Router().ServeHTTP(response, browser)
	requireStatus(t, response, http.StatusForbidden)
}

func TestIssueLabelsAndReactionsRoundTrip(t *testing.T) {
	root := t.TempDir()
	server, err := NewServer(context.Background(), ServerConfig{DatabasePath: filepath.Join(root, "db.sqlite"), FileRoot: filepath.Join(root, "files")})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	handler := server.Router()
	projectResponse := localRequest(t, handler, http.MethodPost, "/api/projects", map[string]any{"title": "Decorated issues"})
	requireStatus(t, projectResponse, http.StatusCreated)
	project := decodeResponse[Project](t, projectResponse)
	labelResponse := localRequest(t, handler, http.MethodPost, "/api/labels", map[string]any{"resource_type": "issue", "name": "Local", "color": "#123456"})
	requireStatus(t, labelResponse, http.StatusCreated)
	label := decodeResponse[map[string]any](t, labelResponse)
	issueResponse := localRequest(t, handler, http.MethodPost, "/api/issues", map[string]any{"title": "Decorated", "project_id": project.ID, "label_ids": []string{label["id"].(string)}})
	requireStatus(t, issueResponse, http.StatusCreated)
	issue := decodeResponse[Issue](t, issueResponse)
	if len(issue.Labels) != 1 {
		t.Fatalf("created issue labels=%v", issue.Labels)
	}
	reactionResponse := localRequest(t, handler, http.MethodPost, "/api/issues/"+issue.ID+"/reactions", map[string]any{"emoji": "👍"})
	requireStatus(t, reactionResponse, http.StatusCreated)
	detailResponse := localRequest(t, handler, http.MethodGet, "/api/issues/"+issue.ID, nil)
	requireStatus(t, detailResponse, http.StatusOK)
	detail := decodeResponse[Issue](t, detailResponse)
	if len(detail.Labels) != 1 || len(detail.Reactions) != 1 {
		t.Fatalf("issue labels=%v reactions=%v", detail.Labels, detail.Reactions)
	}
	sessionResponse := localRequest(t, handler, http.MethodGet, "/api/issues/"+issue.ID+"/session", nil)
	requireStatus(t, sessionResponse, http.StatusOK)
}

func TestDeleteAttachmentRestoresFileWhenMetadataDeleteFails(t *testing.T) {
	root := t.TempDir()
	server, err := NewServer(context.Background(), ServerConfig{DatabasePath: filepath.Join(root, "db.sqlite"), FileRoot: filepath.Join(root, "files")})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()

	itemID := uuid.NewString()
	storagePath := filepath.Join("attachments", itemID)
	if err := server.files.Put(t.Context(), storagePath, strings.NewReader("durable")); err != nil {
		t.Fatal(err)
	}
	if _, err := server.store.createAttachmentMetadata(t.Context(), itemID, LocalWorkspaceID, "", "durable.txt", "text/plain", storagePath, 7); err != nil {
		t.Fatal(err)
	}
	if _, err := server.store.db.Exec(`CREATE TRIGGER block_attachment_delete BEFORE DELETE ON attachments BEGIN SELECT RAISE(ABORT, 'blocked'); END`); err != nil {
		t.Fatal(err)
	}

	response := localRequest(t, server.Router(), http.MethodDelete, "/api/attachments/"+itemID, nil)
	requireStatus(t, response, http.StatusInternalServerError)
	file, err := server.files.Open(storagePath)
	if err != nil {
		t.Fatalf("attachment was not restored: %v", err)
	}
	content, readErr := io.ReadAll(file)
	_ = file.Close()
	if readErr != nil || string(content) != "durable" {
		t.Fatalf("restored content=%q error=%v", content, readErr)
	}
	if _, err := server.store.getAttachment(t.Context(), LocalWorkspaceID, itemID); err != nil {
		t.Fatalf("metadata missing after failed delete: %v", err)
	}
}
