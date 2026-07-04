package enricher

import "regexp"

// rules are applied in order; each match is replaced with RedactedPlaceholder.
// pattern-based — truly custom secret formats with no recognizable
// shape are undetectable by regex; that residual ceiling is inherent.
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
	// JSON-quoted values for common secret key names ("password": "hunter2")
	regexp.MustCompile(`(?i)"(?:api[-_]?key|secret|token|password|passwd|credential|auth[-_]?token)"\s*:\s*"[^"]*"`),
	// JWTs (three base64url segments starting with eyJ)
	regexp.MustCompile(`eyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]+`),
	// GitHub tokens (classic + fine-grained)
	regexp.MustCompile(`(?:gh[pousr]_[A-Za-z0-9]{36,}|github_pat_[A-Za-z0-9_]{22,})`),
	// Google API keys
	regexp.MustCompile(`AIza[0-9A-Za-z_-]{35}`),
	// Slack tokens
	regexp.MustCompile(`xox[baprs]-[A-Za-z0-9-]{10,}`),
	// AWS secret access keys assigned to a recognizable key name
	regexp.MustCompile(`(?i)aws[-_]?secret[-_]?access[-_]?key\s*[=:]\s*\S+`),
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
