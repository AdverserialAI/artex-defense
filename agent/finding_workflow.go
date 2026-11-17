// finding_workflow.go：finding 工作流工具改写——按开关给 finding 相关工具拼接流量绑定/编号约定指引。
// 主要函数/类型：
//   - FindingTrafficBindingEnabled / findingTrafficBindingEnabled：服务端注入的流量绑定总开关。
//   - findingWorkflowTools：按开关改写 finding 工具描述/schema，并生成系统提示层工作流指引。
//   - stripTrafficParameters：关闭绑定时从工具 schema 剔除流量绑定参数。
//   - HintTrafficSchema：hint 工具共享的 traffic_refs 参数 schema。
//   - findingRefsFromHint：读取 hint 的结构化流量引用并与显式引用合并。

package agent

import (
	"encoding/json"
	"fmt"

	"artex/db"
	actool "norma/tool"
)

const findingIDGuidance = `

**Finding ID convention**: finding_id is the independent finding record ID; finding_node_id is the exploration node ID. id in list_findings / list_task_findings / node_detail / get_task_node_detail remains the exploration node ID; read independent finding_id from that same response. get_finding_traffic / bind_finding_traffic use independent finding_id. Legacy update_finding_report still takes finding_node_id in its finding_id parameter. Do not pass report_finding's first-line number to evidence tools or guess other numbers after an ID error.`

// The server supplies the persisted setting. A missing setting/host is off.
// Consulted at assembly and again on writes so an already-running session
// cannot keep binding after the user switches the feature off.
var FindingTrafficBindingEnabled func() bool

func findingTrafficBindingEnabled() bool {
	return FindingTrafficBindingEnabled != nil && FindingTrafficBindingEnabled()
}

// Applied after ToolResolve: user descriptions and prompts remain intact, while
// all actual reporters (including Planner and custom chat agents) see the same
// API contract. Disabled/unbound tools are never reintroduced here.
func findingWorkflowTools(agentKey string, tools []actool.CoreTool) ([]actool.CoreTool, string) {
	if !findingTrafficBindingEnabled() {
		out := make([]actool.CoreTool, 0, len(tools))
		for _, tool := range tools {
			if tool.Name() == "bind_finding_traffic" {
				continue
			}
			if agentKey == "reporter" && (tool.Name() == "traffic_search" || tool.Name() == "traffic_get" || tool.Name() == "traffic_blob") {
				continue
			}
			switch tool.Name() {
			case "report_finding", "add_hint", "add_task_hint":
				// Work on a copy: toggling back on must restore the original schema.
				raw, _ := json.Marshal(tool.InputSchema())
				var schema map[string]any
				if json.Unmarshal(raw, &schema) == nil {
					stripTrafficParameters(schema)
					tool = DecorateTool(tool, tool.Description(), schema)
				}
			}
			out = append(out, tool)
		}
		return out, ""
	}
	out := append([]actool.CoreTool(nil), tools...)
	has := map[string]bool{}
	for i, tool := range out {
		has[tool.Name()] = true
		note := ""
		switch tool.Name() {
		case "report_finding":
			note = `
By default, the reporting Agent verifies and binds traffic before writing. Preserve verification commands, key output, genuine traffic IDs already known, and their roles in evidence for comparison with execution records; no extra searches are required just for binding. Explicit immediate binding remains supported: traffic_refs or evidence_hint_id can carry verified references, the latter from this task's hint. Any invalid reference makes the entire report submission fail. TCP/no-packet cases need no optional bindings. Returned finding_id and finding_node_id identify independent record and exploration node respectively.`
		case "add_hint", "add_task_hint":
			note = "\nWhen handing off confirmed findings, preserve verified traffic IDs, roles, notes, and order in the corresponding hint's traffic_refs (top level for one hint, matching hints entry in a batch). Explain the specific finding supported in text. Never discard existing references by handing off text alone. Unverified candidates are not evidence."
		case "get_finding_traffic", "bind_finding_traffic", "list_findings", "list_task_findings", "node_detail", "get_task_node_detail", "update_finding_report":
			note = findingIDGuidance
		}
		if note != "" {
			out[i] = DecorateTool(tool, tool.Description()+note, tool.InputSchema())
		}
	}
	guidance := ""
	if has["report_finding"] || has["add_task_hint"] || has["add_hint"] {
		guidance = `

**Optional traffic evidence handoff**: By default, the reporting Agent binds traffic after finding registration and before writing. Preserve verification commands, key output, genuine known traffic IDs/roles, and task intent_id in evidence for traceability; no extra searches are needed merely for binding. Auto/Planner reporting on behalf of a Worker must preserve its references. add_hint / add_task_hint support traffic_refs handoff; immediate report_finding traffic_refs / evidence_hint_id remain supported. Register TCP/no-traffic findings normally; never guess IDs or repeat probes merely to obtain packets.`
		if has["add_task_hint"] && !has["add_hint"] {
			guidance += "\nIn platform conversations without task context, do not call report_finding directly. Hand off through add_task_hint to the relevant existing task for its Agent to register, then verify with list_task_findings."
		}
		if has["prove_goal"] || has["goal_met"] {
			guidance += "\nComplete the reporting/handoff of existing evidence before declaring goals achieved. Do not end the task or cancel Workers merely because the textual finding was registered while its evidence handoff remains incomplete. No-packet cases do not require waiting or forced capture."
		}
	}
	if has["update_finding_report"] && has["bind_finding_traffic"] && has["get_finding_traffic"] {
		guidance += `

**Automatic traffic binding before reporting is enabled**: Verify and link traffic for the finding that triggered this run before writing its report. Obtain the independent finding_id and exploration finding_node_id from report_finding JSON or get_task_node_detail / list_task_findings. Read the finding, original evidence, originating intent trace, and existing get_finding_traffic bindings. Search by actual target, endpoint, timing, or distinctive body text; a matching domain/time alone is insufficient. Verify each candidate's full request/response with traffic_get: its method, path, parameters, and response must genuinely support this finding. Bind verified IDs with bind_finding_traffic, using the independent finding_id and ordered roles/notes. Re-read get_finding_traffic for the latest version and pass it as evidence_version to update_finding_report, whose finding_id still takes finding_node_id. If no genuine matching traffic exists (including TCP/non-HTTP or uncaptured cases), do not guess IDs, force bindings, or repeat probes just to obtain packets. Explain the absence in the report and use existing text/command evidence. Do not create duplicate findings or modify unrelated findings.`
	}
	if guidance != "" || has["get_finding_traffic"] || has["update_finding_report"] {
		guidance += findingIDGuidance
	}
	return out, guidance
}

func stripTrafficParameters(schema map[string]any) {
	props, _ := schema["properties"].(map[string]any)
	delete(props, "traffic_refs")
	delete(props, "evidence_hint_id")
	if required, ok := schema["required"].([]any); ok {
		kept := required[:0]
		for _, key := range required {
			if key != "traffic_refs" && key != "evidence_hint_id" {
				kept = append(kept, key)
			}
		}
		schema["required"] = kept
	}
	if hints, ok := props["hints"].(map[string]any); ok {
		if items, ok := hints["items"].(map[string]any); ok {
			stripTrafficParameters(items)
		}
	}
}

// HintTrafficSchema is shared by the task-local and cross-task hint tools.
func HintTrafficSchema() map[string]any {
	return map[string]any{"type": "array", "description": "Optional: verified traffic references supporting the specific finding in this hint, in order. After handoff, report_finding can use evidence_hint_id to carry these references", "items": obj(map[string]any{"traffic_id": str("Genuine traffic ID"), "role": str("baseline / proof / verification / supporting"), "note": str("Which conclusion this traffic supports")}, "traffic_id")}
}

func (t *ToolSet) findingRefsFromHint(hintID int64, explicit []db.TrafficRef) ([]db.TrafficRef, error) {
	if hintID <= 0 {
		return db.NormalizeTrafficRefs(explicit)
	}
	n, err := t.ts.GetNode(hintID) // local store only: inherited hints cannot supply evidence
	if err != nil {
		return nil, err
	}
	if n == nil || n.Kind != db.KindHint {
		return nil, fmt.Errorf("evidence_hint_id=%d must identify a hint in this task (inherited hints cannot be used directly for binding)", hintID)
	}
	var payload struct {
		Refs []db.TrafficRef `json:"traffic_refs"`
	}
	if err := json.Unmarshal(n.Payload, &payload); err != nil {
		return nil, err
	}
	return db.NormalizeTrafficRefs(append(append([]db.TrafficRef{}, explicit...), payload.Refs...))
}
