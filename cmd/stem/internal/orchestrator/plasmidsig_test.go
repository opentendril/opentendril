package orchestrator

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSignAndVerifyPlasmid(t *testing.T) {
	path := filepath.Join(t.TempDir(), "plasmid.md")
	if err := os.WriteFile(path, []byte("# Plasmid\ntrusted context\n"), 0o644); err != nil {
		t.Fatalf("write plasmid: %v", err)
	}

	key := []byte("0123456789abcdef0123456789abcdef")
	sig, err := SignPlasmid(path, key)
	if err != nil {
		t.Fatalf("sign plasmid: %v", err)
	}
	if err := WritePlasmidSignature(path, sig); err != nil {
		t.Fatalf("write plasmid signature: %v", err)
	}

	if err := VerifyPlasmidSignature(path, key); err != nil {
		t.Fatalf("verify plasmid signature: %v", err)
	}
}

func TestVerifyDetectsModifiedContent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "plasmid.md")
	if err := os.WriteFile(path, []byte("# Plasmid\ntrusted context\n"), 0o644); err != nil {
		t.Fatalf("write plasmid: %v", err)
	}

	key := []byte("0123456789abcdef0123456789abcdef")
	sig, err := SignPlasmid(path, key)
	if err != nil {
		t.Fatalf("sign plasmid: %v", err)
	}
	if err := WritePlasmidSignature(path, sig); err != nil {
		t.Fatalf("write plasmid signature: %v", err)
	}
	if err := os.WriteFile(path, []byte("# Plasmid\nmodified context\n"), 0o644); err != nil {
		t.Fatalf("modify plasmid: %v", err)
	}

	if err := VerifyPlasmidSignature(path, key); err == nil {
		t.Fatal("expected modified plasmid content to fail verification")
	}
}

func TestVerifyDetectsMissingSig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "plasmid.md")
	if err := os.WriteFile(path, []byte("# Plasmid\ntrusted context\n"), 0o644); err != nil {
		t.Fatalf("write plasmid: %v", err)
	}

	key := []byte("0123456789abcdef0123456789abcdef")
	if err := VerifyPlasmidSignature(path, key); err == nil {
		t.Fatal("expected missing signature to fail verification")
	}
}
