# Wave B — Local-Model Exercise Notes

Running OpenTendril's first "real work" tendrils with a **local** model
(`qwen2.5-coder:7b` via Ollama), to gauge how a small local model performs as the
sprout brain. The LLM runs host-side (Ollama on `localhost:11434`); the terrarium
is network-isolated (`--network none`) and only executes tools.

## Environment
- Terrarium images present: `opentendril-go`, `opentendril-typescript`,
  `opentendril-python`, `opentendril-sandbox`, etc. (no image build needed).
- Provider: `DEFAULT_LLM_PROVIDER=local`, `LOCAL_MODEL_NAME=qwen2.5-coder:7b`,
  `LOCAL_INFERENCE_URL=http://localhost:11434/v1`.

## B2 — PR-check bot (`scripts/pr-check.sh <PR> qwen2.5-coder:7b`) ✅
Reviewed a real PR (#234) end-to-end: `gh` fetched the diff → sprout ran in a
`--network none` terrarium → the 7B model produced a review, written to
`.tendril/pr-check-<PR>.md` (dry-run; never posted).

**Result:** the model's summary was **accurate** — it correctly identified the
credential-helper refactor, GitHub App auth, the "token only in the environment"
property, and the test coverage.

**7B limitations observed:**
- **Format adherence is weak.** The `pr-reviewer` genotype asks for
  `SUMMARY / FINDINGS (severity-tagged) / VERDICT`; the model instead returned a
  prose summary wrapped in a `{"response": "..."}` JSON blob.
- **Low critical rigor.** It summarized rather than *critiqued* — no findings or
  risks surfaced. Good for "explain this PR", weaker for "catch bugs".

**Takeaways:** great for comprehension/summaries; for a gating PR-review you want
either a larger local model (`qwen2.5-coder:14b`) or a cloud coordinator, and/or
a stricter genotype prompt + output validation. Plumbing is solid.

## Known glitch
- `make install` failed with `mv: cannot stat 'cmd/stem/tendril'` even though
  `go build ./cmd/stem` succeeds — the `stem` target's build output wasn't where
  `install` expected it. Worth a follow-up; direct `go build -o … ./cmd/stem`
  works in the meantime.
