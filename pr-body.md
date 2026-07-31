Implements **slice 1** of issue 659: credential intake for the MCP stdio server.

This slice is deliberately inert. It introduces `TENDRIL_MCP_CREDENTIAL`, allowing the Stem to read a Pollinator durable root at startup. Nothing consumes this credential yet.

### Built features

- **Credential Intake:** Reads a file path from `TENDRIL_MCP_CREDENTIAL`.
- **Mode Validation:** The credential file is refused if it is group- or world-readable (0644). It must be 0600 or stricter.
- **Safe Diagnostics:** Missing or over-permissive files are rejected with errors that name the file and required mode but never emit the secret itself.
- **Byte-Identical Startup:** When no credential is provided, the startup sequence remains completely identical to today.

### Not built

- Slice 2, 3, and 4 items are out of scope (no network forwarding, no mode selection changes, no documentation updates). The `cmd/stem/mcpcredential.go` file exists as a clean seam for slice 2 to consume.

### Test Results

**Mutation result: 0644 mode check removed:**
When the mode check is commented out in `cmd/stem/mcpcredential.go`, the test fails as required:

```
=== RUN   TestMCPCredential_ModeCheck
    mcpcredential_test.go:150: expected 0644 file to fail but it succeeded
--- FAIL: TestMCPCredential_ModeCheck (0.06s)
FAIL
FAIL    github.com/opentendril/opentendril/cmd/stem     2.124s
FAIL
```

**Full suite output:**

```
=== RUN   TestMCPCredential_Unconfigured
--- PASS: TestMCPCredential_Unconfigured (0.11s)
=== RUN   TestMCPCredential_MissingFile
--- PASS: TestMCPCredential_MissingFile (0.05s)
=== RUN   TestMCPCredential_ModeCheck
--- PASS: TestMCPCredential_ModeCheck (0.09s)
=== RUN   TestMCPCredential_SecretLeakOnCorruptFile
--- PASS: TestMCPCredential_SecretLeakOnCorruptFile (0.00s)
PASS
ok      github.com/opentendril/opentendril/cmd/stem     1.680s
```

All acceptance criteria for Slice 1 are met.
