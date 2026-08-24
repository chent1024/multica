package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/spf13/cobra"

	"github.com/multica-ai/multica/server/internal/cli"
	"github.com/multica-ai/multica/server/internal/daemon/execenv"
)

var uuidRegexp = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

func strVal(value map[string]any, key string) string {
	if text, ok := value[key].(string); ok {
		return text
	}
	return ""
}

func resolveProfile(cmd *cobra.Command) string {
	value, _ := cmd.Flags().GetString("profile")
	return value
}

func newAPIClient(cmd *cobra.Command) (*cli.APIClient, error) {
	serverURL := cli.FlagOrEnv(cmd, "server-url", "MULTICA_SERVER_URL", "")
	if serverURL == "" {
		profile := resolveProfile(cmd)
		cfg, err := cli.LoadCLIConfigForProfile(profile)
		if err == nil {
			serverURL = cfg.ServerURL
		}
	}
	if serverURL == "" {
		return nil, fmt.Errorf("local server URL is not configured")
	}
	if !serverHostIsLocal(serverURL) {
		return nil, fmt.Errorf("local mode refuses non-loopback server URL %q", serverURL)
	}

	workspaceID := cli.FlagOrEnv(cmd, "workspace-id", "MULTICA_WORKSPACE_ID", "")
	if workspaceID == "" && !inDaemonManagedExecutionContext() {
		profile := resolveProfile(cmd)
		cfg, err := cli.LoadCLIConfigForProfile(profile)
		if err == nil {
			workspaceID = cfg.WorkspaceID
		}
	}

	client := cli.NewAPIClient(serverURL, workspaceID, "")
	client.AgentID = os.Getenv("MULTICA_AGENT_ID")
	client.TaskID = os.Getenv("MULTICA_TASK_ID")
	return client, nil
}

func inAgentExecutionContext() bool {
	return os.Getenv("MULTICA_AGENT_ID") != "" || os.Getenv("MULTICA_TASK_ID") != ""
}

func inDaemonManagedExecutionContext() bool {
	return inAgentExecutionContext() ||
		os.Getenv("MULTICA_DAEMON_PORT") != "" ||
		hasDaemonTaskContextMarker()
}

func inDaemonTaskIdentityContext() bool {
	return inAgentExecutionContext() ||
		strings.TrimSpace(os.Getenv(cli.TaskConfigRootEnv)) != "" ||
		hasDaemonTaskContextMarker()
}

func hasDaemonTaskContextMarker() bool {
	return daemonTaskContextMarkerPath() != ""
}

func daemonTaskContextMarkerPath() string {
	dir, err := os.Getwd()
	if err != nil {
		return ""
	}
	for {
		markerPath := filepath.Join(dir, execenv.TaskContextMarkerRelPath)
		if data, readErr := os.ReadFile(markerPath); readErr == nil {
			var marker struct {
				ManagedBy string `json:"managed_by"`
			}
			if json.Unmarshal(data, &marker) == nil && marker.ManagedBy == execenv.TaskContextMarkerManagedBy {
				return markerPath
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

func resolveWorkspaceID(cmd *cobra.Command) string {
	value := cli.FlagOrEnv(cmd, "workspace-id", "MULTICA_WORKSPACE_ID", "")
	if value != "" || inDaemonManagedExecutionContext() {
		return value
	}
	cfg, _ := cli.LoadCLIConfigForProfile(resolveProfile(cmd))
	return cfg.WorkspaceID
}

func requireWorkspaceID(cmd *cobra.Command) (string, error) {
	if id := resolveWorkspaceID(cmd); id != "" {
		return id, nil
	}
	return "", fmt.Errorf("local data-space ID is not configured")
}

func requireTaskLocalConfigRoot() error {
	if !inDaemonManagedExecutionContext() {
		return nil
	}
	if strings.TrimSpace(os.Getenv(cli.TaskConfigRootEnv)) != "" {
		return nil
	}
	if markerPath := leftoverDaemonTaskMarkerPath(); markerPath != "" {
		return fmt.Errorf("daemon-managed task requires %s%s", cli.TaskConfigRootEnv, leftoverMarkerSuffix(markerPath))
	}
	return fmt.Errorf("daemon-managed task requires %s", cli.TaskConfigRootEnv)
}

func requireHumanLocalCommand(command string) error {
	if !inDaemonTaskIdentityContext() {
		return nil
	}
	if markerPath := leftoverDaemonTaskMarkerPath(); markerPath != "" {
		return fmt.Errorf("%s is not available inside a daemon-managed task%s", command, leftoverMarkerSuffix(markerPath))
	}
	return fmt.Errorf("%s is not available inside a daemon-managed task", command)
}
