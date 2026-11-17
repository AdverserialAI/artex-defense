// worker_topology_fact_migration.go：旧 record_topology_relation 规则 → record_fact topology 规则的一次性迁移。
// 主要函数/类型：
//   - internalWorkerTopologyFactRule：替换后的内网渗透拓扑 fact 规则行（拓扑边由 fact 自动派生）。
//   - upgradeWorkerTopologyFactPrompt：刷内置模板、任务提示词快照与遗留基础快照，幂等只跑一次。

package server

import (
	"fmt"
	"strings"

	"artex/db"
)

// workerTopologyFactMigrationKey 幂等标记：旧 record_topology_relation 规则 →
// record_fact topology 字段规则，只刷一次。v2 增加遗留基础快照整体替换。
const workerTopologyFactMigrationKey = "worker_topology_fact_edge_v2"

// internalWorkerTopologyFactRule 替换后的内网渗透规则：拓扑关系由 record_fact 的
// topology 字段承载（discovery/src_ip/dst_ips/kind），拓扑边由 fact 自动派生——
// record_topology_relation 工具已删除。与 agent/worker_domain_templates.go 的规则行保持一致。
const internalWorkerTopologyFactRule = `- After confirming network interfaces, routes, container networks, or session connections, save evidence with record_fact and describe the relationship in topology: discovery=how it was discovered (brief method, such as "nmap found port 445 open", "ARP scan found a live host", or "inferred from a Docker bridge", not a conclusion); src_ip=parent node (foothold); dst_ips=target internal IPs (batch only targets sharing the same method, parent, and kind); kind=solid (a vulnerability was found or lateral movement succeeded) | reachable (reachability only, default). Use separate facts for different discovery methods or parents. Topology edges are derived automatically from facts. Without evidence, keep nodes independent; never infer a connection merely from a shared task, subnet, or entry point.`

// upgradeWorkerTopologyFactPrompt 把内置「内网渗透」worker 模板和已存在任务的
// worker 提示词快照里的旧 record_topology_relation 规则替换为 fact topology 规则。
// 自定义模板（builtin=false）不动；幂等，只跑一次。
func upgradeWorkerTopologyFactPrompt(pg *db.DB) error {
	_, ok, err := pg.GetSetting(workerTopologyFactMigrationKey)
	if err != nil || ok {
		return err
	}
	oldRule := strings.TrimPrefix(internalWorkerTopologyRule, "\n")
	if _, err := pg.Exec(`UPDATE worker_templates SET body=replace(body,$1,$2),updated_at=now()
		WHERE builtin AND nkey='内网渗透' AND body LIKE '%record_topology_relation%'`,
		oldRule, internalWorkerTopologyFactRule); err != nil {
		return fmt.Errorf("upgrade worker topology fact prompt: %w", err)
	}
	// 已存在任务的提示词快照一并刷新（tasks.worker_prompt_override）。
	if _, err := pg.Exec(`UPDATE tasks SET worker_prompt_override=replace(worker_prompt_override,$1,$2)
		WHERE worker_prompt_override LIKE '%record_topology_relation%'`,
		oldRule, internalWorkerTopologyFactRule); err != nil {
		return fmt.Errorf("upgrade task worker prompt snapshots: %w", err)
	}
	// 遗留任务快照：创建时只快照了基础模板（含「**边发现边写回**」锚点但没有场景段）
	// 的内网渗透任务，worker 运行时以快照为准，永远看不到域规则——整体替换为当前内置
	// 内网渗透模板（与新任务创建时快照的内容一致，此时模板已在上方刷成新规则）。
	if _, err := pg.Exec(`UPDATE tasks t SET worker_prompt_override = wt.body
		FROM task_categories c, worker_templates wt
		WHERE t.category_id = c.id AND c.nkey = '内网渗透'
		  AND wt.id = (SELECT id FROM worker_templates WHERE builtin AND nkey='内网渗透' ORDER BY id LIMIT 1)
		  AND t.worker_prompt_override LIKE '%**边发现边写回**%'
		  AND t.worker_prompt_override NOT LIKE '%当前场景%'`); err != nil {
		return fmt.Errorf("replace legacy task worker snapshots: %w", err)
	}
	return pg.SetSetting(workerTopologyFactMigrationKey, "done")
}
