package healthmon

import (
	"testing"
)

func TestParseVMStatAvailableKB(t *testing.T) {
	t.Run("valid input", func(t *testing.T) {
		input := `Mach Virtual Memory Statistics: (page size of 16384 bytes)
Pages free:                               58421.
Pages active:                            412932.
Pages inactive:                          198765.
Pages speculative:                        12043.
Pages throttled:                              0.
Pages wired down:                        245123.
Pages purgeable:                           5432.`
		got, err := parseVMStatAvailableKB(input, 16384)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		expected := uint64(58421+198765) * 16384 / 1024
		if got != expected {
			t.Errorf("got %d, want %d", got, expected)
		}
	})

	t.Run("missing Pages free", func(t *testing.T) {
		input := `Mach Virtual Memory Statistics: (page size of 16384 bytes)
Pages active:                            412932.
Pages inactive:                          198765.`
		_, err := parseVMStatAvailableKB(input, 16384)
		if err == nil {
			t.Error("expected error for missing Pages free line, got nil")
		}
	})

	t.Run("missing Pages inactive", func(t *testing.T) {
		input := `Mach Virtual Memory Statistics: (page size of 16384 bytes)
Pages free:                               58421.
Pages active:                            412932.`
		_, err := parseVMStatAvailableKB(input, 16384)
		if err == nil {
			t.Error("expected error for missing Pages inactive line, got nil")
		}
	})

	t.Run("extra whitespace and trailing dot variations", func(t *testing.T) {
		input := "Pages free:  \t \t 100 .\nPages inactive: \t 200 \t"
		got, err := parseVMStatAvailableKB(input, 4096)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		expected := uint64(100+200) * 4096 / 1024
		if got != expected {
			t.Errorf("got %d, want %d", got, expected)
		}
	})
}
