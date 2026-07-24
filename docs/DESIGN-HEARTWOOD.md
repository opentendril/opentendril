# Component: Heartwood — the shared, portable (cgo-free) at-rest confidentiality leaf

## Purpose

`cmd/stem/internal/heartwood` is the shared, portable (cgo-free) at-rest confidentiality leaf — application-level AES-GCM of sensitive values before they reach local SQLite; used by both rhizome and historydb.

## Responsibilities

**Does:**

- `Cipher.Encrypt` always writes the new versioned format `tnd:atrest:1:<keyID>:<base64(nonce||ciphertext)>`, utilizing the exported `Prefix`.
- `Cipher.Decrypt` provides backward read-compat for pre-existing ciphertext and plaintext.
- `ResolveKey` implements a two-tier key model.
- Binds AAD to ciphertexts to prevent cross-column reuse.
- Supports `keyID` and versioning via the prefix.

**Does not:**

- Choose which columns to encrypt (callers decide).
- Enforce per-row AAD (caller supplies AAD).
- Rotate keys (deferred).
- Touch remote backends.
- Own any database schema.

## Public interface

| Symbol | Role |
| --- | --- |
| `Prefix` | The literal prefix for the new versioned ciphertext format. |
| `KeyEnvVar` | The environment variable operators can use to supply a key (`OPEN_TENDRIL_INDEX_KEY`). |
| `KeySource` / `KeySourceFile` / `KeySourceEnv` | Represents the source of the material key (Tier-1 file vs Tier-2 env). |
| `Material` | Holds the key material for encryption and decryption. |
| `ResolveKey` | Resolves the encryption key using a two-tier resolution model. |
| `LegacyKind` / `LegacyCiphertext` / `LegacyPlaintext` | Represents how an unprefixed stored value should be treated. |
| `Cipher` | Provides AEAD encryption and decryption of strings. |
| `NewCipher` | Creates a new `Cipher` using the provided key material. |
| `Encrypt` | Always writes the new versioned format. |
| `Decrypt` | Tolerates pre-existing values. |

## Dependencies

**Fan-out:** standard library only (`crypto/aes`, `crypto/cipher`, `crypto/hkdf`, `crypto/rand`, `crypto/sha256`, `encoding/*`). Boundary note: a strict leaf — imports no internal package, so the storage packages depend inward on it with no cycle.

**Fan-in:**
- `internal/rhizome` (`schema.go`)
- `internal/conductor` (`rhizomefacade.go`)
- `cmd/stem` (`cmdmemory.go`)
- `internal/historydb` (`historydb.go`)

## Limitations

- Tier-1 co-located auto-key is defense-in-depth, not a boundary. The auto-generated `.tendril/rhizome.key` sits beside the ciphertext with the same access control, so it does not defend a wholesale read of `.tendril/` (disk image, full backup, folder sync). It defends casual reads, other-user file perms, and partial copies that exclude the key. Only the operator-supplied `OPEN_TENDRIL_INDEX_KEY` (Tier-2, never written to disk) is a real at-rest control.
- No key rotation (deferred); the versioned keyID prefix leaves the door open without a schema migration.
- Backward read-compat depends on the `:` in `Prefix` never occurring in a base64 value (true for `base64.RawStdEncoding`).
- Column selection and which columns stay plaintext-for-indexing are the caller's decision, not enforced here.

## Design & rationale

The design leverages column-level AES-GCM instead of SQLCipher to keep the binary portable and preserve the CGO-free guarantee of the Stem. Application-side pre-INSERT encryption also keeps WAL and temp files free of protected-column plaintext. HKDF over the env secret derives keys (replacing raw truncation). Two `LegacyKind` variants are provided because `rhizome`'s pre-existing values are ciphertext while `historydb`'s are plaintext. Finally, AAD binds a ciphertext to its column to prevent cross-column reuse.
