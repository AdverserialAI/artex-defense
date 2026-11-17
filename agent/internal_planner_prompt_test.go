package agent

import (
	"strings"
	"testing"
)

func TestInternalPlannerLowNoiseAndOptionalMCP(t *testing.T) {
	body := PlannerDomainSeeds()["内网渗透"]
	if body == "" {
		t.Fatal("内网渗透 Planner 模板不存在")
	}
	for _, unwanted := range []string{
		"Run whoami/hostname",
		"目标主机交互 shell",
		"指定网段覆盖",
	} {
		if strings.Contains(body, unwanted) {
			t.Errorf("内网 Planner 仍包含固定判据或命令：%q", unwanted)
		}
	}
	for _, required := range []string{
		"use actual Goal/intent/Worker evidence",
		"do not prescribe fixed whoami/hostname commands",
		"minimal low-noise operations",
		"WebShell/C2/MCP are optional",
		"plan other viable exploitation paths when MCP is unavailable",
		"Initial completion gate",
		"dispatch current-task reconnaissance first",
		"First dispatch current-task reconnaissance",
		"without fixed command requirements",
	} {
		if !strings.Contains(body, required) {
			t.Errorf("内网 Planner 缺少规则：%q", required)
		}
	}
}
