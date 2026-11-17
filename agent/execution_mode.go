// execution_mode.go：任务执行模式识别——从目标文本判定 foothold / 仅漏洞路径并产出提示词约束。
// 主要函数/类型：
//   - ExecutionMode：执行模式类型与内置常量（foothold / vulnerability_only）。
//   - ExecutionModeFromGoal：识别显式的"仅漏洞路径"意图，Shell/立足点要求优先。
//   - goalExecutionModeSpec / plannerExecutionModeSpec：goal/planner 侧仅漏洞路径提示词选择。
//   - GoalVulnerabilityOnlySpec / PlannerVulnerabilityOnlySpec：代码持有的模式规约文案。

package agent

import "strings"

// ExecutionMode controls how a confirmed finding is interpreted by Goal,
// Planner and vuln_dispatcher. The zero/default mode preserves the original
// foothold-gated dispatcher behavior.
type ExecutionMode string

const (
	ExecutionModeFoothold          ExecutionMode = "foothold"
	ExecutionModeVulnerabilityOnly ExecutionMode = "vulnerability_only"
)

// ExecutionModeFromGoal recognizes only explicit vulnerability-only intent.
// Generic vulnerability validation is not enough: "验证 RCE" still uses the
// default mode, while "只验证 RCE，不需要 getshell" opts in. An explicit Shell
// or foothold request takes precedence when both kinds of intent are present.
func ExecutionModeFromGoal(text string) ExecutionMode {
	normalized := strings.ToLower(strings.NewReplacer(
		" ", "", "\t", "", "\n", "", "\r", "",
		"-", "", "_", "", "—", "", "–", "",
	).Replace(text))
	positive := normalized
	for _, negated := range []string{
		"不需要getshell", "无需getshell", "不要getshell",
		"不需要shell", "无需shell", "不要shell", "不建立shell",
	} {
		positive = strings.ReplaceAll(positive, negated, "")
	}
	for _, marker := range []string{
		"需要getshell", "需要shell", "建立立足点", "获得立足点",
		"反弹shell", "交互shell", "c2上线", "session上线",
		"footholdrequired", "interactiveshell", "reverseshell",
	} {
		if strings.Contains(positive, marker) {
			return ExecutionModeFoothold
		}
	}
	for _, marker := range []string{
		"[执行模式:仅漏洞路径]", "执行模式:仅漏洞路径", "仅漏洞路径",
		"只验证漏洞", "仅验证漏洞", "只复现漏洞", "仅复现漏洞",
		"只做漏洞验证", "仅做漏洞验证", "只确认漏洞", "仅确认漏洞",
		"不需要getshell", "无需getshell", "不要getshell",
		"不需要shell", "无需shell", "不要shell", "不建立shell",
		"[executionmode:vulnerabilityonly]", "vulnerabilityonly", "vulnerabilityvalidationonly", "poconly",
		"proofofconceptonly", "noshellrequired", "donotestablishshell",
	} {
		if strings.Contains(normalized, marker) {
			return ExecutionModeVulnerabilityOnly
		}
	}
	return ExecutionModeFoothold
}

func goalExecutionModeSpec(goal, description string) string {
	if ExecutionModeFromGoal(goal+"\n"+description) == ExecutionModeVulnerabilityOnly {
		return GoalVulnerabilityOnlySpec
	}
	return ""
}

// GoalVulnerabilityOnlySpec is code-owned prompt context. It is appended only
// for an explicitly recognized vulnerability-only task, so old/custom prompt
// templates do not need to duplicate this contract.
const GoalVulnerabilityOnlySpec = `

**Execution mode: vulnerability only (identified; highest-priority semantic constraint)**
- This task requires only confirmation, reproduction, or explanation of the specified vulnerability and its boundaries. Do not add getshell, WebShell, C2, persistence, internal movement, or host-control goals.
- Use verifiable acceptance evidence appropriate to the type: actual command/code execution for RCE, stored/accessed/parsed file evidence for upload, and corresponding reproducible evidence for other vulnerabilities.
- RCE, file upload, deserialization, and privilege escalation can themselves be valid final outcomes; do not require a foothold.
- Leave this mode only if the user also explicitly requires a Shell/foothold. The current classification indicates no such requirement.`

// plannerExecutionModeSpec returns the vulnerability-only prompt block for
// goals recognized as vulnerability-only, or "" for foothold tasks.
func plannerExecutionModeSpec(goal string) string {
	if ExecutionModeFromGoal(goal) == ExecutionModeVulnerabilityOnly {
		return PlannerVulnerabilityOnlySpec
	}
	return ""
}

// PlannerVulnerabilityOnlySpec keeps planning focused on vulnerability
// evidence rather than weaponization or a mandatory foothold.
const PlannerVulnerabilityOnlySpec = `

**Execution mode: vulnerability only (identified by code from the task goal)**
- Plan only the minimum steps needed to confirm, reproduce, and establish boundaries of the vulnerability specified in Goal. Do not make Shell, WebShell, C2, persistence, or internal movement a goal/prerequisite.
- Accept reproducible measured evidence for that vulnerability. Once Goal is confirmed, complete it through prove_goal without adding weaponization/foothold intents.
- RCE output, stored/accessed uploads, and demonstrated deserialization chains may be final results for the current Goal; do not misstate them as C2/interactive sessions.
- Additional useful directions must remain within the current vulnerability-only goal; never expand the task automatically.`
