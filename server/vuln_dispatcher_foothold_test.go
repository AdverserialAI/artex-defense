package server

import (
	"strings"
	"testing"

	"artex/agent"
)

// TestVulnDispatcherRequiresEstablishedFoothold 验证 dispatcher 的行为契约：
// foothold 模式必须有可继续操作的立足点证据；授权边界（goal 禁止行/deny constraint）必须先检查；
// 破坏性操作（data changes、DoS、persistent backdoors）始终禁止。只断言稳定的短契约标记，
// 不重新钉住整段措辞，避免把安全纪律的合法表述误判为回归。
func TestVulnDispatcherRequiresEstablishedFoothold(t *testing.T) {
	prompt := agent.VulnDispatcherDefaultPrompt
	for _, tc := range []struct {
		name string
		want string
	}{
		{name: "webshell foothold", want: "webshell: uploaded"},
		{name: "webshell verified", want: "accessible/connectable"},
		{name: "C2 foothold", want: "online C2/reverse session"},
		{name: "RCE alone insufficient", want: "RCE alone or an uploaded payload without a live session is insufficient"},
		{name: "no dispatch without foothold", want: "do not call spawn_task"},
		{name: "constraint check", want: "Check operational constraints before dispatch"},
		{name: "deny constraint gate", want: "deny constraint"},
		{name: "forbidden direction not dispatched", want: "do not create that subtask"},
		{name: "forbidden direction never derived", want: "never derive explicitly forbidden directions"},
		{name: "explicit authorization only", want: "No other hosts"},
		{name: "reachability is not authorization", want: "directly reachable/shared-subnet"},
		{name: "no self-relaxation", want: "or relax explicit prohibitions"},
		{name: "destructive ops forbidden", want: "No destructive"},
		{name: "data deletion forbidden", want: "data changes"},
		{name: "DoS forbidden", want: "DoS"},
		{name: "persistence backdoor forbidden", want: "persistent backdoors"},
	} {
		if !strings.Contains(prompt, tc.want) {
			t.Errorf("dispatcher prompt missing %s contract marker %q", tc.name, tc.want)
		}
	}
}
