package main

import (
	"reflect"
	"testing"
)

func TestExtractSeedAsyncFlag(t *testing.T) {
	cases := []struct {
		name      string
		in        []string
		wantArgs  []string
		wantAsync bool
	}{
		{
			name:      "no flag",
			in:        []string{"--substrate", ".", "--goal", "g", "--", "go", "test"},
			wantArgs:  []string{"--substrate", ".", "--goal", "g", "--", "go", "test"},
			wantAsync: false,
		},
		{
			name:      "flag before the separator is consumed",
			in:        []string{"--substrate", ".", "--async", "--goal", "g", "--", "go", "test"},
			wantArgs:  []string{"--substrate", ".", "--goal", "g", "--", "go", "test"},
			wantAsync: true,
		},
		{
			name:      "a literal --async in the verify command is kept",
			in:        []string{"--async", "--", "mytool", "--async"},
			wantArgs:  []string{"--", "mytool", "--async"},
			wantAsync: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, async := extractSeedAsyncFlag(tc.in)
			if async != tc.wantAsync {
				t.Fatalf("async = %v, want %v", async, tc.wantAsync)
			}
			if !reflect.DeepEqual(got, tc.wantArgs) {
				t.Fatalf("args = %v, want %v", got, tc.wantArgs)
			}
		})
	}
}
