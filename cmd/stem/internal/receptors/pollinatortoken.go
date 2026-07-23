package receptors

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"

	"github.com/opentendril/opentendril/cmd/stem/internal/core"
)

// PollinatorTokenHandler mints short-lived access tokens from a durable
// Pollinator credential (the refresh root). The root is presented ONLY here; the
// minted token is what every other surface then accepts per request. This route
// is the single seam where a durable secret is exchanged for a short-lived one.
type PollinatorTokenHandler struct {
	Signer      *core.StemSigner
	Credentials PollinatorCredentials
}

// NewPollinatorTokenHandler builds the mint handler over a signer and the set of
// issued credentials it authenticates roots against.
func NewPollinatorTokenHandler(signer *core.StemSigner, credentials PollinatorCredentials) *PollinatorTokenHandler {
	return &PollinatorTokenHandler{Signer: signer, Credentials: credentials}
}

// mintTokenRequest is the optional body: a shorter lifetime may be requested,
// never a longer one. Zero or omitted takes the default. Negative is refused.
// Scope narrowing is deferred; the minted token currently carries an empty
// scope (full grant for its Pollen).
type mintTokenRequest struct {
	TTLSeconds int `json:"ttlSeconds"`
}

type mintTokenResponse struct {
	Token     string    `json:"token"`
	Pollen    string    `json:"pollen"`
	ExpiresAt time.Time `json:"expiresAt"`
}

// HandleMint authenticates a presented root credential and returns a fresh
// signed access token for the Pollen it resolves to.
//
// It accepts ONLY a credential-shaped bearer: an access token cannot mint
// another token (there is no self-refresh without the root), and a plain bearer
// key cannot mint for an identity it merely names. An unknown or revoked root
// resolves to nothing and is refused — the mint path never issues a token for an
// identity the caller could not prove.
func (h *PollinatorTokenHandler) HandleMint(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if h == nil || h.Signer == nil {
		http.Error(w, "access-token minting is not configured", http.StatusServiceUnavailable)
		return
	}

	presented := bearerToken(r)
	if !core.LooksLikePollinatorCredential(presented) {
		http.Error(w, "a Pollinator credential is required to mint an access token", http.StatusUnauthorized)
		return
	}
	pollen := core.ResolvePollenFromCredential(h.Credentials, presented)
	if pollen == "" {
		http.Error(w, "unknown or revoked Pollinator credential", http.StatusUnauthorized)
		return
	}

	var body mintTokenRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil && err != io.EOF {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}
	// Negative is a client error, not a request for the default: MintAccessToken
	// treats <=0 as "use default", which would quietly upgrade a buggy caller.
	if body.TTLSeconds < 0 {
		http.Error(w, "ttlSeconds must not be negative", http.StatusBadRequest)
		return
	}

	token, err := h.Signer.MintAccessToken(pollen, time.Duration(body.TTLSeconds)*time.Second, core.AccessTokenScope{})
	if err != nil {
		// Policy refusals (over-cap) get a stable client message that names the
		// known limit; unexpected failures are generic. Never surface raw
		// internal error strings on the wire.
		log.Printf("access-token mint refused for Pollen %q: %v", pollen, err)
		if body.TTLSeconds > 0 && time.Duration(body.TTLSeconds)*time.Second > core.MaxAccessTokenTTL {
			http.Error(w, fmt.Sprintf("requested ttl exceeds the maximum of %s", core.MaxAccessTokenTTL), http.StatusBadRequest)
			return
		}
		http.Error(w, "access token could not be minted for the requested parameters", http.StatusBadRequest)
		return
	}
	// Read the authoritative expiry back off the signed token rather than
	// recomputing it, so the response cannot drift from what was signed.
	claims, _ := h.Signer.VerifyAccessToken(token)

	// Bearer tokens must never be cached by intermediaries (RFC 6749 §5.1).
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	log.Printf("access token minted for Pollen %q (expires %s)", claims.Pollen, claims.ExpiresAt.Format(time.RFC3339))
	if err := json.NewEncoder(w).Encode(mintTokenResponse{
		Token:     token,
		Pollen:    claims.Pollen,
		ExpiresAt: claims.ExpiresAt,
	}); err != nil {
		log.Printf("failed to write access-token mint response: %v", err)
	}
}

// Register mounts the mint route. It is self-authenticating (the root credential
// is the auth), so it takes no outer bearer wrapper.
func (h *PollinatorTokenHandler) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/pollinator/token", h.HandleMint)
}
