# ARTEX Defense — Implementation Roadmap

## Start here

This repository is the workspace for building **ARTEX Defense**: a patch-engineering agent that turns security findings into small, independently verified pull requests.

Baseline: ARTEX commit `f6692e6` (`Harden dependencies and runtime security`).

The next implementation agent should work incrementally, keep the existing application functional, and deliver the MVP described below before adding dynamic testing or production integrations.

### Mission

**Security finding in → evidence-backed patch and validation results out.**

The defense agent does not need to exploit a vulnerability to remediate it. Its job is to:

1. Find and normalize security findings.
2. Analyze reachability and patchability.
3. Produce the smallest safe patch and a regression test.
4. Validate the patch in a clean, isolated environment.
5. Deliver a human-reviewable pull request with evidence and rollback notes.

### Non-negotiable boundaries

- No autonomous exploitation, reverse shells, persistence, credential harvesting, or data exfiltration.
- No unauthorized network scanning or testing.
- No production writes, direct commits, auto-merge, or auto-deployment.
- No general-purpose Bash tool in the default defense profile.
- No arbitrary dependency install scripts by default.
- No secrets in prompts, logs, tests, fixtures, screenshots, commits, or pull requests.
- No patch may be marked `validated` without actual command output proving it.
- Uncertain findings become `needs_review`; they are never presented as confirmed vulnerabilities.
- A finding that cannot be checked safely is documented, not exploited.
- The patching agent must not approve its own patch. Independent build/test validation and a human reviewer are required.

## MVP definition

The first release works on **local source repositories only** and supports:

- Dependency and lockfile review
- Configuration review
- Static code analysis
- Small patches with regression tests
- Isolated build/test/scanner verification
- Human-reviewed patch/PR output

The MVP explicitly excludes:

- Production scanning
- Dynamic exploitation
- Auto-merge or deployment
- Cloud credential use
- Third-party issue-tracker or GitHub writes
- Automatic patch approval based only on an LLM's explanation

### MVP acceptance scenario

A fixture repository contains a small, exploitable-in-practice prototype-pollution bug and a test suite.

The agent must:

1. Identify the affected file and explain the vulnerable data flow.
2. Reject dangerous keys while preserving nested merge behavior.
3. Add a regression test that fails on the original code and passes on the patched code.
4. Run the original suite, new regression test, build, and configured security scanner in a clean container.
5. Produce a reviewable diff and validation report.
6. Make no network writes and no commits on the user's behalf.

A second fixture with an uncertain finding must produce `needs_review`, not an invented patch.

## Recommended architecture

Prefer **one defense orchestrator plus a deterministic validation service**. Do not add a large multi-agent system unless a real requirement justifies it.

### 1. Defense coordinator

Responsibilities:

- Parse a task's repository, scope, and constraints.
- Run read-only assessment stages in order.
- Track finding and patch state transitions.
- Refuse work outside the declared repository/scope.
- Stop on any validation or policy failure.

### 2. Read-only assessor

Responsibilities:

- Inspect files, manifests, configuration, tests, and CI definitions.
- Run deterministic scanners and normalize their output.
- Trace sources to sinks and identify affected versions/files.
- Assign evidence-based confidence and patchability.

### 3. Patch generator

Responsibilities:

- Propose the smallest behavior-preserving change.
- Add or adjust regression tests.
- Enforce file, directory, and patch-size limits.
- Produce a unified diff; never directly modify the source working tree.

### 4. Independent verifier

Responsibilities:

- Create a clean temporary checkout and apply the proposed diff.
- Run the new regression test, existing tests, build, type checks, and relevant scanners.
- Optionally run the new test against the unpatched baseline to confirm it fails there.
- Return structured evidence: commands, exit codes, durations, logs, artifacts, and environment digest.
- Reject the patch on failure, missing evidence, timeout, or policy violation.

### 5. Remediation reporter

Responsibilities:

- Generate finding summaries, patch rationale, risk assessment, validation results, residual risk, and rollback instructions.
- Produce a PR-ready bundle. GitHub integration is a later, separately authorized feature.

## Existing ARTEX components to reuse

- Task, finding, asset, and evidence models in `db/`
- Finding statuses and reporting in `db/findings.go` and `report/`
- Review/approval UI and activity history in `web/src/app/(main)/`
- Finding retest workflow in `server/finding_retests.go`
- Authentication and API conventions in `server/auth.go` and `server/server.go`
- Guard/intercept infrastructure in `guard/guard.go` and `intercept/`
- Asset allow/block matching in `db/asset_intercept_match.go`
- Existing static code-audit prompts as background only, not as the defense execution policy
- Non-root container initialization in `scripts/docker-entrypoint.sh`
- Existing build and test commands in `build.sh` and `web/package.json`

Do not reuse offensive execution prompts, exploitation tools, or unrestricted shell access as a shortcut.

## Proposed data model

Use explicit, versioned schemas. Do not pass free-form LLM text between stages as the only contract.

### DefenseTask

- `id`
- `repository_root`
- `default_branch`
- `scope`
- `constraints`
- `allowed_read_paths`
- `allowed_write_paths`
- `status`
- `created_at`

### DefenseFinding

- `id`
- `task_id`
- `source_scanner`
- `rule_id`
- `title`
- `severity`
- `confidence`
- `affected_files`
- `affected_package`
- `affected_version`
- `source`
- `sink`
- `evidence`
- `status`: `needs_review | confirmed | patch_pending | validated | rejected | deferred`
- `rejection_reason`

### PatchProposal

- `id`
- `finding_id`
- `summary`
- `rationale`
- `files_changed`
- `unified_diff`
- `regression_tests`
- `risk_level`
- `status`: `draft | verifying | accepted | rejected`
- `created_at`

### ValidationRun

- `id`
- `patch_id`
- `environment_digest`
- `commands`
- `exit_codes`
- `durations`
- `logs`
- `artifacts`
- `baseline_test_result`
- `patched_test_result`
- `build_result`
- `scanner_result`
- `status`: `passed | failed | blocked`

## Tool contracts

Implement a small, explicit tool surface before adding more scanners.

### Repository tools

- `repo.list_files(root, include, exclude)`
- `repo.search(root, query, paths)`
- `repo.read(path, start_line, end_line)`
- `repo.manifest(path)`
- `repo.test_layout(path)`

Requirements:

- Resolve and enforce the repository root.
- Reject symlinks or paths that escape the root.
- Cap file size, search results, and returned content.
- Redact likely secrets before any model call.

### Scanner tools

- `scan.dependencies(path, ecosystem)`
- `scan.configuration(path, rules)`
- `scan.static(path, rules)`
- `scan.secrets(path)`

Requirements:

- Return structured findings with stable fingerprints for deduplication.
- Record scanner name, version, command, and raw evidence location.
- Never treat scanner output alone as proof of reachability.

### Patch tools

- `patch.propose(finding, constraints)`
- `patch.diff(proposal)`
- `patch.validate_constraints(proposal)`

Requirements:

- Generate a diff, not an in-place edit.
- Enforce allowed paths and changed-file limits.
- Reject binary files, generated files, lockfile-only rewrites without a clear reason, and unrelated formatting changes.

### Verification tools

- `verify.prepare(clean_checkout, diff)`
- `verify.run(plan)`
- `verify.collect(run_id)`

Requirements:

- Run in an isolated, non-root container.
- Scrub environment variables by default.
- Disable network access unless explicitly required and approved.
- Apply command allowlists, timeouts, memory limits, CPU limits, and output caps.
- Preserve actual exit codes and logs.
- Destroy temporary resources on completion or failure.

### Reporting tools

- `report.remediation(finding, patch, validation)`
- `report.rollback(patch)`

Requirements:

- Separate evidence, assumptions, and unresolved questions.
- Include affected files, changed behavior, test evidence, residual risk, and rollback steps.

## Implementation phases

### Phase 0 — Baseline and policy harness

- [x] Add a `defense` execution profile distinct from offensive profiles.
- [x] Add configuration for repository root, allowed paths, resource limits, network policy, and validation policy.
- [x] Implement a policy harness that rejects general shell commands and out-of-scope paths.
- [x] Add tests for path traversal, symlink escape, secret redaction, command denial, and timeout handling.
- [x] Document the exact `DefenseTask` lifecycle and status transitions.

Acceptance: an empty task can be created and rejected safely when its repository or policy is invalid.

### Phase 1 — Read-only audit and finding normalization

- [x] Implement repository listing, reading, searching, and manifest inspection.
- [x] Implement dependency scanning for Go and npm first.
- [x] Add configuration and secret scanning with explicit rule definitions.
- [x] Normalize findings into `DefenseFinding`.
- [x] Add stable fingerprints and deduplication.
- [x] Add reachability/patchability assessment and `needs_review` handling.
- [x] Add a read-only API endpoint or CLI that returns findings without modifying the repository.

Acceptance: running against the fixture produces reproducible findings with evidence, not just prose.

### Phase 2 — Patch generation

- [x] Implement `PatchProposal` creation and unified-diff output.
- [x] Add path and patch-size constraints.
- [x] Add regression-test generation for supported project types.
- [x] Preserve APIs and avoid unrelated formatting/lockfile churn.
- [x] Add rejection paths for unsupported or uncertain findings.

Acceptance: the prototype-pollution fixture receives a minimal patch and regression test without any in-place source mutation.

### Phase 3 — Independent validation

- [x] Implement clean-checkout creation and patch application.
- [x] Implement non-root, resource-limited validation containers.
- [x] Add command plans for Go and Node projects.
- [x] Run regression tests, existing tests, builds, and configured scanners.
- [x] Run the new test against the unpatched baseline when practical.
- [x] Return machine-readable `ValidationRun` results and artifacts.
- [x] Destroy temporary resources on every exit path.

Acceptance: a deliberately broken patch is rejected with logs, while the correct patch passes with complete evidence.

### Phase 4 — Reporting and review UX

- [x] Generate remediation reports with rationale, evidence, risk, validation, and rollback.
- [x] Add a defense finding detail view and patch diff view.
- [x] Add validation status and command output.
- [x] Add explicit `needs_review`, `validated`, and `rejected` states.
- [x] Provide a PR-ready export; do not push it automatically.

Acceptance: a reviewer can understand and test the patch without reading model reasoning or hidden state.

### Phase 5 — Later, separately authorized integrations

- [ ] GitHub/GitLab PR creation behind a separate permission gate.
- [ ] Isolated staging-environment validation for findings that need dynamic checks.
- [ ] Additional ecosystems and scanners.
- [ ] Periodic read-only dependency rescanning and regression monitoring.
- [ ] Policy-controlled remediation of infrastructure/configuration issues.

No production exploitation or production writes are implied by this phase.

## Required tests

### Unit tests

- Path traversal and symlink escape
- Secret redaction
- Finding normalization and deduplication
- Confidence/status transitions
- Patch-size and path constraints
- Unified-diff generation
- Command allowlists and timeout behavior

### Integration tests

- Go dependency fixture
- npm dependency fixture
- Prototype-pollution source fixture
- Uncertain-finding fixture
- Broken-patch rejection fixture
- Missing-test-coverage fixture
- Network-disabled validation fixture

### End-to-end tests

- Audit → finding → patch → validation → report
- No network writes and no source working-tree mutations
- Correct patch accepted
- Broken patch rejected
- Uncertain finding routed to `needs_review`
- Validation artifacts destroyed afterward

### Security checks for every significant change

```bash
# Go
GOTOOLCHAIN=go1.26.11 go test -p=1 -count=1 ./...
GOTOOLCHAIN=go1.26.11 go run golang.org/x/vuln/cmd/govulncheck@latest ./...

# Web
cd web && npm audit && npm run build && npm run build:static

# API-recon skill
cd skills/api-recon/scripts && npm audit && node --check runtime_harvest.js

# Docker
cd .. && docker build -f Dockerfile.source -t artex-defense:review .
```

Integration tests requiring PostgreSQL must use a temporary, isolated database, never a development or production DSN.

### MVP verification note (2026-11-15)

- The prototype-pollution fixture passed in a real non-root, network-disabled container with baseline regression failure, patched regression/existing-test success, build success, and offline scanner success.
- Broken-patch, missing-coverage, uncertain-finding, cleanup, source-immutability, API-authentication, and human-approval checks passed.
- Frontend lint/TypeScript/build/static-build, npm audits, API-recon checks, `govulncheck`, and the hardened source image build passed. The image contains a working `artex-defense` CLI.
- The full Go suite passes except for the pre-existing timing-sensitive `TestPreAgentConversationManualRerunRecoverableStates`; it fails in the shared legacy server suite and passes in isolation, matching the baseline caveat below. Its isolated pass does not make the aggregate suite green, so the aggregate verification checkbox remains unchecked.

## Guardrails for the implementation agent

- Work in small, reviewable commits.
- Do not edit generated files or lockfiles manually unless the change requires it and is explained.
- Do not add dependencies when an existing standard library or installed tool can do the job.
- Do not introduce a new agent framework unless the current one cannot support the requirement.
- Do not silently reduce test coverage or skip failing tests.
- Do not hide errors behind LLM-generated success messages.
- Do not weaken the existing TLS, non-root, or capability-hardening work.
- Do not create documentation instead of implementing the requested behavior.
- Keep `TODO.md` current with completed items and commit references where useful.
- If a requirement changes, record the decision and reason before changing the implementation.

## Initial implementation order

1. Policy harness and `DefenseTask` schema.
2. Safe repository reader and secret redaction.
3. Finding schema, normalization, and deduplication.
4. Go/npm dependency scanner adapters.
5. Prototype-pollution static-analysis rule and fixture.
6. Patch proposal and unified-diff generation.
7. Regression-test generation for the fixture.
8. Clean-checkout validation runner.
9. Remediation report and review UI.
10. End-to-end acceptance test covering success, rejection, and uncertainty.

## Definition of done for MVP

- [x] All non-negotiable boundaries hold.
- [x] The fixture succeeds end-to-end with a minimal patch and regression test.
- [x] Broken and uncertain patches are rejected safely.
- [x] Validation uses actual build/test/scanner output, not model assertions.
- [x] No source working-tree changes occur during audit or validation.
- [x] No network writes, auto-commits, auto-merges, or deployments occur.
- [x] Documentation and UI clearly distinguish evidence from assumptions.
- [ ] Go tests, frontend builds, npm audits, Go vulnerability checks, and the hardened Docker build pass.
- [x] A human reviewer can take the generated diff and apply it confidently.

## Existing baseline caveats

- Some pre-agent integration tests are timing-sensitive; rerun them in isolation when diagnosing failures.
- `scripts/test-deploy-scripts.sh` has a GNU `stat -c` portability issue on macOS.
- Biome checks may expose legacy issues in existing files; avoid unrelated mass reformatting.
- TypeScript 5.9 and Node 22 types are intentionally retained for framework compatibility.
- The offensive RoE/default-allow architecture is not a suitable authorization model for the defense profile. Defense must be default-deny for writes, network, and execution.

## Final guidance

The most important component is not the patching model. It is the **independent verification system**.

A good defense agent produces a small patch and proves it. A bad defense agent produces a large explanation and hopes it is right. Build the former.
