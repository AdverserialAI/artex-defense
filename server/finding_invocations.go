// finding_invocations.go：Planner 显式派发 Finding 给自定义 Agent 的通用工具与统一入队路径。
// 主要函数/类型：
//   - toolDispatchFindingToAgent：dispatch_finding_to_agent 工具，校验 Finding 归属与目标 Agent 后记录派发。
//   - enqueueFindingInvocation：初始工具调用与持久化重试扫描共享的统一入队路径，入队前重新校验可变状态。

package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"artex/agent"
	"artex/db"
	actool "norma/tool"
)

func (s *Server) toolDispatchFindingToAgent() actool.CoreTool {
	desc := "Explicitly dispatch a Finding from the current Planner task to an existing, enabled, non-built-in custom Agent. finding_id is the independent finding record ID (findings.id), not the exploration node ID finding_node_id. Each agent_key+finding_id records one invocation; already_invoked returns the first conversation ID, and retry_scheduled means no conversation has been created and fallback delivery is pending. Neither enqueued nor retry_scheduled means the downstream Agent has run or completed. The caller's system prompt defines the scenario's allowed targets and evidence prerequisites."
	schema := objSchema(map[string]any{
		"agent_key":  strParam("Target custom Agent key"),
		"finding_id": map[string]any{"type": "integer", "description": "Independent finding record ID in the current task (findings.id)"},
		"reason":     strParam("Evidence prerequisites confirmed by the Planner and reason for this dispatch"),
	}, "agent_key", "finding_id", "reason")
	return wrTool("dispatch_finding_to_agent", desc, schema, func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
		ri := agent.RunInfoFrom(ctx)
		if ri.TaskID == 0 {
			return actool.Errorf("Missing Planner task context"), nil
		}
		var args struct {
			AgentKey  string          `json:"agent_key"`
			FindingID json.RawMessage `json:"finding_id"`
			Reason    string          `json:"reason"`
		}
		if err := json.Unmarshal(in, &args); err != nil {
			return actool.Errorf(err.Error()), nil
		}
		args.AgentKey = strings.TrimSpace(args.AgentKey)
		args.Reason = strings.TrimSpace(args.Reason)
		findingID := parseProfileID(args.FindingID)
		if args.AgentKey == "" || findingID <= 0 || args.Reason == "" {
			return actool.Errorf("agent_key, a valid finding_id, and a nonempty reason are required"), nil
		}
		finding, err := s.m.pg.GetFinding(findingID)
		if err != nil {
			return actool.Errorf("Failed to read Finding: " + err.Error()), nil
		}
		if finding == nil {
			return actool.Errorf("Finding does not exist"), nil
		}
		if finding.TaskID == nil || *finding.TaskID != ri.TaskID {
			return actool.Errorf("Finding does not belong to the current Planner task"), nil
		}
		target, err := s.m.pg.GetAgentByKey(args.AgentKey)
		if err != nil {
			return actool.Errorf("Failed to read target Agent: " + err.Error()), nil
		}
		if target == nil {
			return actool.Errorf("Target Agent does not exist"), nil
		}
		if !target.Enabled {
			return actool.Errorf("Target Agent is disabled"), nil
		}
		if target.Builtin {
			return actool.Errorf("Target Agent must not be built-in"), nil
		}
		inv, inserted, err := s.m.pg.RecordFindingInvocation(args.AgentKey, findingID, ri.TaskID, args.Reason)
		if err != nil {
			return actool.Errorf("Failed to record Finding dispatch: " + err.Error()), nil
		}
		if !inserted {
			if inv.ConversationID != nil {
				return jsonResult(map[string]any{
					"status": "already_invoked", "agent_key": inv.AgentKey,
					"finding_id": inv.FindingID, "conversation_id": *inv.ConversationID,
				})
			}
			return jsonResult(map[string]any{
				"status": "retry_scheduled", "agent_key": inv.AgentKey, "finding_id": inv.FindingID,
			})
		}
		accepted, err := s.enqueueFindingInvocation(inv)
		if err != nil {
			return actool.Errorf(err.Error()), nil
		}
		status := "retry_scheduled"
		if accepted {
			status = "enqueued"
		}
		return jsonResult(map[string]any{
			"status": status, "agent_key": inv.AgentKey, "finding_id": inv.FindingID,
		})
	})
}

// enqueueFindingInvocation is shared by the initial tool call and the durable
// retry sweep. It revalidates all mutable state before using the trigger queue.
func (s *Server) enqueueFindingInvocation(inv db.FindingInvocation) (bool, error) {
	if s.m.pg == nil {
		return false, errors.New("Database is not initialized")
	}
	finding, err := s.m.pg.GetFinding(inv.FindingID)
	if err != nil {
		return false, fmt.Errorf("Failed to read Finding: %w", err)
	}
	if finding == nil {
		return false, errors.New("Finding does not exist")
	}
	if finding.TaskID == nil || *finding.TaskID != inv.TaskID {
		return false, errors.New("Finding and invocation record belong to different tasks")
	}
	task, err := s.m.pg.GetTask(inv.TaskID)
	if err != nil {
		return false, fmt.Errorf("Failed to read task: %w", err)
	}
	if task == nil {
		return false, errors.New("Task does not exist")
	}
	target, err := s.m.pg.GetAgentByKey(inv.AgentKey)
	if err != nil {
		return false, fmt.Errorf("Failed to read target Agent: %w", err)
	}
	if target == nil {
		return false, errors.New("Target Agent does not exist")
	}
	if !target.Enabled {
		return false, errors.New("Target Agent is disabled")
	}
	if target.Builtin {
		return false, errors.New("Target Agent must not be built-in")
	}
	triggers, err := s.m.pg.ListTriggersFor(inv.AgentKey)
	if err != nil {
		return false, fmt.Errorf("Failed to read target Agent trigger configuration: %w", err)
	}
	triggerMessage := ""
	for _, tr := range triggers {
		if strings.TrimSpace(tr.FindingMessage) != "" {
			triggerMessage = tr.FindingMessage
			break
		}
	}
	triggerMessage = strings.NewReplacer(
		"__VULNCLASS__", finding.VulnClass,
		"__SEVERITY__", finding.Severity,
		"__SUMMARY__", finding.Summary,
	).Replace(triggerMessage)
	assetIDs, _ := json.Marshal(finding.AssetIDs)
	mode := agent.ExecutionModeFromGoal(task.Goal + "\n" + task.Description)
	var message strings.Builder
	if strings.TrimSpace(triggerMessage) != "" {
		message.WriteString(triggerMessage)
		message.WriteString("\n\n")
	}
	fmt.Fprintf(&message, "[Explicit dispatch after the Planner confirmed handling prerequisites]\nexecution_mode: %s\nFindingID: %d\nVulnClass: %s\nSeverity: %s\nSummary: %s\nAssetIDs: %s\nEvidenceVersion: %d\nPlannerReason: %s\nFindingEvidence: %s",
		mode, finding.ID, finding.VulnClass, finding.Severity, trunc(finding.Summary, 1000), assetIDs,
		finding.EvidenceVersion, trunc(inv.Reason, 2000), trunc(finding.Evidence, 12000))
	title := fmt.Sprintf("Planner finding dispatch · task#%d · finding#%d", inv.TaskID, inv.FindingID)
	return s.startTriggeredRun(inv.AgentKey, title, message.String(), inv.TaskID, true, task.Description, task.Goal,
		[]findingInvocationRef{{agentKey: inv.AgentKey, findingID: inv.FindingID}}), nil
}
