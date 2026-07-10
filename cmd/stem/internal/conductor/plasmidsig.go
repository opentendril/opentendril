package conductor

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
)

// NodeSigningKey loads or creates the local node key used for plasmid signatures.
func NodeSigningKey() ([]byte, error) {
	configDir, err := os.UserConfigDir()
	if err != nil {
		return nil, err
	}

	keyPath := filepath.Join(configDir, "opentendril", "node.key")
	if payload, err := os.ReadFile(keyPath); err == nil {
		if len(payload) != 64 {
			return nil, os.ErrInvalid
		}
		key, err := hex.DecodeString(string(payload))
		if err != nil {
			return nil, err
		}
		if len(key) != 32 {
			return nil, os.ErrInvalid
		}
		return key, nil
	} else if !os.IsNotExist(err) {
		return nil, err
	}

	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}

	if err := os.MkdirAll(filepath.Dir(keyPath), 0o755); err != nil {
		return nil, err
	}
	if err := os.WriteFile(keyPath, []byte(hex.EncodeToString(key)), 0o600); err != nil {
		return nil, err
	}

	return key, nil
}

// SignPlasmid returns the HMAC-SHA256 signature for a plasmid file.
func SignPlasmid(sourcePath string, key []byte) ([]byte, error) {
	payload, err := os.ReadFile(sourcePath)
	if err != nil {
		return nil, err
	}

	mac := hmac.New(sha256.New, key)
	if _, err := mac.Write(payload); err != nil {
		return nil, err
	}

	return mac.Sum(nil), nil
}

// WritePlasmidSignature writes the hex-encoded signature next to the plasmid.
func WritePlasmidSignature(sourcePath string, sig []byte) error {
	payload := hex.EncodeToString(sig) + "\n"
	return os.WriteFile(sourcePath+".sig", []byte(payload), 0o644)
}

// VerifyPlasmidSignature validates a plasmid file against its .sig sidecar.
func VerifyPlasmidSignature(sourcePath string, key []byte) error {
	payload, err := os.ReadFile(sourcePath + ".sig")
	if err != nil {
		return err
	}
	if len(payload) > 0 && payload[len(payload)-1] == '\n' {
		payload = payload[:len(payload)-1]
	}

	actual, err := hex.DecodeString(string(payload))
	if err != nil {
		return err
	}

	expected, err := SignPlasmid(sourcePath, key)
	if err != nil {
		return err
	}
	if !hmac.Equal(expected, actual) {
		return os.ErrInvalid
	}

	return nil
}
