package receptors

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/opentendril/opentendril/cmd/stem/internal/core"
	"github.com/opentendril/opentendril/cmd/stem/internal/eventbus"
	"github.com/opentendril/opentendril/cmd/stem/internal/historydb"
)

const (
	watchOwner   = "claude"
	watchOther   = "codex"
	watchSubject = "tendril-owned"
)

// newWatchFixture builds the observation surface over a real history store
// holding two runs: one dispatched by watchOwner into its own phytomer, one
// dispatched by watchOther into a phytomer of its own. Both subjects hold a
// sprout.watch grant on "myrepo", so every refusal these tests assert is a
// refusal about ownership rather than a missing grant.
func newWatchFixture(t *testing.T) (*http.ServeMux, *historydb.Store) {
	t.Helper()

	grants := []core.DelegationGrant{
		{Pollen: watchOwner, OperationClasses: []string{core.CapSproutWatch}, Substrates: []string{"myrepo"}},
		{Pollen: watchOther, OperationClasses: []string{core.CapSproutWatch}, Substrates: []string{"myrepo"}},
	}
	mux, store := newWatchFixtureWithGrants(t, grants)

	seedWatchRun(t, store, historydb.SproutRun{
		RunID: "run-owner", SessionID: watchSubject, StepID: "run-owner",
		Pollen: watchOwner, Substrate: "myrepo", Status: "matured",
	})
	seedWatchRun(t, store, historydb.SproutRun{
		RunID: "run-other", SessionID: "tendril-other", StepID: "run-other",
		Pollen: watchOther, Substrate: "myrepo", Status: "matured",
	})
	return mux, store
}

func newWatchFixtureWithGrants(t *testing.T, grants []core.DelegationGrant) (*http.ServeMux, *historydb.Store) {
	t.Helper()

	store, err := historydb.Open(context.Background(), filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatalf("open history store: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	gate := &DelegationGate{Authorizer: core.NewDelegationAuthorizer(grants), Bus: eventbus.New()}
	bus := eventbus.New()
	coreSvc := core.NewService(nil).WithPhytomerObservationSource(testPhytomerObservationSource(store))
	handler := NewSessionsHandler(coreSvc, nil, store, bus).
		WithWatch(NewWatchAuthority(gate, store))
	handler.watchPoll = 15 * time.Millisecond

	mux := http.NewServeMux()
	// The command lane keeps the blanket delegated-request refusal it has
	// always had; the observation lane does not. Wiring both here is what makes
	// the split under test rather than assumed.
	handler.Register(mux, gate.Middleware, nil)
	return mux, store
}

func seedWatchRun(t *testing.T, store *historydb.Store, run historydb.SproutRun) {
	t.Helper()
	run.StartedAt = time.Now().UTC()
	if err := store.RecordSproutRun(context.Background(), run); err != nil {
		t.Fatalf("record sprout run %s: %v", run.RunID, err)
	}
}

func testPhytomerObservationSource(store *historydb.Store) core.PhytomerObservationSource {
	if store == nil {
		return core.PhytomerObservationSource{}
	}
	return core.PhytomerObservationSource{
		SeedByPhytomer: func(ctx context.Context, phytomerID string) (core.SeedObservationEvidence, bool, error) {
			seed, found, err := store.GetSeedRunByPhytomer(ctx, phytomerID)
			if err != nil || !found {
				return core.SeedObservationEvidence{}, found, err
			}
			return core.SeedObservationEvidence{
				Handle:     seed.Handle,
				Pollen:     seed.Pollen,
				PhytomerID: seed.PhytomerID,
				Substrate:  seed.Substrate,
				Status:     seed.Status,
				Iterations: seed.Iterations,
				Branch:     seed.Branch,
				Commit:     seed.Commit,
				Goal:       seed.Goal,
				Diff:       seed.Diff,
				Logs:       seed.Logs,
				Error:      seed.Error,
				PublicationDiagnostic: func() *core.SeedPublicationDiagnostic {
					if seed.PublicationDiagnostic == nil {
						return nil
					}
					return &core.SeedPublicationDiagnostic{
						FailureCategory: seed.PublicationDiagnostic.FailureCategory,
						ExecutionStatus: seed.PublicationDiagnostic.ExecutionStatus,
						Phase:           seed.PublicationDiagnostic.Phase,
						Outcome:         seed.PublicationDiagnostic.Outcome,
						RetrySafe:       seed.PublicationDiagnostic.RetrySafe,
						Message:         seed.PublicationDiagnostic.Message,
						RequestID:       seed.PublicationDiagnostic.RequestID,
					}
				}(),
				VerificationDiagnostics: func() []core.SeedVerificationDiagnostic {
					if len(seed.VerificationDiagnostics) == 0 {
						return nil
					}
					out := make([]core.SeedVerificationDiagnostic, len(seed.VerificationDiagnostics))
					for i, diagnostic := range seed.VerificationDiagnostics {
						out[i] = core.SeedVerificationDiagnostic{
							Iteration: diagnostic.Iteration,
							Outcome:   diagnostic.Outcome,
							TimedOut:  diagnostic.TimedOut,
							Message:   diagnostic.Message,
						}
						if diagnostic.ExitCode != nil {
							code := *diagnostic.ExitCode
							out[i].ExitCode = &code
						}
					}
					return out
				}(),
			}, true, nil
		},
		SproutsByPhytomer: func(ctx context.Context, phytomerID string) ([]core.SproutObservationEvidence, error) {
			runs, err := store.LoadSproutRuns(ctx, phytomerID, 100)
			if err != nil {
				return nil, err
			}
			out := make([]core.SproutObservationEvidence, 0, len(runs))
			for _, run := range runs {
				evidence := core.SproutObservationEvidence{
					RunID:                    run.RunID,
					Pollen:                   run.Pollen,
					Substrate:                run.Substrate,
					Status:                   run.Status,
					Provider:                 run.Provider,
					Model:                    run.Model,
					Outcome:                  run.Outcome,
					FailureCategory:          run.FailureCategory,
					ProviderRequestAttempted: run.ProviderRequestAttempted,
					ToolInvocations:          run.ToolInvocations,
					Transcript:               run.Transcript,
					Output:                   run.Output,
					Error:                    run.Error,
					StartedAt:                run.StartedAt,
				}
				if run.ProviderDiagnostic != nil {
					copied := core.ProviderDiagnostic{
						StatusCode: run.ProviderDiagnostic.StatusCode,
						Message:    run.ProviderDiagnostic.Message,
						Provider:   run.ProviderDiagnostic.Provider,
					}
					evidence.ProviderDiagnostic = &copied
				}
				out = append(out, evidence)
			}
			return out, nil
		},
		ContinuationsByPhytomer: func(ctx context.Context, phytomerID string) ([]core.ContinuationObservationEvidence, error) {
			rows, err := store.ListContinuationObservationsByPhytomer(ctx, phytomerID)
			if err != nil {
				return nil, err
			}
			out := make([]core.ContinuationObservationEvidence, 0, len(rows))
			for _, row := range rows {
				out = append(out, core.ContinuationObservationEvidence{
					ContinuationID: row.ContinuationID,
					Pollen:         row.Pollen,
					Substrate:      row.Substrate,
					Sequence:       row.Sequence,
					DeliveryState:  row.DeliveryState,
				})
			}
			return out, nil
		},
	}
}

// watchRequest issues one observation read as pollen, or as the operator when
// pollen is blank.
func watchRequest(t *testing.T, mux *http.ServeMux, path, pollen string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, path, nil)
	if pollen != "" {
		request.Header.Set(PollenHeader, pollen)
	}
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, request)
	return recorder
}

func decodeRuns(t *testing.T, body []byte) []historydb.SproutRun {
	t.Helper()
	var payload struct {
		SproutRuns []historydb.SproutRun `json:"sproutRuns"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("decode sprout runs: %v", err)
	}
	return payload.SproutRuns
}

// TestOwnerReadsItsOwnRun is the access half: the subject that dispatched the
// run reads the run's record and the phytomer's events.
func TestOwnerReadsItsOwnRun(t *testing.T) {
	mux, _ := newWatchFixture(t)

	events := watchRequest(t, mux, "/v1/phytomers/"+watchSubject+"/events", watchOwner)
	if events.Code != http.StatusOK {
		t.Fatalf("owner reading its own events = %d, want 200: %s", events.Code, events.Body.String())
	}

	runs := watchRequest(t, mux, "/v1/phytomers/"+watchSubject+"/sprout-runs", watchOwner)
	if runs.Code != http.StatusOK {
		t.Fatalf("owner reading its own runs = %d, want 200: %s", runs.Code, runs.Body.String())
	}
	recorded := decodeRuns(t, runs.Body.Bytes())
	if len(recorded) != 1 || recorded[0].RunID != "run-owner" {
		t.Fatalf("owner read %d run(s), want exactly run-owner: %+v", len(recorded), recorded)
	}
}

// TestOtherSubjectIsRefusedAnotherRun is the half that matters. A subject
// holding an identical grant, differing only in which runs it dispatched, must
// see nothing of another subject's phytomer — not a filtered view, not an empty
// list, and above all not the record.
func TestOtherSubjectIsRefusedAnotherRun(t *testing.T) {
	mux, _ := newWatchFixture(t)

	events := watchRequest(t, mux, "/v1/phytomers/"+watchSubject+"/events", watchOther)
	if events.Code != http.StatusForbidden {
		t.Fatalf("another subject reading events = %d, want 403: %s", events.Code, events.Body.String())
	}

	runs := watchRequest(t, mux, "/v1/phytomers/"+watchSubject+"/sprout-runs", watchOther)
	if runs.Code != http.StatusForbidden {
		t.Fatalf("another subject reading runs = %d, want 403: %s", runs.Code, runs.Body.String())
	}
	// The status alone would not catch a refusal that still wrote the record
	// into the body it refused with.
	if body := runs.Body.String(); strings.Contains(body, "run-owner") {
		t.Fatalf("a refused read still carried the run it refused: %s", body)
	}

	watch := watchRequest(t, mux, "/v1/phytomers/"+watchSubject+"/watch", watchOther)
	if watch.Code != http.StatusForbidden {
		t.Fatalf("another subject watching = %d, want 403: %s", watch.Code, watch.Body.String())
	}
	if body := watch.Body.String(); strings.Contains(body, "run-owner") || strings.Contains(body, "event: observation") {
		t.Fatalf("a refused watch still carried observation: %s", body)
	}
}

// TestUnresolvableCredentialIsNotTheOperator closes the fall-through. A
// credential that no longer resolves — revoked, unknown, malformed — must be
// refused, not quietly demoted to "no Pollen presented", which is the operator
// and sees everything.
func TestUnresolvableCredentialIsNotTheOperator(t *testing.T) {
	mux, _ := newWatchFixture(t)

	for _, path := range []string{
		"/v1/phytomers/" + watchSubject + "/events",
		"/v1/phytomers/" + watchSubject + "/sprout-runs",
		"/v1/phytomers/" + watchSubject + "/watch",
	} {
		request := httptest.NewRequest(http.MethodGet, path, nil)
		request.Header.Set("Authorization", "Bearer tendril_refresh_00000000000000000000000000000000")
		recorder := httptest.NewRecorder()
		mux.ServeHTTP(recorder, request)

		if recorder.Code != http.StatusForbidden {
			t.Fatalf("%s with an unresolvable credential = %d, want 403: %s", path, recorder.Code, recorder.Body.String())
		}
		if strings.Contains(recorder.Body.String(), "run-owner") {
			t.Fatalf("%s answered an unresolvable credential with the record: %s", path, recorder.Body.String())
		}
	}
}

// TestOperatorReadsEverything pins the requirement that closing the read to
// subjects does not close it to the Botanist. The operator holds no Pollen, so
// it is scoped to nothing and both phytomers answer in full.
func TestOperatorReadsEverything(t *testing.T) {
	mux, _ := newWatchFixture(t)

	for _, sessionID := range []string{watchSubject, "tendril-other"} {
		events := watchRequest(t, mux, "/v1/phytomers/"+sessionID+"/events", "")
		if events.Code != http.StatusOK {
			t.Fatalf("operator reading %s events = %d, want 200: %s", sessionID, events.Code, events.Body.String())
		}
		runs := watchRequest(t, mux, "/v1/phytomers/"+sessionID+"/sprout-runs", "")
		if runs.Code != http.StatusOK {
			t.Fatalf("operator reading %s runs = %d, want 200: %s", sessionID, runs.Code, runs.Body.String())
		}
		if len(decodeRuns(t, runs.Body.Bytes())) != 1 {
			t.Fatalf("operator saw no run in %s: %s", sessionID, runs.Body.String())
		}
	}
}

// TestOwnershipAloneDoesNotAdmit separates the two conditions. The subject
// dispatched the run, so it owns it — and is still refused, because no grant
// names sprout.watch. Ownership answers "whose", a grant answers "may".
func TestOwnershipAloneDoesNotAdmit(t *testing.T) {
	mux, store := newWatchFixtureWithGrants(t, []core.DelegationGrant{
		{Pollen: watchOwner, OperationClasses: []string{core.CapSproutGrow}, Substrates: []string{"myrepo"}},
	})
	seedWatchRun(t, store, historydb.SproutRun{
		RunID: "run-owner", SessionID: watchSubject, StepID: "run-owner",
		Pollen: watchOwner, Substrate: "myrepo", Status: "matured",
	})

	events := watchRequest(t, mux, "/v1/phytomers/"+watchSubject+"/events", watchOwner)
	if events.Code != http.StatusForbidden {
		t.Fatalf("owner without a sprout.watch grant = %d, want 403: %s", events.Code, events.Body.String())
	}
	runs := watchRequest(t, mux, "/v1/phytomers/"+watchSubject+"/sprout-runs", watchOwner)
	if runs.Code != http.StatusForbidden {
		t.Fatalf("owner without a sprout.watch grant read runs = %d, want 403: %s", runs.Code, runs.Body.String())
	}
	watch := watchRequest(t, mux, "/v1/phytomers/"+watchSubject+"/watch", watchOwner)
	if watch.Code != http.StatusForbidden {
		t.Fatalf("owner without a sprout.watch grant watching = %d, want 403: %s", watch.Code, watch.Body.String())
	}
}

// TestWatchIsBoundToTheSubstrateItWasGranted keeps observation inside the same
// boundary as the work. The subject owns the run and holds sprout.watch — for
// a different substrate than the one the run targeted.
func TestWatchIsBoundToTheSubstrateItWasGranted(t *testing.T) {
	mux, store := newWatchFixtureWithGrants(t, []core.DelegationGrant{
		{Pollen: watchOwner, OperationClasses: []string{core.CapSproutWatch}, Substrates: []string{"otherrepo"}},
	})
	seedWatchRun(t, store, historydb.SproutRun{
		RunID: "run-owner", SessionID: watchSubject, StepID: "run-owner",
		Pollen: watchOwner, Substrate: "myrepo", Status: "matured",
	})

	events := watchRequest(t, mux, "/v1/phytomers/"+watchSubject+"/events", watchOwner)
	if events.Code != http.StatusForbidden {
		t.Fatalf("watch grant on another substrate = %d, want 403: %s", events.Code, events.Body.String())
	}
	watch := watchRequest(t, mux, "/v1/phytomers/"+watchSubject+"/watch", watchOwner)
	if watch.Code != http.StatusForbidden {
		t.Fatalf("watch grant on another substrate watching = %d, want 403: %s", watch.Code, watch.Body.String())
	}
}

// TestPartialSubstrateGrantDoesNotCoverPhytomer pins the "every one, not any
// one" semantics of authorizeSubstrates. The caller owns every run in the
// phytomer — both dispatched by watchOwner — and holds a real sprout.watch
// grant, but the grant only covers "myrepo". Because one run targeted
// "otherrepo", the grant does not cover the whole phytomer and every surface
// must refuse.
//
// Without this fixture the mutation that replaces "deny on first failure" with
// "return true on first success" passes all observation tests, because every
// other fixture puts only one substrate per phytomer, so the loop never
// iterates more than once.
func TestPartialSubstrateGrantDoesNotCoverPhytomer(t *testing.T) {
	mux, store := newWatchFixtureWithGrants(t, []core.DelegationGrant{
		{Pollen: watchOwner, OperationClasses: []string{core.CapSproutWatch}, Substrates: []string{"myrepo"}},
	})
	seedWatchRun(t, store, historydb.SproutRun{
		RunID: "run-myrepo", SessionID: watchSubject, StepID: "run-myrepo",
		Pollen: watchOwner, Substrate: "myrepo", Status: "matured",
	})
	// Second run by the same caller into the same phytomer — but "otherrepo",
	// which the grant does not reach.
	seedWatchRun(t, store, historydb.SproutRun{
		RunID: "run-otherrepo", SessionID: watchSubject, StepID: "run-otherrepo",
		Pollen: watchOwner, Substrate: "otherrepo", Status: "matured",
	})

	// Events: the phytomer is not divisible, so a partial-substrate grant
	// does not release it.
	events := watchRequest(t, mux, "/v1/phytomers/"+watchSubject+"/events", watchOwner)
	if events.Code != http.StatusForbidden {
		t.Fatalf("events with partial-substrate grant = %d, want 403: %s", events.Code, events.Body.String())
	}
	if body := events.Body.String(); strings.Contains(body, "sessionId") {
		t.Fatalf("a refused events read still carried phytomer data: %s", body)
	}

	// Runs: AuthorizeRuns collects both substrates from the caller's own runs,
	// so it must also refuse — the "any one suffices" mutation would return 200
	// here and hand the caller both run records.
	runs := watchRequest(t, mux, "/v1/phytomers/"+watchSubject+"/sprout-runs", watchOwner)
	if runs.Code != http.StatusForbidden {
		t.Fatalf("sprout-runs with partial-substrate grant = %d, want 403: %s", runs.Code, runs.Body.String())
	}
	if body := runs.Body.String(); strings.Contains(body, "run-otherrepo") || strings.Contains(body, "run-myrepo") {
		t.Fatalf("a refused sprout-runs read still carried run records: %s", body)
	}

	watch := watchRequest(t, mux, "/v1/phytomers/"+watchSubject+"/watch", watchOwner)
	if watch.Code != http.StatusForbidden {
		t.Fatalf("watch with partial-substrate grant = %d, want 403: %s", watch.Code, watch.Body.String())
	}
	if body := watch.Body.String(); strings.Contains(body, "run-otherrepo") || strings.Contains(body, "run-myrepo") || strings.Contains(body, "event: observation") {
		t.Fatalf("a refused watch still carried observation: %s", body)
	}
}

// TestWatchDoesNotEscalateLikeWork pins the impact classification. Looking at a
// run is not doing one, so a grant that escalates high-impact operations back
// to a human must not put a read in that queue — an observer interrupted for
// confirmation is an observer who cannot observe, on the path that exists
// precisely so nobody has to be asked.
func TestWatchDoesNotEscalateLikeWork(t *testing.T) {
	mux, store := newWatchFixtureWithGrants(t, []core.DelegationGrant{{
		Pollen:             watchOwner,
		OperationClasses:   []string{core.CapSproutWatch},
		Substrates:         []string{"myrepo"},
		ConfirmAboveImpact: core.DelegationImpactHigh,
	}})
	seedWatchRun(t, store, historydb.SproutRun{
		RunID: "run-owner", SessionID: watchSubject, StepID: "run-owner",
		Pollen: watchOwner, Substrate: "myrepo", Status: "matured",
	})

	events := watchRequest(t, mux, "/v1/phytomers/"+watchSubject+"/events", watchOwner)
	if events.Code != http.StatusOK {
		t.Fatalf("watch under a confirm-above-high grant = %d, want 200: %s", events.Code, events.Body.String())
	}
}

// TestSharedPhytomerReleasesRunsButNotEvents pins the asymmetry deliberately.
// A run record names its own subject and is therefore filterable; a phytomer's
// events do not and are therefore released whole or not at all. A subject that
// dispatched one run into a phytomer another subject also used gets its own run
// back and is refused the phytomer's telemetry.
func TestSharedPhytomerReleasesRunsButNotEvents(t *testing.T) {
	mux, store := newWatchFixture(t)
	seedWatchRun(t, store, historydb.SproutRun{
		RunID: "run-intruder", SessionID: watchSubject, StepID: "run-intruder",
		Pollen: watchOther, Substrate: "myrepo", Status: "matured",
	})

	events := watchRequest(t, mux, "/v1/phytomers/"+watchSubject+"/events", watchOwner)
	if events.Code != http.StatusForbidden {
		t.Fatalf("events of a shared phytomer = %d, want 403: %s", events.Code, events.Body.String())
	}

	runs := watchRequest(t, mux, "/v1/phytomers/"+watchSubject+"/sprout-runs", watchOwner)
	if runs.Code != http.StatusOK {
		t.Fatalf("runs of a shared phytomer = %d, want 200: %s", runs.Code, runs.Body.String())
	}
	recorded := decodeRuns(t, runs.Body.Bytes())
	if len(recorded) != 1 || recorded[0].RunID != "run-owner" {
		t.Fatalf("shared phytomer returned %d run(s), want only run-owner: %+v", len(recorded), recorded)
	}

	watch := watchRequest(t, mux, "/v1/phytomers/"+watchSubject+"/watch", watchOwner)
	if watch.Code != http.StatusForbidden {
		t.Fatalf("watch of a shared phytomer = %d, want 403: %s", watch.Code, watch.Body.String())
	}
}

// TestEmptyPhytomerBelongsToNobody covers the phytomer nothing was dispatched
// into. There is no owner to compare against, so a delegated observer is
// refused rather than handed session-wide telemetry by default.
func TestEmptyPhytomerBelongsToNobody(t *testing.T) {
	mux, _ := newWatchFixture(t)

	events := watchRequest(t, mux, "/v1/phytomers/tendril-empty/events", watchOwner)
	if events.Code != http.StatusForbidden {
		t.Fatalf("events of an empty phytomer = %d, want 403: %s", events.Code, events.Body.String())
	}
	runs := watchRequest(t, mux, "/v1/phytomers/tendril-empty/sprout-runs", watchOwner)
	if runs.Code != http.StatusForbidden {
		t.Fatalf("runs of an empty phytomer = %d, want 403: %s", runs.Code, runs.Body.String())
	}
	watch := watchRequest(t, mux, "/v1/phytomers/tendril-empty/watch", watchOwner)
	if watch.Code != http.StatusForbidden {
		t.Fatalf("watch of an empty phytomer = %d, want 403: %s", watch.Code, watch.Body.String())
	}
}

// TestObservationLaneDoesNotWidenTheCommandLane guards the split introduced by
// Register: giving the views a delegable operation-class must not put the
// command routes mounted beside them on the same footing.
func TestObservationLaneDoesNotWidenTheCommandLane(t *testing.T) {
	mux, _ := newWatchFixture(t)

	request := httptest.NewRequest(http.MethodPost, "/v1/phytomers/"+watchSubject+"/sequences/grow", nil)
	request.Header.Set(PollenHeader, watchOwner)
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusForbidden {
		t.Fatalf("delegated sequence grow = %d, want 403: %s", recorder.Code, recorder.Body.String())
	}
}

// TestDispatchedRunIsObservableByItsDispatcher joins the two halves of the
// claim end to end: a delegated caller starts a detached run, and the record of
// that run is readable by it — and by nobody else — from the moment the
// dispatch is accepted, without the operator's key standing in anywhere.
func TestDispatchedRunIsObservableByItsDispatcher(t *testing.T) {
	store, err := historydb.Open(context.Background(), filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatalf("open history store: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	// The run is held mid-flight until this test releases it, so the reads
	// below happen against the record written when the dispatch was accepted
	// rather than the one written when it finished. A caller that polls the
	// instant it has its handle is the case that matters, and letting the run
	// complete first would let a surface that only attributes finished runs
	// pass.
	release := make(chan struct{})
	executed := &atomic.Int64{}
	coreSvc := core.NewService(nil).WithSprout(core.SproutOperations{
		Run: func(ctx context.Context, spec core.SproutSpec) (core.SproutRunReport, error) {
			<-release
			executed.Add(1)
			return core.SproutRunReport{Output: "grown", Outcome: "complete"}, nil
		},
	})

	gate := &DelegationGate{
		Authorizer: core.NewDelegationAuthorizer([]core.DelegationGrant{
			{
				Pollen:           watchOwner,
				OperationClasses: []string{core.CapSproutGrow, core.CapSproutWatch},
				Substrates:       []string{"myrepo"},
			},
			{
				Pollen:           watchOther,
				OperationClasses: []string{core.CapSproutGrow, core.CapSproutWatch},
				Substrates:       []string{"myrepo"},
			},
		}),
		Bus: eventbus.New(),
	}

	mux := http.NewServeMux()
	NewSproutHandler(coreSvc, store, nil).WithDelegation(gate).Register(mux, nil)
	NewSessionsHandler(coreSvc, nil, store, nil).
		WithWatch(NewWatchAuthority(gate, store)).
		Register(mux, gate.Middleware, nil)

	dispatch := httptest.NewRequest(http.MethodPost, "/v1/phytomers/tendril-dispatched/sprout/grow",
		strings.NewReader(`{"transcript":"grow","substrate":"myrepo"}`))
	dispatch.Header.Set(PollenHeader, watchOwner)
	dispatched := httptest.NewRecorder()
	mux.ServeHTTP(dispatched, dispatch)
	if dispatched.Code != http.StatusAccepted {
		t.Fatalf("delegated dispatch = %d, want 202: %s", dispatched.Code, dispatched.Body.String())
	}

	own := watchRequest(t, mux, "/v1/phytomers/tendril-dispatched/sprout-runs", watchOwner)
	if own.Code != http.StatusOK {
		t.Fatalf("dispatcher reading its own run = %d, want 200: %s", own.Code, own.Body.String())
	}
	if len(decodeRuns(t, own.Body.Bytes())) != 1 {
		t.Fatalf("dispatcher saw no record of the run it started: %s", own.Body.String())
	}

	events := watchRequest(t, mux, "/v1/phytomers/tendril-dispatched/events", watchOwner)
	if events.Code != http.StatusOK {
		t.Fatalf("dispatcher reading its own events = %d, want 200: %s", events.Code, events.Body.String())
	}

	foreign := watchRequest(t, mux, "/v1/phytomers/tendril-dispatched/sprout-runs", watchOther)
	if foreign.Code != http.StatusForbidden {
		t.Fatalf("another subject reading the dispatched run = %d, want 403: %s", foreign.Code, foreign.Body.String())
	}

	// The inner Run returning is not a receptor terminal write. Ownership was
	// settled on the opening row and must still be the dispatcher's after the
	// Core call comes back — including when that call is a non-terminal detach.
	close(release)
	waitForExecutions(t, executed, 1)
	settled := watchRequest(t, mux, "/v1/phytomers/tendril-dispatched/sprout-runs", watchOwner)
	if settled.Code != http.StatusOK {
		t.Fatalf("dispatcher reading its run after inner return = %d, want 200: %s", settled.Code, settled.Body.String())
	}
	runs := decodeRuns(t, settled.Body.Bytes())
	if len(runs) != 1 || runs[0].Pollen != watchOwner {
		t.Fatalf("run is no longer owned by its dispatcher: %+v", runs)
	}
	if runs[0].Status != "running" {
		t.Fatalf("receptor settled the run to %q; only the conductor observer may write a terminal status", runs[0].Status)
	}
}

// TestUnwiredWatchAuthorityDenies is the deny-closed posture: a handler built
// without an observation authority still serves the operator and still refuses
// every delegated observer, rather than treating "not configured" as "allowed".
func TestSeedOwnedPhytomerIsWatchableBeforeSprout(t *testing.T) {
	mux, store := newWatchFixtureWithGrants(t, []core.DelegationGrant{
		{Pollen: watchOwner, OperationClasses: []string{core.CapSproutWatch}, Substrates: []string{"myrepo"}},
		{Pollen: watchOther, OperationClasses: []string{core.CapSproutWatch}, Substrates: []string{"myrepo"}},
	})
	if err := store.RecordSeedRun(context.Background(), historydb.SeedRun{
		Handle: "seed-owned", Pollen: watchOwner, PhytomerID: "tendril-seed-owned",
		Substrate: "myrepo", Status: "running", StartedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("record seed: %v", err)
	}

	events := watchRequest(t, mux, "/v1/phytomers/tendril-seed-owned/events", watchOwner)
	if events.Code != http.StatusOK {
		t.Fatalf("owner watching seed phytomer before sprout = %d, want 200: %s", events.Code, events.Body.String())
	}
	runs := watchRequest(t, mux, "/v1/phytomers/tendril-seed-owned/sprout-runs", watchOwner)
	if runs.Code != http.StatusOK {
		t.Fatalf("owner listing seed phytomer runs before sprout = %d, want 200: %s", runs.Code, runs.Body.String())
	}
	if len(decodeRuns(t, runs.Body.Bytes())) != 0 {
		t.Fatalf("expected no sprout rows yet: %s", runs.Body.String())
	}

	foreign := watchRequest(t, mux, "/v1/phytomers/tendril-seed-owned/events", watchOther)
	if foreign.Code != http.StatusForbidden {
		t.Fatalf("another pollen watching seed phytomer = %d, want 403: %s", foreign.Code, foreign.Body.String())
	}
}

func TestContradictorySeedSproutOwnershipFailsClosed(t *testing.T) {
	mux, store := newWatchFixtureWithGrants(t, []core.DelegationGrant{
		{Pollen: watchOwner, OperationClasses: []string{core.CapSproutWatch}, Substrates: []string{"myrepo"}},
		{Pollen: watchOther, OperationClasses: []string{core.CapSproutWatch}, Substrates: []string{"myrepo"}},
	})
	if err := store.RecordSeedRun(context.Background(), historydb.SeedRun{
		Handle: "seed-mixed", Pollen: watchOwner, PhytomerID: "tendril-seed-mixed",
		Substrate: "myrepo", Status: "running", StartedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("record seed: %v", err)
	}
	seedWatchRun(t, store, historydb.SproutRun{
		RunID: "run-intruder", SessionID: "tendril-seed-mixed", StepID: "run-intruder",
		Pollen: watchOther, Substrate: "myrepo", Status: "running",
	})

	events := watchRequest(t, mux, "/v1/phytomers/tendril-seed-mixed/events", watchOwner)
	if events.Code != http.StatusForbidden {
		t.Fatalf("contradictory ownership events = %d, want 403: %s", events.Code, events.Body.String())
	}
	runs := watchRequest(t, mux, "/v1/phytomers/tendril-seed-mixed/sprout-runs", watchOwner)
	if runs.Code != http.StatusForbidden {
		t.Fatalf("contradictory ownership runs = %d, want 403: %s", runs.Code, runs.Body.String())
	}
	watch := watchRequest(t, mux, "/v1/phytomers/tendril-seed-mixed/watch", watchOwner)
	if watch.Code != http.StatusForbidden {
		t.Fatalf("contradictory ownership watch = %d, want 403: %s", watch.Code, watch.Body.String())
	}
}

func TestSeedGrowGrantDoesNotImplyWatch(t *testing.T) {
	mux, store := newWatchFixtureWithGrants(t, []core.DelegationGrant{
		{Pollen: watchOwner, OperationClasses: []string{core.CapSeedGrow}, Substrates: []string{"myrepo"}},
	})
	if err := store.RecordSeedRun(context.Background(), historydb.SeedRun{
		Handle: "seed-grow-only", Pollen: watchOwner, PhytomerID: "tendril-seed-grow-only",
		Substrate: "myrepo", Status: "running", StartedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("record seed: %v", err)
	}
	events := watchRequest(t, mux, "/v1/phytomers/tendril-seed-grow-only/events", watchOwner)
	if events.Code != http.StatusForbidden {
		t.Fatalf("seed.grow without sprout.watch = %d, want 403: %s", events.Code, events.Body.String())
	}
	watch := watchRequest(t, mux, "/v1/phytomers/tendril-seed-grow-only/watch", watchOwner)
	if watch.Code != http.StatusForbidden {
		t.Fatalf("seed.grow without sprout.watch watching = %d, want 403: %s", watch.Code, watch.Body.String())
	}
}

func TestUnwiredWatchAuthorityDenies(t *testing.T) {
	store, err := historydb.Open(context.Background(), filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatalf("open history store: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	seedWatchRun(t, store, historydb.SproutRun{
		RunID: "run-owner", SessionID: watchSubject, StepID: "run-owner",
		Pollen: watchOwner, Substrate: "myrepo", Status: "matured",
	})

	mux := http.NewServeMux()
	NewSessionsHandler(core.NewService(nil), nil, store, nil).Register(mux, nil, nil)

	delegated := watchRequest(t, mux, "/v1/phytomers/"+watchSubject+"/events", watchOwner)
	if delegated.Code != http.StatusForbidden {
		t.Fatalf("delegated read with no authority = %d, want 403: %s", delegated.Code, delegated.Body.String())
	}
	operator := watchRequest(t, mux, "/v1/phytomers/"+watchSubject+"/events", "")
	if operator.Code != http.StatusOK {
		t.Fatalf("operator read with no authority = %d, want 200: %s", operator.Code, operator.Body.String())
	}
	delegatedWatch := watchRequest(t, mux, "/v1/phytomers/"+watchSubject+"/watch", watchOwner)
	if delegatedWatch.Code != http.StatusForbidden {
		t.Fatalf("delegated watch with no authority = %d, want 403: %s", delegatedWatch.Code, delegatedWatch.Body.String())
	}
}
