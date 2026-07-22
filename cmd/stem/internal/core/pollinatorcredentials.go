package core

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Pollinator credentials: the credential IS the Pollen. The Stem looks a
// presented credential up and derives the Pollen from it; nothing the caller
// sends is consulted, so a caller cannot declare an identity.
//
// Storage rules, so a leaked store is not a leaked credential:
//
//   - the secret is shown ONCE, at issue, and never persisted;
//   - only a SHA-256 digest is stored, so the file cannot be replayed;
//   - lookup is constant-time against every digest, so a timing signal cannot
//     narrow the search;
//   - the file is written 0600 in the Stem's own directory.

// PollinatorCredentialsFilename is the store, alongside grants.yaml in the
// Stem's control-plane directory.
const PollinatorCredentialsFilename = "pollinators.json"

// pollinatorTokenPrefix makes a credential recognisable in a log or a
// configuration file, so a leaked one can be identified and revoked.
//
// It is functional, not decorative: it discriminates a Pollinator credential
// from the Botanist's key. Changing it invalidates every credential issued.
const pollinatorTokenPrefix = "tendril_"

var pollinatorStoreMu sync.Mutex

// PollinatorCredential is one issued credential. It never contains the secret.
type PollinatorCredential struct {
	// Pollen is the identity this credential authenticates as. It is the whole
	// point: the credential carries it, so the caller cannot.
	Pollen string `json:"pollen"`
	// Digest is the hex SHA-256 of the issued secret.
	Digest string `json:"digest"`
	// IssuedAt records when, so an operator can see the age of a credential.
	IssuedAt time.Time `json:"issuedAt"`
	// Note is an optional operator memo (which machine, which Pollinator).
	Note string `json:"note,omitempty"`
	// RevokedAt is set when the credential is withdrawn. A revoked credential
	// is kept rather than deleted so the record of what existed survives.
	RevokedAt *time.Time `json:"revokedAt,omitempty"`
}

// Active reports whether this credential still authenticates.
func (c PollinatorCredential) Active() bool { return c.RevokedAt == nil }

// pollinatorCredentialsFile is the on-disk shape.
type pollinatorCredentialsFile struct {
	Credentials []PollinatorCredential `json:"credentials"`
}

func pollinatorCredentialsPath(tendrilDir string) string {
	return filepath.Join(strings.TrimSpace(tendrilDir), PollinatorCredentialsFilename)
}

// LoadPollinatorCredentials reads the store. A missing file yields none, which
// is the secure default: no credentials means no Pollinator can authenticate,
// and delegation is denied rather than opened.
//
// A malformed file is an error. It must never degrade into "no credentials
// loaded, so nothing is delegated, so everything runs as something else" —
// callers deny, they do not fall back.
func LoadPollinatorCredentials(tendrilDir string) ([]PollinatorCredential, error) {
	content, err := os.ReadFile(pollinatorCredentialsPath(tendrilDir))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read Pollinator credentials: %w", err)
	}
	var file pollinatorCredentialsFile
	if err := json.Unmarshal(content, &file); err != nil {
		return nil, fmt.Errorf("decode Pollinator credentials: %w", err)
	}
	return file.Credentials, nil
}

// savePollinatorCredentials writes the store atomically at 0600.
func savePollinatorCredentials(tendrilDir string, credentials []PollinatorCredential) error {
	path := pollinatorCredentialsPath(tendrilDir)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create control-plane directory: %w", err)
	}
	sort.Slice(credentials, func(i, j int) bool {
		if credentials[i].Pollen != credentials[j].Pollen {
			return credentials[i].Pollen < credentials[j].Pollen
		}
		return credentials[i].IssuedAt.Before(credentials[j].IssuedAt)
	})
	encoded, err := json.MarshalIndent(pollinatorCredentialsFile{Credentials: credentials}, "", "  ")
	if err != nil {
		return err
	}
	temporary := path + ".tmp"
	if err := os.WriteFile(temporary, append(encoded, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(temporary, path)
}

// hashPollinatorToken is the one place a secret becomes a digest.
func hashPollinatorToken(token string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(token)))
	return hex.EncodeToString(sum[:])
}

// IssuePollinatorCredential mints a credential for a Pollen and returns the
// secret. The secret is returned exactly once and is never written anywhere:
// an operator who loses it issues another and revokes this one.
func IssuePollinatorCredential(tendrilDir, pollen, note string) (secret string, credential PollinatorCredential, err error) {
	pollen = strings.TrimSpace(pollen)
	if pollen == "" {
		return "", PollinatorCredential{}, fmt.Errorf("a credential must name the Pollen it authenticates as")
	}

	buffer := make([]byte, 32)
	if _, err := rand.Read(buffer); err != nil {
		return "", PollinatorCredential{}, fmt.Errorf("generate credential: %w", err)
	}
	secret = pollinatorTokenPrefix + base64.RawURLEncoding.EncodeToString(buffer)

	credential = PollinatorCredential{
		Pollen:   pollen,
		Digest:   hashPollinatorToken(secret),
		IssuedAt: time.Now().UTC(),
		Note:     strings.TrimSpace(note),
	}

	pollinatorStoreMu.Lock()
	defer pollinatorStoreMu.Unlock()

	existing, loadErr := LoadPollinatorCredentials(tendrilDir)
	if loadErr != nil {
		return "", PollinatorCredential{}, loadErr
	}
	if err := savePollinatorCredentials(tendrilDir, append(existing, credential)); err != nil {
		return "", PollinatorCredential{}, err
	}
	return secret, credential, nil
}

// RevokePollinatorCredentials withdraws every active credential for a Pollen
// and reports how many were affected. Revocation is by identity rather than by
// digest so an operator can withdraw a Pollinator's access without first
// working out which credential it holds.
func RevokePollinatorCredentials(tendrilDir, pollen string) (int, error) {
	pollen = strings.TrimSpace(pollen)
	if pollen == "" {
		return 0, fmt.Errorf("a Pollen is required to revoke")
	}

	pollinatorStoreMu.Lock()
	defer pollinatorStoreMu.Unlock()

	credentials, err := LoadPollinatorCredentials(tendrilDir)
	if err != nil {
		return 0, err
	}
	now := time.Now().UTC()
	revoked := 0
	for i := range credentials {
		if credentials[i].Pollen == pollen && credentials[i].Active() {
			at := now
			credentials[i].RevokedAt = &at
			revoked++
		}
	}
	if revoked == 0 {
		return 0, nil
	}
	if err := savePollinatorCredentials(tendrilDir, credentials); err != nil {
		return 0, err
	}
	return revoked, nil
}

// ResolvePollenFromCredential turns a presented secret into the Pollen it
// authenticates as. This is the function that makes the credential the
// identity — no caller-supplied value reaches it.
//
// It returns "" for an unknown, malformed or revoked credential, so every
// failure is the same deny with no signal about which one occurred. Comparison
// is constant-time across all stored digests: it never returns early on a
// match, so the time taken does not narrow a search.
func ResolvePollenFromCredential(credentials []PollinatorCredential, secret string) string {
	presented := strings.TrimSpace(secret)
	if presented == "" || !strings.HasPrefix(presented, pollinatorTokenPrefix) {
		return ""
	}
	digest := hashPollinatorToken(presented)

	resolved := ""
	for _, candidate := range credentials {
		if !candidate.Active() {
			continue
		}
		if subtle.ConstantTimeCompare([]byte(candidate.Digest), []byte(digest)) == 1 {
			resolved = candidate.Pollen
		}
	}
	return resolved
}

// LooksLikePollinatorCredential reports whether a presented bearer value is
// shaped like one of these credentials. Surfaces use it to tell "this caller is
// presenting a Pollinator credential" from "this caller is presenting the
// Botanist's own key", without having to resolve it first.
func LooksLikePollinatorCredential(value string) bool {
	return strings.HasPrefix(strings.TrimSpace(value), pollinatorTokenPrefix)
}
