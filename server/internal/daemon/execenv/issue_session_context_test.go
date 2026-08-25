package execenv

import (
	"strings"
	"testing"
)

func TestRenderIssueContextIncludesSessionAndStructuredHandoff(t *testing.T) {
	content := renderIssueContext("codex", TaskContextForEnv{
		IssueID: "child-1", IssueSessionID: "session-1", IssueStageID: "stage-1", IssueStageKind: "review", ContinuityState: "resumed",
		IssueHandoff: &IssueHandoffForEnv{ParentIssueID: "parent-1", Objective: "Implement the scoped child", ConfirmedFacts: []string{"API is fixed"}},
	})
	for _, want := range []string{"`session-1`", "`review`", "`resumed`", "Implement the scoped child", "API is fixed", "`parent-1`"} {
		if !strings.Contains(content, want) {
			t.Fatalf("issue context missing %q:\n%s", want, content)
		}
	}
}
