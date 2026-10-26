package localworkbench

import (
	"context"
	"database/sql"
	"net/http"
	"strings"

	"github.com/multica-ai/multica/server/internal/service"
)

type localWakeupOrigin struct{ AgentID, TaskID string }
type localWakeupOriginKey struct{}

func (s *Server) localWakeupActorContext(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := localWakeupOrigin{AgentID: strings.TrimSpace(r.Header.Get("X-Agent-ID")), TaskID: strings.TrimSpace(r.Header.Get("X-Task-ID"))}
		if origin.AgentID != "" || origin.TaskID != "" {
			r = r.WithContext(context.WithValue(r.Context(), localWakeupOriginKey{}, origin))
		}
		next.ServeHTTP(w, r)
	})
}

// Attribute every captured fact before committing the same transaction. This
// records the editor, not an old comment's author, without persistent actor state.
func beginLocalWakeupMutation(ctx context.Context, tx *sql.Tx, workspace string) (int64, localWakeupOrigin, error) {
	origin, _ := ctx.Value(localWakeupOriginKey{}).(localWakeupOrigin)
	if origin.AgentID != "" || origin.TaskID != "" {
		var count int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM task_runs t JOIN issues i ON i.id=t.issue_id JOIN agents a ON a.id=t.agent_id AND a.workspace_id=i.workspace_id WHERE t.id=? AND t.agent_id=? AND t.status='running' AND i.workspace_id=?`, origin.TaskID, origin.AgentID, workspace).Scan(&count); err != nil {
			return 0, origin, err
		}
		if count != 1 {
			return 0, origin, service.ErrWakeupForbidden
		}
	}
	var cursor int64
	err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(id),0) FROM issue_wakeup_events`).Scan(&cursor)
	return cursor, origin, err
}

func commitLocalWakeupMutation(ctx context.Context, tx *sql.Tx, workspace string, cursor int64, origin localWakeupOrigin) error {
	actorType, actorID := "member", LocalOperatorID
	if origin.AgentID != "" {
		actorType, actorID = "agent", origin.AgentID
	}
	if _, err := tx.ExecContext(ctx, `UPDATE issue_wakeup_events SET actor_type=?,actor_id=?,source_task_id=COALESCE(?,source_task_id),source_agent_id=COALESCE(?,source_agent_id) WHERE workspace_id=? AND id>? AND event_type NOT LIKE 'task.%'`, actorType, actorID, nullable(origin.TaskID), nullable(origin.AgentID), workspace, cursor); err != nil {
		return err
	}
	return tx.Commit()
}
