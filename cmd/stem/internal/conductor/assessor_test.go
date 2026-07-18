package conductor

import (
	"testing"

	"github.com/opentendril/opentendril/roots/llm"
)

func TestParseRouterResponse(t *testing.T) {
	tests := []struct {
		name      string
		text      string
		wantProv  string
		wantModel string
		wantErr   bool
	}{
		{name: "valid", text: `{"provider":"grok","model":"grok-beta"}`, wantProv: "grok", wantModel: "grok-beta"},
		{name: "whitespace", text: "\n {\"provider\":\"openai\",\"model\":\"gpt-4o\"}\n", wantProv: "openai", wantModel: "gpt-4o"},
		{name: "malformed", text: `{"provider":`, wantErr: true},
		{name: "missing provider", text: `{"model":"gpt-4o"}`, wantErr: true},
		{name: "missing model", text: `{"provider":"openai"}`, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseRouterResponse(tt.text)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("parseRouterResponse(%q) returned nil error", tt.text)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseRouterResponse(%q) returned error: %v", tt.text, err)
			}
			if got.Provider != tt.wantProv {
				t.Fatalf("provider = %q, want %q", got.Provider, tt.wantProv)
			}
			if got.Model != tt.wantModel {
				t.Fatalf("model = %q, want %q", got.Model, tt.wantModel)
			}
		})
	}
}

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
