package receptors

import (
	"net/http"
	"strings"

	"github.com/opentendril/opentendril/cmd/stem/internal/core"
	"github.com/opentendril/opentendril/cmd/stem/internal/gateway"
	"github.com/opentendril/opentendril/cmd/stem/internal/historydb"
)

// WatchAuthority decides who may observe a phytomer — its stored run records,
// its persisted events, and its live stream. All three surfaces route through
// this one type, because three copies of an ownership rule are three rules, and
// the widening this exists to prevent would appear in whichever copy was
// forgotten.
//
// Two subjects reach it. The operator holds the Stem's own key and no Pollen:
// it is scoped to nothing and keeps the whole view it has always had. A
// delegated caller holds a credential that resolves to a Pollen, and sees only
// what that Pollen dispatched.
type WatchAuthority struct {
	delegation *DelegationGate
	history    *historydb.Store
}

// NewWatchAuthority builds the observation authority over the delegation gate
// that resolves callers and the run store that records who dispatched what.
// Either may be nil; a nil store cannot establish ownership and a nil gate
// cannot authorise, so both deny every delegated observer while leaving the
// operator's view untouched.
func NewWatchAuthority(gate *DelegationGate, history *historydb.Store) *WatchAuthority {
	return &WatchAuthority{delegation: gate, history: history}
}

func (a *WatchAuthority) gate() *DelegationGate {
	if a == nil {
		return nil
	}
	return a.delegation
}

func (a *WatchAuthority) store() *historydb.Store {
	if a == nil {
		return nil
	}
	return a.history
}

// Observer resolves the subject a request observes as, and reports whether the
// request may proceed at all. A blank Pollen with ok=true is the operator.
func (a *WatchAuthority) Observer(w http.ResponseWriter, r *http.Request) (pollen string, ok bool) {
	pollen, credentialOK := a.gate().PollenFor(r)
	if !credentialOK {
		http.Error(w, "delegation denied: unknown or revoked Pollinator credential", http.StatusForbidden)
		return "", false
	}
	return pollen, true
}

// AuthorizePhytomer releases a whole phytomer to a delegated observer, and is
// the strict half of the rule: every sprout run recorded against the phytomer
// must have been dispatched by this subject, and the subject must hold a
// sprout.watch grant covering every substrate those runs targeted.
//
// It is strict because what it releases cannot be filtered. A phytomer's event
// stream is session-wide telemetry with no per-run owner on it, so it is either
// entirely the observer's or it is not the observer's at all — and a phytomer
// nothing was ever dispatched into belongs to nobody, which denies.
func (a *WatchAuthority) AuthorizePhytomer(w http.ResponseWriter, r *http.Request, pollen, sessionID string) bool {
	store := a.store()
	if store == nil {
		http.Error(w, "delegation denied: no run record is available to establish ownership", http.StatusForbidden)
		return false
	}

	sessionID = strings.TrimSpace(sessionID)
	seed, hasSeed, err := store.GetSeedRunByPhytomer(r.Context(), sessionID)
	if err != nil {
		http.Error(w, "failed to read seed ownership: "+err.Error(), http.StatusInternalServerError)
		return false
	}

	owners, err := store.SproutRunOwners(r.Context(), sessionID)
	if err != nil {
		http.Error(w, "failed to read run ownership: "+err.Error(), http.StatusInternalServerError)
		return false
	}

	if hasSeed {
		if seed.Pollen != pollen {
			http.Error(w, "delegation denied: this phytomer carries a run dispatched by another subject", http.StatusForbidden)
			return false
		}
		if !seedSproutOwnershipAgrees(seed, owners) {
			http.Error(w, "delegation denied: seed and sprout ownership evidence disagree", http.StatusForbidden)
			return false
		}
		return a.authorizeSubstrates(w, pollen, []string{seed.Substrate})
	}

	substrates := make([]string, 0, len(owners))
	for _, owner := range owners {
		if owner.Pollen != pollen {
			http.Error(w, "delegation denied: this phytomer carries a run dispatched by another subject", http.StatusForbidden)
			return false
		}
		substrates = append(substrates, owner.Substrate)
	}
	return a.authorizeSubstrates(w, pollen, substrates)
}

// AuthorizeRuns releases the subset of a phytomer's run records that the
// delegated observer dispatched, and reports that subset. Unlike a phytomer's
// events, a run record names its own subject, so the answer is a filter rather
// than an all-or-nothing release — and a subject holding none of them is
// refused outright rather than handed an empty list it might read as "no runs
// happened".
func (a *WatchAuthority) AuthorizeRuns(w http.ResponseWriter, r *http.Request, pollen, sessionID string, runs []historydb.SproutRun) ([]historydb.SproutRun, bool) {
	store := a.store()
	sessionID = strings.TrimSpace(sessionID)
	if store != nil && sessionID != "" {
		seed, hasSeed, err := store.GetSeedRunByPhytomer(r.Context(), sessionID)
		if err != nil {
			http.Error(w, "failed to read seed ownership: "+err.Error(), http.StatusInternalServerError)
			return nil, false
		}
		if hasSeed {
			if seed.Pollen != pollen {
				http.Error(w, "delegation denied: this phytomer carries a run dispatched by another subject", http.StatusForbidden)
				return nil, false
			}
			owners, err := store.SproutRunOwners(r.Context(), sessionID)
			if err != nil {
				http.Error(w, "failed to read run ownership: "+err.Error(), http.StatusInternalServerError)
				return nil, false
			}
			if !seedSproutOwnershipAgrees(seed, owners) {
				http.Error(w, "delegation denied: seed and sprout ownership evidence disagree", http.StatusForbidden)
				return nil, false
			}
			owned := make([]historydb.SproutRun, 0, len(runs))
			for _, run := range runs {
				if run.Pollen != pollen {
					continue
				}
				owned = append(owned, run)
			}
			if !a.authorizeSubstrates(w, pollen, []string{seed.Substrate}) {
				return nil, false
			}
			return owned, true
		}
	}

	owned := make([]historydb.SproutRun, 0, len(runs))
	substrates := make([]string, 0, len(runs))
	seen := make(map[string]bool, len(runs))
	for _, run := range runs {
		if run.Pollen != pollen {
			continue
		}
		owned = append(owned, run)
		if !seen[run.Substrate] {
			seen[run.Substrate] = true
			substrates = append(substrates, run.Substrate)
		}
	}
	if !a.authorizeSubstrates(w, pollen, substrates) {
		return nil, false
	}
	return owned, true
}

func seedSproutOwnershipAgrees(seed historydb.SeedRun, owners []historydb.SproutRunOwner) bool {
	for _, owner := range owners {
		if owner.Pollen != seed.Pollen || owner.Substrate != seed.Substrate {
			return false
		}
	}
	return true
}

// StreamMiddleware gates the live event stream. The operator passes through
// untouched and keeps the unfiltered feed. A delegated caller must name the
// phytomer it is watching, must own that phytomer outright, and is handed a
// stream narrowed to it — the alternative, letting a grant onto the unfiltered
// feed, would trade a caller that cannot watch its own work for one that
// watches everybody's.
func (a *WatchAuthority) StreamMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		pollen, ok := a.Observer(w, r)
		if !ok {
			return
		}
		if pollen == "" {
			next(w, r)
			return
		}
		sessionID := strings.TrimSpace(r.URL.Query().Get("sessionId"))
		if sessionID == "" {
			http.Error(w, "delegation denied: a delegated stream must name the phytomer it watches (sessionId)", http.StatusForbidden)
			return
		}
		if !a.AuthorizePhytomer(w, r, pollen, sessionID) {
			return
		}
		next(w, r.WithContext(gateway.WithStreamScope(r.Context(), sessionID)))
	}
}

// authorizeSubstrates requires an active sprout.watch grant for every substrate
// the observer's own runs targeted. Every one, not any one: a phytomer's
// telemetry is not divisible by substrate, so a grant covering part of it does
// not cover it. An empty set means the observer dispatched nothing here and is
// refused before any grant is consulted.
func (a *WatchAuthority) authorizeSubstrates(w http.ResponseWriter, pollen string, substrates []string) bool {
	if len(substrates) == 0 {
		http.Error(w, "delegation denied: nothing in this phytomer was dispatched by Pollen \""+pollen+"\"", http.StatusForbidden)
		return false
	}
	for _, substrate := range substrates {
		decision := a.gate().Authorize(core.DelegationRequest{
			Pollen:         pollen,
			OperationClass: core.CapSproutWatch,
			Substrate:      strings.TrimSpace(substrate),
			Impact:         core.CapabilityImpact(core.CapSproutWatch),
		})
		if !decision.Authorized {
			http.Error(w, "delegation denied: "+decision.Reason, http.StatusForbidden)
			return false
		}
	}
	return true
}
