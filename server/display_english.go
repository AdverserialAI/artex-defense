// display_english.go: English display localization for built-in task categories and intercept rules.
// 主要函数/类型：
//   - localizeScenarioName / localizeTaskCategory: translate built-in scenario display names without changing internal keys.
//   - localizeInterceptRule / localizeInterceptDetail: translate untouched built-in rule text and approval displays.
//   - localizeAssetInterceptRule: translate built-in asset-rule notes while preserving custom rules.

package server

import (
	"artex/db"
	"strings"
)

type englishDisplayText struct {
	legacy  string
	english string
}

var scenarioDisplayNames = map[string]string{
	"外网渗透": "External Penetration Testing",
	"内网渗透": "Internal Penetration Testing",
	"权限提升": "Privilege Escalation",
	"漏洞利用": "Vulnerability Exploitation",
	"漏洞挖掘": "Vulnerability Research",
	"代码审计": "Code Audit",
}

func localizeScenarioName(name string) string {
	if translated, ok := scenarioDisplayNames[name]; ok {
		return translated
	}
	return name
}

// scenarioReference exposes an English alias while keeping saved scenario keys stable.
func scenarioReference(key string) string {
	translated := localizeScenarioName(key)
	if translated == key {
		return key
	}
	return strings.ReplaceAll(strings.ToLower(translated), " ", "-")
}

func localizeTaskCategory(category *db.TaskCategory) {
	if category != nil {
		category.Name = localizeScenarioName(category.Name)
	}
}

func localizeTaskCategories(categories []*db.TaskCategory) {
	for _, category := range categories {
		localizeTaskCategory(category)
	}
}

var interceptRuleDisplayNames = map[string]string{
	"[内置] 递归强制删除 rm -rf":                                  "[Built-in] Recursive forced deletion (rm -rf)",
	"[内置] 删除系统关键目录":                                       "[Built-in] Delete critical system directories",
	"[内置] 磁盘格式化 mkfs":                                     "[Built-in] Disk formatting (mkfs)",
	"[内置] 覆写磁盘设备 dd":                                      "[Built-in] Overwrite disk devices (dd)",
	"[内置] Fork 炸弹":                                        "[Built-in] Fork bomb",
	"[内置] 关机 / 重启":                                        "[Built-in] Shutdown / reboot",
	"[内置] 杀死全部进程":                                         "[Built-in] Kill all processes",
	"[内置] 磁盘擦除 shred / wipe":                              "[Built-in] Disk wiping (shred / wipe)",
	"[内置] 清空防火墙规则":                                        "[Built-in] Flush firewall rules",
	"[内置] SQL DROP DATABASE / TABLE / SCHEMA":             "[Built-in] SQL DROP DATABASE / TABLE / SCHEMA",
	"[内置] SQL TRUNCATE":                                   "[Built-in] SQL TRUNCATE",
	"[内置] MongoDB drop / dropDatabase":                    "[Built-in] MongoDB drop / dropDatabase",
	"[内置] Redis FLUSHALL / FLUSHDB":                       "[Built-in] Redis FLUSHALL / FLUSHDB",
	"[内置] curl / wget 发送 DELETE 请求":                       "[Built-in] HTTP DELETE via curl / wget",
	"[内置] Python HTTP 客户端 DELETE（requests/httpx/aiohttp）": "[Built-in] HTTP DELETE via Python clients (requests/httpx/aiohttp)",
	"[内置] 脚本中声明 HTTP DELETE 方法（JS/通用）":                    "[Built-in] HTTP DELETE declared in scripts (JS/general)",
	"[内置] 批量清空 / 清除接口路径":                                  "[Built-in] Bulk clear / destructive endpoint paths",
	"[内置] 破坏性系统命令":                                        "[Built-in] Destructive system commands",
	"[内置] 数据外泄管道":                                         "[Built-in] Data exfiltration pipes",
	"[内置] 删除类接口路径":                                        "[Built-in] Destructive endpoint paths",
}

var interceptRuleDisplayMessages = map[string]string{
	"禁止执行递归强制删除（rm -rf / rm --recursive），可能永久损坏系统或靶机环境": "Recursive forced deletion (rm -rf / rm --recursive) is forbidden because it can permanently damage the system or target environment",
	"禁止删除系统关键路径":                                                 "Deleting critical system paths is forbidden",
	"禁止格式化磁盘（mkfs）":                                              "Formatting disks (mkfs) is forbidden",
	"禁止使用 dd 覆写磁盘设备":                                             "Using dd to overwrite disk devices is forbidden",
	"禁止执行 Fork 炸弹":                                               "Fork bombs are forbidden",
	"禁止执行关机或重启命令":                                                "Shutdown and reboot commands are forbidden",
	"禁止 kill -9 -1 或 killall -9（杀死所有进程）":                         "kill -9 -1 and killall -9 are forbidden because they kill all processes",
	"禁止对磁盘设备执行 shred/wipe 擦除":                                    "Using shred/wipe to erase disk devices is forbidden",
	"禁止清空防火墙规则（iptables -F / nft flush）":                         "Flushing firewall rules (iptables -F / nft flush) is forbidden",
	"禁止执行 DROP 操作，可能不可逆地销毁数据库对象":                                 "DROP operations are forbidden because they can irreversibly destroy database objects",
	"禁止执行 TRUNCATE，可能清空数据表所有数据":                                  "TRUNCATE is forbidden because it can delete all data from a table",
	"禁止执行 MongoDB drop 操作":                                       "MongoDB drop operations are forbidden",
	"禁止执行 Redis FLUSHALL / FLUSHDB，可能清空全部缓存数据":                   "Redis FLUSHALL / FLUSHDB is forbidden because it can delete all cached data",
	"禁止通过 curl/wget 发送 HTTP DELETE 请求，可能删除目标系统数据":                "Sending HTTP DELETE requests through curl/wget is forbidden because it can delete target-system data",
	"禁止使用 Python HTTP 客户端发送 DELETE 请求":                           "Sending DELETE requests through Python HTTP clients is forbidden",
	"禁止在脚本中声明并发送 HTTP DELETE 请求":                                 "Declaring and sending HTTP DELETE requests in scripts is forbidden",
	"禁止调用批量清空或销毁类接口（/clear /wipe /flush /purge 等）":               "Bulk-clear and destructive endpoints (/clear, /wipe, /flush, /purge, etc.) are forbidden",
	"破坏性命令被拒绝（rm -rf / / mkfs / dd / fork bomb / 关机重启 / 覆写磁盘设备）": "Destructive command denied (rm -rf /, mkfs, dd, fork bomb, shutdown/reboot, or disk-device overwrite)",
	"疑似数据外泄管道被拒绝（命令输出经 curl/wget/nc 外传）":                         "Suspected data-exfiltration pipe denied (command output sent externally through curl/wget/nc)",
	"禁止调用删除类接口（/delete /remove /unlink /erase 等），不论使用哪种 HTTP 方法——多数应用的删除接口用 GET/POST 就能触发，同样会真实删除目标数据": "Destructive endpoints (/delete, /remove, /unlink, /erase, etc.) are forbidden regardless of HTTP method; many applications trigger deletion through GET/POST, which can still permanently delete target data",
}

func localizeInterceptRule(rule *db.InterceptRule) {
	if rule == nil {
		return
	}
	if translated, ok := interceptRuleDisplayNames[rule.Name]; ok {
		rule.Name = translated
	}
	if translated, ok := interceptRuleDisplayMessages[rule.Message]; ok {
		rule.Message = translated
	}
}

func localizeInterceptRules(rules []db.InterceptRule) {
	for i := range rules {
		localizeInterceptRule(&rules[i])
	}
}

func localizeInterceptReason(reason string) string {
	if translated, ok := interceptRuleDisplayMessages[reason]; ok {
		return translated
	}
	return reason
}

func localizeInterceptPending(pending *db.InterceptPending) {
	if pending != nil {
		pending.Reason = localizeInterceptReason(pending.Reason)
	}
}

func localizeInterceptApprovalRow(row *db.InterceptApprovalRow) {
	if row == nil {
		return
	}
	localizeInterceptPending(&row.InterceptPending)
	if translated, ok := interceptRuleDisplayNames[row.RuleName]; ok {
		row.RuleName = translated
	}
}

func localizeInterceptApprovalRows(rows []db.InterceptApprovalRow) {
	for i := range rows {
		localizeInterceptApprovalRow(&rows[i])
	}
}

func localizeInterceptDetail(detail *db.InterceptDetail) {
	if detail == nil {
		return
	}
	localizeInterceptApprovalRow(&detail.InterceptApprovalRow)
	if detail.Audit != nil {
		detail.Audit.InitialReason = localizeInterceptReason(detail.Audit.InitialReason)
		detail.Audit.DecisionReason = localizeInterceptReason(detail.Audit.DecisionReason)
		if translated, ok := interceptRuleDisplayNames[detail.Audit.RuleName]; ok {
			detail.Audit.RuleName = translated
		}
	}
}

func localizeTaskArchive(archive *db.TaskArchive) {
	if archive != nil {
		archive.CategoryNameSnapshot = localizeScenarioName(archive.CategoryNameSnapshot)
	}
}

var assetInterceptDisplayNotes = map[string]string{
	"[内置] 政府网站 (.gov)":    "[Built-in] Government websites (.gov)",
	"[内置] 政府网站 (.gov.cn)": "[Built-in] Government websites (.gov.cn)",
	"[内置] 教育网站 (.edu)":    "[Built-in] Educational websites (.edu)",
	"[内置] 教育网站 (.edu.cn)": "[Built-in] Educational websites (.edu.cn)",
}

func localizeAssetInterceptRule(rule *db.AssetInterceptRule) {
	if rule == nil || !rule.Builtin {
		return
	}
	if translated, ok := assetInterceptDisplayNotes[rule.Note]; ok {
		rule.Note = translated
	}
}

func localizeAssetInterceptRules(rules []db.AssetInterceptRule) {
	for i := range rules {
		localizeAssetInterceptRule(&rules[i])
	}
}
