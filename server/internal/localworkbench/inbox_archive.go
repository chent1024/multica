package localworkbench

import (
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

type localArchiveFilters struct {
	Statuses   []string
	Priorities []string
	Actors     []string
	UnreadOnly bool
}

type localArchiveRow struct {
	item    map[string]any
	created time.Time
	actor   string
}

type localArchiveCursor struct {
	Time  string `json:"time"`
	ID    string `json:"id"`
	Scope string `json:"scope"`
}

func parseLocalArchiveFilters(r *http.Request) (localArchiveFilters, error) {
	var filters localArchiveFilters
	for name, target := range map[string]*[]string{"statuses": &filters.Statuses, "priorities": &filters.Priorities, "actors": &filters.Actors} {
		raw := r.URL.Query().Get(name)
		if raw == "" {
			continue
		}
		if len(raw) > 8192 {
			return filters, errors.New("filter is too long")
		}
		*target = strings.Split(raw, ",")
		if len(*target) > 100 {
			return filters, errors.New("too many filter values")
		}
		for _, value := range *target {
			if value == "" {
				return filters, errors.New("empty filter value")
			}
		}
		slices.Sort(*target)
		*target = slices.Compact(*target)
	}
	if raw := r.URL.Query().Get("unread_only"); raw != "" {
		if raw != "true" && raw != "false" {
			return filters, errors.New("invalid unread_only")
		}
		filters.UnreadOnly = raw == "true"
	}
	return filters, nil
}

// Local notification preferences also apply to the archive. Group before
// filtering, so an older actor cannot make the newest visible row match.
func (s *Server) localArchivedInboxRows(r *http.Request, workspaceID, mode string) ([]localArchiveRow, error) {
	rows, err := s.store.db.QueryContext(r.Context(), inboxSelect+` WHERE workspace_id=? AND recipient_type='member' AND recipient_id=? AND archived=1
		AND (?='all' OR severity='action_required')
		AND (issue_id IS NULL OR NOT EXISTS (
			SELECT 1 FROM inbox_items active WHERE active.workspace_id=inbox_items.workspace_id
			AND active.recipient_type=inbox_items.recipient_type AND active.recipient_id=inbox_items.recipient_id
			AND active.issue_id=inbox_items.issue_id AND active.archived=0))`, workspaceID, LocalOperatorID, mode)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	all := []localArchiveRow{}
	for rows.Next() {
		item, err := scanInbox(rows)
		if err != nil {
			return nil, err
		}
		if details, _ := item["details"].(map[string]string); details == nil {
			item["details"] = map[string]string{}
		}
		created, err := time.Parse(time.RFC3339Nano, stringFromAny(item["created_at"]))
		if err != nil {
			return nil, fmt.Errorf("invalid archived notification time: %w", err)
		}
		actor := stringFromAny(item["actor_type"])
		if actor != "system" {
			if id := stringFromAny(item["actor_id"]); actor != "" && id != "" {
				actor += ":" + id
			} else {
				actor = ""
			}
		}
		all = append(all, localArchiveRow{item: item, created: created, actor: actor})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	// RFC3339Nano omits trailing zeros, so lexical timestamp order is wrong
	// at whole seconds. Compare parsed instants and use IDs to break ties.
	sort.Slice(all, func(i, j int) bool {
		if !all[i].created.Equal(all[j].created) {
			return all[i].created.After(all[j].created)
		}
		return stringFromAny(all[i].item["id"]) > stringFromAny(all[j].item["id"])
	})
	groupIndexes := map[string]int{}
	groups := []localArchiveRow{}
	for _, row := range all {
		group := localArchiveGroup(row.item)
		if index, exists := groupIndexes[group]; exists {
			details := groups[index].item["details"].(map[string]string)
			if details["comment_id"] == "" {
				if commentID := row.item["details"].(map[string]string)["comment_id"]; commentID != "" {
					details["comment_id"] = commentID
				}
			}
			continue
		}
		groupIndexes[group] = len(groups)
		groups = append(groups, row)
	}
	for _, row := range groups {
		row.item["issue_status"], row.item["issue_priority"] = nil, nil
		if issueID := stringFromAny(row.item["issue_id"]); issueID != "" {
			var status, priority string
			err := s.store.db.QueryRowContext(r.Context(), `SELECT status, priority FROM issues WHERE id=? AND workspace_id=?`, issueID, workspaceID).Scan(&status, &priority)
			if err == nil {
				row.item["issue_status"], row.item["issue_priority"] = status, priority
			} else if !errors.Is(err, sql.ErrNoRows) {
				return nil, err
			}
		}
	}
	return groups, nil
}

func localArchiveGroup(item map[string]any) string {
	if id := stringFromAny(item["issue_id"]); id != "" {
		return id
	}
	return stringFromAny(item["id"])
}

func localArchiveMatches(row localArchiveRow, filters localArchiveFilters, omitted string) bool {
	return (omitted == "statuses" || len(filters.Statuses) == 0 || slices.Contains(filters.Statuses, stringFromAny(row.item["issue_status"]))) &&
		(omitted == "priorities" || len(filters.Priorities) == 0 || slices.Contains(filters.Priorities, stringFromAny(row.item["issue_priority"]))) &&
		(omitted == "actors" || len(filters.Actors) == 0 || slices.Contains(filters.Actors, row.actor)) &&
		(omitted == "unread" || !filters.UnreadOnly || row.item["read"] != true)
}

func (s *Server) listArchivedInboxPage(w http.ResponseWriter, r *http.Request) {
	ws, err := s.workspaceFor(r)
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	filters, err := parseLocalArchiveFilters(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	limit := 50
	if raw := r.URL.Query().Get("limit"); raw != "" {
		limit, err = strconv.Atoi(raw)
		if err != nil || limit < 1 || limit > 100 {
			writeError(w, http.StatusBadRequest, errors.New("limit must be between 1 and 100"))
			return
		}
	}
	groupID := r.URL.Query().Get("group_id")
	if groupID != "" {
		parsed, err := uuid.Parse(groupID)
		if err != nil {
			writeError(w, http.StatusBadRequest, errors.New("invalid group_id"))
			return
		}
		groupID = parsed.String()
	}
	mode := s.localNotificationListMode(r.Context(), ws.ID)
	encodedScope, _ := json.Marshal([]any{ws.ID, LocalOperatorID, mode, filters, groupID})
	scope := fmt.Sprintf("%x", sha256.Sum256(encodedScope))
	var before localArchiveCursor
	var beforeTime time.Time
	if raw := r.URL.Query().Get("cursor"); raw != "" {
		decoded, decodeErr := base64.RawURLEncoding.DecodeString(raw)
		if len(raw) > 2048 || decodeErr != nil || json.Unmarshal(decoded, &before) != nil || before.Scope != scope {
			writeError(w, http.StatusBadRequest, errors.New("invalid archive cursor"))
			return
		}
		beforeTime, err = time.Parse(time.RFC3339Nano, before.Time)
		_, idErr := uuid.Parse(before.ID)
		if err != nil || idErr != nil {
			writeError(w, http.StatusBadRequest, errors.New("invalid archive cursor position"))
			return
		}
	}
	rows, err := s.localArchivedInboxRows(r, ws.ID, mode)
	if err != nil {
		s.fail(w, err)
		return
	}
	items := []map[string]any{}
	var last localArchiveRow
	hasMore := false
	for _, row := range rows {
		id := stringFromAny(row.item["id"])
		if groupID != "" && localArchiveGroup(row.item) != groupID {
			continue
		}
		if !localArchiveMatches(row, filters, "") || (!beforeTime.IsZero() && (row.created.After(beforeTime) || (row.created.Equal(beforeTime) && id >= before.ID))) {
			continue
		}
		if len(items) == limit {
			hasMore = true
			break
		}
		items = append(items, row.item)
		last = row
	}
	var nextCursor *string
	if hasMore {
		encoded, _ := json.Marshal(localArchiveCursor{Time: last.created.Format(time.RFC3339Nano), ID: stringFromAny(last.item["id"]), Scope: scope})
		cursor := base64.RawURLEncoding.EncodeToString(encoded)
		nextCursor = &cursor
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "next_cursor": nextCursor, "has_more": hasMore})
}

func (s *Server) archivedInboxFacets(w http.ResponseWriter, r *http.Request) {
	ws, err := s.workspaceFor(r)
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	filters, err := parseLocalArchiveFilters(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	rows, err := s.localArchivedInboxRows(r, ws.ID, s.localNotificationListMode(r.Context(), ws.ID))
	if err != nil {
		s.fail(w, err)
		return
	}
	statuses, priorities, actors := map[string]int{}, map[string]int{}, map[string]int{}
	unread := 0
	for _, row := range rows {
		for dimension, target := range map[string]map[string]int{"statuses": statuses, "priorities": priorities, "actors": actors} {
			key := row.actor
			if dimension == "statuses" {
				key = stringFromAny(row.item["issue_status"])
			} else if dimension == "priorities" {
				key = stringFromAny(row.item["issue_priority"])
			}
			if key != "" {
				// Keep zero counts, matching the upstream disjunctive facets.
				if _, exists := target[key]; !exists {
					target[key] = 0
				}
				if localArchiveMatches(row, filters, dimension) {
					target[key]++
				}
			}
		}
		if row.item["read"] != true && localArchiveMatches(row, filters, "unread") {
			unread++
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"statuses": statuses, "priorities": priorities, "actors": actors, "unread_count": unread})
}
