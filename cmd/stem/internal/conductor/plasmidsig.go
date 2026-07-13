package conductor

import (
	"crypto/ed25519"
	"encoding/hex"
	"fmt"
	"os"
)

// SignPlasmid returns the Ed25519 signature for a plasmid file.
func SignPlasmid(sourcePath string, privateKey ed25519.PrivateKey) ([]byte, error) {
	if len(privateKey) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("invalid ed25519 private key length: %d", len(privateKey))
	}

	payload, err := os.ReadFile(sourcePath)
	if err != nil {
		return nil, err
	}

	return ed25519.Sign(privateKey, payload), nil
}

// WritePlasmidSignature writes the hex-encoded signature next to the plasmid.
func WritePlasmidSignature(sourcePath string, sig []byte) error {
	payload := hex.EncodeToString(sig) + "\n"
	return os.WriteFile(sourcePath+".sig", []byte(payload), 0o644)
}

// VerifyPlasmidSignature validates a plasmid file against its .sig sidecar.
func VerifyPlasmidSignature(sourcePath string, publicKey ed25519.PublicKey) error {
	payload, err := os.ReadFile(sourcePath)
	if err != nil {
		return err
	}

	sigPayload, err := os.ReadFile(sourcePath + ".sig")
	if err != nil {
		return err
	}
	if len(sigPayload) > 0 && sigPayload[len(sigPayload)-1] == '\n' {
		sigPayload = sigPayload[:len(sigPayload)-1]
	}

	actual, err := hex.DecodeString(string(sigPayload))
	if err != nil {
		return err
	}

	if !ed25519.Verify(publicKey, payload, actual) {
		return os.ErrInvalid
	}

	return nil
}
