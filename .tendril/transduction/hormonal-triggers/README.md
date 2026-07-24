# 🧪 Hormonal Triggers (Pre-Tendril Security)

In botany, a **Hormonal Trigger** occurs at the plant's Meristem (the cellular manufacturing hub). It is the genetic "switch" that evaluates if a dormant bud is allowed to mutate and sprout into a Tendril based on its programming and the environment.

In OpenTendril's Pure Micro-Sprout Architecture, this directory serves as your **Outbound Biological Firewall**. It acts as an absolute gatekeeper for the creation of any new Sprout.

## Operator-Authored Policies

Triggers are entirely operator-authored and git-tracked. You must place your executable policy scripts directly in this directory. Triggers are **not** written by the LLM or dynamically uploaded via any API.

## Execution Contract

When the Stem invokes the `sprout_tendril` capability (The Meristem), the framework serializes the requested `genotype` and `transcript` into a JSON payload.

Each script in this directory is executed with the path to this JSON file as its first argument (`$1`).

- **Allow:** Script exits with `0` (Success).
- **Block:** Script exits with `>0` (Failure). The sub-tendril is aborted, and the script's `stderr` output is fed directly back into the orchestrator's LLM context window so it understands why the biological action failed.

### Environment Restrictions (Terrarium)

All triggers must be executable **POSIX-sh scripts** (`#!/bin/sh`). 

Scripts are executed inside an isolated `alpine:3.20` Terrarium, which imposes the following constraints:
- No network access (NetworkMode: None).
- Read-only file system mount.
- Runs as a non-root user.
- Resource-capped.
- Hard 30-second execution timeout.
- No `bash` available (scripts starting with `#!/bin/bash` will fail to execute).

## Gate Modes (`TENDRIL_TRIGGERS_MODE`)

The behavior of the hormonal trigger gate is controlled by the `TENDRIL_TRIGGERS_MODE` environment variable:
- **`enforce` (Default):** All triggers must pass. If the triggers directory is missing or unreadable, the system **fails closed** and blocks all sprouts.
- **`disabled`:** Explicitly applies no policy. All sprouts are allowed.
- *Any invalid value* falls back to `enforce`.

## Host Execution Override

You can bypass the Terrarium isolation by setting `TENDRIL_ALLOW_HOST_EXECUTION=true`. This is an explicit, knowing opt-out that forces triggers to run directly on the host environment instead of inside the isolated Terrarium.

## Usage

Simply drop executable POSIX-sh scripts (`.sh`) into this directory and ensure they are marked as executable (`chmod +x script.sh`).
