package mesh

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path"
	"strings"
	"time"

	"github.com/gorilla/websocket"
)

// Client delegates high-privilege git pushes to a central stem.
type Client struct {
	graftURL  string
	token     string
	dialer    *websocket.Dialer
	logWriter io.Writer
}

type graftRequest struct {
	Type          string `json:"type"`
	WorkspacePath string `json:"workspacePath,omitempty"`
	Branch        string `json:"branch,omitempty"`
	CommitMessage string `json:"commitMessage,omitempty"`
	CommitHash    string `json:"commitHash,omitempty"`
	SequencePath  string `json:"sequencePath,omitempty"`
	Patch         string `json:"patch"`
}

type graftMessage struct {
	Type       string `json:"type"`
	Status     string `json:"status,omitempty"`
	Stream     string `json:"stream,omitempty"`
	Message    string `json:"message,omitempty"`
	CommitHash string `json:"commitHash,omitempty"`
	Error      string `json:"error,omitempty"`
}

// NewClientFromEnv constructs a mesh client when the graft environment is configured.
func NewClientFromEnv() *Client {
	graftURL := strings.TrimSpace(os.Getenv("TENDRIL_GRAFT_URL"))
	token := strings.TrimSpace(os.Getenv("TENDRIL_GRAFT_TOKEN"))
	if graftURL == "" || token == "" {
		return nil
	}

	return NewClient(graftURL, token)
}

// NewClient builds a mesh client for a central stem endpoint.
func NewClient(graftURL, token string) *Client {
	return &Client{
		graftURL: strings.TrimSpace(graftURL),
		token:    strings.TrimSpace(token),
		dialer: &websocket.Dialer{
			HandshakeTimeout: 15 * time.Second,
		},
		logWriter: os.Stderr,
	}
}

// SetLogWriter changes where streamed graft logs are written locally.
func (c *Client) SetLogWriter(w io.Writer) {
	if c == nil {
		return
	}
	c.logWriter = w
}

// DelegatePush sends the most recent commit patch to the central stem for validation and push.
func (c *Client) DelegatePush(ctx context.Context, workspace, branch, commitMessage string) (string, error) {
	if c == nil {
		return "", fmt.Errorf("mesh client is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	workspace = ResolveRepoRoot(workspace)
	if strings.TrimSpace(workspace) == "" {
		return "", fmt.Errorf("mesh delegation requires a workspace path")
	}

	commitHash, patch, err := collectLatestCommitPatch(ctx, workspace)
	if err != nil {
		return "", err
	}

	if strings.TrimSpace(branch) == "" || strings.EqualFold(strings.TrimSpace(branch), "HEAD") {
		branch = "mesh-graft-" + shortCommitHash(commitHash)
	}
	if strings.TrimSpace(commitMessage) == "" {
		commitMessage = "mesh graft delegation"
	}

	endpoint, err := normalizeGraftEndpoint(c.graftURL)
	if err != nil {
		return "", err
	}

	headers := http.Header{}
	headers.Set("Authorization", "Bearer "+strings.TrimSpace(c.token))

	conn, resp, err := c.dialer.DialContext(ctx, endpoint, headers)
	if err != nil {
		if resp != nil && resp.Body != nil {
			defer resp.Body.Close()
			body, _ := io.ReadAll(resp.Body)
			if len(body) > 0 {
				return "", fmt.Errorf("mesh graft websocket dial failed: %w (%s): %s", err, resp.Status, strings.TrimSpace(string(body)))
			}
			return "", fmt.Errorf("mesh graft websocket dial failed: %w (%s)", err, resp.Status)
		}
		return "", fmt.Errorf("mesh graft websocket dial failed: %w", err)
	}
	defer conn.Close()

	request := graftRequest{
		Type:          "graft-request",
		WorkspacePath: workspace,
		Branch:        branch,
		CommitMessage: commitMessage,
		CommitHash:    commitHash,
		SequencePath:  ".tendril/mesh-governance.yaml",
		Patch:         patch,
	}

	if err := conn.SetWriteDeadline(time.Now().Add(15 * time.Second)); err != nil {
		return "", fmt.Errorf("set graft write deadline: %w", err)
	}
	if err := conn.WriteJSON(request); err != nil {
		return "", fmt.Errorf("send graft request: %w", err)
	}

	for {
		var message graftMessage
		if err := conn.ReadJSON(&message); err != nil {
			return "", fmt.Errorf("read graft response: %w", err)
		}

		switch strings.ToLower(strings.TrimSpace(message.Type)) {
		case "graft-log":
			c.writeLog(message.Stream, message.Message)
		case "graft-result":
			if strings.EqualFold(strings.TrimSpace(message.Status), "complete") {
				if strings.TrimSpace(message.CommitHash) != "" {
					return strings.TrimSpace(message.CommitHash), nil
				}
				return commitHash, nil
			}
			if strings.TrimSpace(message.Error) != "" {
				return "", fmt.Errorf("mesh graft failed: %s", message.Error)
			}
			return "", fmt.Errorf("mesh graft failed")
		default:
			if strings.TrimSpace(message.Message) != "" {
				c.writeLog(message.Stream, message.Message)
			}
		}
	}
}

func (c *Client) writeLog(stream, message string) {
	w := c.logWriter
	if w == nil {
		w = os.Stderr
	}

	text := strings.TrimSpace(message)
	if text == "" {
		return
	}

	label := strings.TrimSpace(stream)
	if label == "" {
		label = "graft"
	}

	_, _ = fmt.Fprintf(w, "[mesh:%s] %s\n", label, text)
}

func normalizeGraftEndpoint(rawURL string) (string, error) {
	trimmed := strings.TrimSpace(rawURL)
	if trimmed == "" {
		return "", fmt.Errorf("mesh graft url is required")
	}

	parsed, err := url.Parse(trimmed)
	if err != nil {
		return "", fmt.Errorf("parse mesh graft url: %w", err)
	}

	switch parsed.Scheme {
	case "":
		parsed.Scheme = "ws"
	case "http":
		parsed.Scheme = "ws"
	case "https":
		parsed.Scheme = "wss"
	case "ws", "wss":
	default:
		return "", fmt.Errorf("unsupported mesh graft url scheme %q", parsed.Scheme)
	}

	basePath := strings.TrimSuffix(parsed.Path, "/")
	if !strings.HasSuffix(basePath, "/v1/mesh/graft") {
		parsed.Path = path.Join(basePath, "v1", "mesh", "graft")
	}
	if !strings.HasPrefix(parsed.Path, "/") {
		parsed.Path = "/" + parsed.Path
	}

	return parsed.String(), nil
}

func collectLatestCommitPatch(ctx context.Context, workspace string) (string, string, error) {
	commitHash, err := runGitOutput(ctx, workspace, "rev-parse", "HEAD")
	if err != nil {
		return "", "", err
	}

	args := []string{"diff-tree", "--root", "--binary", "--no-commit-id", "-p", "HEAD"}
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = workspace
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", "", fmt.Errorf("git %s failed: %w (output: %s)", strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}

	patch := strings.TrimSpace(string(output))
	if patch == "" {
		return "", "", fmt.Errorf("no patch available for commit %s", commitHash)
	}

	return commitHash, patch, nil
}

func runGitOutput(ctx context.Context, workspace string, args ...string) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = workspace
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s failed: %w (output: %s)", strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}

	return strings.TrimSpace(string(output)), nil
}

func shortCommitHash(commitHash string) string {
	trimmed := strings.TrimSpace(commitHash)
	if len(trimmed) <= 8 {
		return trimmed
	}
	return trimmed[:8]
}

func scanLines(r io.Reader, send func(string, string) error, stream string) error {
	scanner := bufio.NewScanner(r)
	buffer := make([]byte, 0, 64*1024)
	scanner.Buffer(buffer, 1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		if err := send(stream, line); err != nil {
			return err
		}
	}
	return scanner.Err()
}
