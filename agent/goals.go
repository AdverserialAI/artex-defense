// goals.go：任务目标拆解——让 LLM 把任务目标拆成可独立验证的目标节点。
// 主要函数/类型：
//   - GoalSpec：拆解出的单条目标（text + 可选漏洞类别）。
//   - DecomposeGoals / DecomposeGoalsWithProvider：一次性 LLM 调用完成拆解，set_goals 直写探索图后回读。
//   - goalsDefaultTmpl / goalsScopeTail：拆解器系统提示模板与资产范围登记尾段。

package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"artex/db"
	"norma/agentcore"
	"norma/llm"
	acperm "norma/permission"
	actool "norma/tool"
	"norma/transcript"
)

// goalsDefaultTmpl is the built-in EDITABLE body (段 [A]) of the goals-decomposer
// prompt, seeded into agent_prompts. No template vars are used today.
const goalsDefaultTmpl = `You are a penetration testing goal decomposer. Identify the final results to achieve from user input, rather than planning attack steps.

**First, extract operational constraints before splitting goals.**
Identify the operator's explicit rules about allowed/prohibited actions in the task goal/description. Register each with set_constraints, if any:
- type=deny: prohibited actions, such as no port scanning, no production writes/deletion, no brute force, or excluding a subdomain.
- type=allow: an explicitly permitted/restricted scope, such as passive reconnaissance only or a specified domain only.
- Constraints define action boundaries; they are neither goals nor attack steps.
- Constraints must be self-contained and name concrete targets. Replace references such as current target/current port/current IP/current domain/this site with the actual values in the task. Constraints are injected separately during execution and must remain understandable without context. For https://abc.example.net, write 'Only test abc.example.net', not 'Only test the current target'; write 'Only test target port 443; do not scan other ports', not 'Only test the current port'. Resolve references when the target address is known.
- Register only constraints explicitly stated/emphasized in the goal/description; never invent them. If the type is uncertain, use deny conservatively.
- If there are no operational constraints, do not call set_constraints.
After registering any constraints, split the goals.

**A goal is a final deliverable or verifiable result.**
Do not make reconnaissance, endpoint scanning, analysis/verification processes, attack/exploitation methods, or result-verification steps into subgoals.

**Decomposition principles**
- One final user goal means one goal.
- Split multiple independent final deliverables into separate goals.
- Supply vulnclass when a specific vulnerability class applies; omit for information-gathering/business-logic goals.
- Never invent goals the user did not request.

**Match goal types to Shell intent semantically.**
Ignore differences in case, spaces, hyphens, underscores, and language. Use full meaning rather than mechanical keyword matching; structured goal types take precedence over isolated words.
- Strong Shell/foothold intent: getshell, get shell, shell, interactive/reverse/bind shell, webshell/web shell, C2/command and control, beacon, meterpreter, session, foothold, obtaining control of a host/server, remote/interactive control, uploading and connecting a WebShell, bringing a C2/session online, establishing a foothold, or continuing penetration after entering an internal network. When context explicitly requests these results, set vulnclass=getshell and specify acceptance evidence: a connected WebShell successfully executing a verification command, or an actual reverse/C2 session online with interactive evidence. RCE output, file upload, deserialization, or payload deployment alone is insufficient. WebShell, reverse Shell, and C2 are alternative implementation paths, not separate mandatory goals that must all succeed.
- Weak privilege intent: obtaining privileges, privilege escalation, root/Administrator/SYSTEM/highest privilege, privileged access. These words alone do not imply getshell. Classify as getshell only when context also requires a Shell, interactive access, session, remote control, or foothold; otherwise retain privilege-escalation or vulnerability-verification goals.
- Detection/verification intent: detect, probe, scan, investigate, verify, confirm, reproduce, vulnerability validation, PoC/proof of concept, whether a vulnerability exists/is exploitable/can execute commands, verify RCE/upload/deserialization. Classify by the actual vulnerability (rce, file_upload, deserialization, sqli, etc.) without adding getshell or requiring a Shell. 'Verify RCE' means rce; 'Verify RCE and establish a reverse Shell' includes getshell.

Submit the results with set_goals. Write goals and constraints in English.`

// goalsScopeTail is the code-owned tail appended after the editable goals body
// WHEN an asset store + task context are available. It teaches the decomposer to
// also lift the explicit asset scope out of the goal/description and register it
// via add_task_scope. Kept in code (not the DB-editable body) so it always applies
// on released DBs and can't be edited away — same pattern as the trafficTool tail.
const goalsScopeTail = `

**Additional responsibility: register test asset scope.**
Extract explicitly stated test asset scope from the task goal/description and register it with add_task_scope. This defines the task authorization boundary and coverage denominator. Apply minimum scope: register only the target explicitly named; never broaden it yourself.
- For a URL or hostname-bearing address (https://xxx.example.com/path, app.example.com), use its complete hostname: kind=subdomain, value=complete hostname. For https://a1b2c3.lab.example.net/path, register a1b2c3.lab.example.net, not example.net. Never shorten a subdomain to its root domain; doing so expands authorization beyond the user's target.
- Use kind=root_domain only when the user supplies a bare root domain without subdomains (example.com), or explicitly authorizes the whole site/all subdomains/entire domain.
- For a plain IP/network, use kind=ip/cidr with that IP/CIDR.
- Do not register company scope: a new task usually has no corresponding company in the asset system. Leave company scope to later planning.
- Register only explicitly stated scope; never invent or infer unmentioned domains/IPs.
- Give a brief reason identifying the source statement for auditability.
- If no explicit asset scope exists, do not call add_task_scope.
Register scope first, when present, then submit goals with set_goals.`

// GoalSpec is one decomposed objective.
type GoalSpec struct {
	Text      string `json:"text"`
	VulnClass string `json:"vulnclass,omitempty"`
}

// DecomposeGoals asks the LLM to break a pentest task goal into discrete,
// independently-verifiable objectives (each becomes a goal node). Returns nil if
// no provider is configured or the call yields nothing — the caller then falls
// back to a rule-based split so goal nodes always exist.
//
// prov is supplied by the caller (rather than built here from a Config) so goal
// decomposition rides the SAME provider instance as the rest of the engine — it
// shares the rate limiter, gets recorded by llmrec, and participates in LLM
// failover instead of quietly bypassing all three.
//
// desc is the task's free-text description (背景：靶标范围/flag 数量/交战说明等).
// It is fed alongside the goal so the decomposer no longer splits blind — the
// prompt still forbids inventing anything the two texts don't state.
//
// emit, when non-nil, receives every LLM step (thinking/tool_use/result) with
// Worker="planner" so the round-0 goal-decomposition activity is visible in the UI.
//
// as + taskID, when non-nil/positive, wire the add_task_scope tool so the
// decomposer can register the explicit asset scope it extracts from the goal.
//
// ts is the task's exploration store: set_goals writes the decomposed goal nodes
// straight into it (the same managed tool the main agent uses to add goals at
// runtime). The returned specs are read back from the store so callers can emit
// per-goal activity and detect the "LLM produced nothing" case for their fallback.
func DecomposeGoals(ctx context.Context, prov llm.Provider, dataDir, goalText, desc string, as *db.AssetStore, ts *db.ExplorationStore, taskID int64, emit func(db.Activity)) []GoalSpec {
	if prov == nil {
		return nil
	}
	return DecomposeGoalsWithProvider(ctx, prov, dataDir, goalText, desc, as, ts, taskID, false, 0, emit)
}

// DecomposeGoalsWithProvider is the task-runtime variant used when a task has an
// ordered provider chain. It preserves the same tools and write behavior while
// letting the caller own provider selection/failover. maxTokens is the profile's
// per-reply output cap (0 = send none).
func DecomposeGoalsWithProvider(ctx context.Context, prov llm.Provider, dataDir, goalText, desc string, as *db.AssetStore, ts *db.ExplorationStore, taskID int64, nonStreaming bool, maxTokens int, emit func(db.Activity)) []GoalSpec {
	if prov == nil {
		return nil
	}
	// 目标拆解是一次性调用：不挂 transcript store，所以 agentcore 不会往 ctx 上挂
	// session id（它只在有 writer 时才挂，见 agentcore.Prompt）。而按 session-id 头
	// 做提示缓存/粘性路由的网关（opencode zen 缺 x-opencode-session 直接 400
	// MissingSessionID）读的就是 ctx 上这个值——不补就是「对话正常、拆解 400」。
	// 显式挂一个稳定 id：同一探索的拆解请求共享它（利于命中缓存），且命名与
	// planner/worker 不冲突，能被 llmrec.parseSession 正确归因。
	if ts != nil {
		ctx = transcript.WithSessionID(ctx, fmt.Sprintf("exp%d-goals", ts.ID()))
	}
	// worker="goals" tags the goal nodes' provenance; ts/taskID let set_goals link
	// each goal under the task root. This is the catalog's real set_goals tool, so a
	// web-edited description/schema on it applies here too.
	tsx := &ToolSet{as: as, ts: ts, taskID: taskID, worker: "goals"}
	// Description rides in the user message (same channel as the goal), NOT via the
	// {{.EngagementDescription}} template var — else a prompt that references the var
	// would inject the description twice. System prompt stays pure static instructions.
	sys := renderSystem("goals", goalsDefaultTmpl, GoalsVars{DataDir: dataDir, Now: nowStr()})
	sys += goalExecutionModeSpec(goalText, desc)
	// set_constraints 始终可用(不依赖 asset store):正文已含「先抽操作约束再拆目标」这步
	// (可在 agent 编辑页改措辞),这里只需接上工具。
	tools := []actool.CoreTool{tsx.setGoals(), tsx.setConstraints()}
	// Wire add_task_scope only when we have a real asset store + task to write to.
	// The scope-extraction tail is appended in lockstep so the prompt never asks for
	// a tool that isn't present.
	if as != nil && taskID > 0 {
		tools = append(tools, tsx.addTaskScope())
		sys += goalsScopeTail
	}
	userMsg := "Task goal:\n" + goalText
	if d := strings.TrimSpace(desc); d != "" {
		userMsg += "\n\nTask description (background that may include scope, flag count, or engagement instructions; use only what is stated and do not invent missing details):\n" + d
	}
	// Use captureRun so every LLM step is emitted as an activity record (visible in
	// the plan tab under the round-0 marker). Falls back gracefully when emit is nil.
	captureEmit := func(r db.Activity) {
		if emit != nil {
			r.Worker = "planner"
			emit(r)
		}
	}
	captureRun(ctx, agentcore.Options{
		Provider:               prov,
		SystemPrompt:           []string{sys},
		Tools:                  tools,
		PermissionMode:         acperm.ModeBypass,
		DisableBackgroundTasks: true,
		// 3 步(抽约束 → 登记范围 → 拆目标)各需一次工具调用,给足回合避免收尾前漏调 set_goals。
		MaxTurns:     8,
		NonStreaming: nonStreaming, // 该 profile 选非流式时走 Provider.Complete
		MaxTokens:    maxTokens,    // 0 = 不发上限,由服务端默认值决定
	}, userMsg, captureEmit)
	// set_goals persisted the goals directly; read them back so the caller sees what
	// was written (empty slice ⇒ the LLM produced nothing ⇒ caller falls back).
	if ts == nil {
		return nil
	}
	nodes, _ := ts.ListByKind(db.KindGoal, 10000)
	var out []GoalSpec
	for _, n := range nodes {
		var p struct {
			Text      string `json:"text"`
			VulnClass string `json:"vulnclass"`
		}
		_ = json.Unmarshal(n.Payload, &p)
		if strings.TrimSpace(p.Text) != "" {
			out = append(out, GoalSpec{Text: p.Text, VulnClass: p.VulnClass})
		}
	}
	return out
}
