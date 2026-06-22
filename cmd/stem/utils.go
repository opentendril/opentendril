package main

import (
	"bufio"
	"context"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

var backendCmd *exec.Cmd

func ensureBackendOnline(ctx context.Context, brainURL string) {
	client := &http.Client{Timeout: 500 * time.Millisecond}
	req, err := http.NewRequestWithContext(ctx, "GET", brainURL+"/health", nil)
	if err == nil {
		resp, err := client.Do(req)
		if err == nil && resp.StatusCode == http.StatusOK {
			resp.Body.Close()
			return
		}
	}

	log.Println("⚠️ Backend FastAPI server is offline. Auto-booting...")
	projectDir := findProjectDir()

	if isSandboxEnabled() && isDockerRunning() {
		log.Println("🐳 Booting backend via Docker Compose...")
		c := exec.Command("docker", "compose", "up", "-d")
		c.Dir = projectDir
		c.Stdout = os.Stderr
		c.Stderr = os.Stderr
		_ = c.Run()
	} else {
		log.Println("🚀 Booting backend via standard subprocess (Solo Mode)...")
		var uvicornCmd string
		venvPath := filepath.Join(projectDir, "venv", "bin", "uvicorn")
		if _, err := os.Stat(venvPath); err == nil {
			uvicornCmd = venvPath
		} else {
			uvicornCmd = "uvicorn"
		}

		backendCmd = exec.Command(uvicornCmd, "src.main:app", "--host", "127.0.0.1", "--port", "8080")
		backendCmd.Dir = projectDir
		backendCmd.Stdout = os.Stderr
		backendCmd.Stderr = os.Stderr
		if err := backendCmd.Start(); err != nil {
			log.Printf("Error starting uvicorn: %v", err)
		}
	}

	for i := 0; i < 20; i++ {
		time.Sleep(500 * time.Millisecond)
		req, err := http.NewRequestWithContext(ctx, "GET", brainURL+"/health", nil)
		if err == nil {
			resp, err := client.Do(req)
			if err == nil && resp.StatusCode == http.StatusOK {
				resp.Body.Close()
				log.Println("✅ Backend online!")
				return
			}
		}
	}
	log.Println("❌ Timeout waiting for backend to start.")
}

func isSandboxEnabled() bool {
	if val := os.Getenv("SANDBOX_ENABLED"); val != "" {
		return strings.ToLower(val) == "true"
	}
	projectDir := findProjectDir()
	paths := []string{
		filepath.Join(projectDir, ".env"),
		filepath.Join(projectDir, "core", ".env"),
	}
	for _, p := range paths {
		file, err := os.Open(p)
		if err == nil {
			defer file.Close()
			scanner := bufio.NewScanner(file)
			for scanner.Scan() {
				line := strings.TrimSpace(scanner.Text())
				if strings.HasPrefix(line, "SANDBOX_ENABLED=") {
					parts := strings.SplitN(line, "=", 2)
					if len(parts) == 2 {
						val := strings.Trim(parts[1], `"' `)
						return strings.ToLower(val) == "true"
					}
				}
			}
		}
	}
	return true
}

func isDockerRunning() bool {
	cmd := exec.Command("docker", "info")
	return cmd.Run() == nil
}

func findProjectDir() string {
	cwd, err := os.Getwd()
	if err != nil {
		return "."
	}
	dir := cwd
	for {
		if _, err := os.Stat(filepath.Join(dir, "src", "main.py")); err == nil {
			return dir
		}
		if _, err := os.Stat(filepath.Join(dir, "core", "src", "main.py")); err == nil {
			return filepath.Join(dir, "core")
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return "."
}

func restartBackend(ctx context.Context, brainURL string) {
	if isSandboxEnabled() && isDockerRunning() {
		log.Println("🔄 Remounting volumes and applying configuration via Docker Compose...")
		cmd := exec.Command("docker", "compose", "up", "-d")
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		_ = cmd.Run()
	} else {
		log.Println("🔄 Restarting backend subprocess (Solo Mode)...")
		if backendCmd != nil && backendCmd.Process != nil {
			_ = backendCmd.Process.Kill()
			_ = backendCmd.Wait()
		}

		projectDir := findProjectDir()
		var uvicornCmd string
		venvPath := filepath.Join(projectDir, "venv", "bin", "uvicorn")
		if _, err := os.Stat(venvPath); err == nil {
			uvicornCmd = venvPath
		} else {
			uvicornCmd = "uvicorn"
		}

		backendCmd = exec.Command(uvicornCmd, "src.main:app", "--host", "127.0.0.1", "--port", "8080")
		backendCmd.Dir = projectDir
		backendCmd.Stdout = os.Stderr
		backendCmd.Stderr = os.Stderr
		if err := backendCmd.Start(); err != nil {
			log.Printf("Error starting uvicorn: %v", err)
			return
		}

		client := &http.Client{Timeout: 500 * time.Millisecond}
		for i := 0; i < 20; i++ {
			time.Sleep(500 * time.Millisecond)
			req, err := http.NewRequestWithContext(ctx, "GET", brainURL+"/health", nil)
			if err == nil {
				resp, err := client.Do(req)
				if err == nil && resp.StatusCode == http.StatusOK {
					resp.Body.Close()
					log.Println("✅ Backend online!")
					return
				}
			}
		}
	}
}
