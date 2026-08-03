package main

import (
	"strings"
	"testing"

	"github.com/opentendril/opentendril/cmd/stem/internal/core"
)

// The command line's closing line must report the actual outcome: a run that
// changed nothing may not print "matured", which is how a run that never
// touched its target file once reported success.
func TestSproutRunFooterReportsOutcome(t *testing.T) {
	cases := []struct {
		name        string
		result      core.SproutRunResult
		wantContain string
		forbid      string
	}{
		{
			name:        "changed something",
			result:      core.SproutRunResult{StepID: "step-1", SessionID: "session-1", Outcome: "complete", FilesModified: []string{"a.go", "b.go"}},
			wantContain: "matured: 2 file(s) changed",
		},
		{
			name:        "changed nothing",
			result:      core.SproutRunResult{StepID: "step-1", SessionID: "session-1", Outcome: "no-changes"},
			wantContain: "without changing any files",
			forbid:      "matured",
		},
		{
			// A detached run has not ended. Reporting it as matured claims a
			// finish that has not happened; reporting it as failed claims a
			// breakage that has not happened either. It has to say the work
			// continues, because that is the only true thing about it.
			name:        "detached",
			result:      core.SproutRunResult{StepID: "step-1", SessionID: "session-1", Outcome: "detached"},
			wantContain: "still growing",
			forbid:      "matured",
		},
		{
			// Reaped is an ending, but not a verdict on the work: the run was
			// stopped because nothing was waiting for it. The seedling glyph
			// the default line uses would read as a healthy finish.
			name:        "reaped",
			result:      core.SproutRunResult{StepID: "step-1", SessionID: "session-1", Outcome: "reaped"},
			wantContain: "reaped at the backstop",
			forbid:      "matured",
		},
		{
			name:        "skipped",
			result:      core.SproutRunResult{StepID: "step-1", SessionID: "session-1", Outcome: "skipped"},
			wantContain: "already completed",
			forbid:      "matured",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			line := sproutRunFooter(testCase.result)
			if !strings.Contains(line, testCase.wantContain) {
				t.Fatalf("footer %q does not contain %q", line, testCase.wantContain)
			}
			if testCase.forbid != "" && strings.Contains(line, testCase.forbid) {
				t.Fatalf("footer %q must not contain %q", line, testCase.forbid)
			}
		})
	}
}
