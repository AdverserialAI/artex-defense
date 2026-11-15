package defense

import (
	"regexp"
	"strings"
)

const redactedValue = "[REDACTED]"

var secretPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)(-----BEGIN (?:RSA |EC |OPENSSH |PGP )?PRIVATE KEY-----)[\s\S]*?(-----END (?:RSA |EC |OPENSSH |PGP )?PRIVATE KEY-----)`),
	regexp.MustCompile(`(?i)\b((?:api[_-]?key|access[_-]?token|auth[_-]?token|client[_-]?secret|password|passwd|secret|private[_-]?key)\s*[:=]\s*["']?)([^\s,"'}]+)`),
	regexp.MustCompile(`(?i)\b(authorization\s*[:=]\s*["']?(?:bearer|basic)\s+)([a-z0-9._~+/=-]+)`),
	regexp.MustCompile(`\b(AKIA|ASIA)[A-Z0-9]{16}\b`),
	regexp.MustCompile(`\bgh[pousr]_[A-Za-z0-9_]{20,}\b`),
	regexp.MustCompile(`\bgithub_pat_[A-Za-z0-9_]{20,}\b`),
	regexp.MustCompile(`\bxox[baprs]-[A-Za-z0-9-]{10,}\b`),
	regexp.MustCompile(`\b(sk-(?:live|test|proj)-)[A-Za-z0-9_-]{12,}\b`),
	regexp.MustCompile(`(?i)\b([a-z][a-z0-9+.-]*://[^\s:/@]+:)([^\s/@]+)(@)`),
}

func RedactSecrets(input string) (string, bool) {
	out := input
	for _, pattern := range secretPatterns {
		out = pattern.ReplaceAllStringFunc(out, func(match string) string {
			groups := pattern.FindStringSubmatch(match)
			if len(groups) > 3 && groups[3] == "@" {
				return pattern.ReplaceAllString(match, "${1}"+redactedValue+"${3}")
			}
			if len(groups) > 2 && groups[2] != "" {
				return pattern.ReplaceAllString(match, "${1}"+redactedValue)
			}
			return redactedValue
		})
	}
	return out, out != input
}

func redactEvidence(input string) string {
	out, _ := RedactSecrets(input)
	return strings.TrimSpace(out)
}
