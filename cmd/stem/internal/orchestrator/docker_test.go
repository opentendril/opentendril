package orchestrator

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCloneForeignSubstrate(t *testing.T) {
	// Use a known public repository that is small
	url := "https://github.com/torvalds/test-tlb.git"
	branch := "master"

	shadowPath, err := cloneForeignSubstrate(url, branch)
	if err != nil {
		t.Fatalf("cloneForeignSubstrate failed: %v", err)
	}
	
	// Clean up after test
	defer os.RemoveAll(shadowPath)

	// Verify the directory exists
	info, err := os.Stat(shadowPath)
	if err != nil || !info.IsDir() {
		t.Fatalf("Shadow path %s was not created or is not a directory", shadowPath)
	}

	// Verify it contains a .git directory (is a valid clone)
	gitPath := filepath.Join(shadowPath, ".git")
	gitInfo, err := os.Stat(gitPath)
	if err != nil || !gitInfo.IsDir() {
		t.Fatalf(".git directory not found in shadow path, clone failed")
	}
}
