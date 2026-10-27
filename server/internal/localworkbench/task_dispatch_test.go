package localworkbench

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

func newDispatchServer(t *testing.T) (*Server, http.Handler, string, []string) {
	t.Helper()
	root := t.TempDir()
	s, err := NewServer(context.Background(), ServerConfig{DatabasePath: filepath.Join(root, "test.db"), FileRoot: filepath.Join(root, "files")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	h := s.Router()
	project := localRequest(t, h, http.MethodPost, "/api/projects", map[string]any{"title": "Dispatch regression"})
	requireStatus(t, project, http.StatusCreated)
	projectID := decodeResponse[Project](t, project).ID
	registered := localRequest(t, h, http.MethodPost, "/api/daemon/register", map[string]any{
		"workspace_id": LocalWorkspaceID, "daemon_id": "dispatch-daemon", "device_name": "test",
		"runtimes": []map[string]any{
			{"name": "Codex", "type": "codex", "status": "online", "profile_id": "default"},
			{"name": "Claude", "type": "claude", "status": "online", "profile_id": "default"},
		},
	})
	requireStatus(t, registered, http.StatusOK)
	var runtimes []string
	for _, item := range decodeResponse[map[string]any](t, registered)["runtimes"].([]any) {
		runtimes = append(runtimes, item.(map[string]any)["id"].(string))
	}
	return s, h, projectID, runtimes
}

func enqueueDispatchIssue(t *testing.T, h http.Handler, project, runtime, name string) Issue {
	t.Helper()
	agent := localRequest(t, h, http.MethodPost, "/api/agents", map[string]any{"name": name, "runtime_id": runtime, "max_concurrent_tasks": 1})
	requireStatus(t, agent, http.StatusCreated)
	agentID := decodeResponse[map[string]any](t, agent)["id"].(string)
	issue := localRequest(t, h, http.MethodPost, "/api/issues", map[string]any{"title": name, "project_id": project, "assignee_type": "agent", "assignee_id": agentID, "status": "todo"})
	requireStatus(t, issue, http.StatusCreated)
	return decodeResponse[Issue](t, issue)
}

func TestLocalDaemonBatchFillsAvailableSlots(t *testing.T) {
	for _, limit := range []int{1, 3} {
		t.Run(fmt.Sprintf("limit_%d", limit), func(t *testing.T) {
			_, h, project, runtimes := newDispatchServer(t)
			for i := range 3 {
				enqueueDispatchIssue(t, h, project, runtimes[0], fmt.Sprintf("worker-%d", i))
			}
			claim := localRequest(t, h, http.MethodPost, "/api/daemon/tasks/claim", map[string]any{"daemon_id": "dispatch-daemon", "runtime_ids": runtimes[:1], "max_tasks": limit})
			requireStatus(t, claim, http.StatusOK)
			tasks := decodeResponse[map[string]any](t, claim)["tasks"].([]any)
			if len(tasks) != limit {
				t.Fatalf("claimed %d tasks from one runtime, want %d", len(tasks), limit)
			}
			seen := map[string]bool{}
			for _, task := range tasks {
				id := task.(map[string]any)["id"].(string)
				if seen[id] {
					t.Fatal("duplicate task claim")
				}
				seen[id] = true
			}
		})
	}
}

func TestLocalDaemonBatchPreservesRuntimeFairness(t *testing.T) {
	_, h, project, runtimes := newDispatchServer(t)
	for r, runtime := range runtimes {
		for i := range 3 {
			enqueueDispatchIssue(t, h, project, runtime, fmt.Sprintf("worker-%d-%d", r, i))
		}
	}
	claim := localRequest(t, h, http.MethodPost, "/api/daemon/tasks/claim", map[string]any{"daemon_id": "dispatch-daemon", "runtime_ids": runtimes, "max_tasks": 4})
	requireStatus(t, claim, http.StatusOK)
	tasks := decodeResponse[map[string]any](t, claim)["tasks"].([]any)
	if len(tasks) != 4 {
		t.Fatalf("claimed %d tasks, want 4", len(tasks))
	}
	for i, task := range tasks {
		if task.(map[string]any)["runtime_id"] != runtimes[i%len(runtimes)] {
			t.Fatalf("claim %d did not preserve round-robin runtime order", i)
		}
	}
}

func TestLocalDaemonWebSocketReceivesTaskAvailability(t *testing.T) {
	_, h, project, runtimes := newDispatchServer(t)
	server := httptest.NewServer(h)
	t.Cleanup(server.Close)
	connection, response, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/api/daemon/ws", nil)
	if err != nil {
		status := 0
		if response != nil {
			status = response.StatusCode
		}
		t.Fatalf("daemon websocket handshake failed, HTTP %d: %v", status, err)
	}
	ready := make(chan struct{})
	var readyOnce sync.Once
	connection.SetPongHandler(func(string) error { readyOnce.Do(func() { close(ready) }); return nil })
	available := make(chan protocol.Message, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			var message protocol.Message
			if err := connection.ReadJSON(&message); err != nil {
				return
			}
			if message.Type == protocol.EventDaemonTaskAvailable {
				available <- message
				return
			}
		}
	}()
	t.Cleanup(func() { _ = connection.Close(); <-done })
	if err := connection.WriteMessage(websocket.PingMessage, []byte("ready")); err != nil {
		t.Fatal(err)
	}
	select {
	case <-ready:
	case <-time.After(time.Second):
		t.Fatal("websocket reader did not become ready")
	}
	enqueueDispatchIssue(t, h, project, runtimes[0], "interactive-task")
	select {
	case <-available:
	case <-time.After(time.Second):
		t.Fatal("queued task did not wake the daemon without a polling interval")
	}
}

func TestLocalDaemonWebSocketKeepsRequestBoundary(t *testing.T) {
	_, handler, _, _ := newDispatchServer(t)
	for _, test := range []struct {
		name, remote, origin string
		status               int
	}{
		{"loopback_needs_upgrade", "127.0.0.1:1234", "", http.StatusBadRequest},
		{"reject_browser_origin", "127.0.0.1:1234", "https://example.com", http.StatusForbidden},
		{"reject_remote_client", "192.0.2.1:1234", "", http.StatusForbidden},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "/api/daemon/ws", nil)
			request.RemoteAddr = test.remote
			request.Header.Set("Origin", test.origin)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			requireStatus(t, response, test.status)
		})
	}
}

func TestLocalDaemonConcurrentBatchClaimsAreUnique(t *testing.T) {
	_, handler, project, runtimes := newDispatchServer(t)
	for i := range 6 {
		enqueueDispatchIssue(t, handler, project, runtimes[0], fmt.Sprintf("concurrent-worker-%d", i))
	}
	body, err := json.Marshal(map[string]any{"daemon_id": "dispatch-daemon", "runtime_ids": runtimes[:1], "max_tasks": 3})
	if err != nil {
		t.Fatal(err)
	}
	responses := make(chan *httptest.ResponseRecorder, 2)
	var calls sync.WaitGroup
	for range 2 {
		calls.Add(1)
		go func() {
			defer calls.Done()
			request := httptest.NewRequest(http.MethodPost, "/api/daemon/tasks/claim", strings.NewReader(string(body)))
			request.RemoteAddr = "127.0.0.1:1234"
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			responses <- response
		}()
	}
	calls.Wait()
	close(responses)
	seen := map[string]bool{}
	for response := range responses {
		requireStatus(t, response, http.StatusOK)
		tasks := decodeResponse[map[string]any](t, response)["tasks"].([]any)
		if len(tasks) != 3 {
			t.Fatalf("batch claimed %d tasks, want 3", len(tasks))
		}
		for _, task := range tasks {
			id := task.(map[string]any)["id"].(string)
			if seen[id] {
				t.Fatal("concurrent requests claimed the same task")
			}
			seen[id] = true
		}
	}
	if len(seen) != 6 {
		t.Fatalf("claimed %d unique tasks, want 6", len(seen))
	}
}
