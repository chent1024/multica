package localworkbench

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/pkg/plugincontract"
	"github.com/multica-ai/multica/server/pkg/remotemcp"
)

func decodeLocalPluginManifest(item map[string]any) (plugincontract.Manifest, error) {
	raw, err := json.Marshal(item["manifest"])
	if err != nil {
		return plugincontract.Manifest{}, err
	}
	var manifest plugincontract.Manifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return plugincontract.Manifest{}, err
	}
	return manifest, nil
}

func hookAllowsTrigger(hook plugincontract.Hook, trigger string) bool {
	for _, candidate := range hook.Triggers {
		if candidate == trigger {
			return true
		}
	}
	return false
}

func findLocalPluginHook(manifest plugincontract.Manifest, hookKey string) (plugincontract.Hook, bool) {
	for _, hook := range manifest.Contributes.Hooks {
		if hook.Key == hookKey {
			return hook, true
		}
	}
	return plugincontract.Hook{}, false
}

func stringSlice(value any) []string {
	raw, _ := value.([]any)
	result := make([]string, 0, len(raw))
	for _, item := range raw {
		if text, ok := item.(string); ok {
			result = append(result, text)
		}
	}
	return result
}

func bundleData(bundle plugincontract.Bundle, archive []byte, packageID, versionID, stamp string) map[string]any {
	manifestRaw, _ := json.Marshal(bundle.Manifest)
	manifest := map[string]any{}
	_ = json.Unmarshal(manifestRaw, &manifest)
	digest := sha256.Sum256(archive)
	return map[string]any{"id": packageID, "plugin_key": bundle.Manifest.Key, "name": bundle.Manifest.Name, "manifest": manifest, "versions": []any{map[string]any{"id": versionID, "version": bundle.Manifest.Version, "digest": hex.EncodeToString(digest[:]), "size_bytes": len(archive), "published_at": stamp, "installed": false}}, "created_at": stamp}
}

func (s *Server) publishPluginPackage(w http.ResponseWriter, r *http.Request) {
	ws, err := s.workspaceFor(r)
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, plugincontract.MaxBundleSize+(1<<20))
	if err := r.ParseMultipartForm(1 << 20); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid plugin upload: %w", err))
		return
	}
	file, _, err := r.FormFile("bundle")
	if err != nil {
		writeError(w, http.StatusBadRequest, errors.New("bundle is required"))
		return
	}
	defer file.Close()
	archive, err := io.ReadAll(io.LimitReader(file, plugincontract.MaxBundleSize+1))
	if err != nil || len(archive) > plugincontract.MaxBundleSize {
		writeError(w, http.StatusRequestEntityTooLarge, errors.New("plugin bundle is too large"))
		return
	}
	s.publishPluginBundle(w, r, ws.ID, archive)
}

func (s *Server) publishLocalPluginPackage(w http.ResponseWriter, r *http.Request) {
	ws, err := s.workspaceFor(r)
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	input := struct {
		Name string `json:"name"`
	}{}
	if err := decodeBody(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if input.Name == "" || filepath.Base(input.Name) != input.Name {
		writeError(w, http.StatusBadRequest, errors.New("local plugin name must be one directory name"))
		return
	}
	root := strings.TrimSpace(os.Getenv("MULTICA_PLUGIN_DIR"))
	if root == "" {
		writeError(w, http.StatusFailedDependency, errors.New("MULTICA_PLUGIN_DIR is not configured for local plugin sources"))
		return
	}
	dir := filepath.Join(root, input.Name)
	bundle, err := plugincontract.ParseBundleFromDir(func(entry string) ([]byte, bool, error) {
		data, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(entry)))
		if errors.Is(err, os.ErrNotExist) {
			return nil, false, nil
		}
		return data, err == nil, err
	})
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	archive, err := zipPluginBundle(bundle)
	if err != nil {
		s.fail(w, err)
		return
	}
	s.publishParsedPluginBundle(w, r, ws.ID, bundle, archive)
}

func zipPluginBundle(bundle plugincontract.Bundle) ([]byte, error) {
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	manifest, _ := json.Marshal(bundle.Manifest)
	files := append([]plugincontract.BundleFile{{Path: plugincontract.ManifestFilename, Content: manifest}}, bundle.Files...)
	for _, file := range files {
		entry, err := writer.Create(file.Path)
		if err != nil {
			return nil, err
		}
		if _, err := entry.Write(file.Content); err != nil {
			return nil, err
		}
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}

func (s *Server) publishPluginBundle(w http.ResponseWriter, r *http.Request, workspaceID string, archive []byte) {
	bundle, err := plugincontract.ParseBundle(archive)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	s.publishParsedPluginBundle(w, r, workspaceID, bundle, archive)
}

func (s *Server) publishParsedPluginBundle(w http.ResponseWriter, r *http.Request, workspaceID string, bundle plugincontract.Bundle, archive []byte) {
	packageID, versionID, stamp := uuid.NewString(), uuid.NewString(), now()
	var existingID, existingRaw, createdAt string
	err := s.store.db.QueryRowContext(r.Context(), `SELECT id, data, created_at FROM plugin_packages WHERE workspace_id=? AND plugin_key=?`, workspaceID, bundle.Manifest.Key).Scan(&existingID, &existingRaw, &createdAt)
	data := bundleData(bundle, archive, packageID, versionID, stamp)
	if err == nil {
		packageID = existingID
		existing, _ := decodeJSONMap(existingRaw)
		versions, _ := existing["versions"].([]any)
		next := data["versions"].([]any)[0].(map[string]any)
		for _, raw := range versions {
			if item, ok := raw.(map[string]any); ok && item["version"] == bundle.Manifest.Version {
				writeError(w, http.StatusConflict, errors.New("this plugin version is already published"))
				return
			}
		}
		data["id"], data["created_at"], data["versions"] = packageID, createdAt, append([]any{next}, versions...)
	} else if !errors.Is(err, sql.ErrNoRows) {
		s.fail(w, err)
		return
	}
	storagePath := filepath.Join("plugins", versionID+".zip")
	if err := s.files.Put(r.Context(), storagePath, bytes.NewReader(archive)); err != nil {
		s.fail(w, err)
		return
	}
	raw, _ := encodeJSONMap(data)
	versionData, _ := encodeJSONMap(map[string]any{"manifest": data["manifest"], "version": data["versions"].([]any)[0]})
	version := data["versions"].([]any)[0].(map[string]any)
	tx, err := s.store.db.BeginTx(r.Context(), nil)
	if err == nil {
		_, err = tx.ExecContext(r.Context(), `INSERT INTO plugin_packages(id, workspace_id, plugin_key, name, data, bundle_path, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT(workspace_id, plugin_key) DO UPDATE SET name=excluded.name, data=excluded.data, bundle_path=excluded.bundle_path, updated_at=excluded.updated_at`, packageID, workspaceID, bundle.Manifest.Key, bundle.Manifest.Name, raw, storagePath, data["created_at"], stamp)
	}
	if err == nil {
		_, err = tx.ExecContext(r.Context(), `INSERT INTO plugin_package_versions(id,package_id,version,digest,bundle_path,data,created_at) VALUES(?,?,?,?,?,?,?)`, versionID, packageID, bundle.Manifest.Version, version["digest"], storagePath, versionData, stamp)
	}
	if err == nil {
		err = tx.Commit()
	} else if tx != nil {
		_ = tx.Rollback()
	}
	if err != nil {
		_ = s.files.Remove(storagePath)
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, data)
}

func (s *Server) listPluginPackages(w http.ResponseWriter, r *http.Request) {
	ws, err := s.workspaceFor(r)
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	rows, err := s.store.db.QueryContext(r.Context(), `SELECT data FROM plugin_packages WHERE workspace_id=? ORDER BY updated_at DESC,id`, ws.ID)
	if err != nil {
		s.fail(w, err)
		return
	}
	items := []map[string]any{}
	for rows.Next() {
		var raw string
		_ = rows.Scan(&raw)
		item, _ := decodeJSONMap(raw)
		items = append(items, item)
	}
	_ = rows.Close()
	writeJSON(w, http.StatusOK, map[string]any{"packages": items})
}

func (s *Server) deletePluginPackage(w http.ResponseWriter, r *http.Request) {
	ws, _ := s.workspaceFor(r)
	id := chi.URLParam(r, "packageID")
	var count int
	_ = s.store.db.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM plugin_installations WHERE workspace_id=? AND package_id=?`, ws.ID, id).Scan(&count)
	if count > 0 {
		writeError(w, http.StatusConflict, errors.New("uninstall the plugin before deleting its package"))
		return
	}
	rows, err := s.store.db.QueryContext(r.Context(), `SELECT v.bundle_path FROM plugin_package_versions v JOIN plugin_packages p ON p.id=v.package_id WHERE p.id=? AND p.workspace_id=?`, id, ws.ID)
	if err != nil {
		s.fail(w, err)
		return
	}
	paths := []string{}
	for rows.Next() {
		var path string
		if err := rows.Scan(&path); err != nil {
			_ = rows.Close()
			s.fail(w, err)
			return
		}
		paths = append(paths, path)
	}
	_ = rows.Close()
	var exists int
	if err := s.store.db.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM plugin_packages WHERE id=? AND workspace_id=?`, id, ws.ID).Scan(&exists); err != nil || exists == 0 {
		if err == nil {
			err = sql.ErrNoRows
		}
		s.respondStoreError(w, err)
		return
	}
	tx, err := s.store.db.BeginTx(r.Context(), nil)
	if err == nil {
		_, err = tx.ExecContext(r.Context(), `DELETE FROM plugin_package_versions WHERE package_id=?`, id)
	}
	if err == nil {
		_, err = tx.ExecContext(r.Context(), `DELETE FROM plugin_packages WHERE id=?`, id)
	}
	if err == nil {
		err = tx.Commit()
	} else if tx != nil {
		_ = tx.Rollback()
	}
	if err != nil {
		s.fail(w, err)
		return
	}
	for _, path := range paths {
		_ = s.files.Remove(path)
	}
	w.WriteHeader(http.StatusNoContent)
}

func findPluginVersion(data map[string]any, versionID string) (map[string]any, bool) {
	versions, _ := data["versions"].([]any)
	for _, raw := range versions {
		if item, ok := raw.(map[string]any); ok && item["id"] == versionID {
			return item, true
		}
	}
	return nil, false
}

func (s *Server) previewPlugin(w http.ResponseWriter, r *http.Request) {
	ws, _ := s.workspaceFor(r)
	input := struct {
		VersionID string `json:"version_id"`
	}{}
	if err := decodeBody(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	var raw string
	if err := s.store.db.QueryRowContext(r.Context(), `SELECT v.data FROM plugin_package_versions v JOIN plugin_packages p ON p.id=v.package_id WHERE v.id=? AND p.workspace_id=?`, input.VersionID, ws.ID).Scan(&raw); err != nil {
		s.respondStoreError(w, err)
		return
	}
	data, _ := decodeJSONMap(raw)
	manifest, _ := data["manifest"].(map[string]any)
	version, _ := data["version"].(map[string]any)
	scopes, _ := manifest["scopes"].([]any)
	writeJSON(w, http.StatusOK, map[string]any{"manifest": manifest, "scopes": scopes, "config_schema": pluginConfigSchema(manifest), "version_id": input.VersionID, "version": version["version"], "digest": version["digest"], "installed": false, "added_scopes": scopes})
}

func pluginConfigSchema(manifest map[string]any) []any {
	config, _ := manifest["config"].(map[string]any)
	result := []any{}
	for key, raw := range config {
		field, _ := raw.(map[string]any)
		copy := map[string]any{"key": key, "type": "string", "label": key, "required": false, "options": []any{}}
		mergeJSONMap(copy, field)
		result = append(result, copy)
	}
	return result
}

func pluginInstallationFromData(id, workspaceID, packageID string, enabled bool, data map[string]any, stamp string) map[string]any {
	manifest, _ := data["manifest"].(map[string]any)
	contributes, _ := manifest["contributes"].(map[string]any)
	return map[string]any{"id": id, "plugin_key": manifest["key"], "name": manifest["name"], "description": manifest["description"], "version": manifest["version"], "package_version_id": packageID, "enabled": enabled, "granted_scopes": []any{}, "config_schema": pluginConfigSchema(manifest), "config": map[string]any{}, "configured_secrets": []any{}, "surfaces": arrayFromMap(contributes, "surfaces"), "hooks": arrayFromMap(contributes, "hooks"), "resources": arrayFromMap(contributes, "resources"), "workspace_id": workspaceID, "created_at": stamp, "updated_at": stamp, "manifest": manifest}
}
func arrayFromMap(value map[string]any, key string) []any {
	result, _ := value[key].([]any)
	if result == nil {
		return []any{}
	}
	return result
}

func (s *Server) pluginInstallations(w http.ResponseWriter, r *http.Request) {
	ws, _ := s.workspaceFor(r)
	if r.Method == http.MethodGet {
		rows, err := s.store.db.QueryContext(r.Context(), `SELECT data FROM plugin_installations WHERE workspace_id=? ORDER BY updated_at DESC,id`, ws.ID)
		if err != nil {
			s.fail(w, err)
			return
		}
		items := []map[string]any{}
		for rows.Next() {
			var raw string
			_ = rows.Scan(&raw)
			item, _ := decodeJSONMap(raw)
			delete(item, "manifest")
			items = append(items, item)
		}
		_ = rows.Close()
		writeJSON(w, http.StatusOK, map[string]any{"plugins": items})
		return
	}
	input := struct {
		VersionID     string   `json:"version_id"`
		GrantedScopes []string `json:"granted_scopes"`
	}{}
	if err := decodeBody(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	var packageID, versionRaw string
	if err := s.store.db.QueryRowContext(r.Context(), `SELECT v.package_id,v.data FROM plugin_package_versions v JOIN plugin_packages p ON p.id=v.package_id WHERE v.id=? AND p.workspace_id=?`, input.VersionID, ws.ID).Scan(&packageID, &versionRaw); err != nil {
		s.respondStoreError(w, err)
		return
	}
	versionData, _ := decodeJSONMap(versionRaw)
	data := map[string]any{"manifest": versionData["manifest"]}
	id, createdAt, stamp := uuid.NewString(), "", now()
	pluginKey := stringFromAny(versionData["manifest"].(map[string]any)["key"])
	err := s.store.db.QueryRowContext(r.Context(), `SELECT id,created_at FROM plugin_installations WHERE workspace_id=? AND plugin_key=?`, ws.ID, pluginKey).Scan(&id, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		createdAt, err = stamp, nil
	}
	if err != nil {
		s.fail(w, err)
		return
	}
	item := pluginInstallationFromData(id, ws.ID, input.VersionID, false, data, createdAt)
	item["updated_at"] = stamp
	item["granted_scopes"] = input.GrantedScopes
	encoded, _ := encodeJSONMap(item)
	_, err = s.store.db.ExecContext(r.Context(), `INSERT INTO plugin_installations(id,workspace_id,package_id,plugin_key,enabled,data,created_at,updated_at,package_version_id) VALUES(?,?,?,?,0,?,?,?,?) ON CONFLICT(workspace_id,plugin_key) DO UPDATE SET package_id=excluded.package_id,package_version_id=excluded.package_version_id,enabled=0,data=excluded.data,updated_at=excluded.updated_at`, id, ws.ID, packageID, item["plugin_key"], encoded, createdAt, stamp, input.VersionID)
	if err != nil {
		s.fail(w, err)
		return
	}
	delete(item, "manifest")
	writeJSON(w, http.StatusCreated, item)
}

func (s *Server) readPluginInstallation(ctx context.Context, workspaceID, id string) (map[string]any, error) {
	var raw string
	err := s.store.db.QueryRowContext(ctx, `SELECT data FROM plugin_installations WHERE id=? AND workspace_id=?`, id, workspaceID).Scan(&raw)
	if err != nil {
		return nil, err
	}
	return decodeJSONMap(raw)
}

func (s *Server) configurePlugin(w http.ResponseWriter, r *http.Request) {
	ws, _ := s.workspaceFor(r)
	id := chi.URLParam(r, "installationID")
	item, err := s.readPluginInstallation(r.Context(), ws.ID, id)
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	input := struct {
		Values map[string]any `json:"values"`
	}{}
	if err := decodeBody(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	public, secrets := map[string]any{}, map[string]any{}
	for _, raw := range item["config_schema"].([]any) {
		field, _ := raw.(map[string]any)
		key := stringFromAny(field["key"])
		value, ok := input.Values[key]
		if !ok {
			continue
		}
		if field["type"] == "secret" {
			secrets[key] = value
		} else {
			public[key] = value
		}
	}
	item["config"] = public
	keys := []string{}
	for key := range secrets {
		keys = append(keys, key)
	}
	item["configured_secrets"] = keys
	item["updated_at"] = now()
	raw, _ := encodeJSONMap(item)
	secretRaw, _ := encodeJSONMap(secrets)
	if _, err := s.store.db.ExecContext(r.Context(), `UPDATE plugin_installations SET data=?,secret_config=?,updated_at=? WHERE id=? AND workspace_id=?`, raw, secretRaw, item["updated_at"], id, ws.ID); err != nil {
		s.fail(w, err)
		return
	}
	delete(item, "manifest")
	writeJSON(w, http.StatusOK, item)
}

func (s *Server) uninstallPlugin(w http.ResponseWriter, r *http.Request) {
	ws, _ := s.workspaceFor(r)
	id := chi.URLParam(r, "installationID")
	tx, err := s.store.db.BeginTx(r.Context(), nil)
	if err != nil {
		s.fail(w, err)
		return
	}
	defer func() { _ = tx.Rollback() }()
	var exists int
	if err := tx.QueryRowContext(r.Context(), `SELECT 1 FROM plugin_installations WHERE id=? AND workspace_id=?`, id, ws.ID).Scan(&exists); err != nil {
		s.respondStoreError(w, err)
		return
	}
	for _, statement := range []string{
		`DELETE FROM plugin_storage WHERE installation_id=?`,
		`DELETE FROM plugin_invocations WHERE installation_id=?`,
		`DELETE FROM plugin_installations WHERE id=?`,
	} {
		if _, err := tx.ExecContext(r.Context(), statement, id); err != nil {
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

func (s *Server) setPluginEnabled(w http.ResponseWriter, r *http.Request) {
	ws, _ := s.workspaceFor(r)
	id := chi.URLParam(r, "installationID")
	enabled := chi.URLParam(r, "action") == "enable"
	item, err := s.readPluginInstallation(r.Context(), ws.ID, id)
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	item["enabled"], item["updated_at"] = enabled, now()
	raw, _ := encodeJSONMap(item)
	if _, err := s.store.db.ExecContext(r.Context(), `UPDATE plugin_installations SET enabled=?,data=?,updated_at=? WHERE id=?`, enabled, raw, item["updated_at"], id); err != nil {
		s.fail(w, err)
		return
	}
	delete(item, "manifest")
	writeJSON(w, http.StatusOK, item)
}

func (s *Server) pluginInvocations(w http.ResponseWriter, r *http.Request) {
	ws, _ := s.workspaceFor(r)
	id := chi.URLParam(r, "installationID")
	if _, err := s.readPluginInstallation(r.Context(), ws.ID, id); err != nil {
		s.respondStoreError(w, err)
		return
	}
	rows, _ := s.store.db.QueryContext(r.Context(), `SELECT data FROM plugin_invocations WHERE installation_id=? ORDER BY created_at DESC,id LIMIT 100`, id)
	items := []map[string]any{}
	for rows.Next() {
		var raw string
		_ = rows.Scan(&raw)
		item, _ := decodeJSONMap(raw)
		items = append(items, item)
	}
	_ = rows.Close()
	writeJSON(w, http.StatusOK, map[string]any{"invocations": items})
}

func (s *Server) pluginToken(w http.ResponseWriter, r *http.Request) {
	ws, _ := s.workspaceFor(r)
	id := chi.URLParam(r, "installationID")
	if _, err := s.readPluginInstallation(r.Context(), ws.ID, id); err != nil {
		s.respondStoreError(w, err)
		return
	}
	if r.Method == http.MethodDelete {
		_, _ = s.store.db.ExecContext(r.Context(), `UPDATE plugin_installations SET secret_config='{}',updated_at=? WHERE id=?`, now(), id)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	token, secret := uuid.NewString(), uuid.NewString()
	raw, _ := json.Marshal(map[string]any{"bridge_token": token, "signing_secret": secret})
	_, _ = s.store.db.ExecContext(r.Context(), `UPDATE plugin_installations SET secret_config=?,updated_at=? WHERE id=?`, string(raw), now(), id)
	writeJSON(w, http.StatusOK, map[string]any{"token": token, "signing_secret": secret})
}

func (s *Server) configurePluginMCPTools(w http.ResponseWriter, r *http.Request) {
	ws, _ := s.workspaceFor(r)
	item, err := s.readPluginInstallation(r.Context(), ws.ID, chi.URLParam(r, "installationID"))
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	input := struct {
		Tools []string `json:"tools"`
	}{}
	if err := decodeBody(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	discovered, err := s.discoverLocalPluginMCPTools(r.Context(), item, chi.URLParam(r, "hookKey"))
	if err != nil {
		writeError(w, http.StatusFailedDependency, err)
		return
	}
	byName := make(map[string]remotemcp.Tool, len(discovered))
	for _, tool := range discovered {
		byName[tool.Name] = tool
	}
	selected := make([]remotemcp.Tool, 0, len(input.Tools))
	for _, name := range input.Tools {
		tool, ok := byName[name]
		if !ok {
			writeError(w, http.StatusBadRequest, fmt.Errorf("tool %q is not offered by this MCP server", name))
			return
		}
		selected = append(selected, tool)
	}
	approved, _ := item["approved_mcp_tools"].(map[string]any)
	if approved == nil {
		approved = map[string]any{}
	}
	if len(selected) == 0 {
		delete(approved, chi.URLParam(r, "hookKey"))
	} else {
		approved[chi.URLParam(r, "hookKey")] = selected
	}
	item["approved_mcp_tools"] = approved
	raw, _ := encodeJSONMap(item)
	_, _ = s.store.db.ExecContext(r.Context(), `UPDATE plugin_installations SET data=?,updated_at=? WHERE id=?`, raw, now(), chi.URLParam(r, "installationID"))
	delete(item, "manifest")
	writeJSON(w, http.StatusOK, item)
}

func (s *Server) listPluginMCPTools(w http.ResponseWriter, r *http.Request) {
	ws, err := s.workspaceFor(r)
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	item, err := s.readPluginInstallation(r.Context(), ws.ID, chi.URLParam(r, "installationID"))
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	hookKey := chi.URLParam(r, "hookKey")
	discovered, err := s.discoverLocalPluginMCPTools(r.Context(), item, hookKey)
	if err != nil {
		writeError(w, http.StatusFailedDependency, err)
		return
	}
	approved := approvedLocalPluginMCPTools(item, hookKey)
	items := make([]map[string]any, 0, len(discovered))
	for _, tool := range discovered {
		pinned, ok := approved[tool.Name]
		items = append(items, map[string]any{
			"name": tool.Name, "description": tool.Description, "schema_digest": tool.SchemaDigest,
			"approved": ok, "drifted": ok && pinned.SchemaDigest != tool.SchemaDigest,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"tools": items})
}

func approvedLocalPluginMCPTools(item map[string]any, hookKey string) map[string]remotemcp.Tool {
	result := map[string]remotemcp.Tool{}
	approved, _ := item["approved_mcp_tools"].(map[string]any)
	raw, ok := approved[hookKey]
	if !ok {
		return result
	}
	encoded, _ := json.Marshal(raw)
	tools := []remotemcp.Tool{}
	if json.Unmarshal(encoded, &tools) != nil {
		return result
	}
	for _, tool := range tools {
		result[tool.Name] = tool
	}
	return result
}

func (s *Server) discoverLocalPluginMCPTools(ctx context.Context, item map[string]any, hookKey string) ([]remotemcp.Tool, error) {
	manifest, err := decodeLocalPluginManifest(item)
	if err != nil {
		return nil, err
	}
	hook, ok := findLocalPluginHook(manifest, hookKey)
	if !ok {
		return nil, sql.ErrNoRows
	}
	if hook.Transport.Type != plugincontract.TransportMCP {
		return nil, fmt.Errorf("hook %q is not an MCP transport", hookKey)
	}
	domains := plugincontract.NetDomains(stringSlice(item["granted_scopes"]))
	if len(domains) == 0 {
		return nil, errors.New("plugin has no granted net scope for its MCP endpoint")
	}
	headers := make(http.Header)
	if secret := s.localPluginMCPSecret(ctx, stringFromAny(item["id"]), hookKey); secret != "" {
		headers.Set("Authorization", secret)
	}
	tools, _, err := remotemcp.Discover(ctx, hook.Transport.URL, domains, nil, headers)
	return tools, err
}

func (s *Server) localPluginMCPSecret(ctx context.Context, installationID, hookKey string) string {
	var raw string
	if err := s.store.db.QueryRowContext(ctx, `SELECT secret_config FROM plugin_installations WHERE id=? AND workspace_id=?`, installationID, LocalWorkspaceID).Scan(&raw); err != nil {
		return ""
	}
	values := map[string]any{}
	if json.Unmarshal([]byte(raw), &values) != nil {
		return ""
	}
	return stringFromAny(values[hookKey+"_credential"])
}

func (s *Server) localPluginClaimContext(ctx context.Context, workspaceID string) ([]map[string]any, []remotemcp.Connection, error) {
	rows, err := s.store.db.QueryContext(ctx, `SELECT data FROM plugin_installations WHERE workspace_id=? AND enabled=1 ORDER BY plugin_key,id`, workspaceID)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	tools := []map[string]any{}
	connections := []remotemcp.Connection{}
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, nil, err
		}
		item, err := decodeJSONMap(raw)
		if err != nil {
			continue
		}
		manifest, err := decodeLocalPluginManifest(item)
		if err != nil {
			continue
		}
		installationID := stringFromAny(item["id"])
		for _, hook := range manifest.Contributes.Hooks {
			if !hookAllowsTrigger(hook, plugincontract.TriggerAgent) {
				continue
			}
			toolName := service.PluginToolName(manifest.Key, hook.Key)
			if hook.Transport.Type == plugincontract.TransportHTTP {
				tools = append(tools, map[string]any{
					"installation_id": installationID, "hook_key": hook.Key, "name": toolName,
					"description": hook.Description, "input_schema": hook.InputSchema,
				})
				continue
			}
			if hook.Transport.Type != plugincontract.TransportMCP {
				continue
			}
			approvedByName := approvedLocalPluginMCPTools(item, hook.Key)
			if len(approvedByName) == 0 {
				continue
			}
			approved := make([]remotemcp.Tool, 0, len(approvedByName))
			for _, tool := range approvedByName {
				approved = append(approved, tool)
			}
			sort.Slice(approved, func(i, j int) bool { return approved[i].Name < approved[j].Name })
			digest, err := remotemcp.ToolSetDigest(approved)
			if err != nil {
				return nil, nil, err
			}
			credentialHeader := ""
			if field, ok := manifest.Config.Field(hook.Key + "_credential"); ok && field.Type == plugincontract.ConfigSecret {
				credentialHeader = "Authorization"
			}
			connections = append(connections, remotemcp.Connection{
				InstallationID: installationID, ContributionID: remotemcp.PluginContributionPrefix + installationID + ":" + hook.Key,
				ContributionKey: toolName, Endpoint: hook.Transport.URL, Transport: "http",
				EndpointAllowedHosts: plugincontract.NetDomains(stringSlice(item["granted_scopes"])),
				CredentialHeader:     credentialHeader, ApprovedTools: approved, ToolSchemaDigest: digest, FailurePolicy: "optional",
			})
		}
	}
	return tools, connections, rows.Err()
}

func (s *Server) resolvePluginMCPCredential(w http.ResponseWriter, r *http.Request) {
	if _, err := s.daemonTaskRef(r.Context(), LocalWorkspaceID, chi.URLParam(r, "taskID")); err != nil {
		s.respondStoreError(w, err)
		return
	}
	contribution := chi.URLParam(r, "contributionID")
	trimmed, ok := strings.CutPrefix(contribution, remotemcp.PluginContributionPrefix)
	if !ok {
		writeError(w, http.StatusBadRequest, errors.New("malformed plugin MCP contribution id"))
		return
	}
	installationID, hookKey, ok := strings.Cut(trimmed, ":")
	if !ok || installationID == "" || hookKey == "" {
		writeError(w, http.StatusBadRequest, errors.New("malformed plugin MCP contribution id"))
		return
	}
	item, err := s.readPluginInstallation(r.Context(), LocalWorkspaceID, installationID)
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	manifest, err := decodeLocalPluginManifest(item)
	if err != nil {
		s.fail(w, err)
		return
	}
	hook, found := findLocalPluginHook(manifest, hookKey)
	if !found || hook.Transport.Type != plugincontract.TransportMCP || !hookAllowsTrigger(hook, plugincontract.TriggerAgent) {
		writeError(w, http.StatusForbidden, errors.New("plugin MCP contribution is not available to agents"))
		return
	}
	if len(approvedLocalPluginMCPTools(item, hookKey)) == 0 {
		writeError(w, http.StatusForbidden, errors.New("plugin MCP contribution has no approved tools"))
		return
	}
	secret := s.localPluginMCPSecret(r.Context(), installationID, hookKey)
	header := ""
	if secret != "" {
		header = "Authorization"
	}
	writeJSON(w, http.StatusOK, map[string]any{"credential_header": header, "credential": secret})
}

func (s *Server) invokeAgentPluginHook(w http.ResponseWriter, r *http.Request) {
	if _, err := s.daemonTaskRef(r.Context(), LocalWorkspaceID, chi.URLParam(r, "taskID")); err != nil {
		s.respondStoreError(w, err)
		return
	}
	input := struct {
		InstallationID string          `json:"installation_id"`
		HookKey        string          `json:"hook_key"`
		Input          json.RawMessage `json:"input"`
	}{}
	if err := decodeBody(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if input.InstallationID == "" || input.HookKey == "" {
		writeError(w, http.StatusBadRequest, errors.New("installation_id and hook_key are required"))
		return
	}
	payload := map[string]any{"trigger": plugincontract.TriggerAgent}
	if len(input.Input) > 0 {
		payload["input"] = input.Input
	}
	result, err := s.runLocalPluginHook(r.Context(), input.InstallationID, input.HookKey, payload)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"status": "failed", "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) launchPluginSurface(w http.ResponseWriter, r *http.Request) {
	ws, _ := s.workspaceFor(r)
	id, key := chi.URLParam(r, "installationID"), chi.URLParam(r, "surfaceKey")
	item, err := s.readPluginInstallation(r.Context(), ws.ID, id)
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	if item["enabled"] != true {
		writeError(w, http.StatusConflict, errors.New("plugin is disabled"))
		return
	}
	found := false
	for _, raw := range item["surfaces"].([]any) {
		surface, _ := raw.(map[string]any)
		if surface["key"] == key {
			found = true
			break
		}
	}
	if !found {
		s.respondStoreError(w, sql.ErrNoRows)
		return
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	writeJSON(w, http.StatusOK, map[string]any{"url": scheme + "://" + r.Host + "/api/plugin-assets/" + id + "/" + key, "bridge_token": uuid.NewString(), "version": item["version"], "digest": "local"})
}

func (s *Server) pluginSurfaceAsset(w http.ResponseWriter, r *http.Request) {
	item, err := s.readPluginInstallation(r.Context(), LocalWorkspaceID, chi.URLParam(r, "installationID"))
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	key := chi.URLParam(r, "surfaceKey")
	entry := ""
	for _, raw := range item["surfaces"].([]any) {
		surface, _ := raw.(map[string]any)
		if surface["key"] == key {
			entry = stringFromAny(surface["entry"])
			break
		}
	}
	if entry == "" {
		s.respondStoreError(w, sql.ErrNoRows)
		return
	}
	var path string
	if err := s.store.db.QueryRowContext(r.Context(), `SELECT v.bundle_path FROM plugin_installations i JOIN plugin_package_versions v ON v.id=i.package_version_id WHERE i.id=?`, chi.URLParam(r, "installationID")).Scan(&path); err != nil {
		s.respondStoreError(w, err)
		return
	}
	file, err := s.files.Open(path)
	if err != nil {
		s.fail(w, err)
		return
	}
	archive, err := io.ReadAll(file)
	_ = file.Close()
	if err != nil {
		s.fail(w, err)
		return
	}
	reader, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
	if err != nil {
		s.fail(w, err)
		return
	}
	var script []byte
	for _, file := range reader.File {
		if file.Name == entry || strings.HasSuffix(file.Name, "/"+entry) {
			opened, _ := file.Open()
			script, _ = io.ReadAll(opened)
			_ = opened.Close()
			break
		}
	}
	if script == nil {
		s.respondStoreError(w, sql.ErrNoRows)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'unsafe-inline'; connect-src http: https:; style-src 'unsafe-inline'")
	safeScript := strings.ReplaceAll(string(script), "</script", "<\\/script")
	_, _ = fmt.Fprintf(w, "<!doctype html><meta charset=utf-8><script>%s</script>", safeScript)
}

func (s *Server) invokePluginHook(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.Header.Get("X-Multica-Plugin-Installation"))
	hookKey := chi.URLParam(r, "hookKey")
	input := map[string]any{}
	if err := decodeBody(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	result, err := s.runLocalPluginHook(r.Context(), id, hookKey, input)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			s.respondStoreError(w, err)
			return
		}
		writeJSON(w, http.StatusConflict, map[string]any{"status": "refused", "error": err.Error()})
		return
	}
	status := http.StatusOK
	if result["status"] == "failed" {
		status = http.StatusBadGateway
	}
	writeJSON(w, status, result)
}

func (s *Server) runLocalPluginHook(ctx context.Context, id, hookKey string, input map[string]any) (map[string]any, error) {
	item, err := s.readPluginInstallation(ctx, LocalWorkspaceID, id)
	if err != nil {
		return nil, err
	}
	if item["enabled"] != true {
		return nil, errors.New("plugin is disabled")
	}
	manifest, err := decodeLocalPluginManifest(item)
	if err != nil {
		return nil, err
	}
	hook, ok := findLocalPluginHook(manifest, hookKey)
	if !ok {
		return nil, sql.ErrNoRows
	}
	if input["trigger"] == plugincontract.TriggerAgent && !hookAllowsTrigger(hook, plugincontract.TriggerAgent) {
		return nil, errors.New("plugin hook does not allow the agent trigger")
	}
	endpoint := hook.Transport.URL
	if hook.Transport.Type != plugincontract.TransportHTTP || endpoint == "" {
		return nil, errors.New("plugin hook HTTP transport is not configured")
	}
	started := time.Now()
	body, _ := json.Marshal(input)
	request, _ := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	response, callErr := (&http.Client{Timeout: 15 * time.Second}).Do(request)
	result := map[string]any{"id": uuid.NewString(), "hook_key": hookKey, "trigger": input["trigger"], "status": "ok", "attempts": 1, "latency_ms": time.Since(started).Milliseconds(), "created_at": now()}
	if callErr != nil {
		result["status"], result["error"] = "failed", callErr.Error()
	} else {
		defer response.Body.Close()
		output, _ := io.ReadAll(io.LimitReader(response.Body, 1<<20))
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			result["status"], result["error"] = "failed", fmt.Sprintf("hook returned HTTP %d", response.StatusCode)
		} else {
			var decoded any
			if json.Unmarshal(output, &decoded) == nil {
				result["output"] = decoded
			} else {
				result["output"] = string(output)
			}
		}
	}
	raw, _ := encodeJSONMap(result)
	if _, err := s.store.db.ExecContext(ctx, `INSERT INTO plugin_invocations(id,installation_id,hook_key,trigger_kind,status,data,created_at) VALUES(?,?,?,?,?,?,?)`, result["id"], id, hookKey, stringFromAny(input["trigger"]), result["status"], raw, result["created_at"]); err != nil {
		return nil, err
	}
	return result, nil
}

func (s *Server) pluginBridgeInstallation(w http.ResponseWriter, r *http.Request, scope string) (map[string]any, bool) {
	id := strings.TrimSpace(r.Header.Get("X-Multica-Plugin-Installation"))
	item, err := s.readPluginInstallation(r.Context(), LocalWorkspaceID, id)
	if err != nil {
		s.respondStoreError(w, err)
		return nil, false
	}
	if item["enabled"] != true {
		writeError(w, http.StatusForbidden, errors.New("plugin is disabled"))
		return nil, false
	}
	if scope != "" {
		granted := false
		if values, ok := item["granted_scopes"].([]any); ok {
			for _, value := range values {
				if value == scope {
					granted = true
				}
			}
		}
		if !granted {
			writeError(w, http.StatusForbidden, fmt.Errorf("plugin scope %s was not granted", scope))
			return nil, false
		}
	}
	return item, true
}

func (s *Server) pluginBridgeContext(w http.ResponseWriter, r *http.Request) {
	item, ok := s.pluginBridgeInstallation(w, r, "")
	if !ok {
		return
	}
	identity, err := s.store.Identity(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	result := map[string]any{"workspace": map[string]any{"id": identity.Workspace.ID, "name": identity.Workspace.Name, "slug": identity.Workspace.Slug}, "user": map[string]any{"id": identity.Operator.ID, "name": identity.Operator.Name}, "config": item["config"], "granted_net_domains": []string{}}
	if issueID := strings.TrimSpace(r.URL.Query().Get("issue_id")); issueID != "" {
		issue, err := s.store.GetIssue(r.Context(), LocalWorkspaceID, issueID)
		if err != nil {
			s.respondStoreError(w, err)
			return
		}
		result["issue"] = map[string]any{"id": issue.ID, "identifier": issue.Identifier, "title": issue.Title}
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) pluginBridgeIssue(w http.ResponseWriter, r *http.Request) {
	scope := "issues:read"
	if r.Method == http.MethodPatch {
		scope = "issues:write"
	}
	if _, ok := s.pluginBridgeInstallation(w, r, scope); !ok {
		return
	}
	id := chi.URLParam(r, "id")
	if r.Method == http.MethodGet {
		issue, err := s.store.GetIssue(r.Context(), LocalWorkspaceID, id)
		if err != nil {
			s.respondStoreError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, issue)
		return
	}
	input := map[string]any{}
	if err := decodeBody(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	updates := map[string]any{}
	for _, key := range []string{"title", "description"} {
		if value, exists := input[key]; exists {
			updates[key] = value
		}
	}
	issue, err := s.store.UpdateIssue(r.Context(), LocalWorkspaceID, id, updates)
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, issue)
}

func (s *Server) pluginBridgeComments(w http.ResponseWriter, r *http.Request) {
	scope := "comments:read"
	if r.Method == http.MethodPost {
		scope = "comments:write"
	}
	if _, ok := s.pluginBridgeInstallation(w, r, scope); !ok {
		return
	}
	issue, err := s.store.GetIssue(r.Context(), LocalWorkspaceID, chi.URLParam(r, "id"))
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	if r.Method == http.MethodGet {
		comments, err := s.store.ListComments(r.Context(), LocalWorkspaceID, issue.ID)
		if err != nil {
			s.fail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"comments": comments})
		return
	}
	input := struct {
		Content  string `json:"content"`
		ParentID string `json:"parent_id"`
	}{}
	if err := decodeBody(r, &input); err != nil || strings.TrimSpace(input.Content) == "" {
		writeError(w, http.StatusBadRequest, errors.New("content is required"))
		return
	}
	comment, err := s.store.CreateCommentWithAttachments(r.Context(), LocalWorkspaceID, issue.ID, input.Content, "comment", input.ParentID, nil)
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, comment)
}

func (s *Server) pluginBridgeStorage(w http.ResponseWriter, r *http.Request) {
	scope := chi.URLParam(r, "scope")
	if scope != "user" && scope != "workspace" {
		writeError(w, http.StatusBadRequest, errors.New("storage scope must be user or workspace"))
		return
	}
	if _, ok := s.pluginBridgeInstallation(w, r, "storage:"+scope); !ok {
		return
	}
	installationID := strings.TrimSpace(r.Header.Get("X-Multica-Plugin-Installation"))
	ownerID := LocalWorkspaceID
	if scope == "user" {
		ownerID = LocalOperatorID
	}
	key := strings.TrimSpace(chi.URLParam(r, "key"))
	if key == "" {
		rows, err := s.store.db.QueryContext(r.Context(), `SELECT key, length(value), updated_at FROM plugin_storage WHERE installation_id=? AND scope=? AND owner_id=? ORDER BY key`, installationID, scope, ownerID)
		if err != nil {
			s.fail(w, err)
			return
		}
		keys := []map[string]any{}
		for rows.Next() {
			var name, updatedAt string
			var size int
			_ = rows.Scan(&name, &size, &updatedAt)
			keys = append(keys, map[string]any{"key": name, "size_bytes": size, "updated_at": updatedAt})
		}
		_ = rows.Close()
		writeJSON(w, http.StatusOK, map[string]any{"keys": keys})
		return
	}
	if len(key) > 128 || strings.ContainsAny(key, "/\\") {
		writeError(w, http.StatusBadRequest, errors.New("storage key is invalid"))
		return
	}
	switch r.Method {
	case http.MethodGet:
		var value string
		if err := s.store.db.QueryRowContext(r.Context(), `SELECT value FROM plugin_storage WHERE installation_id=? AND scope=? AND owner_id=? AND key=?`, installationID, scope, ownerID, key).Scan(&value); err != nil {
			s.respondStoreError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"value": value})
	case http.MethodPut:
		input := struct {
			Value string `json:"value"`
		}{}
		if err := decodeBody(r, &input); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		if len(input.Value) > 64<<10 {
			writeError(w, http.StatusRequestEntityTooLarge, errors.New("plugin storage value exceeds 64 KiB"))
			return
		}
		_, err := s.store.db.ExecContext(r.Context(), `INSERT INTO plugin_storage(installation_id,scope,owner_id,key,value,updated_at) VALUES(?,?,?,?,?,?) ON CONFLICT(installation_id,scope,owner_id,key) DO UPDATE SET value=excluded.value,updated_at=excluded.updated_at`, installationID, scope, ownerID, key, input.Value, now())
		if err != nil {
			s.fail(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	case http.MethodDelete:
		_, err := s.store.db.ExecContext(r.Context(), `DELETE FROM plugin_storage WHERE installation_id=? AND scope=? AND owner_id=? AND key=?`, installationID, scope, ownerID, key)
		if err != nil {
			s.fail(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}
