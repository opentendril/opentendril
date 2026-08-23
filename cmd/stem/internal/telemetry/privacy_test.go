package telemetry

import (
	"testing"

	"github.com/opentendril/opentendril/cmd/stem/internal/eventbus"
)

func TestStripPrivateReasoning(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "no thought block",
			input:    "just normal text",
			expected: "just normal text",
		},
		{
			name:     "thought block only",
			input:    "<thought>thinking...</thought>",
			expected: "",
		},
		{
			name:     "text before and after",
			input:    "Before <thought>thinking...</thought> After",
			expected: "Before  After",
		},
		{
			name:     "multiple thought blocks",
			input:    "A <thought>1</thought> B <thought>2</thought> C",
			expected: "A  B  C",
		},
		{
			name:     "unclosed thought block",
			input:    "Start <thought>never ends",
			expected: "Start",
		},
		{
			name:     "orphan closing tag",
			input:    "private</thought>public",
			expected: "public",
		},
		{
			name:     "inverted tags",
			input:    "private</thought>public<thought>secret",
			expected: "public",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := StripPrivateReasoning(tc.input)
			if result != tc.expected {
				t.Errorf("expected %q, got %q", tc.expected, result)
			}
		})
	}
}

func TestSanitizeSproutTranscript(t *testing.T) {
	transcript := `[system]
You are a bot.
[user]
Do something.
[assistant]
<thought>planning...</thought>Here is the result.`

	expected := `[system]
You are a bot.

[user]
Do something.

[assistant]
Here is the result.`

	result := SanitizeSproutTranscript(transcript)
	if result != expected {
		t.Errorf("expected:\n%s\n\ngot:\n%s", expected, result)
	}

	unstructured := `Here is some <thought>private</thought> text without role markers.`
	expectedUnstructured := `Here is some  text without role markers.`
	if got := SanitizeSproutTranscript(unstructured); got != expectedUnstructured {
		t.Errorf("unstructured expected %q, got %q", expectedUnstructured, got)
	}
}

func TestSanitizeObservationEvent(t *testing.T) {
	e := eventbus.Event{
		Type: eventbus.EventStreamToken,
		Data: map[string]interface{}{
			"token": "a",
			"other": "b",
		},
	}

	s := SanitizeObservationEvent(e)
	if _, ok := s.Data["token"]; ok {
		t.Errorf("expected token to be removed")
	}
	if s.Data["other"] != "b" {
		t.Errorf("expected other to be kept")
	}

	e2 := eventbus.Event{
		Type: "thought-branch",
		Data: map[string]interface{}{
			"thought": "abc",
			"foo":     "bar",
		},
	}
	s2 := SanitizeObservationEvent(e2)
	if _, ok := s2.Data["thought"]; ok {
		t.Errorf("expected thought to be removed")
	}
}
