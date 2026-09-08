package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/opentendril/opentendril/cmd/stem/internal/conductor"
	"github.com/opentendril/opentendril/cmd/stem/internal/core"
)

// ---------------------------------------------------------------------------
// chatArgs holds the parsed pre-separator flags for `tendril chat`.
// ---------------------------------------------------------------------------

type chatArgs struct {
	substrate     string // explicit --substrate value; empty means auto-resolve
	maxIterations int    // 0 means omit from Seed spec
	timeout       int    // 0 means omit; seconds
	verifyArgv    []string
}

// parseChatArgs parses:
//
//	tendril chat [--substrate <n>] [--max-iterations N] [--timeout N] -- <verify argv...>
//
// Rules:
//   - bare "--" separator is required
//   - everything after "--" is the verifier argv (must be non-empty)
//   - "--ws" is explicitly rejected (it must never fall back to WS)
//   - unknown pre-separator flags are a local failure, not dispatched
func parseChatArgs(args []string) (chatArgs, error) {
	var (
		out    chatArgs
		sepIdx = -1
		wsFlag = false
	)

	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			sepIdx = i
			break
		}
		if arg == "--ws" {
			wsFlag = true
			continue
		}
		nextVal := func() (string, error) {
			if i+1 >= len(args) || args[i+1] == "--" {
				return "", fmt.Errorf("flag %s requires a value", arg)
			}
			i++
			return args[i], nil
		}
		switch arg {
		case "--substrate":
			v, err := nextVal()
			if err != nil {
				return chatArgs{}, err
			}
			out.substrate = v
		case "--max-iterations":
			v, err := nextVal()
			if err != nil {
				return chatArgs{}, err
			}
			n, convErr := strconv.Atoi(v)
			if convErr != nil {
				return chatArgs{}, fmt.Errorf("--max-iterations must be an integer: %w", convErr)
			}
			out.maxIterations = n
		case "--timeout":
			v, err := nextVal()
			if err != nil {
				return chatArgs{}, err
			}
			n, convErr := strconv.Atoi(v)
			if convErr != nil {
				return chatArgs{}, fmt.Errorf("--timeout must be an integer: %w", convErr)
			}
			out.timeout = n
		default:
			return chatArgs{}, fmt.Errorf("unknown flag %q (use -- to separate verifier argv)", arg)
		}
	}

	if wsFlag {
		return chatArgs{}, fmt.Errorf("--ws is not supported: tendril chat uses the canonical Seed/Phytomer lifecycle, not WebSocket execution")
	}

	if sepIdx == -1 {
		return chatArgs{}, fmt.Errorf("verifier argv requires a bare -- separator\n\nUsage: tendril chat [--substrate <name>] [--max-iterations N] [--timeout N] -- <verify argv...>")
	}

	verifyArgv := args[sepIdx+1:]
	if len(verifyArgv) == 0 {
		return chatArgs{}, fmt.Errorf("verifier argv after -- must be non-empty\n\nExample: tendril chat -- go test ./...")
	}
	out.verifyArgv = verifyArgv
	return out, nil
}

// ---------------------------------------------------------------------------
// resolveDirectChatSubstrate resolves the Substrate name to use for direct chat.
//
// If --substrate was explicit, use it directly (no implicit selection needed).
// If omitted, load the configured Substrates and apply the 0/1/N rule.
// ---------------------------------------------------------------------------

func resolveDirectChatSubstrate(explicit string) (string, error) {
	if explicit != "" {
		return explicit, nil
	}
	config, err := conductor.LoadSubstratesConfig("")
	if err != nil {
		return "", fmt.Errorf("load substrates config: %w", err)
	}
	if config == nil || len(config.Substrates) == 0 {
		return "", fmt.Errorf("no Substrates configured\n\nRun `tendril setup substrate` to configure one")
	}
	names := sortedSubstrateNames(config)
	if len(names) == 1 {
		return names[0], nil
	}
	// 2+ — fail with deterministic sorted list
	return "", fmt.Errorf("multiple Substrates configured; use --substrate to select one:\n  %s", strings.Join(names, "\n  "))
}

// sortedSubstrateNames returns the substrate names in deterministic sorted order.
// Exported as a testable helper.
func sortedSubstrateNames(config *conductor.SubstratesConfig) []string {
	if config == nil {
		return nil
	}
	names := make([]string, 0, len(config.Substrates))
	for name := range config.Substrates {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// ---------------------------------------------------------------------------
// newContinuationKey generates a cryptographically opaque idempotency key for
// one distinct interactive continuation. It is deterministic only in that the
// same call can be retried with the same key; it never derives the key from
// the intent text or timestamps alone.
// ---------------------------------------------------------------------------

func newContinuationKey() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate idempotency key: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

// randReader allows tests to replace rand.Read without a package-level var.
// We expose the key-generation path through newContinuationKey; tests that
// need deterministic keys inject via continuationKeySource.
var continuationKeySource = newContinuationKey

// ---------------------------------------------------------------------------
// chatState is the explicit terminal-safe state machine for direct chat.
// ---------------------------------------------------------------------------

type chatState int

const (
	chatStateIdle   chatState = iota // waiting for a goal
	chatStateActive                  // Seed dispatched; Phytomer active
)

// ---------------------------------------------------------------------------
// directChatSession holds the mutable state of one `tendril chat` run.
// It is not concurrency-safe on its own; the event loop serialises access.
// ---------------------------------------------------------------------------

type directChatSession struct {
	client     *localStemClient
	substrate  string
	verifyArgv []string
	maxIter    int
	timeout    int

	state      chatState
	handle     string
	phytomerID string
}

// dispatchSeed posts the first developer goal as a canonical Seed.
func (s *directChatSession) dispatchSeed(ctx context.Context, goal string) (SeedDispatchResult, error) {
	input := map[string]any{
		"substrate": s.substrate,
		"goal":      goal,
		"verify":    toAnySlice(s.verifyArgv),
		"origin":    "cli",
	}
	if s.maxIter > 0 {
		input["maxIterations"] = s.maxIter
	}
	if s.timeout > 0 {
		input["timeoutSeconds"] = s.timeout
	}
	return s.client.DispatchSeed(ctx, input)
}

// ---------------------------------------------------------------------------
// renderObservation prints safe observation state changes to stdout.
// It never prints raw goal text, continued intent, reasoning, or internal keys.
// ---------------------------------------------------------------------------

func renderObservation(prev, obs core.PhytomerObservation) {
	statusChanged := prev.Status != obs.Status
	iterChanged := prev.Iterations != obs.Iterations

	if statusChanged {
		fmt.Printf("Status:     %s\n", obs.Status)
	}
	if iterChanged && obs.Iterations > 0 {
		fmt.Printf("Iterations: %d\n", obs.Iterations)
	}

	// Sprout lifecycle changes.
	if len(obs.Sprouts) > len(prev.Sprouts) {
		for i := len(prev.Sprouts); i < len(obs.Sprouts); i++ {
			sp := obs.Sprouts[i]
			fmt.Printf("Sprout:     %s  status=%s", sp.RunID, sp.Status)
			if sp.Outcome != "" {
				fmt.Printf("  outcome=%s", sp.Outcome)
			}
			fmt.Println()
		}
	} else {
		// Update status of existing sprouts.
		for i, sp := range obs.Sprouts {
			if i < len(prev.Sprouts) && prev.Sprouts[i].Status != sp.Status {
				fmt.Printf("Sprout:     %s  status=%s", sp.RunID, sp.Status)
				if sp.Outcome != "" {
					fmt.Printf("  outcome=%s", sp.Outcome)
				}
				fmt.Println()
			}
		}
	}

	// Continuation delivery changes.
	prevConts := make(map[string]string, len(prev.Continuations))
	for _, c := range prev.Continuations {
		prevConts[c.ContinuationID] = c.DeliveryState
	}
	for _, c := range obs.Continuations {
		if prevConts[c.ContinuationID] != c.DeliveryState {
			fmt.Printf("Continuation: %s  sequence=%d  state=%s\n",
				c.ContinuationID, c.Sequence, c.DeliveryState)
		}
	}

	// Branch/commit appear only when real.
	if obs.Branch != "" && obs.Branch != prev.Branch {
		fmt.Printf("Branch:     %s\n", obs.Branch)
	}
	if obs.Commit != "" && obs.Commit != prev.Commit {
		fmt.Printf("Commit:     %s\n", obs.Commit)
	}
}

// renderTerminalSettlement prints the safe terminal summary.
func renderTerminalSettlement(obs core.PhytomerObservation) {
	fmt.Println()
	fmt.Printf("Status:     %s\n", obs.Status)
	fmt.Printf("Handle:     %s\n", obs.Handle)
	fmt.Printf("Phytomer:   %s\n", obs.PhytomerID)
	fmt.Printf("Iterations: %d\n", obs.Iterations)
	if obs.Branch != "" {
		fmt.Printf("Branch:     %s\n", obs.Branch)
	}
	if obs.Commit != "" {
		fmt.Printf("Commit:     %s\n", obs.Commit)
	}
	if obs.Status == core.SeedStatusSatisfied {
		fmt.Println("✅ Seed satisfied.")
	} else {
		fmt.Printf("⚠️  Seed ended with non-success status: %s\n", obs.Status)
	}
}

// ---------------------------------------------------------------------------
// runChatCmd is the entry point called from main.go.
// ---------------------------------------------------------------------------

func runChatCmd(ctx context.Context, args []string) {
	parsed, err := parseChatArgs(args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "tendril chat: %v\n", err)
		os.Exit(1)
	}

	substrate, err := resolveDirectChatSubstrate(parsed.substrate)
	if err != nil {
		fmt.Fprintf(os.Stderr, "tendril chat: %v\n", err)
		os.Exit(1)
	}

	sess := &directChatSession{
		client:     newLocalStemClient(),
		substrate:  substrate,
		verifyArgv: parsed.verifyArgv,
		maxIter:    parsed.maxIterations,
		timeout:    parsed.timeout,
	}

	fmt.Printf("🌱 OpenTendril direct chat  [substrate: %s  verify: %s]\n",
		substrate, strings.Join(parsed.verifyArgv, " "))
	fmt.Println("Type your coding goal and press Enter. Type 'exit' or '/exit' to quit.")
	fmt.Println()

	runChatLoop(ctx, sess, os.Stdin)
}

// runChatLoop drives the interactive lifecycle. It accepts a reader so tests
// can inject deterministic input without real stdin.
func runChatLoop(ctx context.Context, sess *directChatSession, input *os.File) {
	scanner := bufio.NewScanner(input)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		if line == "exit" || line == "/exit" {
			fmt.Println("Exiting.")
			return
		}

		switch sess.state {
		case chatStateIdle:
			handleIdleInput(ctx, sess, line)
		case chatStateActive:
			handleActiveInput(ctx, sess, line)
		}
	}
	if err := scanner.Err(); err != nil {
		fmt.Fprintf(os.Stderr, "stdin error: %v\n", err)
	}
}

// handleIdleInput dispatches the first developer goal as a new Seed.
func handleIdleInput(ctx context.Context, sess *directChatSession, goal string) {
	result, err := sess.dispatchSeed(ctx, goal)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Seed dispatch failed: %v\n", err)
		return
	}

	sess.handle = result.Handle
	sess.phytomerID = result.PhytomerID
	sess.state = chatStateActive

	fmt.Printf("\nHandle:   %s\n", result.Handle)
	fmt.Printf("Phytomer: %s\n", result.PhytomerID)
	fmt.Printf("Status:   %s\n", result.Status)
	fmt.Println()
	fmt.Println("Watching progress... (type continued intent at any time)")
	fmt.Println()

	// Watch the Phytomer until terminal settlement. Input after this returns
	// to the event loop; chatStateActive handles continuation.
	watchAndSettle(ctx, sess)
}

// watchAndSettle opens the watch stream and blocks until terminal observation
// or a transport error. On return the session is always in chatStateIdle.
func watchAndSettle(ctx context.Context, sess *directChatSession) {
	var prev core.PhytomerObservation
	var lastObs core.PhytomerObservation
	var terminal bool

	err := sess.client.WatchPhytomer(ctx, sess.phytomerID, func(obs core.PhytomerObservation) error {
		renderObservation(prev, obs)
		prev = obs
		lastObs = obs
		if core.SeedStatusIsTerminal(obs.Status) {
			terminal = true
			// Return nil so we drain any remaining frames before the server
			// closes the stream.
		}
		return nil
	})

	// A transport failure before terminal settlement is an observation failure,
	// not a success. It must not be treated as completion.
	if err != nil && !terminal {
		fmt.Fprintf(os.Stderr, "\nFailed to observe Phytomer %s: %v\n", sess.phytomerID, err)
		fmt.Fprintf(os.Stderr, "The Seed may still be running. Use `tendril session get %s` to check.\n", sess.phytomerID)
		// Reset to idle; the user can start a new interaction but this one is
		// no longer tracked.
		sess.state = chatStateIdle
		sess.handle = ""
		sess.phytomerID = ""
		return
	}

	if terminal {
		renderTerminalSettlement(lastObs)
	}

	// Always return to idle after watch ends (terminal or connection closed).
	sess.state = chatStateIdle
	sess.handle = ""
	sess.phytomerID = ""
}

// handleActiveInput posts continued intent to the active Phytomer. If the
// Phytomer has become terminal, it reports the rejection without dispatching
// a new Seed and without reinterpreting the input as a fresh goal.
func handleActiveInput(ctx context.Context, sess *directChatSession, intent string) {
	key, err := continuationKeySource()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to generate idempotency key: %v\n", err)
		return
	}

	result, err := sess.client.ContinuePhytomer(ctx, sess.phytomerID, intent, key)
	if err != nil {
		// Terminal-race: the Phytomer became terminal between the user typing
		// and the POST arriving. Report it honestly and do not dispatch a new Seed.
		fmt.Fprintf(os.Stderr, "Continuation rejected: %v\n", err)
		fmt.Fprintln(os.Stderr, "The Phytomer may have reached a terminal state. Check status with `tendril session get`.")
		return
	}

	// Print safe acceptance lifecycle. Never print the idempotency key.
	fmt.Printf("Continuation: %s\n", result.ContinuationID)
	fmt.Printf("Sequence:     %d\n", result.Sequence)
	fmt.Printf("State:        %s\n", result.DeliveryState)
}

// ---------------------------------------------------------------------------
// toAnySlice converts []string → []any (required by DispatchSeed input map).
// ---------------------------------------------------------------------------

func toAnySlice(ss []string) []any {
	out := make([]any, len(ss))
	for i, s := range ss {
		out[i] = s
	}
	return out
}
