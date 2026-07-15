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

	"github.com/opentendril/core/cmd/stem/internal/conductor"
	"github.com/opentendril/core/cmd/stem/internal/configurator"
	"github.com/opentendril/core/cmd/stem/internal/core"
	"github.com/opentendril/core/cmd/stem/internal/eventbus"
	"github.com/opentendril/core/cmd/stem/internal/gateway"
	"github.com/opentendril/core/cmd/stem/internal/historydb"
	"github.com/opentendril/core/cmd/stem/internal/mesh"
	"github.com/opentendril/core/cmd/stem/internal/receptors"
	"github.com/opentendril/core/cmd/stem/internal/scheduler"
	"github.com/opentendril/core/cmd/stem/internal/security"
	"github.com/opentendril/core/cmd/stem/internal/session"
	"github.com/opentendril/core/cmd/stem/internal/telemetry"
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

type chatHistoryRecord struct {
	ChatID    string `json:"chatId"`
	StepID    string `json:"stepId"`
	SessionID string `json:"sessionId,omitempty"`
	Model     string `json:"model"`
	Prompt    string `json:"prompt"`
	Status    string `json:"status"`
	Response  string `json:"response,omitempty"`
	Error     string `json:"error,omitempty"`
	Timestamp string `json:"timestamp"`
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

	// The Stem must never serve its API unauthenticated (finding
	// 1): an explicit key wins, otherwise a previously generated key is
	// reused, otherwise a new one is generated and persisted so the
	// zero-config CLI/dashboard flow keeps working without a fail-open gap.
	apiKey, generatedKey, err := getOrCreateAPIKey(tendrilDir)
	if err != nil {
		log.Fatalf("⚠️ Failed to establish an API key: %v", err)
	}
	if generatedKey {
		log.Printf("🔑 Generated API key (saved to %s): %s", apiKeyFilePath(tendrilDir), apiKey)
		log.Println("   Set OPENTENDRIL_API_KEY to use your own instead.")
	}

	bus := eventbus.New()
	repoRoot := resolveRepoRoot("")

	// Persistent state layer (.tendril/history.db). OPENTENDRIL_DB_LOGGING=false
	// bypasses SQLite entirely for high-performance headless runs.
	history, err := historydb.OpenFromEnv(ctx, repoRoot)
	if err != nil {
		log.Printf("⚠️ Failed to open history database: %v (continuing without persistence)", err)
		history = nil
	}
	if history != nil {
		bus.AttachSink(history, 0)
		log.Printf("Persistent state enabled at %s", history.Path())
	} else {
		log.Println("Persistent state disabled (OPENTENDRIL_DB_LOGGING=false or open failure)")
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
		if !os.IsNotExist(err) {
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
	delegationGate := &receptors.DelegationGate{
		Authorizer: core.NewDelegationAuthorizer(delegationGrants),
		Bus:        bus,
	}
	if len(delegationGrants) > 0 {
		log.Printf("Delegation enabled: %d grant(s) loaded from %s", len(delegationGrants), filepath.Join(tendrilDir, core.DelegationGrantsFilename))
	}

	// guardedAuth authenticates with the bearer key and default-denies
	// delegated-marked requests: in this slice only the sprout routes consult
	// the delegation authorizer per-invocation, so every other surface refuses
	// a delegated invocation rather than silently running it as plain traffic.
	guardedAuth := func(next http.HandlerFunc) http.HandlerFunc {
		return withAPIKeyAuth(apiKey, delegationGate.Middleware(next))
	}

	mux := http.NewServeMux()

	mux.HandleFunc("/ws", withWebSocketAuth(apiKey, delegationGate.Middleware(gateway.HandleWebSocket(bus))))

	mux.HandleFunc("/v1/chat/completions", guardedAuth(handleChatCompletions(bus, sessions, history)))
	mux.HandleFunc("GET /health", delegationGate.Middleware(handleHealth))

	// Unified Interface Layer: the transport-free Core owns the session-
	// lifecycle, genome, plasmid, substrate-grafting, mesh trait governance,
	// sequence, and sprout/run capabilities; the REST, MCP, and CLI surfaces
	// are adapters that route through this one service.
	coreSvc := core.NewService(sessions).
		WithGenome(genomeOps(resolveRepoRoot(""))).
		WithPlasmid(plasmidOps(resolveRepoRoot(""))).
		WithMesh(meshOps()).
		WithSequence(serveSequenceOps(resolveRepoRoot(""), bus)).
		WithSprout(sproutOps(history)).
		WithPassthrough(passthroughOps())

	// Native scheduled sequences: cron entries from
	// .tendril/schedules.yaml grow Sequences and Sprouts inside this daemon,
	// through the same governed Core capabilities every other surface uses.
	// The scheduler stops with the daemon: it runs on the same shutdown ctx.
	schedulesPath := filepath.Join(tendrilDir, "schedules.yaml")
	if schedCfg, err := scheduler.LoadConfig(schedulesPath); err != nil {
		log.Printf("⚠️ Failed to load scheduler config: %v (scheduling disabled)", err)
	} else if schedCfg.Enabled && len(schedCfg.Schedules) > 0 {
		firer := scheduledRunFirer(coreSvc, sessions, "./.tendril/transduction/hormonal-triggers")
		scheduler.New(schedCfg, firer, log.Default()).Start(ctx)
		log.Printf("Scheduler enabled: %d schedule(s) loaded from %s", len(schedCfg.Schedules), schedulesPath)
	}

	// Tendril session REST API (adapter).
	sessionsHandler := receptors.NewSessionsHandler(coreSvc, sessions, history, bus)
	sessionsHandler.Register(mux, guardedAuth)

	// Genome REST API (adapter, slice 1).
	genomeHandler := receptors.NewGenomeHandler(coreSvc)
	genomeHandler.Register(mux, guardedAuth)

	// Plasmid REST API (adapter, slice 2).
	plasmidHandler := receptors.NewPlasmidHandler(coreSvc)
	plasmidHandler.Register(mux, guardedAuth)

	// Substrate-grafting REST API (adapter, slice 3). Distinct from
	// the mesh *server* endpoints mounted below: these are the client-side
	// delegation commands.
	graftHandler := receptors.NewGraftHandler(coreSvc)
	graftHandler.Register(mux, guardedAuth)

	// Mesh trait governance REST API (adapter). These routes
	// expose the pending-trait inbox to the Command Center and CLI.
	traitHandler := receptors.NewTraitHandler(coreSvc)
	traitHandler.Register(mux, guardedAuth)

	// Sequence REST API (adapter, slice 4).
	sequenceHandler := receptors.NewSequenceHandler(coreSvc)
	sequenceHandler.Register(mux, guardedAuth)

	// Sprout REST API (adapter, final family). Detached
	// POST /v1/sessions/{sessionId}/sprout/run is registered outside the
	// parity registry inside SproutHandler.Register. Both sprout routes
	// consult the delegation gate per-invocation (with the decoded substrate
	// in hand), so they take the bare bearer auth rather than guardedAuth's
	// blanket delegated-request denial.
	sproutHandler := receptors.NewSproutHandler(coreSvc, history, bus).WithDelegation(delegationGate)
	sproutHandler.Register(mux, func(next http.HandlerFunc) http.HandlerFunc {
		return withAPIKeyAuth(apiKey, next)
	})

	// Passthrough REST API (adapter): one bounded command in a
	// network-sealed terrarium, the minimal delegable operation-class. Like
	// the sprout routes it consults the delegation gate per-invocation (the
	// matching grant supplies the egress allow-list), so it takes the bare
	// bearer auth rather than guardedAuth's blanket delegated-request denial.
	passthroughHandler := receptors.NewPassthroughHandler(coreSvc).WithDelegation(delegationGate)
	passthroughHandler.Register(mux, func(next http.HandlerFunc) http.HandlerFunc {
		return withAPIKeyAuth(apiKey, next)
	})

	// Phase 4: Configuration API
	configHandler := receptors.NewConfigHandler(tendrilDir)
	mux.HandleFunc("/v1/config/triggers", guardedAuth(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			configHandler.ListTriggers(w, r)
			return
		}
		if r.Method == http.MethodPost {
			configHandler.UploadTrigger(w, r)
			return
		}
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}))
	mux.HandleFunc("/v1/config/genotypes", guardedAuth(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			configHandler.ListGenotypes(w, r)
			return
		}
		if r.Method == http.MethodPost {
			configHandler.UploadGenotype(w, r)
			return
		}
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}))

	// Phase 5: MCP API (session-aware — shares the unified SessionManager and
	// projects the same Core session capabilities as REST and the CLI)
	mcpHandler := receptors.NewMCPHandler().WithSessions(sessions, history).WithCore(coreSvc)
	mux.HandleFunc("/v1", guardedAuth(mcpHandler.HandleMCP))

	// Phase 6: Mesh Grafting API
	meshServer := mesh.NewServer(resolveRepoRoot(""))
	adminKey := strings.TrimSpace(os.Getenv("ADMIN_TOKEN"))
	if adminKey == "" {
		// No dedicated admin secret configured: fall back to the Stem's own
		// (never-empty) API key rather than fail open on mesh token issuance.
		adminKey = apiKey
	}
	mux.HandleFunc("/v1/mesh/admin/issue-token", withAPIKeyAuth(adminKey, delegationGate.Middleware(meshServer.HandleAdminIssueToken)))
	mux.HandleFunc("/v1/mesh/graft", delegationGate.Middleware(meshServer.HandleGraftWebSocket))

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	server := &http.Server{
		Addr:    ":" + port,
		Handler: mux,
	}

	go func() {
		<-ctx.Done()
		log.Println("Shutting down API server...")
		server.Shutdown(context.Background())
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
	go func() {
		gatewayMux := http.NewServeMux()
		gatewayMux.HandleFunc("/ws", withWebSocketAuth(apiKey, delegationGate.Middleware(gateway.HandleWebSocket(bus))))
		gatewayServer := &http.Server{
			Addr:    ":" + gatewayPort,
			Handler: gatewayMux,
		}
		log.Printf("Starting Gateway WebSocket server on port %s...", gatewayPort)
		if err := gatewayServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Printf("⚠️ Gateway WebSocket server unavailable on port %s: %v (main /ws endpoint still serves)", gatewayPort, err)
		}
	}()

	log.Printf("Starting Go Stem API on port %s...", port)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("Server failed: %v", err)
	}
}

// scheduledRunFirer is the concrete firing seam the scheduler grows entries
// through. Each fire, in order: (1) the run passes Hormonal Triggers exactly
// like the chat path — a blocked run never grows; (2) the entry grows through
// the same governed Core capability (sequence.run / sprout.run) as every
// other surface, so the run's lifecycle telemetry flows to the Command Center
// via the EventBus-threaded sequence port.
func scheduledRunFirer(coreSvc core.Core, sessions *session.Manager, triggersDir string) scheduler.FirerFunc {
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
		payload := security.TriggerPayload{Genotype: e.Model, Transcript: e.Sequence}
		if e.Sprout != nil {
			payload.Genotype = firstNonEmpty(e.Sprout.Genotype, e.Sprout.Model, e.Model)
			payload.Transcript = e.Sprout.Transcript
		}
		if err := security.EvaluateTriggers(ctx, triggersDir, payload); err != nil {
			log.Printf("🚫 Schedule %q: scheduled run blocked by Hormonal Triggers: %v", name, err)
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
		// session's preferences: the governed sprout.run capability shapes
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
		if sess, err := sessions.Sprout(ctx, scheduledOrigin, prefs); err != nil {
			log.Printf("⚠️ Schedule %q: failed to sprout a session for the run (growing sessionless): %v", name, err)
		} else {
			input["sessionId"] = sess.ID
		}

		log.Printf("⏰ Schedule %q: growing a Sprout: %s", name, e.Sprout.Transcript)
		result, err := coreSvc.Invoke(ctx, core.CapSproutRun, input)
		if err != nil {
			log.Printf("❌ Schedule %q: Sprout withered: %v", name, err)
			return err
		}
		runResult, _ := result.(core.SproutRunResult)
		log.Printf("✅ Schedule %q: Sprout %s matured (session %s)", name, runResult.StepID, runResult.SessionID)
		return nil
	}
}

func resolveServeAPIKey() string {
	if key := strings.TrimSpace(os.Getenv("OPENTENDRIL_API_KEY")); key != "" {
		return key
	}
	return strings.TrimSpace(os.Getenv("ADMIN_TOKEN"))
}

// apiKeyFilePath is where getOrCreateAPIKey persists a generated bearer key,
// mirroring the getOrCreateMemoryKey pattern (cmdmemory.go) used for the
// rhizome encryption key.
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

// getOrCreateAPIKey resolves the Stem's bearer key: OPENTENDRIL_API_KEY/
// ADMIN_TOKEN win, then a previously generated key on disk, then a freshly
// generated one persisted for next time. It never returns an empty key, so
// the Stem can never come up serving its API unauthenticated (// finding 1).
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
	return func(w http.ResponseWriter, r *http.Request) {
		// An empty apiKey is a caller bug, not an invitation to skip auth:
		// fail closed rather than repeat finding 1.
		if strings.TrimSpace(apiKey) == "" || !bearerMatches(r.Header.Get("Authorization"), apiKey) {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}

// withWebSocketAuth gates a WebSocket upgrade handler behind the same bearer
// key as the REST/MCP surface. Browsers cannot attach
// custom headers to the native WebSocket handshake, so a `key` query
// parameter is accepted alongside the Authorization header used by non-browser
// clients (e.g. the CLI's gorilla/websocket dialer).
func withWebSocketAuth(apiKey string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if strings.TrimSpace(apiKey) == "" {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		if bearerMatches(r.Header.Get("Authorization"), apiKey) {
			next(w, r)
			return
		}
		if subtle.ConstantTimeCompare([]byte(r.URL.Query().Get("key")), []byte(apiKey)) == 1 {
			next(w, r)
			return
		}
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
	}
}

func handleHealth(w http.ResponseWriter, r *http.Request) {
	monitor := newDefaultHealthMonitor(nil, 30*time.Second)
	report := monitor.RunOnce(r.Context())

	w.Header().Set("Content-Type", "application/json")
	if !report.Overall {
		w.WriteHeader(http.StatusServiceUnavailable)
	}
	json.NewEncoder(w).Encode(report)
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

		// Bind this interaction to a Tendril session. Clients pass sessionId in
		// the body or the X-Tendril-Session header; absent both, a fresh
		// session sprouts so every run is still traceable.
		requestedSessionID := strings.TrimSpace(req.SessionID)
		if requestedSessionID == "" {
			requestedSessionID = strings.TrimSpace(r.Header.Get("X-Tendril-Session"))
		}
		sess, err := sessions.GetOrSprout(r.Context(), requestedSessionID, session.OriginREST)
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
		historyRoot := resolveRepoRoot("")
		historyPath := filepath.Join(historyRoot, ".tendril", "history", chatID+".json")
		historyRecord := chatHistoryRecord{
			ChatID:    chatID,
			StepID:    stepID,
			SessionID: sess.ID,
			Model:     model,
			Prompt:    taskPrompt,
			Timestamp: runStarted.Format(time.RFC3339Nano),
		}

		// Phase 3 Part 2: Hormonal Triggers (Pre-execution Security)
		payload := security.TriggerPayload{
			Genotype:   model,
			Transcript: taskPrompt,
		}

		triggersDir := "./.tendril/transduction/hormonal-triggers"
		if err := security.EvaluateTriggers(r.Context(), triggersDir, payload); err != nil {
			log.Printf("Sprout blocked by Hormonal Triggers: %v", err)
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

		// Route to internal Configurator Tendril or external Docker Tendril
		if model == "configurator" {
			configTendril := configurator.NewConfiguratorTendril(triggersDir)
			output, err = configTendril.Execute(r.Context(), taskPrompt)
		} else {
			orch := conductor.NewDockerOrchestrator()
			orch.StepID = stepID
			orch.EventBus = bus
			orch.Provider = sess.Preferences.Provider
			orch.Model = sess.Preferences.Model
			orch.Genotype = sess.Preferences.Genotype
			output, err = orch.RunSprout(r.Context(), taskPrompt)
		}

		// Emit stream end event
		bus.Publish(eventbus.Event{
			Type:      eventbus.EventStreamToken,
			Source:    stepID,
			SessionID: sess.ID,
			Data:      map[string]interface{}{"type": "stream.end", "content": output},
		})

		sproutRun.FinishedAt = time.Now().UTC()
		historyRecord.Response = output
		if err != nil {
			log.Printf("Tendril execution failed: %v", err)
			historyRecord.Status = "failed"
			historyRecord.Error = err.Error()
			sproutRun.Status = "withered"
			sproutRun.Error = err.Error()
			if history != nil {
				if recordErr := history.RecordSproutRun(r.Context(), sproutRun); recordErr != nil {
					log.Printf("⚠️ Failed to record sprout run result: %v", recordErr)
				}
			}
			if writeErr := writeChatHistory(historyPath, historyRecord); writeErr != nil {
				log.Printf("⚠️ Failed to write chat history: %v", writeErr)
			}
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		historyRecord.Status = "complete"
		sproutRun.Status = "matured"
		sproutRun.Output = output
		if history != nil {
			if recordErr := history.RecordSproutRun(r.Context(), sproutRun); recordErr != nil {
				log.Printf("⚠️ Failed to record sprout run result: %v", recordErr)
			}
		}
		if writeErr := writeChatHistory(historyPath, historyRecord); writeErr != nil {
			log.Printf("⚠️ Failed to write chat history: %v", writeErr)
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

func writeChatHistory(path string, record chatHistoryRecord) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create chat history directory: %w", err)
	}

	payload, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return fmt.Errorf("encode chat history: %w", err)
	}
	payload = append(payload, '\n')

	if err := os.WriteFile(path, payload, 0o644); err != nil {
		return fmt.Errorf("write chat history %s: %w", path, err)
	}

	return nil
}
