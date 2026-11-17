// customexec.go：意图级自定义执行器执行链——用声明 executor_key 的自定义 agent 跑一条意图。
// 主要函数/类型：
//   - CustomExecutorConfig：自定义执行器输入（agent/provider/意图/工具回调/工作目录等）。
//   - RunCustomExecutor：装配工具与系统提示词执行意图；骨架复刻 agent/worker.go#Worker.Execute（K18/K23），
//     差异只在系统提示词（自定义 agent 模板）、工具（WorkerTools 底座 + agent augment）与意图块（RenderIntentTask）。

package agent

import (
	"context"
	"errors"
	"fmt"
	"log" // 【二开·P-LOG·A2】INFO/WARN 级别直接用 log
	"time"

	"artex/db"
	"artex/intercept"
	"norma/harness"
	"norma/llm"
)

// CustomExecutorConfig 自定义执行器输入。Provider 由 engine 侧按 Agent.LLMProfileID 解析注入（S1 Provider 注）。
type CustomExecutorConfig struct {
	Agent    *db.Agent // S2 resolveExecutor 三层校验后的自定义 agent
	Provider llm.Provider
	TaskID   int64
	Intent   *db.Node
	Assets   *db.AssetStore
	Store    *db.ExplorationStore
	Goal     string
	Hooks    harness.HookRunner
	Emit     func(db.Activity)
	// Enrich/NotifyFinding：tsx.SetEnrich / tsx.SetNotifyFinding 由 ToolSet 内部消费——
	// finding 落库回填与 DNS/HTTP 异步补全均在 ToolSet 层完成（与 worker 同一回调链路），A2 无需自调。
	Enrich        EnrichTrigger
	NotifyFinding func(int64, string)
	WorkDir       string
	ProxyAddr     string
	ProxyCACert   string

	// RequireSelfClaim=true：Agent 自行 list/claim；false：Engine 已预领取，直接执行。
	RequireSelfClaim bool
}

// ErrNoMatchingIntent 表示当前自定义执行器没有可领取的 proposed/open intent。
// Engine 收到该错误时保留原状态并停止 custom 阶段，不启动默认 Worker。
var ErrNoMatchingIntent = errors.New("No matching custom-executor intent")

// 【二开·P-EXEC·A2 自定义执行器】起 —— 意图级自定义执行器：装配工具与提示词执行一条意图（K18/K23）。
// RunCustomExecutor 用声明 executor_key 的自定义 agent 执行一条意图。
// 与 worker.execute() 的差异只有三处（其余逐行对齐）：
//  1. 系统提示词：renderSystem(cfg.Agent.Key, "", vars)——模板从该 agent 的提示词覆盖链取；
//  2. 意图任务块：RenderIntentTask（A1 导出，与 worker 同文案）；
//  3. ToolSet.worker 名 = "exec:"+cfg.Agent.ExecutorKey（活动流可追溯是哪个执行器跑的）。
//
// 返回 (终态原因, 写入统计, 错误)，与 worker.Execute 同签名语义，runIntent 终态分流零改动。
func RunCustomExecutor(ctx context.Context, cfg CustomExecutorConfig) (harness.TerminalReason, WriteCounts, error) {
	if cfg.Agent == nil {
		return "", WriteCounts{}, errors.New("Custom executor Agent is required")
	}
	executorKey := cfg.Agent.ExecutorKey
	if executorKey == "" {
		return "", WriteCounts{}, errors.New("Custom executor Agent.ExecutorKey is required")
	}
	name := "exec:" + executorKey
	tsx := NewToolSet(cfg.Store, name)
	tsx.SetTaskID(cfg.TaskID)
	coverageEnabled := cfg.Assets == nil || cfg.Assets.CoverageEnabled(cfg.TaskID)
	tsx.SetCoverageEnabled(coverageEnabled)
	if cfg.Assets != nil {
		tsx.SetAssetStore(cfg.Assets, cfg.Assets.Companies())
	}
	tsx.SetOwnerNode(cfg.Intent.ID)         // 发现的资产锚定到本意图 → 任务可见
	tsx.SetEnrich(cfg.Enrich)               // DNS/HTTP 异步补全
	tsx.SetNotifyFinding(cfg.NotifyFinding) // report_finding 落库当场唤醒 planner
	// 工具底座：WorkerTools + list_intents + claim_executor_intent + 默认工具族 + agent augment。
	base := append(tsx.CustomExecutorTools(), defaultToolsExcept("MultiEdit", "Glob", "Grep")...)
	ctx = WithRunInfo(ctx, RunInfo{TaskID: cfg.TaskID, ExplorationID: explorationID(cfg.Store), IntentID: cfg.Intent.ID})
	tools, def, cleanup := AugmentTools(ctx, cfg.Agent.Key, base)
	defer cleanup()
	// 本意图的专属工作目录 <workDir>/tasks/<taskID>/i<intentID>（与 worker 同款）。
	runDir := ensureRunDir(cfg.WorkDir, cfg.TaskID, cfg.Intent.ID)
	// 动作审核上下文：工作目录即会话归属（与 worker 同机制；不继承父 run 的 background）。
	ctx = intercept.WithReviewContext(ctx, runDir, intercept.ReviewBackground{})
	// 【二开·P-LOG·A2】启动日志：INFO 记开始执行（排障：这个意图是谁在跑）。
	log.Printf("[executor] custom executor starting: task %d intent #%d agent=%s executor=%s", cfg.TaskID, cfg.Intent.ID, cfg.Agent.Name, name)
	// 系统提示词：该自定义 agent 的模板（renderSystem 走提示词覆盖链）+ 意图块（A1）+ 启动指令。
	sysBody := renderSystem(cfg.Agent.Key, "", map[string]any{
		"Goal": cfg.Goal, "DataDir": cfg.WorkDir, "Now": time.Now().Format("2006-01-02 15:04"),
	})
	if cfg.RequireSelfClaim {
		sysBody += "\n\n[Executor claim protocol] First call list_intents(states=[\"proposed\"]) and verify this intent ID and payload.executor. If no match exists, stop immediately without other tool calls, starting the default Worker, or changing state. Otherwise call claim_executor_intent(intent_id) and confirm state=running and owner=" + name + " before execution. Mark the intent done only when actually complete. A missing match, pause, error, stop, or exhausted budget must never be marked done."
	} else {
		sysBody += "\n\n[Engine pre-claim override] The Engine already claimed this intent as " + name + " and its state is running. Ignore any Agent prompt requiring list/claim first. Do not call list_intents or claim_executor_intent or claim it again. Execute the intent below directly. Mark done only when actually complete; pauses, errors, stops, or exhausted budgets must never be marked done."
	}
	sysBody += RenderIntentTask(cfg.Intent) // A1
	sysBody += "\n\nExecute the intent above: do only this intent, produce only facts, assets, and findings, and stop when complete."
	system, boundary := deferredSystem(sysBody, def)
	// 公共装配块（intent_exec_base.go）：权限旁路/代理/工作目录/输出溢出目录/待办。
	// 差异字段在本函数覆写：Provider/SystemPrompt 用本 agent 模板、行预算用 agent 行预算、
	// 墙钟预算用该 agent 的 run_seconds（0 = 不限，任务级 deadline 夹逼由 ctx 层负责 taskclock）、
	// 命中预算 → SDK 收尾一轮（与 worker 同款 settlement 语义）。
	opts := baseAgentOptions(runDir, cfg.WorkDir, cfg.ProxyAddr, cfg.ProxyCACert)
	opts.Provider = cfg.Provider
	opts.SystemPrompt = system
	opts.DynamicBoundary = boundary
	opts.Tools = tools
	opts.DeferredTools = def.Deferred
	opts.UnlockSet = def.Unlock
	opts.MaxTurns = cfg.Agent.MaxTurns // 0 = 不限
	opts.MaxDuration = time.Duration(cfg.Agent.RunSecs) * time.Second
	opts.Settlement = wrapupSettlement(cfg.Agent.Key, []string{"Bash"})
	if cfg.Hooks != nil { // typed-nil guard：只挂非空 hook（避免 harness panic）
		opts.Hooks = cfg.Hooks
	}
	// emit 包装：打上执行器名与意图 id，活动流可追溯是哪个执行器跑的。
	emitWrap := wrapIntentEmit(cfg.Emit, name, cfg.Intent.ID)
	input := "Execute the following intent."
	if cfg.RequireSelfClaim {
		input = "Execute your claimed intent."
	}
	_, reason, err := captureRun(ctx, opts, input, emitWrap)
	if err != nil {
		// 【二开·P-LOG·A2】异常 WARN（排障：执行器为什么没跑完）。
		log.Printf("[executor] custom executor error ⚠ task %d intent #%d agent=%s: %v", cfg.TaskID, cfg.Intent.ID, name, err)
		return reason, tsx.Writes(), err
	}
	if cfg.RequireSelfClaim {
		// Agent 必须先通过 claim_executor_intent 领取。未领取且直接退出时保留 proposed/open，
		// 不写占位事实、不落 done，由 Engine 停止 custom 阶段而不是回退默认 Worker。
		current, getErr := cfg.Store.GetNode(cfg.Intent.ID)
		if getErr != nil {
			return reason, tsx.Writes(), getErr
		}
		if current == nil || ((current.State == "proposed" || current.State == "open") && current.Owner != name) {
			log.Printf("[executor] task %d intent #%d agent=%s: no matching claim; exiting without starting the default Worker", cfg.TaskID, cfg.Intent.ID, name)
			return reason, tsx.Writes(), ErrNoMatchingIntent
		}
	}
	// 兜底：什么都没写回时落一条事实，保证图上有执行痕迹（与 worker 兜底同语义）。
	if tsx.Writes().Total() == 0 {
		// 【二开·P-LOG·A2】零写占位 INFO（排障：这条占位 fact 是怎么来的）。
		log.Printf("[executor] custom executor zero-write fallback: task %d intent #%d agent=%s; inserting placeholder fact", cfg.TaskID, cfg.Intent.ID, name)
		_, _ = cfg.Store.AddNode("fact", map[string]any{
			"text": fmt.Sprintf("Intent #%d was executed by executor %s (no structured output was saved)", cfg.Intent.ID, name),
		}, 0, "confirmed", name, []int64{cfg.Intent.ID})
	}
	// 【二开·P-LOG·A2】完成 INFO（排障：跑没跑完、写了多少、怎么收的官）。
	log.Printf("[executor] custom executor completed: task %d intent #%d agent=%s writes=%d terminal=%s", cfg.TaskID, cfg.Intent.ID, name, tsx.Writes().Total(), reason)
	return reason, tsx.Writes(), nil
}

// 【二开·P-EXEC·A2 自定义执行器】止
