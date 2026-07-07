package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/opentendril/core/cmd/stem/internal/api"
	"github.com/opentendril/core/cmd/stem/internal/configurator"
	"github.com/opentendril/core/cmd/stem/internal/eventbus"
	"github.com/opentendril/core/cmd/stem/internal/gateway"
	"github.com/opentendril/core/cmd/stem/internal/historydb"
	"github.com/opentendril/core/cmd/stem/internal/mesh"
	"github.com/opentendril/core/cmd/stem/internal/orchestrator"
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
	apiKey := resolveServeAPIKey()
	if apiKey == "" {
		log.Println("⚠️ WARNING: OPENTENDRIL_API_KEY is not set. API endpoints are running without authentication.")
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

	tendrilDir := "./.tendril"
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

	mux := http.NewServeMux()

	mux.Handle("/dashboard/", http.StripPrefix("/dashboard/", http.FileServer(http.Dir("./dashboard"))))
	mux.HandleFunc("/ws", gateway.HandleWebSocket(bus))

	mux.HandleFunc("/v1/chat/completions", withAPIKeyAuth(apiKey, handleChatCompletions(bus, sessions, history)))
	mux.HandleFunc("GET /health", handleHealth)

	// Unified Interface Layer: Tendril session REST API.
	sessionsHandler := api.NewSessionsHandler(sessions, history)
	sessionsHandler.Register(mux, func(next http.HandlerFunc) http.HandlerFunc {
		return withAPIKeyAuth(apiKey, next)
	})

	// Phase 4: Configuration API
	configHandler := api.NewConfigHandler(tendrilDir)
	mux.HandleFunc("/v1/config/triggers", withAPIKeyAuth(apiKey, func(w http.ResponseWriter, r *http.Request) {
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
	mux.HandleFunc("/v1/config/genotypes", withAPIKeyAuth(apiKey, func(w http.ResponseWriter, r *http.Request) {
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

	// Phase 5: MCP API (session-aware — shares the unified SessionManager)
	mcpHandler := api.NewMCPHandler().WithSessions(sessions, history)
	mux.HandleFunc("/v1", withAPIKeyAuth(apiKey, mcpHandler.HandleMCP))

	// Phase 6: Mesh Grafting API
	meshServer := mesh.NewServer(resolveRepoRoot(""))
	mux.HandleFunc("/v1/mesh/admin/issue-token", withAPIKeyAuth(strings.TrimSpace(os.Getenv("ADMIN_TOKEN")), meshServer.HandleAdminIssueToken))
	mux.HandleFunc("/v1/mesh/graft", meshServer.HandleGraftWebSocket)

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
		gatewayMux.HandleFunc("/ws", gateway.HandleWebSocket(bus))
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

func resolveServeAPIKey() string {
	if key := strings.TrimSpace(os.Getenv("OPENTENDRIL_API_KEY")); key != "" {
		return key
	}
	return strings.TrimSpace(os.Getenv("ADMIN_TOKEN"))
}

func withAPIKeyAuth(apiKey string, next http.HandlerFunc) http.HandlerFunc {
	if strings.TrimSpace(apiKey) == "" {
		return next
	}

	return func(w http.ResponseWriter, r *http.Request) {
		if strings.TrimSpace(r.Header.Get("Authorization")) != "Bearer "+apiKey {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		next(w, r)
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
			orch := orchestrator.NewDockerOrchestrator()
			orch.StepID = stepID
			orch.EventBus = bus
			orch.Provider = sess.Preferences.Provider
			orch.Model = sess.Preferences.Model
			orch.Genotype = sess.Preferences.Genotype
			output, err = orch.RunTendril(r.Context(), taskPrompt)
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
