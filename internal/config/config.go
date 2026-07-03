package config

import (
	"time"

	"github.com/caarlos0/env/v11"
)

// Config holds all runtime configuration for kscribe.
// Values are populated from KSCRIBE_* environment variables; Cobra flag
// overrides are applied after via ApplyFlags.
type Config struct {
	// Web server listen address.
	Addr string `env:"KSCRIBE_ADDR" envDefault:":8080"`

	// Kubernetes namespace the operator manages. Empty means cluster-wide.
	OperatorNamespace string `env:"KSCRIBE_OPERATOR_NAMESPACE" envDefault:""`

	// LeaderElect enables leader-election for the controller-manager.
	LeaderElect bool `env:"KSCRIBE_LEADER_ELECT" envDefault:"false"`

	// LLM settings.
	LLMProvider string `env:"KSCRIBE_LLM_PROVIDER" envDefault:"openai"`
	LLMModel    string `env:"KSCRIBE_LLM_MODEL"    envDefault:"gpt-4o-mini"`
	LLMBaseURL  string `env:"KSCRIBE_LLM_BASE_URL" envDefault:""`
	LLMAPIKey   string `env:"KSCRIBE_LLM_API_KEY"  envDefault:""`

	// MaxIterations caps the diagnosis loop depth.
	MaxIterations int `env:"KSCRIBE_MAX_ITERATIONS" envDefault:"5"`

	// DiagnosisConcurrency is the max parallel diagnosis goroutines.
	DiagnosisConcurrency int `env:"KSCRIBE_DIAGNOSIS_CONCURRENCY" envDefault:"4"`

	// EventReasonAllowlist is the set of Kubernetes event reasons kscribe
	// will act on. Comma-separated in env form.
	EventReasonAllowlist []string `env:"KSCRIBE_EVENT_REASON_ALLOWLIST" envSeparator:"," envDefault:"BackOff,OOMKilling,Failed,FailedScheduling"`

	// RedactEnabled is audit metadata only — redaction is always enforced by EncodeSnapshot
	// (SEC-001) and cannot be disabled via this flag. The value flows to prompt_redacted
	// in SQLite and to structured logs so operators can audit redaction posture.
	RedactEnabled bool `env:"KSCRIBE_REDACT_ENABLED" envDefault:"true"`

	// DBPath is the filesystem path for the SQLite state database.
	DBPath string `env:"KSCRIBE_DB_PATH" envDefault:"kscribe.db"`

	// RetentionPeriod is how long incidents, diagnoses, chat history, and
	// finished KscribeDiagnosis CRs are kept before pruning. 0 disables pruning.
	RetentionPeriod time.Duration `env:"KSCRIBE_RETENTION_PERIOD" envDefault:"720h"`

	// ResyncPeriod is how often the controller re-syncs watched resources.
	ResyncPeriod time.Duration `env:"KSCRIBE_RESYNC_PERIOD" envDefault:"10m"`
}

// Load parses Config from environment variables.
func Load() (Config, error) {
	var cfg Config
	if err := env.Parse(&cfg); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// FlagOverrides carries non-zero CLI flag values to apply on top of the
// env-derived Config. Zero values mean "not set by flag" and are skipped.
type FlagOverrides struct {
	Addr              string
	LeaderElect       *bool // pointer so we can distinguish false-from-flag vs not-set
	OperatorNamespace string
}

// ApplyFlags overwrites Config fields with any explicitly set flag values.
func (c *Config) ApplyFlags(f FlagOverrides) {
	if f.Addr != "" {
		c.Addr = f.Addr
	}
	if f.LeaderElect != nil {
		c.LeaderElect = *f.LeaderElect
	}
	if f.OperatorNamespace != "" {
		c.OperatorNamespace = f.OperatorNamespace
	}
}
