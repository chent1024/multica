package localworkbench

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"path/filepath"
	"testing"
)

type localArchiveTestPage struct {
	Items      []map[string]any `json:"items"`
	NextCursor *string          `json:"next_cursor"`
	HasMore    bool             `json:"has_more"`
}

func newLocalArchiveTestServer(t *testing.T) *Server {
	t.Helper()
	root := t.TempDir()
	server, err := NewServer(context.Background(), ServerConfig{DatabasePath: filepath.Join(root, "db.sqlite"), FileRoot: filepath.Join(root, "files")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close() })
	return server
}

func insertLocalArchiveTestNotification(t *testing.T, server *Server, input localNotificationInput, timestamp string, read, archived bool) string {
	t.Helper()
	tx, err := server.store.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	item, inserted, err := insertLocalNotificationTx(t.Context(), tx, input)
	if err != nil || !inserted {
		t.Fatalf("insert fixture: inserted=%v error=%v", inserted, err)
	}
	id := item["id"].(string)
	if _, err := tx.ExecContext(t.Context(), `UPDATE inbox_items SET created_at=?, read=?, archived=? WHERE id=?`, timestamp, read, archived, id); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestLocalArchivedInboxStablePaginationBeyondLegacyLimit(t *testing.T) {
	server := newLocalArchiveTestServer(t)
	handler := server.Router()
	ids := []string{}
	for index := range 505 {
		timestamp := fmt.Sprintf("2026-10-25T00:00:00.%09dZ", index)
		if index == 0 {
			timestamp = "2026-10-25T00:00:00Z"
		}
		ids = append(ids, insertLocalArchiveTestNotification(t, server, localNotificationInput{Type: "runtime_failed", Severity: "action_required", SourceKey: fmt.Sprintf("archive:%d", index), Title: "Runtime", ActorType: "system"}, timestamp, false, true))
	}
	var firstCursor string
	cursor := ""
	seen := map[string]bool{}
	for pageNumber := 0; ; pageNumber++ {
		path := "/api/inbox/archived/page?limit=100"
		if cursor != "" {
			path += "&cursor=" + url.QueryEscape(cursor)
		}
		response := localRequest(t, handler, http.MethodGet, path, nil)
		requireStatus(t, response, http.StatusOK)
		page := decodeResponse[localArchiveTestPage](t, response)
		if page.HasMore != (page.NextCursor != nil) || (page.HasMore && len(page.Items) == 0) {
			t.Fatalf("invalid page cursor contract: %+v", page)
		}
		for _, item := range page.Items {
			id := item["id"].(string)
			if seen[id] || id != ids[len(ids)-1-len(seen)] {
				t.Fatalf("duplicate or incorrectly ordered id=%s at position=%d", id, len(seen))
			}
			seen[id] = true
		}
		if pageNumber == 0 {
			firstCursor = *page.NextCursor
		}
		if !page.HasMore {
			break
		}
		cursor = *page.NextCursor
		if pageNumber > 6 {
			t.Fatal("pagination did not terminate")
		}
	}
	if len(seen) != 505 {
		t.Fatalf("archive records=%d want=505", len(seen))
	}
	for _, test := range []struct {
		name string
		path string
	}{
		{"changed filters", "/api/inbox/archived/page?actors=system&cursor=" + url.QueryEscape(firstCursor)},
		{"changed group", "/api/inbox/archived/page?group_id=" + ids[0] + "&cursor=" + url.QueryEscape(firstCursor)},
		{"invalid cursor", "/api/inbox/archived/page?cursor=broken"},
		{"invalid limit", "/api/inbox/archived/page?limit=101"},
		{"invalid group", "/api/inbox/archived/page?group_id=broken"},
		{"empty filter value", "/api/inbox/archived/page?actors=system,"},
		{"invalid unread", "/api/inbox/archived/page?unread_only=yes"},
	} {
		t.Run(test.name, func(t *testing.T) {
			requireStatus(t, localRequest(t, handler, http.MethodGet, test.path, nil), http.StatusBadRequest)
		})
	}
}

func TestLocalArchivedInboxGroupsFiltersFacetsAndLookup(t *testing.T) {
	server := newLocalArchiveTestServer(t)
	handler := server.Router()
	projectResponse := localRequest(t, handler, http.MethodPost, "/api/projects", map[string]any{"title": "Archive project"})
	requireStatus(t, projectResponse, http.StatusCreated)
	project := decodeResponse[Project](t, projectResponse)
	issueIDs := []string{}
	for _, input := range []map[string]any{
		{"title": "Todo", "status": "todo", "priority": "urgent", "project_id": project.ID},
		{"title": "Done", "status": "done", "priority": "low", "project_id": project.ID},
		{"title": "Active", "status": "in_progress", "priority": "normal", "project_id": project.ID},
	} {
		response := localRequest(t, handler, http.MethodPost, "/api/issues", input)
		requireStatus(t, response, http.StatusCreated)
		issueIDs = append(issueIDs, decodeResponse[Issue](t, response).ID)
	}
	insert := func(key, issueID, actorType, actorID, timestamp string, read, archived bool, details map[string]string) string {
		t.Helper()
		return insertLocalArchiveTestNotification(t, server, localNotificationInput{Type: "review_requested", Severity: "info", SourceKey: key, Title: key, IssueID: issueID, ActorType: actorType, ActorID: actorID, Details: details}, timestamp, read, archived)
	}
	insert("old", issueIDs[0], "member", LocalOperatorID, "2026-10-25T01:00:00Z", false, true, map[string]string{"comment_id": "older-anchor"})
	newest := insert("new", issueIDs[0], "agent", LocalAgentID, "2026-10-25T02:00:00Z", false, true, nil)
	insert("done", issueIDs[1], "system", "", "2026-10-25T03:00:00Z", true, true, nil)
	insert("active-archive", issueIDs[2], "system", "", "2026-10-25T04:00:00Z", false, true, nil)
	insert("active-current", issueIDs[2], "system", "", "2026-10-25T05:00:00Z", false, false, nil)
	systemID := insert("issue-less", "", "system", "", "2026-10-25T06:00:00Z", false, true, nil)
	response := localRequest(t, handler, http.MethodGet, "/api/inbox/archived/page", nil)
	requireStatus(t, response, http.StatusOK)
	page := decodeResponse[localArchiveTestPage](t, response)
	if len(page.Items) != 3 || page.Items[0]["id"] != systemID || page.Items[2]["id"] != newest {
		t.Fatalf("representative rows=%v", page.Items)
	}
	if page.Items[2]["details"].(map[string]any)["comment_id"] != "older-anchor" || page.Items[2]["issue_priority"] != "urgent" {
		t.Fatalf("projection or comment anchor missing: %v", page.Items[2])
	}
	lookup := localRequest(t, handler, http.MethodGet, "/api/inbox/archived/page?group_id="+issueIDs[0], nil)
	requireStatus(t, lookup, http.StatusOK)
	if items := decodeResponse[localArchiveTestPage](t, lookup).Items; len(items) != 1 || items[0]["id"] != newest {
		t.Fatalf("issue group lookup=%v", items)
	}
	lookup = localRequest(t, handler, http.MethodGet, "/api/inbox/archived/page?group_id="+systemID, nil)
	requireStatus(t, lookup, http.StatusOK)
	if items := decodeResponse[localArchiveTestPage](t, lookup).Items; len(items) != 1 || items[0]["id"] != systemID {
		t.Fatalf("system group lookup=%v", items)
	}
	for _, test := range []struct {
		name  string
		query string
		count int
	}{
		{"newest actor", "actors=agent:" + LocalAgentID, 1},
		{"older actor cannot match", "actors=member:" + LocalOperatorID, 0},
		{"status and priority and unread", "statuses=todo&priorities=urgent&unread_only=true", 1},
		{"read filter", "statuses=done&unread_only=true", 0},
		{"system actor", "actors=system", 2},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := localRequest(t, handler, http.MethodGet, "/api/inbox/archived/page?"+test.query, nil)
			requireStatus(t, response, http.StatusOK)
			if items := decodeResponse[localArchiveTestPage](t, response).Items; len(items) != test.count {
				t.Fatalf("filtered items=%v want count=%d", items, test.count)
			}
		})
	}
	facetsResponse := localRequest(t, handler, http.MethodGet, "/api/inbox/archived/facets?statuses=todo&priorities=urgent&actors=agent:"+LocalAgentID+"&unread_only=true", nil)
	requireStatus(t, facetsResponse, http.StatusOK)
	facets := decodeResponse[struct {
		Statuses    map[string]int `json:"statuses"`
		Priorities  map[string]int `json:"priorities"`
		Actors      map[string]int `json:"actors"`
		UnreadCount int            `json:"unread_count"`
	}](t, facetsResponse)
	if facets.Statuses["todo"] != 1 || facets.Statuses["done"] != 0 || facets.Priorities["urgent"] != 1 || facets.Actors["agent:"+LocalAgentID] != 1 || facets.UnreadCount != 1 {
		t.Fatalf("facets=%+v", facets)
	}
	for _, test := range []struct {
		name      string
		query     string
		dimension string
		key       string
		count     float64
	}{
		{"status facet omits selected status", "statuses=missing", "statuses", "todo", 1},
		{"priority facet omits selected priority", "priorities=missing", "priorities", "low", 1},
		{"actor facet omits selected actor", "actors=missing", "actors", "system", 2},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := localRequest(t, handler, http.MethodGet, "/api/inbox/archived/facets?"+test.query, nil)
			requireStatus(t, response, http.StatusOK)
			facets := decodeResponse[map[string]any](t, response)
			if got := facets[test.dimension].(map[string]any)[test.key]; got != test.count {
				t.Fatalf("disjunctive facet=%v want=%v", got, test.count)
			}
		})
	}
	preference := localRequest(t, handler, http.MethodPatch, "/api/notification-preferences", map[string]any{"preferences": map[string]any{"local_delivery": "action_required"}})
	requireStatus(t, preference, http.StatusOK)
	response = localRequest(t, handler, http.MethodGet, "/api/inbox/archived/page", nil)
	requireStatus(t, response, http.StatusOK)
	if items := decodeResponse[localArchiveTestPage](t, response).Items; len(items) != 0 {
		t.Fatalf("notification preference was ignored: %v", items)
	}
}
