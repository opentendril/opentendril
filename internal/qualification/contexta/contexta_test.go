package contexta

import "testing"

func TestLabel(t *testing.T) {
    expected := "context-a"
    if got := Label(); got != expected {
        t.Fatalf("Label() = %s, want %s", got, expected)
    }
}
