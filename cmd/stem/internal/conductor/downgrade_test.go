package conductor

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"

	"github.com/opentendril/opentendril/cmd/stem/internal/eventbus"
	"github.com/opentendril/opentendril/roots/llm"
)

type downgradeFakeLLM struct {
	fakeLLM
}

func (f *downgradeFakeLLM) ToolDefinitionsCapable() bool { return true }

func (f *downgradeFakeLLM) CallWithTools(ctx context.Context, messages []llm.Message, tools []llm.ToolDefinition, observer llm.ToolDowngradeObserver, tokenChan chan<- string) (llm.Result, error) {
	observer()
	if tokenChan != nil {
		close(tokenChan)
	}
	return llm.Result{Text: "done"}, nil
}

func TestDowngradePublishesToStderr(t *testing.T) {
	workspace := t.TempDir()
	client := &downgradeFakeLLM{}
	session := &fakeSession{tools: []ToolDefinition{{Name: "readFile"}}}

	sprout, err := newSprout(context.Background(), workspace, workspace, "workspace-Sprout", client, session, nil, "", "")
	if err != nil {
		t.Fatalf("newSprout: %v", err)
	}

	// Capture stderr
	oldStderr := os.Stderr
	r, w, _ := os.Pipe()
	os.Stderr = w

	sprout.Run(context.Background(), "test")

	w.Close()
	os.Stderr = oldStderr

	var buf bytes.Buffer
	buf.ReadFrom(r)
	stderrOutput := buf.String()

	if !strings.Contains(stderrOutput, "rejected tool definitions") {
		t.Errorf("expected stderr to contain downgrade warning, got: %s", stderrOutput)
	}
}

func TestDowngradePublishesToEventBus(t *testing.T) {
	workspace := t.TempDir()
	client := &downgradeFakeLLM{}
	session := &fakeSession{tools: []ToolDefinition{{Name: "readFile"}}}
	bus := eventbus.New()
	defer bus.Shutdown()

	sprout, err := newSprout(context.Background(), workspace, workspace, "workspace-Sprout", client, session, bus, "step-1", "session-1")
	if err != nil {
		t.Fatalf("newSprout: %v", err)
	}

	sprout.Run(context.Background(), "test")

	var downgradedEvents []eventbus.Event
	for _, ev := range bus.History(50) {
		if ev.Type == eventbus.EventSproutDowngraded {
			downgradedEvents = append(downgradedEvents, ev)
		}
	}

	if len(downgradedEvents) != 1 {
		t.Fatalf("expected exactly 1 EventSproutDowngraded, got %d", len(downgradedEvents))
	}

	if downgradedEvents[0].SessionID != "session-1" {
		t.Errorf("expected sessionID to be session-1, got %s", downgradedEvents[0].SessionID)
	}
}

func TestDowngradeSetsProtocolOnReport(t *testing.T) {
	workspace := t.TempDir()
	client := &downgradeFakeLLM{}
	session := &fakeSession{tools: []ToolDefinition{{Name: "readFile"}}}

	sprout, err := newSprout(context.Background(), workspace, workspace, "workspace-Sprout", client, session, nil, "", "")
	if err != nil {
		t.Fatalf("newSprout: %v", err)
	}

	res, err := sprout.Run(context.Background(), "test")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if res.Protocol != "prose" {
		t.Errorf("expected protocol to be prose, got %s", res.Protocol)
	}
}

type reteachFakeLLM struct {
	fakeLLM
	reteachVerified bool
}

func (f *reteachFakeLLM) ToolDefinitionsCapable() bool { return true }

func (f *reteachFakeLLM) CallWithTools(ctx context.Context, messages []llm.Message, tools []llm.ToolDefinition, observer llm.ToolDowngradeObserver, tokenChan chan<- string) (llm.Result, error) {
	observer()
	// The real client's retry uses the exact messages slice passed into CallWithTools.
	// Since the observer mutates a.messages[0].Content, we verify that this mutation
	// is visible here via the aliased backing array.
	if len(messages) > 0 && strings.Contains(messages[0].Content, "Protocol Rules:") {
		f.reteachVerified = true
	}
	if tokenChan != nil {
		close(tokenChan)
	}
	return llm.Result{Text: "done"}, nil
}

func TestDowngradeReteachesProseProtocol(t *testing.T) {
	workspace := t.TempDir()
	client := &reteachFakeLLM{}
	session := &fakeSession{tools: []ToolDefinition{{Name: "readFile"}}}

	sprout, err := newSprout(context.Background(), workspace, workspace, "workspace-Sprout", client, session, nil, "", "")
	if err != nil {
		t.Fatalf("newSprout: %v", err)
	}

	_, err = sprout.Run(context.Background(), "test")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if !client.reteachVerified {
		t.Errorf("expected the retry request (messages slice in CallWithTools) to contain prose protocol rules in the system prompt after observer fired")
	}
}
