package heartwood

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Prefix is the literal prefix for the new versioned ciphertext format.
const Prefix = "tnd:atrest:1:"

// KeyEnvVar is the environment variable that operators can use to supply a key.
const KeyEnvVar = "OPEN_TENDRIL_INDEX_KEY"

// KeySource represents the source of the material key.
type KeySource int

const (
	KeySourceFile KeySource = iota // Tier 1: auto-generated co-located key
	KeySourceEnv                   // Tier 2: operator-supplied, never persisted
)

// Material holds the key material for encryption and decryption.
type Material struct {
	Key       []byte // 32-byte AES-256 key for new writes / new-format reads
	LegacyKey []byte // key that pre-existing ciphertext may be under; nil when == Key
	Source    KeySource
}

// ResolveKey resolves the encryption key using a two-tier resolution model.
func ResolveKey(keyFilePath string) (Material, error) {
	if secret := os.Getenv(KeyEnvVar); secret != "" {
		key, err := hkdf.Key(sha256.New, []byte(secret), nil, "opentendril-heartwood-atrest-v1", 32)
		if err != nil {
			return Material{}, fmt.Errorf("derive env key via hkdf: %w", err)
		}

		legacy := make([]byte, 32)
		copy(legacy, []byte(secret))

		return Material{
			Key:       key,
			LegacyKey: legacy,
			Source:    KeySourceEnv,
		}, nil
	}

	content, err := os.ReadFile(keyFilePath)
	if err == nil && len(content) == 32 {
		return Material{
			Key:       content,
			LegacyKey: nil,
			Source:    KeySourceFile,
		}, nil
	}

	key := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		return Material{}, fmt.Errorf("generate random key: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(keyFilePath), 0o700); err != nil {
		return Material{}, fmt.Errorf("create key directory: %w", err)
	}

	if err := os.WriteFile(keyFilePath, key, 0o600); err != nil {
		return Material{}, fmt.Errorf("save generated key: %w", err)
	}

	return Material{
		Key:       key,
		LegacyKey: nil,
		Source:    KeySourceFile,
	}, nil
}

// LegacyKind represents how an unprefixed stored value should be treated.
type LegacyKind int

const (
	LegacyCiphertext LegacyKind = iota // an unprefixed stored value is AES-GCM ciphertext (nil AAD)
	LegacyPlaintext                    // an unprefixed stored value is plaintext
)

// Cipher provides AEAD encryption and decryption of strings.
type Cipher struct {
	aead       cipher.AEAD
	legacyAEAD cipher.AEAD
	keyID      string
}

// NewCipher creates a new Cipher using the provided Key material.
func NewCipher(m Material) (*Cipher, error) {
	if len(m.Key) != 32 {
		return nil, fmt.Errorf("invalid key length: must be 32 bytes")
	}

	block, err := aes.NewCipher(m.Key)
	if err != nil {
		return nil, fmt.Errorf("create AES cipher: %w", err)
	}

	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("create AES-GCM cipher: %w", err)
	}

	hash := sha256.Sum256(m.Key)
	keyID := hex.EncodeToString(hash[:])[:16]

	var legacy cipher.AEAD
	if m.LegacyKey != nil {
		if len(m.LegacyKey) != 32 {
			return nil, fmt.Errorf("invalid legacy key length: must be 32 bytes")
		}
		legacyBlock, err := aes.NewCipher(m.LegacyKey)
		if err != nil {
			return nil, fmt.Errorf("create legacy AES cipher: %w", err)
		}
		legacy, err = cipher.NewGCM(legacyBlock)
		if err != nil {
			return nil, fmt.Errorf("create legacy AES-GCM cipher: %w", err)
		}
	}

	return &Cipher{
		aead:       aead,
		legacyAEAD: legacy,
		keyID:      keyID,
	}, nil
}

// Encrypt always writes the new versioned format. aad may be nil.
func (c *Cipher) Encrypt(plaintext string, aad []byte) (string, error) {
	if c == nil || c.aead == nil {
		return "", fmt.Errorf("cipher is not configured")
	}

	nonce := make([]byte, c.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("create nonce: %w", err)
	}

	sealed := c.aead.Seal(nonce, nonce, []byte(plaintext), aad)
	encoded := base64.RawStdEncoding.EncodeToString(sealed)

	return fmt.Sprintf("%s%s:%s", Prefix, c.keyID, encoded), nil
}

// Decrypt tolerates pre-existing values.
func (c *Cipher) Decrypt(stored string, aad []byte, legacy LegacyKind) (string, error) {
	if c == nil || c.aead == nil {
		return "", fmt.Errorf("cipher is not configured")
	}

	if strings.HasPrefix(stored, Prefix) {
		payload := stored[len(Prefix):]
		parts := strings.SplitN(payload, ":", 2)
		if len(parts) != 2 {
			return "", fmt.Errorf("invalid ciphertext format")
		}
		storedKeyID := parts[0]
		encoded := parts[1]

		if storedKeyID != c.keyID {
			return "", fmt.Errorf("key mismatch: ciphertext sealed with keyID %q, have %q", storedKeyID, c.keyID)
		}

		return c.decryptRaw(c.aead, encoded, aad)
	}

	if legacy == LegacyPlaintext {
		return stored, nil
	}

	// LegacyCiphertext (nil AAD)
	plaintext, err := c.decryptRaw(c.aead, stored, nil)
	if err == nil {
		return plaintext, nil
	}

	if c.legacyAEAD != nil {
		legacyPlaintext, legacyErr := c.decryptRaw(c.legacyAEAD, stored, nil)
		if legacyErr == nil {
			return legacyPlaintext, nil
		}
	}

	return "", err
}

func (c *Cipher) decryptRaw(aead cipher.AEAD, encoded string, aad []byte) (string, error) {
	sealed, err := base64.RawStdEncoding.DecodeString(encoded)
	if err != nil {
		return "", fmt.Errorf("decode ciphertext: %w", err)
	}
	if len(sealed) < aead.NonceSize() {
		return "", fmt.Errorf("ciphertext is shorter than nonce")
	}

	nonce := sealed[:aead.NonceSize()]
	body := sealed[aead.NonceSize():]
	plaintext, err := aead.Open(nil, nonce, body, aad)
	if err != nil {
		return "", fmt.Errorf("decrypt ciphertext: %w", err)
	}

	return string(plaintext), nil
}
