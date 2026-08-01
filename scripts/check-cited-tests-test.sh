#!/usr/bin/env bash

set -euo pipefail

script="./scripts/check-cited-tests.sh"
chmod +x "${script}"

temp_dir=$(mktemp -d)
trap 'rm -rf "${temp_dir}"' EXIT

pass=0
fail=0

run_test() {
  local name="$1"
  local body="$2"
  local expect_fail="$3"
  local expected_output="${4:-}"

  local body_file="${temp_dir}/body.md"
  echo "$body" > "$body_file"

  set +e
  local out
  out=$("${script}" "origin/main" --body "${body_file}" 2>&1)
  local status=$?
  set -e

  if [ "$expect_fail" = "true" ]; then
    if [ $status -eq 0 ]; then
      echo "❌ FAIL: ${name} (expected failure, got success)"
      echo "Output:"
      echo "$out"
      fail=$((fail + 1))
      return
    fi
    if [ -n "$expected_output" ] && ! echo "$out" | grep -qF "$expected_output"; then
      echo "❌ FAIL: ${name} (failed as expected, but missing expected output)"
      echo "Expected output to contain: $expected_output"
      echo "Actual output:"
      echo "$out"
      fail=$((fail + 1))
      return
    fi
  else
    if [ $status -ne 0 ]; then
      echo "❌ FAIL: ${name} (expected success, got failure)"
      echo "Output:"
      echo "$out"
      fail=$((fail + 1))
      return
    fi
  fi
  
  echo "✅ PASS: ${name}"
  pass=$((pass + 1))
}

# 1. A body citing a test that exists at HEAD -> passes
# We need to find a test that actually exists. I will use TestCheckTaxonomy or something similar if it exists.
# Let's use `TestParseDesignRFC` which likely doesn't exist, wait. Let's find a real one.
# From conversation history: `TestRunSequenceTimeoutNoRetry` is a real test in `sequence_test.go` or `sequencefailurepolicy_test.go`.
# Another is `TestBotanistLaneRefusesPollinatorLaneBearers`.
# And a real file is `sequence_test.go`.

# For now, let's use TestBotanistLaneRefusesPollinatorLaneBearers and sequence_test.go
run_test "Existing test function" "Fixes TestBotanistLaneRefusesPollinatorLaneBearers in sequence_test.go" "false"

# 2. A body citing a test that exists nowhere -> fails, naming it
run_test "Non-existing test function" "Added TestNeverExistsEverInTheWorld" "true" 'Test function "TestNeverExistsEverInTheWorld" was not found'

# 3. A body citing a test file that exists -> passes
run_test "Existing test file" "Checked sequence_test.go" "false"

# 4. A body with no test citations -> passes
run_test "No test citations" "Fixed a typo in the README." "false"

# 5. A citation inside a fenced code block that is *quoted mutation output* -> must still be checked
run_test "Non-existing test in fenced block" "\`\`\`
FAIL: TestFakeFabricatedMutation
\`\`\`" "true" 'Test function "TestFakeFabricatedMutation" was not found'

run_test "Non-existing test file in fenced block" "\`\`\`
go test fake_missing_test.go
\`\`\`" "true" 'Test file "fake_missing_test.go" does not exist'

echo "=================="
echo "Tests Passed: $pass"
echo "Tests Failed: $fail"

if [ $fail -ne 0 ]; then
  exit 1
fi
