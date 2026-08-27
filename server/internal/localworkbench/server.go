package localworkbench

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/gorilla/websocket"
)

type ServerConfig struct {
	DatabasePath string
	FileRoot     string
	Version      string
	Commit       string
	Logger       *slog.Logger
}

type Server struct {
	store      *Store
	files      *FileStore
	logger     *slog.Logger
	hub        *localHub
	databaseID string
	version    string
	commit     string
	startedAt  string
	cancel     context.CancelFunc
	wg         sync.WaitGroup
}

func NewServer(ctx context.Context, config ServerConfig) (*Server, error) {
	store, err := Open(ctx, config.DatabasePath)
	if err != nil {
		return nil, err
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE runtime_requests SET status='pending', updated_at=? WHERE status='running'`, now()); err != nil {
		_ = store.Close()
		return nil, fmt.Errorf("recover local runtime requests: %w", err)
	}
	files, err := OpenFileStore(config.FileRoot)
	if err != nil {
		_ = store.Close()
		return nil, err
	}
	absolutePath, err := filepath.Abs(config.DatabasePath)
	if err != nil {
		_ = files.Close()
		_ = store.Close()
		return nil, fmt.Errorf("resolve sqlite identity path: %w", err)
	}
	hash := sha256.Sum256([]byte(absolutePath))
	logger := config.Logger
	if logger == nil {
		logger = slog.Default()
	}
	serverContext, cancel := context.WithCancel(context.Background())
	server := &Server{
		store:      store,
		files:      files,
		logger:     logger,
		hub:        newLocalHub(),
		databaseID: hex.EncodeToString(hash[:]),
		version:    config.Version,
		commit:     config.Commit,
		startedAt:  time.Now().UTC().Format(time.RFC3339Nano),
		cancel:     cancel,
	}
	server.wg.Add(1)
	go server.runAutomationScheduler(serverContext)
	return server, nil
}

func New(ctx context.Context, databasePath string, logger *slog.Logger) (*Server, error) {
	return NewServer(ctx, ServerConfig{
		DatabasePath: databasePath,
		FileRoot:     databasePath + ".files",
		Version:      "local-sqlite",
		Logger:       logger,
	})
}

func (s *Server) Close() error {
	s.cancel()
	s.wg.Wait()
	s.hub.close()
	return errors.Join(s.files.Close(), s.store.Close())
}

func (s *Server) Router() http.Handler {
	router := chi.NewRouter()
	router.Use(LoopbackOnly)
	router.Use(localRequestBoundary)
	router.Get("/health", s.health)
	router.Get("/ws", s.websocket)
	router.Get("/ws/daemon", s.websocket)
	router.Route("/api", func(api chi.Router) {
		api.Get("/config", s.appConfig)
		api.Get("/me", s.getMe)
		api.Patch("/me", s.updateMe)
		api.Patch("/me/onboarding", s.patchOnboarding)
		api.Post("/me/onboarding/complete", s.completeOnboarding)
		api.Post("/me/onboarding/cloud-waitlist", s.cloudOnlyUnavailable)
		api.Post("/upload-file", s.uploadFile)
		api.Post("/feedback", localNoContent)
		api.Post("/client-usage", localNoContent)
		api.Post("/cli-token", localObject("token", "mul_local_desktop"))
		api.Get("/tokens", emptyArray())
		api.Post("/tokens", s.cloudOnlyUnavailable)
		api.Delete("/tokens/{id}", s.cloudOnlyUnavailable)
		api.Get("/invitations", emptyArray())
		api.Get("/invitations/{id}", s.cloudOnlyUnavailable)
		api.Post("/invitations/{id}/accept", s.cloudOnlyUnavailable)
		api.Post("/invitations/{id}/decline", s.cloudOnlyUnavailable)
		api.Get("/workspaces/{id}/invitations", emptyArray())
		api.Post("/workspaces/{id}/invitations", s.cloudOnlyUnavailable)
		api.Delete("/workspaces/{id}/invitations/{invitationID}", s.cloudOnlyUnavailable)
		api.Get("/workspaces/{id}/share-links", emptyArray())
		api.Post("/workspaces/{id}/share-links", s.cloudOnlyUnavailable)
		api.Delete("/workspaces/{id}/share-links/{linkID}", s.cloudOnlyUnavailable)
		api.Get("/share-links/{code}", s.cloudOnlyUnavailable)
		api.Post("/share-links/join", s.cloudOnlyUnavailable)
		api.Get("/attachments/{id}", s.getAttachment)
		api.Get("/attachments/{id}/content", s.attachmentContent)
		api.Get("/attachments/{id}/download", s.downloadAttachment)
		api.Delete("/attachments/{id}", s.deleteAttachment)
		api.Get("/workspaces", s.listWorkspaces)
		api.Get("/workspaces/{id}", s.getWorkspace)
		api.Patch("/workspaces/{id}", s.updateWorkspace)
		api.Get("/workspaces/{id}/members", s.workspaceMembers)
		api.Post("/workspaces/{id}/members", s.workspaceMembers)
		api.Patch("/workspaces/{id}/members/{memberID}", s.workspaceMembers)
		api.Delete("/workspaces/{id}/members/{memberID}", s.workspaceMembers)
		api.Post("/workspaces/{id}/leave", s.workspaceMembers)
		api.Get("/workspaces/{id}/mcp-servers", s.workspaceMCPServers)
		api.Post("/workspaces/{id}/mcp-servers", s.workspaceMCPServers)
		api.Put("/workspaces/{id}/mcp-servers/{serverID}", s.workspaceMCPServerItem)
		api.Delete("/workspaces/{id}/mcp-servers/{serverID}", s.workspaceMCPServerItem)
		api.Get("/workspaces/{id}/plugins/packages", s.listPluginPackages)
		api.Post("/workspaces/{id}/plugins/packages", s.publishPluginPackage)
		api.Post("/workspaces/{id}/plugins/packages/local", s.publishLocalPluginPackage)
		api.Delete("/workspaces/{id}/plugins/packages/{packageID}", s.deletePluginPackage)
		api.Post("/workspaces/{id}/plugins/preview", s.previewPlugin)
		api.Get("/workspaces/{id}/plugins", s.pluginInstallations)
		api.Post("/workspaces/{id}/plugins", s.pluginInstallations)
		api.Delete("/workspaces/{id}/plugins/{installationID}", s.uninstallPlugin)
		api.Put("/workspaces/{id}/plugins/{installationID}/config", s.configurePlugin)
		api.Post("/workspaces/{id}/plugins/{installationID}/{action:enable|disable}", s.setPluginEnabled)
		api.Get("/workspaces/{id}/plugins/{installationID}/invocations", s.pluginInvocations)
		api.Post("/workspaces/{id}/plugins/{installationID}/token", s.pluginToken)
		api.Delete("/workspaces/{id}/plugins/{installationID}/token", s.pluginToken)
		api.Get("/workspaces/{id}/plugins/{installationID}/mcp/{hookKey}/tools", s.listPluginMCPTools)
		api.Put("/workspaces/{id}/plugins/{installationID}/mcp/{hookKey}/tools", s.configurePluginMCPTools)
		api.Get("/workspaces/{id}/plugins/{installationID}/surfaces/{surfaceKey}/launch", s.launchPluginSurface)
		api.Get("/plugin-assets/{installationID}/{surfaceKey}", s.pluginSurfaceAsset)
		api.Post("/plugin-bridge/v1/hooks/{hookKey}", s.invokePluginHook)
		api.Get("/plugin-bridge/v1/context", s.pluginBridgeContext)
		api.Get("/plugin-bridge/v1/issues/{id}", s.pluginBridgeIssue)
		api.Patch("/plugin-bridge/v1/issues/{id}", s.pluginBridgeIssue)
		api.Get("/plugin-bridge/v1/issues/{id}/comments", s.pluginBridgeComments)
		api.Post("/plugin-bridge/v1/issues/{id}/comments", s.pluginBridgeComments)
		api.Get("/plugin-bridge/v1/storage/{scope}", s.pluginBridgeStorage)
		api.Get("/plugin-bridge/v1/storage/{scope}/{key}", s.pluginBridgeStorage)
		api.Put("/plugin-bridge/v1/storage/{scope}/{key}", s.pluginBridgeStorage)
		api.Delete("/plugin-bridge/v1/storage/{scope}/{key}", s.pluginBridgeStorage)
		api.Get("/workspaces/{id}/vcs/connections", s.localIntegrationList("vcs"))
		api.Post("/workspaces/{id}/vcs/connections", s.registerLocalIntegration("vcs"))
		api.Delete("/workspaces/{id}/vcs/connections/{installationID}", s.deleteLocalIntegration("vcs"))
		api.Post("/workspaces/{id}/vcs/connections/{installationID}/rotate-webhook", s.rotateVCSWebhook)
		api.Get("/workspaces/{id}/github/connect", localObject("configured", false))
		api.Get("/workspaces/{id}/github/installations", localObject("installations", []any{}, "configured", false, "repository_browse_configured", false, "can_manage", true))
		api.Get("/workspaces/{id}/github/installations/{installationID}/repositories", localObject("repositories", []any{}, "total_count", 0))
		api.Delete("/workspaces/{id}/github/installations/{installationID}", s.deleteLocalIntegration("github"))
		api.Get("/workspaces/{id}/lark/installations", s.localIntegrationList("lark"))
		api.Post("/workspaces/{id}/lark/install/begin", s.localIntegrationUnavailable("Lark device flow"))
		api.Get("/workspaces/{id}/lark/install/{sessionID}/status", s.localIntegrationUnavailable("Lark device flow"))
		api.Delete("/workspaces/{id}/lark/installations/{installationID}", s.deleteLocalIntegration("lark"))
		api.Get("/workspaces/{id}/slack/installations", s.localIntegrationList("slack"))
		api.Post("/workspaces/{id}/slack/install/byo", s.registerLocalIntegration("slack"))
		api.Delete("/workspaces/{id}/slack/installations/{installationID}", s.deleteLocalIntegration("slack"))
		api.Get("/workspaces/{id}/dingtalk/installations", s.localIntegrationList("dingtalk"))
		api.Post("/workspaces/{id}/dingtalk/install/byo", s.registerLocalIntegration("dingtalk"))
		api.Delete("/workspaces/{id}/dingtalk/installations/{installationID}", s.deleteLocalIntegration("dingtalk"))
		api.Get("/workspaces/{id}/dingtalk/groups", localObject("groups", []any{}, "group_discovery_supported", false))
		api.Get("/agents/{id}/dingtalk/groups", localObject("groups", []any{}, "group_discovery_supported", false))
		api.Delete("/workspaces/{id}/dingtalk/installations/{installationID}/groups/{groupID}", localNoContent)
		api.Get("/workspaces/{id}/wecom/installations", s.localIntegrationList("wecom"))
		api.Post("/workspaces/{id}/wecom/install/byo", s.registerLocalIntegration("wecom"))
		api.Delete("/workspaces/{id}/wecom/installations/{installationID}", s.deleteLocalIntegration("wecom"))
		api.Get("/workspaces/{id}/telegram/installations", s.localIntegrationList("telegram"))
		api.Post("/workspaces/{id}/telegram/install", s.registerLocalIntegration("telegram"))
		api.Delete("/workspaces/{id}/telegram/installations/{installationID}", s.deleteLocalIntegration("telegram"))
		for _, path := range []string{"/lark/binding/redeem", "/slack/binding/redeem", "/dingtalk/binding/redeem", "/wecom/binding/redeem", "/telegram/binding/redeem", "/integrations/composio/connect/init"} {
			api.Post(path, s.localIntegrationUnavailable("Integration binding"))
		}
		api.Get("/integrations/composio/toolkits", s.localIntegrationUnavailable("Composio"))
		api.Get("/integrations/composio/connections", emptyArray())
		api.Delete("/integrations/composio/connections/{installationID}", s.localIntegrationUnavailable("Composio"))
		api.Get("/assignee-frequency", s.assigneeFrequency)
		api.Get("/inbox", s.listInbox)
		api.Get("/inbox/archived", s.listInbox)
		api.Get("/inbox/unread-count", s.inboxCount)
		api.Get("/inbox/unread-summary", s.inboxCount)
		api.Post("/inbox/{id}/{action:read|unread|archive|unarchive}", s.inboxItemState)
		api.Post("/inbox/mark-all-read", s.inboxBulk)
		api.Post("/inbox/archive-all", s.inboxBulk)
		api.Post("/inbox/archive-all-read", s.inboxBulk)
		api.Post("/inbox/archive-completed", s.inboxBulk)
		api.Get("/notification-preferences", s.notificationPreferences)
		api.Patch("/notification-preferences", s.notificationPreferences)
		api.Get("/daemon/workspaces", s.listWorkspaces)
		api.Get("/daemon/workspaces/{id}/runtime-profiles", s.runtimeProfiles)
		api.Get("/daemon/workspaces/{id}/repos", emptyObject("workspace_id", LocalWorkspaceID, "repos", []any{}, "repos_version", "local"))
		api.Post("/daemon/register", s.daemonRegister)
		api.Post("/daemon/deregister", s.daemonDeregister)
		api.Post("/daemon/heartbeat", s.daemonHeartbeat)
		api.Post("/daemon/tasks/claim", s.daemonClaimTasks)
		api.Post("/daemon/runtimes/{runtimeID}/tasks/claim", s.daemonClaimTask)
		api.Post("/daemon/runtimes/{runtimeID}/recover-orphans", s.daemonRecoverOrphans)
		api.Post("/daemon/runtimes/{runtimeID}/update/{requestID}/result", s.reportRuntimeRequest)
		api.Post("/daemon/runtimes/{runtimeID}/models/{requestID}/result", s.reportRuntimeRequest)
		api.Post("/daemon/runtimes/{runtimeID}/local-skills/{requestID}/result", s.reportRuntimeRequest)
		api.Post("/daemon/runtimes/{runtimeID}/local-skills/import/{requestID}/result", s.reportRuntimeRequest)
		api.Post("/daemon/runtimes/{runtimeID}/tasks/{taskID}/prepare-lease", s.daemonTaskNoop)
		api.Post("/daemon/tasks/{taskID}/{action:start|complete|fail|wait-local-directory}", s.daemonTaskState)
		api.Post("/daemon/tasks/{taskID}/progress", s.daemonTaskProgress)
		api.Post("/daemon/tasks/{taskID}/messages", s.daemonTaskMessages)
		api.Post("/daemon/tasks/{taskID}/usage", s.daemonTaskUsage)
		api.Post("/daemon/tasks/{taskID}/session", s.daemonTaskSession)
		api.Get("/daemon/tasks/{taskID}/status", s.daemonTaskStatus)
		api.Post("/daemon/tasks/{taskID}/plugin-hooks", s.invokeAgentPluginHook)
		api.Get("/daemon/tasks/{taskID}/plugin-mcp/{contributionID}/credential", s.resolvePluginMCPCredential)

		api.Get("/projects", s.listProjects)
		api.Post("/projects", s.createProject)
		api.Get("/projects/search", s.searchProjects)
		api.Get("/projects/{id}", s.getProject)
		api.Put("/projects/{id}", s.updateProject)
		api.Delete("/projects/{id}", s.deleteProject)
		api.Get("/projects/{id}/resources", s.listProjectResources)
		api.Post("/projects/{id}/resources", s.createProjectResource)
		api.Put("/projects/{id}/resources/{resourceID}", s.updateProjectResource)
		api.Delete("/projects/{id}/resources/{resourceID}", s.deleteProjectResource)

		api.Get("/issues/grouped", s.listGroupedIssues)
		api.Post("/issues/table/groups", s.listIssueTableGroups)
		api.Post("/issues/table/rows", s.listIssueTableRows)
		api.Post("/issues/table/facets", s.listIssueTableFacets)
		api.Get("/issues/search", s.searchIssues)
		api.Get("/issues/child-progress", s.childProgress)
		api.Get("/issues/children", s.childrenByParents)
		api.Post("/issues/preview-trigger", s.previewIssueTrigger)
		api.Get("/issues", s.listIssues)
		api.Post("/issues/query", s.queryIssues)
		api.Post("/issues/quick-create", s.quickCreateIssue)
		api.Post("/issues", s.createIssue)
		api.Post("/issues/batch-update", s.batchUpdateIssues)
		api.Post("/issues/batch-delete", s.batchDeleteIssues)
		api.Get("/issues/{id}", s.getIssue)
		api.Get("/issues/{id}/session", s.getIssueSession)
		api.Post("/issues/{id}/session/continue", s.continueIssueSession)
		api.Post("/issues/{id}/session/messages", s.sendIssueSessionMessage)
		api.Post("/issues/{id}/session/stages", s.activateIssueSessionStage)
		api.Put("/issues/{id}", s.updateIssue)
		api.Post("/issues/{id}/move", s.moveIssue)
		api.Delete("/issues/{id}", s.deleteIssue)
		api.Get("/issues/{id}/children", s.listChildren)
		api.Get("/issues/{id}/comments", s.listComments)
		api.Post("/issues/{id}/comments", s.createComment)
		api.Post("/issues/{id}/comments/trigger-preview", s.previewCommentTriggers)
		api.Post("/comments/{id}/resolve", s.resolveComment)
		api.Delete("/comments/{id}/resolve", s.unresolveComment)
		api.Get("/issues/{id}/attachments", s.listIssueAttachments)
		api.Get("/issues/{id}/labels", s.resourceLabels)
		api.Post("/issues/{id}/labels", s.resourceLabels)
		api.Delete("/issues/{id}/labels/{labelID}", s.resourceLabels)
		api.Put("/issues/{id}/properties/{propertyID}", s.issuePropertyValue)
		api.Delete("/issues/{id}/properties/{propertyID}", s.issuePropertyValue)
		api.Post("/issues/{id}/reactions", s.reactions)
		api.Delete("/issues/{id}/reactions", s.reactions)
		api.Get("/issues/{id}/subscribers", s.issueSubscribers)
		api.Post("/issues/{id}/subscribe", s.issueSubscribers)
		api.Post("/issues/{id}/unsubscribe", s.issueSubscribers)
		api.Post("/issues/{id}/unsubscribe/subtree", s.issueSubscribers)
		api.Post("/issues/{id}/quick-actions/{quickActionID}/run", s.quickActionExecute)
		api.Post("/issues/{id}/quick-actions/{quickActionID}/render", s.quickActionExecute)
		api.Get("/issues/{id}/timeline", s.timeline)
		api.Get("/issues/{id}/active-task", s.activeTask)
		api.Get("/issues/{id}/task-runs", s.listTaskRuns)
		api.Post("/issues/{id}/tasks/{taskID}/cancel", s.cancelTask)
		api.Post("/issues/{id}/rerun", s.rerunIssue)
		api.Get("/issues/{id}/usage", s.issueUsage)
		api.Get("/issues/{id}/pull-requests", s.listIssuePullRequests)

		api.Get("/issue-statuses", s.listIssueStatuses)
		api.Post("/issue-statuses", s.createIssueStatus)
		api.Patch("/issue-statuses/reorder", s.reorderIssueStatuses)
		api.Patch("/issue-statuses/{id}", s.issueStatusItem)
		api.Delete("/issue-statuses/{id}", s.issueStatusItem)
		api.Get("/labels", s.listLabels)
		api.Post("/labels", s.createLabel)
		api.Get("/labels/{id}", s.labelItem)
		api.Put("/labels/{id}", s.labelItem)
		api.Delete("/labels/{id}", s.labelItem)
		api.Get("/properties", s.listProperties)
		api.Post("/properties", s.createProperty)
		api.Patch("/properties/{id}", s.propertyItem)
		api.Delete("/properties/{id}", s.propertyItem)
		api.Get("/issue-views", s.issueViews)
		api.Post("/issue-views", s.issueViews)
		api.Get("/issue-views/{id}", s.issueViewItem)
		api.Patch("/issue-views/{id}", s.issueViewItem)
		api.Delete("/issue-views/{id}", s.issueViewItem)
		api.Get("/issue-view-preferences", s.issueViewPreferences)
		api.Put("/issue-view-preferences", s.issueViewPreferences)
		api.Get("/pins", s.pins)
		api.Post("/pins", s.pins)
		api.Delete("/pins/{itemType}/{itemID}", s.deletePin)
		api.Put("/pins/reorder", s.reorderPins)
		api.Get("/quick-actions", s.quickActions)
		api.Post("/quick-actions", s.quickActions)
		api.Patch("/quick-actions/{id}", s.quickActionItem)
		api.Delete("/quick-actions/{id}", s.quickActionItem)
		api.Put("/comments/{commentID}", s.updateOrDeleteComment)
		api.Delete("/comments/{commentID}", s.updateOrDeleteComment)
		api.Get("/comments/{commentID}/sub-issue-preview", s.previewCommentSubIssue)
		api.Post("/comments/{commentID}/sub-issues", s.createCommentSubIssue)
		api.Post("/comments/{commentID}/reactions", s.reactions)
		api.Delete("/comments/{commentID}/reactions", s.reactions)
		api.Get("/agents", s.listPersistentAgents)
		api.Get("/agent-builder/sessions", s.agentBuilderSessions)
		api.Post("/agent-builder/sessions", s.agentBuilderSessions)
		api.Put("/agent-builder/sessions/{sessionID}/draft", s.agentBuilderDraft)
		api.Patch("/agent-builder/sessions/{sessionID}/runtime", s.agentBuilderRuntime)
		api.Post("/agents", s.createAgent)
		api.Post("/agents/mika", s.createMikaAgent)
		api.Get("/agents/{id}", s.getPersistentAgent)
		api.Get("/agents/{id}/tasks", s.projectedTasks)
		api.Post("/agents/{id}/cancel-tasks", s.cancelAgentTasks)
		api.Put("/agents/{id}", s.updateAgent)
		api.Post("/agents/{id}/archive", s.archiveAgent)
		api.Post("/agents/{id}/restore", s.restoreAgent)
		api.Get("/agents/{id}/env", s.agentEnv)
		api.Put("/agents/{id}/env", s.agentEnv)
		api.Get("/agents/{id}/mcp-servers", s.agentMCPServers)
		api.Post("/agents/{id}/mcp-servers", s.agentMCPServers)
		api.Put("/agents/{id}/mcp-servers/{serverID}/enabled", s.agentMCPServerItem)
		api.Delete("/agents/{id}/mcp-servers/{serverID}", s.agentMCPServerItem)
		api.Get("/agents/{id}/skills", s.agentSkills)
		api.Put("/agents/{id}/skills", s.agentSkills)
		api.Post("/agents/{id}/skills/add", s.agentSkills)
		api.Put("/agents/{id}/skills/{skillID}/enabled", s.agentSkillItem)
		api.Delete("/agents/{id}/skills/{skillID}", s.agentSkillItem)
		api.Put("/agents/{id}/runtime-skills/enabled", s.agentRuntimeSkills)
		api.Get("/agents/{id}/labels", s.resourceLabels)
		api.Post("/agents/{id}/labels", s.resourceLabels)
		api.Delete("/agents/{id}/labels/{labelID}", s.resourceLabels)
		api.Get("/skills", s.listSkills)
		api.Post("/skills", s.createSkill)
		api.Post("/skills/import", s.importSkill)
		api.Get("/skills/{id}", s.skillItem)
		api.Put("/skills/{id}", s.skillItem)
		api.Delete("/skills/{id}", s.skillItem)
		api.Post("/skills/{id}/refresh", s.refreshSkill)
		api.Get("/skills/{id}/labels", s.resourceLabels)
		api.Post("/skills/{id}/labels", s.resourceLabels)
		api.Delete("/skills/{id}/labels/{labelID}", s.resourceLabels)
		api.Get("/squads", s.listSquads)
		api.Post("/squads", s.createSquad)
		api.Get("/squads/{id}", s.squadItem)
		api.Put("/squads/{id}", s.squadItem)
		api.Delete("/squads/{id}", s.squadItem)
		api.Get("/squads/{id}/members", s.squadMembers)
		api.Post("/squads/{id}/members", s.squadMembers)
		api.Delete("/squads/{id}/members", s.squadMembers)
		api.Patch("/squads/{id}/members/role", s.squadMembers)
		api.Get("/squads/{id}/members/status", s.squadMemberStatus)
		api.Get("/autopilots", s.listAutomations)
		api.Post("/autopilots", s.createAutomation)
		api.Get("/autopilots/usage", s.automationUsage)
		api.Get("/autopilots/cron-preview", s.cronPreview)
		api.Get("/autopilots/{id}", s.automationItem)
		api.Patch("/autopilots/{id}", s.automationItem)
		api.Delete("/autopilots/{id}", s.automationItem)
		api.Post("/autopilots/{id}/trigger", s.triggerAutomation)
		api.Post("/autopilots/{id}/collaborators", s.localAutomationCollaborators)
		api.Delete("/autopilots/{id}/collaborators/{userID}", s.localAutomationCollaborators)
		api.Get("/autopilots/{id}/runs", s.listAutomationRuns)
		api.Get("/autopilots/{id}/runs/{runID}", s.getAutomationRun)
		api.Get("/autopilots/{id}/deliveries", s.listAutomationDeliveries)
		api.Get("/autopilots/{id}/deliveries/{deliveryID}", s.getAutomationDelivery)
		api.Post("/autopilots/{id}/deliveries/{deliveryID}/replay", s.replayAutomationDelivery)
		api.Post("/autopilots/{id}/triggers", s.createAutomationTrigger)
		api.Patch("/autopilots/{id}/triggers/{triggerID}", s.automationTriggerItem)
		api.Delete("/autopilots/{id}/triggers/{triggerID}", s.automationTriggerItem)
		api.Post("/autopilots/{id}/triggers/{triggerID}/rotate-webhook-token", s.rotateAutomationWebhook)
		api.Post("/webhooks/autopilots/{token}", s.inboundAutomationWebhook)
		api.Post("/webhooks/vcs/{connectionID}", s.receiveVCSWebhook)
		api.Get("/tasks/{taskID}/messages", s.listTaskMessages)
		api.Post("/tasks/{taskID}/cancel", s.cancelTaskByID)
		api.Post("/tasks/{taskID}/retry-source-context", s.retrySourceContextTask)
		api.Get("/runtimes", s.listRuntimes)
		api.Patch("/runtimes/{id}", s.runtimeItem)
		api.Delete("/runtimes/{id}", s.runtimeItem)
		api.Post("/runtimes/{id}/unbind-agents-and-delete", s.unbindAndDeleteRuntime)
		api.Post("/runtimes/{id}/update", s.createRuntimeRequest)
		api.Get("/runtimes/{id}/update/{requestID}", s.getRuntimeRequest)
		api.Post("/runtimes/{id}/models", s.createRuntimeRequest)
		api.Get("/runtimes/{id}/models/{requestID}", s.getRuntimeRequest)
		api.Post("/runtimes/{id}/local-skills", s.createRuntimeRequest)
		api.Get("/runtimes/{id}/local-skills/{requestID}", s.getRuntimeRequest)
		api.Post("/runtimes/{id}/local-skills/import", s.createRuntimeRequest)
		api.Get("/runtimes/{id}/local-skills/import/{requestID}", s.getRuntimeRequest)
		api.Get("/runtimes/{id}/usage", s.runtimeUsage)
		api.Get("/runtimes/{id}/activity", s.runtimeActivity)
		api.Get("/runtimes/{id}/usage/by-agent", s.runtimeUsageByAgent)
		api.Get("/runtimes/{id}/usage/by-hour", s.runtimeUsageByHour)
		api.Get("/workspaces/{id}/runtime-profiles", s.runtimeProfiles)
		api.Post("/workspaces/{id}/runtime-profiles", s.runtimeProfiles)
		api.Get("/workspaces/{id}/runtime-profiles/{profileID}", s.runtimeProfileItem)
		api.Patch("/workspaces/{id}/runtime-profiles/{profileID}", s.runtimeProfileItem)
		api.Put("/workspaces/{id}/runtime-profiles/{profileID}", s.runtimeProfileItem)
		api.Delete("/workspaces/{id}/runtime-profiles/{profileID}", s.runtimeProfileItem)
		api.Get("/dashboard/usage/daily", s.dashboardUsageDaily)
		api.Get("/dashboard/usage/by-agent", s.dashboardUsageByAgent)
		api.Get("/dashboard/agent-runtime", s.dashboardAgentRuntime)
		api.Get("/dashboard/runtime/daily", s.dashboardRuntimeDaily)
		api.Get("/dashboard/failures/daily", s.dashboardFailuresDaily)
		api.Get("/dashboard/failures/by-agent", s.dashboardFailuresByAgent)
		api.Get("/agent-activity-30d", s.agentActivity30d)
		api.Get("/agent-run-counts", s.agentRunCounts)
		api.Get("/agent-task-snapshot", s.projectedTasks)
		api.Get("/working-agents", s.workingAgents)
		api.Get("/chat/sessions", s.listChatSessions)
		api.Post("/chat/sessions", s.createChatSession)
		api.Get("/chat/sessions/{id}", s.chatSessionItem)
		api.Patch("/chat/sessions/{id}", s.chatSessionItem)
		api.Delete("/chat/sessions/{id}", s.chatSessionItem)
		api.Patch("/chat/sessions/{id}/pin", s.pinChatSession)
		api.Patch("/chat/sessions/{id}/archive", s.archiveChatSession)
		api.Post("/chat/sessions/{id}/read", s.markChatRead)
		api.Post("/chat/sessions/{id}/onboarding", s.startMikaOnboarding)
		api.Post("/chat/sessions/{id}/quick-actions/regenerate", s.regenerateChatQuickActions)
		api.Get("/chat/sessions/{id}/messages", s.chatMessages)
		api.Post("/chat/sessions/{id}/messages", s.chatMessages)
		api.Get("/chat/sessions/{id}/messages/page", s.chatMessagesPage)
		api.Get("/chat/sessions/{id}/pending-task", s.pendingChatTask)
		api.Post("/chat/sessions/{id}/queued-tasks/{taskID}/prioritize", s.prioritizeQueuedChatTask)
		api.Delete("/chat/sessions/{id}/queued-tasks", s.clearQueuedChatTasks)
		api.Get("/chat/sessions/{id}/draft-restores", s.listChatDraftRestores)
		api.Delete("/chat/sessions/{id}/draft-restores/{restoreID}", s.consumeChatDraftRestore)
		api.Get("/chat/pinned-agents", s.chatPinnedAgents)
		api.Post("/chat/pinned-agents", s.chatPinnedAgents)
		api.Delete("/chat/pinned-agents/{agentID}", s.unpinChatAgent)
		api.Get("/chat/pending-tasks", s.pendingChatTasks)
		api.Get("/chat/pending-tasks/has-any", s.pendingChatTasks)

		for _, path := range []string{"/cloud-runtime/nodes", "/cloud-subscriptions/entitlements", "/cloud-subscriptions/prices", "/cloud-subscriptions/summary", "/cloud-billing/balance", "/cloud-billing/price-tiers", "/cloud-billing/batches", "/cloud-billing/topups", "/cloud-billing/transactions"} {
			api.Get(path, s.cloudOnlyUnavailable)
		}
		api.Get("/cloud-billing/checkout-sessions/{id}", s.cloudOnlyUnavailable)
		for _, path := range []string{"/cloud-subscriptions/checkout-sessions", "/cloud-subscriptions/portal-sessions", "/cloud-subscriptions/seats/purchase-preview", "/cloud-subscriptions/seats/purchases", "/cloud-subscriptions/seats/reconcile", "/cloud-billing/checkout-sessions", "/cloud-billing/portal-sessions"} {
			api.Post(path, s.cloudOnlyUnavailable)
		}
	})
	return router
}

func localNoContent(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }

func localObject(values ...any) http.HandlerFunc { return emptyObject(values...) }

func emptyArray() http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, http.StatusOK, []any{}) }
}

func emptyObject(values ...any) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		result := map[string]any{}
		for index := 0; index+1 < len(values); index += 2 {
			result[fmt.Sprint(values[index])] = values[index+1]
		}
		writeJSON(w, http.StatusOK, result)
	}
}

func localRequestBoundary(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.TrimSpace(r.Header.Get("Origin")) != "" {
			writeError(w, http.StatusForbidden, errors.New("browser origins are not accepted by the local workbench"))
			return
		}
		if r.Method == http.MethodPost || r.Method == http.MethodPut || r.Method == http.MethodPatch {
			contentType := strings.ToLower(strings.TrimSpace(strings.Split(r.Header.Get("Content-Type"), ";")[0]))
			if r.Method == http.MethodPost && (r.URL.Path == "/api/upload-file" || strings.HasSuffix(r.URL.Path, "/plugins/packages")) {
				if contentType != "multipart/form-data" {
					writeError(w, http.StatusUnsupportedMediaType, errors.New("multipart/form-data is required"))
					return
				}
			} else if contentType != "application/json" {
				writeError(w, http.StatusUnsupportedMediaType, errors.New("application/json is required"))
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"status":      "ok",
		"mode":        "local",
		"storage":     "sqlite",
		"database_id": s.databaseID,
		"version":     s.version,
		"commit":      s.commit,
		"started_at":  s.startedAt,
	})
}

func (s *Server) appConfig(w http.ResponseWriter, r *http.Request) {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	baseURL := scheme + "://" + r.Host
	writeJSON(w, http.StatusOK, map[string]any{
		"cdn_domain":                            "",
		"cdn_signed":                            false,
		"allow_signup":                          false,
		"daemon_server_url":                     strings.Replace(baseURL, "http", "ws", 1) + "/ws/daemon",
		"daemon_app_url":                        baseURL,
		"workspace_creation_disabled":           true,
		"vcs_integration_available":             true,
		"local_worktree_supported":              true,
		"agent_conversation_starters_supported": true,
		"feature_flags": map[string]bool{
			"plugins_v1":        true,
			"composio_mcp_apps": false,
			"local_workbench":   true,
		},
		"server_version": s.version,
	})
}

func (s *Server) getMe(w http.ResponseWriter, r *http.Request) {
	identity, err := s.store.Identity(r.Context())
	if err != nil {
		s.internalError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, identity.Operator)
}

var localWebsocketUpgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		return strings.TrimSpace(r.Header.Get("Origin")) == ""
	},
}

func (s *Server) websocket(w http.ResponseWriter, r *http.Request) {
	connection, err := localWebsocketUpgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer func() {
		if err := connection.Close(); err != nil {
			s.logger.Debug("close local websocket", "error", err)
		}
	}()
	connection.SetReadLimit(1 << 20)
	s.hub.add(connection)
	defer s.hub.remove(connection)
	for {
		if _, _, err := connection.ReadMessage(); err != nil {
			return
		}
	}
}

func (s *Server) internalError(w http.ResponseWriter, err error) {
	s.logger.Error("local workbench request failed", "error", err)
	writeJSON(w, http.StatusInternalServerError, map[string]string{
		"error": "local data operation failed",
	})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(value); err != nil {
		slog.Debug("encode local response", "error", err)
	}
}

func writeError(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]any{"error": err.Error()})
}

func decodeBody(r *http.Request, target any) error {
	decoder := json.NewDecoder(io.LimitReader(r.Body, 2<<20))
	return decoder.Decode(target)
}
