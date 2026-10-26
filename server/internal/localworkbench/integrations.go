package localworkbench

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/integrations/vcs"
)

var integrationSecretKeys = map[string]bool{"access_token": true, "bot_token": true, "app_token": true, "client_secret": true, "secret": true}

func (s *Server) localIntegrationList(kind string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ws, err := s.workspaceFor(r)
		if err != nil {
			s.respondStoreError(w, err)
			return
		}
		rows, err := s.store.db.QueryContext(r.Context(), `SELECT id, agent_id, display_name, public_data, status, created_at, updated_at FROM local_integrations WHERE workspace_id=? AND kind=? ORDER BY updated_at DESC, id`, ws.ID, kind)
		if err != nil {
			s.fail(w, err)
			return
		}
		items := []map[string]any{}
		for rows.Next() {
			var id, displayName, raw, status, createdAt, updatedAt string
			var agentID sql.NullString
			if err := rows.Scan(&id, &agentID, &displayName, &raw, &status, &createdAt, &updatedAt); err != nil {
				_ = rows.Close()
				s.fail(w, err)
				return
			}
			item, _ := decodeJSONMap(raw)
			item["id"], item["workspace_id"], item["status"] = id, ws.ID, status
			item["display_name"], item["installed_at"], item["created_at"], item["updated_at"] = displayName, createdAt, createdAt, updatedAt
			item["installer_user_id"] = LocalOperatorID
			if agentID.Valid {
				item["agent_id"] = agentID.String
			}
			items = append(items, item)
		}
		_ = rows.Close()
		if kind == "vcs" {
			writeJSON(w, http.StatusOK, map[string]any{"connections": items, "available": true, "configured": true, "can_manage": true})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"installations": items, "configured": true, "install_supported": true})
	}
}

func (s *Server) registerLocalIntegration(kind string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ws, err := s.workspaceFor(r)
		if err != nil {
			s.respondStoreError(w, err)
			return
		}
		input := map[string]any{}
		if err := decodeBody(r, &input); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		agentID := strings.TrimSpace(r.URL.Query().Get("agent_id"))
		if agentID == "" {
			agentID = strings.TrimSpace(stringFromAny(input["agent_id"]))
		}
		if kind != "vcs" {
			if agentID == "" {
				writeError(w, http.StatusBadRequest, errors.New("agent_id is required"))
				return
			}
			var exists int
			if err := s.store.db.QueryRowContext(r.Context(), `SELECT 1 FROM agents WHERE id=? AND workspace_id=? AND archived_at IS NULL`, agentID, ws.ID).Scan(&exists); err != nil {
				s.respondStoreError(w, err)
				return
			}
		}
		public, secrets := map[string]any{}, map[string]any{}
		for key, value := range input {
			if integrationSecretKeys[key] {
				if strings.TrimSpace(stringFromAny(value)) == "" {
					writeError(w, http.StatusBadRequest, errors.New(key+" is required"))
					return
				}
				secrets[key] = value
			} else {
				public[key] = value
			}
		}
		if len(secrets) == 0 {
			writeError(w, http.StatusBadRequest, errors.New("integration credentials are required"))
			return
		}
		id, stamp := uuid.NewString(), now()
		displayName := firstNonEmpty(stringFromAny(public["name"]), stringFromAny(public["provider"]), kind)
		if kind == "vcs" {
			provider, instance := stringFromAny(public["provider"]), strings.TrimRight(stringFromAny(public["instance_url"]), "/")
			if provider == "" || instance == "" {
				writeError(w, http.StatusBadRequest, errors.New("provider and instance_url are required"))
				return
			}
			adapter, ok := vcs.For(provider)
			parsed, parseErr := url.Parse(instance)
			if !ok || parseErr != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
				writeError(w, http.StatusBadRequest, errors.New("unsupported provider or invalid instance_url"))
				return
			}
			account, validationErr := adapter.ValidateToken(r.Context(), instance, stringFromAny(secrets["access_token"]))
			if validationErr != nil {
				status := http.StatusBadGateway
				if errors.Is(validationErr, vcs.ErrUnauthorized) {
					status = http.StatusBadRequest
				}
				writeError(w, status, validationErr)
				return
			}
			public["provider"], public["instance_url"], public["account_login"] = provider, instance, account.Login
			public["webhook_path"] = "/api/webhooks/vcs/" + id
			baseURL, err := localCallbackBaseURL(r)
			if err != nil {
				writeError(w, http.StatusFailedDependency, err)
				return
			}
			public["webhook_url"] = baseURL + stringFromAny(public["webhook_path"])
			secrets["webhook_secret"] = uuid.NewString()
		}
		if kind == "telegram" {
			token := stringFromAny(secrets["bot_token"])
			public["bot_id"] = strings.Split(token, ":")[0]
			public["bot_username"] = firstNonEmpty(stringFromAny(public["bot_username"]), "local_bot")
		}
		if kind == "wecom" && stringFromAny(public["bot_id"]) == "" {
			writeError(w, http.StatusBadRequest, errors.New("bot_id is required"))
			return
		}
		publicRaw, _ := json.Marshal(public)
		secretRaw, _ := json.Marshal(secrets)
		if _, err := s.store.db.ExecContext(r.Context(), `INSERT INTO local_integrations(id, workspace_id, kind, agent_id, display_name, public_data, secret_data, status, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, 'configured', ?, ?)`, id, ws.ID, kind, nullable(agentID), displayName, string(publicRaw), string(secretRaw), stamp, stamp); err != nil {
			s.fail(w, err)
			return
		}
		public["id"], public["workspace_id"], public["agent_id"], public["status"], public["installer_user_id"] = id, ws.ID, nullable(agentID), "configured", LocalOperatorID
		public["installed_at"], public["created_at"], public["updated_at"] = stamp, stamp, stamp
		if kind == "vcs" {
			public["webhook_secret"] = secrets["webhook_secret"]
		}
		writeJSON(w, http.StatusCreated, public)
	}
}

func (s *Server) receiveVCSWebhook(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "connectionID")
	var publicRaw, secretRaw string
	if err := s.store.db.QueryRowContext(r.Context(), `SELECT public_data,secret_data FROM local_integrations WHERE id=? AND workspace_id=? AND kind='vcs'`, id, LocalWorkspaceID).Scan(&publicRaw, &secretRaw); err != nil {
		s.respondStoreError(w, err)
		return
	}
	public, _ := decodeJSONMap(publicRaw)
	secrets, _ := decodeJSONMap(secretRaw)
	body, err := io.ReadAll(io.LimitReader(r.Body, maxLocalWebhookBody+1))
	if err != nil || len(body) > maxLocalWebhookBody {
		writeError(w, http.StatusRequestEntityTooLarge, errors.New("VCS webhook payload exceeds 256 KiB"))
		return
	}
	adapter, ok := vcs.For(stringFromAny(public["provider"]))
	if !ok {
		writeError(w, http.StatusFailedDependency, errors.New("configured VCS provider is unsupported"))
		return
	}
	secret := stringFromAny(secrets["webhook_secret"])
	signatureStatus := "invalid"
	if adapter.VerifySignature(secret, r.Header, body) {
		signatureStatus = "valid"
	}
	event := firstNonEmpty(r.Header.Get("X-Gitea-Event"), r.Header.Get("X-GitHub-Event"), r.Header.Get("X-Gitlab-Event"), "generic")
	dedupe, _ := webhookDedupe(r.Header)
	storageDedupe := dedupe
	deliveryID, stamp, status := uuid.NewString(), now(), "accepted"
	responseStatus := http.StatusAccepted
	var reason any
	if signatureStatus != "valid" {
		status, responseStatus, reason = "rejected", http.StatusUnauthorized, "invalid_signature"
		if storageDedupe != "" {
			storageDedupe = "rejected:" + storageDedupe
		}
	}
	result, err := s.store.db.ExecContext(r.Context(), `INSERT OR IGNORE INTO local_integration_deliveries(id,integration_id,event,dedupe_key,signature_status,status,raw_body,error,created_at) VALUES(?,?,?,?,?,?,?,?,?)`, deliveryID, id, event, nullable(storageDedupe), signatureStatus, status, body, reason, stamp)
	if err != nil {
		s.fail(w, err)
		return
	}
	if inserted, _ := result.RowsAffected(); inserted == 0 {
		var existing string
		_ = s.store.db.QueryRowContext(r.Context(), `SELECT id FROM local_integration_deliveries WHERE integration_id=? AND dedupe_key=?`, id, storageDedupe).Scan(&existing)
		writeJSON(w, http.StatusOK, map[string]any{"status": "duplicate", "delivery_id": existing})
		return
	}
	if signatureStatus == "valid" {
		processErr := s.processVCSWebhook(r.Context(), id, stringFromAny(public["provider"]), adapter, r.Header, body)
		if processErr != nil {
			// A failed business projection is retryable. Release the provider's
			// dedupe identity so the same delivery can be processed again instead
			// of being acknowledged as a false duplicate.
			tx, err := s.store.db.BeginTx(r.Context(), nil)
			if err != nil {
				s.fail(w, err)
				return
			}
			defer func() { _ = tx.Rollback() }()
			result, err := tx.ExecContext(r.Context(), `UPDATE local_integration_deliveries SET status='failed',error=?,dedupe_key=NULL WHERE id=?`, processErr.Error(), deliveryID)
			if err != nil {
				s.fail(w, err)
				return
			}
			updated, err := result.RowsAffected()
			if err != nil {
				s.fail(w, err)
				return
			}
			if updated == 0 {
				s.fail(w, sql.ErrNoRows)
				return
			}
			notificationItem, notificationInserted, err := insertLocalNotificationTx(r.Context(), tx, localNotificationInput{
				Type: "integration_failed", Severity: "action_required", SourceKey: "integration-delivery:" + deliveryID + ":failed",
				Title: "Integration delivery failed: " + stringFromAny(public["provider"]), Body: processErr.Error(),
				Details: map[string]string{"destination": "integration", "integration_id": id, "delivery_id": deliveryID},
			})
			if err != nil {
				s.fail(w, err)
				return
			}
			if err := tx.Commit(); err != nil {
				s.fail(w, err)
				return
			}
			if notificationInserted {
				s.hub.broadcast("inbox:new", map[string]any{"item": notificationItem})
			}
			writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"status": "failed", "delivery_id": deliveryID, "event": event, "provider": public["provider"], "reason": processErr.Error()})
			return
		}
		status = "processed"
		_, _ = s.store.db.ExecContext(r.Context(), `UPDATE local_integration_deliveries SET status=? WHERE id=?`, status, deliveryID)
	}
	writeJSON(w, responseStatus, map[string]any{"status": status, "delivery_id": deliveryID, "event": event, "provider": public["provider"], "reason": reason})
}

var localIssueIdentifier = regexp.MustCompile(`(?i)\b[A-Z][A-Z0-9]+-[0-9]+\b`)
var localClosingIdentifier = regexp.MustCompile(`(?i)\b(?:close[sd]?|fix(?:e[sd])?|resolve[sd]?)\s+#?([A-Z][A-Z0-9]+-[0-9]+)\b`)

func parseVCSTime(value string) string {
	if value != "" {
		if parsed, err := time.Parse(time.RFC3339Nano, value); err == nil {
			return parsed.UTC().Format(time.RFC3339Nano)
		}
	}
	return now()
}

func (s *Server) processVCSWebhook(ctx context.Context, connectionID, provider string, adapter vcs.Provider, headers http.Header, body []byte) error {
	switch adapter.EventKind(headers) {
	case vcs.EventPullRequest:
		event, err := adapter.ParsePullRequest(body)
		if err != nil {
			return err
		}
		return s.mirrorLocalVCSPullRequest(ctx, connectionID, provider, event)
	case vcs.EventCIStatus:
		event, err := adapter.ParseCIStatus(body)
		if err != nil {
			return err
		}
		if strings.TrimSpace(event.SHA) == "" || strings.TrimSpace(event.State) == "" {
			return errors.New("VCS status payload is missing sha or state")
		}
		stamp := parseVCSTime(event.UpdatedAt)
		_, err = s.store.db.ExecContext(ctx, `INSERT INTO vcs_commit_statuses(connection_id,sha,context,state,target_url,description,updated_at)
			VALUES(?,?,?,?,?,?,?) ON CONFLICT(connection_id,sha,context) DO UPDATE SET state=excluded.state,target_url=excluded.target_url,description=excluded.description,updated_at=excluded.updated_at
			WHERE excluded.updated_at>=vcs_commit_statuses.updated_at`, connectionID, event.SHA, event.Context, event.State, nullable(event.TargetURL), nullable(event.Description), stamp)
		if err == nil {
			s.hub.broadcast("pull_request:updated", map[string]any{"head_sha": event.SHA})
		}
		return err
	default:
		return nil
	}
}

func (s *Server) mirrorLocalVCSPullRequest(ctx context.Context, connectionID, provider string, event vcs.PullRequestEvent) error {
	if event.RepoOwner == "" || event.RepoName == "" || event.Number == 0 {
		return errors.New("VCS pull request payload is missing repository identity")
	}
	tx, err := s.store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	prID := uuid.NewString()
	createdAt, updatedAt := parseVCSTime(event.CreatedAt), parseVCSTime(event.UpdatedAt)
	result, err := tx.ExecContext(ctx, `INSERT INTO vcs_pull_requests(id,workspace_id,connection_id,provider,repo_owner,repo_name,pr_number,title,state,html_url,branch,head_sha,author_login,author_avatar_url,merged_at,closed_at,pr_created_at,pr_updated_at,additions,deletions,changed_files)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(connection_id,repo_owner,repo_name,pr_number) DO UPDATE SET title=excluded.title,state=excluded.state,html_url=excluded.html_url,branch=excluded.branch,head_sha=excluded.head_sha,author_login=excluded.author_login,author_avatar_url=excluded.author_avatar_url,merged_at=excluded.merged_at,closed_at=excluded.closed_at,pr_created_at=excluded.pr_created_at,pr_updated_at=excluded.pr_updated_at,additions=excluded.additions,deletions=excluded.deletions,changed_files=excluded.changed_files,
		snapshot_head_sha=CASE WHEN COALESCE(excluded.head_sha,'')<>COALESCE(vcs_pull_requests.head_sha,'') THEN NULL ELSE vcs_pull_requests.snapshot_head_sha END,
		checks_rollup_state=CASE WHEN COALESCE(excluded.head_sha,'')<>COALESCE(vcs_pull_requests.head_sha,'') THEN NULL ELSE vcs_pull_requests.checks_rollup_state END,
		api_mergeable=CASE WHEN COALESCE(excluded.head_sha,'')<>COALESCE(vcs_pull_requests.head_sha,'') THEN NULL ELSE vcs_pull_requests.api_mergeable END
		WHERE excluded.pr_updated_at>=vcs_pull_requests.pr_updated_at`, prID, LocalWorkspaceID, connectionID, provider, event.RepoOwner, event.RepoName, event.Number, event.Title, event.State, event.HTMLURL, nullable(event.Branch), nullable(event.HeadSHA), nullable(event.AuthorLogin), nullable(event.AuthorAvatarURL), nullable(event.MergedAt), nullable(event.ClosedAt), createdAt, updatedAt, event.Additions, event.Deletions, event.ChangedFiles)
	if err != nil {
		return err
	}
	applied, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if applied == 0 {
		// The durable PR projection is newer than this delivery. Do not let an
		// out-of-order webhook rewrite links or complete an issue from stale data.
		return tx.Commit()
	}
	if err := tx.QueryRowContext(ctx, `SELECT id FROM vcs_pull_requests WHERE connection_id=? AND repo_owner=? AND repo_name=? AND pr_number=?`, connectionID, event.RepoOwner, event.RepoName, event.Number).Scan(&prID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM issue_vcs_pull_requests WHERE pull_request_id=?`, prID); err != nil {
		return err
	}

	text := strings.Join([]string{event.Title, event.Body, event.Branch}, "\n")
	closing := map[string]bool{}
	for _, match := range localClosingIdentifier.FindAllStringSubmatch(event.Title+"\n"+event.Body, -1) {
		if len(match) > 1 {
			closing[strings.ToUpper(match[1])] = true
		}
	}
	seen := map[string]bool{}
	linkedIssueIDs := []string{}
	completedIssueIDs := []string{}
	for _, identifier := range localIssueIdentifier.FindAllString(text, -1) {
		identifier = strings.ToUpper(identifier)
		if seen[identifier] {
			continue
		}
		seen[identifier] = true
		var issueID string
		err := tx.QueryRowContext(ctx, `SELECT i.id FROM issues i JOIN workspaces w ON w.id=i.workspace_id WHERE i.workspace_id=? AND upper(w.issue_prefix || '-' || i.number)=? LIMIT 1`, LocalWorkspaceID, identifier).Scan(&issueID)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO issue_vcs_pull_requests(issue_id,pull_request_id,close_intent,reference_only,created_at) VALUES(?,?,?,?,?)`, issueID, prID, closing[identifier], false, now())
		if err != nil {
			return err
		}
		linkedIssueIDs = append(linkedIssueIDs, issueID)
	}
	if event.State == "merged" {
		for _, issueID := range linkedIssueIDs {
			var closeIntent, openCount int
			if err := tx.QueryRowContext(ctx, `SELECT
				COALESCE(MAX(CASE WHEN l.close_intent=1 AND p.state='merged' THEN 1 ELSE 0 END),0),
				COALESCE(SUM(CASE WHEN p.state IN ('open','draft') THEN 1 ELSE 0 END),0)
				FROM issue_vcs_pull_requests l JOIN vcs_pull_requests p ON p.id=l.pull_request_id WHERE l.issue_id=? AND l.reference_only=0`, issueID).Scan(&closeIntent, &openCount); err != nil {
				return err
			}
			if closeIntent > 0 && openCount == 0 {
				stamp := now()
				result, err := tx.ExecContext(ctx, `UPDATE issues SET status='done',duplicate_of_issue_id=NULL,revision=revision+1,last_activity_at=?,updated_at=? WHERE id=? AND workspace_id=? AND status NOT IN ('done','cancelled')`, stamp, stamp, issueID, LocalWorkspaceID)
				if err != nil {
					return err
				}
				if changed, _ := result.RowsAffected(); changed == 1 {
					completedIssueIDs = append(completedIssueIDs, issueID)
				}
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	for _, issueID := range completedIssueIDs {
		s.hub.broadcast("issue:updated", map[string]any{"issue_id": issueID, "status": "done"})
	}
	s.hub.broadcast("pull_request:updated", map[string]any{"pull_request_id": prID, "linked_issue_ids": linkedIssueIDs})
	return nil
}

func (s *Server) listIssuePullRequests(w http.ResponseWriter, r *http.Request) {
	ws, err := s.workspaceFor(r)
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	issueID := chi.URLParam(r, "id")
	if _, err := s.store.GetIssue(r.Context(), ws.ID, issueID); err != nil {
		s.respondStoreError(w, err)
		return
	}
	rows, err := s.store.db.QueryContext(r.Context(), `SELECT p.id,p.provider,p.workspace_id,p.repo_owner,p.repo_name,p.pr_number,p.title,p.state,p.html_url,p.branch,p.author_login,p.author_avatar_url,p.merged_at,p.closed_at,p.pr_created_at,p.pr_updated_at,p.additions,p.deletions,p.changed_files,
		COALESCE(SUM(CASE WHEN cs.state='passed' THEN 1 ELSE 0 END),0),COALESCE(SUM(CASE WHEN cs.state='failed' THEN 1 ELSE 0 END),0),COALESCE(SUM(CASE WHEN cs.state='pending' THEN 1 ELSE 0 END),0),COUNT(cs.sha)
		FROM issue_vcs_pull_requests l JOIN vcs_pull_requests p ON p.id=l.pull_request_id
		LEFT JOIN vcs_commit_statuses cs ON cs.connection_id=p.connection_id AND cs.sha=p.head_sha
		WHERE l.issue_id=? AND l.reference_only=0 GROUP BY p.id ORDER BY p.pr_updated_at DESC,p.id`, issueID)
	if err != nil {
		s.fail(w, err)
		return
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var id, provider, workspaceID, owner, repo, title, state, htmlURL, createdAt, updatedAt string
		var branch, author, avatar, mergedAt, closedAt sql.NullString
		var number, additions, deletions, changed, passed, failed, pending, total int
		if err := rows.Scan(&id, &provider, &workspaceID, &owner, &repo, &number, &title, &state, &htmlURL, &branch, &author, &avatar, &mergedAt, &closedAt, &createdAt, &updatedAt, &additions, &deletions, &changed, &passed, &failed, &pending, &total); err != nil {
			s.fail(w, err)
			return
		}
		conclusion := any(nil)
		if total > 0 {
			if failed > 0 {
				conclusion = "failed"
			} else if pending > 0 {
				conclusion = "pending"
			} else {
				conclusion = "passed"
			}
		}
		items = append(items, map[string]any{"id": id, "provider": provider, "workspace_id": workspaceID, "repo_owner": owner, "repo_name": repo, "number": number, "title": title, "state": state, "html_url": htmlURL, "branch": nullableString(branch), "author_login": nullableString(author), "author_avatar_url": nullableString(avatar), "merged_at": nullableString(mergedAt), "closed_at": nullableString(closedAt), "pr_created_at": createdAt, "pr_updated_at": updatedAt, "checks_conclusion": conclusion, "checks_total": total, "checks_passed": passed, "checks_failed": failed, "checks_pending": pending, "checks_running": pending, "failed_check_names": []string{}, "additions": additions, "deletions": deletions, "changed_files": changed})
	}
	writeJSON(w, http.StatusOK, map[string]any{"pull_requests": items})
}

func (s *Server) deleteLocalIntegration(kind string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ws, err := s.workspaceFor(r)
		if err != nil {
			s.respondStoreError(w, err)
			return
		}
		id := chi.URLParam(r, "installationID")
		tx, err := s.store.db.BeginTx(r.Context(), nil)
		if err != nil {
			s.fail(w, err)
			return
		}
		defer func() { _ = tx.Rollback() }()
		if kind == "vcs" {
			for _, query := range []string{
				`DELETE FROM issue_vcs_pull_requests WHERE pull_request_id IN (SELECT id FROM vcs_pull_requests WHERE connection_id=?)`,
				`DELETE FROM vcs_commit_statuses WHERE connection_id=?`,
				`DELETE FROM vcs_pull_requests WHERE connection_id=?`,
				`DELETE FROM local_integration_deliveries WHERE integration_id=?`,
			} {
				if _, err := tx.ExecContext(r.Context(), query, id); err != nil {
					s.fail(w, err)
					return
				}
			}
		}
		result, err := tx.ExecContext(r.Context(), `DELETE FROM local_integrations WHERE id=? AND workspace_id=? AND kind=?`, id, ws.ID, kind)
		if err != nil {
			s.fail(w, err)
			return
		}
		if count, _ := result.RowsAffected(); count != 1 {
			s.respondStoreError(w, sql.ErrNoRows)
			return
		}
		if err := tx.Commit(); err != nil {
			s.fail(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func (s *Server) rotateVCSWebhook(w http.ResponseWriter, r *http.Request) {
	ws, err := s.workspaceFor(r)
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	id := chi.URLParam(r, "installationID")
	var publicRaw, secretRaw string
	if err := s.store.db.QueryRowContext(r.Context(), `SELECT public_data, secret_data FROM local_integrations WHERE id=? AND workspace_id=? AND kind='vcs'`, id, ws.ID).Scan(&publicRaw, &secretRaw); err != nil {
		s.respondStoreError(w, err)
		return
	}
	public, _ := decodeJSONMap(publicRaw)
	secrets, _ := decodeJSONMap(secretRaw)
	baseURL, err := localCallbackBaseURL(r)
	if err != nil {
		writeError(w, http.StatusFailedDependency, err)
		return
	}
	public["webhook_path"] = "/api/webhooks/vcs/" + id
	public["webhook_url"] = baseURL + stringFromAny(public["webhook_path"])
	secrets["webhook_secret"] = uuid.NewString()
	publicRaw, _ = encodeJSONMap(public)
	secretRaw, _ = encodeJSONMap(secrets)
	if _, err := s.store.db.ExecContext(r.Context(), `UPDATE local_integrations SET public_data=?, secret_data=?, updated_at=? WHERE id=?`, publicRaw, secretRaw, now(), id); err != nil {
		s.fail(w, err)
		return
	}
	public["id"], public["workspace_id"], public["webhook_secret"] = id, ws.ID, secrets["webhook_secret"]
	writeJSON(w, http.StatusOK, public)
}

func (s *Server) localIntegrationUnavailable(name string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusFailedDependency, map[string]any{"error": name + " requires an external provider connection that is not configured for this local installation", "code": "local_integration_not_configured"})
	}
}
