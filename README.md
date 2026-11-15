# ARTEX Defense

ARTEX Defense turns local security findings into small, reviewable patches backed by independent validation evidence.

**Security finding in → evidence-backed patch and validation results out.**

The defense profile is default-deny for writes, network access, and execution. It does not autonomously exploit vulnerabilities, use a general-purpose shell, edit the audited source tree, commit, push, merge, deploy, or approve its own patches.

The legacy ARTEX runtime remains for compatibility, but it is not the defense execution policy. Use the separate `artex-defense` CLI or the authenticated **Defense Review** console at `/function/defense`.

## MVP capabilities

- Root-confined repository listing, reading, searching, manifest inspection, and test-layout discovery
- Path traversal and symlink-escape prevention, content caps, and secret redaction
- Go and npm dependency-scan adapters (`govulncheck` and `npm audit`) behind explicit network approval
- Deterministic configuration, secret, and JavaScript static scans with structured, deduplicated findings
- Evidence-based `needs_review`, `confirmed`, `patch_pending`, `validated`, `rejected`, and `deferred` states
- Minimal CommonJS prototype-pollution patch generation with a regression test and unified diff
- Independent baseline and patched runs in fresh, disposable checkouts
- Non-root containers with networking disabled by default, scrubbed environments, dropped capabilities, command allowlists, timeouts, CPU/memory/PID limits, and output caps
- Actual commands, exit codes, durations, logs, environment digest, artifacts, and rollback notes
- Authenticated web review UI and PR-ready exports; human approval is separate from validation

Deterministic patch generation currently supports the included CommonJS recursive-merge prototype-pollution pattern. Other findings remain `needs_review` rather than receiving invented fixes.

## Quick start

Requires Go 1.26.11+, Node.js 22+ for the web UI, and Docker for independent validation. The validation image must already exist locally; the defense runner never pulls it implicitly.

```bash
# Pulling an image is a separate operator action, not a defense-run side effect.
docker pull node:22-bookworm

# Build the defense CLI.
go build -o artex-defense ./cmd/artex-defense

# Validate a versioned task policy.
./artex-defense check-task -task defense-task.json

# Read-only audit.
./artex-defense audit -task defense-task.json -out findings.json

# Audit → patch proposal → independent validation → review bundle.
./artex-defense run -task defense-task.json -out review-bundle

# After independently reviewing the diff and command evidence:
./artex-defense review -bundle review-bundle/bundle.json -reviewer "your-name"
```

The output directory must be outside the audited repository. A run does not create a Git branch or apply the patch to the source working tree.

## Defense task

```json
{
  "schema_version": "defense.artex/v1",
  "id": "local-merge-review",
  "profile": "defense",
  "repository_root": "/absolute/path/to/repository",
  "default_branch": "main",
  "scope": ["local-repository"],
  "constraints": ["no source edits", "no network writes"],
  "allowed_read_paths": ["."],
  "allowed_write_paths": ["merge.js", "merge.regression.test.js"],
  "limits": {
    "max_file_bytes": 1048576,
    "max_content_bytes": 131072,
    "max_search_results": 200,
    "max_files_changed": 5,
    "max_changed_lines": 250,
    "timeout_seconds": 120,
    "memory_mb": 1024,
    "cpu_millicpus": 1000,
    "pids": 128,
    "output_bytes": 65536
  },
  "validation": {
    "image": "node:22-bookworm",
    "network_allowed": false,
    "require_baseline_failure": true,
    "command_allowlist": ["node", "npm"]
  },
  "status": "draft",
  "created_at": "2026-11-15T00:00:00Z"
}
```

An empty task, an unreadable repository, unsafe paths, unsupported executables, or excessive resource limits are rejected before assessment. `allowed_write_paths` constrains proposed diffs only; it never permits an in-place source edit.

A `defense` task can also be placed in the existing JSON config file. CLI `-task` input takes precedence over that config, and `-repo` overrides the task root.

## Web review workflow

1. Start the normal ARTEX backend and web console.
2. Open **Functions → Defense Review**.
3. Enter an absolute local repository path and explicit read/patch scopes.
4. Leave network access off unless dependency scanning requires it and you approve it.
5. Choose **Audit only** or **Patch & validate**.
6. Review finding evidence, scanner records, the unified diff, baseline failure, patched tests, build, scanner output, residual risk, and rollback instructions.
7. Download `patch.diff`, `remediation.md`, or the complete JSON bundle.
8. Optionally record human approval. Approval never pushes or merges the patch.

The API uses the same JWT authentication as the rest of the console:

- `GET|POST /api/defense/tasks`
- `GET /api/defense/tasks/{id}`
- `POST /api/defense/tasks/{id}/approve`
- `GET /api/defense/tasks/{id}/export?file=...`

Review sessions are in memory; exported bundles are written under `data/defense/<task-id>` with mode `0600` files.

## Evidence and lifecycle

A finding becomes `validated` only after actual output proves that:

- The new regression test fails against the unpatched baseline when baseline proof is required.
- The new regression test and existing tests pass against the patched checkout.
- The configured build and security scanner pass.
- Required commands have environment, timestamp, duration, exit-code, and output records.

Validation failure, missing evidence, timeout, or policy violation rejects the patch. Successful validation leaves the patch as `draft` until an independent human accepts it.

See [docs/ARTEX-DEFENSE.md](docs/ARTEX-DEFENSE.md) for state transitions, architecture, API details, and operational limits.

## Verification

```bash
GOTOOLCHAIN=go1.26.11 go test -p=1 -count=1 ./...
GOTOOLCHAIN=go1.26.11 go run golang.org/x/vuln/cmd/govulncheck@latest ./...
cd web && npm audit && npm run build && npm run build:static
cd ../skills/api-recon/scripts && npm audit && node --check runtime_harvest.js
cd ../../.. && docker build -f Dockerfile.source -t artex-defense:review .
```

Integration tests requiring PostgreSQL must use a temporary isolated database, never a development or production DSN.

## License

[AGPL v3](LICENSE), following the upstream ARTEX license. Third-party component obligations remain unchanged.
