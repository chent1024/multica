package localworkbench

import (
	"database/sql"
	"github.com/multica-ai/multica/server/internal/service"
)

type localWakeup struct {
	ID, WorkspaceID, IssueID, AgentID, CreatedBy                                       string
	Config                                                                             service.WakeupInput
	Enabled                                                                            bool
	Revision                                                                           int64
	NextFireAt, ExpiresAt, DisabledAt, LastTaskID, LastError, TimedOutAt, PausedReason sql.NullString
	SourceTaskID                                                                       sql.NullString
	FireCount                                                                          int
	CreatedAt, UpdatedAt                                                               string
}
