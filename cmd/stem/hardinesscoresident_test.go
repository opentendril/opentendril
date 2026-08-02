package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
)

// healthServingOwner stands up a health surface publishing the given owner, and
// points address resolution at it. A nil owner publishes no owner at all, which
// is a different condition from publishing someone else's.
func healthServingOwner(t *testing.T, owner *int) {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(struct {
			Owner *int `json:"owner,omitempty"`
		}{Owner: owner})
	}))
	t.Cleanup(server.Close)

	parts := strings.Split(strings.TrimPrefix(server.URL, "http://"), ":")
	if len(parts) != 2 {
		t.Fatalf("unexpected server URL %q", server.URL)
	}
	t.Setenv("TERROIR_HOST", parts[0])
	t.Setenv("PORT", parts[1])
}

// The condition the observation exists for: another principal's Stem is serving
// here, and the graded findings would never mention it.
func TestCoResidentStemIsReportedWhenOwnedByAnotherPrincipal(t *testing.T) {
	other := os.Getuid() + 1
	healthServingOwner(t, &other)

	got := coResidentStemObservation(context.Background())
	if got == "" {
		t.Fatal("a Stem owned by another principal was not reported")
	}
	if !strings.Contains(got, strconv.Itoa(other)) {
		t.Errorf("observation does not name the owning principal %d: %s", other, got)
	}
	if !strings.Contains(got, os.Getenv("TERROIR_HOST")) {
		t.Errorf("observation does not name the address probed: %s", got)
	}
}

// The regression that matters most. A single-principal installation running its
// own Stem must say nothing — this is the common case, and a report that fires
// on it would be noise on every well-configured host.
func TestNoObservationWhenTheStemIsThisAccountsOwn(t *testing.T) {
	mine := os.Getuid()
	healthServingOwner(t, &mine)

	if got := coResidentStemObservation(context.Background()); got != "" {
		t.Errorf("reported a co-resident Stem for this account's own Stem: %s", got)
	}
}

// Answering without publishing an owner does not establish a second principal.
// Reporting one would be asserting something the probe did not observe.
func TestNoObservationWhenOwnerIsNotPublished(t *testing.T) {
	healthServingOwner(t, nil)

	if got := coResidentStemObservation(context.Background()); got != "" {
		t.Errorf("reported a co-resident Stem from a report carrying no owner: %s", got)
	}
}

func TestNoObservationWhenNothingIsServing(t *testing.T) {
	// A port nothing is listening on. The probe carries its own bound, so this
	// returns promptly rather than waiting on the caller's context.
	t.Setenv("TERROIR_HOST", "127.0.0.1")
	t.Setenv("PORT", "65533")

	if got := coResidentStemObservation(context.Background()); got != "" {
		t.Errorf("reported a co-resident Stem with nothing serving: %s", got)
	}
}

// The settled answer to the issue's second sub-question, asserted as behaviour:
// a co-resident Stem is a property of the host, so it must not change the grade
// of the installation being measured.
//
// Asserted on the counters the verdict is computed from rather than on rendered
// text, because the verdict line is chosen from exactly these two numbers.
func TestCoResidentStemDoesNotChangeTheGrade(t *testing.T) {
	// One control plane for both measurements, so the only thing differing
	// between them is whether another principal's Stem is serving.
	tendrilDir := t.TempDir()

	// Measured with a co-resident Stem present.
	other := os.Getuid() + 1
	healthServingOwner(t, &other)

	if coResidentStemObservation(context.Background()) == "" {
		t.Fatal("no observation produced, so this test proves nothing about grading")
	}
	withCoResident := severities(collectHardinessFindings(context.Background(), tendrilDir))

	// Measured again with nothing serving. Pointing at a dead port rather than
	// stopping the server keeps the change to the single condition under test.
	t.Setenv("TERROIR_HOST", "127.0.0.1")
	t.Setenv("PORT", "65533")

	if coResidentStemObservation(context.Background()) != "" {
		t.Fatal("observation still produced with nothing serving; the two measurements are not distinct")
	}
	withoutCoResident := severities(collectHardinessFindings(context.Background(), tendrilDir))

	// Comparing the graded findings themselves, not the rendered text. The
	// verdict is computed from these severities, so identical severities is
	// exactly the claim "the grade did not change" — and it holds whatever
	// title a future finding might be given.
	if withCoResident != withoutCoResident {
		t.Errorf("graded findings differ when a co-resident Stem is present\n with: %s\n without: %s", withCoResident, withoutCoResident)
	}
}

// severities renders the graded shape of a report: the ordered severities the
// verdict is derived from.
func severities(findings []hardinessFinding) string {
	parts := make([]string, 0, len(findings))
	for _, finding := range findings {
		parts = append(parts, finding.Severity)
	}
	return strings.Join(parts, ",")
}
