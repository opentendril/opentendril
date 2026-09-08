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
	"sync/atomic"

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

type chatState = int32

const (
	chatStateIdle            chatState = iota // waiting for a goal
	chatStateActive                           // Seed dispatched; Phytomer active
	chatStateObservationLost                  // watch ended before terminal; identity preserved
)

// ---------------------------------------------------------------------------
// directChatSession holds the mutable state of one `tendril chat` run.
//
// The event loop goroutine is the sole writer of the mutable fields; it never
// needs a lock for its own accesses. The state field uses atomic load/store so
// that test goroutines (and future read-only observers) can safely sample it
// without introducing a data race.
// ---------------------------------------------------------------------------

type directChatSession struct {
	client     *localStemClient
	substrate  string
	verifyArgv []string
	maxIter    int
	timeout    int

	state      atomic.Int32 // chatState constants; use loadState/storeState
	handle     string
	phytomerID string
}

// loadState returns the current chatState with acquire semantics.
func (s *directChatSession) loadState() chatState { return s.state.Load() }

// storeState writes the new chatState with release semantics.
func (s *directChatSession) storeState(st chatState) { s.state.Store(st) }

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
// Event types for the chat event loop.
// ---------------------------------------------------------------------------

// chatInputEvent carries a line typed by the developer.
type chatInputEvent struct {
	line string
}

// chatObsEvent carries the result of one WatchPhytomer call.
// lastObs is the final observation seen (zero value if none).
// terminal is true when a terminal observation was received before the stream closed.
// err is non-nil when the stream closed with a transport failure.
type chatObsEvent struct {
	lastObs  core.PhytomerObservation
	terminal bool
	err      error
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

// runChatLoop drives the interactive lifecycle using a real concurrent event
// loop. It accepts a reader so tests can inject deterministic input without
// real stdin.
//
// Architecture:
//   - A stdin-reader goroutine sends non-empty non-exit lines on inputCh.
//   - When a Seed is accepted, a watch goroutine sends one chatObsEvent on obsCh.
//   - The event loop (this goroutine) is the sole owner of *directChatSession
//     state; it processes events serially so no lock is needed.
func runChatLoop(ctx context.Context, sess *directChatSession, input *os.File) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// inputCh carries lines from stdin. Closed by the reader goroutine on EOF.
	inputCh := make(chan string)
	// obsCh carries the result of exactly one WatchPhytomer call. Buffered so
	// the watch goroutine never blocks even if the event loop is busy.
	obsCh := make(chan chatObsEvent, 1)

	// Stdin reader goroutine.
	go func() {
		scanner := bufio.NewScanner(input)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line == "" {
				continue
			}
			select {
			case inputCh <- line:
			case <-ctx.Done():
				return
			}
		}
		if err := scanner.Err(); err != nil {
			fmt.Fprintf(os.Stderr, "stdin error: %v\n", err)
		}
		close(inputCh)
	}()

	// watchActive tracks whether a watch goroutine is running.
	// Only one watch is permitted at a time.
	watchActive := false

	startWatch := func(phytomerID string) {
		watchActive = true
		go func() {
			var lastObs core.PhytomerObservation
			var prev core.PhytomerObservation
			var terminal bool

			err := sess.client.WatchPhytomer(ctx, phytomerID, func(obs core.PhytomerObservation) error {
				renderObservation(prev, obs)
				prev = obs
				lastObs = obs
				if core.SeedStatusIsTerminal(obs.Status) {
					terminal = true
				}
				return nil
			})

			obsCh <- chatObsEvent{lastObs: lastObs, terminal: terminal, err: err}
		}()
	}

	for {
		select {
		case <-ctx.Done():
			return

		case line, ok := <-inputCh:
			if !ok {
				// stdin closed (EOF). If a watch is still running, wait for its
				// result so that terminal settlement can complete before we return.
				if watchActive {
					ev := <-obsCh
					handleObsEvent(sess, ev)
				}
				return
			}
			if line == "exit" || line == "/exit" {
				fmt.Println("Exiting.")
				return
			}
			handleLoopInput(ctx, sess, line, &watchActive, startWatch)

		case ev := <-obsCh:
			watchActive = false
			handleObsEvent(sess, ev)
		}
	}
}

// handleLoopInput is called by the event loop for each developer input line.
// It owns the state transition from Idle→Active (Seed dispatch + watch start)
// and the Active/ObservationLost continuation path.
func handleLoopInput(
	ctx context.Context,
	sess *directChatSession,
	line string,
	watchActive *bool,
	startWatch func(string),
) {
	switch sess.loadState() {
	case chatStateIdle:
		result, err := sess.dispatchSeed(ctx, line)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Seed dispatch failed: %v\n", err)
			return
		}

		sess.handle = result.Handle
		sess.phytomerID = result.PhytomerID
		sess.storeState(chatStateActive)

		fmt.Printf("\nHandle:   %s\n", result.Handle)
		fmt.Printf("Phytomer: %s\n", result.PhytomerID)
		fmt.Printf("Status:   %s\n", result.Status)
		fmt.Println()
		fmt.Println("Watching progress... (type continued intent at any time)")
		fmt.Println()

		if !*watchActive {
			startWatch(sess.phytomerID)
		}

	case chatStateActive:
		doActiveInput(ctx, sess, line)

	case chatStateObservationLost:
		// Observation was lost before terminal settlement. Refuse a replacement Seed.
		// The developer may type "exit" to leave; any other line reports the fenced state.
		fmt.Fprintln(os.Stderr, "⚠️  Observation was lost for Phytomer "+sess.phytomerID+": terminal state is unknown.")
		fmt.Fprintln(os.Stderr, "The Seed may still be running. Use `tendril session get` to check status.")
		fmt.Fprintln(os.Stderr, "Type 'exit' or '/exit' to leave without starting a replacement Seed.")
	}
}

// handleObsEvent processes the result of a completed WatchPhytomer call.
// It is always called from the event loop goroutine.
func handleObsEvent(sess *directChatSession, ev chatObsEvent) {
	if ev.terminal {
		renderTerminalSettlement(ev.lastObs)
		// Terminal observation is the only normal transition back to Idle.
		sess.handle = ""
		sess.phytomerID = ""
		sess.storeState(chatStateIdle)
		return
	}

	// Watch ended without a terminal observation — either a transport error
	// or a clean premature EOF. Preserve identity; do not permit a new Seed.
	phytomerID := sess.phytomerID
	if ev.err != nil {
		fmt.Fprintf(os.Stderr, "\nFailed to observe Phytomer %s: %v\n", phytomerID, ev.err)
	} else {
		fmt.Fprintf(os.Stderr, "\nObservation stream for Phytomer %s closed before terminal state was received.\n", phytomerID)
	}
	fmt.Fprintf(os.Stderr, "Terminal state is unknown. The Seed may still be running.\n")
	fmt.Fprintf(os.Stderr, "Use `tendril session get %s` to check.\n", phytomerID)

	// Transition to observation-lost: handle and phytomerID are retained;
	// no new Seed may be started in this run.
	sess.storeState(chatStateObservationLost)
	// handle and phytomerID are deliberately preserved.
}

// doActiveInput posts continued intent to the active Phytomer. If the
// Phytomer has become terminal, it reports the rejection without dispatching
// a new Seed and without reinterpreting the input as a fresh goal.
func doActiveInput(ctx context.Context, sess *directChatSession, intent string) {
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

// handleActiveInput is retained as a focused helper for existing unit tests
// that call it directly to verify continuation semantics.
func handleActiveInput(ctx context.Context, sess *directChatSession, intent string) {
	doActiveInput(ctx, sess, intent)
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
