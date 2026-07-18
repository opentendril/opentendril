# Substrate State Externalization: Resilient Commits & Status Tracking (Issue)

This plan details the implementation of **Substrate State Externalization** to ensure sprout task progress (success/failure) is committed to git, preventing work loss and enabling task resumption.

---

## 1. Pre-Flight & Post-Flight SDLC Git Safety

To ensure that starting state is clear and no temporary/compiled files are accidentally committed (reducing dependency on `.gitignore`), the Go Stem will manage a strict pre/post-flight Git lifecycle:

### A. Pre-Flight: Host Git Check & Stash
1.  Go Stem checks the host repository status using `git status --porcelain`.
2.  If the host workspace is dirty:
    *   Go Stem automatically stashes the host changes: `git stash save -u "opentendril-host-pre-flight-stash-<id>"`.
    *   This ensures the host repository is clean, preventing conflicts during checkout/merge.
3.  Go Stem sprouts the detached shadow git worktree `/tmp/opentendril-terrarium-...` based on the clean `HEAD`.

### B. Post-Flight: File Sanitization & Merging
1.  After sprout execution completes, Go Stem runs `git status --porcelain` inside the terrarium.
2.  **Sanitization Filter:** Before staging, Go Stem filters the list of modified/untracked files. Any path matching compiler/runtime artifacts (e.g., `*.log`, `__pycache__/`, `dist/`, `build/`, `.cache/`, `tmp/`) will **not** be staged. This safeguards the repository from "trash commits" even if `.gitignore` is missing.
3.  Go Stem stages only the sanitized files: `git add <sanitized_files>`.
4.  Go Stem commits the changes: `git commit -m "tendril(step-<id>): <prompt_summary>"`.
5.  Go Stem merges the commit back to the host workspace: `git -C hostPath merge --ff-only <commit_hash>`.
6.  **Restore Host State:** Go Stem pops the host stash: `git stash pop`. Any conflicts between the Sprout's changes and the developer's original uncommitted work will be flagged natively by Git for the developer to resolve.

---

## 2. Success vs. Failure Behaviours

### A. On Successful Execution
*   Sanitize files, commit inside the terrarium, merge back to host, pop host stash.
*   If `TENDRIL_GENOME_AUTO_PUSH=true`, run `git -C hostPath push origin HEAD`.

### B. On Failed Execution (Crashes, Exit Errors, Timeouts)
*   Write `tendril-status.json` into the terrarium.
*   Sanitize files, commit partial progress as `tendril(step-<id>) [INCOMPLETE]: <error_message>`, merge back to host, and pop host stash.

---

## 3. Status File & History Logging

To distinguish between structured sequence tasks and ad-hoc chat sessions:

### A. Structured Sequence Steps (e.g. Issue)
*   Writes a `tendril-status.json` file directly to the Substrate root directory:
    ```json
    {
      "stepId": "step-abc1234",
      "status": "failed",
      "error": "pytest: 3 tests failed",
      "timestamp": "2026-06-29T02:00:00Z",
      "filesModified": [
        "src/auth/token.go",
        "tests/token_test.go"
      ]
    }
    ```

### B. Ad-Hoc Chat Tasks (`tendril chat`)
*   To avoid cluttering the repository root with status files for ad-hoc chat runs, Go Stem will write execution logs and metadata to a centralized history folder: `.tendril/history/chat-<timestamp>.json`.
*   Step IDs for ad-hoc runs will be formatted as `chat-<timestamp>`.

---

## 4. Host Orchestrator Resumption

Before executing a sprout task:
1.  Go Stem checks for the presence of `tendril-status.json` in the host workspace.
2.  If the file exists:
    *   If status is `"complete"` $\rightarrow$ skip the step and output: `Step <id> already completed. Skipping.`
    *   If status is `"failed"` $\rightarrow$ report the failure, ask the user/IDE if they want to retry, or halt execution depending on the configuration.

---

## 5. Proposed Changes

### Component: Go Stem Orchestrator

#### [MODIFY] [orchestrator/docker.go](file:///home/dr3w/GitHub/opentendril/opentendril/cmd/stem/internal/orchestrator/docker.go)
*   Implement `runGitCommand(dir string, args ...string)` to handle host/terrarium git calls.
*   Implement pre-flight host stashing (`git stash -u`) and post-flight stash popping (`git stash pop`).
*   Implement post-flight status checking (`git status --porcelain`) and file sanitization to exclude log/build/temp files before committing.
*   Write `tendril-status.json` on failure, and check it at startup for resumption.

---

## 6. Verification Plan

### Automated Tests
*   **Stash-Pop Integration Tests:** Run test cases where a dirty host is stashed, a terrarium commit is merged, and the stash is popped.
*   **Sanitization Filter Tests:** Verify that temporary file extensions (`.log`, `.tmp`) created during execution are ignored during commits.

### Manual Verification
1.  Modify a file locally in your IDE (making the tree dirty).
2.  Run `tendril chat` to read a file.
3.  Verify that your local modifications were safely stashed during the run, and restored afterward.
