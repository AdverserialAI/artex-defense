// orchestration.go：编排与平台内置工具的注册、seed 及存量库一次性升级迁移。
// 主要函数/类型：
//   - orchestrationTools / hostTools：声明编排（任务操作）类内置工具集。
//   - roTool / wrTool / objSchema / delegateToTask：构造只读/只写工具、JSON schema，并把调用委派到任务运行时的辅助。
//   - toolSpawnTask / toolPauseTask / toolGetTaskGraph 等：spawn/pause 任务、任务图、约束、发现、worker trace 等工具实现。
//   - seedOrchestrationTools：把内置工具与默认绑定 seed 进 DB，并串联全部一次性提示词/绑定升级迁移。
//   - refreshBuiltinToolSchemas：按版本标记把代码里的 schema/描述变更刷入已 seed 的工具行。

package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"artex/agent"
	"artex/db"
	"artex/xlog" // 【二开·P-LOG·S8】spawn_task 四参数接收 DEBUG
	"norma/permission"
	actool "norma/tool"
)

// jsonResult marshals v to a JSON tool result.
func jsonResult(v any) (actool.Result, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return actool.Errorf(err.Error()), nil
	}
	return actool.Text(string(b)), nil
}

// 本文件实现 P2「跨任务编排工具集」(docs/跑分编排 §2 P2)。这些是 host 工具——需要
// 访问 Manager(任意任务的 Store)、Engine(暂停)、以及建任务流程,所以住在 server 层。
// 读类工具把「现有 per-task 工具」重定向到目标任务的 store 上跑(建一个临时 ToolSet
// 并 Call 其对应工具),从而复用完全相同的逻辑;控制类(spawn/pause)直接调 Manager/Engine。
// 它们像流量工具一样 seed 进 tools 表、按 agent 绑定(只绑给编排 agent 才可见)。

// hostTools is the runtime host-tool provider fed to ToolAugment: traffic tools
// (gated by capture) + cross-task orchestration tools + user-defined custom tools.
// The second return is the names of custom tools flagged `deferred` (schema
// withheld, routed via SearchExtraTools/ExecuteExtraTool). Per-agent binding still
// decides who actually sees any of them.
//
//nolint:unused // used as the hostTools provider in wireAgentAugment
func (s *Server) hostTools() ([]actool.CoreTool, map[string][]string) {
	tools := append(s.m.HostTools(), s.orchestrationTools()...)
	tools = append(tools, s.findingRetestTools()...)
	tools = append(tools, s.platformTools()...) // 平台操作工具(建改 skill/工具/MCP，给 Auto 用)
	custom, err := s.customTools()
	if err != nil {
		log.Printf("[custom-tool] failed to load: %v", err)
		return tools, nil
	}
	tools = append(tools, custom...)
	// deferred custom tools → name -> its bound agent keys. ToolAugment turns a
	// name into a deferred entry only for agents it's actually bound to (so we don't
	// advertise a tool the per-agent binding will drop from the callable set).
	deferred := map[string][]string{}
	rows, _ := s.m.pg.ListCustomTools()
	for _, t := range rows {
		if t.Deferred && t.Enabled {
			deferred[t.Key] = t.Agents
		}
	}
	return tools, deferred
}

// orchestrationTools returns the cross-task tool set. Bound per-agent via the
// tools table (default: no binding — opt-in for orchestration agents).
func (s *Server) orchestrationTools() []actool.CoreTool {
	return []actool.CoreTool{
		s.toolListTasks(),
		s.toolListLLMProfiles(),
		s.toolCheckInternalAssetTask(),
		s.toolSpawnTask(),
		s.toolPauseTask(),
		s.toolGetTaskGraph(),
		s.toolListTaskFindings(),
		s.toolAddHint(),
		s.toolAppendFindingToTask(),
		s.toolGetWorkerTrace(),
		s.toolListWorkerTraces(),
		s.toolSearchWorkerTraces(),
		s.toolGetTaskNodeDetail(),
		s.toolUpdateFindingReport(),
		s.toolGetFindingTraffic(),
		s.toolBindFindingTraffic(),
		s.toolDispatchFindingToAgent(),
		s.toolListTaskConstraints(), // 【二开·P-INTENT·U22】跨任务只读约束查询
	}
}

// --- schema helpers ---

func strParam(desc string) map[string]any {
	return map[string]any{"type": "string", "description": desc}
}

// parseProfileID reads an LLM profile id from a tool arg that may arrive as a JSON
// number (5) or a numeric string ("5"); returns 0 when absent/unparseable.
func parseProfileID(raw json.RawMessage) int64 {
	if len(raw) == 0 {
		return 0
	}
	var n int64
	if json.Unmarshal(raw, &n) == nil {
		return n
	}
	var str string
	if json.Unmarshal(raw, &str) == nil {
		v, _ := strconv.ParseInt(strings.TrimSpace(str), 10, 64)
		return v
	}
	return 0
}

func objSchema(props map[string]any, required ...string) map[string]any {
	m := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		req := make([]any, len(required))
		for i, r := range required {
			req[i] = r
		}
		m["required"] = req
	}
	return m
}

func roTool(name, desc string, schema map[string]any, run func(context.Context, json.RawMessage) (actool.Result, error)) actool.CoreTool {
	return actool.Build(actool.Spec{
		Name: name, Description: desc, Schema: schema,
		ReadOnly:   func(json.RawMessage) bool { return true },
		Concurrent: func(json.RawMessage) bool { return true },
		Permissions: func(context.Context, json.RawMessage, permission.Context) permission.Decision {
			return permission.Allowed()
		},
		Run: func(ctx context.Context, in json.RawMessage, _ *actool.ToolContext) (actool.Result, error) {
			return run(ctx, in)
		},
	})
}

func wrTool(name, desc string, schema map[string]any, run func(context.Context, json.RawMessage) (actool.Result, error)) actool.CoreTool {
	return actool.Build(actool.Spec{
		Name: name, Description: desc, Schema: schema,
		Permissions: func(context.Context, json.RawMessage, permission.Context) permission.Decision {
			return permission.Allowed()
		},
		Run: func(ctx context.Context, in json.RawMessage, _ *actool.ToolContext) (actool.Result, error) {
			return run(ctx, in)
		},
	})
}

// delegateToTask resolves the `task_id` in the input, builds a ToolSet bound to
// that task's store, strips task_id, and calls the chosen per-task tool — so the
// cross-task read reuses the exact in-task logic against another task.
func (s *Server) delegateToTask(ctx context.Context, in json.RawMessage, pick func(*agent.ToolSet) actool.CoreTool) (actool.Result, error) {
	var head struct {
		TaskID string `json:"task_id"`
	}
	_ = json.Unmarshal(in, &head)
	if strings.TrimSpace(head.TaskID) == "" {
		return actool.Errorf("task_id is required"), nil
	}
	t, ok := s.m.Task(head.TaskID)
	if !ok {
		return actool.Errorf("Task does not exist: " + head.TaskID), nil
	}
	var m map[string]json.RawMessage
	_ = json.Unmarshal(in, &m)
	delete(m, "task_id")
	inner, _ := json.Marshal(m)
	tsx := agent.NewToolSet(t.Store, "orchestrator")
	if s.m.Assets() != nil {
		tsx.SetAssetStore(s.m.Assets(), s.m.Assets().Companies())
	}
	tsx.SetNotify(t.Notify)         // 通用唤醒（无专用回调的写操作走它；读工具为 no-op）
	tsx.SetNotifyHint(t.NotifyHint) // add_hint → 记一条「人新增了 N 条战略提示：…」触发并唤醒 planner
	return pick(tsx).Call(ctx, inner, nil)
}

// --- tools ---

func (s *Server) toolListTasks() actool.CoreTool {
	return roTool("list_tasks",
		"List all tasks with ID, description, goal, status, run duration, parent task, and LLM profile so the orchestration agent can understand global progress, stalled tasks, and model assignments. Run duration is creation to now for running tasks and creation to last activity for terminal tasks (seconds). llm_profile shows the planner/worker profile name; (active profile) means it follows the global active profile.",
		objSchema(map[string]any{}),
		func(context.Context, json.RawMessage) (actool.Result, error) {
			lastAct, _ := s.m.PG().LastActivityAll()
			// id -> name to resolve each task's pinned LLM profile.
			profName := map[int64]string{}
			if profs, err := s.m.pg.ListProfiles(); err == nil {
				for _, p := range profs {
					profName[p.ID] = p.Name
				}
			}
			out := make([]map[string]any, 0)
			for _, t := range s.m.List() {
				// 与 Web /api/tasks 共用同一状态推导：queued 与 LLM 链耗尽
				// （chain_exhausted）必须与页面显示完全一致。
				status := s.resolvedTaskStatus(t)
				end := lastAct[t.ExpID]
				if live := s.engine.LastActivity(t.ID); live > end {
					end = live
				}
				dur := int64(0)
				if status == "running" {
					dur = time.Now().Unix() - t.CreatedAt
				} else if end > t.CreatedAt {
					dur = end - t.CreatedAt
				}
				row := map[string]any{"id": t.ID, "description": t.Description, "goal": t.Goal, "status": status, "run_seconds": dur}
				if t.ParentRef != "" {
					row["parent_ref"] = t.ParentRef
				}
				llmState := t.llmStateSnapshot()
				if llmState.ProfileID == nil {
					row["llm_profile"] = "(active configuration)"
				} else if n, ok := profName[*llmState.ProfileID]; ok {
					row["llm_profile"] = n
				} else {
					row["llm_profile"] = fmt.Sprintf("#%d (deleted)", *llmState.ProfileID)
				}
				out = append(out, row)
			}
			return jsonResult(out)
		})
}

// toolListLLMProfiles lists the available LLM profiles (name/model/active) so an
// orchestration agent can pick one for spawn_task's llm_profile. Never leaks keys.
func (s *Server) toolListLLMProfiles() actool.CoreTool {
	return roTool("list_llm_profiles",
		"List available LLM profiles with ID, name, model, format, and whether each is active. Use the ID as spawn_task's llm_profile_id to assign a task-specific model, such as a cheaper model for reconnaissance or a stronger model for exploitation. API keys are never included.",
		objSchema(map[string]any{}),
		func(context.Context, json.RawMessage) (actool.Result, error) {
			profs, err := s.m.pg.ListProfiles()
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			out := make([]map[string]any, 0, len(profs))
			for _, p := range profs {
				out = append(out, map[string]any{
					"id": p.ID, "name": p.Name, "model": p.Model, "format": p.Format, "is_active": p.IsDefault,
				})
			}
			return jsonResult(map[string]any{"profiles": out})
		})
}

// resolveSpawnReview preserves an explicitly enabled parent review and otherwise
// applies the selected scenario default. scenarioNKey is resolved for both
// stable-nkey and legacy numeric template references before this is called.
func (s *Server) resolveSpawnReview(inherited bool, scenarioNKey string) (bool, error) {
	if inherited || scenarioNKey == "" {
		return inherited, nil
	}
	cat, err := s.m.PG().GetTaskCategoryByNKey(scenarioNKey)
	if err != nil {
		return false, err
	}
	return cat != nil && cat.DefaultReviewEnabled, nil
}

func (s *Server) toolSpawnTask() actool.CoreTool {
	return wrTool("spawn_task",
		"Create a child task and start its exploration engine, returning task_id. The child has an independent exploration graph; when finding_id is provided, only that Finding's complete ancestor chain is copied. parent_ref is optional and records only the parent/child relationship.",
		objSchema(map[string]any{
			"finding_id":             map[string]any{"type": "integer", "description": "Required for internal penetration testing dispatch: triggering Finding's global ID, used for cross-task deduplication by actual IP/domain and selecting the Finding ancestry chain to copy"},
			"target_asset":           strParam("Optional: select the primary target when the Finding links to multiple hosts; it must belong to this Finding"),
			"name":                   strParam("Short task-list name; internal subtasks are normalized by the server to Internal Penetration Testing · <foothold host>"),
			"description":            strParam("Complete task execution instructions"),
			"goal":                   strParam("Task goal (what to achieve)"),
			"parent_ref":             strParam("Optional: parent task ID (for parent/child linkage)"),
			"llm_profile_id":         map[string]any{"type": "integer", "description": "Optional: LLM profile ID for this subtask's planner/worker (see list_llm_profiles); inherits the parent's configuration, then the globally active profile, when omitted"},
			"timeout_seconds":        map[string]any{"type": "integer", "description": "Optional: task timeout in seconds. Triggers graceful finalization and the timeout terminal state; omitted or 0 means unlimited"},
			"plan_heartbeat_seconds": map[string]any{"type": "integer", "description": "Optional: planner heartbeat interval in seconds. Runs planning when this interval has elapsed since the last planning round/task start without another trigger (deadlock fallback and supervision of active workers). Omitted or 0 defaults to 600 (10 minutes)"},
			"seed_first_intent":      map[string]any{"type": "boolean", "description": "Optional: for simple tasks, immediately submit one seed intent containing description+goal so the worker can begin without waiting for initial planning. Defaults to false (plan before execution)"},
			// 【二开·P-ORCH·S8-1 spawn_task 四参数 schema】起 —— 子任务建任务四参数的 JSON Schema
			"pre_agent":             strParam("Optional: custom executor_key for the seed intent (agent must be enabled, non-built-in, and declare executor_key); empty uses the default worker"),
			"planner_prompt":        strParam("Optional: complete task-level planner prompt override (maximum 32KB). Written atomically to tasks.planner_prompt_override at creation; cannot be edited while running. Takes precedence over planner_template_id"),
			"planner_template_id":   map[string]any{"type": "integer", "description": "Optional (legacy calls): select a preset planner template by numeric ID; its body is snapshotted into planner_prompt_override at creation. Prefer planner_template_nkey for new calls"},
			"planner_template_nkey": strParam("Optional: select a preset planner template by stable normalized name nkey; body is snapshotted at creation. Mutually exclusive with planner_template_id; planner_prompt takes precedence when both are provided"),
			"worker_count":          map[string]any{"type": "integer", "description": "Optional: task-level worker concurrency, 0–16; 0 or omitted uses the global default"},
			// 【二开·P-ORCH·S8-1 spawn_task 四参数 schema】止
		}, "description", "goal"),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct {
				FindingID            int64           `json:"finding_id"`
				TargetAsset          string          `json:"target_asset"`
				Name                 string          `json:"name"`
				Description          string          `json:"description"`
				Goal                 string          `json:"goal"`
				ParentRef            string          `json:"parent_ref"`
				LLMProfileID         json.RawMessage `json:"llm_profile_id"`
				TimeoutSeconds       int             `json:"timeout_seconds"`
				PlanHeartbeatSeconds int             `json:"plan_heartbeat_seconds"`
				SeedFirstIntent      bool            `json:"seed_first_intent"`
				// 【二开·P-ORCH·S8-2 spawn_task 四参数入参】起 —— 工具入参结构体中的四参数
				PreAgent            string `json:"pre_agent"`
				PlannerPrompt       string `json:"planner_prompt"`
				PlannerTemplateID   int64  `json:"planner_template_id"`
				PlannerTemplateNKey string `json:"planner_template_nkey"`
				WorkerCount         int    `json:"worker_count"`
				// 【二开·P-ORCH·S8-2 spawn_task 四参数入参】止
				// 【二开·P-INTENT·U23】可选审核开关：只能加强不能减弱
				ReviewEnabled *bool `json:"review_enabled"`
			}
			_ = json.Unmarshal(in, &a)
			// 【二开·P-LOG·S8】起 —— 四参数接收留痕（DEBUG 级；排障：LLM 到底传了什么）
			xlog.Debugf("[spawn] spawn_task parameters: pre_agent=%q planner_template_id=%d planner_template_nkey=%q worker_count=%d planner_prompt_bytes=%d", a.PreAgent, a.PlannerTemplateID, a.PlannerTemplateNKey, a.WorkerCount, len(a.PlannerPrompt))
			// 【二开·P-LOG·S8】止
			if strings.TrimSpace(a.Description) == "" {
				a.Description = "Untitled task"
			}
			if strings.TrimSpace(a.Goal) == "" {
				return actool.Errorf("goal is required"), nil
			}
			// 【二开·P-ORCH·S8-3 spawn_task 参数校验】起 —— pre_agent/planner_prompt 校验（与 00 册 S7 createTask 同款规则；工具侧失败=错误文本回 LLM）
			if a.PreAgent != "" {
				if err := validatePreAgent(s.m.PG(), a.PreAgent); err != nil {
					log.Printf("[spawn] spawn_task rejected ⚠ invalid pre_agent=%q (must be an executor_key declared by an enabled custom agent)", a.PreAgent)
					return actool.Errorf(fmt.Sprintf("Invalid pre_agent: must be an executor_key declared by an enabled custom agent; available: %s", s.executorKeyList())), nil
				}
			}
			if len(a.PlannerPrompt) > 32*1024 {
				log.Printf("[spawn] spawn_task rejected ⚠ planner_prompt too long: bytes=%d (maximum 32KB)", len(a.PlannerPrompt))
				return actool.Errorf("planner_prompt is too long (maximum 32KB)"), nil
			}
			plannerNKey := strings.TrimSpace(a.PlannerTemplateNKey)
			if plannerNKey != "" && a.PlannerTemplateID != 0 {
				return actool.Errorf("planner_template_id and planner_template_nkey are mutually exclusive; provide only one"), nil
			}
			// 【二开·P-ORCH·S8-3 spawn_task 参数校验】止
			// 【二开·P-ORCH·S8-4 spawn_task 模板与 worker 校验】起 —— 模板快照解析、场景分类与 worker_count 校验
			// planner_prompt 优先；模板引用只负责在创建时取正文快照，之后改/删模板不影响子任务。
			plannerPrompt := a.PlannerPrompt
			if plannerPrompt == "" && a.PlannerTemplateID == 0 && plannerNKey == "" {
				// 【二开·P-TPL·S13】与 createTask 同型快照链第三级：两者皆不传 → 默认模板正文快照
				// （子任务出生即带快照，无"先建后改"窗口期）；表为空则 ''，落 A4 运行时链兜底。
				if body, dErr := defaultPlannerPrompt(s.m.PG()); dErr == nil {
					plannerPrompt = body
				}
			}
			if plannerPrompt == "" && (a.PlannerTemplateID > 0 || plannerNKey != "") {
				var tpl *db.PlannerTemplate
				var tplErr error
				reference := fmt.Sprintf("planner_template_id #%d", a.PlannerTemplateID)
				if plannerNKey != "" {
					tpl, tplErr = s.m.PG().GetPlannerTemplateByNKey(plannerNKey)
					reference = fmt.Sprintf("planner_template_nkey %q", plannerNKey)
				} else {
					tpl, tplErr = s.m.PG().GetPlannerTemplate(a.PlannerTemplateID)
				}
				if tplErr != nil {
					return actool.Errorf(fmt.Sprintf("Failed to read planner template: %v", tplErr)), nil
				}
				if tpl == nil {
					log.Printf("[spawn] spawn_task rejected ⚠ %s does not exist", reference)
					return actool.Errorf(fmt.Sprintf("%s does not exist; available templates: %s", reference, s.plannerTemplateList())), nil
				}
				if err := validatePlannerTemplateBody(tpl.Body); err != nil {
					log.Printf("[spawn] spawn_task rejected ⚠ template #%d failed render validation: %v", tpl.ID, err)
					return actool.Errorf(fmt.Sprintf("Template #%d failed render validation: %s", tpl.ID, err.Error())), nil
				}
				plannerPrompt = tpl.Body
			}
			// 子任务按 Planner 场景选择同 nkey 分类，并快照该分类的 Worker 模板。
			scenarioNKey := plannerNKey
			if scenarioNKey == "" && a.PlannerTemplateID > 0 {
				if tpl, tplErr := s.m.PG().GetPlannerTemplate(a.PlannerTemplateID); tplErr != nil {
					return actool.Errorf(fmt.Sprintf("Failed to read planner template: %v", tplErr)), nil
				} else if tpl != nil {
					scenarioNKey = tpl.NKey
				}
			}
			var categoryID *int64
			workerPrompt := ""
			if scenarioNKey != "" {
				cat, catErr := s.m.PG().GetTaskCategoryByNKey(scenarioNKey)
				if catErr != nil {
					return actool.Errorf(fmt.Sprintf("Failed to read Worker scenario: %v", catErr)), nil
				}
				if cat != nil {
					categoryID = &cat.ID
					if cat.DefaultWorkerTemplateID > 0 {
						tpl, tplErr := s.m.PG().GetWorkerTemplate(cat.DefaultWorkerTemplateID)
						if tplErr != nil {
							return actool.Errorf(fmt.Sprintf("Failed to read Worker template: %v", tplErr)), nil
						}
						if tpl == nil {
							return actool.Errorf(fmt.Sprintf("Worker template for scenario %q (#%d) does not exist", scenarioNKey, cat.DefaultWorkerTemplateID)), nil
						}
						workerPrompt = tpl.Body
					}
				}
			}
			if workerPrompt == "" {
				body, bodyErr := defaultWorkerPrompt(s.m.PG())
				if bodyErr != nil {
					return actool.Errorf(fmt.Sprintf("Failed to read default Worker template: %v", bodyErr)), nil
				}
				workerPrompt = body
			}
			if err := validateWorkerTemplateBody(workerPrompt); err != nil {
				return actool.Errorf("Worker template render validation failed: " + err.Error()), nil
			}
			if err := validateWorkerCount(a.WorkerCount); err != nil {
				log.Printf("[spawn] spawn_task rejected ⚠ worker_count=%d out of range (valid: 0–16)", a.WorkerCount)
				return actool.Errorf("worker_count must be 0–16 (0 uses global default)"), nil
			}
			// 【二开·P-ORCH·S8-4 spawn_task 模板与 worker 校验】止
			if a.TimeoutSeconds < 0 {
				a.TimeoutSeconds = 0
			}
			// 约束轻校验（m01210 拍板·简化版，00 册 S8）：父任务 goal 文本里「禁止」行 → 子串匹配子任务 goal。
			// 这是 LLM 无法绕过的代码侧硬闸门，与父任务 task_constraints 的 deny 闸门互补。
			if a.ParentRef != "" {
				if rule := s.matchForbiddenRule(a.ParentRef, a.Goal); rule != "" {
					log.Printf("[spawn] spawn_task rejected ⚠ blocked by parent task constraints: rule=%q child_goal=%q", rule, a.Goal)
					return actool.Errorf(fmt.Sprintf("Blocked by parent task constraint: %s (subtask goal includes a forbidden direction; if authorized, adjust the goal wording or create the task manually)", rule)), nil
				}
				// 【二开·P-INTENT·U22】操作约束硬闸门：父任务 deny 约束「禁止X」同样拦截（结构化载体）
				if rule := s.matchForbiddenConstraint(a.ParentRef, a.Goal); rule != "" {
					log.Printf("[spawn] spawn_task rejected ⚠ blocked by parent operational constraints: rule=%q child_goal=%q", rule, a.Goal)
					return actool.Errorf(fmt.Sprintf("Blocked by parent operational constraint: deny rule '%s' matched the subtask goal (explicitly prohibited directions must not produce subtasks; if authorized, adjust the goal wording or modify parent constraints)", rule)), nil
				}
			}
			// LLM profile resolution: explicit id > inherit parent's pin > active(nil).
			var pin *int64
			if id := parseProfileID(a.LLMProfileID); id > 0 {
				if _, ok := s.loadProfileConfig(id); !ok {
					return actool.Errorf(fmt.Sprintf("LLM profile #%d does not exist or has no API key", id)), nil
				}
				pin = &id
			} else if a.ParentRef != "" {
				if pt, ok := s.m.Task(a.ParentRef); ok {
					pin = pt.LLMProfileID
				}
			}
			// 【二开·P-DISPATCH·阶段0①】ReviewEnabled 继承父任务（04 册 §5.1①：spawn_task 不透传
			// 会让 vuln_dispatcher 派出的内网子任务绕过审核闸门）。无 parent_ref 时保持默认关。
			// ①b 修订（用户拍板 2026-10-25，m00799「父任务不需要开启审核」）：父任务关审 ≠
			// 子任务连坐——父关时按 planner 模板 nkey 反查同 nkey 分类的 default_review_enabled
			// 兑底（内网渗透/权限提升/漏洞利用分类默认 t）。父开仍强制开（只能加强不能减弱）。
			// dispatcher 依旧无发言权：review_enabled 参数不下放，值只来自人设定的两级默认。
			reviewEnabled := false
			if a.ParentRef != "" {
				if pt, ok := s.m.Task(a.ParentRef); ok {
					reviewEnabled = pt.ReviewEnabled
				}
			}
			inheritedReview := reviewEnabled
			resolvedReview, reviewErr := s.resolveSpawnReview(inheritedReview, scenarioNKey)
			if reviewErr != nil {
				return actool.Errorf("Failed to read default Review scenario: " + reviewErr.Error()), nil
			}
			reviewEnabled = resolvedReview
			if !inheritedReview && reviewEnabled {
				xlog.Infof("[spawn] spawn_task review enabled by category default: scenario %s default_review_enabled=t", scenarioNKey)
			}
			var dispatchAssetKeys []string
			if scenarioNKey == "内网渗透" {
				var keyErr error
				dispatchAssetKeys, keyErr = s.findingDispatchAssetKeys(a.FindingID, a.TargetAsset)
				if keyErr != nil {
					return actool.Errorf(keyErr.Error()), nil
				}
			}
			var llmIDs []int64
			if pin != nil {
				llmIDs = []int64{*pin}
			}
			t, err := s.m.CreateTaskWithOptions(a.Description, a.Goal, db.TaskCreateOptions{
				Name:                 resolveSpawnTaskName(scenarioNKey, a.Name, dispatchAssetKeys),
				CategoryID:           categoryID,
				WorkerPrompt:         workerPrompt,
				CopyFindingChain:     a.FindingID > 0,
				LLMProfileIDs:        llmIDs,
				TimeoutSeconds:       a.TimeoutSeconds,
				PlanHeartbeatSeconds: a.PlanHeartbeatSeconds,
				// 【二开·P-ORCH·S8-5 spawn_task 建任务透传】起 —— 四参数写入建任务选项
				PreAgent:      a.PreAgent,    // executor_key 随 tasks.pre_agent 持久化，重启恢复仍由对话型 Agent 接管
				PlannerPrompt: plannerPrompt, // 模板快照/手改全文已归一（K22 prompt 优先）
				WorkerCount:   a.WorkerCount, // 0=跟随全局；03 册 S12 Engine.Run 读列
				// 【二开·P-ORCH·S8-5 spawn_task 建任务透传】止
				ReviewEnabled:     reviewEnabled, // 【二开·P-DISPATCH·阶段0①】父开强制开；父关按分类默认兜底（resolveSpawnReview）
				DispatchAssetKeys: dispatchAssetKeys,
				DispatchFindingID: a.FindingID,
			})
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			// 【二开·P-LOG·S8】起 —— 子任务出生（INFO 级；编排链终点锚点）
			log.Printf("[spawn] spawn_task created subtask: task_id=%s name=%q parent_ref=%q pre_agent=%q worker_count=%d prompt_snapshot_bytes=%d", t.ID, t.Name, a.ParentRef, a.PreAgent, a.WorkerCount, len(plannerPrompt))
			// 【二开·P-LOG·S8】止
			if a.ParentRef != "" {
				t.ParentRef = a.ParentRef
				if id, e := strconv.ParseInt(t.ID, 10, 64); e == nil {
					_ = s.m.PG().SetParentRef(id, a.ParentRef)
				}
			}
			// 共享的建后流程,与 HTTP 建任务(server.go createTask)复用同一段 launchTask:
			// seed + 后台可见地做目标分解(第0轮/LLM步骤/逐条goal) + engine.Run。
			// seed_first_intent 默认 false(标准先规划再执行);简单任务可开启直接下发一 work 测试。
			s.launchTask(t, a.Description+" "+a.Goal, a.SeedFirstIntent)
			return actool.Text(fmt.Sprintf("task created: %s", t.ID)), nil
		})
}

func (s *Server) toolPauseTask() actool.CoreTool {
	return wrTool("pause_task", "Pause the specified task by stopping its planner and worker loops.",
		objSchema(map[string]any{"task_id": strParam("ID of the task to pause")}, "task_id"),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct {
				TaskID string `json:"task_id"`
			}
			_ = json.Unmarshal(in, &a)
			t, ok := s.m.Task(a.TaskID)
			if !ok {
				return actool.Errorf("Task does not exist: " + a.TaskID), nil
			}
			if _, err := s.applyTaskControlWithCause(t, "pause", agent.AbortPausedByOrchestrator); err != nil {
				return actool.Errorf(err.Error()), nil
			}
			return actool.Text("task paused: " + a.TaskID), nil
		})
}

func (s *Server) toolGetTaskGraph() actool.CoreTool {
	return roTool("get_task_graph", "Read a task's exploration overview (asset counts, frontier, findings, coverage, and more, matching graph_overview) plus its spawned child tasks in child_tasks for dispatch deduplication. Specify the task with task_id.",
		objSchema(map[string]any{"task_id": strParam("Task ID")}, "task_id"),
		func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			res, err := s.delegateToTask(ctx, in, (*agent.ToolSet).GraphOverviewTool)
			if err != nil {
				return res, err
			}
			// 附带已派生子任务列表：探索图本身不含任务派生关系，v5 派发去重纪律
			//（同资产已有子任务则不派）此前无数据可查，导致重复派生（task 207 实测踩中）。
			var head struct {
				TaskID string `json:"task_id"`
			}
			_ = json.Unmarshal(in, &head)
			pid, perr := strconv.ParseInt(strings.TrimSpace(head.TaskID), 10, 64)
			if perr != nil {
				return res, nil
			}
			children, cerr := s.m.pg.ChildTasksByParentRef(pid)
			if cerr != nil {
				return res, nil
			}
			txt := res.Flatten()
			var obj map[string]any
			if json.Unmarshal([]byte(txt), &obj) != nil {
				return res, nil
			}
			obj["child_tasks"] = children
			merged, _ := json.Marshal(obj)
			return actool.Text(string(merged)), nil
		})
}

func (s *Server) toolListTaskConstraints() actool.CoreTool {
	return roTool("list_task_constraints", "Read a task's operational constraints (allow/deny entries with origin). Read parent constraints before spawning a child task; directions explicitly forbidden by those constraints must not be spawned. Specify the task with task_id.",
		objSchema(map[string]any{"task_id": strParam("Task ID")}, "task_id"),
		func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			return s.delegateToTask(ctx, in, (*agent.ToolSet).ListConstraintsTool)
		})
}

func (s *Server) toolListTaskFindings() actool.CoreTool {
	return roTool("list_task_findings", "Read a task's confirmed findings, including flags and PoC. Each entry includes id, task_id, intent_id, vulnerability class, severity, summary, and status. Specify the task with task_id.",
		objSchema(map[string]any{"task_id": strParam("Task ID")}, "task_id"),
		func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			return s.delegateToTask(ctx, in, (*agent.ToolSet).ListFindingsTool)
		})
}

func (s *Server) toolAddHint() actool.CoreTool {
	return wrTool("add_task_hint", "Inject strategic guidance into a task so its planner reads it before creating the next intents. Prefer batch submission through the hints array; returned ids match the input order and failed entries have id=0. For one hint, omit hints and provide top-level text.",
		objSchema(map[string]any{
			"task_id":      strParam("Task ID"),
			"hints":        map[string]any{"type": "array", "description": "Preferred: hints array; each entry has the same fields as the top level (text/asset_ids/traffic_refs).", "items": objSchema(map[string]any{"text": strParam("Hint content"), "asset_ids": map[string]any{"type": "array", "items": map[string]any{"type": "integer"}}, "traffic_refs": agent.HintTrafficSchema()})},
			"text":         strParam("[Single hint] Hint content"),
			"traffic_refs": agent.HintTrafficSchema(),
			"asset_ids":    map[string]any{"type": "array", "items": map[string]any{"type": "integer"}, "description": "Anchored asset IDs (optional: zero, one, or more asset IDs from this task)"},
		}, "task_id"),
		func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			return s.delegateToTask(ctx, in, (*agent.ToolSet).AddHintTool)
		})
}

func (s *Server) toolGetWorkerTrace() actool.CoreTool {
	return roTool("get_task_worker_trace",
		"Inspect a work intent in a task: get_task_worker_trace(task_id, intent_id) returns step summaries; add step_ids=[...] to retrieve full contents for up to 5 selected steps.",
		objSchema(map[string]any{
			"task_id":   strParam("Task ID"),
			"intent_id": map[string]any{"type": "integer", "description": "Intent ID (work in this task)"},
			"step_ids":  map[string]any{"type": "array", "items": map[string]any{"type": "integer"}, "description": "Optional: step IDs for full content (maximum 5 per call; extra IDs are returned in omitted_step_ids)"},
		}, "task_id", "intent_id"),
		func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			return s.delegateToTask(ctx, in, (*agent.ToolSet).GetWorkerTraceTool)
		})
}

func (s *Server) toolListWorkerTraces() actool.CoreTool {
	return roTool("list_task_worker_traces", "List work intents that have run in a task and their step counts to identify traces worth inspecting with get_task_worker_trace.",
		objSchema(map[string]any{"task_id": strParam("Task ID")}, "task_id"),
		func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			return s.delegateToTask(ctx, in, (*agent.ToolSet).ListWorkerTracesTool)
		})
}

func (s *Server) toolSearchWorkerTraces() actool.CoreTool {
	return roTool("search_task_worker_traces", "Search all work execution traces in a task by keyword, returning matching step summaries and intent_id values.",
		objSchema(map[string]any{"task_id": strParam("Task ID"), "q": strParam("Search keyword")}, "task_id", "q"),
		func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			return s.delegateToTask(ctx, in, (*agent.ToolSet).SearchWorkerTracesTool)
		})
}

func (s *Server) toolGetTaskNodeDetail() actool.CoreTool {
	return roTool("get_task_node_detail",
		"Read a complete exploration node in a task (finding, fact, intent, or goal), including summary, details, evidence, and PoC. id is the exploration node ID returned by report_finding or list_task_findings, not an asset ID. Use it to retrieve complete finding evidence before writing a report.",
		objSchema(map[string]any{
			"task_id": strParam("Task ID"),
			"id":      map[string]any{"type": "integer", "description": "Exploration node ID (not an asset ID)"},
		}, "task_id", "id"),
		func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			return s.delegateToTask(ctx, in, (*agent.ToolSet).NodeDetailTool)
		})
}

// toolUpdateFindingReport writes/overwrites a finding's detailed Markdown report.
// finding_id is the id report_finding returned ("finding recorded: <id>", the
// finding node id). The write (SetFindingReportByNodeID) is keyed by node_id and
// task-agnostic, so this host tool needs no task_id / exploration store.
func (s *Server) toolUpdateFindingReport() actool.CoreTool {
	return wrTool("update_finding_report",
		"Write or replace a registered finding's complete detailed Markdown report. finding_id is the ID returned by report_finding (the number in \"finding recorded: <id>\"). Recommended sections: overview, impact, reproduction steps, evidence/PoC, and remediation.",
		objSchema(map[string]any{
			"finding_id":       map[string]any{"type": "integer", "description": "Target finding ID (ID returned by report_finding)"},
			"report":           strParam("Complete detailed report in Markdown"),
			"evidence_version": map[string]any{"type": "integer", "description": "Evidence version returned by get_finding_traffic; prevents a report from overwriting newer evidence changes"},
		}, "finding_id", "report"),
		func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct {
				EvidenceVersion *int64          `json:"evidence_version"`
				FindingID       json.RawMessage `json:"finding_id"`
				Report          string          `json:"report"`
			}
			_ = json.Unmarshal(in, &a)
			nodeID := parseProfileID(a.FindingID) // 复用「数字或数字字符串」解析
			if nodeID <= 0 {
				return actool.Errorf("Invalid finding_id"), nil
			}
			n, err := s.m.pg.SetFindingReportVersionByNodeID(ctx, nodeID, a.Report, a.EvidenceVersion)
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			if n == 0 {
				return actool.Errorf(fmt.Sprintf("No finding record for finding_id=%d (register it with report_finding first)", nodeID)), nil
			}
			return actool.Text(fmt.Sprintf("finding %d report updated (%d chars)", nodeID, len(a.Report))), nil
		})
}

// seedOrchestrationTools seeds the cross-task tools into the tools table so they
// are bindable per-agent (default: bound to nobody — opt-in for orchestration
// agents). First-insert only, like the traffic seeds.
func (s *Server) seedOrchestrationTools() {
	// task-op + platform tools default-bind to the built-in Auto agent (它天生用来
	// 操作平台)。SeedTool 首插入生效;老库已 seed 的行由 seedAutoDefaultBindings 补绑。
	autoAgents, _ := json.Marshal([]string{"auto"})
	for _, t := range s.orchestrationTools() {
		schema, _ := json.Marshal(t.InputSchema())
		bindings := autoAgents
		switch t.Name() {
		case "bind_finding_traffic":
			bindings = json.RawMessage(`["reporter"]`)
		case "dispatch_finding_to_agent":
			bindings = json.RawMessage(`["planner"]`)
		}
		_ = s.m.PG().SeedTool(t.Name(), t.Description(), schema, bindings)
	}
	for _, t := range s.platformTools() {
		schema, _ := json.Marshal(t.InputSchema())
		_ = s.m.PG().SeedTool(t.Name(), t.Description(), schema, autoAgents)
	}
	s.refreshBuiltinToolSchemas()
	s.seedAutoDefaultBindings()
	s.seedPlannerDefaultBindings()
	s.seedPlannerListAssetsBinding()
	s.seedCompanyScopeRebind()
	s.seedWorkerReadToolsUnbind() // list_facts/list_companies/list_worker_traces 从 worker 默认解绑(一次性)
	s.seedWorkerReadbackRebind()  // 修复旧迁移误删：把 search_all_worker_traces/get_worker_trace/node_detail 补绑回 worker(一次性)
	s.seedAutoReportFindingBinding()
	s.unbindGoalMetDefault()
	s.reseedGoalsPrompt()             // goals 提示词加入「抽操作约束」步 → 旧库追加一版新默认(一次性)
	s.reseedMainAgentPrompt()         // mainagent 提示词加入「目标达成后 add_intent 反问是否建目标」(一次性)
	s.reseedPlannerPrompt()           // planner 提示词:重写「0 意图」正当理由 + 加量化验收核对(一次性)
	s.reseedWorkerPrompt()            // worker 提示词:加否定结论证据门槛(一次性)
	s.upgradeExternalFindingPrompts() // 外网内置 Planner/Worker 提示词加入 Finding 证据与显式派发规则
	s.seedReporterAgent()             // 预置「报告撰写」agent + 工具绑定 + finding 触发器(一次性)
	s.seedVulnDispatcherAgent()       // 【二开·P-DISPATCH】预置「漏洞派发」agent + finding 触发器(一次性)
	if err := s.seedTraceCleanerAgent(); err != nil {
		log.Printf("[trace_cleaner] seed: %v", err)
	}
	s.upgradeVulnDispatcherV2()            // 【二开·P-INTENT·U22】存量库升级：约束工具绑定+提示词/触发消息迁移(一次性；提示词现为 v4：审核由人决定)
	s.upgradeVulnDispatcherV5()            // 【二开·P-DISPATCH·阶段0②】存量库升级：提示词 v5 同资产去重强化(一次性，血统未改才覆盖)
	s.upgradeVulnDispatcherNKey()          // v6：场景模板改用稳定 nkey，清除跨环境失效数字 ID
	s.upgradeVulnDispatcherFindingPrompt() // 默认血统改为 Planner 显式派发入口
	s.upgradeVulnDispatcherIndependentGraph()
	s.upgradePlannerFindingDispatch() // 关闭 dispatcher/cleaner 旧全局 Finding 入口
	s.upgradeReporterTriggerMessage() // 老库补迁移:让 reporter 回传 evidence_version(一次性)
	s.seedFindingTrafficTools()       // 增加可选证据参数及只读证据工具，保留用户配置
	s.seedFindingWorkflowTools()
	// 注：pentest 的默认工具绑定无需迁移——BuiltinToolSeeds 在全新初始化时就把
	// list_assets/insert_assets/report_finding/list_findings/list_companies 连同
	// pentest 一起 seed 好了（项目尚无旧库，不做迁移）。
}

// refreshBuiltinToolSchemas propagates code schema/description changes on the
// orchestration + platform tools into already-seeded rows ONCE per version flag —
// SeedTool is first-insert-only, so a new param (e.g. spawn_task 的 llm_profile) never
// reaches an old DB otherwise. Preserves each tool's agent binding + enabled flag.
// Bump the flag whenever these tools' schemas/descriptions change in code.
func (s *Server) refreshBuiltinToolSchemas() {
	const flag = "tool_schema_refresh_v14_independent_spawn" // v14：spawn_task 使用独立探索链并按 finding_id 选择性复制血缘
	if v, _, _ := s.m.pg.GetSetting(flag); v == "true" {
		return
	}
	tools := append(s.orchestrationTools(), s.platformTools()...)
	for _, t := range tools {
		schema, _ := json.Marshal(t.InputSchema())
		if err := s.m.pg.RefreshToolDefaults(t.Name(), t.Description(), schema); err != nil {
			log.Printf("[tools] refresh %s schema failed: %v", t.Name(), err)
		}
	}
	// 同时把部分内置 agent 工具刷成代码默认：
	//   - goal_met：旧库 seed 的描述带“结束本轮规划”的误导，会让 planner 把它当成
	//     “结束空轮”的手段、刚开跑就误判整个任务完成。
	//   - insert_assets：新增 related 入参(标记资产是否与当前任务相关、决定是否入覆盖度)，
	//     SeedTool 首插入only，旧库已 seed 的 schema 否则收不到这个新参数。
	//   - list_facts：改为分页，新增 limit/before/q 入参；旧库已 seed 的空 schema 否则
	//     在工具管理页显示「无参数」，模型也拿不到这几个参数说明。
	// 【二开·P-ORCH·§4.2.8 report_finding 词表】vulnclass 词表描述（m03215 九类两档，触发条件=乱写率>20%，101 实测约 90% 已命中）
	refreshBuiltin := map[string]bool{"goal_met": true, "insert_assets": true, "list_facts": true, "report_finding": true}
	for _, sd := range agent.BuiltinToolSeeds() {
		if !refreshBuiltin[sd.Key] {
			continue
		}
		schema, _ := json.Marshal(sd.Schema)
		if err := s.m.pg.RefreshToolDefaults(sd.Key, sd.Desc, schema); err != nil {
			log.Printf("[tools] refresh %s desc failed: %v", sd.Key, err)
		}
	}
	_ = s.m.pg.SetSetting(flag, "true")
	log.Printf("[tools] refreshed orchestration/platform tool schemas to code defaults (one-time migration)")
}

// unbindGoalMetDefault removes goal_met's default "planner" binding ONCE (guarded by
// a settings flag), so existing DBs match the new default of NO agent. goal_met bypasses
// per-goal prove_goal to declare the whole task done — powerful/risky and redundant with
// the prove_goal→auto-complete path — so it ships unbound; users can re-bind it per agent
// in the UI. A user's own binding to another agent is untouched (we only strip planner).
func (s *Server) unbindGoalMetDefault() {
	const flag = "goal_met_unbind_default_v1"
	if v, _, _ := s.m.pg.GetSetting(flag); v == "true" {
		return
	}
	if err := s.m.pg.RemoveAgentFromTool("planner", "goal_met"); err != nil {
		log.Printf("[tools] failed to unbind goal_met from planner: %v", err)
		return
	}
	_ = s.m.pg.SetSetting(flag, "true")
}

// reseedGoalsPrompt 把 goals 目标拆解器的提示词刷成【当前代码默认】——因为默认正文新增了
// 「先抽操作约束(set_constraints)再拆目标」这一步,而 SeedPromptIfEmpty 首插入only,旧库
// 已有的 version 1 收不到这步。这里用版本管理【追加一个新版本】并切过去(ResetPromptToDefault),
// 旧的版本仍保留在历史里,用户若自定义过可从版本记录找回。settings flag 守卫 → 只做一次;
// 以后默认再变就 bump 这个 flag。全新库无需处理(SeedPromptIfEmpty 已 seed 最新默认)。
func (s *Server) reseedGoalsPrompt() {
	const flag = "goals_prompt_constraint_step_v1"
	if v, _, _ := s.m.pg.GetSetting(flag); v == "true" {
		return
	}
	defer func() { _ = s.m.pg.SetSetting(flag, "true") }() // 无论成功与否只尝试一次
	a, err := s.m.pg.GetAgentByKey("goals")
	if err != nil || a == nil {
		return // 全新库尚未建 agent 行时,seedPrompts 会直接 seed 最新默认,无需此迁移
	}
	tmpl := agent.BuiltinPromptSeeds()["goals"]
	if tmpl == "" {
		return
	}
	// 全新库 seedPrompts 已 seed 最新默认 → 当前版本已等于代码默认,不必再追加重复版本。
	if cur, err := s.m.pg.CurrentPrompt(a.ID); err == nil && cur == tmpl {
		return
	}
	if _, err := s.m.pg.ResetPromptToDefault(a.ID, tmpl); err != nil {
		log.Printf("[prompts] failed to refresh goals prompt to new default: %v", err)
		return
	}
	log.Printf("[prompts] added new default goals prompt version (operational-constraint extraction; one-time migration)")
}

// reseedMainAgentPrompt 把 mainagent 提示词刷成【当前代码默认】——默认正文新增了「目标全部
// 达成后 add_intent 直投意图时,反问人是否登记为正式目标」这段引导,而 SeedPromptIfEmpty 首插入
// only,旧库已有版本收不到。用版本管理【追加一个新版本】并切过去(ResetPromptToDefault),旧版本仍
// 保留在历史里,用户若自定义过可从版本记录找回。settings flag 守卫 → 只做一次。全新库无需处理
// (SeedPromptIfEmpty 已 seed 最新默认)。与 reseedGoalsPrompt 完全同构。
func (s *Server) reseedMainAgentPrompt() {
	const flag = "mainagent_prompt_goalless_intent_v1"
	if v, _, _ := s.m.pg.GetSetting(flag); v == "true" {
		return
	}
	defer func() { _ = s.m.pg.SetSetting(flag, "true") }() // 无论成功与否只尝试一次
	a, err := s.m.pg.GetAgentByKey("mainagent")
	if err != nil || a == nil {
		return // 全新库尚未建 agent 行时,seedPrompts 会直接 seed 最新默认,无需此迁移
	}
	tmpl := agent.BuiltinPromptSeeds()["mainagent"]
	if tmpl == "" {
		return
	}
	// 全新库 seedPrompts 已 seed 最新默认 → 当前版本已等于代码默认,不必再追加重复版本。
	if cur, err := s.m.pg.CurrentPrompt(a.ID); err == nil && cur == tmpl {
		return
	}
	if _, err := s.m.pg.ResetPromptToDefault(a.ID, tmpl); err != nil {
		log.Printf("[prompts] failed to refresh mainagent prompt to new default: %v", err)
		return
	}
	log.Printf("[prompts] added new default mainagent prompt version (offer to create goals after completion; one-time migration)")
}

// reseedPlannerPrompt 把 planner 提示词刷成【当前代码默认】——默认正文做了精简重构,并把「克制」降级为
// 仅去重、新增「深度优先于覆盖度」「硬底线:目标未达成且无在跑意图必须产出」、给否定结论复核加上界。
// 每次默认有实质变更就 bump 下面的 flag(当前 v2)让存量旧库再刷一次。SeedPromptIfEmpty 首插入only,旧库已有版本收不到,故用版本管理
// 【追加一个新版本】并切过去(ResetPromptToDefault),旧版本仍保留在历史里,用户若自定义过可从版本记录
// 找回。settings flag 守卫 → 只做一次。全新库无需处理(SeedPromptIfEmpty 已 seed 最新默认)。与
// reseedGoalsPrompt 完全同构。
func (s *Server) reseedPlannerPrompt() {
	const flag = "planner_prompt_compact_realistic_v2"
	if v, _, _ := s.m.pg.GetSetting(flag); v == "true" {
		return
	}
	defer func() { _ = s.m.pg.SetSetting(flag, "true") }() // 无论成功与否只尝试一次
	a, err := s.m.pg.GetAgentByKey("planner")
	if err != nil || a == nil {
		return // 全新库尚未建 agent 行时,seedPrompts 会直接 seed 最新默认,无需此迁移
	}
	tmpl := agent.BuiltinPromptSeeds()["planner"]
	if tmpl == "" {
		return
	}
	// 全新库 seedPrompts 已 seed 最新默认 → 当前版本已等于代码默认,不必再追加重复版本。
	if cur, err := s.m.pg.CurrentPrompt(a.ID); err == nil && cur == tmpl {
		return
	}
	if _, err := s.m.pg.ResetPromptToDefault(a.ID, tmpl); err != nil {
		log.Printf("[prompts] failed to refresh planner prompt to new default: %v", err)
		return
	}
	log.Printf("[prompts] added new default planner prompt version (simplification, restrained deduplication, depth first, bounded negative-result review; one-time migration)")
}

// reseedWorkerPrompt 把 worker 提示词刷成【当前代码默认】——v6 在 v5（中性化重写）基础上
// 给 report_finding 补 vulnclass 归类规范（九类定义+严重档「已执行/已控制」证据门槛）：vulnclass
// 影响 Planner 后续显式派发，随手写 other 会让严重漏洞漏派。bump flag 让存量旧库再刷一次。
// SeedPromptIfEmpty 首插入only,旧库已有版本收不到,故用版本管理【追加一个新版本】并切过去,旧版本仍保留在历史里可找回。
// settings flag 守卫 → 只做一次。全新库无需处理。与 reseedGoalsPrompt 完全同构。
func (s *Server) reseedWorkerPrompt() {
	const flag = "worker_prompt_vulnclass_v6"
	if v, _, _ := s.m.pg.GetSetting(flag); v == "true" {
		return
	}
	defer func() { _ = s.m.pg.SetSetting(flag, "true") }() // 无论成功与否只尝试一次
	a, err := s.m.pg.GetAgentByKey("worker")
	if err != nil || a == nil {
		return // 全新库尚未建 agent 行时,seedPrompts 会直接 seed 最新默认,无需此迁移
	}
	tmpl := agent.BuiltinPromptSeeds()["worker"]
	if tmpl == "" {
		return
	}
	// 全新库 seedPrompts 已 seed 最新默认 → 当前版本已等于代码默认,不必再追加重复版本。
	if cur, err := s.m.pg.CurrentPrompt(a.ID); err == nil && cur == tmpl {
		return
	}
	if _, err := s.m.pg.ResetPromptToDefault(a.ID, tmpl); err != nil {
		log.Printf("[prompts] failed to refresh worker prompt to new default: %v", err)
		return
	}
	log.Printf("[prompts] added new default worker prompt version (v6: vulnerability classification and evidence thresholds for severe findings; one-time migration)")
}

// reporterToolCallMessage 必须无条件要求先读一次 get_finding_traffic 再写报告。
// 该工具是只读的、「不依赖捕获开关」,自动绑定关不关都能读到人工绑定的证据。若这里
// 写成「启用自动绑定才读」,默认关闭配置下 reporter 就不会传 evidence_version,
// SetFindingReportVersionByNodeID 便按 legacy 语义写 -1,漏洞详情与 Markdown 导出
// 从此常驻「证据已变更，报告待更新」,而 UI 上没有任何入口能把它清掉。
const reporterToolCallMessage = "A finding was just registered by report_finding. Read finding_id (independent finding record ID) and finding_node_id (exploration node ID) from the returned JSON." +
	"First call get_finding_traffic(finding_id) to read the current evidence list and version (an empty list is normal; still write the report)." +
	"If runtime guidance enables automatic binding, verify and link this finding's traffic before reading. Use finding_node_id for node details." +
	"Finally call update_finding_report(finding_id=finding_node_id, report, evidence_version=the version actually read) to save." +
	"evidence_version is required; otherwise the report is permanently marked as needing an update. Do not mix up the two ID types."

// 旧版触发消息(0.3.8 及更早)。只有仍与它逐字相同的记录才会被迁移覆盖，用户改过的保持原样。
const reporterToolCallMessageV1 = "上面刚有一个漏洞被 report_finding 登记。请从触发上下文里取出 finding_id" +
	"（工具返回 \"finding recorded: <id>\" 里的数字）与任务 id，按你的职责撰写该漏洞的详细报告，" +
	"最后调用 update_finding_report(finding_id, report) 保存。"

// upgradeReporterTriggerMessage 把老库里仍是默认文案的 reporter 触发消息刷成新版本。
// seedReporterAgent 受 reporter_agent_seed_v1 守卫且只在新建 agent 时写触发器，所以
// 升级上来的库拿不到新文案 —— 工具 schema 由 seedFindingTrafficTools 补齐了
// evidence_version，但没有任何东西告诉 reporter 去用它。一次性，且只覆盖未被改动的文案。
func (s *Server) upgradeReporterTriggerMessage() {
	const flag = "reporter_trigger_evidence_version_v1"
	if v, _, _ := s.m.pg.GetSetting(flag); v == "true" {
		return
	}
	defer func() { _ = s.m.pg.SetSetting(flag, "true") }() // 只尝试一次
	triggers, err := s.m.pg.ListTriggersFor("reporter")
	if err != nil {
		log.Printf("[reporter] failed to read triggers: %v", err)
		return
	}
	for _, t := range triggers {
		if !t.OnToolCall || t.ToolCallMessage != reporterToolCallMessageV1 {
			continue // 用户改过或不是 finding 触发器，不动。
		}
		t.ToolCallMessage = reporterToolCallMessage
		if err := s.m.pg.UpdateTrigger(t); err != nil {
			log.Printf("[reporter] failed to upgrade trigger message: %v", err)
			return
		}
		log.Printf("[reporter] trigger message upgraded to read and return evidence_version")
	}
}

const vulnDispatcherDescription = "Explicitly invoked after the Planner confirms the finding satisfies current execution-mode prerequisites: check constraints, deduplicate by asset, then dispatch or append an internal penetration testing subtask according to the finding."

const vulnDispatcherDescriptionV1 = "严重档立足点漏洞(getshell/rce/file_upload/deserialization × critical/high)自动派发深度子任务：finding 触发→约束检查→判定→去重→按 vulnclass/漏洞描述选场景模板 spawn_task。"

const vulnDispatcherFindingMessagePrefix = "The Planner confirmed this finding needs follow-up assessment: [vulnclass=__VULNCLASS__/severity=__SEVERITY__] __SUMMARY__. First read execution_mode from the explicit dispatch context:" +
	"For foothold or missing mode, assess dispatch only for getshell/webshell/reverse_shell with critical/high severity and evidence of a connectable WebShell, actual interactive command output, or an online C2/reverse session;" +
	"For vulnerability_only, reproducible confirmed rce/file_upload/deserialization/privesc/target_access with critical/high severity can justify an internal penetration testing subtask without a foothold."

// vulnDispatcherFindingMessageUnrestricted 仅用于识别回滚前的系统默认触发消息。
const vulnDispatcherFindingMessageUnrestricted = vulnDispatcherFindingMessagePrefix +
	"Do not dispatch other classes. Check parent-task context for deduplication as instructed."

// vulnDispatcherFindingMessage 保存显式派发时使用的消息模板；OnFinding 已默认关闭。
const vulnDispatcherFindingMessage = vulnDispatcherFindingMessagePrefix +
	"Do not dispatch other classes. Read parent constraints with list_task_constraints first (never derive directions explicitly forbidden by goal/deny), then deduplicate using parent-task context as instructed."

const vulnDispatcherFindingMessageV2 = "New finding: [vulnclass=__VULNCLASS__/severity=__SEVERITY__] __SUMMARY__. First read execution_mode from trigger context:" +
	"For foothold or missing mode, assess dispatch only for getshell/webshell/reverse_shell with critical/high severity and evidence of a connectable WebShell, actual interactive command output, or an online C2/reverse session;" +
	"For vulnerability_only, reproducible confirmed rce/file_upload/deserialization/privesc/target_access with critical/high severity can justify an internal penetration testing subtask without a foothold." +
	"Do not dispatch other classes. Check parent-task context for deduplication as instructed."

// vulnDispatcherFindingMessageV1 旧版触发消息（仅「内网渗透」单场景）。只有仍与它逐字相同的
// 触发器记录才会被 upgradeVulnDispatcherV2 迁移覆盖，用户改过的保持原样。
const vulnDispatcherFindingMessageV1 = "上面刚有漏洞被 report_finding 登记（上下文给了 [vulnclass/severity] 与摘要）。按你的判定纪律处理：" +
	"仅严重档(getshell/rce/file_upload/deserialization)且 critical/high 才允许派发内网渗透子任务，派发前必须做同立足点去重；其余一律不派，直接结束。"

// seedVulnDispatcherAgent 预置「漏洞派发」自定义 agent：Planner 确认 execution mode
// 证据前提后显式派发，Dispatcher 检查约束、按资产去重并新建或追加内网渗透子任务。
// 工具面收窄：只读工具 + spawn_task + 同血缘运行中子任务 Finding 追加。serial + by_task：同任务突发 finding 合并成
// 一次运行且串行执行，从调度层面缓解重复派发（阶段 1 的 L1 代码判定层落地前的事实上去抖）。
// 一次性(settings flag 守卫)：用户删掉后不再重建。
func (s *Server) seedVulnDispatcherAgent() {
	const flag = "vuln_dispatcher_seed_v1"
	if v, _, _ := s.m.pg.GetSetting(flag); v == "true" {
		return
	}
	defer func() { _ = s.m.pg.SetSetting(flag, "true") }() // 无论成功与否只尝试一次

	if exist, _ := s.m.pg.GetAgentByKey("vuln_dispatcher"); exist != nil {
		return // key 已被占用(用户手建过)——不覆盖
	}
	a, err := s.m.pg.CreateAgent("vuln_dispatcher", "Finding Dispatcher", vulnDispatcherDescription)
	if err != nil {
		log.Printf("[vuln_dispatcher] failed to create agent: %v", err)
		return
	}
	prompt := s.resolveVulnDispatcherPrompt(agent.VulnDispatcherDefaultPrompt)
	if err := s.m.pg.SeedPromptIfEmpty(a.ID, prompt); err != nil {
		log.Printf("[vuln_dispatcher] failed to seed prompt: %v", err)
	}
	if err := s.m.pg.SetAgentTriggerBehavior("vuln_dispatcher", "serial", "by_task", 0); err != nil {
		log.Printf("[vuln_dispatcher] failed to set trigger execution policy: %v", err)
	}
	// 最小工具面：只读 + 新建子任务 + 向同血缘运行中内网子任务补充 Finding。
	if err := s.m.pg.AddAgentToToolBinding("vuln_dispatcher", []string{
		"spawn_task", "check_internal_asset_task", "append_finding_to_task", "list_llm_profiles", "get_task_node_detail", "list_task_findings", "get_task_graph", "list_task_constraints",
	}); err != nil {
		log.Printf("[vuln_dispatcher] failed to bind tools: %v", err)
	}
	if _, err := s.m.pg.CreateTrigger(&db.AgentTrigger{
		AgentKey:       "vuln_dispatcher",
		Enabled:        true,
		OnFinding:      false,
		FindingMessage: vulnDispatcherFindingMessage,
	}); err != nil {
		log.Printf("[vuln_dispatcher] failed to create trigger: %v", err)
	}
	log.Printf("[vuln_dispatcher] preconfigured Finding Dispatcher agent and finding trigger")
}

// vulnDispatcherLegacyTemplateRefs 只用于兼容旧默认提示词；新提示词直接保存稳定 nkey。
var vulnDispatcherLegacyTemplateRefs = map[string]string{
	"内网渗透": "【TPL_ID_内网渗透】",
	"权限提升": "【TPL_ID_权限提升】",
	"漏洞利用": "【TPL_ID_漏洞利用】",
	"漏洞挖掘": "【TPL_ID_漏洞挖掘】",
	"代码审计": "【TPL_ID_代码审计】",
}

func (s *Server) resolveVulnDispatcherPrompt(tmpl string) string {
	prompt := tmpl
	for key, placeholder := range vulnDispatcherLegacyTemplateRefs {
		prompt = strings.ReplaceAll(prompt, "planner_template_id="+placeholder, "planner_template_nkey="+scenarioReference(key))
	}
	return prompt
}

// 【二开·P-INTENT·U22 存量库升级】起 —— upgradeVulnDispatcherV2：约束工具绑定 + 提示词 v3/v4 + 触发消息 v2（一次性）
// upgradeVulnDispatcherV2 存量库一次性升级（【二开·P-INTENT·U22】）：
//  1. 给 vuln_dispatcher 绑定 list_task_constraints（幂等）；
//  2. 提示词升级到 v3（约束检查+场景映射+审核建议），仅当当前提示词仍是旧血统（含特征句且无「审核建议」章节）
//     才替换，用户改过的版本不动（日志提示可手动「恢复默认」）；
//  3. finding 触发消息 v1→v2（仍与 v1 逐字相同才覆盖）。
func (s *Server) upgradeVulnDispatcherV2() {
	// 工具绑定必须独立于一次性提示词升级 flag：存量库即使早已完成 V4，
	// 仍需获得后续新增的运行中子任务 Finding 追加能力。
	if err := s.m.pg.AddAgentToToolBinding("vuln_dispatcher", []string{"list_task_constraints", "check_internal_asset_task", "append_finding_to_task"}); err != nil {
		log.Printf("[vuln_dispatcher] failed to bind read-only constraints/Finding append tools: %v", err)
	}
	const flag = "vuln_dispatcher_v4_review_by_human"
	if v, _, _ := s.m.pg.GetSetting(flag); v == "true" {
		return
	}
	defer func() { _ = s.m.pg.SetSetting(flag, "true") }() // 只尝试一次
	a, err := s.m.pg.GetAgentByKey("vuln_dispatcher")
	if err != nil || a == nil {
		return // 未启用该 agent，无活可干
	}
	if cur, cerr := s.m.pg.CurrentPrompt(a.ID); cerr == nil {
		switch {
		case strings.Contains(cur, "审核开关由人决定"):
			// 已是 v4 血统（审核由人决定版）
		case strings.Contains(cur, "漏洞派发 agent"):
			if _, uerr := s.m.pg.ResetPromptToDefault(a.ID, s.resolveVulnDispatcherPrompt(agent.VulnDispatcherDefaultPrompt)); uerr != nil {
				log.Printf("[vuln_dispatcher] prompt v3 upgrade failed: %v", uerr)
			} else {
				log.Printf("[vuln_dispatcher] prompt upgraded to v4 (operational constraint checks, five-scenario template mapping, human-controlled inherited review setting; one-time migration)")
			}
		default:
			log.Printf("[vuln_dispatcher] user-modified prompt; skipping v2 upgrade (use Restore defaults on the agent page to reset)")
		}
	}
	if triggers, terr := s.m.pg.ListTriggersFor("vuln_dispatcher"); terr == nil {
		for _, tg := range triggers {
			if !tg.OnFinding || tg.FindingMessage != vulnDispatcherFindingMessageV1 {
				continue // 用户改过或不是 finding 触发器，不动
			}
			tg.FindingMessage = vulnDispatcherFindingMessage
			if uerr := s.m.pg.UpdateTrigger(tg); uerr != nil {
				log.Printf("[vuln_dispatcher] failed to upgrade trigger message: %v", uerr)
			} else {
				log.Printf("[vuln_dispatcher] trigger message upgraded to v2")
			}
		}
	}
}

// 【二开·P-INTENT·U22 存量库升级】止

// 【二开·P-DISPATCH·阶段0②】upgradeVulnDispatcherV5 存量库一次性升级：同资产去重强化（用户反馈 m00837：
// 多个 finding 指向同一资产时派了多个内网渗透子任务）。仅当当前提示词仍是 v4 默认血统（含「审核开关由人决定」
// 且不含 v5 特征句「去重的键是」）才整段覆盖；用户改过的版本不动。
func (s *Server) upgradeVulnDispatcherV5() {
	const flag = "vuln_dispatcher_v5_asset_dedup"
	if v, _, _ := s.m.pg.GetSetting(flag); v == "true" {
		return
	}
	defer func() { _ = s.m.pg.SetSetting(flag, "true") }() // 只尝试一次
	a, err := s.m.pg.GetAgentByKey("vuln_dispatcher")
	if err != nil || a == nil {
		return // 未启用该 agent，无活可干
	}
	cur, cerr := s.m.pg.CurrentPrompt(a.ID)
	if cerr != nil {
		return
	}
	switch {
	case strings.Contains(cur, "去重的键是"):
		// 已是 v5 血统
	case strings.Contains(cur, "审核开关由人决定"):
		if _, uerr := s.m.pg.ResetPromptToDefault(a.ID, s.resolveVulnDispatcherPrompt(agent.VulnDispatcherDefaultPrompt)); uerr != nil {
			log.Printf("[vuln_dispatcher] prompt v5 upgrade failed: %v", uerr)
		} else {
			log.Printf("[vuln_dispatcher] prompt upgraded to v5 (asset-based deduplication, cross-run task-graph checks, combined dispatch for findings on the same asset)")
		}
	default:
		log.Printf("[vuln_dispatcher] user-modified prompt; skipping v5 upgrade (use Restore defaults on the agent page to reset)")
	}
}

// upgradeVulnDispatcherNKey 将默认血统的存量提示词迁移到稳定 nkey。
// 自定义血统不覆盖；旧 spawn_task 数字 ID 调用仍由工具层兼容。
func (s *Server) upgradeVulnDispatcherNKey() {
	const flag = "vuln_dispatcher_v6_template_nkey"
	if v, _, _ := s.m.pg.GetSetting(flag); v == "true" {
		return
	}
	defer func() { _ = s.m.pg.SetSetting(flag, "true") }()
	a, err := s.m.pg.GetAgentByKey("vuln_dispatcher")
	if err != nil || a == nil {
		return
	}
	cur, err := s.m.pg.CurrentPrompt(a.ID)
	if err != nil || (!strings.Contains(cur, "planner_template_id") && !strings.Contains(cur, "【TPL_ID_")) {
		return
	}
	if !strings.Contains(cur, "漏洞派发 agent") || !strings.Contains(cur, "审核开关由人决定") || !strings.Contains(cur, "去重的键是") {
		log.Printf("[vuln_dispatcher] prompt contains custom changes; skipping automatic nkey migration")
		return
	}
	if _, err := s.m.pg.ResetPromptToDefault(a.ID, agent.VulnDispatcherDefaultPrompt); err != nil {
		log.Printf("[vuln_dispatcher] nkey prompt migration failed: %v", err)
		return
	}
	log.Printf("[vuln_dispatcher] scenario templates migrated to stable nkeys")
}

// seedReporterAgent 预置一个「报告撰写」自定义 agent(builtin=false，可在 UI 编辑/删除)：
// 绑定 update_finding_report + 任务查询工具，并挂一个「report_finding 被调用即触发」的
// 触发器 —— 每登记一个漏洞就唤起它写详细报告。一次性(settings flag 守卫)：用户删掉后不再重建。
// 依赖：orchestration 工具已在本函数上方 SeedTool 入库，故绑定得上。
func (s *Server) seedReporterAgent() {
	const flag = "reporter_agent_seed_v1"
	if v, _, _ := s.m.pg.GetSetting(flag); v == "true" {
		return
	}
	defer func() { _ = s.m.pg.SetSetting(flag, "true") }() // 无论成功与否只尝试一次

	if exist, _ := s.m.pg.GetAgentByKey("reporter"); exist != nil {
		return // key 已被占用(用户手建过)——不覆盖
	}
	a, err := s.m.pg.CreateAgent("reporter", "Report Writer",
		"Detailed finding reports: triggered automatically when a finding is discovered; retrieves evidence and execution traces, writes a Markdown report, and saves it back.")
	if err != nil {
		log.Printf("[reporter] failed to create agent: %v", err)
		return
	}
	if err := s.m.pg.SeedPromptIfEmpty(a.ID, agent.ReporterDefaultPrompt); err != nil {
		log.Printf("[reporter] failed to seed prompt: %v", err)
	}
	// 触发运行策略：parallel + none —— 一漏洞一报告、多个 finding 并发各写各的。
	// merge 必须为 none：否则(默认 all)一波 finding 会被合并成一次运行，并行就没意义。
	// maxParallel=5：同时最多 5 个报告会话，避免瞬时太多 LLM 调用。
	if err := s.m.pg.SetAgentTriggerBehavior("reporter", "parallel", "none", 5); err != nil {
		log.Printf("[reporter] failed to set trigger execution policy: %v", err)
	}
	// 绑定它需要的工具：写报告 + 读证据/执行过程/态势。
	if err := s.m.pg.AddAgentToToolBinding("reporter", []string{
		"update_finding_report", "get_task_node_detail", "list_task_findings",
		"get_task_worker_trace", "list_task_worker_traces", "search_task_worker_traces",
		"get_task_graph",
	}); err != nil {
		log.Printf("[reporter] failed to bind tools: %v", err)
	}
	// 触发器：report_finding 被调用即触发（工具返回 "finding recorded: <id>" 带上 finding_id，
	// 任务 id 也在触发消息里）。
	if _, err := s.m.pg.CreateTrigger(&db.AgentTrigger{
		AgentKey:        "reporter",
		Enabled:         true,
		OnToolCall:      true,
		ToolNames:       []string{"report_finding"},
		ToolCallMessage: reporterToolCallMessage,
	}); err != nil {
		log.Printf("[reporter] failed to create trigger: %v", err)
	}
	log.Printf("[reporter] preconfigured Report Writer agent and finding trigger")
}

// seedAutoReportFindingBinding adds "auto" to report_finding's binding ONCE so
// conversation-context agents can call it without requiring an intent_id.
func (s *Server) seedAutoReportFindingBinding() {
	const flag = "auto_report_finding_v1"
	if v, _, _ := s.m.pg.GetSetting(flag); v == "true" {
		return
	}
	if err := s.m.pg.AddAgentToToolBinding("auto", []string{"report_finding"}); err != nil {
		log.Printf("[auto] failed to bind report_finding by default: %v", err)
		return
	}
	_ = s.m.pg.SetSetting(flag, "true")
}

// seedPlannerDefaultBindings adds "planner" to report_finding's binding ONCE
// (guarded by a settings flag), so existing DBs — whose report_finding row was
// seeded as worker-only — also let the planner record findings. Fresh DBs already
// get it via PlannerTools(); this only backfills without overriding a user unbind.
func (s *Server) seedPlannerDefaultBindings() {
	const flag = "planner_report_finding_v1"
	if v, _, _ := s.m.pg.GetSetting(flag); v == "true" {
		return
	}
	if err := s.m.pg.AddAgentToToolBinding("planner", []string{"report_finding"}); err != nil {
		log.Printf("[planner] failed to bind report_finding by default: %v", err)
		return
	}
	_ = s.m.pg.SetSetting(flag, "true")
}

// seedPlannerListAssetsBinding adds "planner" to list_assets's binding ONCE
// (guarded by a settings flag), so existing DBs — whose list_assets row was seeded
// as auto/pentest-only — also let the planner query the asset store by DSL. Fresh
// DBs already get it via PlannerTools(); this only backfills without overriding a
// user unbind.
func (s *Server) seedPlannerListAssetsBinding() {
	const flag = "planner_list_assets_v1"
	if v, _, _ := s.m.pg.GetSetting(flag); v == "true" {
		return
	}
	if err := s.m.pg.AddAgentToToolBinding("planner", []string{"list_assets"}); err != nil {
		log.Printf("[planner] failed to bind list_assets by default: %v", err)
		return
	}
	_ = s.m.pg.SetSetting(flag, "true")
}

// seedCompanyScopeRebind changes add_company_scope's default binding ONCE on
// existing DBs (guarded by a settings flag): the tool moves off worker and onto
// planner — defining a company's asset scope is a planning/main/auto concern, not
// something a worker does mid-exploration. Fresh DBs already get planner via
// PlannerTools() and lack worker via WorkerTools(); this only backfills old rows.
// One-shot + flag-guarded so a user who later re-binds worker isn't overridden.
func (s *Server) seedCompanyScopeRebind() {
	const flag = "company_scope_rebind_v1" // worker→planner 默认绑定切换
	if v, _, _ := s.m.pg.GetSetting(flag); v == "true" {
		return
	}
	if err := s.m.pg.AddAgentToToolBinding("planner", []string{"add_company_scope"}); err != nil {
		log.Printf("[planner] failed to bind add_company_scope by default: %v", err)
		return
	}
	if err := s.m.pg.RemoveAgentFromTool("worker", "add_company_scope"); err != nil {
		log.Printf("[worker] failed to unbind add_company_scope: %v", err)
		return
	}
	_ = s.m.pg.SetSetting(flag, "true")
}

// seedWorkerReadToolsUnbind strips the read-context tools off worker's default
// binding ONCE on existing DBs (guarded by a settings flag): a worker executes one
// intent and writes back — reading facts/companies and listing all workers' traces is
// a planning/main concern, not the executor's. Fresh DBs already lack these via
// WorkerTools(); this only backfills old rows without overriding a user who
// deliberately re-binds worker. Each RemoveAgentFromTool is per-tool +
// membership-guarded, so planner/mainagent bindings of the same tool are untouched.
//
// NOTE: search_all_worker_traces / get_worker_trace / node_detail are intentionally NOT
// unbound — worker owns them for cross-work look-back + node drill-down (see WorkerTools).
// They used to be in this list back when worker lacked them; seedWorkerReadbackRebind
// repairs DBs whose old run stripped them.
func (s *Server) seedWorkerReadToolsUnbind() {
	const flag = "worker_readtools_unbind_v1"
	if v, _, _ := s.m.pg.GetSetting(flag); v == "true" {
		return
	}
	for _, k := range []string{
		"list_facts", "list_companies", "list_worker_traces",
	} {
		if err := s.m.pg.RemoveAgentFromTool("worker", k); err != nil {
			log.Printf("[worker] failed to unbind %s from worker: %v", k, err)
			return // 出错则不落 flag，下次启动重试
		}
	}
	_ = s.m.pg.SetSetting(flag, "true")
}

// seedWorkerReadbackRebind re-binds the cross-work look-back / drill-down tools onto
// worker ONCE (guarded by a settings flag): an earlier seedWorkerReadToolsUnbind wrongly
// stripped search_all_worker_traces / get_worker_trace / node_detail from worker after
// they had been added to WorkerTools(), so any DB that ran that migration lost them.
// Fresh DBs already have them via WorkerTools() and this is a harmless no-op there.
// One-shot + flag-guarded so a user who later deliberately unbinds them isn't overridden.
func (s *Server) seedWorkerReadbackRebind() {
	const flag = "worker_readback_rebind_v2" // v2: 追加 node_detail
	if v, _, _ := s.m.pg.GetSetting(flag); v == "true" {
		return
	}
	if err := s.m.pg.AddAgentToToolBinding("worker", []string{
		"search_all_worker_traces", "get_worker_trace", "node_detail",
	}); err != nil {
		log.Printf("[worker] failed to bind history/detail tools: %v", err)
		return // 出错则不落 flag，下次启动重试
	}
	_ = s.m.pg.SetSetting(flag, "true")
}

// seedAutoDefaultBindings adds "auto" to the task-op + platform tools' bindings
// ONCE (guarded by a settings flag), so existing DBs whose tool rows were seeded
// before Auto existed still give Auto its default toolset — without re-adding it
// after a user deliberately unbinds.
func (s *Server) seedAutoDefaultBindings() {
	const flag = "auto_default_bindings_v3" // v3: 替换旧资产工具名，加入 insert_assets/add_company_scope
	if v, _, _ := s.m.pg.GetSetting(flag); v == "true" {
		return
	}
	keys := make([]string, 0, len(platformToolKeys)+12)
	for _, t := range s.orchestrationTools() {
		if t.Name() == "dispatch_finding_to_agent" {
			continue
		}
		keys = append(keys, t.Name())
	}
	keys = append(keys, platformToolKeys...)
	// 资产工具：Auto 操作平台常要看/登记资产、管理公司范围。
	keys = append(keys, "insert_assets", "add_company_scope", "list_assets")
	if err := s.m.pg.AddAgentToToolBinding("auto", keys); err != nil {
		log.Printf("[auto] default binding failed: %v", err)
		return
	}
	_ = s.m.pg.SetSetting(flag, "true")
}

// 【二开·P-ORCH·S8-6 spawn_task 约束关键词匹配】起 —— 父任务 goal 禁止行/deny 约束的关键词提取与匹配
func forbiddenKeywordOf(line string) string {
	line = strings.TrimSpace(strings.TrimLeft(line, "-·* ")) // 容忍列表符
	if !strings.HasPrefix(line, "禁止") {
		return ""
	}
	kw := strings.TrimSpace(strings.TrimPrefix(line, "禁止"))
	kw = strings.Trim(kw, "：:。；;，,") // "禁止内网渗透：…" → 取关键词
	if i := strings.IndexAny(kw, "：:。；;，, "); i > 0 {
		kw = kw[:i]
	}
	return kw
}

// matchForbiddenRule 父任务 goal 文本逐行扫，取「禁止」开头的行，关键词（"禁止"后的词）
// 在子任务 goal 里子串命中即返回该行原文；未命中返回 ""。
// 约束唯一权威载体 = 任务的 goal 文本（m01210：零新字段、无词表、无解析器）。
func (s *Server) matchForbiddenRule(parentRef, childGoal string) string {
	pt, ok := s.m.Task(parentRef) // 内存任务对象（含 Goal 字段）
	if !ok || pt == nil {
		return ""
	}
	for _, line := range strings.Split(pt.Goal, "\n") {
		if kw := forbiddenKeywordOf(line); kw != "" && strings.Contains(childGoal, kw) {
			return line // 命中：返回规则行原文（给 LLM/日志看）
		}
	}
	return ""
}

// 【二开·P-ORCH·S8-6 spawn_task 约束关键词匹配】止

// 【二开·P-INTENT·U22 操作约束硬闸门】起 —— 结构化 deny 约束的代码侧兜底（与 goal 文本禁止行互补）
// matchForbiddenConstraint 检查父任务 task_constraints 的 deny 行：含「禁止X」表述且
// 子任务 goal 命中 X → 返回约束原文；未命中返回 ""。与 matchForbiddenRule（goal 文本
// 禁止行）互补：deny 约束是结构化权威载体，本函数是代码侧兜底，LLM 判定不可绕过。
func (s *Server) matchForbiddenConstraint(parentRef, childGoal string) string {
	pt, ok := s.m.Task(parentRef)
	if !ok || pt == nil || pt.Store == nil {
		return ""
	}
	cons, err := pt.Store.ListConstraints()
	if err != nil {
		return ""
	}
	for _, c := range cons {
		if c.Kind != "deny" {
			continue
		}
		if kw := forbiddenKeywordOf(c.Text); kw != "" && strings.Contains(childGoal, kw) {
			return c.Text
		}
	}
	return ""
}

// 【二开·P-INTENT·U22 操作约束硬闸门】止

// 【二开·P-ORCH·S8-7 spawn_task 校验辅助清单】起 —— executor_key 与 planner 模板的可用清单（供校验失败文案）
// executorKeyList 列出可用的 executor_key（已启用、非内置、已声明），供校验失败文案。
func (s *Server) executorKeyList() string {
	agents, err := s.m.PG().ListAgents()
	if err != nil {
		return "(Failed to read list)"
	}
	var keys []string
	for _, a := range agents {
		if a.ExecutorKey != "" && a.Enabled && !a.Builtin {
			keys = append(keys, a.ExecutorKey)
		}
	}
	if len(keys) == 0 {
		return "(None; declare executor_key for a custom agent on the Agents page first)"
	}
	return strings.Join(keys, ", ")
}

// plannerTemplateList 列出可用模板（id=名称），供校验失败文案。
func (s *Server) plannerTemplateList() string {
	tpls, err := s.m.PG().ListPlannerTemplates()
	if err != nil {
		return "(Failed to read list)"
	}
	if len(tpls) == 0 {
		return "(Template library is empty)"
	}
	var items []string
	for _, t := range tpls {
		items = append(items, fmt.Sprintf("#%d=%s", t.ID, localizeScenarioName(t.Name)))
	}
	return strings.Join(items, ", ")
}

// 【二开·P-ORCH·S8-7 spawn_task 校验辅助清单】止
