package main

import (
	"strings"
	"testing"
)

func TestAdaptProposalSuccessMessageDescribesRhizomeProposals(t *testing.T) {
	message := adaptProposalSuccessMessage(3)
	if !strings.Contains(message, "3 commit(s)") || !strings.Contains(message, "source-local Rhizome") || strings.Contains(message, "epigenetics.md") {
		t.Fatalf("adapt success message = %q, want proposal persistence wording", message)
	}
}
