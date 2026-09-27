#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd "${script_dir}/.." && pwd)"
verifier="${script_dir}/check-release-artifacts.sh"
tmp_root="$(mktemp -d "${TMPDIR:-/tmp}/release-artifact-test.XXXXXX")"
dist="${tmp_root}/dist"
version="$(cat "${repo_root}/VERSION")"
source_sha="aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
passes=0
failures=0
trap 'rm -rf "${tmp_root}"' EXIT

pass() {
  echo "ok: $1"
  passes=$((passes + 1))
}

fail() {
  echo "FAIL: $1"
  failures=$((failures + 1))
}

write_greenhouse_archive() {
  local mode="${1:-valid}"
  python3 - "${dist}/opentendril-greenhouse-linux-amd64.tar.gz" "${version}" "${source_sha}" "${mode}" <<'PY'
import gzip
import io
import json
import sys
import tarfile

path, version, revision, mode = sys.argv[1:]
tag = "opentendril-greenhouse:" + version
labels = {
    "org.opencontainers.image.version": version,
    "org.opencontainers.image.revision": revision,
}
if mode == "wrong-version":
    labels["org.opencontainers.image.version"] = "9.9.9"
if mode == "wrong-revision":
    labels["org.opencontainers.image.revision"] = "b" * 40

config = {
    "architecture": "amd64",
    "os": "linux",
    "config": {"Labels": labels},
    "rootfs": {"type": "layers", "diff_ids": ["sha256:" + "c" * 64]},
}
config_bytes = json.dumps(config, separators=(",", ":")).encode()
layer_bytes = b"fixture image layer\n"
manifest = {
    "Config": "image-config.json",
    "RepoTags": [tag],
    "Layers": ["layer/layer.tar"],
}
manifest_bytes = json.dumps([manifest] * (2 if mode == "multiple-images" else 1)).encode()

if mode == "malformed":
    with open(path, "wb") as output:
        output.write(b"not a gzip Docker image archive\n")
    raise SystemExit(0)

with open(path, "wb") as output:
    with gzip.GzipFile(filename="", mode="wb", fileobj=output, mtime=0) as compressed:
        with tarfile.open(fileobj=compressed, mode="w:") as archive:
            for name, content in (
                ("manifest.json", manifest_bytes),
                ("image-config.json", config_bytes),
                ("layer/", b""),
                ("layer/layer.tar", layer_bytes),
            ):
                info = tarfile.TarInfo(name)
                info.size = len(content)
                if name.endswith("/"):
                    info.type = tarfile.DIRTYPE
                archive.addfile(info, io.BytesIO(content))
PY
}

write_checksums() {
  (
    cd "${dist}"
    sha256sum \
      install.sh \
      opentendril-linux-amd64.tar.gz \
      opentendril-linux-arm64.tar.gz \
      opentendril-darwin-amd64.tar.gz \
      opentendril-darwin-arm64.tar.gz \
      opentendril-greenhouse-linux-amd64.tar.gz \
      > checksums.txt
  )
}

reset_fixture() {
  rm -rf "${dist}"
  mkdir -p "${dist}"
  printf 'fixture installer\n' > "${dist}/install.sh"
  for archive in \
    opentendril-linux-amd64.tar.gz \
    opentendril-linux-arm64.tar.gz \
    opentendril-darwin-amd64.tar.gz \
    opentendril-darwin-arm64.tar.gz
  do
    printf 'fixture platform archive: %s\n' "${archive}" > "${dist}/${archive}"
  done
  write_greenhouse_archive
  write_checksums
}

expect_failure() {
  local label="$1"
  if bash "${verifier}" "${dist}" "${version}" "${source_sha}" >/dev/null 2>&1; then
    fail "${label}"
  else
    pass "${label}"
  fi
}

reset_fixture
if bash "${verifier}" "${dist}" "${version}" "${source_sha}" >/dev/null; then
  pass "valid release asset set, checksum, and image metadata are accepted"
else
  fail "valid release asset set, checksum, and image metadata are accepted"
fi

rm "${dist}/opentendril-greenhouse-linux-amd64.tar.gz"
expect_failure "missing Greenhouse artifact is rejected"

reset_fixture
printf 'corruption' >> "${dist}/opentendril-greenhouse-linux-amd64.tar.gz"
expect_failure "checksum-invalid Greenhouse artifact is rejected"

reset_fixture
printf 'unlisted drift\n' > "${dist}/unexpected.txt"
expect_failure "unexpected release asset is rejected"

reset_fixture
tail -n 1 "${dist}/checksums.txt" >> "${dist}/checksums.txt"
expect_failure "duplicate checksum entry is rejected"

for mode in malformed wrong-version wrong-revision multiple-images; do
  reset_fixture
  write_greenhouse_archive "${mode}"
  write_checksums
  expect_failure "${mode} Greenhouse archive is rejected"
done

echo
if [ "${failures}" -gt 0 ]; then
  echo "${failures} release artifact test(s) failed, ${passes} passed."
  exit 1
fi
echo "All ${passes} release artifact tests passed."
