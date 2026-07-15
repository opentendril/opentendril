package main

import (
	"context"
	"fmt"
	"os"

	"github.com/opentendril/core/cmd/stem/internal/conductor"
)

func runRepoMapCmd(args []string) {
	if len(args) > 0 {
		switch args[0] {
		case "-h", "--help", "help":
			printRepoMapUsage()
			return
		}
	}

	mapMarkdown, err := conductor.GenerateRepoMap(context.Background(), mustGetwd())
	if err != nil {
		fmt.Fprintf(os.Stderr, "❌ Failed to generate repo map: %v\n", err)
		os.Exit(1)
	}

	fmt.Print(mapMarkdown)
}

func printRepoMapUsage() {
	fmt.Println("Usage: tendril repomap")
	fmt.Println("  repomap  Generate and print the active repository map")
}

func mustGetwd() string {
	wd, err := os.Getwd()
	if err != nil {
		return "."
	}
	return wd
}
