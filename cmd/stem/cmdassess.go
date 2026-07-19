package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/opentendril/opentendril/roots/llm"
)

// Fit verdicts, ordered from best to worst. Thresholds (documented at
// assessFitVerdict) work on "usable VRAM": pooled VRAM across all GPUs minus a
// fixed OS/driver reserve.
const (
	verdictFitsFully      = "fits-fully"      // loads entirely in VRAM with headroom
	verdictFitsTight      = "fits-tight"      // fits in VRAM, little margin left
	verdictGPUCPUSplit    = "gpu-cpu-split"   // partial CPU offload needed
	verdictCPUOnly        = "cpu-only"        // no usable GPU, runs from system RAM
	verdictExceedsMachine = "exceeds-machine" // does not fit VRAM plus RAM
	verdictUnknown        = "unknown"         // hardware probe incomplete; fit cannot be judged
)

const (
	// assessVRAMReserveBytes is VRAM assumed unavailable to the model: the OS
	// compositor, display buffers, and the CUDA/driver context. 1 GiB is a
	// conservative middle ground between headless (~300 MiB) and desktop (~1.5 GiB).
	assessVRAMReserveBytes = 1 << 30
	// assessHeadroomRatio splits fits-fully from fits-tight: a model is only
	// "fully" comfortable when it needs at most 90% of usable VRAM, leaving
	// >=10% for fragmentation and runtime scratch buffers.
	assessHeadroomRatio = 0.90
	// assessBytesPerParam assumes ~Q4_K quantization (about 0.6 bytes per
	// weight) when deriving a parameter count from the on-disk model size.
	assessBytesPerParam = 0.6
	// assessKVBytesPerTokenPerBParam estimates KV-cache cost: roughly 16 KiB
	// per token per billion parameters (fp16 cache, GQA-era models; e.g. an
	// 8B llama needs ~128 KiB/token).
	assessKVBytesPerTokenPerBParam = 16 * 1024
	// assessDefaultContextTokens is the working context the KV cache is sized
	// for. Local runtimes rarely run models at their advertised maximum
	// context; 8192 matches common Ollama num_ctx defaults. Override via --ctx.
	assessDefaultContextTokens = 8192
)

type assessGPU struct {
	TotalBytes uint64 `json:"totalBytes"`
	FreeBytes  uint64 `json:"freeBytes"`
}

type assessHardware struct {
	GPUs              []assessGPU `json:"gpus"`
	VRAMTotalBytes    uint64      `json:"vramTotalBytes"`
	CPUCores          int         `json:"cpuCores"`
	RAMAvailableBytes uint64      `json:"ramAvailableBytes"`
	RAMAvailableKnown bool        `json:"ramAvailableKnown"`
}

type assessModel struct {
	Name      string `json:"name"`
	SizeBytes uint64 `json:"sizeBytes"`
}

type assessModelFit struct {
	assessModel
	ContextTokens int    `json:"contextTokens"`
	RequiredBytes uint64 `json:"requiredBytes"`
	Verdict       string `json:"verdict"`
}

type assessReport struct {
	Hardware assessHardware   `json:"hardware"`
	Models   []assessModelFit `json:"models"`
}

// Probing seams, overridable in tests so nothing shells out or dials a server.
var (
	assessQueryGPUs    = queryNvidiaSMIGPUs
	assessMemAvailable = readProcMemAvailableBytes
	assessListModels   = listOllamaModels
)

func runAssessCmd(ctx context.Context, args []string) {
	flags := flag.NewFlagSet("assess", flag.ExitOnError)
	jsonOut := flags.Bool("json", false, "Emit the assessment as JSON")
	baseURL := flags.String("url", "", "OpenAI-compatible local server base URL")
	ctxTokens := flags.Int("ctx", assessDefaultContextTokens, "Working context length (tokens) to size the KV cache for")
	flags.Usage = func() {
		printAssessUsage()
		flags.PrintDefaults()
	}
	if err := flags.Parse(args); err != nil {
		os.Exit(1)
	}

	hw := probeAssessHardware(ctx)

	models, err := assessListModels(ctx, *baseURL)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: could not list local models: %v\n", err)
	}

	report := buildAssessReport(hw, models, *ctxTokens)
	if *jsonOut {
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(report); err != nil {
			fmt.Fprintf(os.Stderr, "Failed to encode report: %v\n", err)
			os.Exit(1)
		}
		return
	}
	printAssessReport(report)
}

func buildAssessReport(hw assessHardware, models []assessModel, contextTokens int) assessReport {
	report := assessReport{Hardware: hw, Models: []assessModelFit{}}
	usable := assessUsableVRAM(hw.VRAMTotalBytes)
	for _, model := range models {
		required := assessRequiredBytes(model.SizeBytes, contextTokens)
		report.Models = append(report.Models, assessModelFit{
			assessModel:   model,
			ContextTokens: contextTokens,
			RequiredBytes: required,
			Verdict:       assessFitVerdict(usable, required, hw.RAMAvailableBytes, hw.RAMAvailableKnown),
		})
	}
	return report
}

// assessUsableVRAM pools VRAM across all GPUs (llama.cpp-style tensor split)
// and subtracts the fixed OS/driver reserve once from the pooled total.
func assessUsableVRAM(vramTotal uint64) uint64 {
	if vramTotal <= assessVRAMReserveBytes {
		return 0
	}
	return vramTotal - assessVRAMReserveBytes
}

// assessRequiredBytes estimates the memory a model needs resident: its weights
// plus a KV cache sized for contextTokens. Parameter count is inferred from
// the on-disk size assuming ~Q4 quantization; see the constants above.
func assessRequiredBytes(modelSizeBytes uint64, contextTokens int) uint64 {
	if contextTokens < 0 {
		contextTokens = 0
	}
	paramsBillions := float64(modelSizeBytes) / assessBytesPerParam / 1e9
	kvBytes := paramsBillions * assessKVBytesPerTokenPerBParam * float64(contextTokens)
	return modelSizeBytes + uint64(kvBytes)
}

// assessFitVerdict maps usable VRAM, required bytes, and available RAM to a
// verdict. Thresholds:
//   - fits-fully:      required <= 90% of usable VRAM (headroom retained)
//   - fits-tight:      required <= usable VRAM
//   - gpu-cpu-split:   GPU present, but the overflow must spill into RAM and
//     the total still fits usable VRAM + available RAM
//   - cpu-only:        no usable GPU, model fits in available RAM
//   - exceeds-machine: nothing above holds
//   - unknown:         the RAM probe failed and the decision would depend on RAM
func assessFitVerdict(usableVRAM uint64, requiredBytes uint64, ramAvailable uint64, ramKnown bool) string {
	if usableVRAM == 0 {
		if !ramKnown {
			return verdictUnknown
		}
		if requiredBytes <= ramAvailable {
			return verdictCPUOnly
		}
		return verdictExceedsMachine
	}
	if float64(requiredBytes) <= assessHeadroomRatio*float64(usableVRAM) {
		return verdictFitsFully
	}
	if requiredBytes <= usableVRAM {
		return verdictFitsTight
	}
	if !ramKnown {
		return verdictUnknown
	}
	if requiredBytes <= usableVRAM+ramAvailable {
		return verdictGPUCPUSplit
	}
	return verdictExceedsMachine
}

// probeAssessHardware gathers GPU, CPU, and RAM facts. Every probe degrades
// gracefully: a machine without nvidia-smi is assessed as CPU-only rather
// than failing the command.
func probeAssessHardware(ctx context.Context) assessHardware {
	hw := assessHardware{GPUs: []assessGPU{}, CPUCores: runtime.NumCPU()}
	if gpus, err := assessQueryGPUs(ctx); err == nil {
		hw.GPUs = gpus
		for _, gpu := range gpus {
			hw.VRAMTotalBytes += gpu.TotalBytes
		}
	}
	if ram, err := assessMemAvailable(); err == nil {
		hw.RAMAvailableBytes = ram
		hw.RAMAvailableKnown = true
	} else {
		fmt.Fprintf(os.Stderr, "Warning: could not read available RAM: %v\n", err)
	}
	return hw
}

func queryNvidiaSMIGPUs(ctx context.Context) ([]assessGPU, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "nvidia-smi",
		"--query-gpu=memory.total,memory.free", "--format=csv,noheader,nounits").Output()
	if err != nil {
		return nil, err
	}
	return parseNvidiaSMIMemory(string(out))
}

// parseNvidiaSMIMemory parses `nvidia-smi --query-gpu=memory.total,memory.free
// --format=csv,noheader,nounits` output: one "total, free" line per GPU, in MiB.
func parseNvidiaSMIMemory(output string) ([]assessGPU, error) {
	gpus := []assessGPU{}
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Split(line, ",")
		if len(fields) != 2 {
			continue // skip malformed lines (e.g. "NVIDIA-SMI has failed")
		}
		totalMiB, err := strconv.ParseUint(strings.TrimSpace(fields[0]), 10, 64)
		if err != nil {
			continue // skip non-numeric fields (e.g. "[N/A]")
		}
		freeMiB, err := strconv.ParseUint(strings.TrimSpace(fields[1]), 10, 64)
		if err != nil {
			continue // skip non-numeric fields (e.g. "[N/A]")
		}
		gpus = append(gpus, assessGPU{TotalBytes: totalMiB << 20, FreeBytes: freeMiB << 20})
	}
	return gpus, nil
}

func readProcMemAvailableBytes() (uint64, error) {
	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0, err
	}
	return parseMemAvailableBytes(string(data))
}

// parseMemAvailableBytes extracts MemAvailable (reported in kB) from
// /proc/meminfo content.
func parseMemAvailableBytes(meminfo string) (uint64, error) {
	for _, line := range strings.Split(meminfo, "\n") {
		if !strings.HasPrefix(line, "MemAvailable:") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			return 0, fmt.Errorf("malformed MemAvailable line")
		}
		kb, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil {
			return 0, err
		}
		return kb * 1024, nil
	}
	return 0, fmt.Errorf("MemAvailable not found")
}

// listOllamaModels asks the local provider's native Ollama API for installed
// models and their on-disk sizes, which the OpenAI-compatible /v1 surface does
// not expose. Each configured base URL candidate is tried with its /v1 suffix
// stripped back to the Ollama root.
func listOllamaModels(ctx context.Context, baseURLOverride string) ([]assessModel, error) {
	spec := llm.ResolveLocalProviderSpec()
	applyLLMBaseURLOverride(&spec, baseURLOverride)
	candidates := spec.BaseURLs
	if len(candidates) == 0 {
		candidates = []string{spec.BaseURL}
	}

	client := &http.Client{Timeout: 5 * time.Second}
	var lastErr error
	for _, baseURL := range candidates {
		root := strings.TrimSuffix(strings.TrimRight(strings.TrimSpace(baseURL), "/"), "/v1")
		if root == "" {
			continue
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, root+"/api/tags", nil)
		if err != nil {
			lastErr = err
			continue
		}
		resp, err := client.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			lastErr = fmt.Errorf("GET %s/api/tags: %s", root, resp.Status)
			continue
		}
		if readErr != nil {
			lastErr = readErr
			continue
		}
		models, err := parseOllamaTags(body)
		if err != nil {
			lastErr = err
			continue
		}
		return models, nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("no local provider base URL configured")
	}
	return nil, lastErr
}

// parseOllamaTags decodes an Ollama GET /api/tags response body.
func parseOllamaTags(body []byte) ([]assessModel, error) {
	var payload struct {
		Models []struct {
			Name string `json:"name"`
			Size uint64 `json:"size"`
		} `json:"models"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("decode /api/tags response: %w", err)
	}
	models := make([]assessModel, 0, len(payload.Models))
	for _, m := range payload.Models {
		models = append(models, assessModel{Name: m.Name, SizeBytes: m.Size})
	}
	return models, nil
}

func printAssessReport(report assessReport) {
	hw := report.Hardware
	fmt.Printf("Hardware: %d GPU(s), %s VRAM (%s usable), %d CPU cores, %s RAM available\n\n",
		len(hw.GPUs), assessHumanBytes(hw.VRAMTotalBytes),
		assessHumanBytes(assessUsableVRAM(hw.VRAMTotalBytes)),
		hw.CPUCores, assessHumanBytes(hw.RAMAvailableBytes))

	if len(report.Models) == 0 {
		fmt.Println("No local models found.")
		return
	}
	w := tabwriter.NewWriter(os.Stdout, 2, 4, 2, ' ', 0)
	fmt.Fprintln(w, "MODEL\tSIZE\tCONTEXT\tREQUIRED\tVERDICT")
	for _, fit := range report.Models {
		fmt.Fprintf(w, "%s\t%s\t%d\t%s\t%s\n",
			fit.Name, assessHumanBytes(fit.SizeBytes), fit.ContextTokens,
			assessHumanBytes(fit.RequiredBytes), fit.Verdict)
	}
	w.Flush()
}

func assessHumanBytes(b uint64) string {
	const gib = 1 << 30
	const mib = 1 << 20
	switch {
	case b >= gib:
		return fmt.Sprintf("%.1f GiB", float64(b)/gib)
	case b >= mib:
		return fmt.Sprintf("%.0f MiB", float64(b)/mib)
	default:
		return fmt.Sprintf("%d B", b)
	}
}

func printAssessUsage() {
	fmt.Println("Usage:")
	fmt.Println("  tendril assess [--json] [--url http://localhost:11434/v1] [--ctx 8192]")
}
