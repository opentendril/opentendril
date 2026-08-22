package main

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"errors"
	"io/fs"
	"net"

	"github.com/opentendril/opentendril/cmd/stem/internal/conductor"
	"github.com/opentendril/opentendril/cmd/stem/internal/core"
	"github.com/opentendril/opentendril/cmd/stem/internal/eventbus"
	"github.com/opentendril/opentendril/cmd/stem/internal/gateway"
	"github.com/opentendril/opentendril/cmd/stem/internal/healthmon"
	"github.com/opentendril/opentendril/cmd/stem/internal/historydb"
	"github.com/opentendril/opentendril/cmd/stem/internal/mesh"
	"github.com/opentendril/opentendril/cmd/stem/internal/receptors"
	"github.com/opentendril/opentendril/cmd/stem/internal/scheduler"
	"github.com/opentendril/opentendril/cmd/stem/internal/session"
	"github.com/opentendril/opentendril/cmd/stem/internal/telemetry"
	"github.com/opentendril/opentendril/cmd/stem/internal/terrarium"
	"github.com/opentendril/opentendril/cmd/stem/internal/triggers"
)

type ChatCompletionRequest struct {
	Model     string       `json:"model"`
	SessionID string       `json:"sessionId,omitempty"`
	Messages  []APIMessage `json:"messages"`
}

type APIMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type ChatCompletionResponse struct {
	ID        string   `json:"id"`
	Object    string   `json:"object"`
	Created   int64    `json:"created"`
	Model     string   `json:"model"`
	SessionID string   `json:"sessionId,omitempty"`
	Choices   []Choice `json:"choices"`
}

type Choice struct {
	Index        int        `json:"index"`
	Message      APIMessage `json:"message"`
	FinishReason string     `json:"finishReason"`
}

func runServeCmd(ctx context.Context, args []string) {
	if len(args) >= 2 && strings.EqualFold(strings.TrimSpace(args[0]), "mcp") && strings.EqualFold(strings.TrimSpace(args[1]), "stdio") {
		runMCPCmd(ctx, args[2:])
		return
	}
	if len(args) > 0 {
		fmt.Fprintf(os.Stderr, "Unknown serve command: %s\n", strings.Join(args, " "))
		fmt.Fprintln(os.Stderr, "Usage: tendril serve")
		fmt.Fprintln(os.Stderr, "       tendril serve mcp stdio")
		os.Exit(1)
	}

	tendrilDir := "./.tendril"

	// Ensure hormonal triggers directory exists (Slice 1 requirement)
	triggersDir := getTriggersDir()
	if err := os.MkdirAll(triggersDir, 0o755); err != nil {
		log.Printf("⚠️ Could not create triggers directory: %v", err)
	}

	// The Stem must never serve its API unauthenticated (finding
	// 1): an explicit key wins, otherwise a previously generated key is
	// reused, otherwise a new one is generated and persisted so the
	// zero-config CLI/dashboard flow keeps working without a fail-open gap.
	if err := recordStemIdentity(tendrilDir); err != nil {
		log.Printf("⚠️ Could not record this Stem's executable identity: %v", err)
	}

	apiKey, generatedKey, err := getOrCreateAPIKey(tendrilDir)
	if err != nil {
		log.Fatalf("⚠️ Failed to establish an API key: %v", err)
	}
	if generatedKey {
		log.Printf("🔑 Generated API key (saved to %s)", apiKeyFilePath(tendrilDir))
		log.Printf("   Set %s to use your own instead.", EnvBotanistKey)
	}

	bus := eventbus.New()

	if triggersModeFromEnv() == triggers.ModeDisabled {
		log.Printf("⚠️ Hormonal Trigger gate is DISABLED (TENDRIL_TRIGGERS_MODE=disabled) — runs are not screened before growing")
	}

	repoRoot := resolveRepoRoot("")

	// Persistent state layer (.tendril/history.db). TENDRIL_DB_LOGGING=false
	// bypasses SQLite entirely for high-performance headless runs.
	history, err := historydb.OpenFromEnv(ctx, repoRoot)
	if err != nil {
		log.Printf("⚠️ Failed to open history database: %v (continuing without persistence)", err)
		history = nil
	}
	if history != nil {
		bus.AttachSink(history, 0, "historydb")
		log.Printf("Persistent state enabled at %s", history.Path())
	} else {
		log.Println("Persistent state disabled (TENDRIL_DB_LOGGING=false or open failure)")
	}

	// Unified SessionManager: every interface surface (CLI, MCP, REST, WS)
	// resolves its Tendril sessions through this single manager.
	var sessionStore session.Store
	if history != nil {
		sessionStore = history
	}
	sessions, err := session.NewManager(ctx, sessionStore)
	if err != nil {
		log.Printf("⚠️ Failed to resume persisted sessions: %v (starting empty)", err)
		sessions, _ = session.NewManager(ctx, nil)
	}

	// Remote EventBus sinks (Redis / remote WebSockets / webhooks) from env.
	remoteSinks, sinkErrs := telemetry.TransportersFromEnv()
	for _, sinkErr := range sinkErrs {
		log.Printf("⚠️ Skipping malformed remote sink: %v", sinkErr)
	}
	for _, transporter := range remoteSinks {
		telemetry.AttachTransporter(bus, transporter)
	}
	if len(remoteSinks) > 0 {
		log.Printf("%d remote telemetry sink(s) attached from %s", len(remoteSinks), telemetry.EnvRemoteSinks)
	}

	telemetryPath := filepath.Join(tendrilDir, "telemetry.yaml")
	if cfg, err := telemetry.LoadConfig(telemetryPath); err != nil {
		// errors.Is rather than os.IsNotExist: the loader wraps, and
		// os.IsNotExist does not unwrap, so an absent optional file warned.
		if !errors.Is(err, fs.ErrNotExist) {
			log.Printf("⚠️ Failed to load telemetry config: %v", err)
		}
	} else if cfg.Enabled {
		if cfg.Resin.Enabled {
			if _, err := telemetry.InitResinSink(bus, cfg.Resin, filepath.Join(tendrilDir, "logs", "resin.log")); err != nil {
				log.Printf("⚠️ Failed to initialize Resin sink: %v", err)
			} else {
				log.Println("Resin telemetry sink enabled")
			}
		}
		for _, transporterCfg := range cfg.Transporters {
			transporter, err := telemetry.NewTransporter(transporterCfg)
			if err != nil {
				log.Printf("⚠️ Failed to create transporter %q: %v", transporterCfg.Type, err)
				continue
			}
			telemetry.AttachTransporter(bus, transporter)
			log.Printf("Transporter %q attached to event bus", transporterCfg.Type)
		}
	}

	// Delegated-execution control plane: capability grants live in
	// the Stem's own .tendril/grants.yaml — never inside a Substrate checkout,
	// so repository content can never widen capability. With zero grants (the
	// default) every delegated invocation is denied and all non-delegated
	// behavior is untouched; a malformed grants file degrades the same way,
	// never open.
	delegationGrants, grantsErr := core.LoadDelegationGrants(tendrilDir)
	if grantsErr != nil {
		log.Printf("⚠️ Failed to load delegation grants: %v (delegation disabled — every delegated invocation is denied)", grantsErr)
		delegationGrants = nil
	}
	// Issued credentials are what let a caller PROVE a Pollen rather than
	// declare one. A malformed store is fatal rather than empty: degrading to
	// "no credentials" would silently return every caller to the declared-Pollen
	// path, which is the weaker tier.
	pollinatorCredentials, credentialsErr := core.LoadPollinatorCredentials(tendrilDir)
	if credentialsErr != nil {
		log.Fatalf("❌ Pollinator credentials could not be read: %v", credentialsErr)
	}
	// The Stem's own signing key: what mints and verifies short-lived access
	// tokens. A presented token is proven by this key's signature, not a store
	// lookup, so verification needs no shared state. A read failure is fatal
	// rather than degraded — a surface that cannot verify a token must not run as
	// though none was presented.
	stemSigner, signerErr := core.LoadOrCreateStemSigner(tendrilDir)
	if signerErr != nil {
		log.Fatalf("❌ Stem signing key could not be read: %v", signerErr)
	}
	pendingStore := core.NewPendingConfirmationStore()
	delegationGate := &receptors.DelegationGate{
		Pollinators: pollinatorCredentials,
		Signer:      stemSigner,
		Authorizer:  core.NewDelegationAuthorizer(delegationGrants).WithPendingStore(pendingStore, time.Hour),
		Bus:         bus,
	}
	if len(pollinatorCredentials) > 0 {
		active := 0
		for _, credential := range pollinatorCredentials {
			if credential.Active() {
				active++
			}
		}
		fmt.Fprintf(os.Stderr, "🔏 %d Pollinator credential(s) loaded (%d active): a presented credential DERIVES its Pollen; the header claim is ignored for those callers\n", len(pollinatorCredentials), active)
	}
	if len(delegationGrants) > 0 {
		log.Printf("Delegation enabled: %d grant(s) loaded from %s", len(delegationGrants), filepath.Join(tendrilDir, core.DelegationGrantsFilename))
	}

	// Bind posture: loopback by default. An off-host bind is self-declaring —
	// no opt-in flag — and is the signal that engages root-credential hardening
	// on data routes (access tokens required; roots still accepted at mint).
	listenHost := serveListenHost()
	networked := isNetworkedBindHost(listenHost)

	if substratesConfig, err := conductor.LoadSubstratesConfig(""); err != nil {
		log.Printf("⚠️ Failed to load substrates config for startup materialization: %v", err)
	} else {
		conductor.MaterializeManagedCheckouts(ctx, substratesConfig)
	}

	// Daemon-lifetime health monitor: constructed before its /health
	// registration so the same instance serves both the interval loop and the
	// on-demand endpoint. This assignment must precede the scheduler block
	// below because Start is called there alongside scheduler.New(...).Start.
	healthMonitor := newDefaultHealthMonitor(bus, 30*time.Second)

	// Unified Interface Layer: the transport-free Core owns the session-
	// lifecycle, genome, plasmid, substrate-grafting, mesh trait governance,
	// sequence, and sprout/run capabilities; the REST, MCP, and CLI surfaces
	// are adapters that route through this one service.
	coreSvc := core.NewService(sessions).
		WithTendrilDir(tendrilDir).
		WithGenome(genomeOperations(resolveRepoRoot(""))).
		WithPlasmid(plasmidOperations(resolveRepoRoot(""))).
		WithMesh(meshOperations()).
		WithSequence(serveSequenceOperations(resolveRepoRoot(""), bus)).
		WithSprout(sproutOperations(history, bus)).
		WithStoma(stomaOperations()).
		WithSeed(seedOperations()).
		WithGit(gitOperations())

	meshServer := mesh.NewServer(resolveRepoRoot(""))

	// Mesh admin uses the same Botanist-key chain as the Stem bearer; never
	// fail open when no dedicated secret is configured.
	adminKey := resolveServeAPIKey()
	if adminKey == "" {
		adminKey = apiKey
	}

	deps := serveDependencies{
		APIKey:                apiKey,
		PollinatorCredentials: pollinatorCredentials,
		StemSigner:            stemSigner,
		Networked:             networked,
		DelegationGate:        delegationGate,
		EventBus:              bus,
		Sessions:              sessions,
		History:               history,
		CoreService:           coreSvc,
		HealthMonitor:         healthMonitor,
		TendrilDir:            tendrilDir,
		MeshServer:            meshServer,
		PendingStore:          pendingStore,
		AdminKey:              adminKey,
	}
	mux := buildServeMux(deps)

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	apiAddr := net.JoinHostPort(listenHost, port)

	server := &http.Server{
		Addr:    apiAddr,
		Handler: mux,
	}

	// Optional local transport: the same authenticated mux, not a second
	// authority. Failure here must not take the TCP Stem down.
	localSock := startOptionalLocalSocket(mux, os.Getenv(EnvLocalSocket))

	go func() {
		<-ctx.Done()
		log.Println("Shutting down API server...")
		server.Shutdown(context.Background())
		if err := localSock.Close(); err != nil {
			log.Printf("⚠️ Failed to close local Stem socket: %v", err)
		}
		// Drain telemetry sinks, then release the history database.
		bus.Shutdown()
		if history != nil {
			if err := history.Close(); err != nil {
				log.Printf("⚠️ Failed to close history database: %v", err)
			}
		}
	}()

	// Start the standalone Gateway server. The same /ws surface is also
	// mounted on the main API mux, so a bind failure here (e.g. another Stem
	// already owns the port) degrades gracefully instead of killing the API.
	gatewayPort := os.Getenv("GATEWAY_PORT")
	if gatewayPort == "" {
		gatewayPort = "9090"
	}
	gatewayAddr := net.JoinHostPort(listenHost, gatewayPort)
	go func() {
		gatewayMux := http.NewServeMux()
		gatewayMux.HandleFunc("/ws", withWebSocketAuth(apiKey, pollinatorCredentials, stemSigner, networked,
			receptors.NewWatchAuthority(delegationGate, history).StreamMiddleware(gateway.HandleWebSocket(bus))))
		gatewayServer := &http.Server{
			Addr:    gatewayAddr,
			Handler: gatewayMux,
		}
		log.Printf("Starting Gateway WebSocket server on %s...", gatewayAddr)
		if err := gatewayServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Printf("⚠️ Gateway WebSocket server unavailable on %s: %v (main /ws endpoint still serves)", gatewayAddr, err)
		}
	}()

	if networked {
		log.Printf("Starting Go Stem API on %s (off-host: data routes require access tokens; durable roots only at mint)...", apiAddr)
	} else {
		log.Printf("Starting Go Stem API on %s (loopback: durable Pollinator credentials still accepted on data routes)...", apiAddr)
	}
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("Server failed: %v", err)
	}
}

// serveListenHost resolves the Terroir bind host from TERROIR_HOST, defaulting
// to loopback so a bare start never exposes the Stem beyond the local machine.
// Values must be a host/IP only (no port). If a caller accidentally includes a
// port, it is stripped so net.JoinHostPort does not produce a double-port address.
func serveListenHost() string {
	host := strings.TrimSpace(os.Getenv(EnvTerroirHost))
	if host == "" {
		return "127.0.0.1"
	}
	if h, _, err := net.SplitHostPort(host); err == nil {
		return h
	}
	return host
}

// isLoopbackBindHost reports whether host is a loopback bind target. Empty,
// 127.0.0.1, ::1, and localhost are loopback; 0.0.0.0, ::, and any other
// hostname or address are networked (off-host exposure is self-declaring).
func isLoopbackBindHost(host string) bool {
	host = strings.TrimSpace(host)
	if host == "" {
		return true
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	// net.ParseIP rejects bracketed IPv6; strip a single pair if present.
	if strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]") {
		host = host[1 : len(host)-1]
	}
	ip := net.ParseIP(host)
	if ip != nil {
		return ip.IsLoopback()
	}
	return false
}

// isNetworkedBindHost is the inverse of isLoopbackBindHost: true when the bind
// exposes the Stem beyond loopback and therefore engages off-host hardening.
func isNetworkedBindHost(host string) bool {
	return !isLoopbackBindHost(host)
}

// scheduledRunFirer is the concrete firing seam the scheduler grows entries
// through. Each fire, in order: (1) the run passes Hormonal Triggers exactly
// like the chat path — a blocked run never grows; (2) the entry grows through
// the same governed Core capability (sequence.grow / sprout.grow) as every
// other surface, so the run's lifecycle telemetry flows to the Command Center
// via the EventBus-threaded sequence port.
func scheduledRunFirer(coreSvc core.Core, sessions *session.Manager, triggersDir string, bus *eventbus.Bus) scheduler.FirerFunc {
	// scheduledOrigin records which surface grew the run, alongside the
	// session package's cli/mcp/rest/ws origins.
	const scheduledOrigin = "scheduler"

	firstNonEmpty := func(values ...string) string {
		for _, v := range values {
			if strings.TrimSpace(v) != "" {
				return v
			}
		}
		return ""
	}

	return func(ctx context.Context, name string, e scheduler.Entry) error {
		// Hormonal Triggers gate every scheduled run pre-fire (mirroring the
		// chat path in handleChatCompletions).
		payload := triggers.TriggerPayload{Genotype: e.Model, Transcript: e.Sequence}
		if e.Sprout != nil {
			payload.Genotype = firstNonEmpty(e.Sprout.Genotype, e.Sprout.Model, e.Model)
			payload.Transcript = e.Sprout.Transcript
		}
		mode, runner := resolveTriggerModeAndRunner(bus)
		if err := triggers.EvaluateTriggers(ctx, mode, runner, triggersDir, payload); err != nil {
			log.Printf("🚫 Schedule %q: scheduled run blocked by Hormonal Triggers: %v", name, err)
			bus.Publish(eventbus.Event{
				Type:   eventbus.EventTriggerBlocked,
				Source: "scheduler",
				Data: map[string]interface{}{
					"schedule": name,
					"genotype": payload.Genotype,
					"reason":   err.Error(),
				},
			})
			return fmt.Errorf("blocked by Hormonal Triggers: %w", err)
		}

		if e.Sequence != "" {
			// Sequence runs (scheduled or manual) have no per-run history row;
			// they are observed via the bus's sequence-complete/-failure events.
			log.Printf("⏰ Schedule %q: growing Sequence %q", name, e.Sequence)
			result, err := coreSvc.SequenceRun(ctx, core.SequenceRunInput{
				PathOrName: e.Sequence,
				Provider:   e.Provider,
				Model:      e.Model,
			})
			if err != nil {
				log.Printf("❌ Schedule %q: Sequence %q withered: %v", name, e.Sequence, err)
				return err
			}
			log.Printf("✅ Schedule %q: Sequence %q finished (%d step(s))", name, firstNonEmpty(result.Name, e.Sequence), len(result.Steps))
			return nil
		}

		// Sprout entry (LoadConfig guarantees exactly one of sequence/sprout).
		// Entry-level provider/model/genotype overrides ride a dedicated
		// session's preferences: the governed sprout.grow capability shapes
		// each run from the preferences of the session it is bound to.
		input := map[string]any{
			"transcript": e.Sprout.Transcript,
			"substrate":  e.Sprout.Substrate,
			"origin":     scheduledOrigin,
		}
		prefs := session.Preferences{
			Provider: firstNonEmpty(e.Sprout.Provider, e.Provider),
			Model:    firstNonEmpty(e.Sprout.Model, e.Model),
			Genotype: e.Sprout.Genotype,
		}
		if sess, err := sessions.Initiate(ctx, scheduledOrigin, prefs); err != nil {
			log.Printf("⚠️ Schedule %q: failed to initiate a session for the run (growing sessionless): %v", name, err)
		} else {
			input["sessionId"] = sess.ID
		}

		log.Printf("⏰ Schedule %q: growing a Sprout: %s", name, e.Sprout.Transcript)
		result, err := coreSvc.Invoke(ctx, core.CapSproutGrow, input)
		if err != nil {
			log.Printf("❌ Schedule %q: Sprout withered: %v", name, err)
			return err
		}
		runResult, _ := result.(core.SproutRunResult)
		log.Printf("✅ Schedule %q: Sprout %s matured (session %s)", name, runResult.StepID, runResult.SessionID)
		return nil
	}
}

// EnvBotanistKey is the single authoritative Stem bearer secret — the Botanist's
// own key. It must stay distinct from any inference provider's key: a provider
// key may be shared and is passed into every Terrarium; this key grants unscoped
// access and must never enter one. One name only — no alternate env aliases.
const EnvBotanistKey = "BOTANIST_KEY"

// EnvTerroirHost names the bind address for the Stem's network habitat. One
// name only; unset means loopback (127.0.0.1). Off-host classification uses
// this TCP bind only; an optional local Unix socket does not change it.
const EnvTerroirHost = "TERROIR_HOST"

// resolveServeAPIKey returns the Stem bearer from EnvBotanistKey, or "" when unset.
func resolveServeAPIKey() string {
	return strings.TrimSpace(os.Getenv(EnvBotanistKey))
}

// apiKeyFilePath is where getOrCreateAPIKey persists a generated bearer key,
// mirroring the heartwood.ResolveKey pattern used for the rhizome encryption
// key: environment override wins, else a key persisted on disk, else generate.
func apiKeyFilePath(tendrilDir string) string {
	return filepath.Join(tendrilDir, "api-key")
}

func readPersistedAPIKey(tendrilDir string) string {
	content, err := os.ReadFile(apiKeyFilePath(tendrilDir))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(content))
}

// getOrCreateAPIKey resolves the Stem's bearer key: EnvBotanistKey wins, then a
// key already on disk, then a freshly generated one persisted for next time. It
// never returns an empty key, so the Stem cannot come up serving its API
// unauthenticated.
func getOrCreateAPIKey(tendrilDir string) (key string, generated bool, err error) {
	if key = resolveServeAPIKey(); key != "" {
		return key, false, nil
	}
	if key = readPersistedAPIKey(tendrilDir); key != "" {
		return key, false, nil
	}

	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", false, fmt.Errorf("generate API key: %w", err)
	}
	key = hex.EncodeToString(buf)

	if err := os.MkdirAll(tendrilDir, 0o755); err != nil {
		return "", false, fmt.Errorf("create %s: %w", tendrilDir, err)
	}
	if err := os.WriteFile(apiKeyFilePath(tendrilDir), []byte(key+"\n"), 0o600); err != nil {
		return "", false, fmt.Errorf("persist generated API key: %w", err)
	}
	return key, true, nil
}

// bearerMatches compares an Authorization header against the configured key
// in constant time.
func bearerMatches(header, apiKey string) bool {
	want := "Bearer " + apiKey
	got := strings.TrimSpace(header)
	return subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}

func withAPIKeyAuth(apiKey string, next http.HandlerFunc) http.HandlerFunc {
	// API-key-only paths never accept a Pollinator root, so the bind posture
	// does not change their behaviour; networked=false is the no-op default.
	return withAPIKeyOrPollinatorAuth(apiKey, nil, nil, false, next)
}

// registerBotanistRoute mounts a management route. The lane is not a parameter:
// a route mounted here is on the Botanist lane and cannot be on any other.
func registerBotanistRoute(mux *http.ServeMux, deps serveDependencies, pattern string, handler http.HandlerFunc) {
	mux.HandleFunc(pattern, withAPIKeyAuth(deps.AdminKey, deps.DelegationGate.Middleware(handler)))
}

// withAPIKeyOrPollinatorAuth authenticates a caller as EITHER the Botanist
// (the Stem's own bearer key) or a Pollinator (an issued credential or a
// short-lived access token).
//
// A Pollinator credential has to authenticate the transport as well as carry
// the identity, otherwise a Pollinator would still need the Botanist's key to
// get through the door — and a Pollinator holding that key could reach every
// route, including those with no delegable operation-class. One credential,
// one identity, one level of access.
//
// networked is true when the Stem is bound beyond loopback. In that posture a
// durable Pollinator credential is refused on every data route (mint a token
// instead); access tokens and the Botanist api-key path are unchanged. On
// loopback, root credentials still work exactly as before.
//
// The credential or token is only accepted here; what it may then DO is decided
// by the grant model downstream, which derives the Pollen from this same bearer.
func withAPIKeyOrPollinatorAuth(apiKey string, credentials receptors.PollinatorCredentials, verifier receptors.AccessTokenVerifier, networked bool, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if reason, ok := resolveAuthorization(r.Header.Get("Authorization"), apiKey, credentials, verifier, networked); !ok {
			http.Error(w, reason, http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}

// resolveAuthorization decides whether one presented Authorization header
// authenticates, as the Botanist or as a Pollinator. It is the single answer to
// "who may come in", shared by every surface, so no surface can develop its own
// opinion — which is exactly how the event stream came to accept only the
// Botanist while the data routes accepted issued credentials.
//
// It reports only whether the caller is admitted. WHICH Pollen it is, and what
// that Pollen may then do, is decided downstream from the same header.
func resolveAuthorization(authorization, apiKey string, credentials receptors.PollinatorCredentials, verifier receptors.AccessTokenVerifier, networked bool) (reason string, ok bool) {
	presented := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(authorization), "Bearer "))

	if core.LooksLikeAccessToken(presented) {
		// A token-shaped bearer is verified or refused. Like a credential it
		// never falls back to the Botanist key comparison, so an expired or
		// forged token cannot be retried as anything else. A nil verifier
		// proves nothing and denies (deny-closed).
		if verifier == nil {
			return "Unauthorized", false
		}
		if _, verified := verifier.VerifyAccessToken(presented); !verified {
			return "Unauthorized", false
		}
		return "", true
	}

	if core.LooksLikePollinatorCredential(presented) {
		// Off-host: the durable root is the refresh secret only. Present it
		// at POST /v1/pollinator/token; data routes accept the short-lived
		// access token it mints. Loopback keeps the prior root-on-data-routes
		// behaviour so personal local setups are unchanged.
		if networked {
			return "Unauthorized: durable Pollinator credentials are not accepted on off-host binds; mint an access token via POST /v1/pollinator/token", false
		}
		// A credential-shaped bearer is resolved or refused. It never falls
		// back to the Botanist key comparison, so a revoked credential
		// cannot be retried as anything else.
		if core.ResolvePollenFromCredential(credentials, presented) == "" {
			return "Unauthorized", false
		}
		return "", true
	}

	// An empty apiKey is a caller bug, not an invitation to skip auth:
	// fail closed rather than repeat finding 1.
	if strings.TrimSpace(apiKey) == "" || !bearerMatches(authorization, apiKey) {
		return "Unauthorized", false
	}
	return "", true
}

// withWebSocketAuth gates a WebSocket upgrade handler with the same resolver
// the data routes use, so an issued credential opens a stream exactly as it
// opens a read. Browsers cannot attach custom headers to the native WebSocket
// handshake, so a `key` query parameter is accepted alongside the
// Authorization header used by non-browser clients (e.g. the CLI's
// gorilla/websocket dialer).
//
// A bearer that arrived in the query parameter is moved into the Authorization
// header before the request continues. Everything downstream derives the
// caller's identity from that header alone, so leaving a credential in the
// query string would let it authenticate the connection and then be invisible
// to the gate that decides what the connection may see — authenticated as a
// Pollinator, treated as the operator.
func withWebSocketAuth(apiKey string, credentials receptors.PollinatorCredentials, verifier receptors.AccessTokenVerifier, networked bool, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		reason, ok := resolveAuthorization(r.Header.Get("Authorization"), apiKey, credentials, verifier, networked)
		if ok {
			next(w, r)
			return
		}

		if key := strings.TrimSpace(r.URL.Query().Get("key")); key != "" {
			authorization := "Bearer " + key
			if queryReason, queryOK := resolveAuthorization(authorization, apiKey, credentials, verifier, networked); queryOK {
				r = r.Clone(r.Context())
				r.Header.Set("Authorization", authorization)
				next(w, r)
				return
			} else if strings.TrimSpace(r.Header.Get("Authorization")) == "" {
				reason = queryReason
			}
		}

		http.Error(w, reason, http.StatusUnauthorized)
	}
}

func handleHealth(monitor *healthmon.Monitor, networked bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		report := monitor.RunOnceAndPublish(r.Context())

		// Compose the response rather than widening healthmon.HealthReport, which
		// is purely for liveness checks. The numeric owner is only disclosed on
		// loopback binds, where it is not new information to the caller.
		type extendedReport struct {
			healthmon.HealthReport
			Owner *int `json:"owner,omitempty"`
		}
		resp := extendedReport{HealthReport: report}
		if !networked {
			uid := os.Getuid()
			resp.Owner = &uid
		}

		w.Header().Set("Content-Type", "application/json")
		if !report.Overall {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
		json.NewEncoder(w).Encode(resp)
	}
}

func handleChatCompletions(bus *eventbus.Bus, sessions *session.Manager, history *historydb.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req ChatCompletionRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		if len(req.Messages) == 0 {
			http.Error(w, "no messages provided", http.StatusBadRequest)
			return
		}

		// Bind this interaction to a Phytomer. Clients pass sessionId in the
		// body or the canonical X-Phytomer header (the legacy X-Tendril-Session
		// header is still honored); absent all, a fresh Phytomer is initiated so
		// every run is still traceable.
		requestedSessionID := strings.TrimSpace(req.SessionID)
		if requestedSessionID == "" {
			requestedSessionID = strings.TrimSpace(r.Header.Get("X-Phytomer"))
		}
		if requestedSessionID == "" {
			requestedSessionID = strings.TrimSpace(r.Header.Get("X-Tendril-Session"))
		}
		sess, err := sessions.GetOrInitiate(r.Context(), requestedSessionID, session.OriginREST)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		// Session preferences fill in anything the request leaves unset.
		model := strings.TrimSpace(req.Model)
		if model == "" {
			model = sess.Preferences.Model
		}

		taskPrompt := req.Messages[len(req.Messages)-1].Content
		runStarted := time.Now().UTC()
		runStamp := runStarted.UnixNano()
		chatID := fmt.Sprintf("chat-%d", runStamp)
		stepID := fmt.Sprintf("step-%d", runStamp)
		completionID := fmt.Sprintf("chatcmpl-%d", runStamp)

		// Phase 3 Part 2: Hormonal Triggers (Pre-execution Security)
		payload := triggers.TriggerPayload{
			Genotype:   model,
			Transcript: taskPrompt,
		}

		triggersDir := getTriggersDir()
		mode, runner := resolveTriggerModeAndRunner(bus)
		if err := triggers.EvaluateTriggers(r.Context(), mode, runner, triggersDir, payload); err != nil {
			log.Printf("Sprout blocked by Hormonal Triggers: %v", err)
			bus.Publish(eventbus.Event{
				Type:      eventbus.EventTriggerBlocked,
				Source:    "chat",
				SessionID: sess.ID,
				Data: map[string]interface{}{
					"genotype": payload.Genotype,
					"reason":   err.Error(),
				},
			})
			http.Error(w, err.Error(), http.StatusForbidden)
			return
		}

		if recordErr := sessions.RecordMessage(r.Context(), session.Message{
			SessionID: sess.ID,
			Role:      "user",
			Content:   taskPrompt,
			Model:     model,
			CreatedAt: runStarted,
		}); recordErr != nil {
			log.Printf("⚠️ Failed to record user message: %v", recordErr)
		}

		sproutRun := historydb.SproutRun{
			RunID:      stepID,
			SessionID:  sess.ID,
			StepID:     stepID,
			Origin:     sess.Origin,
			Model:      model,
			Genotype:   sess.Preferences.Genotype,
			Transcript: taskPrompt,
			Status:     "running",
			StartedAt:  runStarted,
		}
		if history != nil {
			if recordErr := history.RecordSproutRun(r.Context(), sproutRun); recordErr != nil {
				log.Printf("⚠️ Failed to record sprout run start: %v", recordErr)
			}
		}

		// Emit stream start event
		bus.Publish(eventbus.Event{
			Type:      eventbus.EventStreamToken,
			Source:    stepID,
			SessionID: sess.ID,
			Data:      map[string]interface{}{"type": "stream.start"},
		})

		log.Printf("Sprouting Tendril for task: %s", taskPrompt)
		log.Printf("Chat run %s mapped to step %s (session %s)", chatID, stepID, sess.ID)

		var output string

		// Route to external Docker Tendril
		orch := conductor.NewDockerOrchestrator()
		orch.StepID = stepID
		orch.EventBus = bus
		orch.SessionID = sess.ID
		applySessionPreferences(orch, sess.Preferences)
		if history != nil {
			installSproutTerminalHistory(orch, history, context.WithoutCancel(r.Context()), sproutRun)
		}
		var sproutReport conductor.SproutRunReport
		sproutReport, err = orch.RunSprout(r.Context(), taskPrompt)
		output = sproutReport.Output

		// Emit stream end event
		bus.Publish(eventbus.Event{
			Type:      eventbus.EventStreamToken,
			Source:    stepID,
			SessionID: sess.ID,
			Data:      map[string]interface{}{"type": "stream.end"},
		})

		if err != nil {
			log.Printf("Tendril execution failed: %v", err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		if recordErr := sessions.RecordMessage(r.Context(), session.Message{
			SessionID: sess.ID,
			Role:      "assistant",
			Content:   output,
			Model:     model,
		}); recordErr != nil {
			log.Printf("⚠️ Failed to record assistant message: %v", recordErr)
		}

		// Format response as OpenAI completion
		resp := ChatCompletionResponse{
			ID:        completionID,
			Object:    "chat.completion",
			Created:   runStarted.Unix(),
			Model:     model,
			SessionID: sess.ID,
			Choices: []Choice{
				{
					Index: 0,
					Message: APIMessage{
						Role:    "assistant",
						Content: string(output),
					},
					FinishReason: "stop",
				},
			},
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}
}

// applySessionPreferences copies Phytomer overrides onto the orchestrator the
// chat grow path uses. Substrate is first-class: an empty preference stays
// empty so the conductor can apply the unique-named-Substrate fallback or the
// Stem-home refusal. Chat must not invent the Stem working directory here.
func applySessionPreferences(orch *conductor.DockerOrchestrator, prefs session.Preferences) {
	if orch == nil {
		return
	}
	orch.Provider = prefs.Provider
	orch.Model = prefs.Model
	orch.Genotype = prefs.Genotype
	orch.Substrate = strings.TrimSpace(prefs.Substrate)
}

const triggerExecTimeout = 30 * time.Second

func getTriggersDir() string {
	return filepath.Join(".", ".tendril", "transduction", "hormonal-triggers")
}

// terrariumRunner executes triggers inside an isolated Terrarium.
// Note: Hormonal triggers run in an isolated alpine:3.20 Terrarium.
// The script must be an executable POSIX sh script; #!/bin/sh is the only available shell.
// Full operator documentation in docs/DESIGN-SECURITY.md.
type terrariumRunner struct {
	providerName string
	bus          *eventbus.Bus
}

func (r terrariumRunner) RunTrigger(ctx context.Context, scriptPath string, payload triggers.TriggerPayload) error {
	obs := terrarium.ActivationObserver(func(name string) {
		r.bus.Publish(eventbus.Event{
			Type:   eventbus.EventHostExecutionActivated,
			Source: "hormonal-trigger",
			Data: map[string]interface{}{
				"provider": name,
				"trigger":  scriptPath,
			},
		})
	})
	provider, err := terrarium.NewProvider(ctx, r.providerName, obs)
	if err != nil {
		return fmt.Errorf("Hormonal Trigger blocked: failed to resolve terrarium provider for isolated execution: %w", err)
	}

	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("Hormonal Trigger blocked: failed to serialize payload: %w", err)
	}

	spec := terrarium.TerrariumSpec{
		Image:       "alpine:3.20",
		WorkingDir:  "/app",
		NetworkMode: terrarium.NetworkModeNone,
		Timeout:     triggerExecTimeout,
		// Defense in depth
		RunAsUser:     "65534", // nobody
		PidsLimit:     128,
		MemoryLimitMB: 256,
		// Note: ReadOnlyRootFS is intentionally omitted because the payload is delivered
		// as a file payload under /tmp, which requires a writable rootfs in some providers.
		Mounts: []terrarium.MountSpec{
			{Source: filepath.Dir(scriptPath), Target: "/triggers", ReadOnly: true},
		},
		Files: []terrarium.FilePayload{
			{Path: "/tmp/payload.json", Content: payloadJSON, Mode: 0o444},
		},
		Command: []string{filepath.Join("/triggers", filepath.Base(scriptPath)), "/tmp/payload.json"},
	}

	instance, err := provider.Create(ctx, spec)
	if err != nil {
		return fmt.Errorf("Hormonal Trigger blocked: failed to create isolated runner terrarium: %w", err)
	}
	defer func() { _ = instance.Stop(context.Background()) }()

	result, runErr := instance.Run(ctx, terrarium.CommandSpec{
		Command:    spec.Command,
		WorkingDir: "/triggers",
	})
	if result.TimedOut || runErr != nil {
		if result.TimedOut {
			return fmt.Errorf("Hormonal Trigger blocked: script '%s' exceeded timeout of %v", filepath.Base(scriptPath), triggerExecTimeout)
		}
		return fmt.Errorf("Hormonal Trigger blocked: script '%s' failed to execute: %w (hormonal triggers run in an isolated alpine:3.20 Terrarium — the script must be an executable POSIX sh script; #!/bin/bash is not available)", filepath.Base(scriptPath), runErr)
	}

	if result.ExitCode != 0 {
		errMsg := strings.TrimSpace(result.Stderr)
		if errMsg == "" {
			errMsg = fmt.Sprintf("exit code %d", result.ExitCode)
		}
		return fmt.Errorf("Hormonal Trigger Blocked: script '%s' failed.\nReason: %s", filepath.Base(scriptPath), errMsg)
	}

	return nil
}

func triggersModeFromEnv() triggers.TriggerMode {
	modeStr := strings.ToLower(strings.TrimSpace(os.Getenv("TENDRIL_TRIGGERS_MODE")))
	if modeStr == string(triggers.ModeDisabled) {
		return triggers.ModeDisabled
	}
	return triggers.ModeEnforce
}

func resolveTriggerModeAndRunner(bus *eventbus.Bus) (triggers.TriggerMode, triggers.TriggerRunner) {

	providerName := ""
	if strings.EqualFold(strings.TrimSpace(os.Getenv("TENDRIL_ALLOW_HOST_EXECUTION")), "true") {
		providerName = terrarium.ProviderHost
	}

	return triggersModeFromEnv(), terrariumRunner{providerName: providerName, bus: bus}
}

type serveDependencies struct {
	APIKey                string
	PollinatorCredentials []core.PollinatorCredential
	StemSigner            *core.StemSigner
	Networked             bool
	DelegationGate        *receptors.DelegationGate
	EventBus              *eventbus.Bus
	Sessions              *session.Manager
	History               *historydb.Store
	CoreService           *core.Service
	HealthMonitor         *healthmon.Monitor
	TendrilDir            string
	MeshServer            *mesh.Server
	PendingStore          *core.PendingConfirmationStore
	AdminKey              string
}

func buildServeMux(deps serveDependencies) *http.ServeMux {
	mux := http.NewServeMux()

	// guardedAuth authenticates with the bearer key and default-denies
	// delegated-marked requests: in this slice only the sprout routes consult
	// the delegation authorizer per-invocation, so every other surface refuses
	// a delegated invocation rather than silently running it as plain traffic.
	guardedAuth := func(next http.HandlerFunc) http.HandlerFunc {
		return withAPIKeyOrPollinatorAuth(deps.APIKey, deps.PollinatorCredentials, deps.StemSigner, deps.Networked, deps.DelegationGate.Middleware(next))
	}

	// Access-token mint: a durable Pollinator credential (the refresh root) is
	// exchanged here for a short-lived signed token. Self-authenticating on the
	// presented credential, so it takes no outer bearer wrapper; a token cannot
	// mint another token, and a plain bearer key cannot mint for a named identity.
	receptors.NewPollinatorTokenHandler(deps.StemSigner, deps.PollinatorCredentials).Register(mux)

	// observeAuth authenticates without the blanket delegated-request denial:
	// the surfaces it carries reach a decision about the phytomer the caller
	// named, which is a decision guardedAuth's default cannot make because it
	// runs before any phytomer is in hand.
	observeAuth := func(next http.HandlerFunc) http.HandlerFunc {
		return withAPIKeyOrPollinatorAuth(deps.APIKey, deps.PollinatorCredentials, deps.StemSigner, deps.Networked, next)
	}

	// Who may observe a phytomer: one authority, shared by the stored views and
	// the live stream, so the two cannot answer the same question differently.
	watch := receptors.NewWatchAuthority(deps.DelegationGate, deps.History)

	mux.HandleFunc("/ws", withWebSocketAuth(deps.APIKey, deps.PollinatorCredentials, deps.StemSigner, deps.Networked,
		watch.StreamMiddleware(gateway.HandleWebSocket(deps.EventBus))))

	mux.HandleFunc("/v1/chat/completions", guardedAuth(handleChatCompletions(deps.EventBus, deps.Sessions, deps.History)))

	// Daemon-lifetime health monitor: constructed before its /health
	// registration so the same instance serves both the interval loop and the
	// on-demand endpoint.
	mux.HandleFunc("GET /health", deps.DelegationGate.Middleware(handleHealth(deps.HealthMonitor, deps.Networked)))

	// Tendril session REST API (adapter).
	sessionsHandler := receptors.NewSessionsHandler(deps.CoreService, deps.Sessions, deps.History, deps.EventBus).WithWatch(watch)
	sessionsHandler.Register(mux, guardedAuth, observeAuth)

	// Genome REST API (adapter, slice 1).
	genomeHandler := receptors.NewGenomeHandler(deps.CoreService)
	genomeHandler.Register(mux, guardedAuth)

	// Plasmid REST API (adapter, slice 2).
	plasmidHandler := receptors.NewPlasmidHandler(deps.CoreService)
	plasmidHandler.Register(mux, guardedAuth)

	// Substrate-grafting REST API (adapter, slice 3). Distinct from
	// the mesh *server* endpoints mounted below: these are the client-side
	// delegation commands.
	graftHandler := receptors.NewGraftHandler(deps.CoreService)
	graftHandler.Register(mux, guardedAuth)

	// Mesh trait governance REST API (adapter). These routes
	// expose the pending-trait inbox to the Command Center and CLI.
	traitHandler := receptors.NewTraitHandler(deps.CoreService)
	traitHandler.Register(mux, guardedAuth)

	// Sequence REST API (adapter, slice 4).
	sequenceHandler := receptors.NewSequenceHandler(deps.CoreService)
	sequenceHandler.Register(mux, guardedAuth)

	// Sprout REST API (adapter, final family). Detached
	// POST /v1/sessions/{sessionId}/sprout/grow is registered outside the
	// parity registry inside SproutHandler.Register. Both sprout routes
	// consult the delegation gate per-invocation (with the decoded substrate
	// in hand), so they take the bare bearer auth rather than guardedAuth's
	// blanket delegated-request denial.
	sproutHandler := receptors.NewSproutHandler(deps.CoreService, deps.History, deps.EventBus).WithDelegation(deps.DelegationGate)
	sproutHandler.Register(mux, func(next http.HandlerFunc) http.HandlerFunc {
		return withAPIKeyOrPollinatorAuth(deps.APIKey, deps.PollinatorCredentials, deps.StemSigner, deps.Networked, next)
	})

	// Stoma REST API (adapter): one bounded command in a
	// network-sealed terrarium, the minimal delegable operation-class. Like
	// the sprout routes it consults the delegation gate per-invocation (the
	// matching grant supplies the egress allow-list), so it takes the bare
	// bearer auth rather than guardedAuth's blanket delegated-request denial.
	stomaHandler := receptors.NewStomaHandler(deps.CoreService).WithDelegation(deps.DelegationGate)
	stomaHandler.Register(mux, func(next http.HandlerFunc) http.HandlerFunc {
		return withAPIKeyOrPollinatorAuth(deps.APIKey, deps.PollinatorCredentials, deps.StemSigner, deps.Networked, next)
	})

	// Git REST API (adapter): commit a substrate's workspace under its
	// configured commit identity, the lowest rung of the delegated-execution
	// ladder. Like the stoma route it consults the delegation gate
	// per-invocation, so it takes the bare bearer auth rather than
	// guardedAuth's blanket delegated-request denial.
	// Seed REST API (adapter): grow a Seed (a bounded intent) to Fruit. Like the
	// stoma route it consults the delegation gate per-invocation (the
	// matching grant supplies the egress allow-list), so it takes the bare
	// bearer auth rather than guardedAuth's blanket delegated-request denial.
	seedHandler := receptors.NewSeedHandler(deps.CoreService).WithDelegation(deps.DelegationGate).WithHistory(deps.History)
	seedHandler.Register(mux, func(next http.HandlerFunc) http.HandlerFunc {
		return withAPIKeyOrPollinatorAuth(deps.APIKey, deps.PollinatorCredentials, deps.StemSigner, deps.Networked, next)
	})

	gitHandler := receptors.NewGitHandler(deps.CoreService).WithDelegation(deps.DelegationGate)
	gitHandler.Register(mux, func(next http.HandlerFunc) http.HandlerFunc {
		return withAPIKeyOrPollinatorAuth(deps.APIKey, deps.PollinatorCredentials, deps.StemSigner, deps.Networked, next)
	})

	// Phase 4: Configuration API
	configHandler := receptors.NewConfigHandler(deps.CoreService, deps.TendrilDir).WithDelegation(deps.DelegationGate)
	mux.HandleFunc("/v1/config/triggers", guardedAuth(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			configHandler.ListTriggers(w, r)
			return
		}
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}))
	mux.HandleFunc("/v1/config/genotypes", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			guardedAuth(configHandler.ListGenotypes)(w, r)
			return
		}
		if r.Method == http.MethodPost {
			withAPIKeyOrPollinatorAuth(deps.APIKey, deps.PollinatorCredentials, deps.StemSigner, deps.Networked, configHandler.UploadGenotype)(w, r)
			return
		}
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	})
	mux.HandleFunc("/v1/config/substrates", guardedAuth(configHandler.ListSubstrates))

	// Phase 5: MCP API (session-aware — shares the unified SessionManager and
	// projects the same Core session capabilities as REST and the CLI)
	mcpHandler := receptors.NewMCPHandler().WithSessions(deps.Sessions, deps.History).WithCore(deps.CoreService).WithDelegation(deps.DelegationGate, "")
	mux.HandleFunc("/v1", withAPIKeyOrPollinatorAuth(deps.APIKey, deps.PollinatorCredentials, deps.StemSigner, deps.Networked, mcpHandler.HandleMCP))

	// Phase 6: Mesh Grafting API
	registerBotanistRoute(mux, deps, "/v1/mesh/admin/issue-token", deps.MeshServer.HandleAdminIssueToken)
	mux.HandleFunc("/v1/mesh/graft", deps.DelegationGate.Middleware(deps.MeshServer.HandleGraftWebSocket))

	// Phase 7: Delegation Pending Confirmations API
	pendingHandler := receptors.NewDelegationPendingHandler(deps.PendingStore)
	for _, route := range pendingHandler.Routes() {
		registerBotanistRoute(mux, deps, route.Pattern, route.Handler)
	}

	return mux
}
