package localworkbench

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"sort"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/util"
)

func scanLabel(row rowScanner) (map[string]any, error) {
	var id, workspaceID, resourceType, name, description, color, createdAt, updatedAt string
	var usage int
	err := row.Scan(&id, &workspaceID, &resourceType, &name, &description, &color, &createdAt, &updatedAt, &usage)
	return map[string]any{"id": id, "workspace_id": workspaceID, "resource_type": resourceType, "name": name, "description": description, "color": color, "usage_count": usage, "created_at": createdAt, "updated_at": updatedAt}, err
}

func (s *Server) listLabels(w http.ResponseWriter, r *http.Request) {
	ws, err := s.workspaceFor(r)
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	kind := strings.TrimSpace(r.URL.Query().Get("resource_type"))
	query, args := `SELECT l.id, l.workspace_id, l.resource_type, l.name, l.description, l.color, l.created_at, l.updated_at, COUNT(rl.label_id) FROM labels l LEFT JOIN resource_labels rl ON rl.label_id=l.id WHERE l.workspace_id=?`, []any{ws.ID}
	if kind != "" {
		query += ` AND l.resource_type=?`
		args = append(args, kind)
	}
	query += ` GROUP BY l.id ORDER BY l.resource_type, l.name, l.id`
	rows, err := s.store.db.QueryContext(r.Context(), query, args...)
	if err != nil {
		s.fail(w, err)
		return
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		item, err := scanLabel(rows)
		if err != nil {
			s.fail(w, err)
			return
		}
		items = append(items, item)
	}
	writeJSON(w, http.StatusOK, map[string]any{"labels": items, "total": len(items)})
}

func (s *Server) createIssueStatus(w http.ResponseWriter, r *http.Request) {
	ws, err := s.workspaceFor(r)
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	var input struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		Category    string `json:"category"`
		Color       string `json:"color"`
	}
	if err := decodeBody(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	input.Name = strings.TrimSpace(input.Name)
	if input.Name == "" || !validStatusCategory(input.Category) {
		writeError(w, http.StatusBadRequest, errors.New("name and a valid category are required"))
		return
	}
	if input.Color == "" {
		input.Color = "#6b7280"
	}
	var position int
	_ = s.store.db.QueryRowContext(r.Context(), `SELECT COALESCE(MAX(position),-1)+1 FROM issue_statuses WHERE workspace_id=? AND category=?`, ws.ID, input.Category).Scan(&position)
	id, stamp := uuid.NewString(), now()
	key := "custom_" + strings.ReplaceAll(id, "-", "")
	if _, err := s.store.db.ExecContext(r.Context(), `INSERT INTO issue_statuses(id, workspace_id, key, name, description, category, color, is_system, position, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, 0, ?, ?, ?)`, id, ws.ID, key, input.Name, input.Description, input.Category, input.Color, position, stamp, stamp); err != nil {
		s.fail(w, err)
		return
	}
	item, _ := s.store.ListStatuses(r.Context(), ws.ID)
	for _, candidate := range item {
		if candidate["id"] == id {
			writeJSON(w, http.StatusCreated, candidate)
			return
		}
	}
	s.respondStoreError(w, sql.ErrNoRows)
}

func validStatusCategory(value string) bool {
	switch value {
	case "unstarted", "started", "closed", "backlog", "todo", "in_progress", "in_review", "done", "blocked", "cancelled":
		return true
	default:
		return false
	}
}

func (s *Server) issueStatusItem(w http.ResponseWriter, r *http.Request) {
	ws, err := s.workspaceFor(r)
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	id := chi.URLParam(r, "id")
	var system bool
	if err := s.store.db.QueryRowContext(r.Context(), `SELECT is_system FROM issue_statuses WHERE id=? AND workspace_id=?`, id, ws.ID).Scan(&system); err != nil {
		s.respondStoreError(w, err)
		return
	}
	if system {
		writeError(w, http.StatusForbidden, errors.New("built-in statuses cannot be changed"))
		return
	}
	if r.Method == http.MethodDelete {
		tx, err := s.store.db.BeginTx(r.Context(), nil)
		if err != nil {
			s.fail(w, err)
			return
		}
		defer tx.Rollback()
		var statusKey string
		if err := tx.QueryRowContext(r.Context(), `SELECT key FROM issue_statuses WHERE workspace_id=? AND id=?`, ws.ID, id).Scan(&statusKey); err != nil {
			s.respondStoreError(w, err)
			return
		}
		if err := archiveLocalWorkflowStatusTx(r.Context(), tx, ws.ID, statusKey); err != nil {
			s.respondStoreError(w, err)
			return
		}
		if _, err := tx.ExecContext(r.Context(), `UPDATE issue_statuses SET archived_at=?,updated_at=? WHERE id=? AND workspace_id=? AND archived_at IS NULL`, now(), now(), id, ws.ID); err != nil {
			s.fail(w, err)
			return
		}
		if err := tx.Commit(); err != nil {
			s.fail(w, err)
			return
		}
		s.hub.broadcast("issue_status:changed", map[string]any{"workspace_id": ws.ID})
		s.hub.broadcast("issue_workflow:changed", map[string]any{"workspace_id": ws.ID})
	} else {
		input := map[string]any{}
		if err := decodeBody(r, &input); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		if category, ok := input["category"].(string); ok && !validStatusCategory(category) {
			writeError(w, http.StatusBadRequest, errors.New("invalid status category"))
			return
		}
		set, args := []string{"updated_at=?"}, []any{now()}
		for _, key := range []string{"name", "description", "category", "color"} {
			if value, ok := input[key]; ok {
				set, args = append(set, key+"=?"), append(args, value)
			}
		}
		args = append(args, id, ws.ID)
		if _, err := s.store.db.ExecContext(r.Context(), `UPDATE issue_statuses SET `+strings.Join(set, ",")+` WHERE id=? AND workspace_id=?`, args...); err != nil {
			s.fail(w, err)
			return
		}
	}
	statuses, _ := s.store.ListStatuses(r.Context(), ws.ID)
	for _, candidate := range statuses {
		if candidate["id"] == id {
			writeJSON(w, http.StatusOK, candidate)
			return
		}
	}
	// Archived rows are intentionally omitted by ListStatuses; return the
	// stable archived record expected by the settings mutation.
	var key, name, description, category, color, createdAt, updatedAt string
	var position int
	var archivedAt sql.NullString
	if err := s.store.db.QueryRowContext(r.Context(), `SELECT key, name, description, category, color, position, archived_at, created_at, updated_at FROM issue_statuses WHERE id=?`, id).Scan(&key, &name, &description, &category, &color, &position, &archivedAt, &createdAt, &updatedAt); err != nil {
		s.respondStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "workspace_id": ws.ID, "key": key, "name": name, "description": description, "category": category, "color": color, "is_system": false, "position": position, "archived_at": nullableString(archivedAt), "created_at": createdAt, "updated_at": updatedAt})
}

func (s *Server) reorderIssueStatuses(w http.ResponseWriter, r *http.Request) {
	ws, err := s.workspaceFor(r)
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	var input struct {
		Category string   `json:"category"`
		IDs      []string `json:"ids"`
	}
	if err := decodeBody(r, &input); err != nil || !validStatusCategory(input.Category) {
		writeError(w, http.StatusBadRequest, errors.New("category and ids are required"))
		return
	}
	rows, err := s.store.db.QueryContext(r.Context(), `SELECT id, category FROM issue_statuses WHERE workspace_id=? AND archived_at IS NULL ORDER BY position, id`, ws.ID)
	if err != nil {
		s.fail(w, err)
		return
	}
	existing := []string{}
	for rows.Next() {
		var id, category string
		if err := rows.Scan(&id, &category); err != nil {
			_ = rows.Close()
			s.fail(w, err)
			return
		}
		if localStatusCategoryMatches(category, input.Category) {
			existing = append(existing, id)
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		s.fail(w, err)
		return
	}
	_ = rows.Close()
	want, got := append([]string(nil), input.IDs...), append([]string(nil), existing...)
	sort.Strings(want)
	sort.Strings(got)
	if fmt.Sprint(want) != fmt.Sprint(got) {
		writeError(w, http.StatusConflict, errors.New("ids must contain every active status in the category exactly once"))
		return
	}
	tx, err := s.store.db.BeginTx(r.Context(), nil)
	if err != nil {
		s.fail(w, err)
		return
	}
	defer func() { _ = tx.Rollback() }()
	for position, id := range input.IDs {
		if _, err := tx.ExecContext(r.Context(), `UPDATE issue_statuses SET position=?, updated_at=? WHERE id=? AND workspace_id=?`, position, now(), id, ws.ID); err != nil {
			s.fail(w, err)
			return
		}
	}
	if err := tx.Commit(); err != nil {
		s.fail(w, err)
		return
	}
	s.listIssueStatuses(w, r)
}

func (s *Server) createLabel(w http.ResponseWriter, r *http.Request) {
	ws, err := s.workspaceFor(r)
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	var input struct {
		ResourceType string `json:"resource_type"`
		Name         string `json:"name"`
		Description  string `json:"description"`
		Color        string `json:"color"`
	}
	if err := decodeBody(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	input.Name, input.ResourceType = strings.TrimSpace(input.Name), strings.TrimSpace(input.ResourceType)
	if input.ResourceType == "" {
		input.ResourceType = "issue"
	}
	if input.Color == "" {
		input.Color = "#6b7280"
	}
	if input.Name == "" || (input.ResourceType != "issue" && input.ResourceType != "agent" && input.ResourceType != "skill") {
		writeError(w, http.StatusBadRequest, errors.New("name and a valid resource_type are required"))
		return
	}
	id, stamp := uuid.NewString(), now()
	if _, err := s.store.db.ExecContext(r.Context(), `INSERT INTO labels(id, workspace_id, resource_type, name, description, color, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, id, ws.ID, input.ResourceType, input.Name, input.Description, input.Color, stamp, stamp); err != nil {
		s.fail(w, err)
		return
	}
	item, _ := scanLabel(s.store.db.QueryRowContext(r.Context(), `SELECT l.id, l.workspace_id, l.resource_type, l.name, l.description, l.color, l.created_at, l.updated_at, 0 FROM labels l WHERE l.id=?`, id))
	writeJSON(w, http.StatusCreated, item)
}

func (s *Server) labelItem(w http.ResponseWriter, r *http.Request) {
	ws, err := s.workspaceFor(r)
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	id := chi.URLParam(r, "id")
	if r.Method == http.MethodGet {
		item, err := scanLabel(s.store.db.QueryRowContext(r.Context(), `SELECT l.id, l.workspace_id, l.resource_type, l.name, l.description, l.color, l.created_at, l.updated_at, COUNT(rl.label_id) FROM labels l LEFT JOIN resource_labels rl ON rl.label_id=l.id WHERE l.id=? AND l.workspace_id=? GROUP BY l.id`, id, ws.ID))
		if err != nil {
			s.respondStoreError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, item)
		return
	}
	if r.Method == http.MethodDelete {
		tx, err := s.store.db.BeginTx(r.Context(), nil)
		if err != nil {
			s.fail(w, err)
			return
		}
		defer func() { _ = tx.Rollback() }()
		_, _ = tx.ExecContext(r.Context(), `DELETE FROM resource_labels WHERE label_id=?`, id)
		result, err := tx.ExecContext(r.Context(), `DELETE FROM labels WHERE id=? AND workspace_id=?`, id, ws.ID)
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
		return
	}
	var input map[string]any
	if err := decodeBody(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	set, args := []string{"updated_at=?"}, []any{now()}
	for _, key := range []string{"name", "description", "color"} {
		if value, ok := input[key]; ok {
			set, args = append(set, key+"=?"), append(args, value)
		}
	}
	args = append(args, id, ws.ID)
	if _, err := s.store.db.ExecContext(r.Context(), `UPDATE labels SET `+strings.Join(set, ",")+` WHERE id=? AND workspace_id=?`, args...); err != nil {
		s.fail(w, err)
		return
	}
	item, err := scanLabel(s.store.db.QueryRowContext(r.Context(), `SELECT l.id, l.workspace_id, l.resource_type, l.name, l.description, l.color, l.created_at, l.updated_at, COUNT(rl.label_id) FROM labels l LEFT JOIN resource_labels rl ON rl.label_id=l.id WHERE l.id=? AND l.workspace_id=? GROUP BY l.id`, id, ws.ID))
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func resourceLabelTarget(r *http.Request) (string, string) {
	path := r.URL.Path
	switch {
	case strings.HasPrefix(path, "/api/issues/"):
		return "issue", chi.URLParam(r, "id")
	case strings.HasPrefix(path, "/api/agents/"):
		return "agent", chi.URLParam(r, "id")
	default:
		return "skill", chi.URLParam(r, "id")
	}
}

func (s *Server) resourceLabels(w http.ResponseWriter, r *http.Request) {
	ws, err := s.workspaceFor(r)
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	kind, resourceID := resourceLabelTarget(r)
	if !s.resourceExists(r, ws.ID, kind, resourceID) {
		s.respondStoreError(w, sql.ErrNoRows)
		return
	}
	var tx *sql.Tx
	var cursor int64
	var origin localWakeupOrigin
	if r.Method == http.MethodPost || r.Method == http.MethodDelete {
		tx, err = s.store.db.BeginTx(r.Context(), nil)
		if err != nil {
			s.fail(w, err)
			return
		}
		defer tx.Rollback()
		cursor, origin, err = beginLocalWakeupMutation(r.Context(), tx, ws.ID)
		if err != nil {
			s.localWakeupError(w, err)
			return
		}
	}
	if r.Method == http.MethodPost {
		var input struct {
			LabelID string `json:"label_id"`
		}
		if err := decodeBody(r, &input); err != nil || input.LabelID == "" {
			writeError(w, http.StatusBadRequest, errors.New("label_id is required"))
			return
		}
		result, err := tx.ExecContext(r.Context(), `INSERT INTO resource_labels(workspace_id, resource_type, resource_id, label_id, created_at) SELECT ?, ?, ?, id, ? FROM labels WHERE id=? AND workspace_id=? AND resource_type=? ON CONFLICT DO NOTHING`, ws.ID, kind, resourceID, now(), input.LabelID, ws.ID, kind)
		if err != nil {
			s.fail(w, err)
			return
		}
		if count, _ := result.RowsAffected(); count == 0 {
			var exists int
			_ = tx.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM labels WHERE id=? AND workspace_id=? AND resource_type=?`, input.LabelID, ws.ID, kind).Scan(&exists)
			if exists == 0 {
				s.respondStoreError(w, sql.ErrNoRows)
				return
			}
		}
	}
	if r.Method == http.MethodDelete {
		_, _ = tx.ExecContext(r.Context(), `DELETE FROM resource_labels WHERE workspace_id=? AND resource_type=? AND resource_id=? AND label_id=?`, ws.ID, kind, resourceID, chi.URLParam(r, "labelID"))
	}
	if tx != nil {
		if err := commitLocalWakeupMutation(r.Context(), tx, ws.ID, cursor, origin); err != nil {
			s.fail(w, err)
			return
		}
	}
	rows, err := s.store.db.QueryContext(r.Context(), `SELECT l.id, l.workspace_id, l.resource_type, l.name, l.description, l.color, l.created_at, l.updated_at, (SELECT COUNT(*) FROM resource_labels x WHERE x.label_id=l.id) FROM resource_labels rl JOIN labels l ON l.id=rl.label_id WHERE rl.workspace_id=? AND rl.resource_type=? AND rl.resource_id=? ORDER BY l.name, l.id`, ws.ID, kind, resourceID)
	if err != nil {
		s.fail(w, err)
		return
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		item, err := scanLabel(rows)
		if err != nil {
			s.fail(w, err)
			return
		}
		items = append(items, item)
	}
	writeJSON(w, http.StatusOK, map[string]any{"labels": items})
}

func (s *Server) resourceExists(r *http.Request, workspaceID, kind, id string) bool {
	table := map[string]string{"issue": "issues", "agent": "agents", "skill": "skills"}[kind]
	var exists int
	return s.store.db.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM `+table+` WHERE id=? AND workspace_id=?`, id, workspaceID).Scan(&exists) == nil && exists == 1
}

func scanProperty(row rowScanner) (map[string]any, error) {
	var id, workspaceID, name, kind, description, icon, raw, createdAt, updatedAt string
	var position, usage int
	var archivedAt sql.NullString
	err := row.Scan(&id, &workspaceID, &name, &kind, &description, &icon, &raw, &position, &archivedAt, &createdAt, &updatedAt, &usage)
	config := map[string]any{}
	_ = json.Unmarshal([]byte(raw), &config)
	return map[string]any{"id": id, "workspace_id": workspaceID, "name": name, "type": kind, "description": description, "icon": icon, "config": config, "position": position, "archived": archivedAt.Valid, "archived_at": nullableString(archivedAt), "usage_count": usage, "created_at": createdAt, "updated_at": updatedAt}, err
}

func (s *Server) listProperties(w http.ResponseWriter, r *http.Request) {
	ws, err := s.workspaceFor(r)
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	query := `SELECT p.id, p.workspace_id, p.name, p.type, p.description, p.icon, p.config, p.position, p.archived_at, p.created_at, p.updated_at, (SELECT COUNT(*) FROM issues i WHERE i.workspace_id=p.workspace_id AND json_type(i.properties, '$.' || p.id) IS NOT NULL) FROM issue_properties p WHERE p.workspace_id=?`
	if r.URL.Query().Get("include_archived") != "true" {
		query += ` AND p.archived_at IS NULL`
	}
	query += ` ORDER BY p.position, p.id`
	rows, err := s.store.db.QueryContext(r.Context(), query, ws.ID)
	if err != nil {
		s.fail(w, err)
		return
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		item, err := scanProperty(rows)
		if err != nil {
			s.fail(w, err)
			return
		}
		items = append(items, item)
	}
	writeJSON(w, http.StatusOK, map[string]any{"properties": items, "total": len(items)})
}

func (s *Server) createProperty(w http.ResponseWriter, r *http.Request) {
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
	name, kind := strings.TrimSpace(stringFromAny(input["name"])), strings.TrimSpace(stringFromAny(input["type"]))
	if name == "" || kind == "" {
		writeError(w, http.StatusBadRequest, errors.New("name and type are required"))
		return
	}
	config, _ := json.Marshal(input["config"])
	if string(config) == "null" {
		config = []byte("{}")
	}
	id, stamp := uuid.NewString(), now()
	var position int
	_ = s.store.db.QueryRowContext(r.Context(), `SELECT COALESCE(MAX(position),-1)+1 FROM issue_properties WHERE workspace_id=?`, ws.ID).Scan(&position)
	if _, err := s.store.db.ExecContext(r.Context(), `INSERT INTO issue_properties(id, workspace_id, name, type, description, icon, config, position, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, id, ws.ID, name, kind, stringFromAny(input["description"]), stringFromAny(input["icon"]), string(config), position, stamp, stamp); err != nil {
		s.fail(w, err)
		return
	}
	item, _ := scanProperty(s.store.db.QueryRowContext(r.Context(), `SELECT id, workspace_id, name, type, description, icon, config, position, archived_at, created_at, updated_at, 0 FROM issue_properties WHERE id=?`, id))
	writeJSON(w, http.StatusCreated, item)
}

func (s *Server) propertyItem(w http.ResponseWriter, r *http.Request) {
	ws, err := s.workspaceFor(r)
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	id := chi.URLParam(r, "id")
	if r.Method == http.MethodDelete {
		result, err := s.store.db.ExecContext(r.Context(), `UPDATE issue_properties SET archived_at=?, updated_at=? WHERE id=? AND workspace_id=? AND archived_at IS NULL`, now(), now(), id, ws.ID)
		if err != nil {
			s.fail(w, err)
			return
		}
		if count, _ := result.RowsAffected(); count != 1 {
			s.respondStoreError(w, sql.ErrNoRows)
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	input := map[string]any{}
	if err := decodeBody(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	set, args := []string{"updated_at=?"}, []any{now()}
	for _, key := range []string{"name", "description", "icon", "type", "position"} {
		if value, ok := input[key]; ok {
			set, args = append(set, key+"=?"), append(args, value)
		}
	}
	if value, ok := input["config"]; ok {
		raw, _ := json.Marshal(value)
		set, args = append(set, "config=?"), append(args, string(raw))
	}
	args = append(args, id, ws.ID)
	if _, err := s.store.db.ExecContext(r.Context(), `UPDATE issue_properties SET `+strings.Join(set, ",")+` WHERE id=? AND workspace_id=?`, args...); err != nil {
		s.fail(w, err)
		return
	}
	item, err := scanProperty(s.store.db.QueryRowContext(r.Context(), `SELECT p.id, p.workspace_id, p.name, p.type, p.description, p.icon, p.config, p.position, p.archived_at, p.created_at, p.updated_at, (SELECT COUNT(*) FROM issues i WHERE i.workspace_id=p.workspace_id AND json_type(i.properties, '$.' || p.id) IS NOT NULL) FROM issue_properties p WHERE p.id=? AND p.workspace_id=?`, id, ws.ID))
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func (s *Server) issuePropertyValue(w http.ResponseWriter, r *http.Request) {
	ws, err := s.workspaceFor(r)
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	issueID, propertyID := chi.URLParam(r, "id"), chi.URLParam(r, "propertyID")
	var raw string
	var revision int64
	if err := s.store.db.QueryRowContext(r.Context(), `SELECT properties, revision FROM issues WHERE id=? AND workspace_id=?`, issueID, ws.ID).Scan(&raw, &revision); err != nil {
		s.respondStoreError(w, err)
		return
	}
	values := map[string]any{}
	_ = json.Unmarshal([]byte(raw), &values)
	if r.Method == http.MethodDelete {
		delete(values, propertyID)
	} else {
		var input map[string]any
		if err := decodeBody(r, &input); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		var active int
		if err := s.store.db.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM issue_properties WHERE id=? AND workspace_id=? AND archived_at IS NULL`, propertyID, ws.ID).Scan(&active); err != nil || active != 1 {
			s.respondStoreError(w, sql.ErrNoRows)
			return
		}
		values[propertyID] = input["value"]
	}
	encoded, _ := json.Marshal(values)
	revision++
	if _, err := s.store.db.ExecContext(r.Context(), `UPDATE issues SET properties=?, revision=?, updated_at=?, last_activity_at=? WHERE id=? AND workspace_id=?`, string(encoded), revision, now(), now(), issueID, ws.ID); err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"properties": values, "issue_revision": revision})
}

func scanIssueView(row rowScanner) (map[string]any, error) {
	var id, workspaceID, ownerID, name, scopeType, visibility, queryRaw, displayRaw, createdAt, updatedAt string
	var scopeID, scopeVariant sql.NullString
	var version, revision int
	err := row.Scan(&id, &workspaceID, &ownerID, &name, &scopeType, &scopeID, &scopeVariant, &visibility, &version, &queryRaw, &displayRaw, &revision, &createdAt, &updatedAt)
	query, display := map[string]any{}, map[string]any{}
	_ = json.Unmarshal([]byte(queryRaw), &query)
	_ = json.Unmarshal([]byte(displayRaw), &display)
	return map[string]any{"id": id, "workspace_id": workspaceID, "owner_id": ownerID, "name": name, "scope_type": scopeType, "scope_id": nullableString(scopeID), "scope_variant": nullableString(scopeVariant), "visibility": visibility, "definition_version": version, "query": query, "display": display, "revision": revision, "created_at": createdAt, "updated_at": updatedAt}, err
}

func (s *Server) issueViews(w http.ResponseWriter, r *http.Request) {
	ws, err := s.workspaceFor(r)
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	if r.Method == http.MethodPost {
		input := map[string]any{}
		if err := decodeBody(r, &input); err != nil || strings.TrimSpace(stringFromAny(input["name"])) == "" {
			writeError(w, http.StatusBadRequest, errors.New("name is required"))
			return
		}
		id, stamp := uuid.NewString(), now()
		query, _ := json.Marshal(input["query"])
		display, _ := json.Marshal(input["display"])
		if _, err := s.store.db.ExecContext(r.Context(), `INSERT INTO issue_views(id, workspace_id, owner_id, name, scope_type, scope_id, scope_variant, visibility, definition_version, query, display, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, id, ws.ID, LocalOperatorID, input["name"], stringDefault(input["scope_type"], "workspace"), input["scope_id"], input["scope_variant"], stringDefault(input["visibility"], "private"), intDefault(input["definition_version"], 1), jsonDefault(query), jsonDefault(display), stamp, stamp); err != nil {
			s.fail(w, err)
			return
		}
		item, _ := scanIssueView(s.store.db.QueryRowContext(r.Context(), `SELECT id, workspace_id, owner_id, name, scope_type, scope_id, scope_variant, visibility, definition_version, query, display, revision, created_at, updated_at FROM issue_views WHERE id=?`, id))
		writeJSON(w, http.StatusCreated, item)
		return
	}
	rows, err := s.store.db.QueryContext(r.Context(), `SELECT id, workspace_id, owner_id, name, scope_type, scope_id, scope_variant, visibility, definition_version, query, display, revision, created_at, updated_at FROM issue_views WHERE workspace_id=? AND (owner_id=? OR visibility='workspace') ORDER BY updated_at DESC, id`, ws.ID, LocalOperatorID)
	if err != nil {
		s.fail(w, err)
		return
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		item, err := scanIssueView(rows)
		if err != nil {
			s.fail(w, err)
			return
		}
		items = append(items, item)
	}
	writeJSON(w, http.StatusOK, items)
}

func (s *Server) issueViewItem(w http.ResponseWriter, r *http.Request) {
	ws, err := s.workspaceFor(r)
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	id := chi.URLParam(r, "id")
	if r.Method == http.MethodGet {
		item, err := scanIssueView(s.store.db.QueryRowContext(r.Context(), `SELECT id, workspace_id, owner_id, name, scope_type, scope_id, scope_variant, visibility, definition_version, query, display, revision, created_at, updated_at FROM issue_views WHERE id=? AND workspace_id=?`, id, ws.ID))
		if err != nil {
			s.respondStoreError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, item)
		return
	}
	if r.Method == http.MethodDelete {
		_, _ = s.store.db.ExecContext(r.Context(), `DELETE FROM issue_views WHERE id=? AND workspace_id=? AND owner_id=?`, id, ws.ID, LocalOperatorID)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	input := map[string]any{}
	if err := decodeBody(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	var currentRevision int
	if err := s.store.db.QueryRowContext(r.Context(), `SELECT revision FROM issue_views WHERE id=? AND workspace_id=? AND owner_id=?`, id, ws.ID, LocalOperatorID).Scan(&currentRevision); err != nil {
		s.respondStoreError(w, err)
		return
	}
	if expected, ok := input["expected_revision"].(float64); ok && int(expected) != currentRevision {
		writeError(w, http.StatusConflict, errors.New("view was updated elsewhere"))
		return
	}
	set, args := []string{"revision=revision+1", "updated_at=?"}, []any{now()}
	for _, key := range []string{"name", "visibility", "scope_variant"} {
		if value, ok := input[key]; ok {
			set, args = append(set, key+"=?"), append(args, value)
		}
	}
	for _, key := range []string{"query", "display"} {
		if value, ok := input[key]; ok {
			raw, _ := json.Marshal(value)
			set, args = append(set, key+"=?"), append(args, string(raw))
		}
	}
	args = append(args, id, ws.ID, LocalOperatorID)
	if _, err := s.store.db.ExecContext(r.Context(), `UPDATE issue_views SET `+strings.Join(set, ",")+` WHERE id=? AND workspace_id=? AND owner_id=?`, args...); err != nil {
		s.fail(w, err)
		return
	}
	item, _ := scanIssueView(s.store.db.QueryRowContext(r.Context(), `SELECT id, workspace_id, owner_id, name, scope_type, scope_id, scope_variant, visibility, definition_version, query, display, revision, created_at, updated_at FROM issue_views WHERE id=?`, id))
	writeJSON(w, http.StatusOK, item)
}

func stringDefault(value any, fallback string) string {
	if text := strings.TrimSpace(stringFromAny(value)); text != "" {
		return text
	}
	return fallback
}

func intDefault(value any, fallback int) int {
	if number, ok := value.(float64); ok && number > 0 {
		return int(number)
	}
	return fallback
}

func jsonDefault(value []byte) string {
	if string(value) == "null" || len(value) == 0 {
		return "{}"
	}
	return string(value)
}

func (s *Server) issueViewPreferences(w http.ResponseWriter, r *http.Request) {
	ws, err := s.workspaceFor(r)
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	scopeType, scopeID := r.URL.Query().Get("scope_type"), r.URL.Query().Get("scope_id")
	prefs := map[string]any{"hidden": []any{}, "order": []any{}}
	if r.Method == http.MethodPut {
		var input map[string]any
		if err := decodeBody(r, &input); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		scopeType, scopeID = stringDefault(input["scope_type"], "workspace"), stringFromAny(input["scope_id"])
		if candidate, ok := input["prefs"].(map[string]any); ok {
			prefs = candidate
		}
		raw, _ := json.Marshal(map[string]any{"scope_type": scopeType, "scope_id": nullable(scopeID), "prefs": prefs})
		kind := "issue_view_preferences:" + scopeType + ":" + scopeID
		if _, err := s.store.db.ExecContext(r.Context(), `INSERT INTO local_preferences(workspace_id, owner_id, kind, data, updated_at) VALUES (?, ?, ?, ?, ?) ON CONFLICT(workspace_id, owner_id, kind) DO UPDATE SET data=excluded.data, updated_at=excluded.updated_at`, ws.ID, LocalOperatorID, kind, string(raw), now()); err != nil {
			s.fail(w, err)
			return
		}
	} else {
		if scopeType == "" {
			scopeType = "workspace"
		}
		var raw string
		if err := s.store.db.QueryRowContext(r.Context(), `SELECT data FROM local_preferences WHERE workspace_id=? AND owner_id=? AND kind=?`, ws.ID, LocalOperatorID, "issue_view_preferences:"+scopeType+":"+scopeID).Scan(&raw); err == nil {
			var stored map[string]any
			if json.Unmarshal([]byte(raw), &stored) == nil {
				if candidate, ok := stored["prefs"].(map[string]any); ok {
					prefs = candidate
				}
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"scope_type": scopeType, "scope_id": nullable(scopeID), "prefs": prefs, "updated_at": now()})
}

func pinPayload(workspaceID, itemType, itemID string, position int, createdAt string) map[string]any {
	return map[string]any{"id": itemType + ":" + itemID, "workspace_id": workspaceID, "user_id": LocalOperatorID, "item_type": itemType, "item_id": itemID, "position": position, "created_at": createdAt}
}

func (s *Server) pins(w http.ResponseWriter, r *http.Request) {
	ws, err := s.workspaceFor(r)
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	if r.Method == http.MethodPost {
		var input struct {
			ItemType string `json:"item_type"`
			ItemID   string `json:"item_id"`
		}
		if err := decodeBody(r, &input); err != nil || input.ItemType == "" || input.ItemID == "" {
			writeError(w, http.StatusBadRequest, errors.New("item_type and item_id are required"))
			return
		}
		var position int
		_ = s.store.db.QueryRowContext(r.Context(), `SELECT COALESCE(MAX(position),-1)+1 FROM pins WHERE workspace_id=? AND owner_id=?`, ws.ID, LocalOperatorID).Scan(&position)
		stamp := now()
		if _, err := s.store.db.ExecContext(r.Context(), `INSERT INTO pins(workspace_id, owner_id, item_type, item_id, position, created_at) VALUES (?, ?, ?, ?, ?, ?) ON CONFLICT DO UPDATE SET position=excluded.position`, ws.ID, LocalOperatorID, input.ItemType, input.ItemID, position, stamp); err != nil {
			s.fail(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, pinPayload(ws.ID, input.ItemType, input.ItemID, position, stamp))
		return
	}
	rows, err := s.store.db.QueryContext(r.Context(), `SELECT item_type, item_id, position, created_at FROM pins WHERE workspace_id=? AND owner_id=? ORDER BY position, item_type, item_id`, ws.ID, LocalOperatorID)
	if err != nil {
		s.fail(w, err)
		return
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var kind, id, createdAt string
		var position int
		if err := rows.Scan(&kind, &id, &position, &createdAt); err != nil {
			s.fail(w, err)
			return
		}
		items = append(items, pinPayload(ws.ID, kind, id, position, createdAt))
	}
	writeJSON(w, http.StatusOK, items)
}

func (s *Server) deletePin(w http.ResponseWriter, r *http.Request) {
	ws, _ := s.workspaceFor(r)
	_, _ = s.store.db.ExecContext(r.Context(), `DELETE FROM pins WHERE workspace_id=? AND owner_id=? AND item_type=? AND item_id=?`, ws.ID, LocalOperatorID, chi.URLParam(r, "itemType"), chi.URLParam(r, "itemID"))
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) reorderPins(w http.ResponseWriter, r *http.Request) {
	ws, _ := s.workspaceFor(r)
	var input struct {
		Items []struct {
			ID       string `json:"id"`
			Position int    `json:"position"`
		} `json:"items"`
	}
	if err := decodeBody(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	tx, err := s.store.db.BeginTx(r.Context(), nil)
	if err != nil {
		s.fail(w, err)
		return
	}
	defer func() { _ = tx.Rollback() }()
	for _, item := range input.Items {
		parts := strings.SplitN(item.ID, ":", 2)
		if len(parts) != 2 {
			writeError(w, http.StatusBadRequest, errors.New("invalid pin id"))
			return
		}
		if _, err := tx.ExecContext(r.Context(), `UPDATE pins SET position=? WHERE workspace_id=? AND owner_id=? AND item_type=? AND item_id=?`, item.Position, ws.ID, LocalOperatorID, parts[0], parts[1]); err != nil {
			s.fail(w, err)
			return
		}
	}
	if err := tx.Commit(); err != nil {
		s.fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func scanQuickAction(row rowScanner) (map[string]any, error) {
	var id, workspaceID, name, assigneeType, assigneeID, raw, status, createdAt, updatedAt string
	var lastUsed sql.NullString
	var useCount int
	err := row.Scan(&id, &workspaceID, &name, &assigneeType, &assigneeID, &raw, &status, &useCount, &lastUsed, &createdAt, &updatedAt)
	value, decodeErr := decodeJSONMap(raw)
	if err == nil {
		err = decodeErr
	}
	value["id"], value["workspace_id"], value["name"] = id, workspaceID, name
	value["assignee_type"], value["assignee_id"], value["status"] = assigneeType, assigneeID, status
	value["use_count"], value["last_used_at"], value["created_at"], value["updated_at"] = useCount, nullableString(lastUsed), createdAt, updatedAt
	return value, err
}

func (s *Server) quickActions(w http.ResponseWriter, r *http.Request) {
	ws, err := s.workspaceFor(r)
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	if r.Method == http.MethodPost {
		input := map[string]any{}
		if err := decodeBody(r, &input); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		name, kind, target := strings.TrimSpace(stringFromAny(input["name"])), stringFromAny(input["assignee_type"]), stringFromAny(input["assignee_id"])
		if name == "" || target == "" || (kind != "agent" && kind != "squad") {
			writeError(w, http.StatusBadRequest, errors.New("name and a valid assignee are required"))
			return
		}
		if err := s.requireAutomationAssignee(r, ws.ID, kind, target); err != nil {
			s.respondStoreError(w, err)
			return
		}
		id, stamp := uuid.NewString(), now()
		value := map[string]any{"description": "", "prompt": "", "visibility": "public", "created_by_id": LocalOperatorID, "target_public": true, "target_missing": false}
		mergeJSONMap(value, input, "id", "workspace_id", "created_at", "updated_at", "use_count", "last_used_at")
		raw, _ := encodeJSONMap(value)
		if _, err := s.store.db.ExecContext(r.Context(), `INSERT INTO quick_actions(id, workspace_id, name, assignee_type, assignee_id, data, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, id, ws.ID, name, kind, target, raw, stamp, stamp); err != nil {
			s.fail(w, err)
			return
		}
		item, _ := scanQuickAction(s.store.db.QueryRowContext(r.Context(), `SELECT id, workspace_id, name, assignee_type, assignee_id, data, status, use_count, last_used_at, created_at, updated_at FROM quick_actions WHERE id=?`, id))
		writeJSON(w, http.StatusCreated, item)
		return
	}
	query := `SELECT id, workspace_id, name, assignee_type, assignee_id, data, status, use_count, last_used_at, created_at, updated_at FROM quick_actions WHERE workspace_id=?`
	if r.URL.Query().Get("include_archived") != "true" {
		query += ` AND status='active'`
	}
	query += ` ORDER BY updated_at DESC, id`
	rows, err := s.store.db.QueryContext(r.Context(), query, ws.ID)
	if err != nil {
		s.fail(w, err)
		return
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		item, err := scanQuickAction(rows)
		if err != nil {
			s.fail(w, err)
			return
		}
		items = append(items, item)
	}
	writeJSON(w, http.StatusOK, map[string]any{"quick_actions": items})
}

func (s *Server) quickActionItem(w http.ResponseWriter, r *http.Request) {
	ws, err := s.workspaceFor(r)
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	id := chi.URLParam(r, "id")
	if r.Method == http.MethodDelete {
		_, _ = s.store.db.ExecContext(r.Context(), `UPDATE quick_actions SET status='archived', updated_at=? WHERE id=? AND workspace_id=?`, now(), id, ws.ID)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	item, err := scanQuickAction(s.store.db.QueryRowContext(r.Context(), `SELECT id, workspace_id, name, assignee_type, assignee_id, data, status, use_count, last_used_at, created_at, updated_at FROM quick_actions WHERE id=? AND workspace_id=?`, id, ws.ID))
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	input := map[string]any{}
	if err := decodeBody(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	mergeJSONMap(item, input, "id", "workspace_id", "created_at", "updated_at", "use_count", "last_used_at")
	stamp := now()
	item["updated_at"] = stamp
	raw, _ := encodeJSONMap(item)
	if _, err := s.store.db.ExecContext(r.Context(), `UPDATE quick_actions SET name=?, assignee_type=?, assignee_id=?, data=?, status=?, updated_at=? WHERE id=? AND workspace_id=?`, item["name"], item["assignee_type"], item["assignee_id"], raw, stringDefault(item["status"], "active"), stamp, id, ws.ID); err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func (s *Server) quickActionExecute(w http.ResponseWriter, r *http.Request) {
	ws, err := s.workspaceFor(r)
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	issueID, actionID := chi.URLParam(r, "id"), chi.URLParam(r, "quickActionID")
	issue, err := s.store.GetIssue(r.Context(), ws.ID, issueID)
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	action, err := scanQuickAction(s.store.db.QueryRowContext(r.Context(), `SELECT id, workspace_id, name, assignee_type, assignee_id, data, status, use_count, last_used_at, created_at, updated_at FROM quick_actions WHERE id=? AND workspace_id=? AND status='active'`, actionID, ws.ID))
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	prompt := strings.TrimSpace(stringFromAny(action["prompt"]))
	if r.URL.Path[len(r.URL.Path)-6:] == "render" {
		writeJSON(w, http.StatusOK, map[string]any{"content": prompt})
		return
	}
	stamp, commentID := now(), uuid.NewString()
	tx, err := s.store.db.BeginTx(r.Context(), nil)
	if err != nil {
		s.fail(w, err)
		return
	}
	defer func() { _ = tx.Rollback() }()
	revision := issue.Revision + 1
	if _, err := tx.ExecContext(r.Context(), `UPDATE issues SET assignee_type=?, assignee_id=?, revision=?, last_activity_at=?, updated_at=? WHERE id=? AND workspace_id=?`, action["assignee_type"], action["assignee_id"], revision, stamp, stamp, issueID, ws.ID); err != nil {
		s.fail(w, err)
		return
	}
	if _, err := tx.ExecContext(r.Context(), `INSERT INTO comments(id, issue_id, author_type, author_id, content, quick_action_id, issue_revision, created_at, updated_at) VALUES (?, ?, 'member', ?, ?, ?, ?, ?, ?)`, commentID, issueID, LocalOperatorID, prompt, actionID, revision, stamp, stamp); err != nil {
		s.fail(w, err)
		return
	}
	if _, err := tx.ExecContext(r.Context(), `UPDATE quick_actions SET use_count=use_count+1, last_used_at=?, updated_at=? WHERE id=?`, stamp, stamp, actionID); err != nil {
		s.fail(w, err)
		return
	}
	if err := tx.Commit(); err != nil {
		s.fail(w, err)
		return
	}
	if err := s.store.enqueueAssignedIssue(r.Context(), ws.ID, issueID, prompt); err != nil {
		writeError(w, http.StatusConflict, fmt.Errorf("quick action comment saved but task could not start: %w", err))
		return
	}
	comment, _ := scanComment(s.store.db.QueryRowContext(r.Context(), `SELECT id, issue_id, author_type, author_id, content, type, parent_id, created_at, updated_at, revision, issue_revision, resolved_at, resolved_by_type, resolved_by_id, source_task_id, quick_action_id FROM comments WHERE id=?`, commentID))
	writeJSON(w, http.StatusCreated, comment)
}

func (s *Server) reactions(w http.ResponseWriter, r *http.Request) {
	ws, _ := s.workspaceFor(r)
	resourceType, resourceID := "comment", chi.URLParam(r, "commentID")
	if resourceID == "" {
		resourceType, resourceID = "issue", chi.URLParam(r, "id")
	}
	var input struct {
		Emoji string `json:"emoji"`
	}
	if err := decodeBody(r, &input); err != nil || strings.TrimSpace(input.Emoji) == "" {
		writeError(w, http.StatusBadRequest, errors.New("emoji is required"))
		return
	}
	tx, err := s.store.db.BeginTx(r.Context(), nil)
	if err != nil {
		s.fail(w, err)
		return
	}
	defer tx.Rollback()
	cursor, origin, err := beginLocalWakeupMutation(r.Context(), tx, ws.ID)
	if err != nil {
		s.localWakeupError(w, err)
		return
	}
	var exists int
	query := `SELECT COUNT(*) FROM issues WHERE workspace_id=? AND id=?`
	if resourceType == "comment" {
		query = `SELECT COUNT(*) FROM comments c JOIN issues i ON i.id=c.issue_id WHERE i.workspace_id=? AND c.id=?`
	}
	if err := tx.QueryRowContext(r.Context(), query, ws.ID, resourceID).Scan(&exists); err != nil {
		s.fail(w, err)
		return
	}
	if exists != 1 {
		s.respondStoreError(w, sql.ErrNoRows)
		return
	}
	actorType, actorID := "member", LocalOperatorID
	if origin.AgentID != "" {
		actorType, actorID = "agent", origin.AgentID
	}
	if r.Method == http.MethodDelete {
		_, _ = tx.ExecContext(r.Context(), `DELETE FROM reactions WHERE workspace_id=? AND resource_type=? AND resource_id=? AND actor_type=? AND actor_id=? AND emoji=?`, ws.ID, resourceType, resourceID, actorType, actorID, input.Emoji)
		if err := commitLocalWakeupMutation(r.Context(), tx, ws.ID, cursor, origin); err != nil {
			s.fail(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	stamp := now()
	if _, err := tx.ExecContext(r.Context(), `INSERT INTO reactions(workspace_id, resource_type, resource_id, actor_type, actor_id, emoji, created_at) VALUES (?, ?, ?, ?, ?, ?, ?) ON CONFLICT DO NOTHING`, ws.ID, resourceType, resourceID, actorType, actorID, input.Emoji, stamp); err != nil {
		s.fail(w, err)
		return
	}
	if err := commitLocalWakeupMutation(r.Context(), tx, ws.ID, cursor, origin); err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"emoji": input.Emoji, "user_type": actorType, "user_id": actorID, "created_at": stamp})
}

func (s *Server) issueSubscribers(w http.ResponseWriter, r *http.Request) {
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
	if r.Method == http.MethodPost {
		var input map[string]any
		if err := decodeBody(r, &input); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		userID, userType := stringDefault(input["user_id"], LocalOperatorID), stringDefault(input["user_type"], "member")
		if strings.Contains(r.URL.Path, "/unsubscribe") {
			_, _ = s.store.db.ExecContext(r.Context(), `DELETE FROM issue_subscribers WHERE issue_id=? AND user_type=? AND user_id=?`, issueID, userType, userID)
		} else {
			_, err = s.store.db.ExecContext(r.Context(), `INSERT INTO issue_subscribers(issue_id, user_type, user_id, reason, created_at) VALUES (?, ?, ?, 'manual', ?) ON CONFLICT DO NOTHING`, issueID, userType, userID, now())
		}
		if err != nil {
			s.fail(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	rows, err := s.store.db.QueryContext(r.Context(), `SELECT issue_id, user_type, user_id, reason, created_at FROM issue_subscribers WHERE issue_id=? ORDER BY created_at, user_type, user_id`, issueID)
	if err != nil {
		s.fail(w, err)
		return
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var id, kind, userID, reason, createdAt string
		if err := rows.Scan(&id, &kind, &userID, &reason, &createdAt); err != nil {
			s.fail(w, err)
			return
		}
		items = append(items, map[string]any{"issue_id": id, "user_type": kind, "user_id": userID, "reason": reason, "created_at": createdAt})
	}
	writeJSON(w, http.StatusOK, items)
}

func (s *Server) updateOrDeleteComment(w http.ResponseWriter, r *http.Request) {
	ws, err := s.workspaceFor(r)
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	id := chi.URLParam(r, "commentID")
	var issueID, previousContent string
	if err := s.store.db.QueryRowContext(r.Context(), `SELECT c.issue_id,c.content FROM comments c JOIN issues i ON i.id=c.issue_id WHERE c.id=? AND i.workspace_id=?`, id, ws.ID).Scan(&issueID, &previousContent); err != nil {
		s.respondStoreError(w, err)
		return
	}
	if r.Method == http.MethodDelete {
		attachments, err := s.store.listAttachments(r.Context(), ws.ID, "", id)
		if err != nil {
			s.fail(w, err)
			return
		}
		type stagedFile struct{ original, staged string }
		staged := make([]stagedFile, 0, len(attachments))
		for _, attachment := range attachments {
			if attachment.storagePath == "" {
				continue
			}
			entry := stagedFile{original: attachment.storagePath, staged: filepath.Join("trash", uuid.NewString())}
			if err := s.files.Rename(entry.original, entry.staged); err != nil {
				for index := len(staged) - 1; index >= 0; index-- {
					_ = s.files.Rename(staged[index].staged, staged[index].original)
				}
				s.fail(w, err)
				return
			}
			staged = append(staged, entry)
		}
		tx, err := s.store.db.BeginTx(r.Context(), nil)
		if err != nil {
			for index := len(staged) - 1; index >= 0; index-- {
				_ = s.files.Rename(staged[index].staged, staged[index].original)
			}
			s.fail(w, err)
			return
		}
		defer func() { _ = tx.Rollback() }()
		cursor, origin, err := beginLocalWakeupMutation(r.Context(), tx, ws.ID)
		if err != nil {
			s.localWakeupError(w, err)
			return
		}

		_, _ = tx.ExecContext(r.Context(), `DELETE FROM reactions WHERE resource_type='comment' AND resource_id=?`, id)
		_, _ = tx.ExecContext(r.Context(), `DELETE FROM attachments WHERE comment_id=?`, id)
		if _, err := tx.ExecContext(r.Context(), `DELETE FROM comments WHERE id=?`, id); err != nil {
			for index := len(staged) - 1; index >= 0; index-- {
				_ = s.files.Rename(staged[index].staged, staged[index].original)
			}
			s.fail(w, err)
			return
		}
		if err := commitLocalWakeupMutation(r.Context(), tx, ws.ID, cursor, origin); err != nil {
			for index := len(staged) - 1; index >= 0; index-- {
				_ = s.files.Rename(staged[index].staged, staged[index].original)
			}
			s.fail(w, err)
			return
		}
		for _, entry := range staged {
			if err := s.files.Remove(entry.staged); err != nil {
				s.logger.Warn("remove staged comment attachment", "path", entry.staged, "error", err)
			}
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	var input struct {
		Content          string   `json:"content"`
		ExpectedRevision *int64   `json:"expected_revision"`
		SuppressAgentIDs []string `json:"suppress_agent_ids"`
	}
	if err := decodeBody(r, &input); err != nil || strings.TrimSpace(input.Content) == "" {
		writeError(w, http.StatusBadRequest, errors.New("content is required"))
		return
	}
	query, args := `UPDATE comments SET content=?, revision=revision+1, updated_at=? WHERE id=?`, []any{input.Content, now(), id}
	if input.ExpectedRevision != nil {
		query += ` AND revision=?`
		args = append(args, *input.ExpectedRevision)
	}
	tx, err := s.store.db.BeginTx(r.Context(), nil)
	if err != nil {
		s.fail(w, err)
		return
	}
	defer tx.Rollback()
	cursor, origin, err := beginLocalWakeupMutation(r.Context(), tx, ws.ID)
	if err != nil {
		s.localWakeupError(w, err)
		return
	}
	result, err := tx.ExecContext(r.Context(), query, args...)
	if err != nil {
		s.fail(w, err)
		return
	}
	if count, _ := result.RowsAffected(); count != 1 {
		writeError(w, http.StatusConflict, errors.New("comment was updated elsewhere"))
		return
	}
	if err := commitLocalWakeupMutation(r.Context(), tx, ws.ID, cursor, origin); err != nil {
		s.fail(w, err)
		return
	}
	item, _ := scanComment(s.store.db.QueryRowContext(r.Context(), `SELECT id, issue_id, author_type, author_id, content, type, parent_id, created_at, updated_at, revision, issue_revision, resolved_at, resolved_by_type, resolved_by_id, source_task_id, quick_action_id FROM comments WHERE id=?`, id))
	for _, mention := range util.ParseMentions(previousContent) {
		if target, err := s.store.resolveMentionTarget(r.Context(), ws.ID, mention); err == nil {
			input.SuppressAgentIDs = append(input.SuppressAgentIDs, target.AgentID)
		}
	}
	item.TriggerOutcomes, _ = s.dispatchCommentMentions(r.Context(), ws.ID, issueID, item.ID, input.Content, input.SuppressAgentIDs)
	writeJSON(w, http.StatusOK, item)
}

func sortedKeys(values map[string]any) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// Lifecycle presentation is separate from the stored status behavior. In
// particular, backlog and todo share a column category but schedule differently.
func localStatusCategory(value string) string {
	switch value {
	case "backlog", "todo":
		return "unstarted"
	case "in_progress", "in_review", "blocked":
		return "started"
	case "cancelled", "canceled":
		return "closed"
	default:
		return value
	}
}

func localStatusCategoryMatches(stored, requested string) bool {
	return stored == requested || localStatusCategory(stored) == requested
}
