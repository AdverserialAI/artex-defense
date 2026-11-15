# ARTEX Defense architecture and operations

## Trust boundary

A defense task authorizes inspection of one local repository and explicitly listed relative paths. It does not authorize a network target, production system, cloud account, dependency-install script, arbitrary shell command, Git write, merge, deployment, or third-party issue/PR write.

The defense implementation lives in `defense/` and has a separate entry point in `cmd/artex-defense/`. The authenticated console API is in `server/defense.go`, and the review interface is in `web/src/app/(main)/function/defense/page.tsx`.

The legacy agent, MCP, skill, and shell-capable execution paths are not assembled into the defense coordinator.

## Components

1. **Coordinator** validates policy and runs assessment, patch, verification, and reporting in order.
2. **Repository reader** resolves a canonical root, rejects absolute paths, traversal, and symlinks, enforces read scopes and caps, and redacts likely secrets.
3. **Scanners** produce versioned findings with structured evidence, stable SHA-256 fingerprints, confidence, reachability, and patchability.
4. **Patch generator** supports the deterministic CommonJS recursive-merge prototype-pollution case. It produces a unified diff and regression test without editing source files.
5. **Validator** copies the repository into disposable baseline and patched checkouts. Only regression tests are added to the baseline, so its failure demonstrates the vulnerable behavior rather than a missing test file.
6. **Reporter** writes the task, findings, patch, validation, hashes, Markdown remediation report, and complete bundle outside the source repository.
7. **Human review** records an independent reviewer only after complete passing evidence. It never creates a commit or pull request.

## Task lifecycle

```text
draft
  → auditing
    → needs_review       no safely patchable confirmed finding, or uncertain evidence
    → patch_pending      deterministic proposal created
      → validating
        → completed      validation passed; human review may still be pending
        → rejected       test, build, scanner, or baseline expectation failed
        → failed         policy, checkout, image, or execution setup error
```

An empty or invalid task is rejected before `auditing`. Task state is returned in the bundle; it does not mutate the source repository or legacy task database.

## Finding lifecycle

```text
needs_review → confirmed → patch_pending → validated
     │             │             │
     │             │             ├→ rejected
     │             │             ├→ deferred
     │             │             └→ needs_review
     │             ├→ rejected
     │             └→ deferred
     ├→ rejected
     └→ deferred

validated / rejected / deferred → needs_review
```

`validated` has a guarded transition: the finding must be `patch_pending`, and the referenced validation run must have complete passing patched-test, build, and scanner evidence. Scanner output alone does not prove reachability. Low-confidence or unknown-reachability findings normalize to `needs_review`.

## Patch lifecycle

```text
draft → verifying → draft       validation finished without human approval
draft → accepted               independent human + complete passing validation
draft → rejected               validation or policy failure
```

The generator is identified as `artex-defense`; that identity cannot accept a patch. Console approval requires an explicit confirmation request. CLI approval requires `-reviewer` with a different identity.

Patch constraints include:

- Allowed relative write paths
- Changed-file and changed-line limits
- Mandatory regression coverage
- No binary, generated, or non-UTF-8 content
- No lockfile-only rewrite through the deterministic source-patch path
- Clean old-content context when the diff is applied

## Validation contract

Every required command runs directly, not through a shell. Default supported executables are `go`, `node`, `npm`, and `govulncheck`; the task can only narrow that allowlist.

Docker validation uses:

- The already-local task image, identified by its image ID
- A random disposable container name and forced cleanup after normal exit, error, cancellation, or timeout
- UID/GID `65532:65532`
- `--network none` unless network access was explicitly approved
- A scrubbed environment with only fixed runtime variables
- `--cap-drop ALL`, `no-new-privileges`, and a read-only root filesystem
- CPU, memory, PID, timeout, and captured-output limits
- Fresh writable checkout and temporary filesystem mounts

Checkouts are created under the user cache by default because Docker Desktop and Colima may not share the operating system temp directory. `ARTEX_DEFENSE_TEMP` can select another Docker-shared disposable parent. The per-run directory is removed on every exit path. No dependency installation or image pull occurs implicitly.

For npm projects, the plan runs each new regression against baseline and patched checkouts, the complete Node test suite, the configured build (or syntax check), and offline `npm audit` by default. Go plans run tests and builds; a Go scanner command must be explicitly configured and present in the image.

`ValidationRun` preserves commands, environment, start times, durations, actual exit codes, redacted output, output-cap markers, check statuses, failure reasons, and artifact names. An expected baseline failure is not rewritten as a passing command: its exit code and `failed` baseline check remain visible.

## API and exports

All defense API routes require the normal ARTEX JWT:

- `GET /api/defense/tasks` — list active in-memory review sessions
- `POST /api/defense/tasks` — body contains `task` and `mode: audit|run`
- `GET /api/defense/tasks/{id}` — retrieve the structured bundle
- `POST /api/defense/tasks/{id}/approve` — body must contain `confirm: true`
- `GET /api/defense/tasks/{id}/export?file=...` — download an allowlisted artifact

Export names are restricted to `bundle.json`, `findings.json`, `patch.diff`, `validation.json`, and `remediation.md`. Files are stored under `data/defense/<task-id>` and are not browsable through arbitrary paths. Review sessions are process-local; exported files remain on disk for operator follow-up.

## Reports and rollback

The remediation report separates:

- Recorded finding evidence
- Assumptions and unresolved questions
- Changed behavior and patch rationale
- Actual validation commands and output
- Residual risk
- Human approval state
- Rollback instructions

The defense runner never applies the diff to the original repository. After a reviewer applies `patch.diff` on a separate branch through the repository's normal process, `git apply -R patch.diff` is the supplied reverse-patch rollback option. If later changes conflict, use the normal reviewed Git revert process.

## Current limits

- Automatic patch generation is intentionally limited to the supported CommonJS prototype-pollution merge pattern.
- Go and npm advisory normalization is implemented, but dependency updates are not automatically rewritten.
- Secret and configuration matches are review signals, not automatic proof of exploitability.
- Network-dependent advisory scans require explicit approval; offline npm audit cannot refresh advisories.
- GitHub/GitLab creation, staging validation, production integration, auto-merge, deployment, and additional ecosystems remain later, separately authorized work.
