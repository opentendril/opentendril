package conductor

type genotypeDefinition struct {
	Name                     string   `json:"name"`
	System                   bool     `json:"system,omitempty"`
	Instructions             string   `json:"instructions"`
	Plasmids                 []string `json:"plasmids,omitempty"`
	DenyPlasmids             []string `json:"denyPlasmids,omitempty"`
	RequirePlasmidSignatures bool     `json:"requirePlasmidSignatures,omitempty"`
}
