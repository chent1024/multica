package localworkbench

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

func TestServerBootstrapsFixedLocalContext(t *testing.T) {
	t.Parallel()
	server, err := NewServer(t.Context(), ServerConfig{
		DatabasePath: filepath.Join(t.TempDir(), "data", "multica.db"),
		FileRoot:     filepath.Join(t.TempDir(), "files"),
		Version:      "test",
		Commit:       "test-commit",
	})
	if err != nil {
		t.Fatalf("NewServer() error = %v", err)
	}
	t.Cleanup(func() { _ = server.Close() })

	tests := []struct {
		name  string
		path  string
		check func(t *testing.T, body []byte)
	}{
		{
			name: "health identifies sqlite instance",
			path: "/health",
			check: func(t *testing.T, body []byte) {
				var response struct {
					Mode       string `json:"mode"`
					Storage    string `json:"storage"`
					DatabaseID string `json:"database_id"`
				}
				if err := json.Unmarshal(body, &response); err != nil {
					t.Fatalf("decode health: %v", err)
				}
				if response.Mode != "local" || response.Storage != "sqlite" || response.DatabaseID == "" {
					t.Fatalf("health = %#v", response)
				}
			},
		},
		{
			name: "me is onboarded local operator",
			path: "/api/me",
			check: func(t *testing.T, body []byte) {
				var user User
				if err := json.Unmarshal(body, &user); err != nil {
					t.Fatalf("decode user: %v", err)
				}
				if user.ID != LocalOperatorID || user.OnboardedAt == nil {
					t.Fatalf("user = %#v", user)
				}
			},
		},
		{
			name: "workspace list has fixed workspace",
			path: "/api/workspaces",
			check: func(t *testing.T, body []byte) {
				var workspaces []Workspace
				if err := json.Unmarshal(body, &workspaces); err != nil {
					t.Fatalf("decode workspaces: %v", err)
				}
				if len(workspaces) != 1 || workspaces[0].ID != LocalWorkspaceID {
					t.Fatalf("workspaces = %#v", workspaces)
				}
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodGet, test.path, nil)
			request.RemoteAddr = "127.0.0.1:45000"
			server.Router().ServeHTTP(recorder, request)
			if recorder.Code != http.StatusOK {
				t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
			}
			test.check(t, recorder.Body.Bytes())
		})
	}
}
