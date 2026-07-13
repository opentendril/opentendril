package mesh

// TraitKind identifies the epigenetic trait family carried by a signed envelope.
type TraitKind string

const (
	TraitKindPlasmid  TraitKind = "plasmid"
	TraitKindGenotype TraitKind = "genotype"
	TraitKindSequence TraitKind = "sequence"
)

// TraitPayload is the transport-agnostic trait body shared between mesh instances.
type TraitPayload struct {
	Kind    TraitKind `json:"kind"`
	Name    string    `json:"name"`
	Version string    `json:"version"`
	Content string    `json:"content"`
}

// TraitOrigin captures the node that minted the signed trait envelope.
type TraitOrigin struct {
	NodeID               string `json:"nodeId"`
	PublicKeyFingerprint string `json:"publicKeyFingerprint"`
}

// TraitEnvelope packages a trait with its origin and signature.
//
// acceptPolicy (deny | manual | allowlist) will be wired in a later slice once
// the REST and CLI routes are connected and can enforce mesh ingestion policy.
type TraitEnvelope struct {
	Trait     TraitPayload `json:"trait"`
	Origin    TraitOrigin  `json:"origin"`
	SignedAt  int64        `json:"signedAt"`
	Signature string       `json:"signature"` // base64url(ed25519(canonicalJson(trait+origin+signedAt)))
}
