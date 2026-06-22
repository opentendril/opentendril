package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"os"

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
	FinishReason string     `json:"finish_reason"`
}

func runServeCmd(ctx context.Context, args []string) {
	mux := http.NewServeMux()

	mux.HandleFunc("/v1/chat/completions", handleChatCompletions)
	mux.HandleFunc("GET /health", handleHealth)

	// Phase 4: Configuration API
	tendrilDir := "./.tendril"
	configHandler := api.NewConfigHandler(tendrilDir)
	configHandler.SetupRoutes(mux)

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
		Persona: req.Model,
		Task:    taskPrompt,
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
