package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"strings"

	"github.com/opentendril/core/cmd/stem/internal/api"
	"github.com/opentendril/core/cmd/stem/internal/configurator"
	"github.com/opentendril/core/cmd/stem/internal/orchestrator"
	"github.com/opentendril/core/cmd/stem/internal/security"
)

type ChatCompletionRequest struct {
	Model    string       `json:"model"`
	Messages []APIMessage `json:"messages"`
}

type APIMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type ChatCompletionResponse struct {
	ID      string   `json:"id"`
	Object  string   `json:"object"`
	Created int64    `json:"created"`
	Model   string   `json:"model"`
	Choices []Choice `json:"choices"`
}

type Choice struct {
	Index        int        `json:"index"`
	Message      APIMessage `json:"message"`
	FinishReason string     `json:"finishReason"`
}

func runServeCmd(ctx context.Context, args []string) {
	apiKey := resolveServeAPIKey()
	if apiKey == "" {
		log.Println("⚠️ WARNING: OPENTENDRIL_API_KEY is not set. API endpoints are running without authentication.")
	}

	mux := http.NewServeMux()

	mux.HandleFunc("/v1/chat/completions", withAPIKeyAuth(apiKey, handleChatCompletions))
	mux.HandleFunc("GET /health", handleHealth)

	// Phase 4: Configuration API
	tendrilDir := "./.tendril"
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

	// Phase 5: MCP API
	mcpHandler := api.NewMCPHandler()
	mux.HandleFunc("/v1", withAPIKeyAuth(apiKey, mcpHandler.HandleMCP))

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
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"status": "healthy",
		"kernel": "Go Stem",
	})
}

func handleChatCompletions(w http.ResponseWriter, r *http.Request) {
	var req ChatCompletionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if len(req.Messages) == 0 {
		http.Error(w, "no messages provided", http.StatusBadRequest)
		return
	}

	taskPrompt := req.Messages[len(req.Messages)-1].Content

	// Phase 3 Part 2: Hormonal Triggers (Pre-execution Security)
	payload := security.TriggerPayload{
		Genotype:   req.Model,
		Transcript: taskPrompt,
	}

	triggersDir := "./.tendril/transduction/hormonal-triggers"
	if err := security.EvaluateTriggers(r.Context(), triggersDir, payload); err != nil {
		log.Printf("Sprout blocked by Hormonal Triggers: %v", err)
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}

	log.Printf("Sprouting Tendril for task: %s", taskPrompt)

	var output string
	var err error

	// Route to internal Configurator Tendril or external Docker Tendril
	if req.Model == "configurator" {
		configTendril := configurator.NewConfiguratorTendril(triggersDir)
		output, err = configTendril.Execute(r.Context(), taskPrompt)
	} else {
		// Phase 3: Execute the Python Tendril via the Orchestrator
		orch := &orchestrator.DockerOrchestrator{
			ImageName: "opentendril-tendril:latest", // Hardcoded for now
		}
		output, err = orch.RunTendril(r.Context(), taskPrompt)
	}

	if err != nil {
		log.Printf("Tendril execution failed: %v", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Format response as OpenAI completion
	resp := ChatCompletionResponse{
		ID:      "chatcmpl-tendril",
		Object:  "chat.completion",
		Created: 0,
		Model:   req.Model,
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
