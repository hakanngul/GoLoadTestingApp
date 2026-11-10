package store

import (
	"strings"
	"testing"
)

// The query constants are written out as plain literals so static analysis
// can see they are not built at run time. This test keeps them in sync with
// the shared column lists and prune condition they were derived from.
func TestSQLiteQueryLiteralsMatchFragments(t *testing.T) {
	norm := func(s string) string { return strings.Join(strings.Fields(s), " ") }
	cases := []struct {
		name, query, fragment string
	}{
		{"ListTests", sqliteListTestsSQL, sqliteRunColsLR},
		{"CreateRun", sqliteCreateRunSQL, sqliteRunColsBare},
		{"GetRun", sqliteGetRunSQL, sqliteRunColsR},
		{"ListRuns", sqliteListRunsSQL, sqliteRunColsR},
		{"ListTestRuns", sqliteListTestRunsSQL, sqliteRunColsR},
		{"UpdateRunMeta", sqliteUpdateRunMetaSQL, sqliteRunColsBare},
		{"RunsSince", sqliteRunsSinceSQL, sqliteRunColsSlimR},
		{"PruneCandidates", sqlitePruneCandidatesSQL, sqlitePrunable},
		{"PruneDelete", sqlitePruneDeleteSQL, sqlitePrunable},
	}
	for _, c := range cases {
		if !strings.Contains(norm(c.query), norm(c.fragment)) {
			t.Errorf("%s query no longer contains its shared fragment; update the literal", c.name)
		}
	}
}
