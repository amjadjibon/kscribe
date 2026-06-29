package enricher

import "regexp"

// rules are applied in order; each match is replaced with RedactedPlaceholder.
// ponytail: covers common patterns; deeply nested JSON/YAML values and custom
// secret formats are not detected — extend rules if scope grows.
var rules = []*regexp.Regexp{
	// Bearer tokens (Authorization: Bearer <token>)
	regexp.MustCompile(`(?i)bearer\s+[A-Za-z0-9\-._~+/]+=*`),
	// AWS-style access key IDs (AKIA/ASIA/AROA/AIDA prefixes)
	regexp.MustCompile(`(?:AKIA|ASIA|AROA|AIDA)[A-Z0-9]{16}`),
	// PEM private key blocks (multi-line, (?s) makes . match \n in RE2)
	regexp.MustCompile(`(?s)-----BEGIN [A-Z ]*PRIVATE KEY-----.*?-----END [A-Z ]*PRIVATE KEY-----`),
	// Database/service connection strings with embedded credentials
	regexp.MustCompile(`(?i)(?:postgres|postgresql|mysql|mongodb|redis)://[^:]+:[^@]+@\S+`),
	// Generic basic-auth URLs (scheme://user:pass@host)
	regexp.MustCompile(`(?i)[a-z][a-z0-9+\-.]*://[^:@\s/"']+:[^@\s/"']+@\S+`),
	// k=v or k: v patterns for common secret key names
	regexp.MustCompile(`(?i)(?:api[-_]?key|secret|token|password|passwd|credential|auth[-_]?token)\s*[=:]\s*\S+`),
}

// sensitiveEnvKey matches env var names that conventionally hold secrets.
var sensitiveEnvKey = regexp.MustCompile(`(?i)PASSWORD|PASSWD|SECRET|TOKEN|KEY|CREDENTIAL|AUTH|APIKEY|API_KEY`)

// Redact applies all rules to s and returns the sanitised string.
func Redact(s string) string {
	for _, r := range rules {
		s = r.ReplaceAllLiteralString(s, RedactedPlaceholder)
	}
	return s
}

// RedactSnapshot redacts all free-text fields in s in-place.
// This is called by EncodeSnapshot; callers that inspect the snapshot before
// serialization should call it manually first.
func RedactSnapshot(s *Snapshot) {
	s.Message = Redact(s.Message)
	for i := range s.PodContexts {
		pc := &s.PodContexts[i]
		for k, v := range pc.Annotations {
			pc.Annotations[k] = Redact(v)
		}
		for j := range pc.EnvVars {
			pc.EnvVars[j].Value = redactEnvValue(pc.EnvVars[j].Name, pc.EnvVars[j].Value)
		}
		for j := range pc.Logs {
			pc.Logs[j].Lines = Redact(pc.Logs[j].Lines)
		}
	}
	for i := range s.RelatedEvents {
		s.RelatedEvents[i].Message = Redact(s.RelatedEvents[i].Message)
	}
	for i := range s.NodeConditions {
		s.NodeConditions[i].Message = Redact(s.NodeConditions[i].Message)
	}
	if s.DeploymentStatus != nil {
		for i := range s.DeploymentStatus.Conditions {
			s.DeploymentStatus.Conditions[i] = Redact(s.DeploymentStatus.Conditions[i])
		}
	}
	if s.ReplicaSetStatus != nil {
		for i := range s.ReplicaSetStatus.Conditions {
			s.ReplicaSetStatus.Conditions[i] = Redact(s.ReplicaSetStatus.Conditions[i])
		}
	}
}

// redactEnvValue redacts the value if the key name suggests it holds a secret;
// otherwise runs the general Redact rules.
func redactEnvValue(name, value string) string {
	if sensitiveEnvKey.MatchString(name) {
		return RedactedPlaceholder
	}
	return Redact(value)
}
