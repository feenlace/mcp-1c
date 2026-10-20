package main

import (
	"github.com/feenlace/mcp-1c/tools"
	"testing"
)

func TestFeature06RootAndConnectionSelection(t *testing.T) {
	for _, tc := range []struct {
		name, source, project, wantSource, wantProject string
		declared                                       bool
		mode                                           tools.PassportMode
	}{
		{"legacy default", "", "", "", "", false, tools.ModeLive},
		{"source only", "A", "", "A", "A", false, tools.ModeOffline},
		{"source and project", "A", "B", "A", "B", false, tools.ModeOffline},
		{"project without index", "", "B", "B", "B", false, tools.ModeOffline},
		{"explicit live source", "A", "B", "A", "B", true, tools.ModeLive},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := localToolOptions("1.2.3", tc.source, tc.project, tc.declared)
			if got.SourceRoot != tc.wantSource || got.ProjectRoot != tc.wantProject || got.Passport.Mode != tc.mode {
				t.Fatalf("options %+v", got)
			}
		})
	}
}
