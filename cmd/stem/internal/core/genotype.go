package core

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// GenotypeCreateInput identifies the genotype to create and its payload.
type GenotypeCreateInput struct {
	Substrate    string `json:"substrate"`
	Name         string `json:"name"`
	Instructions string `json:"instructions"`
	Origin       string `json:"origin,omitempty"`
}

// validConfigFileName reports whether a caller-supplied name is safe to embed
// in a filename inside the .tendril configuration tree. Valid names carry no
// path separators and no traversal component.
func validConfigFileName(name string) bool {
	if name == "" || name == "." || name == ".." {
		return false
	}
	if !filepath.IsLocal(name + ".json") {
		return false
	}
	return !strings.ContainsAny(name, `/\`)
}

// GenotypeCreate creates or updates a genotype definition in the configuration tree.
func (s *Service) GenotypeCreate(ctx context.Context, in GenotypeCreateInput) (any, error) {
	if !validConfigFileName(in.Name) {
		return nil, fmt.Errorf("invalid genotype name: must not contain path separators or traversal components")
	}

	tendrilDir := s.tendrilDir
	if tendrilDir == "" {
		tendrilDir = filepath.Join(".", ".tendril")
	}
	genotypesDir := filepath.Join(tendrilDir, "genotypes")

	if err := os.MkdirAll(genotypesDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create config directory: %w", err)
	}

	root, err := os.OpenRoot(genotypesDir)
	if err != nil {
		return nil, fmt.Errorf("failed to open config directory: %w", err)
	}
	defer root.Close()

	out, err := root.OpenFile(in.Name+".json", os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644)
	if err != nil {
		return nil, fmt.Errorf("failed to create file: %w", err)
	}
	defer out.Close()

	payload := map[string]interface{}{
		"name":         in.Name,
		"instructions": in.Instructions,
	}

	if err := json.NewEncoder(out).Encode(payload); err != nil {
		return nil, fmt.Errorf("failed to write config: %w", err)
	}

	return map[string]interface{}{
		"name":    in.Name,
		"created": true,
	}, nil
}

// genotypeCapabilities declares the genotype family's registry entries.
func (s *Service) genotypeCapabilities() []Capability {
	return []Capability{
		{
			Name:        CapGenotypeCreate,
			Description: "Dynamically create or update an OpenTendril genotype (core identity/persona). Creates a new JSON configuration file in the genotypes directory. This allows you to define a new base role before sprouting a tendril.",
			InputSchema: schemaObject(map[string]any{
				"substrate":    stringProp("The absolute path or named substrate key for the target workspace."),
				"name":         stringProp("The unique name of the genotype (e.g. 'frontend-dev'). Do not use spaces or special characters."),
				"instructions": stringProp("The system prompt or instructions detailing exactly what this genotype's core identity or role is."),
			}, []string{"substrate", "name", "instructions"}),
			Invoke: func(ctx context.Context, input map[string]any) (any, error) {
				var in GenotypeCreateInput
				if err := decodeInput(input, &in); err != nil {
					return nil, err
				}
				return s.GenotypeCreate(ctx, in)
			},
		},
	}
}
