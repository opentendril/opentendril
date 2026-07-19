package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"strings"
	"testing"
)

const testGiB = uint64(1) << 30

func TestAssessFitVerdict(t *testing.T) {
	cases := []struct {
		name       string
		usableVRAM uint64
		required   uint64
		ram        uint64
		ramKnown   bool
		want       string
	}{
		{"comfortable fit in vram", 23 * testGiB, 10 * testGiB, 32 * testGiB, true, verdictFitsFully},
		{"exactly at headroom boundary", 10 * testGiB, 9 * testGiB, 32 * testGiB, true, verdictFitsFully},
		{"just past headroom boundary", 10 * testGiB, 9*testGiB + 1, 32 * testGiB, true, verdictFitsTight},
		{"exactly fills usable vram", 10 * testGiB, 10 * testGiB, 32 * testGiB, true, verdictFitsTight},
		{"spills into ram", 10 * testGiB, 20 * testGiB, 32 * testGiB, true, verdictGPUCPUSplit},
		{"exactly fills vram plus ram", 10 * testGiB, 42 * testGiB, 32 * testGiB, true, verdictGPUCPUSplit},
		{"too big even with ram", 10 * testGiB, 42*testGiB + 1, 32 * testGiB, true, verdictExceedsMachine},
		{"no gpu fits in ram", 0, 8 * testGiB, 32 * testGiB, true, verdictCPUOnly},
		{"no gpu exactly fills ram", 0, 32 * testGiB, 32 * testGiB, true, verdictCPUOnly},
		{"no gpu too big for ram", 0, 33 * testGiB, 32 * testGiB, true, verdictExceedsMachine},
		{"no gpu, ram unknown", 0, 8 * testGiB, 0, false, verdictUnknown},
		{"vram overflow, ram unknown", 10 * testGiB, 20 * testGiB, 0, false, verdictUnknown},
		{"fits vram, ram unknown", 23 * testGiB, 10 * testGiB, 0, false, verdictFitsFully},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := assessFitVerdict(tc.usableVRAM, tc.required, tc.ram, tc.ramKnown)
			if got != tc.want {
				t.Errorf("assessFitVerdict(%d, %d, %d, %v) = %q, want %q",
					tc.usableVRAM, tc.required, tc.ram, tc.ramKnown, got, tc.want)
			}
		})
	}
}

func TestAssessUsableVRAM(t *testing.T) {
	if got := assessUsableVRAM(0); got != 0 {
		t.Errorf("no GPU should yield 0 usable VRAM, got %d", got)
	}
	if got := assessUsableVRAM(assessVRAMReserveBytes); got != 0 {
		t.Errorf("VRAM equal to the reserve should yield 0, got %d", got)
	}
	if got := assessUsableVRAM(24 * testGiB); got != 23*testGiB {
		t.Errorf("24 GiB VRAM should yield 23 GiB usable, got %d", got)
	}
}

func TestAssessRequiredBytes(t *testing.T) {
	size := 5 * testGiB // roughly an 8B model at Q4

	zeroCtx := assessRequiredBytes(size, 0)
	if zeroCtx != size {
		t.Errorf("zero context should add no KV cache: got %d, want %d", zeroCtx, size)
	}

	small := assessRequiredBytes(size, 2048)
	large := assessRequiredBytes(size, 8192)
	if small <= size || large <= small {
		t.Errorf("required bytes must grow with context: size=%d ctx2048=%d ctx8192=%d", size, small, large)
	}
	// ~8B params * 16 KiB/token * 8192 tokens is on the order of 1 GiB; the
	// estimate should stay in a sane band, not be off by orders of magnitude.
	kv := large - size
	if kv < testGiB/2 || kv > 4*testGiB {
		t.Errorf("KV estimate out of expected band: %d bytes", kv)
	}

	if got := assessRequiredBytes(size, -100); got != size {
		t.Errorf("negative context must clamp to zero KV: got %d", got)
	}
}

func TestParseNvidiaSMIMemory(t *testing.T) {
	t.Run("single gpu", func(t *testing.T) {
		gpus, err := parseNvidiaSMIMemory("24576, 23001\n")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(gpus) != 1 {
			t.Fatalf("expected 1 GPU, got %d", len(gpus))
		}
		if gpus[0].TotalBytes != 24576<<20 || gpus[0].FreeBytes != 23001<<20 {
			t.Errorf("unexpected GPU memory: %+v", gpus[0])
		}
	})

	t.Run("multi gpu pools independently", func(t *testing.T) {
		gpus, err := parseNvidiaSMIMemory("24576, 20000\n11264, 11000\n")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(gpus) != 2 {
			t.Fatalf("expected 2 GPUs, got %d", len(gpus))
		}
		if gpus[1].TotalBytes != 11264<<20 {
			t.Errorf("second GPU total = %d, want %d", gpus[1].TotalBytes, uint64(11264)<<20)
		}
	})

	t.Run("empty output yields no gpus", func(t *testing.T) {
		gpus, err := parseNvidiaSMIMemory("\n")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(gpus) != 0 {
			t.Errorf("expected no GPUs, got %d", len(gpus))
		}
	})

	t.Run("garbage lines are skipped", func(t *testing.T) {
		for _, input := range []string{"NVIDIA-SMI has failed\n", "abc, def\n"} {
			gpus, err := parseNvidiaSMIMemory(input)
			if err != nil {
				t.Errorf("parseNvidiaSMIMemory(%q) returned error: %v", input, err)
			}
			if len(gpus) != 0 {
				t.Errorf("parseNvidiaSMIMemory(%q) = %d GPUs, want 0", input, len(gpus))
			}
		}

		gpus, err := parseNvidiaSMIMemory("24576, 23001\n[N/A], [N/A]\n11264, 11000\n")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(gpus) != 2 {
			t.Errorf("mixed batch: expected 2 GPUs with the [N/A] line skipped, got %d", len(gpus))
		}
	})
}

func TestParseMemAvailableBytes(t *testing.T) {
	meminfo := "MemTotal:       65536000 kB\nMemFree:         1234567 kB\nMemAvailable:   32768000 kB\n"
	got, err := parseMemAvailableBytes(meminfo)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if want := uint64(32768000) * 1024; got != want {
		t.Errorf("parseMemAvailableBytes = %d, want %d", got, want)
	}

	if _, err := parseMemAvailableBytes("MemTotal: 1 kB\n"); err == nil {
		t.Error("expected error when MemAvailable is missing")
	}
}

func TestParseOllamaTags(t *testing.T) {
	body := `{"models":[{"name":"llama3.2:latest","size":2019393189},{"name":"qwen2.5-coder:14b","size":8988124315}]}`
	models, err := parseOllamaTags([]byte(body))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(models) != 2 {
		t.Fatalf("expected 2 models, got %d", len(models))
	}
	if models[0].Name != "llama3.2:latest" || models[0].SizeBytes != 2019393189 {
		t.Errorf("unexpected first model: %+v", models[0])
	}

	if _, err := parseOllamaTags([]byte("not json")); err == nil {
		t.Error("expected error for malformed body")
	}
}

func TestListOllamaModels(t *testing.T) {
	t.Run("success strips /v1 and hits /api/tags", func(t *testing.T) {
		var gotPath string
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotPath = r.URL.Path
			fmt.Fprint(w, `{"models":[{"name":"m","size":123}]}`)
		}))
		t.Cleanup(server.Close)

		models, err := listOllamaModels(context.Background(), server.URL+"/v1")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if gotPath != "/api/tags" {
			t.Errorf("request path = %q, want %q", gotPath, "/api/tags")
		}
		if len(models) != 1 {
			t.Fatalf("expected 1 model, got %d", len(models))
		}
		if models[0].Name != "m" || models[0].SizeBytes != 123 {
			t.Errorf("unexpected model: %+v", models[0])
		}
	})

	t.Run("non-200 response errors", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "boom", http.StatusInternalServerError)
		}))
		t.Cleanup(server.Close)

		if _, err := listOllamaModels(context.Background(), server.URL+"/v1"); err == nil {
			t.Error("expected error for a 500 response")
		}
	})

	t.Run("unreachable candidate exhausts and errors", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
		url := server.URL
		server.Close() // nothing listening: connection refused

		if _, err := listOllamaModels(context.Background(), url+"/v1"); err == nil {
			t.Error("expected error when every candidate is unreachable")
		}
	})
}

func TestAssessHumanBytes(t *testing.T) {
	cases := []struct {
		in   uint64
		want string
	}{
		{0, "0 B"},
		{(1 << 20) - 1, "1048575 B"},
		{1 << 20, "1 MiB"},
		{(1 << 30) - 1, "1024 MiB"},
		{1 << 30, "1.0 GiB"},
		{24 * testGiB, "24.0 GiB"},
	}
	for _, tc := range cases {
		if got := assessHumanBytes(tc.in); got != tc.want {
			t.Errorf("assessHumanBytes(%d) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestPrintAssessReportEmpty(t *testing.T) {
	origStdout := os.Stdout
	t.Cleanup(func() { os.Stdout = origStdout })

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stdout = w
	printAssessReport(assessReport{Hardware: assessHardware{CPUCores: 4}, Models: []assessModelFit{}})
	w.Close()
	os.Stdout = origStdout
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read captured stdout: %v", err)
	}

	if !strings.Contains(string(out), "No local models found.") {
		t.Errorf("expected empty-report notice, got: %s", out)
	}
}

func TestBuildAssessReport(t *testing.T) {
	hw := assessHardware{
		GPUs:              []assessGPU{{TotalBytes: 24 * testGiB, FreeBytes: 20 * testGiB}},
		VRAMTotalBytes:    24 * testGiB,
		CPUCores:          16,
		RAMAvailableBytes: 32 * testGiB,
		RAMAvailableKnown: true,
	}
	models := []assessModel{
		{Name: "small", SizeBytes: 2 * testGiB},
		{Name: "huge", SizeBytes: 200 * testGiB},
	}

	report := buildAssessReport(hw, models, 8192)
	if len(report.Models) != 2 {
		t.Fatalf("expected 2 model fits, got %d", len(report.Models))
	}
	if report.Models[0].Verdict != verdictFitsFully {
		t.Errorf("small model verdict = %q, want %q", report.Models[0].Verdict, verdictFitsFully)
	}
	if report.Models[1].Verdict != verdictExceedsMachine {
		t.Errorf("huge model verdict = %q, want %q", report.Models[1].Verdict, verdictExceedsMachine)
	}
	if report.Models[0].RequiredBytes <= report.Models[0].SizeBytes {
		t.Errorf("required bytes should include KV overhead beyond model size")
	}

	// No GPU and no models: the report must still be well-formed (cpu-only path).
	empty := buildAssessReport(assessHardware{CPUCores: 4, RAMAvailableBytes: 8 * testGiB}, nil, 8192)
	if empty.Models == nil || len(empty.Models) != 0 {
		t.Errorf("expected empty non-nil model list, got %#v", empty.Models)
	}
}

func TestProbeAssessHardwareDegrades(t *testing.T) {
	origGPUs := assessQueryGPUs
	origMem := assessMemAvailable
	t.Cleanup(func() {
		assessQueryGPUs = origGPUs
		assessMemAvailable = origMem
	})

	// Both probes fail: the command must degrade to a CPU-only, RAM-unknown view.
	assessQueryGPUs = func(ctx context.Context) ([]assessGPU, error) {
		return nil, fmt.Errorf("no nvidia-smi")
	}
	assessMemAvailable = func() (uint64, error) {
		return 0, fmt.Errorf("no /proc")
	}
	hw := probeAssessHardware(context.Background())
	if hw.CPUCores != runtime.NumCPU() {
		t.Errorf("CPUCores = %d, want %d", hw.CPUCores, runtime.NumCPU())
	}
	if hw.VRAMTotalBytes != 0 {
		t.Errorf("VRAMTotalBytes = %d, want 0", hw.VRAMTotalBytes)
	}
	if hw.RAMAvailableBytes != 0 {
		t.Errorf("RAMAvailableBytes = %d, want 0", hw.RAMAvailableBytes)
	}
	if hw.RAMAvailableKnown {
		t.Error("RAMAvailableKnown = true, want false when the RAM probe fails")
	}
	if len(hw.GPUs) != 0 {
		t.Errorf("expected 0 GPUs, got %d", len(hw.GPUs))
	}

	// Both probes succeed: values must propagate.
	assessQueryGPUs = func(ctx context.Context) ([]assessGPU, error) {
		return []assessGPU{{TotalBytes: 24 * testGiB, FreeBytes: 20 * testGiB}}, nil
	}
	assessMemAvailable = func() (uint64, error) {
		return 32 * testGiB, nil
	}
	hw = probeAssessHardware(context.Background())
	if hw.VRAMTotalBytes != 24*testGiB {
		t.Errorf("VRAMTotalBytes = %d, want %d", hw.VRAMTotalBytes, 24*testGiB)
	}
	if hw.VRAMFreeBytes != 20*testGiB {
		t.Errorf("VRAMFreeBytes = %d, want %d", hw.VRAMFreeBytes, 20*testGiB)
	}
	if hw.RAMAvailableBytes != 32*testGiB {
		t.Errorf("RAMAvailableBytes = %d, want %d", hw.RAMAvailableBytes, 32*testGiB)
	}
	if !hw.RAMAvailableKnown {
		t.Error("RAMAvailableKnown = false, want true when the RAM probe succeeds")
	}
	if len(hw.GPUs) != 1 {
		t.Errorf("expected 1 GPU, got %d", len(hw.GPUs))
	}
}

func TestRunAssessCmdJSONWithDeadProvider(t *testing.T) {
	origGPUs := assessQueryGPUs
	origMem := assessMemAvailable
	origList := assessListModels
	origStdout := os.Stdout
	t.Cleanup(func() {
		assessQueryGPUs = origGPUs
		assessMemAvailable = origMem
		assessListModels = origList
		os.Stdout = origStdout
	})

	assessQueryGPUs = func(ctx context.Context) ([]assessGPU, error) {
		return []assessGPU{{TotalBytes: 24 * testGiB, FreeBytes: 20 * testGiB}}, nil
	}
	assessMemAvailable = func() (uint64, error) {
		return 32 * testGiB, nil
	}
	assessListModels = func(ctx context.Context, baseURLOverride string) ([]assessModel, error) {
		return nil, fmt.Errorf("connection refused")
	}

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stdout = w
	runAssessCmd(context.Background(), []string{"--json"})
	w.Close()
	os.Stdout = origStdout
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read captured stdout: %v", err)
	}

	var report struct {
		Warnings []string `json:"warnings"`
	}
	if err := json.Unmarshal(out, &report); err != nil {
		t.Fatalf("unmarshal captured JSON: %v\noutput: %s", err, out)
	}
	if len(report.Warnings) == 0 {
		t.Fatalf("expected a warning about the failed model listing, got none; output: %s", out)
	}
	if !strings.Contains(report.Warnings[0], "could not list local models") ||
		!strings.Contains(report.Warnings[0], "connection refused") {
		t.Errorf("warning does not mention the listing failure: %q", report.Warnings[0])
	}
}
