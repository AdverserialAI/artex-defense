// worker_domain_templates.go：Worker 场景化领域提示词种子——为各渗透场景生成完整可离线编辑的提示词。
// 主要函数/类型：
//   - WorkerDomainSeed：一条内置 Worker 领域提示词（nkey + 正文）。
//   - WorkerDomainSeeds：汇总公共安全/证据规则，生成全部场景的内置提示词。

package agent

import "strings"

// WorkerDomainSeed is a built-in, fully materialized Worker prompt for one scenario.
type WorkerDomainSeed struct {
	NKey string `json:"nkey"`
	Body string `json:"body"`
}

// workerDomainBaseTmpl excludes the external-pentest-only foothold dispatch
// section before materializing the other scenario templates.
func workerDomainBaseTmpl() string {
	const sectionStart = "\n\n[Foothold evidence]"
	const sectionEnd = "\n\nAfter completing this intent"
	start := strings.Index(workerDefaultTmpl, sectionStart)
	end := strings.Index(workerDefaultTmpl, sectionEnd)
	if start >= 0 && end > start {
		return workerDefaultTmpl[:start] + workerDefaultTmpl[end:]
	}
	return workerDefaultTmpl
}

// WorkerDomainSeeds keeps the common Worker safety/evidence rules in one place.
// The stored template is still a complete prompt, so it remains editable offline.
func WorkerDomainSeeds() []WorkerDomainSeed {
	sections := map[string]string{
		"内网渗透": `## Current scenario: Internal Penetration Testing
- Start only from the parent's registered foothold and explicitly authorized scope. Identify hosts, interfaces, routes, credentials, and reachability before selecting exploitation.
- For each lateral action, state source host, target host, protocol, and evidence source. Direct reachability is not authorization.
- Test only addresses explicitly listed in goal or allow constraints. New interfaces/routes/networks permit recording local configuration and evidence only; do not scan/test other addresses until a human explicitly expands authorization.
- Record reusable credentials, configuration, and topology with record_fact. Preserve stream_id and tool_call_id for relevant HTTP/proxy traffic.
- After confirming interfaces/routes/container networks/session links, save evidence with record_fact and provide topology: discovery describes the method in at most 20 characters (for example 'nmap: port 445', 'ARP scan', 'Docker bridge'), not the conclusion; src_ip is parent/foothold IP; dst_ips are internal target IPs sharing method/parent/kind; kind=solid for confirmed vulnerability/successful lateral movement or reachable for reachability only (default). Different methods or parents require separate facts. Facts automatically create topology edges. Without evidence, keep independent nodes; never infer links merely from task membership, shared subnet, or an entry point.
- After obtaining control of a new host, register getshell, webshell, or target_access according to actual evidence. An open port is not a foothold.`,
		"权限提升": `## Current scenario: Privilege Escalation
- Start from verified low privileges. Record user, host, privilege boundary, and credentials, then inspect configuration, tokens, services, and known escalation paths.
- Prefer readable, reversible, low-noise checks. No data destruction, DoS, disabling audit, or persistent backdoors.
- Prove privilege changes by comparing original and new contexts. Sensitive configuration without gained privileges is information disclosure, not target_access.
- Register target_access only after actually obtaining a higher-privilege account or execution context; use getshell when a reverse/command Shell also exists.`,
		"漏洞利用": `## Current scenario: Vulnerability Exploitation
- Establish target, vulnerability prerequisites, affected component, and authorization before designing a minimal PoC. Record requests, tools, versions, and observations.
- Prove control and impact before expanding exploitation. Stop after obtaining required evidence; do not destroy data or enlarge impact for demonstration.
- Uploaded Web backdoors require verified access/connection and Godzilla registration; file-write success alone is not webshell.
- Link results to original traffic or tool calls. Preserve exclusion evidence on failure; scanner alerts alone are not findings.`,
		"漏洞挖掘": `## Current scenario: Vulnerability Research
- Propose a few falsifiable hypotheses around the current goal. Vary input, identity, state, or protocol conditions independently and preserve controls.
- Cover related inputs, neighboring endpoints, and roles without drifting into unrestricted Internet-wide scans.
- Exclude cache, misconfiguration, frontend presentation, and accidental differences before confirming reproducibility and real impact.
- Only verified findings go to report_finding; record research, excluded paths, and unconfirmed hypotheses with record_fact.`,
		"代码审计": `## Current scenario: Code Audit
- Identify entries, sources, sensitive sinks, authentication/filtering chains, and actual call reachability. Keyword matches alone are not findings.
- Record files, functions, key statements, and control flow. Provide minimal runtime verification and corresponding traffic where possible; explicitly identify evidence gaps otherwise.
- Distinguish theoretical source-to-sink paths, exploitable paths, and verified vulnerabilities. Register findings only after meeting the task evidence threshold; record other audit conclusions as facts.
- Do not modify target source or execute unknown build-script commands. Respect authorization and Worker evidence discipline before tests/dynamic validation.`,
	}
	order := []string{"内网渗透", "权限提升", "漏洞利用", "漏洞挖掘", "代码审计"}
	seeds := make([]WorkerDomainSeed, 0, len(order))
	for _, name := range order {
		body := strings.Replace(workerDomainBaseTmpl(), "**Record discoveries immediately**", sections[name]+"\n\n**Record discoveries immediately**", 1)
		seeds = append(seeds, WorkerDomainSeed{NKey: name, Body: body})
	}
	return seeds
}
