/**
 * types.ts: ARTEX domain model — types shared across the UI.
 * Derived from section 7 of the functional spec (key data shapes).
 * Main functions/types (selected by domain):
 *   - Task / TaskStatus / EngineMode: task domain model and status enums
 *   - Asset / AssetNode / AssetRel / NewAssetType: asset graph nodes, relations, and creation types
 *   - TaskArchive / TaskArchivePage / ArchiveBatchItem: archive structures and paging
 *   - CoverageGraph*: coverage graph data shapes
 */

export type TaskStatus = "created" | "queued" | "running" | "paused" | "done" | "failed" | "timeout";
export type EngineMode = "exploring" | "paused" | "stalled" | "idle";

export interface Task {
  id: string;
  name?: string; // optional task name; empty/absent = unnamed, display falls back to description
  category_id?: number;
  category_name?: string;
  category_nkey?: string;
  pinned?: boolean;
  pinned_at?: string | null;
  description: string;
  goal: string;
  status: TaskStatus;
  created_at: string;
  created_unix?: number; // created_at as unix seconds (run-duration calc)
  completed_at?: string; // RFC3339 finish time (done/failed); "" if unfinished
  completed_unix?: number; // completed_at as unix seconds (0/undef if unfinished)
  last_activity_unix?: number; // unix seconds of the last activity (0/undef if none)
  paused?: boolean;
  queued?: boolean;
  active?: boolean;
  in_flight?: number;
  findings?: { critical: number; high: number; medium: number; low: number }; // registered finding counts by severity
  last_activity?: string;
  stalled?: boolean;
  goals_total?: number;
  goals_met?: number;
  engine_mode?: EngineMode;
  tokens?: TokenTotal; // whole-task token consumption
  llm_profile_id?: number; // LLM profile used; absent = default profile
  llm_profile_ids?: number[]; // ordered task-level failover chain
  active_llm_profile_id?: number; // profile used by the next LLM call
  llm_failover_state?: "default" | "ready" | "chain_exhausted" | string;
  llm_failover_reason?: string;
  source_task_ids?: string[]; // directly related tasks inherited as read-only context
  archive_blocked_by_task_id?: string; // live direct dependent that must be archived first
  company_ids?: number[]; // associated company scopes; current company assets join the task at creation
  coverage_enabled?: boolean; // asset-coverage feature switch (set at creation, default on); false = do not compute or display coverage
  // [fork P-INTENT U5] begin — new task-level fields (vol 00 U5 / D2 / D7)
  planner_prompt_override?: string; // task-level snapshot of the planner prompt (D2 column; empty = follow global/default chain)
  worker_prompt_override?: string; // task-level snapshot of the Worker prompt; later template edits have no effect
  worker_count?: number; // task-level worker pool size: 0 = follow global, 1–16 dedicated
  review_auto?: boolean; // auto-disposition switch (Switch in the review chat; true = pre-screen rounds auto-decide)
  review_enabled?: boolean; // [fork P-REVIEW D2c] task-level review master switch (m03934: default off; on = review tab + runner + endpoints enabled, LLM follows the task)
  asset_mode?: string; // [fork P-REVIEW D12] asset graph mode label (m04039: pentest/vuln_research/code_audit)
  // [fork P-INTENT U5] end
}

export interface TaskCategory {
  id: number;
  name: string;
  // [fork P-REVIEW D12] category preset bundle (m04039)
  default_review_enabled?: boolean;
  default_planner_template_id?: number;
  default_worker_template_id?: number;
  asset_mode?: string;
  default_description?: string;
  // [fork P-SCENARIO MERGE] seed-intent preset (m03167 scenarios merged into categories)
  seed_first_intent?: boolean;
  seed_executor?: string; // '' = default worker; otherwise = custom agent's executor_key
  task_count: number;
  created_at: string;
  updated_at: string;
}

export interface TaskTemplate {
  id: number;
  name: string;
  description: string;
  goal: string;
  category_id?: number | null; // preset category; null/absent = none
  intercept_rules?: AssetInterceptRuleInput[]; // preset task-level block/allow rules
  created_at: string;
  updated_at: string;
}

// [fork P-TPL U5] begin — planner template (maps to db.PlannerTemplate; snapshot semantics in vol 03 D11-b)
export interface PlannerTemplate {
  id: number;
  name: string;
  body: string; // planner prompt body (Go template)
  is_default: boolean;
  builtin: boolean;
  created_at: string;
  updated_at: string;
}
// [fork P-TPL U5] end
export type WorkerTemplate = PlannerTemplate;

export interface DeleteTaskOptions {
  delete_assets: boolean;
  delete_traffic: boolean;
  delete_files: boolean;
  delete_findings: boolean;
  delete_llm_records: boolean;
}

export interface DeleteTaskResult {
  deleted: string;
  assets_deleted: number;
  assets_detached: number;
  traffic_deleted: number;
  files_deleted: boolean;
  findings_deleted: number;
  llm_records_deleted: number;
  cleanup_warning?: string;
}

export type TaskArchiveState =
  | "archive_queued"
  | "archiving"
  | "archive_failed"
  | "ready"
  | "restore_queued"
  | "restoring"
  | "restore_failed"
  | "delete_queued"
  | "deleting"
  | "delete_failed";

export interface TaskArchiveTokenStats {
  calls?: number;
  input_tokens?: number;
  output_tokens?: number;
  cache_read_tokens?: number;
  cache_write_tokens?: number;
}

export interface TaskArchive {
  id: number;
  task_id: number;
  state: TaskArchiveState;
  phase: string;
  progress: number;
  error?: string;
  warnings?: string[];
  format_version: number;
  sha256?: string;
  original_size: number;
  compressed_size: number;
  task_name: string;
  task_description: string;
  task_goal: string;
  original_status: TaskStatus;
  category_id?: number;
  category_name?: string;
  source_task_ids: number[];
  remaining_timeout_seconds: number;
  data_counts: Record<string, number>;
  aggregate_stats: {
    tokens?: TaskArchiveTokenStats;
    skills?: Record<string, number>;
    tools?: Record<string, number>;
    findings?: Record<string, number>;
  };
  archived_at?: string;
  requested_at: string;
  created_at: string;
  updated_at: string;
}

export interface TaskArchivePage {
  items: TaskArchive[];
  total: number;
  page: number;
  size: number;
}

export interface ArchiveBatchItem {
  id: string;
  archive_id?: number;
  ok: boolean;
  queued: boolean;
  error?: string;
}

// ---- Asset graph (global, shared across tasks) ----
export type AssetType =
  | "company"
  | "domain"
  | "ip"
  | "port"
  | "service"
  | "site"
  | "endpoint"
  | "parameter"
  | "tech"
  | "credential"
  | "data";

export type NodeState = "observed" | "confirmed" | "tombstoned";

export interface AssetNode {
  id: string;
  type: AssetType;
  name: string;
  key: string; // nkey
  value?: string;
  company_id?: string; // owning company asset id; empty = unassigned
  state: NodeState;
  confidence: number; // 0..1
  attrs?: Record<string, unknown>;
  first_seen: string;
  last_seen: string;
}

export type AssetRel =
  | "owns"
  | "resolves"
  | "exposes"
  | "runs"
  | "serves"
  | "has_endpoint"
  | "has_param"
  | "fingerprinted"
  | "authenticates_as"
  | "reachable"
  | "has_subdomain";

export interface Edge {
  src: string;
  dst: string;
  rel: AssetRel | ExploreRel;
}

// ---- New unified asset model (new backend) ----
export type NewAssetType = "root_domain" | "ip" | "subdomain" | "app" | "service" | "endpoint";

export interface AssetHostService {
  asset_id: number;
  type: "app" | "service" | "endpoint";
  label: string;
  url?: string;
  method?: string;
}

export interface AssetHostRow {
  key: string;
  company_id?: number;
  company_name?: string;
  root_domain?: string;
  subdomains: string[];
  ip: string;
  services: AssetHostService[];
  asset_ids: number[];
  finding_count: number;
  critical: number;
  high: number;
  medium: number;
  low: number;
}

export interface Asset {
  id: number;
  type: NewAssetType;
  company_id?: number;
  task_ids: number[];
  domain?: string;
  root_domain?: string;
  ip?: string;
  c_segment?: string;
  port?: number;
  icp?: string;
  bound_domains?: string[];
  open_ports?: { port: number; service?: string }[];
  record_type?: string;
  record_value?: string[] | string;
  bundle_id?: string;
  app_name?: string;
  category?: string;
  app_description?: string;
  app_icp?: string;
  url?: string;
  service_type?: string;
  service_name?: string;
  favicon_mmh3?: string;
  status_code?: number;
  content_length?: number;
  page_title?: string;
  technologies?: string[];
  auth?: Record<string, unknown>[];
  method?: string;
  params?: Record<string, unknown>[];
  extra?: Record<string, unknown>;
  last_seen: string;
  task_source?: string;
  task_source_summary?: string;
  task_source_node_id?: number;
}

export interface IntentAsset {
  intent_id: number | string;
  asset_id: number;
  type: NewAssetType;
  label: string;
  source: string;
  source_summary: string;
  source_node_id?: number;
  source_task_id: number;
  inherited: boolean;
}

export interface TaskAssetMutation {
  requested: number;
  attached: number;
  existing: number;
}

export interface TaskAssetScopeMutation {
  requested: number;
  assets_linked: number;
  assets_existing: number;
  scopes_added: number;
  scopes_existing: number;
}

// ---- Asset coverage graph (per task) ----
// A node in the force-directed "asset coverage graph". Unique key: asset = "a:<id>", company = "c:<id>",
// root domain with no asset row = "r:<domain>". in_scope=false marks gray context nodes used only for linking.
export interface CoverageGraphNode {
  key: string;
  kind: "company" | "root_domain" | "subdomain" | "ip" | "service" | "app" | "endpoint";
  label: string;
  tested: boolean;
  in_scope: boolean;
  asset_id?: number;
  company_id?: number;
  domain?: string;
  root_domain?: string;
  ip?: string;
  url?: string;
  port?: number;
  service_type?: string;
  app_name?: string;
  page_title?: string;
  status_code?: number;
}

export interface CoverageGraphEdge {
  src: string;
  dst: string;
}

export interface CoverageGraphData {
  nodes: CoverageGraphNode[];
  edges: CoverageGraphEdge[];
}

// Intents/facts/findings linked to an asset in this task's exploration graph (used by the coverage node drawer).
export interface CoverageAssetRef {
  id: number;
  kind: string;
  state: string;
  summary: string;
  source_task_id?: string;
  inherited?: boolean;
}
export interface CoverageAssetRefs {
  intents: CoverageAssetRef[];
  facts: CoverageAssetRef[];
  findings: CoverageAssetRef[];
}

// ---- Workspace file manager (workDir) ----
export interface WorkspaceEntry {
  name: string;
  path: string; // workspace-relative, forward slashes
  dir: boolean;
  size: number;
  mtime: number; // unix millis
}
export interface WorkspaceListing {
  path: string;
  entries: WorkspaceEntry[];
}
export interface WorkspaceFile {
  path: string;
  size: number;
  binary: boolean;
  too_large?: boolean;
  content?: string;
}

// One entry of a task's test scope (coverage denominator + authorization boundary).
export interface TaskScopeRow {
  id: number;
  task_id: number;
  kind: "company" | "root_domain" | "subdomain" | "ip" | "cidr" | "icp" | "keyword";
  company_id?: number;
  company_name?: string; // resolved by the backend via a companies JOIN; only set when kind=company
  domain?: string;
  net?: string;
  value?: string;
  source: "auto" | "agent" | "manual";
  reason?: string;
}

export type CompanyScopeKind = "domain" | "ip" | "cidr" | "icp" | "keyword";

// Structured asset-scope rules submitted when creating a company.
export interface CompanyScopeRule {
  kind: CompanyScopeKind;
  value: string;
}

// Result of a scope write. errors are invalid lines in this submission; warnings are pre-existing data issues, unrelated to this submission,
// that could make ownership assignments surprising (e.g. assets whose ip field contains a hostname).
export interface CompanyScopeMutation {
  added: number;
  skipped: number;
  invalid: number;
  errors?: string[];
  warnings?: string[];
}

// One company asset-scope rule (the single source of truth for ownership).
export interface ScopeRow {
  id: number;
  company_id: number;
  kind: CompanyScopeKind;
  domain?: string; // set when kind=domain
  net?: string; // set when kind=ip|cidr
  value?: string; // may be returned directly by the backend when kind=icp|keyword
  raw: string; // raw user input, used for display and form backfill
  reason?: string;
}

// Company: type=company asset node + icon + asset count + asset scope rules.
export interface Company {
  id: number;
  name: string;
  logo?: string; // remote icon URL; the frontend falls back to the name's initials when empty
  asset_count: number;
  scope?: ScopeRow[];
}

// ---- Exploration graph (per task) ----
export type ExploreKind = "task" | "begin" | "goal" | "intent" | "fact" | "finding" | "hint" | "digest";
export type GoalState = "open" | "met" | "abandoned";
export type IntentState = "open" | "running" | "paused" | "done" | "blocked" | "exhausted" | "stopped";
export type FindingState = "confirmed" | "dismissed";
export type HintState = "active" | "consumed";
export type ExploreRel = "spawns" | "derived_from" | "yields" | "proves" | "covers";

export interface TaskNode {
  id: string;
  type: ExploreKind;
  payload?: string; // raw JSONB (frontend JSON.parses it for summary/executor/review_note/pending_executor keys, U3/U4)
  priority: number; // 0..10
  state: string; // GoalState | IntentState | FindingState | HintState
  origin: string;
  ts: string;
  source_task_id?: string;
  inherited?: boolean;
  delete_reason?: string; // reason given when the intent was soft-deleted (state='deleted')
}

// One broadcast page: nodes paged by creation order + edges touched by this page + nodes at the other end of those edges (refs, indexed by id),
// so every broadcast can explain where it came from and what it produced without downloading the whole graph.
export interface ExplorationNodePage {
  items: TaskNode[];
  total: number;
  page: number;
  size: number;
  edges: Edge[];
  refs: Record<string, TaskNode>;
  // node id → assets anchored to that node (shown when a broadcast expands, covering the page's nodes and their neighbors).
  assets: Record<string, FindingAsset[]>;
}

export interface ExplorationNodeQuery {
  page?: number;
  size?: number;
  kinds?: ExploreKind[];
  states?: string[];
  q?: string;
  order?: "asc" | "desc";
}

// Goal used by the goal-management card (the backend has already split payload into text/vulnclass).
export interface TaskGoal {
  id: string;
  text: string;
  vulnclass?: string;
  state: string; // GoalState
  origin?: string;
  ts: string;
}

// Operational constraint used by the constraint-management card (allow=permit / deny=forbid).
export type ConstraintKind = "allow" | "deny";
export interface TaskConstraint {
  id: string;
  kind: ConstraintKind;
  text: string;
  origin?: string;
  ts?: string;
}

// ---- Findings ----
export type Severity = "critical" | "high" | "medium" | "low";

// Finding handling statuses: pending / in_progress / confirmed / resolved / fixed / false_positive / ignored / duplicate / risk_accepted.
export type FindingStatus =
  | "pending"
  | "in_progress"
  | "confirmed"
  | "resolved"
  | "fixed"
  | "false_positive"
  | "ignored"
  | "duplicate"
  | "risk_accepted";

// FindingAsset is an asset bound to a finding (its label is pre-rendered by the backend).
export interface FindingAsset {
  id: string;
  type: string;
  label: string;
}

export interface Finding {
  traffic_count?: number;
  evidence_version?: number;
  report_evidence_version?: number;
  report_stale?: boolean;
  id: string;
  finding_id?: string; // row id in the standalone findings table — the handle for status updates (may be missing on legacy in-task nodes)
  vulnclass: string;
  name?: string; // finding name; display falls back to vulnclass when empty
  severity: Severity;
  status: FindingStatus;
  summary: string;
  evidence: string;
  report?: string; // detailed report (Markdown); only returned by the detail endpoint, empty in lists
  intent_id?: string;
  param_id?: string;
  task_id?: string;
  task_description?: string;
  source_task_id?: string;
  inherited?: boolean;
  assets?: FindingAsset[];
  ts: string;
}

// FindingsPage is the server-paginated findings list response.
export interface FindingsPage {
  items: Finding[];
  total: number;
  page: number;
  page_size: number;
}

export interface FindingGroup {
  task_id: string | number | null;
  task_name?: string; // optional task name; empty/absent = unnamed
  task_description: string;
  task_status: string;
  count: number;
  critical: number;
  high: number;
  medium: number;
  low: number;
  last_found_at: string;
}

export interface FindingGroupsPage {
  items: FindingGroup[];
  total: number;
  finding_total: number;
  page: number;
  page_size: number;
}

export interface FindingDeepenResponse {
  task_id: string;
  intent_id: string;
  state: IntentState;
  queued: boolean;
}

// FindingStats is a whole-table aggregate (stat cards + vulnclass dropdown), computed server-side and unaffected by paging.
export interface FindingStats {
  total: number;
  pending: number;
  critical: number;
  high: number;
  medium: number;
  low: number;
  vulnclasses: string[];
  tasks: FindingTaskOption[];
}

// FindingTaskOption is one entry of the findings page's "by task" filter dropdown: a task with findings (empty description means the task was deleted,
// and the frontend falls back to its id) plus its finding count.
export interface FindingTaskOption {
  id: string | number;
  name?: string; // optional task name; empty/absent = unnamed
  description: string;
  count: number;
}

// FindingQuery holds the findings list paging/filter/sort parameters.
export interface FindingQuery {
  page: number;
  pageSize: number;
  severity?: "all" | Severity;
  status?: "all" | FindingStatus;
  vulnclass?: string;
  task?: string; // task id; "all"/empty = no task filter
  query?: string;
  sort?: "severity" | "time";
  // Asset-tree node key; selecting a node selects its entire subtree. Empty = no asset filter.
  assetScope?: string;
  // Exact asset-ID set aggregated by host rows; findings linked to any of them match.
  assetIDs?: number[];
}

// ---- Findings by asset (asset view) ----
export type FindingAssetKind = "company" | "root_domain" | "subdomain" | "ip" | "service" | "app" | "endpoint" | "none";

// FindingAssetNode is one node of the asset tree. Keys look like a:<id> (asset), c:<id> (company),
// r:<domain> (root domain with no asset row), __none__ (unlinked assets).
export interface FindingAssetNode {
  key: string;
  parent?: string;
  kind: FindingAssetKind;
  label: string;
  asset_id?: number;
  company_id?: number;
  self: number; // findings attached directly to this asset
  total: number; // including descendants, deduplicated by finding
  critical: number;
  high: number;
  medium: number;
  low: number;
  last_found_at: string;
}

export interface FindingAssetTree {
  nodes: FindingAssetNode[];
  finding_total: number;
  truncated: boolean;
  dropped_kinds?: string[];
}

// FINDING_UNASSIGNED_ASSET mirrors the backend's db.FindingUnassignedAsset.
export const FINDING_UNASSIGNED_ASSET = "__none__";

// ---- Activity / sessions ----
export type ActivityKind =
  | "tool_use"
  | "tool_result"
  | "text"
  | "thinking"
  | "result"
  | "user"
  | "intent" // LLM-generated exploration objective leading a worker session (UI-synthesized)
  | "round" // planner round boundary marker (engine-emitted)
  | "usage" // live cumulative token usage (per model turn); not rendered
  | "llm_switch" // automatic/manual task-level LLM switch
  | "llm_failover" // task-level provider switch / chain exhaustion audit event
  | "intercept_request"; // user-approval request from the intercept layer

// ChatAttachment is an uploaded file: path is relative to the conversation/task workdir (i.e. the agent's CWD).
export interface ChatAttachment {
  name: string;
  path: string;
  size: number;
  abs?: string; // absolute path (returned for scope=staging uploads; written into the description before task creation)
}

export interface Activity {
  seq: number;
  intent_id?: string;
  worker: string; // session owner: planner | mainagent | work#1 ...
  ts: string;
  kind: ActivityKind;
  tool?: string;
  tool_use_id?: string;
  is_error?: boolean;
  summary: string;
  detail?: string;
  metadata?: {
    llm_transition?: LLMTransition;
  };
  source_task_id?: string;
  inherited?: boolean;
  main_seg?: number; // main-agent conversation segment (present only on worker="mainagent" rows)
  // token usage (present only on kind='result')
  input_tokens?: number;
  output_tokens?: number;
  cache_read_tokens?: number;
  cache_write_tokens?: number;
}

export interface LLMAuditProfile {
  id: number;
  name: string;
  format: string;
  model: string;
}

export interface LLMTransition {
  mode: "automatic" | "manual" | "exhausted";
  reason: string;
  previous?: LLMAuditProfile;
  next?: LLMAuditProfile;
}

export interface TaskLLMResolution {
  profile_id?: number;
  name: string;
  format: string;
  model: string;
  source: "task_chain" | "agent_binding" | "global_profile" | "environment" | "global";
  available: boolean;
  reason?: string;
}

export interface TaskLLMResolutions {
  mainagent: TaskLLMResolution;
  planner: TaskLLMResolution;
  worker: TaskLLMResolution;
}

// ---- Agent triggers (P3 scheduling, custom agents only) ----
export interface AgentTrigger {
  id: number;
  agent_key: string;
  enabled: boolean;
  interval_sec: number; // interval: every N seconds (0 = no interval)
  on_finding: boolean; // fires when any task records a finding
  on_goal_met: boolean; // fires when any task reaches a goal
  on_task_timeout: boolean; // fires when any task times out
  on_tool_call: boolean; // fires when a selected tool completes a call
  on_task_create: boolean; // fires when any task is created
  interval_message: string; // independent user message for each trigger condition
  finding_message: string;
  goal_message: string;
  task_timeout_message: string;
  tool_call_message: string;
  task_create_message: string;
  tool_names: string[]; // tool keys selected for on_tool_call (at least one)
  last_fire?: string;
}

// ---- Conversations (chat page) ----
export interface ActiveFindingRetest {
  id: number;
  finding_id: string;
  conversation_id: number;
  status: "pending" | "running";
}

export interface FindingRetest {
  id: number;
  finding_id: number;
  conversation_id: number | null;
  status: "pending" | "running" | "completed" | "failed" | "stopped";
  verdict: "" | "reproduced" | "fixed" | "inconclusive";
  notes: string;
  summary: string;
  evidence: string;
  error: string;
  created_at: string;
  started_at: string | null;
  finished_at: string | null;
}

export interface Conversation {
  id: number;
  running?: boolean; // live server state, returned with the conversation list
  agent_key: string;
  title: string;
  llm_profile_id?: number;
  pinned?: boolean;
  pinned_at?: string | null;
  created_at: string;
  updated_at: string;
}

// ---- Backend logs (/logs page) ----
export interface LogLine {
  seq: number;
  db_id?: number; // server_logs.id; present for DB-persisted lines
  ts: string;
  level: "debug" | "info" | "warn" | "error"; // [fork P-LOG] debug tier (log-level hot switch)
  tag: string;
  text: string;
}

export type SessionRole = "mainagent" | "planner" | "worker" | "system" | "review"; // [fork P-INTENT U5] adds review (review session role, U3)
export type SessionStatus = "running" | "paused" | "done" | "blocked" | "exhausted" | "pending" | "stopped" | "deleted";

// Daily token aggregate bucket (GET /api/tokens/daily).
export interface DailyTokenBucket {
  date: string; // "YYYY-MM-DD"
  input_tokens: number;
  output_tokens: number;
  cache_read_tokens: number;
  cache_write_tokens: number;
}

// Per-worker token usage (GET /api/exploration/tokens).
export interface TokenUsage {
  worker: string;
  input_tokens: number;
  output_tokens: number;
  cache_read_tokens: number;
  cache_write_tokens: number;
}

export interface SessionTokenUsage {
  session: string;
  input_tokens: number;
  output_tokens: number;
  cache_read_tokens: number;
  cache_write_tokens: number;
}

export interface BatchControlItem {
  id: string;
  ok: boolean;
  status?: string;
  queued?: boolean;
  error?: string;
}

// Per-task result of a bulk category change. Failure is only possible when the task was deleted; the category write itself is atomic.
export interface BatchCategoryItem {
  id: string;
  ok: boolean;
  error?: string;
}

// Whole-task (all agents) token aggregate.
export interface TokenTotal {
  input_tokens: number;
  output_tokens: number;
  cache_read_tokens: number;
  cache_write_tokens: number;
}

// Global per-profile token spend from the llm_usage ledger (GET /api/tokens/usage).
export interface ProfileUsage {
  profile_name: string;
  calls: number;
  tasks: number;
  input_tokens: number;
  output_tokens: number;
  cache_read_tokens: number;
  cache_write_tokens: number;
}

// One (profile, UTC day) token bucket for the dashboard's daily chart (new source).
export interface ProfileDayUsage {
  profile_name: string;
  date: string; // YYYY-MM-DD
  input_tokens: number;
  output_tokens: number;
  cache_read_tokens: number;
}

// Response of GET /api/tokens/usage — the dashboard's "new" (llm_usage) token view.
export interface UsageStats {
  by_profile: ProfileUsage[];
  daily: ProfileDayUsage[];
}

// Per-model token usage for one task (GET /api/llm/records/by-model), from the
// always-on llm_usage metering ledger. calls = number of LLM calls on this model.
export interface ModelTokenStat {
  model: string;
  calls: number;
  input_tokens: number;
  output_tokens: number;
  cache_read_tokens: number;
  cache_write_tokens: number;
}

export interface Session {
  id: string;
  role: SessionRole;
  title: string;
  status: SessionStatus;
  live: boolean;
  last_activity: string;
  intent_id?: string;
  source_task_id?: string;
  inherited?: boolean;
  seg?: number; // main-agent session: which conversation segment (0 = original)
  executor?: string; // custom executor key from intent payload (non-review)
}

// ---- Security ----
export interface AuditEntry {
  ts: string;
  tool: string;
  action: "allow" | "block";
  reason?: string;
  command?: string;
}

export interface Audit {
  entries?: AuditEntry[];
  attributions?: Record<string, number>;
}

// ---- Traffic ----
export interface TrafficExchange {
  id: string;
  ts: string;
  host: string;
  method: string;
  url: string;
  status: number;
  content_type: string;
  resp_len: number;
}

export interface TrafficResp {
  enabled: boolean;
  proxy?: string;
  count?: number; // global total (unfiltered)
  total?: number; // rows matching the current filter (for pagination)
  page?: number;
  size?: number;
  exchanges?: TrafficExchange[];
}

// Full raw request/response of one exchange (lazy-loaded on row select).
export interface TrafficDetail {
  req: string;
  resp: string;
}

// One distinct recorded host with its exchange count (target picker).
export interface TrafficHost {
  host: string;
  count: number;
}

// ---- App settings (runtime toggles) ----
export interface Settings {
  traffic_capture: boolean;
  agent_traffic_binding: boolean; // Agent auto traffic-evidence binding, default off; manual binding is unaffected
  llm_record: boolean; // LLM recording switch (default off); no LLM calls are recorded when disabled
  // Web search. brave_key_set / tavily_key_set reflect whether a key is stored
  // (the values are never returned). On PUT, send the corresponding field to set/clear.
  web_search_enabled: boolean;
  web_search_backend: string; // "ddgs" | "brave-free" | "tavily" | "deepseek"
  brave_key_set: boolean;
  tavily_key_set: boolean;
  // write-only: only sent on PUT to store/clear the key.
  brave_search_api_key?: string;
  tavily_search_api_key?: string;
  // Dedicated egress proxy (http/https/socks5) for search endpoints; unrelated to the traffic-recording MITM proxy. Empty = direct.
  web_search_proxy?: string;
  // Global egress proxy (http/https/socks5, optional user:pass) for all target traffic. With traffic capture enabled it acts as the
  // MITM upstream; with capture disabled it is injected directly into agents' bash/WebFetch. Empty = direct.
  global_proxy?: string;
  python_interpreter?: string; // python interpreter path for custom script tools (empty = auto-detect at runtime)
  workers?: number; // concurrent work agents (default 3); applies to tasks started afterwards
  // [P-LOG M0] Log-level hot switch: debug/info/warn/error; effective immediately on selection, no restart needed.
  log_level?: string;
  // Task concurrency cap: max simultaneously running tasks. Off = unlimited; when on, new tasks beyond the cap queue and start automatically when a slot frees.
  task_concurrency_enabled?: boolean; // default false
  task_concurrency_limit?: number; // default 5 when enabled
  // LLM rotation (failover). Default off; when enabled, agents without an assigned model fail over to the next profile
  // when the current one is unavailable (out of balance / invalid key / rate limited / service errors).
  llm_pool_enabled?: boolean; // default false
  // Whether agents/tasks with an explicitly assigned profile also fall back to the rotation chain on failure. Default false = assignment is exclusive.
  llm_pool_bind_fallback?: boolean;
  // Operational-constraint injection scope (all on by default): appends a task's allow/deny constraints to the corresponding agent's system prompt.
  constraints_inject_planner?: boolean;
  constraints_inject_worker?: boolean;
  // Experimental feature: noa model-driven context compaction (default off). The four agent types integrated by the platform (planner/
  // worker/main agent/chat) switch to noa-managed context compaction instead of the built-in one; read once per run and applies to
  // runs started afterwards.
  noa_compaction?: boolean;
  // ---- Finding IM notifications (channels are independent resources, see /api/notify/*; only three global settings here) ----
  notify_enabled?: boolean; // master notification switch, default on; one-click kill switch for maintenance
  notify_public_base_url?: string; // external base URL for finding-detail links; empty = no link in messages
  notify_digest_interval_min?: number; // digest-mode period in minutes, default 30
}

// ---- Finding IM notifications ----

// NotificationFilter holds a channel's filter conditions; all fields optional — empty means no filtering.
// The backend does not validate any field: malformed configs are treated as matches (over-notify rather than miss).
export interface NotificationFilter {
  min_severity?: string; // "" | low | medium | high | critical
  task_ids?: number[]; // empty = unlimited; non-empty requires overlap with the finding's task
  asset_ids?: number[]; // empty = unlimited; non-empty requires overlap with the finding's anchored assets
  vulnclass_include?: string[]; // empty = all classes; non-empty requires the vulnclass to match any keyword (case-insensitive substring)
  vulnclass_exclude?: string[]; // matching any keyword excludes (exclusion beats inclusion)
  on_status_change?: boolean; // whether to also receive finding handling-status change events
}

// NotificationChannel is one channel instance. config fields vary by kind,
// and credential fields are replaced with "__masked__"-prefixed masked values on read — returning them verbatim means "unchanged".
export interface NotificationChannel {
  id: number;
  name: string;
  kind: string;
  enabled: boolean;
  mode: "realtime" | "digest";
  config: Record<string, unknown>;
  filter: NotificationFilter;
  rate_per_min: number;
  created_at: string;
  updated_at: string;
  // secret_keys are provided by the backend per channel type; the frontend uses them to render password inputs and "leave empty to keep" hints,
  // with no channel-specific knowledge hard-coded.
  secret_keys: string[];
}

// NotificationKind is the channel-type metadata returned by /api/notify/meta.
export interface NotificationKind {
  kind: string;
  default_rate_per_min: number;
  secret_keys: string[];
}

export interface NotificationMeta {
  kinds: NotificationKind[];
  enabled: boolean;
  public_base_url: string;
  digest_interval_min: string;
  defaults: { digest_interval_min: number };
  stats: {
    channels: number;
    channels_on: number;
    pending: number;
    failed: number;
    sent_today: number;
    backlog_age_ms: number;
  };
}

// NotificationDelivery is one delivery record, used for delivery history and failed resends.
export interface NotificationDelivery {
  id: number;
  finding_id: string;
  event_kind: string; // finding_created | finding_status_changed
  channel_id: number;
  channel_name: string;
  channel_kind: string;
  state: "pending" | "sending" | "sent" | "failed" | "skipped";
  attempts: number;
  last_error: string;
  batch_id?: number;
  created_at: string;
  sent_at?: string;
  next_attempt_at: string;
  title: string;
  severity: string;
}

// ---- LLM config ----
export interface LLMProfile {
  id: string;
  name: string;
  format: "openai" | "anthropic" | "openai-responses";
  base_url?: string;
  proxy?: string;
  model: string;
  api_key_hint?: string;
  rate_per_second: number;
  rate_per_minute: number;
  context_window_k?: number;
  // Thinking toggle (thinking.type): "" = not sent (default) | "disabled" = off | "enabled" = on
  thinking_type?: string;
  // Thinking effort: "" = not sent (default) | "low"/"medium"/"high"/"xhigh"/"max"
  reasoning_effort?: string;
  is_default: boolean;
  // Rotation order: higher values are tried first. The active profile is always first regardless of this value.
  priority?: number;
  // true = never a failover target (agents/tasks can still bind it explicitly).
  pool_exclude?: boolean;
  // true (default) = streaming (SSE) | false = true non-streaming (stream:false, one complete response).
  streaming?: boolean;
  // Output cap per response (tokens). 0 = field not sent; the server's default applies.
  // Not to be confused with context_window_k: that is the model's total capacity, used locally for the compaction threshold.
  max_tokens?: number;
  // Which request field carries the cap — only meaningful for format="openai":
  // "" = max_tokens (default) | "max_completion_tokens" (required by OpenAI reasoning models)
  max_tokens_field?: string;
  // Custom session header name: when set, every request carries this HTTP header with the current conversation/intent's session id.
  // "" = not sent. For gateways doing prompt caching / sticky routing by session id.
  session_header_key?: string;
  // This profile's retry overrides (connect / empty response / same-provider safety window). Empty/all zeros = follow the global policy.
  retry?: LLMRetryOverride;
}

// ---- LLM retry policy ----
// The two knobs of one retry layer. Both use "0 = not configured":
//   attempts    0 = default attempts | -1 = disable this retry layer | >0 = retry attempts
//   interval_ms 0 = default exponential backoff | >0 = use this fixed millisecond interval instead
export interface LLMRetryRule {
  attempts: number;
  interval_ms: number;
}

// The three layers a single LLM profile can override (all endpoint-following retries).
export interface LLMRetryOverride {
  connect: LLMRetryRule; // connect retry: connection reset/timeouts/429/5xx, before the stream starts
  empty: LLMRetryRule; // empty-response retry: completed with no content at all (openai format only)
  stream: LLMRetryRule; // same-provider safety-window retry: replay after a drop before any output was delivered
}

// Global policy = defaults for the three layers above + two global-only layers:
//   breaker: poll circuit breaker (attempts = consecutive transient failures before tripping, interval_ms = fixed cooldown)
//   intent: intent rerun (the whole intent reruns after a worker ends with model_error)
export interface LLMRetryPolicy extends LLMRetryOverride {
  breaker: LLMRetryRule;
  intent: LLMRetryRule;
}

// ---- LLM rotation (failover) ----
// A profile's position and health within the rotation chain. state:
//   ok: healthy
//   degraded: consecutive failures below the trip threshold
//   tripped: tripped and skipped during cooldown (cooldown_secs = seconds remaining)
export interface LLMPoolMember {
  profile_id: string;
  name: string;
  model: string;
  format: string;
  priority: number;
  active: boolean; // whether this is the active profile (always first in the chain)
  excluded: boolean; // pool_exclude: not in rotation
  state: "ok" | "degraded" | "tripped";
  fails: number;
  trips: number;
  cooldown_secs: number;
  last_error?: string;
  last_at?: string;
}

export interface LLMPoolStatus {
  enabled: boolean;
  bind_fallback: boolean;
  chain: LLMPoolMember[];
}

// ---- Agents ----
export interface Agent {
  id: string;
  key: string; // built-ins are goals/planner/mainagent/worker; custom agents use a user-defined key
  name: string;
  description?: string;
  role: string;
  builtin: boolean;
  enabled: boolean;
  llm_profile_id?: number | null; // bound LLM profile; null/absent = follow task/conversation/global
  max_turns?: number; // 0 = unlimited
  run_seconds?: number; // wall-clock cap for one worker run (seconds); 0 = unlimited
  web_search?: boolean; // whether web search is enabled (gated by the global master switch)
  interactive_shell?: boolean; // whether interactive shell (persistent PTY session tools) is enabled
  // P3 post-trigger run policy (only meaningful for custom agents)
  trigger_run_mode?: "serial" | "parallel"; // serial queue / one concurrent conversation per trigger
  trigger_merge_mode?: "by_task" | "all" | "none"; // serial only: merge by task / merge all / no merging
  trigger_max_parallel?: number; // parallel only: per-agent concurrency cap; 0 = unlimited
  // [fork P-EXEC U5] begin — executor_key (K18: declarable by custom agents only; empty = behavior unchanged, K23)
  executor_key?: string;
  // [fork P-EXEC U5] end
  // [fork P-FINTENT U1] begin — planner_controlled (first-intent gating: true = planned by the Planner,
  // false = no automatic planning until the first intent completes; missing values backfill true; custom agents only)
  planner_controlled?: boolean;
  // [fork P-FINTENT U1] end
  // Binding counts (list endpoint only): visible MCP / visible Skills / bound tools
  mcp_count?: number;
  skill_count?: number;
  tool_count?: number;
}

export interface PromptVar {
  name: string;
  description: string;
  example: string;
  source: "exploration" | "runtime" | "distilled";
}

export interface PromptVersion {
  version: number;
  ts: string;
  note: string;
  template_text: string;
}

export interface AgentDetail {
  agent: Agent;
  prompt: string;
  variables: PromptVar[];
  versions: PromptVersion[];
  visibility: { mcp: number[]; skill: string[] };
  // LLM profile candidates for the "default model" dropdown; the current binding is in agent.llm_profile_id
  llm_profiles?: { id: number; name: string; model: string; is_default: boolean }[];
  wrapup_prompt?: string; // saved wrap-up prompt (empty = built-in default)
  wrapup_default?: string; // built-in default wrap-up prompt (placeholder/restore default)
  wrapup_max_turns?: number; // saved wrap-up turn count (0 = built-in default)
  wrapup_max_turns_default?: number; // built-in default wrap-up turns (for the "0 = default N" hint)
  // Task-timeout wrap-up prompt (worker/planner only; that section only renders when task_timeout_wrapup_supported=true)
  task_timeout_wrapup_supported?: boolean;
  task_timeout_wrapup_prompt?: string;
  task_timeout_wrapup_default?: string;
  task_timeout_wrapup_max_turns?: number;
  task_timeout_wrapup_max_turns_default?: number;
}

// ---- MCP ----
export interface MCPServer {
  id: number;
  name: string;
  transport: "stdio" | "http" | "sse";
  command?: string;
  args: string[];
  env: Record<string, string>;
  url?: string;
  enabled: boolean;
  insecure?: boolean; // http: skip TLS cert verification (self-signed servers)
  tools?: string[]; // mcp_tools_cache (names only, for the count)
}

export interface MCPTool {
  name: string;
  description: string;
}

// ---- Skills ----
// Fields align with the agentskills.io open specification.
// description covers both "what the skill does" and "when to use it".
export interface SkillItem {
  name: string; // unique key = directory name
  description?: string; // required per spec; covers what + when to use
  license?: string; // optional: SPDX identifier or free text
  compatibility?: string; // optional: environment requirements
  mcps?: string[]; // MCP server names this skill unlocks on load
  files: string[]; // files in the skill directory
  // Invocation stats (skill_usage ledger). A never-invoked skill has calls=0 and no last_used.
  calls: number;
  tasks: number; // number of tasks that loaded it (chat sessions excluded)
  usage_agents: string[]; // keys of agents that loaded it
  last_used?: string;
}

// SkillCall is one Skill() invocation (a single skill's recent-invocation list).
export interface SkillCall {
  ts: string;
  agent_key: string;
  task_id: number; // 0 = non-task context (chat session)
  session_id: string;
  args_len: number;
}

// MissingSkill is a skill that was named but doesn't exist — a "wanted but absent" gap.
export interface MissingSkill {
  skill: string;
  calls: number;
  agents: string[];
  last_used?: string;
}

// ---- Tools (built-in tool catalog) ----
// key + handler live in Go; only these fields are page-editable. system tools lock
// the key and the parameter *structure* (name/type/required) — the per-param
// description/default and the agent binding are what move.
export interface Tool {
  key: string;
  system: boolean;
  description: string;
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  schema: Record<string, any>; // full JSON-Schema (object with properties)
  agents: string[]; // bound agent keys
  enabled: boolean;
  kind?: "builtin" | "shell" | "command" | "script" | "http"; // custom tool type
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  exec?: Record<string, any>; // custom tool execution spec (kind != builtin)
  deferred?: boolean; // deferred schema (SearchExtraTools/ExecuteExtraTool)
  calls?: number; // persistent runtime invocation count (older APIs may omit it)
}

// ---- Stats ----
export interface Stats {
  assets: number;
  engine_mode: EngineMode;
  llm_configured: boolean;
  /** The model is connected outside ARTEX (hosted agents); LLM settings are hidden. */
  llm_managed?: boolean;
  roe_enabled: boolean;
  findings_confirmed: number;
  active_task?: Partial<Task>;
}

// ---- Intercept Rules ----
export type InterceptAction = "allow" | "deny" | "ask";
export type InterceptMatchTarget = "tool_name" | "tool_input";
export type InterceptMatchType = "string" | "regex";

export interface InterceptRule {
  id: number;
  name: string;
  enabled: boolean;
  priority: number;
  match_target: InterceptMatchTarget;
  match_type: InterceptMatchType;
  pattern: string;
  action: InterceptAction;
  message: string;
  timeout_enabled: boolean;
  timeout_seconds: number;
  timeout_action: "deny" | "allow";
  created_at: string;
  updated_at: string;
}

// ---- Asset Intercept Rules (asset interception: global blacklist) ----
export type AssetInterceptKind =
  | "exact_domain"
  | "exact_ip"
  | "exact_url"
  | "fuzzy_domain"
  | "fuzzy_ip"
  | "fuzzy_url"
  | "cidr";

// action is only used for task-level rules: block = forbid testing, allow = whitelist.
export type AssetInterceptAction = "block" | "allow";

// Input item for task-level asset block/allow rules (used at task creation and in task-detail editing).
export interface AssetInterceptRuleInput {
  action: AssetInterceptAction;
  kind: AssetInterceptKind;
  pattern: string;
  note: string;
  enabled: boolean;
}

export interface AssetInterceptRule {
  id: number;
  enabled: boolean;
  action?: AssetInterceptAction; // absent on global rules (always block); task-level rules distinguish block/allow
  kind: AssetInterceptKind;
  pattern: string;
  note: string;
  builtin: boolean;
  created_at: string;
  updated_at: string;
}

export interface InterceptPending {
  decision_source?: "rule" | "model" | "unknown" | "";
  id: number;
  rule_id?: number;
  conversation_id?: number;
  task_id?: string;
  agent_name: string;
  tool_name: string;
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  tool_input: Record<string, any>;
  status: "pending" | "allowed" | "denied" | "timeout";
  reason: string; // rule message or model decision reason (model decisions carry a [模型] prefix)
  decided_at?: string;
  created_at: string;
}

// JudgeConfig: global config for model-fallback approval (the model judges only when no intercept rule matched).
export interface JudgeConfig {
  enabled: boolean;
  profile_id: number; // 0 = follow the active/default profile
  prompt: string; // decision prompt; GET backfills the full built-in template when unset
  timeout_seconds: number; // model call timeout
  fail_action: "allow" | "ask" | "deny"; // fallback on model error/timeout/unparseable
  ask_timeout_seconds: number; // human-approval wait timeout after the model escalates to ask
  ask_timeout_action: "allow" | "deny"; // default action after the approval timeout
}

// JudgeUsage: cumulative token usage of model-fallback approval (judge channel) + a daily series for the last N days.
export interface JudgeDayUsage {
  date: string; // YYYY-MM-DD (UTC)
  calls: number;
  input_tokens: number;
  output_tokens: number;
}
export interface JudgeUsage {
  calls: number;
  input_tokens: number;
  output_tokens: number;
  cache_read_tokens: number;
  cache_write_tokens: number;
  daily: JudgeDayUsage[];
}

export interface InterceptApprovalFilter {
  status?: InterceptPending["status"];
  decision_source?: "rule" | "model" | "unknown";
}

// InterceptApprovalRow enriches InterceptPending with conversation/task and rule context.
export interface InterceptApprovalRow extends InterceptPending {
  conv_title: string; // "" if no linked conversation
  conv_agent_key: string; // "" if no linked conversation
  rule_name: string; // "" if rule was deleted
}

// ── Fork types (P-INTENT review domain, vol 01 S6/U3) ──────────────────────────
// [fork P-INTENT U5] begin — review panel data types
/** Pending-review intent (row of GET /tasks/{id}/review/proposed). */
export interface ReviewProposedIntent {
  id: number;
  summary?: string;
  priority?: number;
  origin?: string;
  pending_executor?: string | null; // preselected executor at submission (K24), promoted on approval
  review_note?: string | null; // K20 pre-screen summary (≤200 chars)
  replaces?: number | null; // replacement lineage (old intent id, K21)
  created_at?: number; // unix seconds
  content_version?: number; // PR3: sent back on approve/reject as expected_version for CAS
}
/** Review session message (row of GET /tasks/{id}/review/messages). */
export interface ReviewMessage {
  id: number;
  worker: string; // "review" = agent / "user" = human
  kind: string;
  summary: string;
  detail?: string; // full agent reply (summary holds only the first 200 chars)
  is_error: boolean; // renders as a red error card
  created_at: number; // unix seconds
}
// [fork P-INTENT U5] end

// ── Asset sync (ScopeSentry data source) ──────────────────────────────────────────────
export interface SSProject {
  id: string; // MongoDB ObjectID — used as filter.project
  name: string;
  logo?: string;
  AssetCount?: number;
  tag?: string;
}

export interface SSTask {
  id: string;
  name: string; // used as filter.task
  status?: number;
  progress?: number;
  creatTime?: string;
  endTime?: string;
}

// ConvTokenSummary — one conversation's token total (+ profile/date) for merging
// chat usage into the dashboard token stats. GET /api/tokens/conversations.
export interface ConvTokenSummary {
  llm_profile_id: number | null;
  created_at: string;
  input_tokens: number;
  output_tokens: number;
  cache_read_tokens: number;
  cache_write_tokens: number;
}

// ---- Command recording (Bash execution history) ----
export interface CommandRecord {
  id: number;
  exploration_id: number;
  worker: string;
  tool: string;
  command: string; // raw tool input (JSON)
  output: string;
  is_error: boolean;
  created_at: string;
}

// Per-tool call stats (/commands/stats); errors is the number of failed calls.
export interface ToolStat {
  tool: string;
  total: number;
  errors: number;
}

// ---- LLM recording ----
export interface LLMRecordItem {
  id: number;
  ts: string;
  model: string;
  profile_name: string;
  session_id: string;
  task_id: string;
  worker: string;
  latency_ms: number;
  input_tokens: number;
  output_tokens: number;
  cache_read: number;
  cache_write: number;
  status: string;
  error?: string;
}

export interface LLMRecordDetail extends LLMRecordItem {
  request_body: string;
  response_body: string;
  // Raw HTTP exchanged with the provider: the request is the complete body emitted by buildBody() (including tool
  // schemas); the response is raw SSE frames. request_body/response_body above are normalized views
  // that drop tool schemas and tool_use blocks. Empty on old records.
  raw_request?: string;
  raw_response?: string;
}

// One distinct task with its LLM-record count (task picker on the records page).
export interface LLMTask {
  task_id: string;
  count: number;
}

// The exact JSON sent to the review model, retained for all model verdicts.
export interface InterceptReviewInput {
  version: number;
  background?: {
    // worker_summary is retained only for immutable v2/v3 snapshots.
    source: "user_message" | "worker_summary";
    text: string;
    truncated?: boolean;
  };
  // Version 1 snapshots are immutable and remain readable in historical audits.
  task?: {
    task_id: number;
    description: string;
    goal: string;
    constraints: { id: number; kind: string; text: string; origin: string; created_at: number }[];
    truncated?: boolean;
  };
  working_directory?: string;
  worker_intent?: string;
  turn_input?: string;
  background_truncated?: boolean;
  // Legacy v1/v2 snapshots only; v3 never sends execution history.
  history?: {
    tool_use_id: string;
    tool: string;
    arguments_preview: string;
    result: string;
    status: "succeeded" | "failed";
    truncated?: boolean;
  }[];
  history_truncated?: boolean;
  correlation?: "exact" | "ambiguous" | "unavailable";
  tool_name: string;
  arguments: Record<string, unknown>;
}

// Immutable review snapshot plus separately recorded execution outcome.
export interface InterceptAudit {
  model_input?: InterceptReviewInput;
  model_input_digest?: string;
  run_id?: string;
  tool_use_id?: string;
  correlation: "exact" | "ambiguous" | "unavailable";
  input_digest: string;
  user_message: string;
  user_truncated?: boolean;
  context:
    | { kind: string; tool?: string; tool_use_id?: string; text: string; is_error?: boolean; truncated?: boolean }[]
    | null;
  context_truncated?: boolean;
  captured_at: string;
  model_fallback?: boolean;
  initial_action: "allow" | "ask" | "deny";
  initial_reason: string;
  effective_action?: "allow" | "deny";
  decision_reason?: string;
  rule_name?: string;
  config_digest?: string;
  profile_id?: number;
  execution_status: "not_started" | "not_executed" | "awaiting_result" | "succeeded" | "failed" | "unknown";
  output?: string;
  output_truncated?: boolean;
  execution_ended_at?: string;
}
export interface InterceptDetail extends InterceptApprovalRow {
  audit: InterceptAudit | null;
}

export type TrafficEvidenceRole = "baseline" | "proof" | "verification" | "supporting";
export interface TrafficEvidenceRef {
  traffic_id: string;
  role?: TrafficEvidenceRole;
  note?: string;
}
export interface TrafficEvidenceSnapshot {
  id: string;
  source_traffic_id: string;
  captured_at: number;
  url: string;
  method: string;
  status: number;
  content_type: string;
  req_head?: string;
  resp_head?: string;
  req_hash: string;
  resp_hash: string;
  req_len: number;
  resp_len: number;
}
export interface FindingTrafficBinding {
  id: string;
  finding_id: string;
  snapshot_id: string;
  role: TrafficEvidenceRole;
  note: string;
  position: number;
  created_at: string;
  snapshot: TrafficEvidenceSnapshot;
}
export interface FindingTraffic {
  finding_id: string;
  version: number;
  report_version: number;
  bindings: FindingTrafficBinding[];
}
export interface EvidenceBodyPreview {
  content: string;
  offset: number;
  total: number;
  next_offset: number;
  truncated: boolean;
  binary: boolean;
}
export interface FindingTrafficDetail {
  binding: FindingTrafficBinding;
  request: EvidenceBodyPreview;
  response: EvidenceBodyPreview;
}

// Original execution selected from an approval, never submitted to the reviewer.
export interface InterceptExecution {
  conversation_id: number | null;
  task_id: string | null;
  session: string;
  seq: number;
  items: Activity[];
}

// ---- Task topology editor ----
// Faithful projection of the backend's db/task_topology_editor.go: every write endpoint returns the full graph, and the frontend only
// "request → replace local state with the response" and never infers locally (entry recomputation/orphan repair happens on the backend).
export type TopologyEdgeKind = "solid" | "reachable";
export type TopologyEntryStatus = "" | "suspected" | "confirmed";

// Node coordinates are relative: relative to the group frame's top-left when group_id is set, absolute canvas coordinates otherwise.
export interface TopologyGraphNode {
  ip: string;
  hostname: string;
  display_label: string;
  group_id: number | null;
  x: number;
  y: number;
  entry_status: TopologyEntryStatus;
  color: string;
  description: string;
  updated_by?: string;
  updated_at?: string;
}
export interface TopologyGraphEdge {
  id: number;
  src_ip: string;
  dst_ip: string;
  label: string;
  kind: TopologyEdgeKind;
  evidence_ref: string;
  color: string;
  updated_by?: string;
  updated_at?: string;
}
export interface TopologyGraphGroup {
  id: number;
  name: string;
  x: number;
  y: number;
  width: number;
  height: number;
  color: string;
  updated_by?: string;
  updated_at?: string;
}
export interface TopologyNote {
  id: number;
  body: string;
  created_by: string;
  created_at: string;
}
export interface TopologyGraph {
  nodes: TopologyGraphNode[];
  edges: TopologyGraphEdge[];
  groups: TopologyGraphGroup[];
  notes: Record<string, TopologyNote[]>;
}
// First connection of a new node: empty dst_ip is rewritten to the new node by the backend; empty src_ip is auto-linked to the entry by the backend.
export interface TopologyEdgeInput {
  src_ip?: string;
  dst_ip?: string;
  label?: string;
  kind?: TopologyEdgeKind;
  evidence_ref?: string;
  color?: string;
}
export interface TopologyNodeInput {
  ip: string;
  hostname?: string;
  display_label?: string;
  group_id?: number | null;
  x?: number;
  y?: number;
  entry_status?: TopologyEntryStatus;
}
export interface TopologyNodePatch {
  hostname?: string;
  display_label?: string;
  x?: number;
  y?: number;
  group_id?: number | null;
  entry_status?: TopologyEntryStatus;
  color?: string;
  description?: string;
}
export interface TopologyEdgePatch {
  src_ip?: string;
  dst_ip?: string;
  label?: string;
  kind?: TopologyEdgeKind;
  color?: string;
}
export interface TopologyGroupInput {
  name: string;
  x: number;
  y: number;
  width: number;
  height: number;
  color?: string;
}
