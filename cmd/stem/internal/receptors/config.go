package receptors

import (
	"encoding/json"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/opentendril/opentendril/cmd/stem/internal/conductor"
	"github.com/opentendril/opentendril/cmd/stem/internal/core"
	"github.com/opentendril/opentendril/cmd/stem/internal/eventbus"
	"github.com/opentendril/opentendril/cmd/stem/internal/session"
)

// PollenHeader marks an HTTP request as a *delegated* capability
// invocation and names the trust-root Pollen exercising a delegation grant.
//
// It is a CLAIM, and it is only honoured when the caller presents no Pollinator
// credential. When a credential is presented the Pollen is DERIVED from it and
// this header is ignored entirely — a caller that could both authenticate and
// name itself could name any identity, which is the gap Tier 2 closes.
const PollenHeader = "X-OpenTendril-Pollen"

// bearerToken extracts the presented bearer value, or "" when absent.
func bearerToken(r *http.Request) string {
	header := strings.TrimSpace(r.Header.Get("Authorization"))
	if !strings.HasPrefix(header, "Bearer ") {
		return ""
	}
	return strings.TrimSpace(strings.TrimPrefix(header, "Bearer "))
}

// PollinatorCredentials is the set a surface resolves presented credentials
// against. Empty means none have been issued, so no caller can authenticate as
// a Pollen by credential — the secure default.
type PollinatorCredentials []core.PollinatorCredential

// AccessTokenVerifier verifies a presented access token and returns its claims.
// It is satisfied by the Stem's own signer; a nil verifier proves nothing, so a
// token-bearing request denies (deny-closed) rather than falling back.
type AccessTokenVerifier interface {
	VerifyAccessToken(token string) (core.AccessTokenClaims, bool)
}

// DelegatedPollen returns the Pollen this request acts as, and whether it was
// PROVEN (by a token or a credential) rather than merely claimed.
//
// Three paths, in precedence order:
//
//   - An access token is proven by SIGNATURE: the verifier validates it and the
//     Pollen comes from its claims. A token that does not verify (or a nil
//     verifier) resolves to nothing and is denied. This is a boundary.
//   - A Pollinator credential resolves to the Pollen it was issued for. The
//     caller cannot influence the result; an unknown or revoked credential
//     resolves to nothing and is denied. This is a boundary.
//   - Neither presented: the header claim is returned, as before. This is an
//     audit control, and it is what a Botanist's own key plus a header has
//     always been.
//
// A caller presenting a token or a credential can never fall back to the claim,
// so proving one identity can never be used to assert a different one.
func DelegatedPollen(r *http.Request, credentials PollinatorCredentials, verifier AccessTokenVerifier) (pollen string, proven bool) {
	presented := bearerToken(r)
	if core.LooksLikeAccessToken(presented) {
		// Deliberately terminal, exactly like a credential-shaped bearer: a
		// token is verified or denied, and never degrades into the header claim.
		if verifier != nil {
			if claims, ok := verifier.VerifyAccessToken(presented); ok {
				return claims.Pollen, true
			}
		}
		return "", true
	}
	if core.LooksLikePollinatorCredential(presented) {
		// Deliberately terminal: a credential-shaped bearer is resolved or
		// denied. It never degrades into the header claim.
		return core.ResolvePollenFromCredential(credentials, presented), true
	}
	return strings.TrimSpace(r.Header.Get(PollenHeader)), false
}

// PollenFor resolves the Pollen a request acts as, and reports whether the
// request may proceed at all.
//
// ok is false in exactly one case: the caller presented a Pollinator
// credential that did not resolve — unknown, malformed or revoked. That must
// DENY rather than fall through to the plain bearer-authenticated path, which
// would let a revoked credential quietly become an ungoverned request. Every
// unresolvable credential fails the same way, so nothing distinguishes "never
// existed" from "revoked yesterday".
func (g *DelegationGate) PollenFor(r *http.Request) (pollen string, ok bool) {
	var credentials PollinatorCredentials
	var verifier AccessTokenVerifier
	if g != nil {
		credentials = g.Pollinators
		verifier = g.Signer
	}
	resolved, proven := DelegatedPollen(r, credentials, verifier)
	if proven && resolved == "" {
		return "", false
	}
	return resolved, true
}

// DelegationGate couples the Core's grant authorizer with the audit lane:
// every delegated invocation it evaluates — authorized or denied — is
// published to the EventBus (and thereby persisted to history.db by the
// historydb sink). A nil gate, or a gate with no authorizer, denies every
// delegated invocation: with no delegation configured, delegation is
// impossible while non-delegated traffic is untouched.
type DelegationGate struct {
	// Pollinators is the set of issued credentials this surface resolves
	// presented bearers against. Empty means none were issued, so no caller can
	// authenticate as a Pollen by credential.
	Pollinators PollinatorCredentials
	// Signer verifies presented access tokens. Nil means no token can be proven,
	// so a token-bearing request denies (deny-closed) — the same posture as an
	// empty credential set.
	Signer     AccessTokenVerifier
	Authorizer *core.DelegationAuthorizer
	Bus        *eventbus.Bus
}

// Authorize evaluates one delegated invocation against the active grants and
// audits the outcome.
func (g *DelegationGate) Authorize(request core.DelegationRequest) core.DelegationDecision {
	var decision core.DelegationDecision
	if g == nil || g.Authorizer == nil {
		decision = core.DelegationDecision{Reason: "delegation is not configured"}
	} else {
		decision = g.Authorizer.Authorize(request)
	}
	g.audit(request, decision)
	return decision
}

// Middleware gates a route that exposes no delegable operation-class:
// non-delegated requests pass through untouched, while delegated-marked
// requests are denied and audited (security-first default — a delegation
// attempt must never silently run as a plain invocation).
func (g *DelegationGate) Middleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		pollen, credentialOK := g.PollenFor(r)
		if !credentialOK {
			reason := "unknown or revoked Pollinator credential"
			g.audit(core.DelegationRequest{}, core.DelegationDecision{Reason: reason})
			http.Error(w, "delegation denied: "+reason, http.StatusForbidden)
			return
		}
		if pollen == "" {
			next(w, r)
			return
		}
		reason := "this endpoint exposes no delegable operation-class"
		g.audit(core.DelegationRequest{Pollen: pollen}, core.DelegationDecision{Reason: reason})
		http.Error(w, "delegation denied: "+reason, http.StatusForbidden)
	}
}

// audit publishes the delegation decision to the EventBus; the historydb sink
// persists it to history.db. Best-effort: a nil gate or bus only loses the
// record, never the enforcement.
func (g *DelegationGate) audit(request core.DelegationRequest, decision core.DelegationDecision) {
	if g == nil || g.Bus == nil {
		return
	}
	eventType := eventbus.EventDelegationDenied
	if decision.Authorized {
		eventType = eventbus.EventDelegationAuthorized
	}
	data := map[string]any{
		"pollen":         request.Pollen,
		"operationClass": request.OperationClass,
		"substrate":      request.Substrate,
		"authorized":     decision.Authorized,
	}
	if decision.Reason != "" {
		data["reason"] = decision.Reason
	}
	g.Bus.Publish(eventbus.Event{
		Type:      eventType,
		Source:    "delegation-authorizer",
		Timestamp: time.Now().UTC(),
		Data:      data,
	})
}

// validConfigFileName reports whether a caller-supplied name is safe to embed
// in a filename inside the .tendril configuration tree. Valid names carry no
// path separators and no traversal component, so a request can never write
// outside the directory its handler targets. Shared by the REST config
// uploads and the MCP createGenotype tool so every surface enforces the same
// boundary.
func validConfigFileName(name string) bool {
	if name == "" || name == "." || name == ".." {
		return false
	}
	if !filepath.IsLocal(name + ".json") {
		return false
	}
	return !strings.ContainsAny(name, `/\`)
}

// ConfigHandler provides HTTP endpoints for managing .tendril configs
type ConfigHandler struct {
	TendrilDir string
	core       core.Core
	delegation *DelegationGate
}

func NewConfigHandler(coreSvc core.Core, tendrilDir string) *ConfigHandler {
	return &ConfigHandler{
		TendrilDir: tendrilDir,
		core:       coreSvc,
	}
}

func (h *ConfigHandler) WithDelegation(gate *DelegationGate) *ConfigHandler {
	h.delegation = gate
	return h
}

// Capabilities declares the capabilities governed by this handler.
func (h *ConfigHandler) Capabilities() []string {
	return []string{core.CapGenotypeCreate}
}

// Register mounts the configuration endpoints.
func (h *ConfigHandler) Register(mux *http.ServeMux, auth func(http.HandlerFunc) http.HandlerFunc) {
	mux.HandleFunc("/v1/config/triggers", func(w http.ResponseWriter, r *http.Request) {
		if auth != nil {
			auth(h.ListTriggers)(w, r)
		} else {
			h.ListTriggers(w, r)
		}
	})
	mux.HandleFunc("/v1/config/genotypes", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			if auth != nil {
				auth(h.ListGenotypes)(w, r)
			} else {
				h.ListGenotypes(w, r)
			}
		} else if r.Method == http.MethodPost {
			if auth != nil {
				auth(h.UploadGenotype)(w, r)
			} else {
				h.UploadGenotype(w, r)
			}
		} else {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
	})
	mux.HandleFunc("/v1/config/substrates", func(w http.ResponseWriter, r *http.Request) {
		if auth != nil {
			auth(h.ListSubstrates)(w, r)
		} else {
			h.ListSubstrates(w, r)
		}
	})
}

type Trigger struct {
	Name string `json:"name"`
	Size int64  `json:"size"`
}

// ListTriggers handles GET /v1/config/triggers
func (h *ConfigHandler) ListTriggers(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	triggersDir := filepath.Join(h.TendrilDir, "transduction", "hormonal-triggers")
	entries, err := os.ReadDir(triggersDir)
	if err != nil && !os.IsNotExist(err) {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	var triggers []Trigger
	for _, entry := range entries {
		if !entry.IsDir() {
			info, _ := entry.Info()
			triggers = append(triggers, Trigger{
				Name: entry.Name(),
				Size: info.Size(),
			})
		}
	}

	if triggers == nil {
		triggers = []Trigger{}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"triggers": triggers,
	})
}

// ListGenotypes handles GET /v1/config/genotypes
func (h *ConfigHandler) ListGenotypes(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	genotypesDir := filepath.Join(h.TendrilDir, "genotypes")
	entries, err := os.ReadDir(genotypesDir)
	if err != nil && !os.IsNotExist(err) {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	var genotypes []string
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".json") {
			genotypes = append(genotypes, strings.TrimSuffix(entry.Name(), ".json"))
		}
	}

	if genotypes == nil {
		genotypes = []string{}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"genotypes": genotypes,
	})
}

// ListSubstrates handles GET /v1/config/substrates. It is a view: names only,
// from the configured substrates.yaml. The adapter does not resolve checkouts.
func (h *ConfigHandler) ListSubstrates(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	names := []string{}
	config, err := conductor.LoadSubstratesConfig("")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if config != nil {
		for name := range config.Substrates {
			if trimmed := strings.TrimSpace(name); trimmed != "" {
				names = append(names, trimmed)
			}
		}
		sort.Strings(names)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"substrates": names,
	})
}

// UploadGenotype handles POST /v1/config/genotypes
func (h *ConfigHandler) UploadGenotype(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req core.GenotypeCreateInput
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid JSON body", http.StatusBadRequest)
		return
	}

	if strings.TrimSpace(req.Name) == "" || strings.TrimSpace(req.Instructions) == "" {
		http.Error(w, "name and instructions are required", http.StatusBadRequest)
		return
	}

	pollen, credentialOK := h.delegation.PollenFor(r)
	if !credentialOK {
		http.Error(w, "delegation denied: unknown or revoked Pollinator credential", http.StatusForbidden)
		return
	}
	if pollen != "" {
		decision := h.delegation.Authorize(core.DelegationRequest{
			Pollen:         pollen,
			OperationClass: core.CapGenotypeCreate,
			Substrate:      strings.TrimSpace(req.Substrate),
			Impact:         core.DelegationImpactMedium,
		})
		if !decision.Authorized {
			http.Error(w, "delegation denied: "+decision.Reason, http.StatusForbidden)
			return
		}
		r = r.WithContext(core.WithPollen(r.Context(), pollen))
	}

	if strings.TrimSpace(req.Origin) == "" {
		req.Origin = session.OriginREST
	}

	result, err := h.core.GenotypeCreate(r.Context(), req)
	if err != nil {
		if strings.Contains(err.Error(), "invalid genotype name") {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeCoreErr(w, err)
		return
	}

	log.Printf("Uploaded new AI Genotype: %s", req.Name)
	if err := SyncGenotypeIndex(); err != nil {
		log.Printf("Failed to sync genotype index after upload: %v", err)
	}

	writeJSON(w, http.StatusCreated, result)
}
