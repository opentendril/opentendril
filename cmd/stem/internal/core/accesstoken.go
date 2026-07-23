package core

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Access tokens: the short-lived half of the two-tier credential. A Pollinator
// credential (the refresh root) is durable, digest-stored and revocable; it is
// presented only to mint. What a surface then accepts per request is an access
// token — a Stem-signed assertion of a Pollen that carries an expiry and, at
// most, a narrower scope than the root's grant.
//
// The token is verified by SIGNATURE, not by a store lookup: any holder of the
// Stem's public key can validate one with no shared state. That is what keeps a
// remote executor able to verify a token the local Stem minted. Revocation is at
// the root — revoke the credential and minting stops, so outstanding tokens die
// within their (short, hard-capped) lifetime rather than needing a per-token
// denylist that would reintroduce the state statelessness removes.

// AccessTokenPrefix tags a bearer as an access token so a surface routes it to
// signature verification rather than to the digest resolver.
//
// It is deliberately distinct from pollinatorTokenPrefix ("tendril_") and shares
// no prefix relationship with it, so LooksLikeAccessToken and
// LooksLikePollinatorCredential are mutually exclusive: a bearer is at most one
// of the two.
const AccessTokenPrefix = "tendrilat_"

// StemSigningKeyFilename holds the Stem's ed25519 signing key, alongside the
// credential store in the Stem's control-plane directory. Deleting it rotates
// the key: outstanding tokens then fail verification and age out; the durable
// credentials are untouched.
const StemSigningKeyFilename = "stem-signing-key.json"

// DefaultAccessTokenTTL is the lifetime a token receives when none is requested.
// MaxAccessTokenTTL is the hard cap: a mint may request a SHORTER lifetime, never
// a longer one. The short cap is not a tuning knob — root-only revocation is only
// sufficient because a leaked token expires quickly.
const (
	DefaultAccessTokenTTL = 15 * time.Minute
	MaxAccessTokenTTL     = 15 * time.Minute
)

// AccessTokenScope is the OPTIONAL narrowing an access token carries. An empty
// field inherits the full grant for the token's Pollen; a populated field is a
// subset the request may not exceed. It can only narrow — a downstream authorizer
// intersects it with the grant and never widens.
type AccessTokenScope struct {
	OperationClasses []string `json:"operationClasses,omitempty"`
	Substrates       []string `json:"substrates,omitempty"`
}

// AccessTokenClaims is the verified content of a token. Pollen is the identity;
// it is never taken from anything the caller declares separately.
type AccessTokenClaims struct {
	Pollen    string           `json:"pollen"`
	IssuedAt  time.Time        `json:"iat"`
	ExpiresAt time.Time        `json:"exp"`
	Scope     AccessTokenScope `json:"scope,omitempty"`
}

// StemSigner mints and verifies access tokens with the Stem's own key. The
// private key never leaves the Stem.
type StemSigner struct {
	private ed25519.PrivateKey
}

// Public returns the verification key. A remote verifier needs only this — no
// credential store, no private material — to validate a token.
func (s *StemSigner) Public() ed25519.PublicKey {
	return s.private.Public().(ed25519.PublicKey)
}

// stemSigningKeyFile is the on-disk shape: the raw ed25519 private key, base64.
type stemSigningKeyFile struct {
	PrivateKey string `json:"privateKey"`
}

func stemSigningKeyPath(tendrilDir string) string {
	return filepath.Join(strings.TrimSpace(tendrilDir), StemSigningKeyFilename)
}

// LoadOrCreateStemSigner loads the Stem signing key, generating and persisting
// one on first use. The key file is written 0600 in the Stem's own directory,
// exactly like the credential store, so a leaked directory is the same exposure
// it already was.
func LoadOrCreateStemSigner(tendrilDir string) (*StemSigner, error) {
	path := stemSigningKeyPath(tendrilDir)
	content, err := os.ReadFile(path)
	if err == nil {
		var file stemSigningKeyFile
		if err := json.Unmarshal(content, &file); err != nil {
			return nil, fmt.Errorf("decode Stem signing key: %w", err)
		}
		raw, err := base64.StdEncoding.DecodeString(file.PrivateKey)
		if err != nil {
			return nil, fmt.Errorf("decode Stem signing key material: %w", err)
		}
		if len(raw) != ed25519.PrivateKeySize {
			return nil, fmt.Errorf("Stem signing key has wrong size %d", len(raw))
		}
		return &StemSigner{private: ed25519.PrivateKey(raw)}, nil
	}
	if !os.IsNotExist(err) {
		return nil, fmt.Errorf("read Stem signing key: %w", err)
	}

	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate Stem signing key: %w", err)
	}
	if err := saveStemSigningKey(path, private); err != nil {
		return nil, err
	}
	return &StemSigner{private: private}, nil
}

// saveStemSigningKey writes the key atomically at 0600.
func saveStemSigningKey(path string, private ed25519.PrivateKey) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create control-plane directory: %w", err)
	}
	encoded, err := json.MarshalIndent(stemSigningKeyFile{
		PrivateKey: base64.StdEncoding.EncodeToString(private),
	}, "", "  ")
	if err != nil {
		return err
	}
	temporary := path + ".tmp"
	if err := os.WriteFile(temporary, append(encoded, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(temporary, path)
}

// MintAccessToken signs a token for a Pollen. A zero or negative ttl takes the
// default; a ttl above the cap is refused rather than silently clamped, so a
// caller asking for more than the policy allows is told, not quietly downgraded.
func (s *StemSigner) MintAccessToken(pollen string, ttl time.Duration, scope AccessTokenScope) (string, error) {
	pollen = strings.TrimSpace(pollen)
	if pollen == "" {
		return "", fmt.Errorf("an access token must name the Pollen it authenticates as")
	}
	if ttl <= 0 {
		ttl = DefaultAccessTokenTTL
	}
	if ttl > MaxAccessTokenTTL {
		return "", fmt.Errorf("requested ttl %s exceeds the maximum %s", ttl, MaxAccessTokenTTL)
	}

	now := time.Now().UTC()
	claims := AccessTokenClaims{
		Pollen:    pollen,
		IssuedAt:  now,
		ExpiresAt: now.Add(ttl),
		Scope:     scope,
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	signature := ed25519.Sign(s.private, payload)
	return AccessTokenPrefix +
		base64.RawURLEncoding.EncodeToString(payload) + "." +
		base64.RawURLEncoding.EncodeToString(signature), nil
}

// MintFromCredential authenticates a presented root credential and, only if it
// resolves to a Pollen, mints an access token for that Pollen. An unknown,
// malformed or revoked root resolves to nothing and is refused — the mint path
// never issues a token for an identity the caller could not prove.
func (s *StemSigner) MintFromCredential(credentials []PollinatorCredential, rootSecret string, ttl time.Duration, scope AccessTokenScope) (string, error) {
	pollen := ResolvePollenFromCredential(credentials, rootSecret)
	if pollen == "" {
		return "", fmt.Errorf("unknown or revoked Pollinator credential")
	}
	return s.MintAccessToken(pollen, ttl, scope)
}

// VerifyAccessToken verifies a token with the Stem's own public key.
func (s *StemSigner) VerifyAccessToken(token string) (AccessTokenClaims, bool) {
	return VerifyAccessToken(s.Public(), token)
}

// VerifyAccessToken verifies a token against a public key with no other state,
// and returns its claims only if the signature is valid AND the token has not
// expired.
//
// Every failure — wrong prefix, malformed encoding, bad signature, expired —
// returns the same zero claims and false, so nothing distinguishes one cause
// from another to a caller. Deny-closed: a token that does not verify resolves to
// no identity, exactly as an unresolvable credential does.
func VerifyAccessToken(public ed25519.PublicKey, token string) (AccessTokenClaims, bool) {
	token = strings.TrimSpace(token)
	if !strings.HasPrefix(token, AccessTokenPrefix) {
		return AccessTokenClaims{}, false
	}
	body := strings.TrimPrefix(token, AccessTokenPrefix)
	encodedPayload, encodedSignature, found := strings.Cut(body, ".")
	if !found {
		return AccessTokenClaims{}, false
	}
	payload, err := base64.RawURLEncoding.DecodeString(encodedPayload)
	if err != nil {
		return AccessTokenClaims{}, false
	}
	signature, err := base64.RawURLEncoding.DecodeString(encodedSignature)
	if err != nil {
		return AccessTokenClaims{}, false
	}
	if len(public) != ed25519.PublicKeySize || !ed25519.Verify(public, payload, signature) {
		return AccessTokenClaims{}, false
	}
	var claims AccessTokenClaims
	if err := json.Unmarshal(payload, &claims); err != nil {
		return AccessTokenClaims{}, false
	}
	if claims.Pollen == "" || !time.Now().UTC().Before(claims.ExpiresAt) {
		return AccessTokenClaims{}, false
	}
	return claims, true
}

// LooksLikeAccessToken reports whether a presented bearer is shaped like an
// access token. Surfaces use it to route a bearer to signature verification
// rather than to the digest resolver, without verifying it first.
func LooksLikeAccessToken(value string) bool {
	return strings.HasPrefix(strings.TrimSpace(value), AccessTokenPrefix)
}
