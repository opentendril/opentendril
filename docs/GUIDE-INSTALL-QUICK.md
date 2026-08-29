# Quick install

Two postures. They are not interchangeable.

**Local / evaluation** is LOCAL / SINGLE-PRINCIPAL. The Stem runs as this
account. It does not claim the governed boundary.

**Governed** is a separately owned Stem. It is qualified only on the matrix
below. WSL and macOS are not governed.

The deeper hardened/manual reference — invariants, rationale, the full
procedure, variations, and troubleshooting — is
[GUIDE-INSTALL.md](./GUIDE-INSTALL.md). This page does not repeat those
topics.

## Platforms

**Local / evaluation**

- Linux amd64
- Linux arm64
- WSL2 amd64
- WSL2 arm64
- macOS Intel
- macOS Apple Silicon

**Governed**

- Ubuntu 24.04 LTS
- Linux amd64
- systemd
- rootless Docker

## Local / evaluation

This path requires no repository checkout, Go, or Make. It installs the full
`tendril` executable at `~/.local/bin/tendril`. It is not a governed
installation.

```bash
curl -fsSL \
  https://github.com/opentendril/opentendril/releases/latest/download/install.sh \
  | sh
```

If the installer says `~/.local/bin` is not on PATH, add it for this shell as
printed.

Then only this initialization sequence:

```bash
tendril hardiness
tendril init
```

`tendril hardiness` reports; it does not gate. After `tendril init`, the first
session is [GUIDE-QUICKSTART.md](./GUIDE-QUICKSTART.md).

## Governed

Download `install.sh` and `checksums.txt` from the governed release below,
verify the installer against the checksum file, then run it with
administrative authority. Name the ordinary Pollinator-hosting account
explicitly — not `root`, and not `tendril`.

Do **not** pipe the installer into `sudo sh`.

```bash
RELEASE=v0.3.6
curl -fsSL -o install.sh \
  "https://github.com/opentendril/opentendril/releases/download/${RELEASE}/install.sh"
curl -fsSL -o checksums.txt \
  "https://github.com/opentendril/opentendril/releases/download/${RELEASE}/checksums.txt"
grep 'install.sh$' checksums.txt | sha256sum -c
```

Read the verified `install.sh` before privileged execution. Then:

```bash
RELEASE=v0.3.6
sudo sh install.sh \
  --governed \
  --pollinator-user <ordinary-user> \
  --version "${RELEASE}"
```

The installer does not run `tendril init` and does not start the Stem. Complete
configuration as the Stem, from `/home/tendril`.

An LLM is required before `tendril init`. Local
[Ollama](https://ollama.com) is the worked default. A clean machine does not
include it.

**If using local Ollama**, install it from the
[public Linux instructions](https://docs.ollama.com/linux) and pull the
default local model:

```bash
# [root] local Ollama only
curl -fsSL https://ollama.com/install.sh | sh
```

```bash
# local Ollama only
sudo -u tendril -H bash -lc 'ollama pull llama3.2'
```

**If using a cloud provider**, do not install Ollama and do not pull a
model. Have a supported credential ready for the wizard (Anthropic, OpenAI,
xAI, or Google).

The wizard detects a running Ollama instance at `localhost:11434` — including
one with no models pulled yet — and defaults to fully local inference. If none
is reachable, it asks for a cloud provider and its key.

Both paths create `/home/tendril/.env` before the wizard. `tendril init`
writes to `~/.tendril/.env` when no `./.env` exists, and the Stem reads
`./.env`. These commands are safe if the installer already created the file.

```bash
sudo -u tendril -H bash -lc 'touch /home/tendril/.env && chmod 600 /home/tendril/.env'

sudo -u tendril -H bash -lc 'cd /home/tendril && export DOCKER_HOST=unix:///run/user/$(id -u tendril)/docker.sock && /home/tendril/.local/bin/tendril init'

sudo -u tendril -H bash -lc 'cd /home/tendril && /home/tendril/.local/bin/tendril hardiness'
```

Git connection, the explicit `seed.grow` / `sprout.watch` grant, and Pollinator
credentials still require Botanist intent — Stage 5 and Stage 6 of
[GUIDE-INSTALL.md](./GUIDE-INSTALL.md). Configure those grants before starting
the Stem. When configuration is complete, start it:

```bash
# [root]
systemctl enable --now tendril
```

The first session after that is [GUIDE-QUICKSTART.md](./GUIDE-QUICKSTART.md):
dispatch a Seed with the Pollinator credential, watch that Phytomer, and review
the Git Fruit. `main` stays unchanged until a human merges.
