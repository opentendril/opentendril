# Release operator procedure

This procedure describes the current governed OpenTendril release path.

## Governing distinction

Merging a pull request to `main` advances development. A release is a deliberate
operation that names one verified `main` commit as an immutable public
OpenTendril version.

Ordinary merges do not publish releases. Publication requires explicit action by
a maintainer. No auto-merge is involved.

## Canonical version authority

`VERSION` is the repository release-identity authority. It contains one stable
`MAJOR.MINOR.PATCH` version. The governed `RELEASE=v...` pins in `README.md`,
`docs/GUIDE-INSTALL-QUICK.md`, and `docs/GUIDE-INSTALL.md` are synchronized
projections of `VERSION`. They are not independent release identity.

Use `scripts/release-version.sh` for version operations:

```bash
scripts/release-version.sh current
scripts/release-version.sh next patch
scripts/release-version.sh next minor
scripts/release-version.sh next major
scripts/release-version.sh bump patch
scripts/release-version.sh bump minor
scripts/release-version.sh bump major
scripts/release-version.sh check
```

The commands have these meanings:

- `current` prints the version in `VERSION`.
- `next patch`, `next minor`, and `next major` calculate the next version from
  the current `VERSION` without changing files.
- `bump patch`, `bump minor`, and `bump major` require `VERSION` to equal the
  highest stable release tag found on the configured remote. They update
  `VERSION` and the governed documentation pins only.
- `check` validates `VERSION` against authoritative remote release history and
  validates all governed documentation pins.
- `intended-tag` prints `v` plus the current `VERSION`.
- `publication-state <source-sha>` verifies that the source is current remote
  `main` and reports whether publication is `first-publication` or the exact
  source-match `safe-retry` state.

The helper also supports `intended-tag` and `publication-state` for the
publication workflow. Authoritative published-history checks use the remote
(normally `origin`) through `git ls-remote`; local tags are not consulted.

The version helper's preparation commands do not tag, push, merge, or publish.
Pushing a preparation branch for its normal pull request is separate from
release publication.

## Preparing a release

1. Start from synchronized `main`:

   ```bash
   git switch main
   git pull --ff-only origin main
   ```

2. Decide `patch`, `minor`, or `major` using the convention in [Version
   convention](#version-convention). Inspect the candidate with `next`:

   ```bash
   releasekind=patch
   candidate="$(scripts/release-version.sh next "${releasekind}")"
   printf 'Preparing OpenTendril %s\n' "${candidate}"
   ```

3. Create a normal release-preparation branch:

   ```bash
   git switch -c "release/${candidate}"
   ```

4. Run the matching `bump` command:

   ```bash
   scripts/release-version.sh bump "${releasekind}"
   ```

5. Inspect the resulting changes. The bump is limited to the canonical version
   file and its governed documentation projections:

   ```bash
   git diff -- VERSION README.md docs/GUIDE-INSTALL-QUICK.md docs/GUIDE-INSTALL.md
   git status --short
   ```

6. Run the required validation:

   ```bash
   scripts/release-version.sh check
   git diff --check
   make check-all
   ```

7. Commit normally with the repository's required signature:

   ```bash
   git add VERSION README.md docs/GUIDE-INSTALL-QUICK.md docs/GUIDE-INSTALL.md
   git commit -S -m "chore(release): prepare ${candidate}"
   ```

8. Push the preparation branch and open a normal pull request into `main`:

   ```bash
   git push -u origin HEAD
   ```

   Do not push directly to `main`. Do not automate pull request creation or
   merge.

9. Wait for the normal required checks and maintainer review.

10. The maintainer manually merges the pull request. Merging advances `main`;
    it does not publish the release.

## Publishing

The normal publication path is the GitHub Actions workflow **Publish Release**.
A maintainer explicitly dispatches it from `main`.

The workflow currently:

- accepts `workflow_dispatch`;
- requires the dispatch to be on `main`;
- binds the publication to the starting commit SHA;
- derives the version and tag identity from `VERSION` and
  `scripts/release-version.sh`;
- runs the repository's full reusable CI verification;
- rechecks authoritative remote `main` and the intended-tag state before public
  mutation;
- fails if remote `main` moved away from the bound source commit;
- creates an annotated `v<VERSION>` tag only when that tag is absent;
- never force-moves an existing tag;
- permits the implemented safe-retry state only when the existing intended tag
  resolves to the exact bound source commit; and
- creates the GitHub Release for the exact bound source.

Do not use manual `git tag` and `git push` commands as the normal publication
path. The maintainer dispatches **Publish Release** after the preparation merge
and selects `main` as the workflow branch.

## Current public asset contract

The GitHub Release contains exactly these assets:

- `install.sh`
- `opentendril-linux-amd64.tar.gz`
- `opentendril-linux-arm64.tar.gz`
- `opentendril-darwin-amd64.tar.gz`
- `opentendril-darwin-arm64.tar.gz`
- `checksums.txt`

Each platform archive contains exactly two files:

- `tendril`
- `tendril-mcp`

They are independently built, distinct executables. `tendril` is the full
Stem/operator executable. `tendril-mcp` is the restricted MCP bridge. They are
not interchangeable.

`checksums.txt` covers `install.sh` and all four platform archives. The current
release contract is binary-first and does not include Docker or GHCR
publication.

## Post-publication verification

Run the following from a checkout of the bound source commit recorded by the
**Publish Release** run. Use a compatible host for the final executable check.

```bash
version="$(scripts/release-version.sh current)"
tag="v${version}"
sourceSHA="$(git rev-parse HEAD)"

test "${tag}" = "v${version}"
tagcommit="$(git ls-remote origin "refs/tags/${tag}^{}" | awk 'NR == 1 {print $1}')"
test -n "${tagcommit}"
test "${tagcommit}" = "${sourceSHA}"

releaseinfo="$(gh release view "${tag}" \
  --json tagName,isDraft,publishedAt \
  --jq '[.tagName, (.isDraft | tostring), .publishedAt] | @tsv')"
printf '%s\n' "${releaseinfo}"
test "$(printf '%s\n' "${releaseinfo}" | cut -f1)" = "${tag}"
test "$(printf '%s\n' "${releaseinfo}" | cut -f2)" = "false"
test -n "$(printf '%s\n' "${releaseinfo}" | cut -f3)"

expectedassets=$'checksums.txt\ninstall.sh\nopentendril-darwin-amd64.tar.gz\nopentendril-darwin-arm64.tar.gz\nopentendril-linux-amd64.tar.gz\nopentendril-linux-arm64.tar.gz'
actualassets="$(gh release view "${tag}" --json assets --jq '.assets[].name' | LC_ALL=C sort)"
test "${actualassets}" = "${expectedassets}"

verifydir="$(mktemp -d)"
gh release download "${tag}" --dir "${verifydir}"
(
  cd "${verifydir}"
  sha256sum -c checksums.txt
)

wantentries=$'tendril\ntendril-mcp'
for archive in "${verifydir}"/opentendril-*.tar.gz; do
  got="$(tar -tzf "${archive}" | sed '/\/$/d' | LC_ALL=C sort)"
  test "${got}" = "${wantentries}"
done

tar -xzf "${verifydir}/opentendril-linux-amd64.tar.gz" -C "${verifydir}"
"${verifydir}/tendril" --help
```

This verifies that:

- the release tag has the intended `v<VERSION>` identity;
- the tag resolves to the intended source commit;
- the public GitHub Release identifies that tag and is not a draft;
- exactly the expected assets are present;
- `checksums.txt` validates the downloaded assets;
- every platform archive contains exactly `tendril` and `tendril-mcp`; and
- the released `tendril --help` command executes on a compatible Linux amd64
  host. Use the matching platform archive for another compatible host.

## Failure and retry rules

- Public release tags are permanent.
- Never delete a legitimate release tag as routine recovery.
- Never force-move or recreate a release tag for another source commit.
- A failure before the intended tag exists can be corrected normally. Retry
  after the reviewed changes are merged.
- Once the intended tag exists, retry is legitimate only when the tag resolves
  to the exact approved source expected by the implemented publication-state
  contract, and remote `main` still resolves to that source.
- Contradictory or ambiguous publication state fails closed.

Do not invent a manual recovery path. The publication workflow is the authority
for tag creation and GitHub Release publication.

## Version convention

For pre-1.0 releases:

- **PATCH** is for compatible fixes, hardening, and corrections.
- **MINOR** is for a meaningful capability, distribution, workflow, or
  deliberate pre-1.0 contract change.
- `1.0.0` requires a separate explicit stability decision.
