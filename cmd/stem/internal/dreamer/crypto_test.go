package dreamer

import (
	"strings"
	"testing"
)

func TestEncryptorRoundTrip(t *testing.T) {
	encryptor, err := NewEncryptor([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatalf("NewEncryptor returned error: %v", err)
	}

	plaintext := "func ProprietarySecret() string"
	ciphertext, err := encryptor.EncryptString(plaintext)
	if err != nil {
		t.Fatalf("EncryptString returned error: %v", err)
	}
	if ciphertext == plaintext || strings.Contains(ciphertext, "ProprietarySecret") {
		t.Fatalf("ciphertext leaked plaintext: %q", ciphertext)
	}

	decrypted, err := encryptor.DecryptString(ciphertext)
	if err != nil {
		t.Fatalf("DecryptString returned error: %v", err)
	}
	if decrypted != plaintext {
		t.Fatalf("decrypted text mismatch: got %q want %q", decrypted, plaintext)
	}
}
