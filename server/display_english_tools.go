// display_english_tools.go: English display localization for built-in tool descriptions.
// 主要函数/类型：
//   - toolDisplayDescriptions: English defaults keyed by built-in tool name.
//   - localizeToolCatalog / localizeTools: translate untouched defaults while preserving customized descriptions.

package server

import (
	_ "embed"
	"encoding/json"

	"artex/agent"
	"artex/db"
	"artex/traffic"
	actool "norma/tool"
)

var toolDisplayDescriptions = map[string]string{
	"graph_overview":               "Distilled exploration-graph overview: asset counts, sites without endpoints, frontier, findings, and hints (strategic guidance from humans or the main agent that must be considered when creating intents). Call this first when planning.",
	"list_findings":                "List confirmed findings for this task and directly linked tasks in compact form (id, task_id, intent_id, vulnerability class, severity, summary, and status). Linked-task entries include source_task_id/inherited=true and are read-only. This contains findings only; use list_facts for ordinary exploration facts and node_detail(id) for full details.",
	"list_facts":                   "Page through exploration facts and conclusions for this task and directly linked tasks, newest first, in compact form (id, summary, and state; long summaries are truncated). All parameters are optional: limit (default 20, maximum 100), before (pass the previous page's next_before cursor for an older page; omit or use 0 for the newest page), and q (filter summaries by keyword). Returns {facts, total, has_more, next_before}. Linked-task entries include source_task_id/inherited=true and are read-only. Use list_findings for findings.",
	"node_detail":                  "Get the complete contents of an exploration-graph node by id from this task or a directly linked task. Inherited nodes include source_task_id/inherited=true and are read-only. Only ids returned by list_facts, list_findings, or graph_overview are accepted; use list_assets or asset_neighbors for assets.",
	"list_intents":                 "Page through intents for this task and directly linked tasks, newest first, in compact form (id, state, priority, summary, and executor; summaries are truncated). All parameters are optional: states (open/running/paused/done/blocked/exhausted/stopped/deleted/proposed; omit for all), q (summary keyword filter), limit (default 20, maximum 100), and before (previous page's next_before cursor). Returns {intents, total, has_more, next_before}. Linked-task entries include source_task_id/inherited=true and are read-only. Use states=[\"proposed\"] for intents awaiting review.",
	"expand_digest":                "Expand a cold digest into its collapsed members in compact form (id, summary, state, and confidence), matching the shape of recent_facts and recent_done_intents in the overview. Use node_detail(member_id) for a member's full details or evidence.",
	"get_worker_output":            "Get the final output or conclusion of an intent (work) in this task or a directly linked task. Linked-task results include source_task_id/inherited=true and are read-only. Normally completed work returns its summary; stopped or failed work returns its last output before termination.",
	"get_worker_trace":             "Inspect an intent's execution process rather than only its final conclusion. Three modes are available:\n1. Pass only intent_id to return a summary stream for every step (summary up to 100 characters, including step_id; action outlines only).\n2. Pass intent_id + q to return only matching step summaries; both summaries and full output are searched.\n3. Pass intent_id + step_ids to return full step details, up to 5 steps per call; additional ids are reported in notice/omitted_step_ids.\nTypically, locate suspicious step ids with mode 1 or 2, then retrieve their full output with mode 3. Thinking steps are excluded. Traces from directly linked tasks are supported and marked source_task_id/inherited=true and read-only.",
	"search_all_worker_traces":     "Usually unnecessary because most information is already provided. Search other work execution traces in this task by keyword (q) to recover something a worker saw but did not record as a fact, such as a path, token, or error. Steps from your own intent are excluded because they are already in your context. Returns only matching step summaries (up to 100 characters) with intent_id values; use get_worker_trace(intent_id, step_ids=[...]) for full content.",
	"add_hint":                     "Attach strategic guidance from a human or the main agent to the exploration graph so the planner reads it before creating the next intents. Prefer batch submission: put multiple hints in the hints array to save round trips. Returns an ids array in the same order as hints (failed entries have id=0; see errors). For one hint, omit hints and provide top-level text.",
	"add_intent":                   "Create an exploration direction directly in the frontier with open status so a worker can claim it immediately, bypassing manual review. Use add_review_intent instead when task review is enabled and the direction requires human approval. An intent is an open exploration direction, not a fixed type; describe what to explore, verify, or exploit in one summary sentence. Prefer batch submission through the intents array. Returns ids in the same order (failed entries have id=0; see errors). For one intent, provide top-level summary.",
	"update_intent":                "Revise an open or rejected intent. summary, priority, asset_ids, and target_executor can be changed; omitted fields remain unchanged. Pending or executing intents cannot be modified. expected_version is required (read content_version with list_intents first). Any modification to a rejected draft automatically resubmits it for review (review_status=pending). Intents with output cannot be changed; cancel them and create a new intent with add_intent or add_review_intent.",
	"cancel_intent":                "Soft-delete an open or proposed intent (state=deleted, preserving its node and lineage and recording delete_reason). Use it to abandon a pending intent, discard a rejected draft, or withdraw an intent submitted for review by mistake. expected_version is required (read content_version with list_intents). Running or paused intents cannot be cancelled; stop them with control_work first.",
	"steer_work":                   "Inject real-time corrective instructions into a running intent without interrupting it or losing progress; the worker receives the instructions before its next action. Use this for corrections within the current direction, such as stopping work on X and focusing on Y. If the entire direction is wrong, use kill_work and create a new intent. Consider checking current activity with get_worker_output first.",
	"set_goals":                    "Add exploration goals to this task. A goal is a final deliverable or verifiable result, not an attack step or reconnaissance action. Prefer batch submission through the goals array; returned ids match the input order (failed entries have id=0; see errors). For one goal, provide top-level text. vulnclass is optional (for example SQLi or IDOR); leave it empty for business-logic goals. The system marks goals met; this tool only adds them.",
	"set_constraints":              "Add operational constraints to this task to bound exploration: type=allow for permitted operations or deny for forbidden operations. Constraints define what may or may not be done, such as testing only the current port, prohibiting writes to a production database, or allowing passive reconnaissance only; they are not goals or attack steps. Prefer batch submission through the constraints array; returned ids match the input order (failed entries have id=0; see errors). For one constraint, provide top-level text/type. Record only constraints explicitly stated in the task goal or description; do not invent them, and use deny when uncertain.",
	"insert_assets":                "Register newly discovered assets in batches, mixing types as needed. Required fields by type: root_domain=domain; ip=ip (IPv4/IPv6, not a hostname); subdomain=domain; app=app_name; HTTP service=url; non-HTTP service=service_name+port with at least one of ip/domain; endpoint=url+method. Other fields are described in the schema. auth, technologies, and params are appended and merged rather than replacing existing values. Returns {results:[{index,id,type}], errors:[{index,error}]}.",
	"add_company_scope":            "Add domains, IPs, CIDR ranges, ICP registrations, or company keywords to a company's asset scope. Domains, networks, and ICP registrations automatically claim matching assets; keywords are scope hints for the agent. Company names are unique: a missing company is created, while an existing one is reused. Each scope entry is auto-detected as a root domain, URL, single IP, CIDR range, ICP registration, or company keyword. Always provide reason with evidence such as WHOIS, certificates, or ASN. Bare TLDs and overly broad networks are rejected (IPv4 /16-/32; IPv6 /32-/128); invalid entries are skipped and returned in errors.",
	"list_assets":                  "Query the asset inventory with a DSL expression or directly by id/ids, with pagination. Returns only assets within the test scope of this task and directly linked tasks. DSL supports field=value fuzzy matching (ILIKE), field==value exact matching, field!=value exclusion, numeric comparisons (> >= < <=), bare full-text terms, AND/OR (AND has higher precedence), and parentheses. Use the separate type parameter for asset type rather than putting it in the DSL. Unless id/ids is supplied, dsl must be non-empty. Fields include domain, root_domain, ip, url, page_title, icp, service_name, app_name, method, service_type, record_type, technology, port, status_code, and company_id. Example: status_code>=400 AND technology=shiro; (port==80 OR port==443) AND technology=nginx.",
	"report_finding":               "Record a confirmed finding with verifiable evidence such as command output or logs. Pass the current intent_id as task context. The returned finding_id identifies the independent finding record, while finding_node_id identifies the exploration node (the first line retains that node number).",
	"record_fact":                  "Write an exploration fact or conclusion to the graph and link it to the producing intent (intent_id). Record positive conclusions such as fingerprinting or enumeration and negative conclusions such as a closed port, a non-injectable parameter, or no login entry found. Combine related observations from one exploration into a single fact rather than fragmenting them: summary is a one-sentence conclusion and detail contains the supporting specifics. Use the facts array only for genuinely distinct conclusions; each entry can omit intent_id and inherit the top-level value. Never speculate: evidence and confidence prevent inaccurate conclusions from polluting the graph. evidence is one concise line containing the command and its most decisive output; confidence is observed (directly seen) or inferred (deduced). Negative conclusions should state only what was observed and a tentative interpretation; the planner decides whether to abandon the direction. For internal-network topology, include topology when confirming interfaces, routes, container networking, session connections, or new internal assets: discovery=how it was found (up to 20 characters), src_ip=parent foothold, dst_ips=target internal IPs, and kind=solid (a finding or successful lateral movement) or reachable (reachability only, the default). Split facts by discovery method or parent. Topology edges are generated automatically and labeled with discovery. Never guess links from shared tasks, subnets, or entry points without evidence.",
	"add_task_scope":               "Add test scope to this task, defining its authorization boundary and the denominator for asset-test coverage. kind supports company, root_domain (including all subdomains), subdomain, ip, cidr, icp, and keyword. Hosts encountered individually by workers are added automatically as exact subdomains; use this tool to deliberately expand scope to an entire root domain or company, or to add a specific subdomain/IP. value accepts a company name or id for company, a domain for root_domain/subdomain, an IP or network for ip/cidr, or a registration/keyword for icp/keyword. Always provide an auditable reason. Use entries for batch submission.",
	"list_untested_assets":         "List assets within this task's and directly linked tasks' scope that are not yet covered by fact anchors. Linked scope is read-only and provided to help you decide whether additional testing is needed, not to make that decision for you. Optionally filter by root_domain, subdomain, service, app, endpoint, or ip. Pagination starts at page 1; page_size defaults to 10. Returns {assets:[{id,type,label}], total, page, page_size}. Task context is required.",
	"list_goals":                   "List this task's goal nodes and their states (open/met) to determine whether objectives have been achieved.",
	"add_review_intent":            "Submit an intent for manual review: it is stored as proposed with review_status=pending and executor=review, so it cannot execute directly and is picked up only by the review panel or review runner. After approval, the same node becomes open while assets and lineage remain unchanged. Unlike add_intent, this tool only submits for review. Supports a single intent or batch submission through intents.",
	"prove_goal":                   "Call when a finding or fact proves that a goal has been achieved: link the evidence node to the goal node and mark the goal met.",
	"goal_met":                     "Finish the entire task immediately only after confirming that every goal has genuinely been achieved and the engagement is complete. A single goal, flag, or finding is not enough; use prove_goal for that. Do not use this to finish a planning round: when no new intents are needed or workers are still producing output, simply end the round; zero new intents is normal. Prefer proving goals individually with prove_goal and reserve goal_met for direct global completion.",
	"kill_work":                    "Terminate a running intent (work) when it is off track or pointless. The intent is marked stopped and is not automatically claimed again. Check its current activity with get_worker_output first.",
	"list_companies":               "List companies in the asset inventory with their asset scopes and attributed asset counts. Use it to discover company_id values for insert_assets or company_id filtering in list_assets. Optional search filters company names case-insensitively; leave it empty to return all companies.",
	"claim_executor_intent":        "Custom executor claim: atomically change a proposed/open intent to running only when payload.executor exactly matches the current agent's executor_key. Callable only by a custom agent declaring executor_key or its internal exec:<key> identity; it bypasses the default Worker. Returns {claimed,state,owner,intent_id,reason}. Repeated claims, executor mismatches, and changed states return claimed=false without modifying the intent.",
	"check_internal_asset_task":    "Check whether an internal penetration-testing subtask already exists for the IP or domain associated with a Finding. Different ports on the same host share one asset key. Must be called before spawn_task.",
	"get_finding_traffic":          "Read verified traffic evidence already bound to a finding, regardless of the capture switch. finding_id is the independent finding-record ID returned by report_finding, not the exploration node ID. Omit binding_id to retrieve the list and version; an empty list is normal for non-HTTP findings or when capture was unavailable. When bindings exist, read bodies in segments using binding_id, side (request/response), and offset. Pass the version read here as evidence_version to update_finding_report; that tool still uses the exploration node ID.",
	"append_finding_to_task":       "Append a Finding's contents and evidence to the internal penetration-testing task responsible for the same asset. task_id is the internal task ID and finding_id is the Finding ID; target_asset is required for multi-host findings. Cross parent/child lineage is allowed, but the target task must register the same asset. Terminal tasks gain a new goal and resume; repeated appends return already_appended.",
	"list_tasks":                   "List all tasks with ID, description, goal, status, run duration, parent task, and LLM profile so the orchestration agent can understand global progress, stalled tasks, and model assignments. Run duration is creation to now for running tasks and creation to last activity for terminal tasks (seconds). llm_profile shows the planner/worker profile name; (active profile) means it follows the global active profile.",
	"list_llm_profiles":            "List available LLM profiles with ID, name, model, format, and whether each is active. Use the ID as spawn_task's llm_profile_id to assign a task-specific model, such as a cheaper model for reconnaissance or a stronger model for exploitation. API keys are never included.",
	"spawn_task":                   "Create a child task and start its exploration engine, returning task_id. The child has an independent exploration graph; when finding_id is provided, only that Finding's complete ancestor chain is copied. parent_ref is optional and records only the parent/child relationship.",
	"pause_task":                   "Pause the specified task by stopping its planner and worker loops.",
	"get_task_graph":               "Read a task's exploration overview (asset counts, frontier, findings, coverage, and more, matching graph_overview) plus its spawned child tasks in child_tasks for dispatch deduplication. Specify the task with task_id.",
	"list_task_constraints":        "Read a task's operational constraints (allow/deny entries with origin). Read parent constraints before spawning a child task; directions explicitly forbidden by those constraints must not be spawned. Specify the task with task_id.",
	"list_task_findings":           "Read a task's confirmed findings, including flags and PoC. Each entry includes id, task_id, intent_id, vulnerability class, severity, summary, and status. Specify the task with task_id.",
	"add_task_hint":                "Inject strategic guidance into a task so its planner reads it before creating the next intents. Prefer batch submission through the hints array; returned ids match the input order and failed entries have id=0. For one hint, omit hints and provide top-level text.",
	"get_task_worker_trace":        "Inspect a work intent in a task: get_task_worker_trace(task_id, intent_id) returns step summaries; add step_ids=[...] to retrieve full contents for up to 5 selected steps.",
	"list_task_worker_traces":      "List work intents that have run in a task and their step counts to identify traces worth inspecting with get_task_worker_trace.",
	"search_task_worker_traces":    "Search all work execution traces in a task by keyword, returning matching step summaries and intent_id values.",
	"get_task_node_detail":         "Read a complete exploration node in a task (finding, fact, intent, or goal), including summary, details, evidence, and PoC. id is the exploration node ID returned by report_finding or list_task_findings, not an asset ID. Use it to retrieve complete finding evidence before writing a report.",
	"update_finding_report":        "Write or replace a registered finding's complete detailed Markdown report. finding_id is the ID returned by report_finding (the number in \"finding recorded: <id>\"). Recommended sections: overview, impact, reproduction steps, evidence/PoC, and remediation.",
	"dispatch_finding_to_agent":    "Explicitly dispatch a Finding from the current Planner task to an existing, enabled, non-built-in custom agent. finding_id is the independent findings.id, not finding_node_id. Each agent_key+finding_id pair is recorded once; already_invoked returns the first conversation ID, while retry_scheduled means no conversation has been created yet and a fallback retry is pending. enqueued/retry_scheduled does not mean the downstream agent has run or completed. The caller's system prompt defines the allowed scenarios and required evidence.",
	"delete_assets_by_host":        "Delete assets by exact host: the host's domains/subdomains and their services and endpoints. Host matching is exact after lowercasing and trimming, never fuzzy or wildcard-based. A root domain also deletes its subdomains and their services/endpoints; a subdomain or IP deletes only that host and its own services/endpoints. Warning: hard deletion across the global shared asset inventory; irreversible.",
	"create_skill":                 "Create a new skill by writing an agentskills.io-compliant SKILL.md. The name must start with a lowercase letter and contain only lowercase letters, digits, and hyphens. Use this to install new platform capabilities.",
	"update_skill_file":            "Write or replace one file in a skill (SKILL.md by default). Use it to modify skill content or add scripts and references.",
	"create_custom_tool":           "Important: use this to install tools the platform does not already provide so agents can call them. Creates a custom tool (shell/command/script/http). shell declares a Bash environment and needs only key, description, and agents; exec and schema are not required.",
	"update_custom_tool":           "Modify an existing custom tool by key.",
	"create_mcp":                   "Create an MCP server (stdio/http/sse). After creation, its tools must be granted visibility to each agent.",
	"update_mcp":                   "Modify an existing MCP server by ID.",
	"bind_finding_traffic":         "Bind verified real HTTP traffic to a registered finding. finding_id is the independent finding-record ID, not the exploration node ID. All references in a batch succeed or fail together, and repeated references do not overwrite existing descriptions. Binding marks existing reports for review; do not reprobe or recreate findings solely to add packaging evidence.",
	"get_finding_retest_context":   "Read the finding evidence snapshot, retest state, supplemental notes, and current task constraints for this retest conversation. Takes no parameters and can read only the current conversation.",
	"record_finding_retest_result": "Save the single conclusion for this retest conversation without changing the original finding evidence or report. If the conversation finishes successfully with verdict=fixed, the finding is automatically marked fixed; other verdicts preserve its state. Include evidence from the checks actually performed, or explain any blocker when confirmation is impossible.",
	"traffic_search":               "Search target traffic captured by the recording proxy. host is required and accepts a bare host, host:port, or full URL; optional filters include URL substring and request/response body text. A port restricts results to that service, preventing traffic from different ports on the same IP from being mixed. body_contains performs full-text search across captured headers and bodies (minimum 3 characters). Returns only lightweight index entries (id/method/url/status/resp_len), never response content. Use traffic_get to verify each relevant exchange before passing its ID to bind_finding_traffic. Defaults to 3 results with at most 10 per page; use page for additional results.",
	"traffic_get":                  "Retrieve one captured exchange's request and response text by ID (large content is truncated). Use with traffic_search instead of repeating requests with curl.",
	"traffic_blob":                 "Read an oversized request or response body in segments. When traffic_get shows '…[truncated] @blob sha256:<hash>', pass that hash here to retrieve the complete content. Returns at most 8KB per call; use offset to continue reading and the returned total length to track progress. Useful for backup files, source-code disclosures, large JSON exports, and other responses above the inline threshold.",
}

// Legacy defaults are retained only to recognize saved, unmodified installations.
//
//go:embed english_legacy_tools.json
var legacyToolDescriptionsJSON []byte

var legacyToolDescriptions = func() map[string]string {
	var descriptions map[string]string
	if err := json.Unmarshal(legacyToolDescriptionsJSON, &descriptions); err != nil {
		panic(err)
	}
	return descriptions
}()

var builtinToolDisplayDescriptions = func() map[string]englishDisplayText {
	translations := make(map[string]englishDisplayText, len(toolDisplayDescriptions))
	for _, seed := range agent.BuiltinToolSeeds() {
		if english, ok := toolDisplayDescriptions[seed.Key]; ok {
			translations[seed.Key] = englishDisplayText{legacy: legacyToolDescriptions[seed.Key], english: english}
		}
	}
	return translations
}()

func localizeToolWithTranslations(tool *db.Tool, translations map[string]englishDisplayText) {
	if tool == nil || !tool.System {
		return
	}
	localizeToolSchema(tool)
	if translation, ok := translations[tool.Key]; ok && tool.Description == translation.legacy {
		tool.Description = translation.english
	}
}

func localizeTool(tool *db.Tool) {
	localizeToolWithTranslations(tool, builtinToolDisplayDescriptions)
}

func localizeTools(tools []*db.Tool, defaultGroups ...[]actool.CoreTool) {
	translations := builtinToolDisplayDescriptions
	if len(defaultGroups) > 0 {
		translations = make(map[string]englishDisplayText, len(builtinToolDisplayDescriptions)+len(toolDisplayDescriptions))
		for key, translation := range builtinToolDisplayDescriptions {
			translations[key] = translation
		}
		for _, group := range defaultGroups {
			for _, defaultTool := range group {
				if english, ok := toolDisplayDescriptions[defaultTool.Name()]; ok {
					translations[defaultTool.Name()] = englishDisplayText{legacy: legacyToolDescriptions[defaultTool.Name()], english: english}
				}
			}
		}
	}
	for _, tool := range tools {
		localizeToolWithTranslations(tool, translations)
	}
}

func localizeToolCatalog(s *Server, tools []*db.Tool) {
	localizeTools(tools, s.orchestrationTools(), s.platformTools(), s.findingRetestTools(), traffic.SeedToolMetas())
}
