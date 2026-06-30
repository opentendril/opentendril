package dreamer

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"io"
)

// Encryptor handles application-level encryption for repository stubs before
// they reach SQLite.
type Encryptor struct {
	aead cipher.AEAD
}

func NewEncryptor(key []byte) (*Encryptor, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("create AES cipher: %w", err)
	}

	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("create AES-GCM cipher: %w", err)
	}

	return &Encryptor{aead: aead}, nil
}

func (e *Encryptor) EncryptString(plaintext string) (string, error) {
	if e == nil || e.aead == nil {
		return "", fmt.Errorf("encryptor is not configured")
	}

	nonce := make([]byte, e.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("create nonce: %w", err)
	}

	sealed := e.aead.Seal(nonce, nonce, []byte(plaintext), nil)
	return base64.RawStdEncoding.EncodeToString(sealed), nil
}

func (e *Encryptor) DecryptString(ciphertext string) (string, error) {
	if e == nil || e.aead == nil {
		return "", fmt.Errorf("encryptor is not configured")
	}

	sealed, err := base64.RawStdEncoding.DecodeString(ciphertext)
	if err != nil {
		return "", fmt.Errorf("decode ciphertext: %w", err)
	}
	if len(sealed) < e.aead.NonceSize() {
		return "", fmt.Errorf("ciphertext is shorter than nonce")
	}

	nonce := sealed[:e.aead.NonceSize()]
	body := sealed[e.aead.NonceSize():]
	plaintext, err := e.aead.Open(nil, nonce, body, nil)
	if err != nil {
		return "", fmt.Errorf("decrypt ciphertext: %w", err)
	}

	return string(plaintext), nil
}
