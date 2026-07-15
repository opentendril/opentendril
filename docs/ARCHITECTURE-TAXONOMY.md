# OpenTendril Architecture Taxonomy

> **See also:** [SYNTHETIC-TAXONOMY.md](../SYNTHETIC-TAXONOMY.md) · [ARCHITECTURE.md](../ARCHITECTURE.md) · [SECURITY.md](../SECURITY.md)

This document provides visual architecture diagrams for OpenTendril, mapping the biological taxonomy to actual system components, security boundaries, and execution flows.

---

## 1. The Living Organism — Full Execution Pipeline

```mermaid
graph TD
    classDef host fill:#1a1a2e,stroke:#4a90d9,stroke-width:2px,color:#e0e0e0
    classDef terrarium fill:#0f3d2e,stroke:#2e8b57,stroke-width:2px,color:#b8f0cc
    classDef genetics fill:#2c1654,stroke:#9b59b6,stroke-width:2px,color:#e8daef
    classDef external fill:#3d2200,stroke:#e67e22,stroke-width:2px,color:#fdebd0
    classDef security fill:#4a0000,stroke:#c0392b,stroke-width:2px,color:#fadbd8
    classDef data fill:#1a3a4a,stroke:#2980b9,stroke-width:2px,color:#d6eaf8

    User(["👤 Developer / CI / Trigger"])

    subgraph HostSystem ["🖥️  Host System  (outside all Terrariums)"]
        Stem["🌿 Stem\nGo Orchestrator\n(tendril serve)"]
        HT["⚡ Hormonal Triggers\nSecurity Gates\n(pre-flight bash hooks)"]
        Rhizome[("🍄 Rhizome\nSQLite AST Index\n(background scanner)")]
        SystemConf["🔒 System Config\n~/.opentendril/\nor /etc/opentendril/\n(immutable from Terrariums)"]
        WorkspaceConf["📁 Workspace Config\n./.tendril/\n(read by Stem; privileged\nfields stripped before use)"]
        MergeBack["🔀 Merge Back\nStash pop → Worktree merge\n→ Commit → PR"]
    end

    subgraph LLMNetwork ["🌐 Mycorrhizal Network  (LLM Providers)"]
        LLM["🧠 LLM\nClaude / GPT / Ollama\n(all reasoning happens here;\noutside the plant)"]
    end

    User -->|"Request / Transcript"| Stem
    Stem --> HT
    HT -->|"Growth Blocked"| Abort["❌ Abort"]
    HT -->|"Growth Allowed"| Orchestrator

    subgraph Orchestrator ["🔬 Orchestrator  (inside Stem)"]
        SeqRunner["📋 Sequence Runner\nChains multiple Sprout steps"]
        PhenoSelect["🔀 Phenotypic Selection\nParallel speculative variants\n→ first to pass tests wins"]
        SubstratePlan["📐 Substrate Resolver\nLoads substrates.yaml\nStrips provider/command\nif source = Workspace Config"]
    end

    SubstratePlan -->|"provider: docker (default)"| DockerProvider
    SubstratePlan -->|"provider: host\n(only if source = System Config\nAND TENDRIL_ALLOW_HOST_EXECUTION=true)"| HostProvider

    subgraph TerrariumLayer ["🏺 Terrarium Provider"]
        DockerProvider["🐳 Docker Terrarium\n(default; network-isolated,\nread-only rootfs, no-new-privs)"]
        GVisor["🛡️ gVisor Terrarium\n(enterprise; syscall interception)"]
        Firecracker["🔥 Firecracker MicroVM\n(zero-trust; full VM isolation)"]
        HostProvider["⚠️ Host Terrarium\n(direct os/exec on host;\nrequires explicit operator opt-in)"]
    end

    subgraph TerrariumSandbox ["🌱 Terrarium Sandbox  (ephemeral; destroyed after each Sprout)"]
        direction TB
        Sprout["🌱 Sprout\n(the ephemeral worker;\ngrows, touches code, then is shed)"]

        subgraph GeneticInjection ["💉 Genetic Injection"]
            direction LR
            Genotype["🧬 Genotype\n(Core Identity / System Prompt)\ne.g. code-writer, github-ops"]
            Plasmid1["💊 Plasmid\n(Tool: bash terminal)"]
            Plasmid2["💊 Plasmid\n(Tool: GitHub MCP)"]
            Plasmid3["💊 Plasmid\n(Skill: repomap.md)"]
        end

        Genotype -->|"defines identity & deny-list"| Sprout
        Plasmid1 -.->|"injected if not in deny-list"| Sprout
        Plasmid2 -.->|"injected if not in deny-list"| Sprout
        Plasmid3 -.->|"injected if not in deny-list"| Sprout
    end

    subgraph SubstrateLayer ["🌍 Substrate  (target codebase)"]
        Repo["📦 Repository\n(ephemeral git worktree checkout)"]
    end

    DockerProvider --> TerrariumSandbox
    GVisor --> TerrariumSandbox
    Firecracker --> TerrariumSandbox
    HostProvider -->|"Direct host process\n(no container isolation)"| Sprout

    Sprout <-->|"LLM completions\n(Stem-mediated)"| LLM
    Sprout -->|"File edits / commands"| Repo
    Sprout -->|"Reads index"| Rhizome
    TerrariumSandbox --> MergeBack

    SystemConf -.->|"Trusted: provider, command,\nsystem genotypes"| SubstratePlan
    WorkspaceConf -.->|"Untrusted for privileged\nfields (stripped by Stem)"| SubstratePlan

    class Stem,HT,Rhizome,MergeBack,SeqRunner,PhenoSelect,SubstratePlan host
    class DockerProvider,GVisor,Firecracker,TerrariumSandbox,Sprout terrarium
    class Genotype,Plasmid1,Plasmid2,Plasmid3,GeneticInjection genetics
    class LLM,LLMNetwork external
    class HostProvider,Abort,SystemConf security
    class Repo,SubstrateLayer,WorkspaceConf data
```

---

## 2. System Genotype Hierarchy

System Genotypes ship with OpenTendril and cannot be modified by agents operating within a Terrarium Sandbox. Workspace Genotypes are user-defined and can be customised per project.

```mermaid
graph LR
    classDef sys fill:#4a0000,stroke:#c0392b,color:#fadbd8
    classDef ws fill:#1a3a4a,stroke:#2980b9,color:#d6eaf8

    subgraph SystemGenotypes ["🔒 System Genotypes  (shipped with OpenTendril — immutable from Terrariums)"]
        SG1["🤖 setup-guide\nPlasmids: none\nDeny: all execution\n→ RAG over docs only"]
        SG2["🐙 github-ops\nPlasmids: github-mcp\nDeny: filesystem, bash, docker, host\n→ Issues & PRs only"]
        SG3["📖 codebase-reader\nPlasmids: rhizome-read, repomap\nDeny: filesystem writes, bash\n→ Read-only Q&A"]
        SG4["🩺 sre-monitor\nPlasmids: eventbus-read, docker-stats\nDeny: filesystem, bash\n→ Metrics & alerts only"]
        SG5["🧪 test-runner\nPlasmids: bash (test cmds only)\nDeny: source file writes\n→ Run & report tests"]
    end

    subgraph WorkspaceGenotypes ["📁 Workspace Genotypes  (user-defined in .tendril/genotypes/)"]
        WG1["✏️ code-writer\nPlasmids: bash, filesystem\n→ Default coding Sprout"]
        WG2["🔍 debugger\nPlasmids: bash, filesystem\n→ Bug investigation"]
        WG3["✅ verifier\nPlasmids: bash (read-only)\n→ Code review & test runs"]
        WG4["🧠 thinker\nPlasmids: none\n→ Planning & specification only"]
    end

    class SG1,SG2,SG3,SG4,SG5 sys
    class WG1,WG2,WG3,WG4 ws
```

> 📖 **See also:** the Pre-built System Genotypes design RFC.

---

## 3. Security Trust Boundaries

This diagram shows what each layer can and cannot access. A Terrarium Sandbox can **never** read System Config or modify Genotype definitions — these are host-level files that are never mounted into containers.

```mermaid
graph TB
    classDef trusted fill:#4a0000,stroke:#c0392b,color:#fadbd8
    classDef untrusted fill:#1a3a4a,stroke:#2980b9,color:#d6eaf8
    classDef stem fill:#1a1a2e,stroke:#4a90d9,color:#e0e0e0

    A["🔒 System Config\n(~/.opentendril/ or /etc/opentendril/)\n\n✅ Trusted for: provider, command,\n   system genotypes, host execution\n❌ Never mounted into Terrariums"]
    B["📁 Workspace Config\n(./.tendril/ · substrates.yaml)\n\n⚠️ Untrusted for: provider, command\n   (these fields are stripped by the Stem\n   before any Terrarium launch)"]
    C["🌱 Terrarium Sandbox\n(Docker / gVisor / Firecracker)\n\n❌ Cannot read System Config\n❌ Cannot modify Genotypes\n❌ Cannot escalate to Host provider\n✅ Can only modify its mounted workspace"]

    A -->|"Stem reads on boot\n(not mounted into containers)"| StemCore["🌿 Stem\n(host process — the trust anchor)"]
    B -->|"Stem reads from workspace\n(privileged fields stripped at parse time)"| StemCore
    StemCore -->|"Mounts only workspace repo\n+ approved Plasmids from Genotype manifest"| C

    class A trusted
    class B,C untrusted
    class StemCore stem
```

---

## Key Definitions Reference

| Biological Term | IT Equivalent | Security Role |
|---|---|---|
| **Stem** | Go Orchestrator | Trust anchor. Runs on host. Owns all security decisions. |
| **Mycorrhizal Network** | LLM (Claude/GPT/Ollama) | External. Never touches the host filesystem. |
| **Hormonal Trigger** | Security Gate / Hook | Pre-flight bash script. Blocks execution before Terrarium boots. |
| **Sprout** | One Terrarium execution run — the ephemeral worker | Created fresh, destroyed on completion. The executor: dumb by design, only follows Genotype instructions. *(Formerly split as "Tendril" for the worker loop; now a single organ.)* |
| **Terrarium** | Docker / gVisor / Firecracker / Host | Isolation layer. Defines the security boundary. |
| **Genotype** | System Prompt / Persona | Defines identity, capability scope, and **deny-list** of blocked tools. |
| **Plasmid** | Tool / RAG context block | Modular capability injected at runtime. Blocked by Genotype deny-list. |
| **Substrate** | Target Repository | The codebase being operated on. Mounted read-write into the Terrarium. |
| **Rhizome** | Background AST Index | Host-resident scanner. Read-only from Terrariums via API. |
| **Phenotype** | Speculative parallel variant | Multiple concurrent Sprouts racing to pass tests. |
