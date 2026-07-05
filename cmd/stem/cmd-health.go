package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/opentendril/core/cmd/stem/internal/eventbus"
	"github.com/opentendril/core/cmd/stem/internal/healthmon"
)

func runHealthCmd(ctx context.Context, args []string) {
	jsonOutput := false
	watch := false
	for _, arg := range args {
		switch strings.TrimSpace(arg) {
		case "--json":
			jsonOutput = true
		case "--watch":
			watch = true
		case "-h", "--help", "help":
			printHealthUsage()
			return
		default:
			fmt.Fprintf(os.Stderr, "Unknown health flag: %s\n", arg)
			printHealthUsage()
			os.Exit(1)
		}
	}

	bus := eventbus.New()
	monitor := newDefaultHealthMonitor(bus, 30*time.Second)

	for {
		report := monitor.RunOnce(ctx)
		if watch {
			fmt.Print("\033[H\033[2J")
		}
		if jsonOutput {
			writeHealthJSON(report)
		} else {
			writeHealthTable(report)
		}
		if !watch {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(30 * time.Second):
		}
	}
}

func newDefaultHealthMonitor(bus *eventbus.Bus, interval time.Duration) *healthmon.Monitor {
	monitor := healthmon.New(bus, interval)
	for _, check := range healthmon.DefaultChecks() {
		monitor.RegisterCheck(check)
	}
	return monitor
}

func writeHealthJSON(report healthmon.HealthReport) {
	payload, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "marshal health report: %v\n", err)
		os.Exit(1)
	}
	fmt.Println(string(payload))
}

func writeHealthTable(report healthmon.HealthReport) {
	status := "✅ healthy"
	if !report.Overall {
		status = "❌ degraded"
	}
	fmt.Printf("OpenTendril health: %s\n", status)
	fmt.Printf("Timestamp: %s\n\n", report.Timestamp.Format(time.RFC3339))
	fmt.Printf("%-18s %-4s %s\n", "Check", "", "Message")
	for _, name := range sortedHealthCheckNames(report.Results) {
		result := report.Results[name]
		icon := healthIcon(result)
		fmt.Printf("%-18s %-4s %s\n", name, icon, result.Message)
	}
}

func sortedHealthCheckNames(results map[string]healthmon.CheckResult) []string {
	names := make([]string, 0, len(results))
	for name := range results {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func healthIcon(result healthmon.CheckResult) string {
	if !result.Healthy {
		return "❌"
	}
	if severity, ok := result.Data["severity"].(string); ok && strings.EqualFold(severity, "warning") {
		return "⚠️"
	}
	return "✅"
}

func printHealthUsage() {
	fmt.Println("Usage: tendril health [--json] [--watch]")
}
