package localworkbench

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
)

type localSourceContext struct {
	Snapshot map[string]any
	Token    string
	Issue    Issue
}

func (s *Server) localSourceContextDetail(r *http.Request, issue Issue) (map[string]any, error) {
	var id, sourceIssueID, anchorID, token, rawSnapshot, capturedAt string
	err := s.store.db.QueryRowContext(r.Context(), `SELECT id,source_issue_id,anchor_comment_id,capture_token,snapshot,created_at FROM issue_source_contexts WHERE issue_id=?`, issue.ID).Scan(&id, &sourceIssueID, &anchorID, &token, &rawSnapshot, &capturedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	snapshot := map[string]any{}
	if err := json.Unmarshal([]byte(rawSnapshot), &snapshot); err != nil {
		return nil, fmt.Errorf("decode source context snapshot: %w", err)
	}
	detail := map[string]any{
		"id": id, "version": 1, "usage": "read_only_historical_background", "captured_at": capturedAt,
		"display_state": "unavailable", "source_issue_state": "unavailable", "comment_thread_state": "unavailable",
		"anchor_comment_state": "unavailable", "can_open_current_source": false, "snapshot": snapshot,
	}
	var identifier string
	if err := s.store.db.QueryRowContext(r.Context(), `SELECT w.issue_prefix || '-' || i.number FROM issues i JOIN workspaces w ON w.id=i.workspace_id WHERE i.id=? AND i.workspace_id=?`, sourceIssueID, issue.WorkspaceID).Scan(&identifier); errors.Is(err, sql.ErrNoRows) {
		detail["display_state"], detail["source_issue_state"] = "deleted", "deleted"
		return detail, nil
	} else if err != nil {
		return nil, err
	}
	detail["source_issue_state"] = "changed"
	var anchorExists int
	if err := s.store.db.QueryRowContext(r.Context(), `SELECT EXISTS(SELECT 1 FROM comments WHERE id=? AND issue_id=?)`, anchorID, sourceIssueID).Scan(&anchorExists); err != nil {
		return nil, err
	}
	if anchorExists == 0 {
		detail["display_state"], detail["comment_thread_state"], detail["anchor_comment_state"] = "changed", "changed", "deleted"
		return detail, nil
	}
	detail["anchor_comment_state"], detail["can_open_current_source"] = "available", true
	detail["current_source"] = map[string]any{"issue_id": sourceIssueID, "identifier": identifier, "anchor_comment_id": anchorID}
	current, err := s.buildLocalSourceContext(r, anchorID)
	if err != nil {
		detail["display_state"], detail["comment_thread_state"] = "unavailable", "unavailable"
		return detail, nil
	}
	if current.Token == token {
		detail["display_state"], detail["source_issue_state"], detail["comment_thread_state"] = "unchanged", "unchanged", "unchanged"
	} else {
		detail["display_state"], detail["comment_thread_state"] = "changed", "changed"
		detail["change_reasons"] = []string{"source_context_changed"}
	}
	return detail, nil
}

func sourceAttachment(ownerType, ownerID string, item Attachment) map[string]any {
	return map[string]any{
		"id": item.ID, "owner_type": ownerType, "owner_id": ownerID,
		"filename": item.Filename, "content_type": item.ContentType,
		"size_bytes": item.SizeBytes, "created_at": item.CreatedAt,
	}
}

func (s *Server) buildLocalSourceContext(r *http.Request, anchorID string) (localSourceContext, error) {
	ws, err := s.workspaceFor(r)
	if err != nil {
		return localSourceContext{}, err
	}
	var issueID string
	if err := s.store.db.QueryRowContext(r.Context(), `SELECT c.issue_id FROM comments c JOIN issues i ON i.id=c.issue_id WHERE c.id=? AND i.workspace_id=?`, anchorID, ws.ID).Scan(&issueID); err != nil {
		return localSourceContext{}, err
	}
	issue, err := s.store.GetIssue(r.Context(), ws.ID, issueID)
	if err != nil {
		return localSourceContext{}, err
	}
	comments, err := s.store.ListComments(r.Context(), ws.ID, issue.ID)
	if err != nil {
		return localSourceContext{}, err
	}
	byID := make(map[string]Comment, len(comments))
	anchorIndex := -1
	for index, comment := range comments {
		byID[comment.ID] = comment
		if comment.ID == anchorID {
			anchorIndex = index
		}
	}
	if anchorIndex < 0 {
		return localSourceContext{}, sql.ErrNoRows
	}
	rootOf := func(comment Comment) (string, bool) {
		seen := map[string]bool{}
		for comment.ParentID != nil {
			if seen[comment.ID] {
				return "", false
			}
			seen[comment.ID] = true
			parent, ok := byID[*comment.ParentID]
			if !ok {
				return "", false
			}
			comment = parent
		}
		return comment.ID, true
	}
	rootID, ok := rootOf(comments[anchorIndex])
	if !ok {
		return localSourceContext{}, errors.New("comment thread is invalid")
	}
	issueAttachments, err := s.store.listAttachments(r.Context(), ws.ID, issue.ID, "")
	if err != nil {
		return localSourceContext{}, err
	}
	issueFiles := []any{}
	for _, item := range issueAttachments {
		if item.CommentID == nil {
			issueFiles = append(issueFiles, sourceAttachment("issue", issue.ID, item))
		}
	}
	thread := []any{}
	semanticComments := []any{}
	textBytes, attachmentCount, attachmentBytes := 0, len(issueFiles), int64(0)
	for _, raw := range issueFiles {
		attachmentBytes += int64(raw.(map[string]any)["size_bytes"].(int64))
	}
	for index := 0; index <= anchorIndex; index++ {
		comment := comments[index]
		root, valid := rootOf(comment)
		if !valid {
			return localSourceContext{}, errors.New("comment thread is invalid")
		}
		if root != rootID {
			continue
		}
		files := []any{}
		for _, item := range comment.Attachments {
			files = append(files, sourceAttachment("comment", comment.ID, item))
			attachmentCount++
			attachmentBytes += item.SizeBytes
		}
		authorName := "Local Operator"
		if comment.AuthorType == "agent" {
			_ = s.store.db.QueryRowContext(r.Context(), `SELECT name FROM agents WHERE id=?`, comment.AuthorID).Scan(&authorName)
		}
		thread = append(thread, map[string]any{
			"id": comment.ID, "parent_id": comment.ParentID, "type": comment.Type,
			"content": comment.Content, "author": map[string]any{"type": comment.AuthorType, "id": comment.AuthorID, "name": authorName},
			"created_at": comment.CreatedAt, "updated_at": comment.UpdatedAt,
			"revision": comment.Revision, "attachments": files,
		})
		semanticComments = append(semanticComments, map[string]any{"id": comment.ID, "parent_id": comment.ParentID, "type": comment.Type, "content": comment.Content, "attachments": files})
		textBytes += len([]byte(comment.Content))
	}
	issueSnapshot := map[string]any{
		"id": issue.ID, "identifier": issue.Identifier, "number": issue.Number,
		"title": issue.Title, "description": issue.Description, "created_at": issue.CreatedAt,
		"updated_at": issue.UpdatedAt, "revision": issue.Revision, "attachments": issueFiles,
	}
	semantic, _ := json.Marshal(map[string]any{"issue": map[string]any{"id": issue.ID, "title": issue.Title, "description": issue.Description, "attachments": issueFiles}, "comments": semanticComments, "anchor": anchorID})
	digest := sha256.Sum256(semantic)
	token := "sha256:" + hex.EncodeToString(digest[:])
	snapshot := map[string]any{
		"version": 1, "captured_by_user_id": LocalOperatorID, "captured_at": now(),
		"source_issue": issueSnapshot, "comment_thread": thread, "anchor_comment_id": anchorID,
		"capture_token": token,
		"limits":        map[string]any{"comment_count": len(thread), "text_bytes": textBytes, "attachment_count": attachmentCount, "attachment_bytes": attachmentBytes},
	}
	return localSourceContext{Snapshot: snapshot, Token: token, Issue: issue}, nil
}

func (s *Server) previewCommentSubIssue(w http.ResponseWriter, r *http.Request) {
	context, err := s.buildLocalSourceContext(r, chi.URLParam(r, "commentID"))
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, context.Snapshot)
}

func (s *Server) createCommentSubIssue(w http.ResponseWriter, r *http.Request) {
	context, err := s.buildLocalSourceContext(r, chi.URLParam(r, "commentID"))
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	var input struct {
		Mode         string          `json:"mode"`
		CaptureToken string          `json:"capture_token"`
		Issue        json.RawMessage `json:"issue"`
		QuickCreate  struct {
			AgentID       string   `json:"agent_id"`
			SquadID       string   `json:"squad_id"`
			Prompt        string   `json:"prompt"`
			Priority      string   `json:"priority"`
			DueDate       *string  `json:"due_date"`
			ProjectID     *string  `json:"project_id"`
			AttachmentIDs []string `json:"attachment_ids"`
		} `json:"quick_create"`
	}
	if err := decodeBody(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if input.CaptureToken == "" || input.CaptureToken != context.Token {
		writeJSON(w, http.StatusConflict, map[string]any{"error": "source context changed; preview it again", "code": "source_context_changed"})
		return
	}
	snapshot, _ := json.Marshal(context.Snapshot)
	source := &IssueSourceContext{SourceIssueID: context.Issue.ID, AnchorCommentID: chi.URLParam(r, "commentID"), CaptureToken: context.Token, Snapshot: string(snapshot)}
	parentID := context.Issue.ID
	projectID := context.Issue.ProjectID
	if input.Mode == "manual" {
		var create CreateIssueInput
		if len(input.Issue) == 0 || json.Unmarshal(input.Issue, &create) != nil || strings.TrimSpace(create.Title) == "" {
			writeError(w, http.StatusBadRequest, errors.New("a valid issue is required"))
			return
		}
		create.ParentIssueID, create.SourceContext = &parentID, source
		if create.ProjectID == nil {
			create.ProjectID = projectID
		}
		item, err := s.store.CreateIssue(r.Context(), LocalWorkspaceID, create)
		if err != nil {
			s.respondStoreError(w, err)
			return
		}
		if err := s.store.enqueueAssignedIssue(r.Context(), LocalWorkspaceID, item.ID, ""); err != nil {
			s.respondStoreError(w, err)
			return
		}
		s.hub.broadcast("issue:created", map[string]any{"issue": item})
		writeJSON(w, http.StatusCreated, item)
		return
	}
	if input.Mode != "agent" || strings.TrimSpace(input.QuickCreate.Prompt) == "" {
		writeError(w, http.StatusBadRequest, errors.New("mode must be manual or agent"))
		return
	}
	agentID, squadID := strings.TrimSpace(input.QuickCreate.AgentID), strings.TrimSpace(input.QuickCreate.SquadID)
	if (agentID == "") == (squadID == "") {
		writeError(w, http.StatusBadRequest, errors.New("exactly one agent_id or squad_id is required"))
		return
	}
	assigneeType, assigneeID := "agent", agentID
	if squadID != "" {
		assigneeType, assigneeID = "squad", squadID
	}
	title := strings.TrimSpace(strings.SplitN(input.QuickCreate.Prompt, "\n", 2)[0])
	if len([]rune(title)) > 120 {
		title = string([]rune(title)[:120])
	}
	create := CreateIssueInput{Title: title, Description: input.QuickCreate.Prompt, Status: "todo", Priority: input.QuickCreate.Priority, AssigneeType: &assigneeType, AssigneeID: &assigneeID, ParentIssueID: &parentID, ProjectID: projectID, DueDate: input.QuickCreate.DueDate, AttachmentIDs: input.QuickCreate.AttachmentIDs, SourceContext: source}
	item, err := s.store.CreateIssue(r.Context(), LocalWorkspaceID, create)
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	if err := s.store.enqueueAssignedIssue(r.Context(), LocalWorkspaceID, item.ID, ""); err != nil {
		s.respondStoreError(w, err)
		return
	}
	var taskID string
	if err := s.store.db.QueryRowContext(r.Context(), `SELECT id FROM task_runs WHERE issue_id=? AND status='queued' ORDER BY created_at DESC,id DESC LIMIT 1`, item.ID).Scan(&taskID); err != nil {
		writeError(w, http.StatusConflict, errors.New("the source-context issue was created but its task could not be queued"))
		return
	}
	s.hub.broadcast("issue:created", map[string]any{"issue": item})
	writeJSON(w, http.StatusCreated, map[string]any{"task_id": taskID})
}

func (s *Server) retrySourceContextTask(w http.ResponseWriter, r *http.Request) {
	taskID := chi.URLParam(r, "taskID")
	var issueID, status string
	if err := s.store.db.QueryRowContext(r.Context(), `SELECT tr.issue_id,tr.status FROM task_runs tr JOIN issues i ON i.id=tr.issue_id JOIN issue_source_contexts sc ON sc.issue_id=i.id WHERE tr.id=? AND i.workspace_id=?`, taskID, LocalWorkspaceID).Scan(&issueID, &status); err != nil {
		s.respondStoreError(w, err)
		return
	}
	if status != "failed" && status != "cancelled" {
		writeError(w, http.StatusConflict, errors.New("only a failed source-context task can be retried"))
		return
	}
	if err := s.store.enqueueAssignedIssue(r.Context(), LocalWorkspaceID, issueID, ""); err != nil {
		s.respondStoreError(w, err)
		return
	}
	var nextID string
	if err := s.store.db.QueryRowContext(r.Context(), `SELECT id FROM task_runs WHERE issue_id=? AND id<>? AND status='queued' ORDER BY created_at DESC,id DESC LIMIT 1`, issueID, taskID).Scan(&nextID); err != nil {
		writeError(w, http.StatusConflict, errors.New("the source-context retry could not be queued"))
		return
	}
	item, err := s.taskRun(r.Context(), LocalWorkspaceID, issueID, nextID)
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, item)
}
