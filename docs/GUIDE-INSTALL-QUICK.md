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

Pin **v0.3.1**. Download `install.sh` and `checksums.txt` from that release,
verify the installer against the checksum file, then run it with
administrative authority. Name the ordinary Pollinator-hosting account
explicitly — not `root`, and not `tendril`.

Do **not** pipe the installer into `sudo sh`.

```bash
curl -fsSL -o install.sh \
  https://github.com/opentendril/opentendril/releases/download/v0.3.1/install.sh
curl -fsSL -o checksums.txt \
  https://github.com/opentendril/opentendril/releases/download/v0.3.1/checksums.txt
grep 'install.sh$' checksums.txt | sha256sum -c
```

Read the verified `install.sh` before privileged execution. Then:

```bash
sudo sh install.sh \
  --governed \
  --pollinator-user <ordinary-user> \
  --version v0.3.1
```

The installer does not run `tendril init` and does not start the Stem. Complete
configuration as the Stem, from `/home/tendril`:

```bash
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

The first session after that is [GUIDE-QUICKSTART.md](./GUIDE-QUICKSTART.md).
