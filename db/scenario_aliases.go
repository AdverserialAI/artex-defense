package db

// scenarioLegacyKey resolves English references without rewriting stable keys
// in existing databases. Exact custom-key matches are checked by callers first.
func scenarioLegacyKey(key string) string {
	switch key {
	case "external-penetration-testing":
		return "\u5916\u7f51\u6e17\u900f"
	case "internal-penetration-testing":
		return "\u5185\u7f51\u6e17\u900f"
	case "privilege-escalation":
		return "\u6743\u9650\u63d0\u5347"
	case "vulnerability-exploitation":
		return "\u6f0f\u6d1e\u5229\u7528"
	case "vulnerability-research":
		return "\u6f0f\u6d1e\u6316\u6398"
	case "code-audit":
		return "\u4ee3\u7801\u5ba1\u8ba1"
	}
	return key
}
