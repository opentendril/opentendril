package conductor

import (
	"crypto/ed25519"
	"crypto/rand"
	"os"
	"path/filepath"
	"testing"
)

func TestSignAndVerifyPlasmid(t *testing.T) {
	path := filepath.Join(t.TempDir(), "plasmid.md")
	if err := os.WriteFile(path, []byte("# Plasmid\ntrusted context\n"), 0o644); err != nil {
		t.Fatalf("write plasmid: %v", err)
	}

	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate keypair: %v", err)
	}

	sig, err := SignPlasmid(path, privateKey)
	if err != nil {
		t.Fatalf("sign plasmid: %v", err)
	}
	if err := WritePlasmidSignature(path, sig); err != nil {
		t.Fatalf("write plasmid signature: %v", err)
	}

	if err := VerifyPlasmidSignature(path, publicKey); err != nil {
		t.Fatalf("verify plasmid signature: %v", err)
	}
}

func TestVerifyDetectsModifiedContent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "plasmid.md")
	if err := os.WriteFile(path, []byte("# Plasmid\ntrusted context\n"), 0o644); err != nil {
		t.Fatalf("write plasmid: %v", err)
	}

	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate keypair: %v", err)
	}

	sig, err := SignPlasmid(path, privateKey)
	if err != nil {
		t.Fatalf("sign plasmid: %v", err)
	}
	if err := WritePlasmidSignature(path, sig); err != nil {
		t.Fatalf("write plasmid signature: %v", err)
	}
	if err := os.WriteFile(path, []byte("# Plasmid\nmodified context\n"), 0o644); err != nil {
		t.Fatalf("modify plasmid: %v", err)
	}

	if err := VerifyPlasmidSignature(path, publicKey); err == nil {
		t.Fatal("expected modified plasmid content to fail verification")
	}
}

func TestVerifyDetectsMissingSig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "plasmid.md")
	if err := os.WriteFile(path, []byte("# Plasmid\ntrusted context\n"), 0o644); err != nil {
		t.Fatalf("write plasmid: %v", err)
	}

	publicKey, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate keypair: %v", err)
	}

	if err := VerifyPlasmidSignature(path, publicKey); err == nil {
		t.Fatal("expected missing signature to fail verification")
	}
}
