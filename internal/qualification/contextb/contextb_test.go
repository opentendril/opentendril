package contextb

import "testing"

func TestLabel(t *testing.T) {
    expected := "context-b"
    if got := Label(); got != expected {
        t.Fatalf("Label() = %s, want %s", got, expected)
    }
}
