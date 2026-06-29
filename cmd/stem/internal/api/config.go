package api

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// AuthMiddleware wraps a handler to require an ADMIN_TOKEN.
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
		next(w, r)
	}
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

	if strings.Contains(header.Filename, "/") || strings.Contains(header.Filename, "\\") {
		http.Error(w, "Invalid filename", http.StatusBadRequest)
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

	genotypesDir := filepath.Join(h.TendrilDir, "genotypes")
	os.MkdirAll(genotypesDir, 0755)

	targetPath := filepath.Join(genotypesDir, name+".json")
	out, err := os.OpenFile(targetPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644)
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
