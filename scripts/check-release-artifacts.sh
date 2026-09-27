#!/usr/bin/env bash
set -euo pipefail

if [ "$#" -ne 3 ]; then
  echo "Usage: $0 <dist-directory> <version> <source-sha>" >&2
  exit 2
fi

python3 - "$1" "$2" "$3" <<'PY'
import gzip
import hashlib
import json
import pathlib
import re
import sys
import tarfile


def fail(message):
    raise SystemExit("release artifact verification failed: " + message)


dist = pathlib.Path(sys.argv[1])
version = sys.argv[2]
source_sha = sys.argv[3]
if not re.fullmatch(r"[0-9]+\.[0-9]+\.[0-9]+(?:[-+][0-9A-Za-z.-]+)?", version):
    fail("invalid canonical version")
if not re.fullmatch(r"[0-9a-f]{40}|[0-9a-f]{64}", source_sha):
    fail("source revision must be a full Git object ID")
if not dist.is_dir():
    fail("release staging directory is missing")

platform_archives = [
    "opentendril-linux-amd64.tar.gz",
    "opentendril-linux-arm64.tar.gz",
    "opentendril-darwin-amd64.tar.gz",
    "opentendril-darwin-arm64.tar.gz",
]
greenhouse_name = "opentendril-greenhouse-linux-amd64.tar.gz"
checksum_names = ["install.sh", *platform_archives, greenhouse_name]
asset_names = ["checksums.txt", *checksum_names]

entries = list(dist.iterdir())
if any(entry.is_symlink() or not entry.is_file() for entry in entries):
    fail("release staging may contain only regular files")
actual_assets = sorted(entry.name for entry in entries)
if actual_assets != sorted(asset_names):
    fail("release asset set differs from the exact expected set: " + ", ".join(actual_assets))
print("PASS exact release staging asset set")

checksum_path = dist / "checksums.txt"
checksum_lines = checksum_path.read_text(encoding="ascii").splitlines()
if len(checksum_lines) != len(checksum_names):
    fail("checksums.txt must contain exactly one entry for each of six payload assets")

checksums = {}
for line in checksum_lines:
    match = re.fullmatch(r"([0-9a-f]{64})  ([A-Za-z0-9._-]+)", line)
    if not match:
        fail("checksums.txt contains a malformed SHA-256 line")
    digest, name = match.groups()
    if name not in checksum_names or name in checksums:
        fail("checksums.txt contains an unexpected or duplicate asset entry")
    checksums[name] = digest
if set(checksums) != set(checksum_names):
    fail("checksums.txt does not name exactly the six payload assets")

for name in checksum_names:
    hasher = hashlib.sha256()
    with (dist / name).open("rb") as payload:
        for chunk in iter(lambda: payload.read(1024 * 1024), b""):
            hasher.update(chunk)
    digest = hasher.hexdigest()
    if digest != checksums[name]:
        fail("SHA-256 verification failed for " + name)
print("PASS all six payload checksums, including exactly one Greenhouse entry")

archive_path = dist / greenhouse_name
expected_tag = "opentendril-greenhouse:" + version
try:
    with gzip.open(archive_path, "rb") as compressed:
        with tarfile.open(fileobj=compressed, mode="r:") as archive:
            members = archive.getmembers()
            member_map = {}
            for member in members:
                path = pathlib.PurePosixPath(member.name)
                if path.is_absolute() or ".." in path.parts or not member.isfile():
                    fail("Greenhouse Docker archive contains an unsafe or non-file member")
                if member.name in member_map:
                    fail("Greenhouse Docker archive contains duplicate members")
                member_map[member.name] = member

            manifest_member = member_map.get("manifest.json")
            if manifest_member is None:
                fail("Greenhouse Docker archive has no manifest.json")
            manifest = json.load(archive.extractfile(manifest_member))
            if not isinstance(manifest, list) or len(manifest) != 1:
                fail("Greenhouse Docker archive must contain exactly one image")
            image = manifest[0]
            if image.get("RepoTags") != [expected_tag]:
                fail("Greenhouse Docker archive tag does not match VERSION")
            config_name = image.get("Config")
            config_member = member_map.get(config_name)
            if config_member is None:
                fail("Greenhouse Docker archive is missing its image config")
            config = json.load(archive.extractfile(config_member))
            if config.get("os") != "linux" or config.get("architecture") != "amd64":
                fail("Greenhouse image is not linux/amd64")
            labels = (config.get("config") or {}).get("Labels") or {}
            if labels.get("org.opencontainers.image.version") != version:
                fail("Greenhouse image version label does not match VERSION")
            if labels.get("org.opencontainers.image.revision") != source_sha:
                fail("Greenhouse image source revision label does not match the bound checkout")
            layers = image.get("Layers")
            if not isinstance(layers, list) or not layers:
                fail("Greenhouse Docker archive has no image layers")
            if any(layer not in member_map for layer in layers):
                fail("Greenhouse Docker archive is missing a referenced image layer")
except (OSError, EOFError, tarfile.TarError, json.JSONDecodeError, TypeError, AttributeError) as error:
    fail("Greenhouse Docker archive is malformed: " + str(error))

print("PASS Greenhouse Docker archive integrity and single-image manifest")
print("PASS Greenhouse linux/amd64 tag, VERSION, and source revision metadata")
PY
