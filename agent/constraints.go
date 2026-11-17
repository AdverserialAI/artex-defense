// constraints.go：读取任务 allow/deny 操作约束并渲染 Planner/Worker 系统提示词块。
// 主要函数/类型：
//   - constraintBlock：读取任务约束并渲染为系统提示词块。
//   - renderConstraintBlock：把已加载的约束行渲染为 allow/deny 文本。

package agent

import (
	"strings"

	"artex/db"
)

// constraintBlock renders this task's operation constraints (task_constraints) as a
// high-priority block appended to the planner/worker system prompt. allow/deny are
// grouped; empty string when there are no constraints (or ts is nil). The framing
// deliberately puts these ABOVE the exploration/expansion heuristics so a declared
// boundary wins the tug-of-war against "chase another entry surface".
func constraintBlock(ts *db.ExplorationStore) string {
	if ts == nil {
		return ""
	}
	rows, err := ts.ListConstraints()
	if err != nil {
		return ""
	}
	return renderConstraintBlock(rows)
}

// renderConstraintBlock formats already-loaded constraints. Kept separate from the
// store lookup so the wording can be verified without a database.
func renderConstraintBlock(rows []db.Constraint) string {
	var allow, deny []string
	for _, c := range rows {
		text := strings.TrimSpace(c.Text)
		if text == "" {
			continue
		}
		if c.Kind == "allow" {
			allow = append(allow, "- "+text)
		} else {
			deny = append(deny, "- "+text)
		}
	}
	if len(allow) == 0 && len(deny) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\n[Operational constraints: highest priority, overriding all exploration/expansion heuristics below. Check every new intent and action against them first; do not proceed if a constraint would be violated]:")
	if len(allow) > 0 {
		b.WriteString("\nAllowed operations:\n")
		b.WriteString(strings.Join(allow, "\n"))
	}
	if len(deny) > 0 {
		b.WriteString("\nProhibited operations:\n")
		b.WriteString(strings.Join(deny, "\n"))
	}
	b.WriteString("\n(Discovering a new target, port, or host does not authorize testing it. Unless it falls within the allowed scope above, record an out-of-scope fact and skip it; do not derive intents or execute actions against it.)")
	return b.String()
}
