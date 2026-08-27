package localworkbench

import (
	"context"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/go-chi/chi/v5"
)

func TestRetainedDesktopRouteInventory(t *testing.T) {
	server, err := NewServer(context.Background(), ServerConfig{DatabasePath: filepath.Join(t.TempDir(), "db.sqlite"), FileRoot: filepath.Join(t.TempDir(), "files")})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	routes, ok := server.Router().(chi.Routes)
	if !ok {
		t.Fatal("local router does not expose its route inventory")
	}
	actual := map[string]bool{}
	if err := chi.Walk(routes, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		actual[method+" "+route] = true
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	required := []string{
		"GET /api/config",
		"GET /api/projects", "GET /api/issues", "GET /api/inbox", "GET /api/chat/sessions",
		"GET /api/agents", "GET /api/squads", "GET /api/autopilots", "GET /api/runtimes",
		"GET /api/skills", "GET /api/dashboard/usage/daily", "GET /api/workspaces/{id}/plugins",
		"POST /api/issues", "POST /api/issues/quick-create", "POST /api/chat/sessions/{id}/messages",
		"GET /api/comments/{commentID}/sub-issue-preview", "POST /api/comments/{commentID}/sub-issues",
		"POST /api/autopilots/{id}/trigger", "POST /api/webhooks/autopilots/{token}",
		"GET /api/workspaces/{id}/vcs/connections", "POST /api/webhooks/vcs/{connectionID}",
		"POST /api/daemon/tasks/claim", "GET /ws", "GET /health",
	}
	for _, route := range required {
		if !actual[route] {
			t.Errorf("retained Desktop route missing: %s", route)
		}
	}
}

func TestLocalConfigDoesNotAdvertiseUnavailableHostedIntegrations(t *testing.T) {
	server, err := NewServer(context.Background(), ServerConfig{DatabasePath: filepath.Join(t.TempDir(), "db.sqlite"), FileRoot: filepath.Join(t.TempDir(), "files")})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()

	response := localRequest(t, server.Router(), http.MethodGet, "/api/config", nil)
	requireStatus(t, response, http.StatusOK)
	config := decodeResponse[map[string]any](t, response)
	flags := config["feature_flags"].(map[string]any)
	if flags["composio_mcp_apps"] != false {
		t.Fatalf("composio_mcp_apps=%v, want false for local runtime", flags["composio_mcp_apps"])
	}
}
