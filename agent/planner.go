// planner.go：规划者——读态势、判目标、生成探索意图的执行循环与态势渲染。
// 主要函数/类型：
//   - Planner / NewPlanner：规划者状态与构造。
//   - Plan：跑一轮规划（装配工具、渲染触发与态势、执行、返回目标是否达成）。
//   - plannerSystem：四级模板覆盖链渲染规划者系统提示词。
//   - TriggerEvent / renderTriggers：本轮触发事件结构及提示词渲染。
//   - renderGraphOverview / intentSummary / workerOutput：图态势概览与意图/产物摘要。
//   - renderPlannerTodos / todoFor：待办渲染与会话待办存储。

package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log" // 【二开·P-TPL·A4】四级链回退 WARN
	"strings"
	"sync"

	"artex/db"
	"artex/intercept"
	"artex/xlog" // 【二开·P-TPL·A4】四级链命中 DEBUG
	"norma/agentcore"
	"norma/llm"
	"norma/permission"
	actool "norma/tool"
	"norma/transcript"
)

// Planner is the event-driven LLM planner (docs §4.3): each time the asset or
// exploration graph changes (debounced), it reads the exploration route, queries
// assets, judges whether the task goal is met, and emits 0..N exploration intents
// into the frontier. It is the sole intent generator.
type Planner struct {
	findingRecorder   FindingRecorder
	prov              llm.Provider
	model             string
	tx                *transcript.Store                      // raw LLM conversation persistence (nil = off)
	window            int                                    // context window in tokens (for compaction)
	windowFn          func() int                             // optional dynamic task-chain minimum
	maxTurns          int                                    // max agent turns per run (0 = unlimited)
	killWork          func(intentID int64) error             // engine callback to terminate a running work (nil = off)
	steerWork         func(intentID int64, msg string) error // engine callback to steer a running work mid-run (nil = off)
	proxyAddr         string                                 // recording proxy for WebFetch (empty = direct)
	proxyCACert       string                                 // recording proxy's CA cert path (HTTPS verify)
	webSearch         WebSearchOpts                          // web_search tool backend selection (off by default)
	workDir           string                                 // shared work dir (surfaced in prompt as artifact-output target)
	injectConstraints func() bool                            // resolver: inject task operation constraints into system prompt? (nil = yes)
	// 【二开·P-TPL·A4】起 —— 任务级 planner 提示词快照读取器（tasks.planner_prompt_override；
	// 建任务时原子写入，运行中无修改入口；每轮现读保持与其它 resolver 同风格）
	taskPromptFn func() string
	// 【二开·P-TPL·A4】止
	nonStreamingFn func() bool // resolver: use non-streaming (Complete) path? (nil = streaming)
	noaEnabledFn   func() bool // resolver: use experimental noa compaction? (nil = off)
	maxTokensFn    func() int  // resolver: per-reply output cap (nil/0 = send no cap)
	compactor      *Compactor  // cold-node compaction (§7); nil = disabled
	// 【二开·审核流 PR1】planner 侧审核流回调：每轮 Plan 注入 ToolSet。
	notifyReview    func(taskID int64)      // §2.6：送审成功唤醒 ReviewRunner（nil = 只靠 15s ticker 兜底）
	reviewEnabledFn func(taskID int64) bool // §2.4：任务级审核总开关即时解析（nil = 不校验）
	checkExecutor   func(key string) error  // §2.9.3：target_executor 合法性校验（nil = 不校验）

	// todos keeps ONE plan-scratchpad per task (keyed by exploration id) so the
	// planner's multi-step plan survives across wake-ups — each Plan() is a fresh
	// session, but the shared store lets it record a serial exploit chain once and
	// dispatch it step-by-step over rounds instead of front-loading it in parallel.
	todoMu sync.Mutex
	todos  map[int64]*actool.TodoStore
}

func NewPlanner(prov llm.Provider, model, workDir string, tx *transcript.Store, window, maxTurns int) *Planner {
	return &Planner{prov: prov, model: model, workDir: workDir, tx: tx, window: window, maxTurns: maxTurns, todos: map[int64]*actool.TodoStore{}}
}

func (p *Planner) SetCompactionWindowResolver(fn func() int) { p.windowFn = fn }

// SetCompactor wires the cold-node compactor (cold-digest §7). Called each
// planner wake-up to advance the round counter, maintain cold stamps, and
// (off the hot path) fold cold nodes into digests. nil = feature disabled.
func (p *Planner) SetCompactor(c *Compactor) { p.compactor = c }

// SetNonStreaming wires a resolver deciding whether runs use the non-streaming
// model path (true = non-streaming). nil/unset = streaming (default).
func (p *Planner) SetNonStreaming(fn func() bool) { p.nonStreamingFn = fn }

func (p *Planner) nonStreaming() bool { return p.nonStreamingFn != nil && p.nonStreamingFn() }

// SetNoaEnabled wires a resolver deciding whether runs use the experimental noa
// context-compression mechanism. nil/unset = off (built-in compaction). Read per
// run so the settings toggle takes effect without rebuilding the agent.
func (p *Planner) SetNoaEnabled(fn func() bool) { p.noaEnabledFn = fn }

// SetMaxTokens wires a resolver for the per-reply output cap. nil/unset or 0 =
// send no cap and let the endpoint decide. Read per run, like nonStreaming.
func (p *Planner) SetMaxTokens(fn func() int) { p.maxTokensFn = fn }

func (p *Planner) maxTokens() int {
	if p.maxTokensFn == nil {
		return 0
	}
	return p.maxTokensFn()
}

func (p *Planner) compactionWindow() int {
	if p.windowFn != nil {
		return p.windowFn()
	}
	return p.window
}

// SetProxy points the planner's WebFetch at the recording proxy plus the CA cert
// it trusts to verify HTTPS through it (empty addr = direct).
func (p *Planner) SetProxy(addr, caCert string) { p.proxyAddr, p.proxyCACert = addr, caCert }

// SetWebSearch selects the web_search backend for the planner (off by default).
func (p *Planner) SetWebSearch(o WebSearchOpts) { p.webSearch = o }

// SetConstraintInject wires a resolver deciding whether this task's operation
// constraints get injected into the planner system prompt. Read per round so the
// settings toggle takes effect without rebuilding the agent. nil = inject (default).
func (p *Planner) SetConstraintInject(fn func() bool) { p.injectConstraints = fn }

// 【二开·P-TPL·A4 planner 提示词四级链】
// 功能介绍：SetTaskPromptOverride 接线任务级提示词快照读取器（四级链第①级来源），
//
//	仿 :101 SetConstraintInject 风格；nil = 无任务级覆盖（落②③④）。
func (p *Planner) SetTaskPromptOverride(fn func() string) { p.taskPromptFn = fn }

// taskPromptOverride 每轮现读任务级快照（空串=未设置）。
func (p *Planner) taskPromptOverride() string {
	if p.taskPromptFn == nil {
		return ""
	}
	return p.taskPromptFn()
}

// wantConstraints reports whether constraint injection is enabled (default yes).
func (p *Planner) wantConstraints() bool { return p.injectConstraints == nil || p.injectConstraints() }

// todoFor returns the task's persistent planning todo store, creating it on first
// use. Shared across all of this task's planner wake-ups.
func (p *Planner) todoFor(expID int64) *actool.TodoStore {
	p.todoMu.Lock()
	defer p.todoMu.Unlock()
	s := p.todos[expID]
	if s == nil {
		s = actool.NewTodoStore()
		p.todos[expID] = s
	}
	return s
}

// SetKillWork wires the engine's per-work terminate callback so the planner's
// kill_work tool can stop a single running worker.
func (p *Planner) SetKillWork(fn func(intentID int64) error) { p.killWork = fn }

// SetSteerWork wires the engine's per-work steering callback so the planner's
// steer_work tool can inject a mid-run course-correction into a running worker.
func (p *Planner) SetSteerWork(fn func(intentID int64, msg string) error) { p.steerWork = fn }

// 【二开·审核流 PR1/§2.6】SetNotifyReview wires the ReviewRunner wake channel for
// the planner's add_review_intent（送审即唤醒，不再空等一个扫描周期）。
func (p *Planner) SetNotifyReview(fn func(taskID int64)) { p.notifyReview = fn }

// 【二开·审核流 PR1/§2.4】SetReviewEnabled wires the per-task review gate resolver
// (tasks.review_enabled，落库前即时现读)。
func (p *Planner) SetReviewEnabled(fn func(taskID int64) bool) { p.reviewEnabledFn = fn }

// 【二开·审核流 PR1+PR8/§2.9.3】SetExecutorChecker wires the target_executor validator
// (server 侧复用 engine.resolveExecutor)。
func (p *Planner) SetExecutorChecker(fn func(key string) error) { p.checkExecutor = fn }

// renderPlannerTodos formats the persistent planning todo for injection into the
// wake-up prompt (empty when there are no todos yet — first wake-up).
func renderPlannerTodos(items []actool.Todo) string {
	if len(items) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\n[Your planning todo list, preserved across wakeups from the previous round]:\n")
	for _, it := range items {
		mark := map[actool.TodoStatus]string{actool.TodoPending: "☐", actool.TodoInProgress: "▶", actool.TodoCompleted: "✔"}[it.Status]
		if mark == "" {
			mark = "☐"
		}
		b.WriteString(fmt.Sprintf("  %s %s\n", mark, it.Content))
	}
	b.WriteString("Advance this plan: dispatch only steps whose prerequisites are complete and dependent facts exist. Update TodoWrite (mark steps satisfied by facts completed). Do not redispatch steps already pending/in_progress.")
	return b.String()
}

// TriggerEvent describes what concretely caused this planning round to fire, so
// the planner looks first at the actual change instead of re-scanning the whole
// overview. Kind:
//
//	"done"    — a worker finished intent IntentID (its output conclusion is fetched).
//	"finding" — a worker reported a finding on intent IntentID (Detail = 摘要).
//	"goal"    — the human (via 主 agent 的 set_goals) added one OR MORE goals in a
//	            single call (Goals = 本次新增的目标文本，1+ 条；set_goals 支持批量).
//	"goal_deleted" — the human deleted a goal from 总览的目标管理 (Detail = 被删目标文本).
//	"goal_edited"  — the human edited a goal from 总览的目标管理 (OldGoal→NewGoal 文本).
//	"cancelled" — the human deleted intent IntentID (Detail = 删除原因). The intent is
//	            stopped (not deleted) and the reason is attached to it as a fact.
//	"revision_requested" — 意图 IntentID 被审核驳回退回修订（Detail = 驳回理由）。
//	            【二开·审核流 PR5/§4.2.3】三件套之一：本 case 必须存在——switch 无枚举
//	            校验，漏加 case 会静默落进 default(="done") 文案，既不报错也不像驳回。
type TriggerEvent struct {
	Kind     string
	IntentID int64
	Detail   string
	Summary  string   // Kind=="cancelled" 专用：删除前捕获的意图摘要（真删除后节点已不存在，无法再查）
	Goals    []string // Kind=="goal" 专用：本次 set_goals 新增的目标文本（1 条或多条）
	OldGoal  string   // Kind=="goal_edited" 专用：修改前的目标文本
	NewGoal  string   // Kind=="goal_edited" 专用：修改后的目标文本
	Hints    []string // Kind=="hint" 专用：本次 add_hint 新增的提示文本（1 条或多条）
}

// renderTriggers spells out the change(s) that fired this round: for a finished
// worker — which intent + its output conclusion; for a finding — which intent +
// what was found. Empty for time/heartbeat wakes. Reads the store (best-effort;
// a blank field never blocks the round).
func renderTriggers(ts *db.ExplorationStore, evs []TriggerEvent) string {
	if len(evs) == 0 || ts == nil {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\n[Actual changes triggering this round: read these before deciding whether to add directions]:")
	for _, ev := range evs {
		switch ev.Kind {
		case "goal":
			if len(ev.Goals) == 1 {
				b.WriteString(fmt.Sprintf("\n- The human (Main Agent) added a goal: %s. Add an exploration direction for this new goal if no corresponding intent exists.", ev.Goals[0]))
			} else {
				b.WriteString(fmt.Sprintf("\n- The human (Main Agent) added %d goals: %s. Add a direction for each new goal without a corresponding intent.", len(ev.Goals), strings.Join(ev.Goals, "；")))
			}
		case "hint":
			if len(ev.Hints) == 1 {
				b.WriteString(fmt.Sprintf("\n- The human (Main Agent) added a strategic hint: %s. It is attached to the exploration graph; adjust/add directions accordingly if no corresponding intent exists.", ev.Hints[0]))
			} else {
				b.WriteString(fmt.Sprintf("\n- The human (Main Agent) added %d strategic hints: %s. They are attached to the exploration graph; adjust/add directions for each as appropriate.", len(ev.Hints), strings.Join(ev.Hints, "；")))
			}
		case "goal_deleted":
			b.WriteString(fmt.Sprintf("\n- The human deleted goal: %s. Reassess the remaining goals/directions; do not dispatch intents for the removed goal.", ev.Detail))
		case "goal_edited":
			b.WriteString(fmt.Sprintf("\n- The human changed a goal from '%s' to '%s'. Adjust exploration directions to the new goal; stop dispatching directions that no longer apply.", ev.OldGoal, ev.NewGoal))
		case "finding":
			b.WriteString(fmt.Sprintf("\n- The Worker for intent #%d (%s) reported a finding: %s", ev.IntentID, intentSummary(ts, ev.IntentID), ev.Detail))
		case "cancelled":
			// 意图内容优先用删除时捕获的 Summary（真删除后节点已不存在，intentSummary 查不到）。
			sm := ev.Summary
			if sm == "" {
				sm = intentSummary(ts, ev.IntentID)
			}
			b.WriteString(fmt.Sprintf("\n- The user deleted intent #%d. Intent: %s; deletion reason: %s. It will no longer execute; replan accordingly.", ev.IntentID, sm, ev.Detail))
		case "revision_requested":
			// 【二开·审核流 PR5/§4.2.3】驳回退回修订：明确指向修订同一节点，禁止新建替代。
			b.WriteString(fmt.Sprintf(
				"\n- Intent #%d was rejected in review. Reason: %s. Use update_intent to revise this same intent"+
					" (do not create a replacement or lose its assets and lineage). Editing automatically resubmits it for review.",
				ev.IntentID, ev.Detail))
		default: // "done"
			b.WriteString(fmt.Sprintf("\n- The Worker for intent #%d (%s) finished. Conclusion: %s", ev.IntentID, intentSummary(ts, ev.IntentID), workerOutput(ts, ev.IntentID)))
			if fids := factIDsYielded(ts, ev.IntentID); fids != "" {
				b.WriteString(fmt.Sprintf("; new fact IDs produced by this intent: %s ", fids))
			}
		}
	}
	b.WriteString("\n(Use node_detail / get_worker_output / list_findings for complete details.)")
	return b.String()
}

// factIDsYielded lists the fact ids an intent produced this run as "#12、#15", so the
// planner can jump straight to the round's incremental facts. Empty (best-effort) when
// the intent yielded no facts or the lookup fails.
func factIDsYielded(ts *db.ExplorationStore, id int64) string {
	ids, err := ts.FactsYielded(id)
	if err != nil || len(ids) == 0 {
		return ""
	}
	parts := make([]string, len(ids))
	for i, fid := range ids {
		parts[i] = fmt.Sprintf("#%d", fid)
	}
	return strings.Join(parts, "、")
}

// intentSummary reads an intent node's one-line summary (best-effort, "?" on miss).
func intentSummary(ts *db.ExplorationStore, id int64) string {
	n, err := ts.GetNode(id)
	if err != nil || n == nil {
		return "?"
	}
	var p map[string]any
	if json.Unmarshal(n.Payload, &p) == nil {
		if s, ok := p["summary"].(string); ok && s != "" {
			return s
		}
	}
	return "?"
}

// workerOutput returns the finished worker's conclusion for an intent — the last
// 'result' (else 'text') activity's full detail, truncated. Same source get_worker_output uses.
func workerOutput(ts *db.ExplorationStore, id int64) string {
	acts, _, err := ts.ActivityList(&id, 0, 1000)
	if err != nil {
		return "(Failed to retrieve output)"
	}
	var pick *db.Activity
	for i := range acts {
		if acts[i].Kind == "result" {
			pick = &acts[i]
		} else if acts[i].Kind == "text" && pick == nil {
			pick = &acts[i]
		}
	}
	if pick == nil {
		return "(This work has no output records yet)"
	}
	out, _ := ts.ActivityDetail(pick.ID)
	if out == "" {
		out = pick.Summary
	}
	return truncOutput(out, 800)
}

// truncOutput caps a worker-output blob so the trigger context doesn't bloat the
// system prompt every round; full text is one get_worker_output call away.
func truncOutput(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + " …(truncated; see get_worker_output for complete output)"
}

// renderGraphOverview folds the pre-computed graph_overview snapshot into the
// wake-up prompt so the planner starts each round with the full situation in
// hand — saving the round-trip it would otherwise spend calling the tool. It is
// the exact same JSON graph_overview would return; deeper detail is still one
// tool call away (node_detail / list_facts / …).
func renderGraphOverview(data map[string]any) string {
	b, err := json.Marshal(data)
	if err != nil {
		return "" // fall back to the model calling graph_overview itself
	}
	return "\n\n[Current situation: prefetched graph_overview, identical to calling that tool; use node_detail/list_facts for details as needed]:\n" + string(b)
}

// plannerDefaultTmpl is the built-in EDITABLE body (段 [A]) of the planner prompt,
// seeded into agent_prompts. Goal is a {{.Goal}} template var; the 中间产物输出规约
// tail is code-owned (artifactSpec) and appended by plannerSystem after rendering.
const plannerDefaultTmpl = `You are the Planner of an authorized penetration testing system. You wake frequently when the graph changes. Read the situation, assess goals, and add exploration intents only for genuinely uncovered directions. Plan and describe intents or assess goals; do not do the Workers' execution yourself.

Task goal: {{.Goal}}

**How many intents to produce this round**
- Highest-priority floor: if the goal is incomplete and no open/running/assigned intents exist (frontier_open=0 and running_intents/assigned_intents empty), produce at least one intent advancing the goal. With no active or queued work, zero intents stalls the task. Even if all known directions appear only in recent_done, open a new direction or continue one according to done/exhausted/blocked evidence below.
- Otherwise, zero intents is valid only for a reason: every proposed direction is already covered by open/running/assigned work, or the next step depends on output from active work that does not yet exist. Do not create duplicates under new wording or dispatch downstream work without prerequisites.
- Dispatch genuinely uncovered independent directions and remaining in-scope untested surfaces when goals remain incomplete. Zero intents is not a default shortcut.

**Decision workflow on each wakeup**

1. The complete situation is attached below, identical to graph_overview; do not fetch it again. It includes task title/goal/root, asset counts, goals/states, open/running/assigned/recent_done intents, sites_without_endpoints, facts count, and recent_facts {id,summary,confidence?}.
   - Scope: exploration nodes (goals/intents/facts/findings) belong to this task; the asset graph is globally shared. Counts are global within scope, not exclusively this task. Ignore unrelated assets.
   - Lineage: intents have parents (upstream derivation) and yields (produced facts/findings); recent_facts have from_intent. Use them to understand origin and synthesize directions.
   - Negative/uncertain observations such as closed ports/noninjectable parameters are observations, not final truth. Inspect node_detail evidence. Treat a direction as temporarily closed only with strong observed evidence and exhausted reasonable methods. Missing evidence, one attempt, apparent behavior, or inferred confidence means unexplored. If in scope and uncovered, dispatch verification; review the same negative direction at most once. Respect a reasonable negative result after that review.
   - Fetch details only as needed: list_facts (newest first, default 20, q filter, before cursor, total/has_more), list_findings, node_detail(id), list_assets (search/filter/pagination or id/ids), and asset_neighbors. Do not pull all globally shared assets by default.

2. Assess goals, your central responsibility. For each unmet goal proved by a finding/fact, call prove_goal(goal_id,evidence_id,reason). Proving the last unmet goal automatically completes the task; completion follows individual proofs, not a one-click shortcut.
   - Quantitative acceptance: before prove_goal, check actual graph_overview measurements for requested coverage, flag count, or privilege. Never mark met early because the core result seems mostly achieved. A 100% coverage requirement with coverage.pct=40% is incomplete; dispatch work to close the gap.
   - For vulnclass=getshell goals, prioritize structured Finding vulnclass over isolated keywords. Only a confirmed webshell/reverse_shell/getshell Finding targeting the correct asset and proving a usable foothold can satisfy the goal.
   - Valid evidence includes a connected WebShell, successful shell_test, target command execution through AntSword/Behinder/Godzilla, or actual reverse/C2 sessions: session opened, reverse connection accepted, beacon/meterpreter/C2 online, callback received, and interactive target command output. An attacker-local Shell or started listener is not a target session.
   - RCE/id/whoami/root output alone, successful file/JSP upload, file existence, HTTP 200, a deployed payload, deserialization, privilege escalation, a listening port, or PoC success alone does not prove getshell. It may satisfy the corresponding rce/file_upload/deserialization/sqli/privilege-verification goal without requiring a Shell.
   - If a getshell goal has only RCE/upload/ordinary facts, do not prove it. Dispatch establishment and verification of an interactive foothold. Record real blockers on failure; never close early or ask Workers to fabricate evidence. The same acceptance checks apply before goal_met; do not bypass unmet getshell goals.

**Dispatch downstream Agents for Findings**
Current scenario: External Penetration Testing.
Allowed agent_key values only:
- vuln_dispatcher: assess and dispatch/append follow-up exploitation or post-exploitation tasks from Finding evidence.
- trace_cleaner: verify this task's deployment facts, prepare a cleanup plan, and execute after confirmation.

Use the independent finding_id (findings.id), not finding_node_id. Read Finding details and proof/verification traffic, and the originating Worker trace when needed. Determine execution_mode; missing/unrecognized means foothold. Evaluate each Agent independently for each Finding; dispatch to one does not exclude another.
- vuln_dispatcher: foothold mode requires a usable connected WebShell, genuine interactive command output, or an online C2/reverse session. vulnerability_only mode requires confirmed reproducible vulnerability evidence without a foothold. If satisfied, call dispatch_finding_to_agent(agent_key="vuln_dispatcher", finding_id=<independent ID>, reason=<evidence, execution_mode, dispatch reason>). Otherwise explain missing evidence.
- trace_cleaner: requires both a usable foothold and at least one confirmed [Deployment] fact in this task. Call dispatch_finding_to_agent(agent_key="trace_cleaner", finding_id=<independent ID>, reason=<foothold evidence, Deployment fact IDs, reason>). Otherwise explain which prerequisite is missing. It preserves WebShell footholds and forensic evidence by default and decides which artifacts are no longer needed during cleanup audit. Preparing the plan and obtaining real user confirmation happen later and do not block this dispatch.
- Deduplicate by agent_key+finding_id; never redispatch the same Finding to the same Agent. Existing invocation/conversation for one Agent does not affect another Agent. enqueued/retry_scheduled means queued/awaiting redelivery, not executed/completed. Do not wait for downstream results or invent their status. At round end, briefly record actual dispatch results by Finding and unmet conditions for non-dispatch.

3. Optional minimal initial reconnaissance: only when the task has almost no facts and the situation is insufficient to describe initial intents, use a tiny amount of read-only probing (for example 1–2 homepage/fingerprint curl requests). Its only output is a more precise intent description, never vulnerability discovery/verification/exploitation or endpoint/directory/parameter enumeration.
   - Once Worker facts exist (facts>0 or recent_facts nonempty), do not probe yourself. Base decisions on facts; dispatch Workers for deeper investigation.
   - Even initially, stop after at most 3 probes. If you begin deep verification, endpoint/directory/ID enumeration, decoding chains, repeating requests, or injection/access-control testing, stop and make it an intent.
   - No probing is needed when the existing situation already suffices.

4. Choose new directions. Restraint means avoiding duplicate intents, not minimizing useful work. With unmet goals, ask which deeper uncovered approach advances them, rather than whether you can finish. Intents are open exploration directions, not a fixed menu. Compare them with open/running/recent_done:
   - Covered by open/running: do not duplicate.
   - recent_done: inspect the intent's state and evidence. done means executed, not necessarily a dead end; do not repeat unchanged. Redispatch only for a materially new mechanism (new facts/assets/parameters/substantially different method), explaining the difference in summary. Rewording or 'try again' is insufficient.
   - exhausted means budget interruption with partial results; blocked often means model/network failure with little exploration. Inspect get_worker_trace/get_worker_output for actual progress and blockers. Continue from previous progress after near-breakthrough exhaustion, retry the same direction after a pure external failure, or change methods if repeatedly blocked at the same point. Decide from traces, not state alone.
   - New uncovered direction: submit it.
   - If active/queued intents cover all directions, stop this round and wait. If only recent_done remains and goals are unmet, the top floor requires a new/continued direction.
   - Depth before coverage: coverage is a minimum/acceptance metric, not exploration's entire objective. Prioritize a valuable route toward RCE/privilege/data impact over shallow tests of every asset merely to balance coverage.
   - Maintain diverse mechanisms. If all intents cluster on one route and a substantially different uncovered entry surface/asset/exploit chain exists, add it instead of synonymous intents. Aim for 2–3 distinct routes, concentrating after evidence shows goal proximity. Never use diversity to violate operational constraints or add prohibited ports/hosts/actions.

   Serial exploit chains must be dispatched stepwise. When step 2 depends on actual output of step 1, do not dispatch them in parallel. Record the whole chain in TodoWrite, one item per step; dispatch only a step with satisfied prerequisites, then dispatch the next after a resulting fact appears and mark satisfied steps completed. Do not split one action ('confirm trigger'/'trigger it') into duplicate intents. Parallel intents are for independent dimensions only.

5. Submit the selected directions in one add_intent batch (intents array), at most 4 highest-value entries, not repeated individual calls. If the highest-priority Intent review enabled constraint is present, use one add_review_intent batch instead; do not submit directly. It additionally supports target_executor and replaces. Revise original rejected_intents using update_intent (automatic resubmission) and cancel valueless drafts with cancel_intent.
   - summary: one natural-language sentence with full target address, what to do, and why; use it for substantive deduplication rather than fixed categories.
   - asset_ids: target asset IDs from list_assets (zero/one/more). Always include them for specific sites/endpoints/parameters/hosts and include all affected assets; omit only for global reconnaissance without concrete assets.
   - parent_ids: upstream supporting nodes (optional, zero/one/more). Include all facts jointly motivating a direction and relevant upstream intent/finding IDs; omit for new top-level directions.

Do not duplicate or invent filler, but dispatch uncovered deeper directions while goals remain unmet. Be concise, focused, and efficient. Use English for all generated text.`

// 【二开·P-TPL·A4 planner 提示词四级链】
// 功能介绍：plannerSystem 四级优先级链（m00521 拍板）——
//
//	① 任务级覆盖（tasks.planner_prompt_override 快照：手改 planner_prompt / 模板正文快照）
//	② 全局 DB（agent_prompts 覆盖链，PromptOverride 现状）
//	③ planner_templates 默认模板（只兜直写库建任务的边角路径——正常路径 S13 已快照进①）
//	④ 内置默认（plannerDefaultTmpl）
//	渲染失败逐级回退；代码占有的尾部规约（artifactSpec）渲染后追加，任何一级不可覆盖。
func plannerSystem(goal, dataDir, workDir, taskOverride string) string {
	vars := PlannerVars{Goal: goal, DataDir: dataDir, Now: nowStr()}
	// ① 任务级覆盖快照
	if strings.TrimSpace(taskOverride) != "" {
		if out, err := renderTmpl(taskOverride, vars); err == nil {
			// 【二开·P-LOG·A4】命中①任务级覆盖（DEBUG 级）
			xlog.Debugf("[planner] plannerSystem using task-level override: bytes=%d", len(taskOverride))
			return out + plannerExecutionModeSpec(goal) + artifactSpec(workDir)
		} else {
			// 【二开·P-LOG·A4】渲染失败回退（WARN 级）
			log.Printf("[planner] plannerSystem render failed ⚠ falling back to next level: level=1 err=%v", err)
		}
	}
	// ② 全局 DB 覆盖链（agent_prompts，PromptOverride 现状）
	if PromptOverride != nil {
		if t, ok := PromptOverride("planner"); ok && t != "" {
			if out, err := renderTmpl(t, vars); err == nil {
				// 【二开·P-LOG·A4】命中②全局 DB 覆盖（DEBUG 级）
				xlog.Debugf("[planner] plannerSystem using global DB override: bytes=%d", len(t))
				return out + plannerExecutionModeSpec(goal) + artifactSpec(workDir)
			} else {
				log.Printf("[planner] plannerSystem render failed ⚠ falling back to next level: level=2 err=%v", err)
			}
		}
	}
	// ③ planner_templates 默认模板（is_default 行；只兜直写库建任务的边角路径）
	if PlannerDefaultTemplateBodyFn != nil {
		if body := PlannerDefaultTemplateBodyFn(); body != "" {
			if out, err := renderTmpl(body, vars); err == nil {
				// 【二开·P-LOG·A4】命中③模板库默认模板（DEBUG 级）
				xlog.Debugf("[planner] plannerSystem using template-library default: bytes=%d", len(body))
				return out + plannerExecutionModeSpec(goal) + artifactSpec(workDir)
			} else {
				log.Printf("[planner] plannerSystem render failed ⚠ falling back to next level: level=3 err=%v", err)
			}
		}
	}
	// ④ 内置默认（渲染失败=模板自身坏，返回未渲染原文兜底）
	// 【二开·P-LOG·A4】命中④内置默认模板（DEBUG 级）
	xlog.Debugf("[planner] plannerSystem using built-in default template")
	if out, err := renderTmpl(plannerDefaultTmpl, vars); err == nil {
		return out + plannerExecutionModeSpec(goal) + artifactSpec(workDir)
	}
	return plannerDefaultTmpl + plannerExecutionModeSpec(goal) + artifactSpec(workDir)
}

// Plan runs one planning round. emit, if non-nil, receives the planner's execution
// steps (so users can see how it reads the situation and judges goals — the
// planner is the intent generator and was previously a black box). Returns whether
// the planner judged the goal met.
// triggers carries the concrete change(s) that fired this round — worker(s) done
// and/or finding(s) reported (may be several — the engine debounces a burst; empty
// for time/heartbeat wakes). They are spelled out at the top of the prompt so the
// planner looks first at the actual change (which intent, its output/finding).
func (p *Planner) Plan(ctx context.Context, taskID int64, as *db.AssetStore, ts *db.ExplorationStore, goal string, triggers []TriggerEvent, emit func(db.Activity)) (met bool, reason string, err error) {
	// cold-digest §2.3/§7: advance this task's planner-round counter, maintain the
	// cold_since_round stamps, and (if a threshold is hit) kick off background
	// compaction. Synchronous part is cheap (a few queries); the LLM compaction
	// runs in a detached goroutine so it never adds latency to this round.
	p.compactor.OnPlannerRound(ctx, ts)
	tsx := NewToolSet(ts, "planner")
	tsx.SetFindingRecorder(p.findingRecorder)
	if as != nil {
		tsx.SetAssetStore(as, as.Companies())
	}
	tsx.SetTaskID(taskID)
	tsx.SetCoverageEnabled(as == nil || as.CoverageEnabled(taskID))
	tsx.killWork = p.killWork   // enable kill_work tool (nil = unavailable)
	tsx.steerWork = p.steerWork // enable steer_work tool (nil = unavailable)
	// 【二开·审核流 PR1】审核流三回调注入 planner ToolSet（每轮现接，跟随任务实时配置）。
	tsx.SetNotifyReview(p.notifyReview)
	if p.reviewEnabledFn != nil {
		tsx.SetReviewEnabled(func() bool { return p.reviewEnabledFn(taskID) })
	}
	tsx.SetExecutorChecker(p.checkExecutor)
	if origin, _ := ts.OriginFactID(); origin > 0 {
		tsx.SetOwnerNode(origin) // planner-side anchors default to the task root (origin fact)
	}
	// 领域工具 + 基础默认工具集（Read/Write/Edit/MultiEdit/LS/Glob/Grep/Bash）
	// 资产覆盖度功能关闭时剔除 add_task_scope/list_untested_assets（不入 prompt）。
	base := append(tsx.DropCoverageTools(tsx.PlannerTools()), actool.DefaultTools()...)
	ctx = WithRunInfo(ctx, RunInfo{TaskID: taskID, ExplorationID: explorationID(ts)})
	tools, def, cleanup := AugmentTools(ctx, "planner", base)
	defer cleanup()
	// 关键态势（刚完成的意图 + 预取的完整图）改放【本轮 user 输入】(见下方 input)，system
	// 只留静态规划正文。move-out 让 system 每轮稳定、更利于缓存；代价是若单轮变长，态势可能
	// 被 compaction 压缩（planner 单轮通常短，风险低）。situational 会拼进下方 input。
	situational := renderTriggers(ts, triggers) + renderGraphOverview(tsx.graphOverviewData())
	// 任务级 deadline / 终局模式(经 ctx 注入,见 taskclock.go)。终局那一轮把任务超时
	// planner 收尾词作为【本轮操作指令】拼进本轮 user 输入(随 situational),让它只做最后
	// 目标判定、不产新意图。
	tc := taskClockFrom(ctx)
	if tc.Final {
		situational += "\n\n[Task finalization: special instructions for this round, overriding the normal planning workflow above]:" + resolveTaskTimeoutWrapup("planner")
	}
	// 本任务的工作目录 <workDir>/tasks/<taskID>，先建好。
	taskDir := ensureRunDir(p.workDir, taskID, 0)
	ctx = intercept.WithReviewContext(ctx, taskDir, intercept.ReviewBackground{})
	sysBody := plannerSystem(goal, p.workDir, taskDir, p.taskPromptOverride()) // 【二开·P-TPL·A4】第①级任务级快照
	// 【二开·审核流 PR1/审核-2 P0】开审核的任务必须显式送审：该指令对全部四级模板一律追加
	// （含用户自定义模板），保证 add_intent 直投不会在审核任务里绕过待审队列。
	if p.reviewEnabledFn != nil && p.reviewEnabledFn(taskID) {
		sysBody += "\n\n[Intent review enabled: highest-priority constraint] This task uses human review. Submit new intents with add_review_intent (same parameters as add_intent, plus optional target_executor and replaces); do not submit directly as open with add_intent. Revise rejected_intents from the overview using update_intent on the original draft to resubmit automatically. Cancel valueless intents with cancel_intent (reason required)."
	}
	if p.wantConstraints() {
		sysBody += constraintBlock(ts) // 操作约束(若有)注入系统提示,框定探索边界
	}
	system, boundary := deferredSystem(sysBody, def)
	// planner 无自身墙钟预算;有 deadline 时把 MaxDuration 夹逼到剩余,让在跑的规划轮在
	// 任务到点时进收尾(因超时→任务超时词,因步数→per-run 词)。
	maxDur, clamped := clampMaxDuration(tc.DeadlineUnix, 0)
	settle := wrapupSettlement("planner", nil)
	if tc.DeadlineUnix > 0 {
		settle = wrapupSettlementForTask("planner", nil, clamped)
	}
	opts := agentcore.Options{
		Provider:        p.prov,
		SystemPrompt:    system,
		DynamicBoundary: boundary,
		Tools:           tools,
		DeferredTools:   def.Deferred,
		UnlockSet:       def.Unlock,
		PermissionMode:  permission.ModeBypass,
		EnableWebFetch:  true, // 走记录代理留痕；载入代理 CA 验证 MITM 重签的 HTTPS 证书
		WebFetchProxy:   p.proxyAddr,
		WebFetchCACert:  p.proxyCACert,
		// 联网搜索(可选)。ddgs 无需 key；brave-free 需 BraveKey；tavily 需 TavilyKey。
		// WebSearchProxy 是独立出口代理(http/https/socks5)，与记录流量的 MITM 代理无关；空则直连。
		EnableWebSearch:       p.webSearch.Enabled,
		WebSearchBackend:      p.webSearch.Backend,
		BraveSearchAPIKey:     p.webSearch.BraveKey,
		TavilySearchAPIKey:    p.webSearch.TavilyKey,
		DeepSeekSearchBaseURL: p.webSearch.DeepSeekBaseURL,
		DeepSeekSearchAPIKey:  p.webSearch.DeepSeekAPIKey,
		DeepSeekSearchModel:   p.webSearch.DeepSeekModel,
		WebSearchProxy:        p.webSearch.Proxy,
		BashEnv:               proxyEnv(p.proxyAddr, p.proxyCACert), // Bash 子命令默认走代理+信任 CA
		WorkingDir:            taskDir,                              // 本任务工作目录 <workDir>/tasks/<taskID>
		Root:                  p.workDir,                            // 文件工具硬边界（同 worker）
		ToolOutputDir:         cmdOutDir(taskDir),
		MaxTurns:              p.maxTurns, // 0 = unlimited (configurable in agent management)
		MaxDuration:           maxDur,     // 0=不限;有 deadline 时=距 deadline 剩余
		Compaction:            compactionConfig(p.compactionWindow()),
		// 跨唤醒共享的规划待办：让串行链在多轮之间保留（session 是新的，store 不是）。
		Todos: p.todoFor(ts.ID()),
		// 命中【本轮】步数预算→ SDK 跑收尾:把本轮已想清楚的结论落地(该派的 add_intent、
		// 能证的 prove_goal、串行链记 TodoWrite),而非停止规划——planner 之后仍会被反复唤醒。
		// clamped(被任务 deadline 夹逼)时改用 PromptByReason(见 wrapupSettlementForTask)。
		Settlement:   settle,
		NonStreaming: p.nonStreaming(), // 该 profile 选非流式时走 Provider.Complete
		MaxTokens:    p.maxTokens(),    // 0 = 不发上限,由服务端默认值决定
	}
	if p.tx != nil { // persist raw LLM conversation; one accumulating file per task's planner
		opts.Transcript = p.tx
		opts.SessionID = fmt.Sprintf("exp%d-planner", ts.ID())
	}
	// 实验功能:开启后由 noa 接管上下文压缩(归档集中在 <workDir>/noa/<SessionID> 下,持久)。
	noaSession := fmt.Sprintf("exp%d-planner", ts.ID())
	enableNoa(&opts, p.noaEnabledFn, p.workDir, noaSession, noaWarn(noaSession))
	// 态势（刚完成的意图 + 完整图）现在拼进本轮 user 输入（见下方 input）。user 里还有
	// 指令 + 跨唤醒待办（todo 是模型自己的规划便签，可再生，放 user 即可）。
	// 开场白按「本轮有无具体变动」分两种：有变动 → 指向下方【实际变动】块；无变动
	// (心跳定时巡检 / hint / 恢复等) → 别谎称"图发生了变化",转而提示顺带复查在跑意图。
	lead := "A specific change occurred (see Actual changes triggering this round below). Plan the next step accordingly:"
	if len(triggers) == 0 {
		lead = "This wakeup is a scheduled inspection (heartbeat) without a specific change signal; the graph may be unchanged. Review running intents: correct stalled/diverging work with steer_work and stop wholly wrong directions with kill_work. Then assess goals and decide whether to add directions:"
		// 心跳/无变动唤醒时,若全图已无任何 open 或 running 意图 → 探索已停摆(没 worker 在跑、
		// 也没排队方向)。明确告知 planner 并强制其本轮补出新方向,别只复查在跑意图后空转一轮。
		if active, err := ts.HasActiveIntent(); err == nil && !active {
			lead = "This is a scheduled heartbeat and there are no open, running, or assigned intents. No Worker or queued direction remains; exploration has stalled. You must produce at least one new nonduplicate intent advancing the goal. Assess goal completion from the situation below first; if incomplete, immediately add directions:"
		}
	}
	input := lead + situational + `

Assess goals using the situation above. Mark genuinely achieved goals with prove_goal. Hard rule: if goals remain incomplete and no open/running/assigned intents exist (frontier_open=0 and running_intents/assigned_intents are empty), produce at least one intent advancing the goal. There is no active work or queued direction to wait for; zero new intents would stall the task. Zero new intents is allowed only while open/running/assigned intents are advancing or the goal is achieved.` +
		renderPlannerTodos(opts.Todos.List())
	// MaxDuration 现在会在墙钟到点打断在跑工具并就地进收尾(在活 ctx 上),单轮卡死不再
	// 绕过收尾,无需外部硬 ctx 兜底。ctx 只承载 pause / kill / shutdown。
	_, _, err = captureRun(ctx, opts, input,
		func(r db.Activity) {
			if emit != nil {
				r.Worker = "planner" // planner activity has no intent_id (it generates them)
				emit(r)
			}
		})
	return tsx.GoalMet, tsx.Reason, err
}
