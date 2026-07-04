package orchestrator

import (
	"testing"

	"github.com/opentendril/core/cmd/stem/internal/llm"
)

func TestParseAssessorResponse(t *testing.T) {
	tests := []struct {
		name    string
		text    string
		want    llm.ModelTier
		wantErr bool
	}{
		{name: "premium", text: `{"tier":"premium"}`, want: llm.TierPremium},
		{name: "standard", text: `{"tier":"standard"}`, want: llm.TierStandard},
		{name: "cheapest", text: `{"tier":"cheapest"}`, want: llm.TierCheapest},
		{name: "whitespace", text: "\n\t {\"tier\":\"standard\"} \n", want: llm.TierStandard},
		{name: "malformed", text: `{"tier":`, wantErr: true},
		{name: "invalid tier", text: `{"tier":"expensive"}`, wantErr: true},
		{name: "missing tier", text: `{}`, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseAssessorResponse(tt.text)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("parseAssessorResponse(%q) returned nil error", tt.text)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseAssessorResponse(%q) returned error: %v", tt.text, err)
			}
			if got != tt.want {
				t.Fatalf("parseAssessorResponse(%q) = %q, want %q", tt.text, got, tt.want)
			}
		})
	}
}
