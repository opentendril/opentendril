package receptors

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/opentendril/opentendril/cmd/stem/internal/core"
	"github.com/opentendril/opentendril/cmd/stem/internal/eventbus"
)

// AuthMiddleware wraps a handler to require an ADMIN_TOKEN. Bearer presence
// authenticates the caller; *delegated* invocations (marked with
// DelegationSubjectHeader) are additionally gated by the delegation
// authorizer. These config routes expose no delegable operation-class, so a
// delegated-marked request is denied outright rather than silently executed
// as if it were non-delegated.
func AuthMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token := os.Getenv("ADMIN_TOKEN")
		if token != "" {
			authHeader := r.Header.Get("Authorization")
			if !strings.HasPrefix(authHeader, "Bearer ") || strings.TrimPrefix(authHeader, "Bearer ") != token {
				http.Error(w, "Unauthorized", http.StatusUnauthorized)
				return
			}
		}
		if subject := DelegatedSubject(r); subject != "" {
			log.Printf("🚫 Delegation denied for subject %q: %s exposes no delegable operation-class", subject, r.URL.Path)
			http.Error(w, "delegation denied: this endpoint exposes no delegable operation-class", http.StatusForbidden)
			return
		}
		next(w, r)
	}
}

// DelegationSubjectHeader marks an HTTP request as a *delegated* capability
// invocation and names the trust-root subject exercising a delegation grant.
// A request without this header is not delegated: it follows today's
// bearer-authenticated path untouched, whether or not any grants exist. The
// subject is a claim scoped by the already-required bearer key — a grant only
// ever narrows what an authenticated caller may run delegated, never widens
// what the bearer key already allows (short-lived scoped subject tokens are a
// later slice).
const DelegationSubjectHeader = "X-OpenTendril-Delegation-Subject"

// DelegatedSubject returns the delegated subject named by the request, or an
// empty string when the request is not a delegated invocation.
func DelegatedSubject(r *http.Request) string {
	return strings.TrimSpace(r.Header.Get(DelegationSubjectHeader))
}

// DelegationGate couples the Core's grant authorizer with the audit lane:
// every delegated invocation it evaluates — authorized or denied — is
// published to the EventBus (and thereby persisted to history.db by the
// historydb sink). A nil gate, or a gate with no authorizer, denies every
// delegated invocation: with no delegation configured, delegation is
// impossible while non-delegated traffic is untouched.
type DelegationGate struct {
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
		subject := DelegatedSubject(r)
		if subject == "" {
			next(w, r)
			return
		}
		reason := "this endpoint exposes no delegable operation-class"
		g.audit(core.DelegationRequest{Subject: subject}, core.DelegationDecision{Reason: reason})
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
		"subject":        request.Subject,
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
}

func NewConfigHandler(tendrilDir string) *ConfigHandler {
	return &ConfigHandler{TendrilDir: tendrilDir}
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

// UploadTrigger handles POST /v1/config/triggers
func (h *ConfigHandler) UploadTrigger(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// 10 MB max memory for parsing multipart form
	if err := r.ParseMultipartForm(10 << 20); err != nil {
		http.Error(w, "Failed to parse form", http.StatusBadRequest)
		return
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		http.Error(w, "Missing file part", http.StatusBadRequest)
		return
	}
	defer file.Close()

	if !validConfigFileName(header.Filename) {
		http.Error(w, "Invalid filename: must not be empty or contain path separators or traversal components", http.StatusBadRequest)
		return
	}

	triggersDir := filepath.Join(h.TendrilDir, "transduction", "hormonal-triggers")
	os.MkdirAll(triggersDir, 0755)

	targetPath := filepath.Join(triggersDir, header.Filename)
	out, err := os.OpenFile(targetPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0755) // Ensure executable
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to create file: %v", err), http.StatusInternalServerError)
		return
	}
	defer out.Close()

	if _, err := io.Copy(out, file); err != nil {
		http.Error(w, fmt.Sprintf("Failed to save file: %v", err), http.StatusInternalServerError)
		return
	}

	log.Printf("Uploaded new Hormonal Trigger: %s", header.Filename)
	w.WriteHeader(http.StatusCreated)
	w.Write([]byte("Trigger uploaded successfully.\n"))
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

// UploadGenotype handles POST /v1/config/genotypes
func (h *ConfigHandler) UploadGenotype(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var payload map[string]interface{}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		http.Error(w, "Invalid JSON body", http.StatusBadRequest)
		return
	}

	nameObj, ok := payload["name"]
	if !ok {
		http.Error(w, "Missing 'name' field", http.StatusBadRequest)
		return
	}
	name, ok := nameObj.(string)
	if !ok || name == "" {
		http.Error(w, "Invalid 'name' field", http.StatusBadRequest)
		return
	}
	// The name becomes the on-disk filename: reject path separators and
	// traversal components so a request can never write outside the
	// genotypes directory (the same boundary the MCP createGenotype tool
	// enforces).
	if !validConfigFileName(name) {
		http.Error(w, "Invalid 'name' field: must not contain path separators or traversal components", http.StatusBadRequest)
		return
	}

	genotypesDir := filepath.Join(h.TendrilDir, "genotypes")
	os.MkdirAll(genotypesDir, 0755)

	root, err := os.OpenRoot(genotypesDir)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to open config directory: %v", err), http.StatusInternalServerError)
		return
	}
	defer root.Close()
	out, err := root.OpenFile(name+".json", os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to create file: %v", err), http.StatusInternalServerError)
		return
	}
	defer out.Close()

	if err := json.NewEncoder(out).Encode(payload); err != nil {
		http.Error(w, fmt.Sprintf("Failed to write config: %v", err), http.StatusInternalServerError)
		return
	}

	log.Printf("Uploaded new AI Genotype: %s", name)
	if err := syncGenotypeIndex(); err != nil {
		log.Printf("Failed to sync genotype index after upload: %v", err)
	}
	w.WriteHeader(http.StatusCreated)
	w.Write([]byte("Genotype saved successfully.\n"))
}

// SetupRoutes registers the configuration endpoints
func (h *ConfigHandler) SetupRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/v1/config/triggers", func(w http.ResponseWriter, r *http.Request) {
		AuthMiddleware(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodGet {
				h.ListTriggers(w, r)
			} else if r.Method == http.MethodPost {
				h.UploadTrigger(w, r)
			} else {
				http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			}
		})(w, r)
	})

	mux.HandleFunc("/v1/config/genotypes", func(w http.ResponseWriter, r *http.Request) {
		AuthMiddleware(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodGet {
				h.ListGenotypes(w, r)
			} else if r.Method == http.MethodPost {
				h.UploadGenotype(w, r)
			} else {
				http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			}
		})(w, r)
	})
}
