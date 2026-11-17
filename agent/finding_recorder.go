// finding_recorder.go：finding 上报侧接线——recorder 接口、注入方法与流量证据指引文案。
// 主要函数/类型：
//   - FindingRecorder：report_finding 落库时的上报/登记回调接口。
//   - findingTrafficGuidance：report_finding 流量证据可选绑定的模型指引文案。
//   - SetFindingRecorder（ToolSet / Worker / Planner / MainAgent）：向各 agent 注入 recorder。

package agent

import (
	"context"

	"artex/db"
)

// FindingRecorder is injected by the host; agents never synthesize or copy
// evidence bodies themselves. Its implementation owns the atomic write.
type FindingRecorder interface {
	Record(context.Context, db.RecordFindingInput, []db.TrafficRef) (*db.RecordedFinding, error)
}

// Tool-use guidance is appended without replacing the user's editable prompt.
// It does not require capture or claim that unavailable traffic tools exist.
const findingTrafficGuidance = `

**Optional finding traffic evidence**: When reporting a finding, use traffic_refs to link genuine HTTP request/response IDs in reproduction order only after viewing and verifying they support the conclusion. Domain/time are candidate filters, not proof of association. For TCP/non-HTTP, uncaptured, or no exact match, omit or use []; retain command output/logs/other verifiable evidence and optionally explain the absence. Never guess IDs or rescan merely to obtain packets.`

func (t *ToolSet) SetFindingRecorder(r FindingRecorder)   { t.findingRecorder = r }
func (w *Worker) SetFindingRecorder(r FindingRecorder)    { w.findingRecorder = r }
func (p *Planner) SetFindingRecorder(r FindingRecorder)   { p.findingRecorder = r }
func (m *MainAgent) SetFindingRecorder(r FindingRecorder) { m.findingRecorder = r }
