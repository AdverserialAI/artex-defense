export type DefenseFindingStatus =
  | "needs_review"
  | "confirmed"
  | "patch_pending"
  | "validated"
  | "rejected"
  | "deferred";
export type DefensePatchStatus = "draft" | "verifying" | "accepted" | "rejected";
export type DefenseValidationStatus = "passed" | "failed" | "blocked";

export interface DefenseTask {
  schema_version: string;
  id: string;
  profile: "defense";
  repository_root: string;
  default_branch?: string;
  scope: string[] | null;
  constraints: string[] | null;
  allowed_read_paths: string[];
  allowed_write_paths: string[];
  limits: {
    max_file_bytes: number;
    max_content_bytes: number;
    max_search_results: number;
    max_files_changed: number;
    max_changed_lines: number;
    timeout_seconds: number;
    memory_mb: number;
    cpu_millicpus: number;
    pids: number;
    output_bytes: number;
  };
  validation: {
    image: string;
    network_allowed: boolean;
    require_baseline_failure: boolean;
    command_allowlist: string[];
    scanner_command?: string[];
  };
  status: string;
  created_at: string;
}

export interface DefenseEvidence {
  kind: string;
  path?: string;
  start_line?: number;
  end_line?: number;
  snippet?: string;
  description: string;
  data?: Record<string, string>;
  raw_location?: string;
}

export interface DefenseFinding {
  schema_version: string;
  id: string;
  task_id: string;
  fingerprint: string;
  source_scanner: string;
  scanner_version: string;
  rule_id: string;
  title: string;
  severity: string;
  confidence: string;
  affected_files: string[];
  affected_package?: string;
  affected_version?: string;
  source: string;
  sink: string;
  reachability: string;
  patchable: boolean;
  evidence: DefenseEvidence[];
  status: DefenseFindingStatus;
  rejection_reason?: string;
}

export interface DefenseScan {
  scanner: string;
  version: string;
  command: string[];
  raw_evidence?: string;
  raw_location?: string;
  finding_count: number;
  warnings?: string[];
}

export interface DefenseAssessment {
  schema_version: string;
  task_id: string;
  findings: DefenseFinding[] | null;
  scans: DefenseScan[] | null;
}

export interface DefensePatch {
  schema_version: string;
  id: string;
  finding_id: string;
  summary: string;
  rationale: string;
  files_changed: string[];
  unified_diff: string;
  regression_tests: string[];
  risk_level: string;
  status: DefensePatchStatus;
  generated_by: string;
  reviewed_by?: string;
  rejection_reason?: string;
  created_at: string;
}

export interface DefenseCommandResult {
  command: string[];
  kind: string;
  environment: string;
  exit_code: number;
  duration: number;
  output: string;
  output_truncated: boolean;
  started_at: string;
  timed_out: boolean;
}

export interface DefenseCheck {
  name: string;
  status: string;
  exit_code: number;
  evidence_command_indexes: number[];
}

export interface DefenseValidation {
  schema_version: string;
  id: string;
  patch_id: string;
  environment_digest: string;
  commands: DefenseCommandResult[];
  exit_codes: number[];
  durations: number[];
  logs: string[];
  artifacts: string[];
  baseline_test_result: DefenseCheck;
  patched_test_result: DefenseCheck;
  build_result: DefenseCheck;
  scanner_result: DefenseCheck;
  status: DefenseValidationStatus;
  failure_reason?: string;
  created_at: string;
}

export interface DefenseBundle {
  task: DefenseTask;
  assessment: DefenseAssessment;
  patch?: DefensePatch;
  validation?: DefenseValidation;
  report: string;
}

export interface DefenseTaskListItem {
  id: string;
  repository_root: string;
  status: string;
  created_at: string;
  updated_at: string;
  finding_count: number;
  patch_status?: DefensePatchStatus;
  validation_status?: DefenseValidationStatus;
}
