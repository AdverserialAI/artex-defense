// tools_intent.go：意图修订与取消工具——update_intent 改稿（含乐观锁与自动重送审核）、cancel_intent 软删除。
// 主要函数/类型：
//   - updateIntent：修订 open/rejected 意图原稿，pending 一律拒绝，改后自动重送审核。
//   - cancelIntent：软删除 open/proposed 意图（running/paused 拒绝并引导改走 control）。
//   - intentIDVersionReq / validate：意图 id + expected_version 请求体及校验。
//   - mainAgentOpenOnly：限制 mainagent 只能改动 open 意图。
//   - claimExecutorIntent：自定义执行器领取工具（claim_executor_intent，原子领取匹配意图）。

package agent

// 【二开·审核流 PR4 意图修订/取消工具】
// update_intent / cancel_intent 工具（§5/§9）：
//   - update_intent：修订 open 意图或被驳回（rejected）意图原稿；pending（审核中）一律
//     拒绝；带 expected_version 乐观锁与产出锁；驳回稿改后自动重送审核（review_status
//     复位 pending）并唤醒 review 轮。
//   - cancel_intent：软删除 open/proposed 意图（running/paused 拒绝并引导 control_work）。
// 调用方策略（§5.4）：planner 可改/可取消 open+rejected(proposed)；mainagent(worker="human")
// 只动 open；review 对话两个工具都挂 ask 确认卡（seedReviewIntercept 内置规则）；worker 不注册。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"

	"artex/db"
	actool "norma/tool"
)

// intentIDVersionReq 是两个工具共用的必填参数：intent_id + expected_version。
// ExpectedVersion 用 *int 以区分「没填」（拒绝，强制模型先 list_intents 取版本）与「填 0」。
type intentIDVersionReq struct {
	IntentID        int64 `json:"intent_id"`
	ExpectedVersion *int  `json:"expected_version"`
}

func (r *intentIDVersionReq) validate() error {
	if r.IntentID <= 0 {
		return fmt.Errorf("intent_id is required and must be a positive integer")
	}
	if r.ExpectedVersion == nil {
		return fmt.Errorf("expected_version is required. Call list_intents and copy this intent's content_version exactly (optimistic locking prevents overwriting concurrent edits)")
	}
	return nil
}

// mainAgentOpenOnly mainagent（worker="human"）只允许动 open 意图（§5.4）：
// 驳回稿的修订权在 planner（revision_requested 事件驱动），人要走审核面板/对话。
// 前置预检（最终一致性仍由 db 层状态门禁+版本 CAS 保证）。
func (t *ToolSet) mainAgentOpenOnly(intentID int64) error {
	if t.worker != "human" {
		return nil
	}
	n, err := t.ts.GetNodeWithSources(intentID)
	if err != nil || n == nil {
		return fmt.Errorf("Intent #%d does not exist", intentID)
	}
	if n.State != "open" {
		return fmt.Errorf("mainagent can modify/cancel only open intents (#%d currently state=%s). Use the review panel for pending/rejected drafts and steer_work/control_work for executing intents", intentID, n.State)
	}
	return nil
}

// updateIntent 修订一条意图（§5/§6）。
func (t *ToolSet) updateIntent() actool.CoreTool {
	return t.writeExpTool("update_intent",
		"Revise an open or rejected intent. summary, priority, asset_ids, and target_executor can be changed; omitted fields remain unchanged. Pending or executing intents cannot be modified. expected_version is required (read content_version with list_intents first). Any modification to a rejected draft automatically resubmits it for review (review_status=pending). Intents with output cannot be changed; cancel them and create a new intent with add_intent or add_review_intent.",
		obj(map[string]any{
			"intent_id":        idp("ID of the intent to revise"),
			"expected_version": intp("Required: current content_version from list_intents (optimistic lock)"),
			"summary":          str("Optional: new intent description"),
			"priority":         intp("Optional: new priority, 0–10"),
			"asset_ids":        map[string]any{"type": "array", "items": map[string]any{"type": "integer"}, "description": "Optional: replace the complete target asset ID set (asset anchors are rebuilt)"},
			"target_executor":  str("Optional: new suggested executor_key (stored in pending_executor and applied on approval; empty clears the suggestion)"),
		}),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			var req struct {
				intentIDVersionReq
				Summary        *string  `json:"summary"`
				Priority       *int     `json:"priority"`
				AssetIDs       *[]int64 `json:"asset_ids"`
				TargetExecutor *string  `json:"target_executor"`
			}
			if err := json.Unmarshal(in, &req); err != nil {
				return actool.Errorf(fmt.Sprintf("invalid input: %v", err)), nil
			}
			if err := req.validate(); err != nil {
				return actool.Errorf(err.Error()), nil
			}
			if req.Summary == nil && req.Priority == nil && req.AssetIDs == nil && req.TargetExecutor == nil {
				return actool.Errorf("Provide at least one field to modify (summary/priority/asset_ids/target_executor)"), nil
			}
			if err := t.mainAgentOpenOnly(req.IntentID); err != nil {
				return actool.Errorf(err.Error()), nil
			}
			if req.TargetExecutor != nil && strings.TrimSpace(*req.TargetExecutor) != "" && t.checkExecutor != nil {
				te := strings.TrimSpace(*req.TargetExecutor)
				if err := t.checkExecutor(te); err != nil {
					return actool.Errorf(fmt.Sprintf("target_executor %q is unavailable: %v. Use a registered, enabled custom agent", te, err)), nil
				}
			}
			// 【审核-2 P1-4】修订 asset_ids 同样过资产范围拦截（与 createIntent 同口径）：
			// 否则范围外资产可经修订静默挂到意图锚点并交给 worker 执行。
			if req.AssetIDs != nil && len(*req.AssetIDs) > 0 && t.as != nil {
				if hits, err := t.as.CheckAssetsIntercept(t.taskID, *req.AssetIDs); err != nil {
					return actool.Errorf(fmt.Sprintf("Asset block-rule validation failed: %v", err)), nil
				} else if len(hits) > 0 {
					var b strings.Builder
					b.WriteString("Revised intent assets failed scope validation. Stop testing the affected assets: ")
					for _, h := range hits {
						fmt.Fprintf(&b, "\n - %s", h.Describe())
					}
					return actool.Errorf(b.String()), nil
				}
			}
			upd, err := t.ts.UpdateIntent(req.IntentID, *req.ExpectedVersion, db.IntentPatch{
				Summary: req.Summary, Priority: req.Priority,
				AssetIDs: req.AssetIDs, TargetExecutor: req.TargetExecutor,
			}, t.worker)
			if err != nil {
				switch {
				case errors.Is(err, db.ErrStaleVersion):
					return actool.Errorf(fmt.Sprintf("Version conflict: %v. Call list_intents for the latest content_version and retry", err)), nil
				case errors.Is(err, db.ErrIntentNotEditable), errors.Is(err, db.ErrIntentProduced),
					errors.Is(err, db.ErrNoChange), errors.Is(err, db.ErrIntentNotFound):
					return actool.Errorf(err.Error()), nil
				}
				return actool.Result{}, err
			}
			log.Printf("[intent] update_intent: intent #%d revised %v (review_reset=%v, by %s)", upd.ID, upd.Changed, upd.ReviewReset, t.worker)
			if upd.ReviewReset && t.notifyReview != nil {
				t.notifyReview(upd.ID) // 驳回稿重送 → 立即唤醒审核轮（§2.6）
			}
			return jsonResult(upd)
		})
}

// cancelIntent 取消（软删除）一条意图（§9.1）。
func (t *ToolSet) cancelIntent() actool.CoreTool {
	return t.writeExpTool("cancel_intent",
		"Soft-delete an open or proposed intent (state=deleted, preserving its node and lineage and recording delete_reason). Use it to abandon a pending intent, discard a rejected draft, or withdraw an intent submitted for review by mistake. expected_version is required (read content_version with list_intents). Running or paused intents cannot be cancelled; stop them with control_work first.",
		obj(map[string]any{
			"intent_id":        idp("ID of the intent to cancel"),
			"expected_version": intp("Required: current content_version from list_intents (optimistic lock)"),
			"reason":           str("Required: cancellation reason (saved in delete_reason and visible in audits)"),
		}),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			var req struct {
				intentIDVersionReq
				Reason string `json:"reason"`
			}
			if err := json.Unmarshal(in, &req); err != nil {
				return actool.Errorf(fmt.Sprintf("invalid input: %v", err)), nil
			}
			if err := req.validate(); err != nil {
				return actool.Errorf(err.Error()), nil
			}
			if err := t.mainAgentOpenOnly(req.IntentID); err != nil {
				return actool.Errorf(err.Error()), nil
			}
			// 【审核-2 P2-8】reason 必填（§11 PR4 规格）——取消是破坏性动作，理由必须可追溯；
			// db 侧的默认理由只作兜底，不再作为主要路径。
			if strings.TrimSpace(req.Reason) == "" {
				return actool.Errorf("reason is required (saved in delete_reason and visible in audits)"), nil
			}
			summary, err := t.ts.SoftDeleteIntentAt(req.IntentID, strings.TrimSpace(req.Reason), *req.ExpectedVersion)
			if err != nil {
				switch {
				case errors.Is(err, db.ErrStaleVersion):
					return actool.Errorf(fmt.Sprintf("Version conflict: %v. Call list_intents for the latest content_version and retry", err)), nil
				case errors.Is(err, db.ErrIntentNotEditable), errors.Is(err, db.ErrIntentNotFound):
					return actool.Errorf(err.Error()), nil
				}
				return actool.Result{}, err
			}
			log.Printf("[intent] cancel_intent: intent #%d soft-deleted (by %s, reason=%q)", req.IntentID, t.worker, req.Reason)
			if t.notifyHint != nil {
				// 取消改变 frontier 格局 → 唤醒 planner 重审目标达成判定。
				t.notifyHint([]string{fmt.Sprintf("Intent #%d (%s) cancelled: %s", req.IntentID, firstLine(summary, 60), req.Reason)})
			} else if t.notify != nil {
				t.notify()
			}
			return jsonResult(map[string]any{
				"intent_id": req.IntentID, "state": "deleted", "summary": summary,
			})
		})
}

// claimExecutorIntent 让当前自定义执行器直接领取一条匹配的 intent。
// executor_key 只从 ToolSet.executorKey 的服务端身份取得，模型不能指定其他执行器。
// 同一个 CAS 同时完成 proposed/open → running 和 owner=exec:<key>，确保不会重复领取。
func (t *ToolSet) claimExecutorIntent() actool.CoreTool {
	return t.writeExpTool("claim_executor_intent",
		"Custom executor claim: atomically change a proposed/open intent to running only when payload.executor exactly matches the current agent's executor_key. Callable only by a custom agent declaring executor_key or its internal exec:<key> identity; it bypasses the default Worker. Returns {claimed,state,owner,intent_id,reason}. Repeated claims, executor mismatches, and changed states return claimed=false without modifying the intent.",
		obj(map[string]any{
			"intent_id": idp("Exploration intent ID to claim (query list_intents first)"),
		}),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			executorKey := t.executorKey
			if executorKey == "" {
				return actool.Errorf("Current identity has no executor_key and cannot claim custom-executor intents"), nil
			}
			if !strings.HasPrefix(t.worker, "exec:") {
				if t.worker == "" || t.checkExecutor == nil {
					return actool.Errorf("Only custom executors can call claim_executor_intent"), nil
				}
				if err := t.checkExecutor(executorKey); err != nil {
					return actool.Errorf(fmt.Sprintf("Current Agent is not a valid custom executor: %v", err)), nil
				}
			}
			var req struct {
				IntentID int64 `json:"intent_id"`
			}
			if err := json.Unmarshal(in, &req); err != nil {
				return actool.Errorf(fmt.Sprintf("invalid input: %v", err)), nil
			}
			if req.IntentID <= 0 {
				return actool.Errorf("intent_id is required and must be a positive integer"), nil
			}
			n, err := t.ts.GetNode(req.IntentID)
			if err != nil {
				return actool.Result{}, err
			}
			if n == nil {
				return jsonResult(map[string]any{"claimed": false, "reason": "Intent does not exist or has been deleted"})
			}
			if n.State != "proposed" && n.State != "open" {
				return jsonResult(map[string]any{"claimed": false, "reason": "Intent is not currently proposed/open", "state": n.State})
			}
			var payload map[string]any
			if err := json.Unmarshal(n.Payload, &payload); err != nil {
				return jsonResult(map[string]any{"claimed": false, "reason": "Intent payload.executor is invalid"})
			}
			executor, _ := payload["executor"].(string)
			if executor == "" || executor == "review" || executor != executorKey {
				return jsonResult(map[string]any{"claimed": false, "reason": "payload.executor does not match the current executor", "expected_executor": executorKey, "actual_executor": executor})
			}
			owner := "exec:" + executorKey
			claimed, err := t.ts.ClaimExecutorIntent(n.ID, n.State, executorKey, owner)
			if err != nil {
				return actool.Result{}, err
			}
			if !claimed {
				return jsonResult(map[string]any{"claimed": false, "reason": "Claim conflict: state or executor changed"})
			}
			log.Printf("[executor] %s claimed intent #%d through claim_executor_intent: %s → running", t.worker, n.ID, n.State)
			return jsonResult(map[string]any{
				"claimed": true, "intent_id": n.ID, "state": "running", "owner": owner,
			})
		})
}
