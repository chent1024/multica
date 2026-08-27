package localworkbench

import (
	"database/sql"
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

func (s *Store) readSquad(id, workspaceID string) (map[string]any, error) {
	var raw, createdAt, updatedAt string
	var archivedAt sql.NullString
	if err := s.db.QueryRow(`SELECT data, archived_at, created_at, updated_at FROM squads WHERE id=? AND workspace_id=?`, id, workspaceID).Scan(&raw, &archivedAt, &createdAt, &updatedAt); err != nil {
		return nil, err
	}
	value, err := decodeJSONMap(raw)
	if err != nil {
		return nil, err
	}
	value["id"], value["workspace_id"] = id, workspaceID
	value["created_at"], value["updated_at"] = createdAt, updatedAt
	value["archived_at"], value["archived_by"] = nil, nil
	if archivedAt.Valid {
		value["archived_at"], value["archived_by"] = archivedAt.String, LocalOperatorID
	}
	rows, err := s.db.Query(`SELECT member_type, member_id, role FROM squad_members WHERE squad_id=? ORDER BY created_at, id LIMIT 5`, id)
	if err != nil {
		return nil, err
	}
	preview := []map[string]any{}
	for rows.Next() {
		var memberType, memberID, role string
		if err := rows.Scan(&memberType, &memberID, &role); err != nil {
			_ = rows.Close()
			return nil, err
		}
		preview = append(preview, map[string]any{"member_type": memberType, "member_id": memberID, "role": role})
	}
	_ = rows.Close()
	var count int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM squad_members WHERE squad_id=?`, id).Scan(&count); err != nil {
		return nil, err
	}
	value["member_count"], value["member_preview"] = count, preview
	return value, nil
}

func (s *Server) listSquads(w http.ResponseWriter, r *http.Request) {
	ws, err := s.workspaceFor(r)
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	rows, err := s.store.db.QueryContext(r.Context(), `SELECT id FROM squads WHERE workspace_id=? AND archived_at IS NULL ORDER BY updated_at DESC, id`, ws.ID)
	if err != nil {
		s.fail(w, err)
		return
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			s.fail(w, err)
			return
		}
		ids = append(ids, id)
	}
	_ = rows.Close()
	result := []map[string]any{}
	for _, id := range ids {
		item, err := s.store.readSquad(id, ws.ID)
		if err != nil {
			s.fail(w, err)
			return
		}
		result = append(result, item)
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) createSquad(w http.ResponseWriter, r *http.Request) {
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
	name, leaderID := strings.TrimSpace(stringFromAny(input["name"])), strings.TrimSpace(stringFromAny(input["leader_id"]))
	if name == "" || leaderID == "" {
		writeError(w, http.StatusBadRequest, errors.New("name and leader_id are required"))
		return
	}
	var exists int
	if err := s.store.db.QueryRowContext(r.Context(), `SELECT 1 FROM agents WHERE id=? AND workspace_id=? AND archived_at IS NULL`, leaderID, ws.ID).Scan(&exists); err != nil {
		s.respondStoreError(w, err)
		return
	}
	id, stamp := uuid.NewString(), now()
	value := map[string]any{"id": id, "workspace_id": ws.ID, "name": name, "description": "", "instructions": "", "avatar_url": nil, "leader_id": leaderID, "creator_id": LocalOperatorID, "created_at": stamp, "updated_at": stamp, "archived_at": nil, "archived_by": nil}
	mergeJSONMap(value, input, "id", "workspace_id", "creator_id", "created_at", "updated_at", "archived_at", "archived_by")
	raw, _ := encodeJSONMap(value)
	tx, err := s.store.db.BeginTx(r.Context(), nil)
	if err != nil {
		s.fail(w, err)
		return
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(r.Context(), `INSERT INTO squads(id, workspace_id, name, leader_id, creator_id, data, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, id, ws.ID, name, leaderID, LocalOperatorID, raw, stamp, stamp); err != nil {
		s.fail(w, err)
		return
	}
	if _, err := tx.ExecContext(r.Context(), `INSERT INTO squad_members(id, squad_id, member_type, member_id, role, created_at) VALUES (?, ?, 'agent', ?, 'leader', ?)`, uuid.NewString(), id, leaderID, stamp); err != nil {
		s.fail(w, err)
		return
	}
	if err := tx.Commit(); err != nil {
		s.fail(w, err)
		return
	}
	item, _ := s.store.readSquad(id, ws.ID)
	s.hub.broadcast("squad:created", map[string]any{"squad": item})
	writeJSON(w, http.StatusCreated, item)
}

func (s *Server) squadItem(w http.ResponseWriter, r *http.Request) {
	ws, err := s.workspaceFor(r)
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	id := chi.URLParam(r, "id")
	item, err := s.store.readSquad(id, ws.ID)
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	if r.Method == http.MethodGet {
		writeJSON(w, http.StatusOK, item)
		return
	}
	if r.Method == http.MethodDelete {
		var references int
		if err := s.store.db.QueryRowContext(r.Context(), `SELECT
			(SELECT COUNT(*) FROM issues WHERE workspace_id=? AND assignee_type='squad' AND assignee_id=?) +
			(SELECT COUNT(*) FROM automations WHERE workspace_id=? AND assignee_type='squad' AND assignee_id=?)`, ws.ID, id, ws.ID, id).Scan(&references); err != nil {
			s.fail(w, err)
			return
		}
		if references > 0 {
			writeError(w, http.StatusConflict, errors.New("squad is still assigned"))
			return
		}
		tx, err := s.store.db.BeginTx(r.Context(), nil)
		if err != nil {
			s.fail(w, err)
			return
		}
		defer func() { _ = tx.Rollback() }()
		_, _ = tx.ExecContext(r.Context(), `DELETE FROM squad_members WHERE squad_id=?`, id)
		if _, err := tx.ExecContext(r.Context(), `DELETE FROM squads WHERE id=? AND workspace_id=?`, id, ws.ID); err != nil {
			s.fail(w, err)
			return
		}
		if err := tx.Commit(); err != nil {
			s.fail(w, err)
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
	if leaderID := strings.TrimSpace(stringFromAny(input["leader_id"])); leaderID != "" {
		var exists int
		if err := s.store.db.QueryRowContext(r.Context(), `SELECT 1 FROM agents WHERE id=? AND workspace_id=? AND archived_at IS NULL`, leaderID, ws.ID).Scan(&exists); err != nil {
			s.respondStoreError(w, err)
			return
		}
		item["leader_id"] = leaderID
	}
	mergeJSONMap(item, input, "id", "workspace_id", "creator_id", "created_at", "updated_at", "archived_at", "archived_by", "member_count", "member_preview")
	stamp := now()
	item["updated_at"] = stamp
	raw, _ := encodeJSONMap(item)
	if _, err := s.store.db.ExecContext(r.Context(), `UPDATE squads SET name=?, leader_id=?, data=?, updated_at=? WHERE id=? AND workspace_id=?`, stringFromAny(item["name"]), stringFromAny(item["leader_id"]), raw, stamp, id, ws.ID); err != nil {
		s.fail(w, err)
		return
	}
	updated, _ := s.store.readSquad(id, ws.ID)
	writeJSON(w, http.StatusOK, updated)
}

func (s *Server) squadMembers(w http.ResponseWriter, r *http.Request) {
	ws, err := s.workspaceFor(r)
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	squadID := chi.URLParam(r, "id")
	if _, err := s.store.readSquad(squadID, ws.ID); err != nil {
		s.respondStoreError(w, err)
		return
	}
	if r.Method == http.MethodGet {
		rows, err := s.store.db.QueryContext(r.Context(), `SELECT id, member_type, member_id, role, created_at FROM squad_members WHERE squad_id=? ORDER BY created_at, id`, squadID)
		if err != nil {
			s.fail(w, err)
			return
		}
		items := []map[string]any{}
		for rows.Next() {
			var id, kind, memberID, role, createdAt string
			if err := rows.Scan(&id, &kind, &memberID, &role, &createdAt); err != nil {
				_ = rows.Close()
				s.fail(w, err)
				return
			}
			items = append(items, map[string]any{"id": id, "squad_id": squadID, "member_type": kind, "member_id": memberID, "role": role, "created_at": createdAt})
		}
		_ = rows.Close()
		writeJSON(w, http.StatusOK, items)
		return
	}
	input := struct {
		MemberType string `json:"member_type"`
		MemberID   string `json:"member_id"`
		Role       string `json:"role"`
	}{}
	if err := decodeBody(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if input.MemberType != "agent" && input.MemberType != "member" {
		writeError(w, http.StatusBadRequest, errors.New("member_type must be agent or member"))
		return
	}
	if input.MemberType == "agent" {
		var exists int
		if err := s.store.db.QueryRowContext(r.Context(), `SELECT 1 FROM agents WHERE id=? AND workspace_id=? AND archived_at IS NULL`, input.MemberID, ws.ID).Scan(&exists); err != nil {
			s.respondStoreError(w, err)
			return
		}
	} else if input.MemberID != LocalOperatorID {
		writeError(w, http.StatusBadRequest, errors.New("unknown local member"))
		return
	}
	if r.Method == http.MethodDelete {
		_, err = s.store.db.ExecContext(r.Context(), `DELETE FROM squad_members WHERE squad_id=? AND member_type=? AND member_id=?`, squadID, input.MemberType, input.MemberID)
		if err != nil {
			s.fail(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if r.Method == http.MethodPatch {
		var id, createdAt string
		err = s.store.db.QueryRowContext(r.Context(), `UPDATE squad_members SET role=? WHERE squad_id=? AND member_type=? AND member_id=? RETURNING id, created_at`, input.Role, squadID, input.MemberType, input.MemberID).Scan(&id, &createdAt)
		if err != nil {
			s.respondStoreError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"id": id, "squad_id": squadID, "member_type": input.MemberType, "member_id": input.MemberID, "role": input.Role, "created_at": createdAt})
		return
	}
	if input.Role == "" {
		input.Role = "member"
	}
	id, stamp := uuid.NewString(), now()
	_, err = s.store.db.ExecContext(r.Context(), `INSERT INTO squad_members(id, squad_id, member_type, member_id, role, created_at) VALUES (?, ?, ?, ?, ?, ?)`, id, squadID, input.MemberType, input.MemberID, input.Role, stamp)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			writeError(w, http.StatusConflict, errors.New("member already belongs to squad"))
		} else {
			s.fail(w, err)
		}
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"id": id, "squad_id": squadID, "member_type": input.MemberType, "member_id": input.MemberID, "role": input.Role, "created_at": stamp})
}

func (s *Server) squadMemberStatus(w http.ResponseWriter, r *http.Request) {
	ws, err := s.workspaceFor(r)
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	squadID := chi.URLParam(r, "id")
	if _, err := s.store.readSquad(squadID, ws.ID); err != nil {
		s.respondStoreError(w, err)
		return
	}
	rows, err := s.store.db.QueryContext(r.Context(), `SELECT member_type, member_id FROM squad_members WHERE squad_id=? ORDER BY created_at, id`, squadID)
	if err != nil {
		s.fail(w, err)
		return
	}
	items := []map[string]any{}
	for rows.Next() {
		var kind, memberID string
		if err := rows.Scan(&kind, &memberID); err != nil {
			_ = rows.Close()
			s.fail(w, err)
			return
		}
		var status any
		if kind == "agent" {
			status = "offline"
			var runtimeStatus string
			_ = s.store.db.QueryRowContext(r.Context(), `SELECT r.status FROM agents a JOIN runtimes r ON r.id=a.runtime_id WHERE a.id=? AND a.workspace_id=?`, memberID, ws.ID).Scan(&runtimeStatus)
			if runtimeStatus == "online" {
				status = "idle"
			}
		}
		items = append(items, map[string]any{"member_type": kind, "member_id": memberID, "status": status, "active_issues": []any{}, "last_active_at": nil})
	}
	_ = rows.Close()
	writeJSON(w, http.StatusOK, map[string]any{"members": items})
}
