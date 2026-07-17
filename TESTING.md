# Verification in OpenTendril

Three tiers verify this repository, and **they do not verify the same things**. Reading one tier's green as another's is the mistake this document exists to prevent.

Every tier obeys one invariant:

> **Fail closed.** Anything not positively verified — a test that skipped, a package left unrun, a change whose scope could not be derived — counts as **not green**. Green is earned only by applicable tests that actually ran and passed.

A skipped test is therefore never a pass. `go test` exits 0 when tests skip, so no tier judges on the exit code alone; each parses the `go test -json` event stream through one shared verdict.

---

## 1. The three tiers

| Tier | Command | Speed | What it can reach |
|---|---|---|---|
| **Sealed local verifier** | `tendril sequence scoped-ci` | seconds | No network, no Docker daemon, no `/dev/kvm`, workspace read-only |
| **Unrestricted local run** | `go test ./...` on your machine | ~minutes | Whatever your machine has |
| **Remote gate** | GitHub Actions, on every pull request | ~minutes | Network, Docker daemon, `/dev/kvm` — everything |

Only the remote gate runs every test. It is the tier that authorizes merges.

---

## 2. What each verdict actually claims

This is the important part.

### `scoped-ci` GREEN

> Every test **applicable to what changed** that **can run inside the seal** ran and passed.

It does **not** mean the full suite passes. It does not mean the change is mergeable. The sealed verifier has no network, no Docker daemon and no `/dev/kvm`, so any test needing those cannot run there — and `scoped-ci` also narrows to the packages your change touched plus their reverse-dependents. It is a fast pre-flight, not a merge gate.

### `scoped-ci` BLOCKED

> Something applicable to your change **could not be verified here**.

This is not a failure and it is not noise. It means the sealed tier is refusing to imply something it did not check. For changes to `conductor` or `terrarium` this is the **normal, expected** result: those packages contain tests that need network, a Docker daemon, or `/dev/kvm`, and the seal denies all three by design. The remote gate verifies them.

BLOCKED means *escalate*, and the escalation target is real: the remote gate runs those tests, with zero skips.

### `scoped-ci` FAILED

> A test that ran, failed. This one is about your change.

### Remote gate GREEN

> **Every test in the repository ran and passed.** Zero skips.

This is the strong claim, and the only one that should be read as "verified".

---

## 3. Why the sealed tier cannot simply match the remote gate

It is tempting to ask why the local tier does not just run everything. The answer is that its restrictions *are* the point, and lifting them removes the property being sought.

- **No network** (`--network none`) — the verifier executes code it is in the middle of judging. Network access is the difference between a verifier and an exfiltration path.
- **No Docker daemon** — reaching one from inside a container means mounting the host's daemon socket. That is Docker-out-of-Docker: host root by another name. **It is rejected permanently, not deferred.**
- **No `/dev/kvm`** — passing the device in is a real capability grant on the most security-sensitive container in the project. It was measured and declined: it would remove 6 of 10 skips and leave the verdict at BLOCKED anyway, because the network and Docker tests would still be unreachable. Paying a capability grant for no change in verdict is a bad trade.
- **Read-only workspace** — a verifier reads and reports; it never writes what it judges.

A sealed verifier granted network, a daemon and KVM is not a sealed verifier. It is a slower, less isolated copy of the remote gate.

So the sealed tier reporting BLOCKED for the execution layer is not a defect to fix. It is a sealed tier accurately reporting that it is sealed.

---

## 4. The tiers share one verdict, deliberately

`conductor.ReportGoTestRun` is the single skip-aware judgement. The sealed verifier calls it directly; the remote gate calls it through `tendril verdict go-test`. Neither reimplements it.

Two implementations would drift into disagreeing about what "green" means, and one of them guards merges. The verdict needs the event stream **and** the process exit code together: a non-zero exit with no failure event — a compile error — must still fail, and a judge reading only the stream would wave it through.

---

## 5. Running each tier

### Sealed local verifier — the everyday pre-flight

```bash
tendril sequence scoped-ci               # against origin/main
tendril sequence scoped-ci --base <ref>  # against another base
```

Derives what changed, expands to affected packages plus their in-repo reverse-dependents, and runs build, vet, gofmt and the scoped tests inside the sealed terrarium. If the change set cannot be derived it widens to the whole module: uncertainty resolves toward more testing, never less.

### Unrestricted local run

```bash
go test ./...          # everything your machine can reach
make test-stem         # the same suite inside a sterile golang container
```

`make test-stem` trades reach for sterility: the container has no Docker daemon, so daemon-dependent tests skip there. On a disposable CI runner that trade makes no sense, which is why the remote gate does **not** use it.

Tests requiring `/dev/kvm` need a firecracker binary and a bootstrapped guest:

```bash
tendril terrarium init-firecracker    # pinned kernel + rootfs, unprivileged
go test ./cmd/stem/internal/terrarium/ -run Firecracker -v
```

### Opt-in live checks

Some checks reach a real third party and cannot run without credentials. They are excluded from the default build behind a tag, rather than skipping — a skip would block the gate forever:

```bash
OPENTENDRIL_LIVE_APP_ID=... OPENTENDRIL_LIVE_APP_KEY=... OPENTENDRIL_LIVE_APP_REPO=... \
  go test -tags livegithub ./cmd/stem/internal/conductor/ -run TestGithubAppLive
```

A build tag rather than a list of tolerated skips: a list is fail-open the moment it drifts, whereas exclusion is compile-time and reviewable.

### The remote gate

Runs automatically on every pull request. `Native PR Gate` is the required status check — **its job name must not change**, or the required context never reports and every pull request becomes unmergeable.

Its path filter is fail closed: a changed path matching no rule requires **every** job. Only documentation is allowlisted as inert.

---

## 6. Real commands

The Makefile does not orchestrate every check, and pretending otherwise is how this document previously came to describe commands that did not exist.

| Command | Purpose |
|---|---|
| `make stem` | Build Stem for the current platform |
| `make test-stem` | Go tests in a sterile Docker container |
| `make test-all` | Currently an alias for `test-stem` |
| `make check-all` | Clean build plus all tests |
| `make help` | The authoritative list |

`make help` is the source of truth for targets. This table is a convenience and can rot; the Makefile cannot.

---

## 7. Reading a result correctly

| You see | It means | Do |
|---|---|---|
| `scoped-ci` GREEN | The seal verified what it could reach, for what you changed | Push. The remote gate is still the real check |
| `scoped-ci` BLOCKED | The seal could not verify something applicable | Normal for `conductor`/`terrarium`. Read the skip list; let the remote gate verify |
| `scoped-ci` FAILED | A test ran and failed | Fix it — this one is yours |
| Remote gate GREEN | Every test ran and passed, zero skips | Verified |
| Remote gate BLOCKED | A test skipped where nothing should skip | Investigate. Something became unrunnable that used to run |

The last row matters most. On the remote gate, a skip is a regression in coverage, and the verdict treats it as one.
