package mesh

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// Server exposes admin token issuance and graft validation endpoints.
type Server struct {
	workspace string
	upgrader  websocket.Upgrader
}

type adminIssueTokenRequest struct {
	Issuer        string `json:"issuer,omitempty"`
	Subject       string `json:"subject,omitempty"`
	Audience      string `json:"audience,omitempty"`
	MeshScope     string `json:"meshScope,omitempty"`
	WorkspacePath string `json:"workspacePath,omitempty"`
	TokenID       string `json:"tokenId,omitempty"`
	TTL           string `json:"ttl,omitempty"`
}

// NewServer constructs a mesh server rooted at the provided workspace.
func NewServer(workspace string) *Server {
	return &Server{
		workspace: ResolveRepoRoot(workspace),
		upgrader: websocket.Upgrader{
			ReadBufferSize:  1024 * 8,
			WriteBufferSize: 1024 * 8,
			CheckOrigin: func(r *http.Request) bool {
				return true
			},
		},
	}
}

// HandleAdminIssueToken issues a signed JWT using the workspace's private key.
func (s *Server) HandleAdminIssueToken(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req adminIssueTokenRequest
	if err := decodeJSONBody(r.Body, &req); err != nil {
		http.Error(w, fmt.Sprintf("Invalid JSON body: %v", err), http.StatusBadRequest)
		return
	}

	ttl := defaultTokenTTL
	if trimmed := strings.TrimSpace(req.TTL); trimmed != "" {
		parsed, err := time.ParseDuration(trimmed)
		if err != nil {
			http.Error(w, fmt.Sprintf("Invalid ttl value: %v", err), http.StatusBadRequest)
			return
		}
		ttl = parsed
	}

	audience := []string{}
	if trimmed := strings.TrimSpace(req.Audience); trimmed != "" {
		audience = append(audience, trimmed)
	}

	workspacePath := strings.TrimSpace(req.WorkspacePath)
	if workspacePath == "" {
		workspacePath = s.workspace
	}

	token, err := IssueWorkspaceToken(s.workspace, TokenOptions{
		Issuer:        req.Issuer,
		Subject:       req.Subject,
		Audience:      audience,
		MeshScope:     req.MeshScope,
		WorkspacePath: workspacePath,
		TokenID:       req.TokenID,
		ExpiresIn:     ttl,
		Now:           time.Now().UTC(),
	})
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to issue token: %v", err), http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{
		"token": token,
	})
}

// HandleGraftWebSocket validates a graft token, applies a patch, runs governance, and pushes the result.
func (s *Server) HandleGraftWebSocket(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	token, err := bearerTokenFromHeader(r.Header.Get("Authorization"))
	if err != nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	publicKey, err := LoadPublicKey(s.workspace)
	if err != nil {
		http.Error(w, fmt.Sprintf("Unable to load mesh public key: %v", err), http.StatusInternalServerError)
		return
	}

	if _, err := VerifyToken(token, publicKey, TokenValidationOptions{
		Now:              time.Now().UTC(),
		ExpectedIssuer:   defaultIssuer,
		ExpectedAudience: defaultAudience,
		ExpectedScope:    defaultMeshScope,
	}); err != nil {
		http.Error(w, fmt.Sprintf("Unauthorized: %v", err), http.StatusUnauthorized)
		return
	}

	conn, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()

	conn.SetReadLimit(64 << 20)

	var writeMu sync.Mutex
	send := func(msg graftMessage) error {
		writeMu.Lock()
		defer writeMu.Unlock()
		if err := conn.SetWriteDeadline(time.Now().Add(15 * time.Second)); err != nil {
			return err
		}
		return conn.WriteJSON(msg)
	}

	if err := send(graftMessage{Type: "graft-status", Status: "connected"}); err != nil {
		return
	}

	var req graftRequest
	if err := conn.ReadJSON(&req); err != nil {
		_ = send(graftMessage{Type: "graft-result", Status: "error", Error: fmt.Sprintf("failed to read graft request: %v", err)})
		return
	}
	if strings.TrimSpace(req.Type) != "" && strings.TrimSpace(req.Type) != "graft-request" {
		_ = send(graftMessage{Type: "graft-result", Status: "error", Error: "unexpected graft message type"})
		return
	}
	if strings.TrimSpace(req.Patch) == "" {
		_ = send(graftMessage{Type: "graft-result", Status: "error", Error: "graft patch is required"})
		return
	}

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	sequenceRelPath, err := resolveSequenceRelativePath(req.SequencePath)
	if err != nil {
		_ = send(graftMessage{Type: "graft-result", Status: "error", Error: err.Error()})
		return
	}

	branchName := strings.TrimSpace(req.Branch)
	if branchName == "" {
		if shortHash := shortCommitHash(req.CommitHash); shortHash != "" {
			branchName = "mesh-graft-" + shortHash
		} else {
			branchName = "mesh-graft"
		}
	}
	if err := validateMeshBranchName(branchName); err != nil {
		_ = send(graftMessage{Type: "graft-result", Status: "error", Error: err.Error()})
		return
	}

	sendLog := func(stream, message string) error {
		return send(graftMessage{
			Type:    "graft-log",
			Stream:  stream,
			Message: message,
		})
	}

	if err := sendLog("server", "Preparing mesh graft terrarium."); err != nil {
		return
	}

	terrariumPath, err := createGraftTerrarium(ctx, s.workspace)
	if err != nil {
		_ = send(graftMessage{Type: "graft-result", Status: "error", Error: err.Error()})
		return
	}
	defer removeGraftTerrarium(s.workspace, terrariumPath)

	if err := copyGovernanceSequence(s.workspace, terrariumPath, sequenceRelPath); err != nil {
		_ = send(graftMessage{Type: "graft-result", Status: "error", Error: err.Error()})
		return
	}

	startingHead, err := runGitOutput(ctx, terrariumPath, "rev-parse", "HEAD")
	if err != nil {
		_ = send(graftMessage{Type: "graft-result", Status: "error", Error: err.Error()})
		return
	}

	if err := applyPatchToTerrarium(ctx, terrariumPath, req.Patch); err != nil {
		_ = send(graftMessage{Type: "graft-result", Status: "error", Error: err.Error()})
		return
	}

	if err := sendLog("server", "Patch applied. Running mesh governance sequence."); err != nil {
		return
	}

	if err := runGovernanceSequence(ctx, terrariumPath, sequenceRelPath, send); err != nil {
		_ = send(graftMessage{Type: "graft-result", Status: "error", Error: err.Error()})
		return
	}

	commitHash, err := commitAndPushValidatedTerrarium(ctx, terrariumPath, branchName, req.CommitMessage, startingHead)
	if err != nil {
		_ = send(graftMessage{Type: "graft-result", Status: "error", Error: err.Error()})
		return
	}

	_ = send(graftMessage{
		Type:       "graft-result",
		Status:     "complete",
		CommitHash: commitHash,
	})
}

func decodeJSONBody(body io.Reader, target any) error {
	if body == nil {
		return errors.New("empty body")
	}

	decoder := json.NewDecoder(body)
	if err := decoder.Decode(target); err != nil {
		if errors.Is(err, io.EOF) {
			return nil
		}
		return err
	}

	return nil
}

func writeJSON(w http.ResponseWriter, statusCode int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	_ = json.NewEncoder(w).Encode(payload)
}

func bearerTokenFromHeader(header string) (string, error) {
	trimmed := strings.TrimSpace(header)
	if trimmed == "" {
		return "", fmt.Errorf("missing authorization header")
	}
	if !strings.HasPrefix(trimmed, "Bearer ") {
		return "", fmt.Errorf("missing bearer token")
	}

	token := strings.TrimSpace(strings.TrimPrefix(trimmed, "Bearer "))
	if token == "" {
		return "", fmt.Errorf("missing bearer token")
	}

	return token, nil
}

func resolveSequenceRelativePath(sequencePath string) (string, error) {
	trimmed := strings.TrimSpace(sequencePath)
	if trimmed == "" {
		return filepath.Join(".tendril", "mesh-governance.yaml"), nil
	}
	if filepath.IsAbs(trimmed) {
		return "", fmt.Errorf("sequence path must be relative to the workspace")
	}

	cleaned := filepath.Clean(trimmed)
	if !filepath.IsLocal(cleaned) {
		return "", fmt.Errorf("invalid sequence path %q", sequencePath)
	}

	return cleaned, nil
}

func createGraftTerrarium(ctx context.Context, sourceRoot string) (string, error) {
	sourceRoot = ResolveRepoRoot(sourceRoot)
	if strings.TrimSpace(sourceRoot) == "" {
		return "", fmt.Errorf("workspace root is required")
	}

	terrariumPath := filepath.Join(os.TempDir(), "opentendril-graft-"+shortCommitHash(randomTokenID()))
	cmd := exec.CommandContext(ctx, "git", "-C", sourceRoot, "worktree", "add", "--detach", terrariumPath, "HEAD")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("create graft terrarium failed: %w (output: %s)", err, strings.TrimSpace(string(output)))
	}

	return terrariumPath, nil
}

func removeGraftTerrarium(sourceRoot, terrariumPath string) {
	sourceRoot = ResolveRepoRoot(sourceRoot)
	if strings.TrimSpace(sourceRoot) != "" {
		cmd := exec.Command("git", "-C", sourceRoot, "worktree", "remove", "--force", terrariumPath)
		_ = cmd.Run()
	}
	_ = os.RemoveAll(terrariumPath)
}

func copyGovernanceSequence(sourceRoot, terrariumPath, sequenceRelPath string) error {
	sourcePath := filepath.Join(sourceRoot, sequenceRelPath)
	content, err := os.ReadFile(sourcePath)
	if err != nil {
		return fmt.Errorf("read mesh governance sequence %s: %w", sourcePath, err)
	}

	targetPath := filepath.Join(terrariumPath, sequenceRelPath)
	if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
		return fmt.Errorf("create governance sequence directory: %w", err)
	}
	if err := os.WriteFile(targetPath, content, 0o644); err != nil {
		return fmt.Errorf("write governance sequence %s: %w", targetPath, err)
	}

	return nil
}

func applyPatchToTerrarium(ctx context.Context, terrariumPath, patch string) error {
	cmd := exec.CommandContext(ctx, "git", "-C", terrariumPath, "apply", "--binary", "--whitespace=nowarn", "-")
	cmd.Stdin = strings.NewReader(patch)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("apply graft patch failed: %w (output: %s)", err, strings.TrimSpace(string(output)))
	}

	return nil
}

func runGovernanceSequence(ctx context.Context, terrariumPath, sequenceRelPath string, send func(graftMessage) error) error {
	subCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	executable, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve tendril executable: %w", err)
	}

	args := []string{"sequence", "run", filepath.ToSlash(sequenceRelPath)}
	cmd := exec.CommandContext(subCtx, executable, args...)
	cmd.Dir = terrariumPath
	cmd.Env = sanitizedValidationEnv(os.Environ())
	cmd.Stdin = strings.NewReader("")

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("prepare governance stdout pipe: %w", err)
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("prepare governance stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start mesh governance sequence: %w", err)
	}

	errCh := make(chan error, 2)
	go func() {
		err := scanLines(stdoutPipe, func(stream, message string) error {
			return send(graftMessage{Type: "graft-log", Stream: stream, Message: message})
		}, "stdout")
		if err != nil {
			cancel()
		}
		errCh <- err
	}()
	go func() {
		err := scanLines(stderrPipe, func(stream, message string) error {
			return send(graftMessage{Type: "graft-log", Stream: stream, Message: message})
		}, "stderr")
		if err != nil {
			cancel()
		}
		errCh <- err
	}()

	waitErr := cmd.Wait()
	stdoutErr := <-errCh
	stderrErr := <-errCh

	if stdoutErr != nil {
		return stdoutErr
	}
	if stderrErr != nil {
		return stderrErr
	}
	if waitErr != nil {
		return fmt.Errorf("mesh governance sequence failed: %w", waitErr)
	}

	return nil
}

func sanitizedValidationEnv(env []string) []string {
	filtered := make([]string, 0, len(env))
	for _, entry := range env {
		switch {
		case strings.HasPrefix(entry, "TENDRIL_GRAFT_URL="):
			continue
		case strings.HasPrefix(entry, "TENDRIL_GRAFT_TOKEN="):
			continue
		default:
			filtered = append(filtered, entry)
		}
	}

	return filtered
}

func commitAndPushValidatedTerrarium(ctx context.Context, terrariumPath, branchName, commitMessage, startingHead string) (string, error) {
	status, err := runGitOutput(ctx, terrariumPath, "status", "--porcelain")
	if err != nil {
		return "", err
	}

	currentHead, err := runGitOutput(ctx, terrariumPath, "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}

	dirty := strings.TrimSpace(status) != ""
	if dirty {
		if _, err := runGitOutput(ctx, terrariumPath, "add", "-A"); err != nil {
			return "", err
		}

		commitMsg := strings.TrimSpace(commitMessage)
		if commitMsg == "" {
			commitMsg = "mesh graft validation"
		}

		if _, err := runGitOutput(ctx, terrariumPath, "commit", "-m", commitMsg); err != nil {
			return "", err
		}

		currentHead, err = runGitOutput(ctx, terrariumPath, "rev-parse", "HEAD")
		if err != nil {
			return "", err
		}
	}

	if !dirty && currentHead == strings.TrimSpace(startingHead) {
		return "", fmt.Errorf("mesh validation completed without material changes to push")
	}

	pushBranch := strings.TrimSpace(branchName)
	if pushBranch == "" {
		pushBranch = "mesh-graft-" + shortCommitHash(currentHead)
	}

	if _, err := runGitOutput(ctx, terrariumPath, "push", "origin", "HEAD:refs/heads/"+pushBranch); err != nil {
		return "", err
	}

	return currentHead, nil
}

func validateMeshBranchName(branch string) error {
	if branch == "" || len(branch) > 200 || strings.HasPrefix(branch, "-") ||
		strings.HasPrefix(branch, "/") || strings.HasSuffix(branch, "/") ||
		strings.HasSuffix(branch, ".") || strings.Contains(branch, "..") ||
		strings.Contains(branch, "//") || strings.Contains(branch, "@{") ||
		strings.ContainsAny(branch, "\\ ~^:?*[\x00\t\n\r") {
		return fmt.Errorf("invalid mesh branch name")
	}
	return nil
}
