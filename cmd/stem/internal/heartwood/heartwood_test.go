package heartwood

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCipherRoundTrip(t *testing.T) {
	key := make([]byte, 32)
	rand.Read(key)

	m := Material{Key: key, LegacyKey: nil, Source: KeySourceFile}
	c, err := NewCipher(m)
	if err != nil {
		t.Fatalf("NewCipher: %v", err)
	}

	plaintext := "secret message"
	aad := []byte("context")

	ciphertext, err := c.Encrypt(plaintext, aad)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}

	if !strings.HasPrefix(ciphertext, Prefix) {
		t.Errorf("ciphertext missing prefix: %q", ciphertext)
	}

	decrypted, err := c.Decrypt(ciphertext, aad, LegacyCiphertext)
	if err != nil {
		t.Fatalf("Decrypt: %v", err)
	}
	if decrypted != plaintext {
		t.Errorf("got %q, want %q", decrypted, plaintext)
	}

	// Wrong AAD should fail
	_, err = c.Decrypt(ciphertext, []byte("wrong"), LegacyCiphertext)
	if err == nil {
		t.Error("expected error with wrong AAD, got nil")
	}

	// Mismatched KeyID
	otherKey := make([]byte, 32)
	rand.Read(otherKey)
	otherCipher, _ := NewCipher(Material{Key: otherKey})
	_, err = otherCipher.Decrypt(ciphertext, aad, LegacyCiphertext)
	if err == nil {
		t.Error("expected error with mismatched keyID, got nil")
	}
}

func TestLegacyPlaintext(t *testing.T) {
	key := make([]byte, 32)
	m := Material{Key: key}
	c, _ := NewCipher(m)

	plaintext := "just a plain string"
	decrypted, err := c.Decrypt(plaintext, nil, LegacyPlaintext)
	if err != nil {
		t.Fatalf("Decrypt LegacyPlaintext: %v", err)
	}
	if decrypted != plaintext {
		t.Errorf("got %q, want %q", decrypted, plaintext)
	}
}

func TestLegacyCiphertext(t *testing.T) {
	key := make([]byte, 32)
	rand.Read(key)

	// Encrypt using the old rhizome method
	block, _ := aes.NewCipher(key)
	aead, _ := cipher.NewGCM(block)
	nonce := make([]byte, aead.NonceSize())
	io.ReadFull(rand.Reader, nonce)

	plaintext := "legacy secret"
	sealed := aead.Seal(nonce, nonce, []byte(plaintext), nil)
	oldCiphertext := base64.RawStdEncoding.EncodeToString(sealed)

	m := Material{Key: key, LegacyKey: nil}
	c, _ := NewCipher(m)

	decrypted, err := c.Decrypt(oldCiphertext, nil, LegacyCiphertext)
	if err != nil {
		t.Fatalf("Decrypt LegacyCiphertext: %v", err)
	}
	if decrypted != plaintext {
		t.Errorf("got %q, want %q", decrypted, plaintext)
	}
}

func TestLegacyCiphertextWithLegacyKey(t *testing.T) {
	legacyKey := make([]byte, 32)
	rand.Read(legacyKey)

	// Encrypt using the old rhizome method with legacy key
	block, _ := aes.NewCipher(legacyKey)
	aead, _ := cipher.NewGCM(block)
	nonce := make([]byte, aead.NonceSize())
	io.ReadFull(rand.Reader, nonce)

	plaintext := "legacy secret with legacy key"
	sealed := aead.Seal(nonce, nonce, []byte(plaintext), nil)
	oldCiphertext := base64.RawStdEncoding.EncodeToString(sealed)

	newKey := make([]byte, 32)
	rand.Read(newKey)

	m := Material{Key: newKey, LegacyKey: legacyKey}
	c, _ := NewCipher(m)

	decrypted, err := c.Decrypt(oldCiphertext, nil, LegacyCiphertext)
	if err != nil {
		t.Fatalf("Decrypt LegacyCiphertext: %v", err)
	}
	if decrypted != plaintext {
		t.Errorf("got %q, want %q", decrypted, plaintext)
	}
}

func TestResolveKeyTier2(t *testing.T) {
	os.Setenv(KeyEnvVar, "my-secret-passphrase")
	defer os.Unsetenv(KeyEnvVar)

	keyPath := filepath.Join(t.TempDir(), "rhizome.key")
	m, err := ResolveKey(keyPath)
	if err != nil {
		t.Fatalf("ResolveKey: %v", err)
	}

	if m.Source != KeySourceEnv {
		t.Errorf("expected KeySourceEnv, got %v", m.Source)
	}
	if m.LegacyKey == nil {
		t.Error("expected non-nil LegacyKey")
	}

	if _, err := os.Stat(keyPath); !os.IsNotExist(err) {
		t.Error("expected key file to not exist")
	}
}

func TestResolveKeyTier1(t *testing.T) {
	os.Unsetenv(KeyEnvVar)

	keyPath := filepath.Join(t.TempDir(), "rhizome.key")

	// First call should generate the file
	m1, err := ResolveKey(keyPath)
	if err != nil {
		t.Fatalf("ResolveKey 1: %v", err)
	}
	if m1.Source != KeySourceFile {
		t.Errorf("expected KeySourceFile, got %v", m1.Source)
	}
	if m1.LegacyKey != nil {
		t.Error("expected nil LegacyKey")
	}

	// Verify file permissions 0600
	info, err := os.Stat(keyPath)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("expected 0600, got %v", info.Mode().Perm())
	}

	// Second call should return the same key
	m2, err := ResolveKey(keyPath)
	if err != nil {
		t.Fatalf("ResolveKey 2: %v", err)
	}
	if m2.Source != KeySourceFile {
		t.Errorf("expected KeySourceFile, got %v", m2.Source)
	}
	if string(m1.Key) != string(m2.Key) {
		t.Error("keys do not match")
	}
}
