package conductor

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/opentendril/opentendril/cmd/stem/internal/terrarium"
)

// event builds one `go test -json` stream line.
func event(action, packagePath, test string) string {
	var b strings.Builder
	b.WriteString(`{"Action":"` + action + `"`)
	if packagePath != "" {
		b.WriteString(`,"Package":"` + packagePath + `"`)
	}
	if test != "" {
		b.WriteString(`,"Test":"` + test + `"`)
	}
	b.WriteString("}\n")
	return b.String()
}

// The evaluator is the security-critical core of the skip-aware verdict:
// these tables pin down every classification rule, above all that a skipped
// *test* can never read as passed while a package with *no test files* stays
// benign.
func TestEvaluateGoTestJSONStream(t *testing.T) {
	cases := []struct {
		name         string
		stream       string
		wantVerdict  goTestVerdict
		wantSkipped  []string
		wantFailures []string
	}{
		{
			name: "clean pass",
			stream: event("run", "example.com/module/alpha", "TestAlpha") +
				event("pass", "example.com/module/alpha", "TestAlpha") +
				event("pass", "example.com/module/alpha", ""),
			wantVerdict: goTestVerdictPassed,
		},
		{
			name: "failing test fails",
			stream: event("run", "example.com/module/alpha", "TestAlpha") +
				event("fail", "example.com/module/alpha", "TestAlpha") +
				event("fail", "example.com/module/alpha", ""),
			wantVerdict: goTestVerdictFailed,
			wantFailures: []string{
				"example.com/module/alpha",
				"example.com/module/alpha.TestAlpha",
			},
		},
		{
			name: "skipped test blocks",
			stream: event("run", "example.com/module/alpha", "TestNeedsDocker") +
				event("skip", "example.com/module/alpha", "TestNeedsDocker") +
				event("pass", "example.com/module/alpha", ""),
			wantVerdict: goTestVerdictBlocked,
			wantSkipped: []string{"example.com/module/alpha.TestNeedsDocker"},
		},
		{
			name: "package-level skip (no test files) is benign",
			// A skip event WITHOUT a Test field is the toolchain reporting
			// "no test files" — it promised nothing, so it blocks nothing.
			stream:      event("skip", "example.com/module/empty", ""),
			wantVerdict: goTestVerdictPassed,
		},
		{
			name: "failure outranks skip",
			stream: event("skip", "example.com/module/alpha", "TestNeedsDocker") +
				event("fail", "example.com/module/beta", "TestBeta"),
			wantVerdict:  goTestVerdictFailed,
			wantSkipped:  []string{"example.com/module/alpha.TestNeedsDocker"},
			wantFailures: []string{"example.com/module/beta.TestBeta"},
		},
		{
			name: "test-level skip blocks even next to a benign package-level skip",
			stream: event("skip", "example.com/module/empty", "") +
				event("skip", "example.com/module/alpha", "TestNeedsNetwork") +
				event("pass", "example.com/module/alpha", ""),
			wantVerdict: goTestVerdictBlocked,
			wantSkipped: []string{"example.com/module/alpha.TestNeedsNetwork"},
		},
		{
			name: "several skipped tests are all collected",
			stream: event("skip", "example.com/module/alpha", "TestOne") +
				event("skip", "example.com/module/beta", "TestTwo"),
			wantVerdict: goTestVerdictBlocked,
			wantSkipped: []string{
				"example.com/module/alpha.TestOne",
				"example.com/module/beta.TestTwo",
			},
		},
		{
			name: "stray non-JSON output is ignored",
			stream: "warming caches...\n" +
				event("pass", "example.com/module/alpha", "TestAlpha") +
				"{not json at all\n" +
				event("pass", "example.com/module/alpha", ""),
			wantVerdict: goTestVerdictPassed,
		},
		{
			name:        "empty stream passes (exit code still governs separately)",
			stream:      "",
			wantVerdict: goTestVerdictPassed,
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			outcome := evaluateGoTestJSONStream(testCase.stream)
			if outcome.Verdict != testCase.wantVerdict {
				t.Fatalf("verdict = %q, want %q", outcome.Verdict, testCase.wantVerdict)
			}
			if strings.Join(outcome.SkippedTests, "|") != strings.Join(testCase.wantSkipped, "|") {
				t.Fatalf("skipped tests = %v, want %v", outcome.SkippedTests, testCase.wantSkipped)
			}
			if strings.Join(outcome.FailedSubjects, "|") != strings.Join(testCase.wantFailures, "|") {
				t.Fatalf("failed subjects = %v, want %v", outcome.FailedSubjects, testCase.wantFailures)
			}
		})
	}
}

// Only a `go test` invocation carrying -json opts into the skip-aware
// verdict; build, vet and format steps must keep the exit-code verdict.
func TestIsGoTestJSONCommand(t *testing.T) {
	cases := []struct {
		command []string
		want    bool
	}{
		{[]string{"go", "test", "-json", "./..."}, true},
		{[]string{"go", "test", "-json", "-short", "example.com/module/alpha"}, true},
		{[]string{"go", "test", "./...", "--json"}, true},
		{[]string{"go", "test", "-json=true", "./..."}, true},
		{[]string{"go", "test", "-short", "./..."}, false},
		{[]string{"go", "build", "./..."}, false},
		{[]string{"go", "vet", "./..."}, false},
		{[]string{"sh", "-c", "gofmt -l ."}, false},
		{[]string{"go"}, false},
		{nil, false},
	}
	for _, testCase := range cases {
		if got := isGoTestJSONCommand(testCase.command); got != testCase.want {
			t.Fatalf("isGoTestJSONCommand(%v) = %v, want %v", testCase.command, got, testCase.want)
		}
	}
}

// The verdict wiring over a real CommandResult: a skip-bearing stream must
// yield the distinct blocked error (errors.Is-able and unmistakable in the
// message), a clean stream must pass, and a failing stream must fail even
// when the process somehow exited 0.
func TestReportGoTestVerifierVerdicts(t *testing.T) {
	command := []string{"go", "test", "-json", "./..."}

	t.Run("clean stream passes", func(t *testing.T) {
		report, err := reportGoTestVerifier(command, terrarium.CommandResult{
			ExitCode: 0,
			Stdout:   event("pass", "example.com/module/alpha", "TestAlpha") + event("pass", "example.com/module/alpha", ""),
		})
		if err != nil {
			t.Fatalf("clean stream returned error: %v", err)
		}
		if !strings.Contains(report, "PASSED") {
			t.Fatalf("pass report missing PASSED label: %q", report)
		}
	})

	t.Run("skip-bearing stream is blocked", func(t *testing.T) {
		report, err := reportGoTestVerifier(command, terrarium.CommandResult{
			ExitCode: 0,
			Stdout: event("skip", "example.com/module/alpha", "TestNeedsDocker") +
				event("pass", "example.com/module/alpha", ""),
		})
		if err == nil {
			t.Fatal("skip-bearing stream must not pass")
		}
		if !errors.Is(err, ErrVerifierBlocked) {
			t.Fatalf("blocked error must wrap ErrVerifierBlocked, got: %v", err)
		}
		if !strings.Contains(err.Error(), "blocked") || !strings.Contains(err.Error(), "skipped and were not verified") {
			t.Fatalf("blocked error message not distinct: %v", err)
		}
		if !strings.Contains(report, "BLOCKED") || !strings.Contains(report, "TestNeedsDocker") {
			t.Fatalf("blocked report missing markers: %q", report)
		}
	})

	t.Run("failing stream fails and is not blocked", func(t *testing.T) {
		_, err := reportGoTestVerifier(command, terrarium.CommandResult{
			ExitCode: 0,
			Stdout:   event("fail", "example.com/module/alpha", "TestAlpha"),
		})
		if err == nil {
			t.Fatal("failing stream must not pass")
		}
		if errors.Is(err, ErrVerifierBlocked) {
			t.Fatalf("failure must stay a failure, not blocked: %v", err)
		}
	})

	t.Run("non-zero exit fails even with a clean stream", func(t *testing.T) {
		_, err := reportGoTestVerifier(command, terrarium.CommandResult{
			ExitCode: 2,
			Stdout:   "",
			Stderr:   "compile error",
		})
		if err == nil {
			t.Fatal("non-zero exit must not pass")
		}
		if errors.Is(err, ErrVerifierBlocked) {
			t.Fatalf("exit failure must not read as blocked: %v", err)
		}
	})
}

// ReportGoTestRun is the one shared judgement both tiers call — the sealed
// verifier and the `tendril verdict go-test` CLI behind the GitHub Actions
// gate — so its table pins down every stream/exit-code combination.
func TestReportGoTestRun(t *testing.T) {
	cases := []struct {
		name        string
		exitCode    int
		stream      string
		wantErr     bool
		wantBlocked bool
		wantStatus  string
	}{
		{
			name: "passing stream with exit 0 is green",
			stream: event("run", "example.com/module/alpha", "TestAlpha") +
				event("pass", "example.com/module/alpha", "TestAlpha") +
				event("pass", "example.com/module/alpha", ""),
			wantStatus: "PASSED",
		},
		{
			name: "test-level skip is blocked, never green",
			stream: event("skip", "example.com/module/alpha", "TestNeedsDocker") +
				event("pass", "example.com/module/alpha", ""),
			wantErr:     true,
			wantBlocked: true,
			wantStatus:  "BLOCKED",
		},
		{
			name:       "fail event fails",
			stream:     event("fail", "example.com/module/alpha", "TestAlpha"),
			wantErr:    true,
			wantStatus: "FAILED",
		},
		{
			name:       "non-zero exit with no fail event (compile error) still fails",
			exitCode:   2,
			stream:     "",
			wantErr:    true,
			wantStatus: "FAILED",
		},
		{
			name:       "package-level no-test-files skip stays benign",
			stream:     event("skip", "example.com/module/empty", ""),
			wantStatus: "PASSED",
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			report, err := ReportGoTestRun("go test -json ./...", testCase.exitCode, testCase.stream, "")
			if (err != nil) != testCase.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, testCase.wantErr)
			}
			if errors.Is(err, ErrVerifierBlocked) != testCase.wantBlocked {
				t.Fatalf("errors.Is(err, ErrVerifierBlocked) = %v, want %v (err: %v)", !testCase.wantBlocked, testCase.wantBlocked, err)
			}
			if !strings.Contains(report, testCase.wantStatus) {
				t.Fatalf("report missing %s label: %q", testCase.wantStatus, report)
			}
		})
	}
}

// The verifier wrapper must stay a pure delegation: after the refactor that
// exposed ReportGoTestRun, runVerifierCommand's `go test -json` path (which
// reaches reportGoTestVerifier) must produce byte-identical reports and
// identical errors to the shared judgement.
func TestReportGoTestVerifierDelegatesToReportGoTestRun(t *testing.T) {
	command := []string{"go", "test", "-json", "./..."}
	results := []terrarium.CommandResult{
		{ExitCode: 0, Stdout: event("pass", "example.com/module/alpha", "TestAlpha")},
		{ExitCode: 0, Stdout: event("skip", "example.com/module/alpha", "TestNeedsDocker")},
		{ExitCode: 1, Stdout: event("fail", "example.com/module/alpha", "TestAlpha")},
		{ExitCode: 2, Stdout: "", Stderr: "compile error"},
	}
	for _, result := range results {
		wrapperReport, wrapperErr := reportGoTestVerifier(command, result)
		sharedReport, sharedErr := ReportGoTestRun(strings.Join(command, " "), result.ExitCode, result.Stdout, result.Stderr)
		if wrapperReport != sharedReport {
			t.Fatalf("reports diverge:\nwrapper: %q\nshared:  %q", wrapperReport, sharedReport)
		}
		if fmt.Sprint(wrapperErr) != fmt.Sprint(sharedErr) {
			t.Fatalf("errors diverge: wrapper %v, shared %v", wrapperErr, sharedErr)
		}
		if errors.Is(wrapperErr, ErrVerifierBlocked) != errors.Is(sharedErr, ErrVerifierBlocked) {
			t.Fatalf("blocked sentinel diverges: wrapper %v, shared %v", wrapperErr, sharedErr)
		}
	}
}

// The blocked verdict must render its own label so a human reading the
// sequence log can never mistake an unverified run for a green or a red one.
func TestFormatGoTestRunReportBlockedLabel(t *testing.T) {
	outcome := goTestOutcome{
		Verdict:      goTestVerdictBlocked,
		SkippedTests: []string{"example.com/module/alpha.TestNeedsDocker"},
	}
	report := formatGoTestRunReport("go test -json ./...", 0, "", "", outcome)
	if !strings.Contains(report, "BLOCKED") {
		t.Fatalf("report missing BLOCKED label: %q", report)
	}
	if strings.Contains(report, "PASSED") || strings.Contains(report, "FAILED") {
		t.Fatalf("blocked report must not carry another verdict label: %q", report)
	}
	if !strings.Contains(report, "example.com/module/alpha.TestNeedsDocker") {
		t.Fatalf("blocked report must name the unverified test: %q", report)
	}
}
