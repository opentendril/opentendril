# Component: Conductor — the orchestration engine that runs sequences, sprouts, and their git, isolation, credential, and selection machinery.

## Purpose

`cmd/stem/internal/conductor` is the **aggregation layer** that turns a Sequence spec into executed, isolated, git-committed sprout work. It owns the Vascular Cambium (DAG sequence runner with recovery), polyglot sprout growth inside terraria, parallel speculative execution and phenotypic fitness selection, substrate configuration and credential resolution, git state externalization (commit / push / PR / branch lifecycle), genomic evolution and plasmid injection, epigenetic chronicling, and dynamic orchestration helpers (model routing, meristem planning, recursive debugger budding). It is a broad multi-subsystem package (~36 non-test source files, on the order of ~13k lines and ~365 exported symbols) driven by CLI verbs and receptors; it is not a leaf.

## Responsibilities

**Does:**

- **Sequence execution & recovery** — Load, save, list, and run YAML Sequences as a concurrency-limited DAG; support `halt` / `retry` / `pause` failure modes; bud recursive Debugger steps on verifier and macrophage failures; append meristem-produced dynamic steps; run command-only verifier steps without an LLM (`sequence.go`, `orchestrator.go`, `meristem.go`, `adaptation.go` for history-driven genome work, `reclaim.go` for owned isolation-branch cleanup after runs).
- **Parallel sprouting & phenotypic selection** — Map-Reduce parallel steps (`parallel: true` with meristem branching + MycelialMerge), first-fit multi-phenotype races (`phenotypesCount` + `fitnessTest`), and generational genetic selection (`SelectionConfig` with population, fitness goal, survivor fraction) (`parallelsprouting.go`, `selection.go`, `macrophage.go` for deterministic post-sprout Go fuzz verification).
- **Sprout execution (polyglot) & isolation** — Host-side ReAct loop (`Sprout`) over a terrarium tool session; Docker (or configured terrarium provider) orchestration with shadow worktrees, host stash/restore, plasmid staging, and merge-back; Seed build/verify loops and Stoma egress-gated one-shot commands (`sprout.go`, `seed.go`, `stoma.go`, `docker.go`, `checkout.go`, `workspace.go`).
- **Substrate configuration** — Parse and resolve named substrates, credential profiles, auth/sign/identity/checkout/commit modes from `substrates.yaml` (`substratesconfig.go`).
- **Git operations & state externalization** — Commit (local or GitHub GraphQL API), push, PR, branch create/list/prune; status classification; default-branch resolution and protection floor; owned-ref registry and reclaim; change-scope for scoped CI; protected-path merge gating (`git.go`, `gitstatus.go`, `gitbranchlifecycle.go`, `defaultbranch.go`, `ownedrefs.go`, `changescope.go`, `protectedpaths.go`).
- **Credentials & GitHub auth** — Resolve PAT / SSH / none / GitHub App credentials; mint installation tokens; materialize git auth into process env without putting secrets on argv or in `.git/config`; trusted-tier definition search paths; plasmid Ed25519 signatures; scoped verification sequence generation (`credentials.go`, `githubapp.go`, `githubauth.go`, `trustedtier.go`, `scopedci.go`, `plasmidsig.go`).
- **Genomics: evolution, disclosure, plasmids** — Find and inject plasmids into the workspace genome; stage genotype-allowlisted plasmids into terraria; adapt traits from commit history; record genomic fitness and evolve/prune rules and plasmids (`plasmids.go`, `adaptation.go`, with genotype data from `data/genotypes` and runtime context loading in `sprout.go` / `docker.go`).
- **Chronicler & outcomes** — Distill successful run diffs into epigenetic genome notes; auto-reduce and optionally auto-push genome updates; classify sprout endings (`complete`, `no-changes`, `failed`, `timed-out`, `skipped`, `no-engagement`) and publish lifecycle events (`chronicler.go`, `outcome.go`).
- **Dynamic orchestration & LLM tooling** — ToolCall / ToolDefinition / ToolResponse protocol types; task complexity assessment and dynamic model routing; rhizome facade for repo/memory maps injected before growth; mesh-delegated push when graft env is set; verifier and go-test-json reporting (`sprout.go` tool types, `assessor.go`, `rhizomefacade.go`, `meshpush.go`, `verifier.go`, `gotestjson.go`).

**Does not:**

- Own CLI, REST, or MCP surface wiring — those live in `cmd/stem` adapters and `cmd/stem/internal/receptors` (and Core projections).
- Own the LLM HTTP client or provider registry — it **calls** `roots/llm` (`llm.Client`, tier/provider resolution, coordinator client) rather than implementing transport.
- Own session storage, telemetry sinks, or the event bus implementation — it **publishes** through `eventbus` when a bus is supplied.
- Own crypto primitives for mesh keys or graft WebSocket transport — it uses `mesh` for delegated push and optional plasmid signature verification.
- Own terrarium provider implementations — it configures and drives `terrarium` sessions (images, mounts, timeouts).
- Own AST parsing / repo-map storage — `rhizomefacade.go` opens rhizome stores and writes maps into the mount path for sprouts.

## Public interface

The package exports on the order of **~365** symbols. The load-bearing surface is grouped by subsystem below (not an exhaustive symbol dump).

| Subsystem | Primary entrypoints |
| --- | --- |
| Sequence execution & recovery | `Sequence`, `SequenceStep`, `SelectionConfig`, `SequenceStepRunner`, `SequenceRunOptions`, `LoadSequence`, `SaveSequence`, `ResolveSequencePath`, `ListSequenceFiles`, `RunSequence`, `ErrRequiresReview`; `Orchestrator` (`orchestrator.go`); reclaim: `ReclaimOwnedRef`, `ReclaimOwnedRefs`, `ReclaimUnusedIsolationBranch`, `ReclaimOutcome` |
| Parallel sprouting & phenotypic selection | Step fields and runners invoked from `defaultSequenceStepRunnerWithOpts` (`parallel`, `phenotypesCount`, `Selection`); selection config lives on `SequenceStep`; macrophage fuzz is internal to step post-processing (`macrophage.go`) |
| Sprout execution & isolation | `Sprout`, `ActionResult`, `SproutRunReport`, `DockerOrchestrator`, `NewDockerOrchestrator`, `(*DockerOrchestrator).RunSprout`, `SeedExecution`, `SeedRunResult`, `RunSeed`, `StomaExecution`, `StomaFetch`, `StomaResult`, `EgressPolicy`, `NewEgressPolicy`, `RunStoma`, `LockWorkspace`, `DelegatedWorkspace`, `ResolveDelegatedWorkspace` |
| Substrate configuration | `SubstratesConfig`, `SubstrateSpec`, `AuthSpec`, `SignSpec`, `IdentitySpec`, `CheckoutSpec`, `CredentialProfile`, `LoadSubstratesConfig`, `ResolveSubstrate`, `SubstrateConfigCandidates` |
| Git & state externalization | `GitCommitExecution` / `GitCommitResult` / `RunGitCommit`, `GitPushExecution` / `RunGitPush`, `GitPRExecution` / `RunGitPullRequest`, `GitBranchExecution` / `RunGitBranch`, `GitStatusExecution` / `RunGitStatus`, `GitBranchListExecution` / `RunGitBranchList`, `GitPruneExecution` / `RunGitPrune`, `ResolveDefaultBranch`, `ResolveDefaultBranchLocal`, `DefaultBranchResolution`, `OwnedRef`, `RegisterOwnedRef`, `ForgetOwnedRef`, `OwnedRefsFor`, `TestScopeForChanges`, `ResolveSubstrateCredential`, `AssessDefaultBranchCommit` |
| Credentials & GitHub auth | `CredentialMethod`, `ResolvedCredential`, `ResolvedSigning`, `ResolvedIdentity`, `AppCredential`, `CommitModeLocal` / `CommitModeAPI`, `DefinitionSearchPath`, `TrustedDefinitionDirs`, `QuarantineDir`, `SignPlasmid`, `WritePlasmidSignature`, `VerifyPlasmidSignature`, `GenerateScopedVerificationSequence` |
| Genomics & plasmids | `FindPlasmidSource`, `InjectPlasmidIntoGenome`, `CopyMarkdownFile`, `CommitSample`, `(*EpigeneticChronicler).AdaptFromHistory`, `RecordGenomicFitness`, `EvolveGenome` |
| Chronicler & outcomes | `EpigeneticChronicler`, `NewEpigeneticChronicler`, `(*EpigeneticChronicler).TranscribeLearnings`, `(*EpigeneticChronicler).ReduceGenomeFile`, `GenomicFitness`, `SproutRunReport`, outcome constants (`SproutOutcomeComplete`, `…NoChanges`, `…Failed`, `…TimedOut`, `…Skipped`, `…NoEngagement`), `ErrSproutTimedOut` |
| Dynamic orchestration & LLM tooling | `ToolArgument`, `ToolDefinition`, `ToolCall`, `ToolResponse`, `AssessTaskComplexity`, `RouteTask`, `GenerateRepoMap`, `GenerateMemoryMap`, `ReportGoTestRun`, `ErrVerifierBlocked` |

## Dependencies

**Fan-out (internal packages counted by the Architecture Audit epic as 4):**

| Package | Role |
| --- | --- |
| `cmd/stem/internal/eventbus` | Sequence and sprout lifecycle events (`sequence-*-*`, sprout emerged/matured/withered, terrarium OOM/timeout). |
| `cmd/stem/internal/terrarium` | Ephemeral tool sessions, verifier/fuzz command runs, file copy-in/out. |
| `cmd/stem/internal/rhizome` | Repo-map and memory-map generation via `rhizomefacade.go`. |
| `cmd/stem/internal/mesh` | Optional delegated push (`meshpush.go`); plasmid public-key verification when staging signed plasmids. |

**Additional non-internal fan-out (not in the epic’s internal fan-out=4 count):**

- **`roots/llm`** — Conductor reaches into the LLM client **directly** from the orchestration layer (`docker.go` resolves clients, `sprout.go` drives the ReAct loop, `assessor.go` / `chronicler.go` / `adaptation.go` / `parallelsprouting.go` call coordinator or tiered clients). This is a structural coupling: orchestration depends on the concrete LLM package rather than a narrow port.
- **`data/genotypes`** — Genotype instruction payloads and deny-plasmid lists consumed when constructing a `Sprout`.
- External modules: `gopkg.in/yaml.v3` (sequences and substrates), stdlib `os/exec` / `net/http` for git and GitHub API paths.

**Fan-in:**

- **`cmd/stem` CLI verbs** — Broad set including `cmdsequence`, `cmdsprout`, `cmdgit`, `cmdgitsetup`, `cmdseed`, `cmdstoma`, `cmdadapt`, `cmdgenome`, `cmdmesh`, `cmdplasmid`, `cmdrepomap`, `cmdhardiness`, `cmdverdict`, `cmdserve` (and related wiring/tests). These adapters decode transport and call conductor entrypoints.
- **`cmd/stem/internal/receptors`** — `mcp.go`, `sessions.go` (and related seed/sequence receptor paths) invoke conductor for MCP/session-driven growth.
- **`cmd/stem/internal/core` boundary** — `conductor` appears in `forbiddenCoreImports` in `boundary_test.go`. Core must never import conductor; adapters inject execution behind ports.

**Boundary note:** Conductor is **driven by** adapters (CLI, receptors, serve). It does **not** import Core, REST, or MCP types. Capability governance and parity live outside this package; conductor is the execution engine those surfaces call into.

## Limitations

**Structural — multi-responsibility mega-package.** Conductor spans orchestration, git, terrarium isolation, credentials, selection, chronicling, genomics, and model routing in one Go package. It is not a single clear responsibility and is a first-class candidate for a future split (sequence runner, sprout runtime, git/credentials, genomics/chronicler). File gravity reinforces this: `sequence.go` alone is ~2.2k lines; `docker.go` ~1.7k; together they dominate the package.

**Sequence runner complexity** (`sequence.go`). One file owns YAML schema, DAG scheduling, failure policy, meristem dynamic-step parsing, recursive debugger budding, parallel/vascular/phenotypic/selection dispatch, genotype inference from step IDs, and event publishing. That concentration makes behavior hard to reason about and review in isolation.

**Isolation can degrade to the host workspace** (`docker.go`). When shadow worktree creation fails, the orchestrator logs a warning and continues on the active workspace rather than failing closed. Parallel and selection paths require git; non-git substrates disable state externalization and cannot classify file-change outcomes honestly.

**Direct `roots/llm` coupling.** Orchestration constructs and configures LLM clients inside conductor instead of accepting an injected client port everywhere. Cost-tier routing and coordinator resolution are therefore entangled with sprout/sequence code paths (conductor-side of cost optimization lives here; client defaults live in `roots/llm`).

**Credential ambient fallback** (`credentials.go`, `githubauth.go`). Unspecified / PAT paths still accept ambient `GITHUB_TOKEN` / `GITHUB_PERSONAL_ACCESS_TOKEN` for github.com remotes when a substrate does not pin auth. That preserves legacy ergonomics but is a fail-open toward host identity. SSH materialization uses `StrictHostKeyChecking=accept-new` (`gitSSHCommand`) so first-seen hosts are accepted without an interactive prompt.

**Empty auth can materialize as “no env”** (`materializeGitAuth`). PAT/unspecified with an empty token returns `nil` env rather than a hard error in some paths, deferring failure to the later git operation.

**Default-branch protection is floor-based when unknown** (`defaultbranch.go`). If the default branch cannot be resolved, `main` and `master` are treated as protected — deliberately wider protection, never a guess of “the” default name for non-protection purposes.

**Macrophage and verifier depend on dedicated images and host module cache** (`macrophage.go`, `verifier.go`). Fuzz/verification terraria need prebuilt images and a readable host `GOMODCACHE` mount because the terrarium has no network; misconfigured host caches fail closed with module-resolution errors.

**Trusted-tier collapse** (`trustedtier.go`). When the Stem control plane and workspace `.tendril` resolve to the same directory, **nothing** is trusted — correct for safety when the Stem runs inside the edited repo, but surprising if operators expected control-plane privilege there.

**Genome side effects are best-effort** (`chronicler.go`). Auto-reduction and auto-push of epigenetic updates log warnings and continue on failure rather than failing the originating sprout.

**Phenotypic “first success wins” vs generational selection.** The older `phenotypesCount` race cancels siblings on first fitness pass; `SelectionConfig` runs a full generational GA. Both coexist; operators must know which step field they set.

## Design & rationale

Conductor exists because OpenTendril keeps **state in git** and **execution in terraria**, with the host Stem owning the ReAct loop. The package is the place where those choices meet: a Sequence YAML (or dynamically appended steps) names work; the runner schedules it; each step grows one or more sprouts against a substrate; outcomes are committed, selected, verified, and chronicled.

**Sequence execution & Vascular Cambium.** Sequences are DAGs with `dependsOn` (plus legacy `depends_on` normalized away), `concurrencyLimit`, and `onFailure` ∈ {halt, retry, pause}. Parallel ready steps share a worker pool; when `concurrencyLimit > 1`, individual steps also run in shadow worktrees and merge back via phloem-style `--no-ff` merge with abort-on-conflict (`runParallelSequenceStep`). Pause mode is interactive on a TTY and poll-based when headless. Status is written back into the sequence YAML so runs can resume. Meristem steps may emit JSON step lists that `appendDynamicSteps` re-indexes into the live DAG. Verifier- and macrophage-named steps that fail (non-command steps) may bud a recursive Debugger step up to a depth limit, then re-queue the failed step — self-healing without a separate control plane.

**Parallel sprouting & selection.** Three related mechanisms share isolation primitives (stash, shadow worktree, `DisableMergeBack`):

1. **Map-Reduce parallel** (`parallel: true`) — coordinator branches the transcript into sub-tasks; a pool grows sprouts; a MycelialMerge consensus step reconciles survivors (a withered sibling does not fail the step if another matures).
2. **First-fit phenotypes** (`phenotypesCount > 1`) — temperature-spread speculative runs; first fitness-passing branch merges; others cancel.
3. **Generational genetic selection** (`selection:`) — population × generations, numeric fitness parse, survivor breeding with parent-diff excerpts, AlphaPhenotype graft.

Macrophage steps after the sprout turn run **deterministic** `go test -fuzz` in a dedicated image; crash detection is exit-code and panic-marker based, never an LLM self-report.

**Stateless polyglot sprouts.** The LLM loop runs on the host (`Sprout.Run`); the terrarium is a tool executor over stdin/stdout JSON (`ToolCall` / `ToolResponse`). Genotype context and genome context shape the system prompt; deny-plasmid lists filter tools. Docker orchestrator resolves substrate plans, isolates work, injects repo maps, stages plasmids, commits and merges (unless disabled), and classifies outcomes. Seed loops alternate build sprouts with verify commands; Stoma runs a single command with optional host-side egress fetches under an allowlist.

**Substrate configuration.** Named substrates carry URL/path, branch, auth, signing, identity, checkout mode, commit mode, default-branch protection (opt-out), and optional credential profiles. Secrets are references (env names, key paths), never values in YAML. Read-only substrates allow local edits inside the terrarium but block merge-back.

**State externalization.** Successful work is a git commit (local or GitHub API createCommitOnBranch), not a durable in-memory session. Owned isolation branches are registered so empty protective branches can be reclaimed. Protected paths refuse merges that touch kernel files. Scoped CI derives package lists from the module graph of changed files.

**Credentials.** Method-typed resolution (pat/ssh/none/app) plus GitHub App JWT → installation token minting keeps long-lived secrets off the happy path when App auth is configured. Token helpers inject credentials only via process environment.

**Genomics & chronicler.** Plasmids are markdown traits injected into `.tendril/genome`; fitness scores reinforce or prune rules/plasmids; adaptation mines commit history through the coordinator into trait bullets; the chronicler transcribes post-run diffs into epigenetic learnings and may reduce/push the genome.

**Dynamic orchestration & cost.** Coordinator vs worker clients, `AssessTaskComplexity` / `RouteTask`, and step-level model overrides implement the dual-client and tiering story on the conductor side. Rhizome maps keep sprout context structural rather than whole-tree. Mesh graft intercepts auto-push when configured so local Stems need not hold durable remote write credentials.

### Legacy-doc reconciliation

| Legacy doc | Subsystem it maps to | As-built vs plan | Retired in this PR? |
| --- | --- | --- | --- |
| `DESIGN-SEQUENCE-RUNNER` | Sequence execution & recovery | **Matches** core DAG, concurrency, halt/retry/pause, YAML schema, CLI/MCP drive. Detached async is adapter/serve concern; runner itself is in-process. | **yes** |
| `DESIGN-VASCULAR-CAMBIUM` | Sequence execution & recovery (parallel steps) | **Matches** shadow worktrees, `DisableMergeBack`, phloem `--no-ff` merge with abort-on-conflict, `depends_on` legacy field, meristem genotype naming. | **yes** |
| `DESIGN-PHENOTYPIC-SELECTION` | Parallel sprouting & phenotypic selection | **Matches** first-fit multi-phenotype race with temperature spread and fitness test. **Diverged / extended:** generational `SelectionConfig` GA and Map-Reduce `parallel` steps are additional shipped mechanisms not fully specified in the original first-fit plan. | **yes** |
| `DESIGN-SUBSTRATE-CONFIG` | Substrate configuration (+ credentials) | **Matches** named substrates, env-referenced secrets, readonly, remote clone, extended auth/sign/checkout/profile/app. **Extended:** commit mode API, protectDefaultBranch opt-out, provider/command for terrarium backend. | **yes** |
| `DESIGN-STATE-EXTERNALIZATION` | Git operations & state externalization | **Matches** commit/push as durable state, status files, isolation branches. **Extended:** GraphQL API commits, owned-ref reclaim, protected paths, default-branch resolver with protection floor, branch list/prune, scoped CI. | **yes** |
| `DESIGN-GENOMIC-EVOLUTION` | Genomics + chronicler | **Matches** epigenetic transcription, fitness, evolve/prune. Chronicler and adaptation live in conductor; genome CLI is adapter-thin. | **yes** |
| `DESIGN-GENOTYPE-DISCLOSURE` | Genomics (genotype context / plasmids) | **Matches** genotype-driven instructions and plasmid allow/deny staging into terraria; genotype payloads remain under `data/genotypes`. | **yes** |
| `DESIGN-DYNAMIC-ORCHESTRATION` | Dynamic orchestration & LLM tooling (+ sequence recovery) | **Matches** dual client (coordinator/worker), meristem dynamic steps, recursive debugger budding, role genotypes. Commands-vs-views distinction for `/ws` remains an adapter/Core concern (see as-built Core/server docs); conductor publishes events but does not own the stream. | **yes** |
| `DESIGN-STATELESS-SPROUT` | Sprout execution & isolation | **Matches** host ReAct + stateless terrarium tool protocol; polyglot images. Full client/image matrix remains co-owned with later `roots/llm` / sprout image audit. | **yes** — retired by the sprouts audit |
| `DESIGN-COST-OPTIMIZATION` | Dynamic orchestration (conductor-side tiering/routing) | **Matches** tier assessment and routing hooks in conductor; **default model tables and provider resolution** remain authoritative in `roots/llm`. | **yes** — retired by the roots/llm audit |
