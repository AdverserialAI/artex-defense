// worker.go：Worker 执行 agent——围绕 agentcore 执行单条意图，组装提示词、工具、代理/联网搜索与结算配置。
// 主要函数/类型：
//   - Worker：执行者 agent，聚合 provider、上下文窗口、轮数/超时预算与各种每轮开关。
//   - NewWorker：按 provider/model/工作目录等构造 Worker。
//   - WorkerSessionID：意图级稳定会话标识（transcript 键）。
//   - WebSearchOpts / SetWebSearch：联网搜索工具后端配置。
//   - SetRunTimeout / SetMaxTokens / SetNonStreaming 等 Set 系列：每轮运行策略开关。
//   - ensureRunDir / workerArtifactSpec：运行目录与产物输出规约。

package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"artex/db"
	"artex/intercept"
	"artex/xlog"
	"norma/agentcore"
	"norma/harness"
	"norma/llm"
	actool "norma/tool"
	"norma/transcript"
)

// WebSearchOpts is the web-search backend selection the server pushes into each
// agent (planner/worker/main). Enabled=false leaves the web_search tool off.
// Backend is "ddgs" (no key), "brave-free" (BraveKey required), "tavily"
// (TavilyKey required), or "deepseek" (DeepSeek* required, filled from the
// active LLM profile). It maps directly onto agentcore.Options.
// Proxy is a dedicated egress proxy for the search request (http/https/socks5),
// independent of the traffic-recording MITM proxy — set it when the search endpoint
// is only reachable via a VPN/SOCKS proxy. Empty = direct.
//
// 注意 deepseek 后端与其它三个的性质不同：DeepSeek 没有可直接调用的搜索接口，
// 搜索只存在于其 Anthropic 兼容 messages 接口内部(web_search_20250305 server
// tool)，因此每次搜索会消耗一次模型调用，且搜索请求由 DeepSeek 服务端发出——
// 不经过本机 Proxy，也不会进流量留痕。
type WebSearchOpts struct {
	Enabled   bool
	Backend   string
	BraveKey  string
	TavilyKey string
	Proxy     string
	// DeepSeek* 来自当前激活的 LLM 配置(仅 anthropic 格式的 DeepSeek 官方端点)，
	// 不单独配置，随 LLM 配置切换而变。
	DeepSeekBaseURL string
	DeepSeekAPIKey  string
	DeepSeekModel   string
}

// Worker is an LLM work agent (docs §4.4): it claims ONE intent, completes it
// with real tools (Bash: kali tooling through the recording proxy), writes the
// FACTS it found back into the graph, and stops. It does NOT generate new
// directions (that is the planner's job) and does NOT keep exploring toward the
// goal on its own. Multiple workers run concurrently as goroutines.
type Worker struct {
	findingRecorder FindingRecorder
	prov            llm.Provider
	model           string
	workDir         string
	proxyAddr       string
	proxyCACert     string            // recording proxy's CA cert path (for WebFetch HTTPS verify)
	webSearch       WebSearchOpts     // web_search tool backend selection (off by default)
	tx              *transcript.Store // raw LLM conversation persistence (nil = off)
	window          int               // context window in tokens (for compaction)
	windowFn        func() int        // optional dynamic task-chain minimum
	maxTurns        int               // max agent turns per run (0 = unlimited)
	// runTimeout is the wall-clock budget for the main exploration of one intent
	// (0 = unlimited). When it fires, the run is cut and a settlement round is
	// forced so already-identified facts get written back instead of being lost.
	runTimeout time.Duration
	// extraTools are host-provided tools (e.g. traffic query, oast) appended to
	// the worker's graph write-back tools.
	extraTools []actool.CoreTool
	// injectConstraints resolves whether this task's operation constraints get
	// injected into the worker system prompt. Read per run so the settings toggle
	// takes effect without rebuilding the agent. nil = inject (default).
	injectConstraints func() bool
	// taskPromptFn 返回本任务的 Worker 提示词快照；空值走全局/默认链。
	taskPromptFn func() string
	// nonStreamingFn resolves whether this run uses the non-streaming (Complete)
	// path. Read per run so a profile/task toggle takes effect without rebuilding
	// the agent. nil = streaming (default).
	nonStreamingFn func() bool
	// noaEnabledFn resolves whether this run uses the experimental noa context-
	// compression mechanism. Read per run, like nonStreaming. nil = off (built-in
	// compaction).
	noaEnabledFn func() bool
	// maxTokensFn resolves the per-reply output cap in tokens, on the same
	// per-run basis. nil or 0 = send no cap and let the endpoint decide.
	maxTokensFn func() int
}

// WorkerSessionID returns the stable transcript key used by a worker intent.
// Worker slots are reusable, so the intent id (rather than work#N) is the
// session identity. Keep this helper public so the Worker message API and UI
// can refer to exactly the conversation that will be resumed.
func WorkerSessionID(explorationID, intentID int64) string {
	return fmt.Sprintf("exp%d-worker-i%d", explorationID, intentID)
}

const workerChatMarkerPrefix = "<!-- ARTEX_WORKER_CHAT:"

func workerChatMarker(requestID string) string {
	return workerChatMarkerPrefix + requestID + " -->"
}

func hasWorkerChatMessage(messages []llm.Message, requestID string) bool {
	marker := workerChatMarker(requestID)
	for _, message := range messages {
		if message.Role == llm.RoleUser && strings.Contains(message.Text(), marker) {
			return true
		}
	}
	return false
}

// SetNonStreaming wires a resolver deciding whether runs use the non-streaming
// model path (true = non-streaming). nil/unset = streaming (default). Read per
// run so a profile or task-chain toggle takes effect without rebuilding.
func (w *Worker) SetNonStreaming(fn func() bool) { w.nonStreamingFn = fn }

func (w *Worker) nonStreaming() bool { return w.nonStreamingFn != nil && w.nonStreamingFn() }

// SetNoaEnabled wires a resolver deciding whether runs use the experimental noa
// context-compression mechanism. nil/unset = off (built-in compaction). Read per
// run so the settings toggle takes effect without rebuilding the agent.
func (w *Worker) SetNoaEnabled(fn func() bool) { w.noaEnabledFn = fn }

// SetMaxTokens wires a resolver for the per-reply output cap. nil/unset or 0 =
// send no cap and let the endpoint decide. Read per run, like nonStreaming.
func (w *Worker) SetMaxTokens(fn func() int) { w.maxTokensFn = fn }

func (w *Worker) maxTokens() int {
	if w.maxTokensFn == nil {
		return 0
	}
	return w.maxTokensFn()
}

// SetConstraintInject wires a resolver deciding whether this task's operation
// constraints get injected into the worker system prompt. nil = inject (default).
func (w *Worker) SetConstraintInject(fn func() bool) { w.injectConstraints = fn }

// SetTaskPromptOverride 注入任务级 Worker 提示词快照。
func (w *Worker) SetTaskPromptOverride(fn func() string) { w.taskPromptFn = fn }

func (w *Worker) taskPromptOverride() string {
	if w.taskPromptFn == nil {
		return ""
	}
	return w.taskPromptFn()
}

// wantConstraints reports whether constraint injection is enabled (default yes).
func (w *Worker) wantConstraints() bool { return w.injectConstraints == nil || w.injectConstraints() }

// SetRunTimeout configures the per-intent wall-clock budget for the main
// exploration (0 = unlimited). When it fires, the SDK settlement phase still runs
// so facts are never lost to a timeout. Safe to call before Execute.
func (w *Worker) SetRunTimeout(run time.Duration) {
	w.runTimeout = run
}

// settleWrapUpPrompt is injected by the SDK settlement phase when a worker hits its
// turn/time budget: stop probing, write back what was found, then end with a
// plain-text one-liner (which becomes this run's displayed result).
const settleWrapUpPrompt = "Your budget is about to expire. Do not run more commands or probes. (1) Save everything already identified but not yet recorded: insert_assets for assets, record_fact for conclusions/facts, report_finding for confirmed findings. (2) Finish with one separate plain-text sentence summarizing what you did and the key conclusions; this sentence is displayed as the run result and is required."

func NewWorker(prov llm.Provider, model, workDir string, tx *transcript.Store, window, maxTurns int, extra ...actool.CoreTool) *Worker {
	return &Worker{prov: prov, model: model, workDir: workDir, tx: tx, window: window, maxTurns: maxTurns, extraTools: extra}
}

// defaultToolsExcept returns actool.DefaultTools() minus the named tools (by
// CoreTool.Name()). Used to trim SDK default tools an agent shouldn't have.
func defaultToolsExcept(exclude ...string) []actool.CoreTool {
	drop := make(map[string]bool, len(exclude))
	for _, n := range exclude {
		drop[n] = true
	}
	all := actool.DefaultTools()
	out := make([]actool.CoreTool, 0, len(all))
	for _, t := range all {
		if !drop[t.Name()] {
			out = append(out, t)
		}
	}
	return out
}

func (w *Worker) SetCompactionWindowResolver(fn func() int) { w.windowFn = fn }

func (w *Worker) compactionWindow() int {
	if w.windowFn != nil {
		return w.windowFn()
	}
	return w.window
}

// SetProxy configures the recording proxy address that workers route target
// traffic through, plus the CA cert path WebFetch trusts to verify HTTPS through
// that MITM proxy. Empty addr disables the hint.
func (w *Worker) SetProxy(addr, caCert string) { w.proxyAddr, w.proxyCACert = addr, caCert }

// SetWebSearch selects the web_search backend for this worker (off by default).
func (w *Worker) SetWebSearch(o WebSearchOpts) { w.webSearch = o }

// proxyEnv builds the Bash-subprocess env that routes child-command HTTP through
// the egress proxy (the recording MITM when capture is on, or the global proxy
// directly when it is off) and, only when a MITM CA is present, makes the common
// toolchain trust it — so tools need no manual -x/--proxy/-k. Each ecosystem reads
// a different CA var (verified empirically): SSL_CERT_FILE→curl/urllib/Go/openssl,
// REQUESTS_CA_BUNDLE→python requests (it ignores SSL_CERT_FILE), CURL_CA_BUNDLE→curl,
// GIT_SSL_CAINFO→git, NODE_EXTRA_CA_CERTS→node; NODE_USE_ENV_PROXY makes Node 24+
// honor the proxy vars. ALL_PROXY is set too so a socks5 egress proxy (which curl
// only reads from ALL_PROXY, not HTTP(S)_PROXY) works in the capture-off path.
// Empty proxyAddr → nil (direct, unchanged env).
func proxyEnv(proxyAddr, caCert string) []string {
	if proxyAddr == "" {
		return nil
	}
	env := []string{
		"HTTP_PROXY=" + proxyAddr, "HTTPS_PROXY=" + proxyAddr,
		"http_proxy=" + proxyAddr, "https_proxy=" + proxyAddr,
		"ALL_PROXY=" + proxyAddr, "all_proxy=" + proxyAddr, // socks5 egress: curl reads only this
		"NODE_USE_ENV_PROXY=1", // Node 24+: honor HTTP(S)_PROXY in built-in fetch/http
	}
	if caCert != "" {
		env = append(env,
			"SSL_CERT_FILE="+caCert,
			"CURL_CA_BUNDLE="+caCert,
			"REQUESTS_CA_BUNDLE="+caCert,
			"GIT_SSL_CAINFO="+caCert,
			"NODE_EXTRA_CA_CERTS="+caCert,
		)
	}
	return env
}

// WorkerDeliveryFactSpec is the record_fact contract consumed by trace_cleaner.
// There is deliberately no separate record_delivery tool: deliveries are facts.
const WorkerDeliveryFactSpec = "- **Deployment registration → record_fact (exploration graph, pass intent_id; mandatory)**: Immediately register every actual upload/write/deployment of WebShells, C2 agents, reverse scripts, proxy/tunnel tools, and other files needing later cleanup. Begin summary with " + "`[Deployment]`" + " and detail the host/container, absolute deployed path, artifact type, upload method, purpose, related finding if any, and cleanup suitability. evidence must include actual upload/write commands and successful output; confidence=observed. Record sha256 when available. **One separate " + "`[Deployment]`" + " fact is required per deployed artifact**, an explicit exception to the usual one-fact-per-intent rule. report_finding does not replace deployment registration. Do not register failed or merely attempted uploads. Cleanup depends on these facts; missing records prevent artifact reconciliation."

// workerDefaultTmpl is the built-in EDITABLE body (段 [A]) of the worker system
// prompt, seeded into agent_prompts. The trafficTool block and the 中间产物输出规约
// are NOT here — they are code-owned and appended by workerSystem after rendering
// (段 [B]/[C]), so editing the DB body can never drop them.
const workerDefaultTmpl = `You are the Worker of an authorized security testing and analysis system. You receive one intent (a one-sentence exploration direction). Your only duty is to complete that intent, save discoveries to the knowledge graph, then stop and return.

**Boundaries**
1. Execute only your claimed intent.
2. An initial obstacle does not mean the direction is exhausted. Try all reasonable methods available under current conditions before concluding: change payload/encoding/path for online targets, entry/call chain/data flow for code, or search perspective for documents.
3. Operate only within authorization. Any Operational constraints at the top of the system prompt take highest priority: check each command/probe before execution and skip violations even if they appear in the assigned intent.

**Webshell discipline**: When deploying a webshell, generate it with mcp__godzilla__shell_create. These are deferred tools: first SearchExtraTools for "godzilla", then ExecuteExtraTool with target language, encryptor, and filtering characteristics. Never handwrite a hard-coded shell. Save the generated shell in this intent's working directory before uploading. After deployment, register with mcp__godzilla__shell_add and test a real connection using shell_test; report webshell only with actual execution output. Other reverse/command-shell evidence is getshell. If MCP is unavailable, record a fact for the Planner; do not fall back to a handwritten shell.

**WebShell filenames**: Match the target stack's extension and normal application naming style, preferably following existing site conventions (index.jsp, home.jsp, main.php). Do not use shell, cmd, backdoor, hacked, test, random characters, timestamps, or Base64-style names. Check for an existing file before upload and choose another normal name if it exists; never overwrite existing files. The deployed filename must match shell_add registration and the absolute path in the [Deployment] fact.

**Record discoveries immediately**: Only knowledge saved to the graph counts; do not accumulate unsaved results until the step budget expires. Keep the three destinations distinct:
- New assets/resources → insert_assets (asset graph): assets themselves, including subdomains/services/endpoints/fingerprints/credentials, or code repositories/modules/key files/entry functions. Exploration conclusions belong in record_fact, not asset properties.
- Exploration conclusions/facts → record_fact (exploration graph, with intent_id). Consolidate multiple observations into one fact: one-sentence summary and expanded detail based on actual execution. Normally one intent produces one fact; combine details rather than exploding the graph into per-property entries. Use separate facts only for genuinely independent conclusions that cannot be combined. Record only new information, not paraphrases of existing facts or redundant confirmation without additions. State only what was actually observed. Provide concise one-line evidence (command/action plus the one or two most probative output lines; details belong in detail), and confidence=observed for direct evidence or inferred for deduction.` + WorkerDeliveryFactSpec + `
- Confirmed finding → report_finding (exploration graph; pass intent_id). Reproducible evidence is required. Online evidence means actual requests/responses or command output triggered during this run. Code evidence means file:line, relevant code excerpts, and entry-to-sink reachability. Version/fingerprint/CVE matches, apparently injectable inputs, and conclusions inferred from documentation/changelogs/code diffs never count as confirmation. If suspicious but unproved, record an inferred fact explaining the lead and why it is unverified; do not force a finding.
- Classify vulnclass accurately, because it affects the Planner's explicit downstream dispatch. Do not casually use other:
  getshell = actual host command execution through a reverse/command Shell;
  webshell = uploaded WebShell verified as accessible or connectable for control;
  target_access = obtained target system/application privileges (account control, successful login, privilege escalation), when not more specifically getshell/webshell;
  rce = verified arbitrary target command/code execution, including expression/template/command injection;
  file_upload = arbitrary file upload stored and accessible/parsed;
  deserialization = demonstrated deserialization exploit chain;
  sqli = demonstrated error/boolean/time-based injection;
  ssrf = demonstrated unexpected request made by the target server;
  logic_flaw = demonstrated access-control/payment/CAPTCHA or other business-logic bypass;
  info_leak = actually retrieved sensitive contents, not possible disclosure;
  unauthorized_api = successful unauthenticated API invocation returning sensitive data;
  other = none of these eleven classes fits.
- Foothold/severe classes (getshell/webshell/target_access/rce/file_upload/deserialization) require executed/controlled-level evidence. A merely theoretical exploit path is an inferred fact, not a finding.

[Foothold evidence] Before claiming a foothold Finding, perform the verification appropriate to the actual method: WebShell connects and executes the intended command; reverse/interactive/C2 session is online and interactive; account/application privileges have actual login, escalation, or control evidence. Scanner leads, suspicious responses, upload success alone, and one RCE echo are not footholds. Register RCE/upload under their actual types without upgrading them to footholds. Link verified HTTP proof/verification exchanges in traffic_refs. For TCP/uncaptured/no-match cases, record actual commands/output and the absence of bindings in evidence; do not force packet capture or repeat probes merely to bind traffic. With insufficient evidence, record_fact with confidence=inferred rather than claiming a foothold through report_finding.

After completing this intent, summarize in one sentence what you did and which facts were saved.`

// workerTrafficBlock is 段 [B]: the traffic-tool note, code-injected only when
// traffic capture (recording) is on — i.e. the traffic_* tools actually exist.
// Gated on recording, NOT on the egress proxy: a global proxy with capture off
// routes traffic but records nothing, so the tools would not be there. Not stored,
// not editable.
func workerTrafficBlock(recording bool) string {
	if !recording {
		return ""
	}
	return `

**Traffic tools**:
- traffic_search / traffic_get / traffic_blob: inspect previous responses and visited resources before repeating curl against the same URL. traffic_search requires host and defaults to 3 lightweight entries (id/method/url/status/resp_len; no response content); explicitly increase limit for more. body_contains searches captured request/response bodies (minimum 3 characters). Read individual exchanges with traffic_get(id); oversized bodies appear as @blob sha256:<hash>, which traffic_blob(hash) retrieves in chunks.`
}

// artifactSpec is 段 [C]: the code-owned, non-editable tail appended to every
// pentest agent's prompt — intermediate artifacts must land in the shared work
// dir, never /tmp. Guaranteed present regardless of how the DB body is edited.
func artifactSpec(dir string) string {
	return "\n\n**Intermediate output rules**: Write scripts, payloads, captured responses, temporary data, and all other intermediate files to this task's working directory " + dir + "** (relative paths resolve here, or use this absolute path). **Do not write to /tmp or other absolute paths**."
}

// workerArtifactSpec is the worker's 段 [C]: its per-intent run dir is pre-created
// by the engine (ensureRunDir), so it just writes relative paths there — no manual
// mkdir, no cross-worker name collisions.
func workerArtifactSpec(runDir string) string {
	return "\n\n**Intermediate output rules**: Write scripts, payloads, captured responses, temporary data, and all other intermediate files to this intent's dedicated working directory " + runDir + "** (already created; use relative paths directly without creating it again). **Do not write to /tmp or other absolute paths**."
}

// ensureRunDir builds and creates an agent's working directory under base:
// <base>/tasks/<taskID> for planner/main; <base>/tasks/<taskID>/i<intentID> for a
// worker (intentID<=0 → task dir only). The "tasks/" segment groups per-task dirs
// symmetrically with the chat agent's "sessions/<sessionID>". Best-effort mkdir — on
// failure, writes fail the same way an unwritable CWD would.
func ensureRunDir(base string, taskID, intentID int64) string {
	dir := filepath.Join(base, "tasks", strconv.FormatInt(taskID, 10))
	if intentID > 0 {
		dir = filepath.Join(dir, "i"+strconv.FormatInt(intentID, 10))
	}
	_ = os.MkdirAll(dir, 0o755)
	return dir
}

// cmdOutDir is the SDK large-tool-output spill dir under an agent's run dir.
func cmdOutDir(dir string) string { return filepath.Join(dir, "cmd-output") }

func workerSystem(proxyAddr, caCert, dataDir, runDir, workerName, taskOverride string) string {
	vars := WorkerVars{ProxyAddr: proxyAddr, WorkerName: workerName, DataDir: dataDir, Now: nowStr()}
	tail := func(body string) string {
		return body + workerTrafficBlock(caCert != "") + workerArtifactSpec(runDir)
	}
	// ① 任务创建时保存的 Worker 模板/手写提示词快照。
	if strings.TrimSpace(taskOverride) != "" {
		if out, err := renderTmpl(taskOverride, vars); err == nil {
			xlog.Debugf("[worker] workerSystem using task-level override: bytes=%d", len(taskOverride))
			return tail(out)
		} else {
			log.Printf("[worker] workerSystem task-level render failed; falling back to global configuration: %v", err)
		}
	}
	// ② Agent 管理中保存的全局 Worker 提示词。
	if PromptOverride != nil {
		if tmpl, ok := PromptOverride("worker"); ok && tmpl != "" {
			if out, err := renderTmpl(tmpl, vars); err == nil {
				xlog.Debugf("[worker] workerSystem using global Agent override: bytes=%d", len(tmpl))
				return tail(out)
			} else {
				log.Printf("[worker] workerSystem global render failed; falling back to default template: %v", err)
			}
		}
	}
	// ③ worker_templates 中唯一的默认模板。
	if WorkerDefaultTemplateBodyFn != nil {
		if tmpl := WorkerDefaultTemplateBodyFn(); tmpl != "" {
			if out, err := renderTmpl(tmpl, vars); err == nil {
				xlog.Debugf("[worker] workerSystem using default Worker template: bytes=%d", len(tmpl))
				return tail(out)
			} else {
				log.Printf("[worker] workerSystem default render failed; falling back to built-in template: %v", err)
			}
		}
	}
	// ④ 不依赖数据库的内置 Worker 提示词。
	if out, err := renderTmpl(workerDefaultTmpl, vars); err == nil {
		return tail(out)
	}
	return tail(workerDefaultTmpl)
}

// intentAssetIDs pulls the intent's target asset ids out of its payload
// (planner's add_intent stores them as a numeric asset_ids array). nil on absence
// or malformed payload.
func intentAssetIDs(intent *db.Node) []int64 {
	if intent == nil {
		return nil
	}
	var p struct {
		AssetIDs []int64 `json:"asset_ids"`
	}
	if err := json.Unmarshal(intent.Payload, &p); err != nil {
		return nil
	}
	return p.AssetIDs
}

// RenderIntentTask formats the claimed intent for the worker's launch USER message:
// the intent is the worker's whole job. It used to live in the system prompt; it now
// rides in the first user turn (together with the situational overview) so the system
// prompt stays static/role-only — same move as the planner's situational block.
//
// 【二开·P-EXEC·A1 意图任务渲染导出】
// 功能介绍：renderIntentTask 改名导出为 RenderIntentTask，供 RunCustomExecutor（A2）复用同一意图任务块文案
func RenderIntentTask(intent *db.Node) string {
	return fmt.Sprintf("\n\n[Your claimed intent: your only task for this run. Execute only this intent, record facts, and stop when complete]:\n%s\nIntent ID: %d (pass this to record_fact / report_finding)", string(intent.Payload), intent.ID)
}

// renderWorkerGraphOverview folds the global situational snapshot into the worker's
// launch USER message for AWARENESS ONLY. The framing is deliberately strong: the overview
// must NOT widen the worker's job — it still does only its assigned intent. Its sole
// purpose is letting the worker read context (existing facts/assets/hints)
// so it avoids redundant work and doesn't re-derive what others already found.
func renderWorkerGraphOverview(data map[string]any) string {
	// coverage 是给规划者判断「哪类测得少 / 要不要扩范围」的信号，与 worker「只做领到的
	// 那条意图、别追未覆盖的点」的职责边界相悖 → 从 worker 视图里剔除。data 是本次 worker
	// 专属的新 map，删键不影响 planner。
	delete(data, "coverage")
	b, err := json.Marshal(data)
	if err != nil {
		return "" // fall back silently: the worker just won't have the global context
	}
	return "\n\n[Global exploration situation: read-only context for your intent]:\n" +
		"The following overview shows what others have discovered so you can avoid duplication and understand your intent's relationship to the overall task.\n" +
		"Think broadly while exploring this intent, but execute only your assigned intent; other intents belong to other workers under planner control. Record valuable related leads (cross-asset connections, other potential exploit-chain entrances, global suspicious patterns) as facts for the planner. These are important output: report useful leads for the planner to assess rather than withholding them.\n" +
		string(b)
}

// Execute runs one intent. hooks (the per-task Guard) gates every tool call; may
// be nil. emit, if non-nil, receives one ActivityRecord per execution step.
// notifyFinding, if non-nil, is called (intentID, summary) when this worker writes
// a finding (report_finding) so the task's planner wakes mid-flight — with context
// on which intent found what — instead of waiting for the worker to finish.
// Returns the terminal reason (so the engine can distinguish completed vs
// max_turns) and a per-kind breakdown of what was written back (so an intent that
// explored but persisted nothing isn't mistaken for done, and the engine can log
// facts/assets/findings separately instead of lumping them under "facts").
func (w *Worker) Execute(ctx context.Context, name string, taskID int64, as *db.AssetStore, ts *db.ExplorationStore, intent *db.Node, hooks harness.HookRunner, emit func(db.Activity), enr EnrichTrigger, notifyFinding func(int64, string)) (harness.TerminalReason, WriteCounts, error) {
	return w.execute(ctx, name, taskID, as, ts, intent, hooks, emit, enr, notifyFinding, "", "")
}

// ExecuteWithMessage runs the next turn in the same intent conversation with a
// human-authored message. The HTTP handler does not edit the transcript;
// agentcore records the message as a normal user turn when this Worker starts.
// This keeps Worker continuation identical to the regular agent chat flow.
func (w *Worker) ExecuteWithMessage(ctx context.Context, name string, taskID int64, as *db.AssetStore, ts *db.ExplorationStore, intent *db.Node, hooks harness.HookRunner, emit func(db.Activity), enr EnrichTrigger, notifyFinding func(int64, string), requestID, message string) (harness.TerminalReason, WriteCounts, error) {
	return w.execute(ctx, name, taskID, as, ts, intent, hooks, emit, enr, notifyFinding, strings.TrimSpace(requestID), strings.TrimSpace(message))
}

func (w *Worker) execute(ctx context.Context, name string, taskID int64, as *db.AssetStore, ts *db.ExplorationStore, intent *db.Node, hooks harness.HookRunner, emit func(db.Activity), enr EnrichTrigger, notifyFinding func(int64, string), requestID, message string) (harness.TerminalReason, WriteCounts, error) {
	tsx := NewToolSet(ts, name)
	tsx.SetFindingRecorder(w.findingRecorder)
	tsx.SetTaskID(taskID)
	coverageEnabled := as == nil || as.CoverageEnabled(taskID)
	tsx.SetCoverageEnabled(coverageEnabled)
	if as != nil {
		tsx.SetAssetStore(as, as.Companies())
	}
	tsx.SetOwnerNode(intent.ID)         // assets this worker discovers anchor to its intent → visible to the task
	tsx.SetEnrich(enr)                  // async DNS/HTTP auto-completion for assets this worker writes
	tsx.SetNotifyFinding(notifyFinding) // report_finding 落库时当场唤醒 planner，带上「哪个意图+finding」
	// base = built-in worker tools ∪ host tools (traffic) ∪ default tools (incl. Bash);
	// then augment with the agent's visible skills/MCP. During the SDK settlement
	// phase, Bash is hidden via Settlement.DisabledTools (no local gating needed).
	base := append(tsx.WorkerTools(), w.extraTools...)
	// worker 刻意不给 MultiEdit/Glob/Grep：文件精改用 Edit、检索走 Bash(grep/find)，
	// 收敛工具面、减少低价值调用。其余 SDK 默认工具(Read/Write/Edit/LS/Bash/Sleep)照常。
	base = append(base, defaultToolsExcept("MultiEdit", "Glob", "Grep")...)
	ctx = WithRunInfo(ctx, RunInfo{TaskID: taskID, ExplorationID: explorationID(ts), IntentID: intent.ID})
	tools, def, cleanup := AugmentTools(ctx, "worker", base)
	defer cleanup()

	// 意图是 worker 的【唯一职责、贯穿整个 run 的不变量】→ 连同启动指令、意图锚定的目标资产
	// 原始数据一起放进 system prompt：system 每次 run 都重新拼一遍、绝不会被 compaction 压掉，
	// 长 run 里意图永远在场，续跑时也不依赖 transcript 历史是否留住那条首消息。代价是 system
	// 混入 per-intent 易变数据、失去跨意图缓存复用；这是刻意的取舍（意图丢失比省 token 严重得多）。
	// 与 planner「态势块放 user turn」分叉是有意的：planner 本身是产意图的那个、没有单一 mandate，
	// worker 有。仅【全局态势 overview】留在启动 user 消息里——它可降级、容忍 stale，压掉无碍。
	// 本次意图的专属工作目录 <workDir>/tasks/<taskID>/i<intentID>，引擎侧先建好。
	runDir := ensureRunDir(w.workDir, taskID, intent.ID)
	// The run-wide intent is not the current tool action. Do not forward it or
	// inherit a parent run's background into the action reviewer.
	ctx = intercept.WithReviewContext(ctx, runDir, intercept.ReviewBackground{})
	overview := renderWorkerGraphOverview(tsx.graphOverviewData())
	sysBody := workerSystem(w.proxyAddr, w.proxyCACert, w.workDir, runDir, name, w.taskPromptOverride())
	if w.wantConstraints() {
		sysBody += constraintBlock(ts) // 操作约束(若有)注入系统提示,worker 执行时严格遵守
	}
	// 意图块 → 意图锚定资产块 → 启动指令，依次追加到 system 尾部（与 constraintBlock 同一套追加法）。
	sysBody += RenderIntentTask(intent) // 【二开·P-EXEC·A1】导出改名
	if as != nil {
		if ids := intentAssetIDs(intent); len(ids) > 0 {
			if assets, err := as.GetByIDs(ids); err == nil && len(assets) > 0 {
				if b, err := json.Marshal(assets); err == nil {
					sysBody += "\n\nTarget assets corresponding to this intent's asset_ids:\n" + string(b)
				}
				// 意图明确针对的这些资产 → 自动纳入任务测试范围（与 insertAssets 同一套
				// 保守粒度）。upsertTaskScope 的 ON CONFLICT DO NOTHING + uq_task_scope
				// 唯一索引保证不会重复添加；重跑/重试同样是幂等 no-op。
				// 资产覆盖度功能关闭时不再累积测试范围(分母)。
				if coverageEnabled {
					for _, a := range assets {
						_ = as.AddAutoScope(taskID, a.Type, a.Domain, a.URL, a.IP)
					}
				}
			}
		}
	}
	sysBody += "\n\nExecute the intent above: do only this intent, produce only facts, assets, and findings, and stop when complete."
	system, boundary := deferredSystem(sysBody, def)
	// 任务级 deadline(经 ctx 注入)夹逼本 run 的墙钟预算 + 决定收尾词(见 taskclock.go)。
	tc := taskClockFrom(ctx)
	maxDur, clamped := clampMaxDuration(tc.DeadlineUnix, w.runTimeout)
	settle := wrapupSettlement("worker", []string{"Bash"})
	if tc.DeadlineUnix > 0 {
		settle = wrapupSettlementForTask("worker", []string{"Bash"}, clamped)
	}
	// 公共装配块（intent_exec_base.go）：权限旁路/WebFetch 代理/Bash 代理环境/工作目录与
	// 文件硬边界/大输出溢出目录/会话待办。差异字段在下方覆写：联网搜索七字段、行/墙钟预算、
	// settlement（任务 deadline 夹逼时用 wrapupSettlementForTask）、Compaction/NonStreaming/MaxTokens。
	opts := baseAgentOptions(runDir, w.workDir, w.proxyAddr, w.proxyCACert)
	opts.Provider = w.prov
	opts.SystemPrompt = system
	opts.DynamicBoundary = boundary
	opts.Tools = tools
	opts.DeferredTools = def.Deferred
	opts.UnlockSet = def.Unlock
	// 联网搜索(可选)。ddgs 无需 key；brave-free 需 BraveKey；tavily 需 TavilyKey。
	// WebSearchProxy 是独立的出口代理(http/https/socks5)，与记录流量的 MITM 代理无关；空则直连。
	opts.EnableWebSearch = w.webSearch.Enabled
	opts.WebSearchBackend = w.webSearch.Backend
	opts.BraveSearchAPIKey = w.webSearch.BraveKey
	opts.TavilySearchAPIKey = w.webSearch.TavilyKey
	opts.DeepSeekSearchBaseURL = w.webSearch.DeepSeekBaseURL
	opts.DeepSeekSearchAPIKey = w.webSearch.DeepSeekAPIKey
	opts.DeepSeekSearchModel = w.webSearch.DeepSeekModel
	opts.WebSearchProxy = w.webSearch.Proxy
	opts.MaxTurns = w.maxTurns // 0 = unlimited (configurable in agent management)
	// 墙钟预算,轮边界判,不打断半路;0 = 不限。有任务级 deadline 时夹逼到 min(自身预算,
	// 距 deadline 剩余),让本 run 在任务到点时自然进收尾(见 taskclock.go)。
	opts.MaxDuration = maxDur
	// 命中预算(轮次 OR 时长)→ SDK 跑一轮收尾(隐藏 Bash),把已识别的写回,避免烂尾。
	// clamped(被任务 deadline 夹逼)时用 PromptByReason:因超时=任务到点→任务超时词,
	// 因步数=夹逼窗口内步数先耗尽→回落 per-run 词。非 clamped 维持纯 per-run。
	opts.Settlement = settle
	opts.Compaction = compactionConfig(w.compactionWindow()) // long tool-heavy runs stay within the window
	opts.NonStreaming = w.nonStreaming()                     // 该 profile 选非流式时走 Provider.Complete
	opts.MaxTokens = w.maxTokens()                           // 0 = 不发上限,由服务端默认值决定
	if hooks != nil {                                        // typed-nil guard: only set when concrete (avoids harness panic)
		opts.Hooks = hooks
	}
	if w.tx != nil { // persist raw LLM conversation; one file per worked intent
		opts.Transcript = w.tx
		opts.SessionID = WorkerSessionID(ts.ID(), intent.ID)
	}
	emitWrap := wrapIntentEmit(emit, name, intent.ID)
	// 意图 / 启动指令 / 意图锚定资产已随 system prompt 下发（见上方 sysBody 组装）。
	// 这条启动 user 消息只承载【全局态势 overview】——可降级的了解大局信息，压掉无碍。
	// overview 罕见地 marshal 失败为空时，回退一句启动词，避免首轮出现空 user 消息。
	input := overview
	if strings.TrimSpace(input) == "" {
		input = "Execute the intent assigned in the system prompt: do only this intent, produce facts, assets, and findings, and stop when complete."
	}

	// 实验功能:开启后由 noa 接管上下文压缩(归档集中在 <workDir>/noa/<SessionID> 下,持久)。
	noaSession := WorkerSessionID(ts.ID(), intent.ID)
	enableNoa(&opts, w.noaEnabledFn, w.workDir, noaSession, noaWarn(noaSession))
	ctx = attachSideCapture(ctx, &opts)
	s := agentcore.NewSession(opts)
	defer s.Close() // release the session's background-task manager (temp dir + processes)

	// Resume prior conversation if this intent was paused/blocked/exhausted and is
	// being re-run. The transcript ID is deterministic per intent, so if a prior
	// session exists the worker continues from where it left off instead of
	// restarting from scratch.
	alreadyRecorded := false
	if w.tx != nil {
		_ = s.Resume(opts.SessionID)
		alreadyRecorded = requestID != "" && hasWorkerChatMessage(s.Messages(), requestID)
		if len(s.Messages()) > 0 && message == "" {
			seedUnlockFromHistory(s.Messages(), def.UnlockSkill)
			input = "Continue execution."
		} else if len(s.Messages()) > 0 {
			seedUnlockFromHistory(s.Messages(), def.UnlockSkill)
		}
	}
	if message != "" {
		if alreadyRecorded {
			input = "Continue the new intent from the previous human message. Do not repeat completed actions."
		} else if len(s.Messages()) > 0 {
			input = workerChatMarker(requestID) + "\n[New intent from human input]\n" + message +
				"\n\nExecute this human instruction immediately. Once finished, decide from context whether to continue the original task."
		} else {
			input += "\n\n" + workerChatMarker(requestID) + "\n[New intent from human input]\n" + message +
				"\n\nPrioritize this human instruction."
		}
	}

	// Budgets + settlement are owned by the SDK (MaxTurns/MaxDuration + Settlement):
	// on hit it runs a wrap-up turn and finishes with ReasonMaxTurns/ReasonTimeout.
	// MaxDuration now interrupts an in-flight tool at the wall-clock deadline and
	// enters the wrap-up phase on the live ctx, so a run whose tool overran the budget
	// still settles (no external hard-timeout backstop needed). ctx itself carries only
	// pause / planner kill / shutdown, which the engine distinguishes and re-queues/stops.
	_, reason, err := captureRunSession(ctx, s, input, emitWrap)
	return reason, tsx.Writes(), err
}
