package config

import (
	"testing"
	"time"
)

func TestDefaults(t *testing.T) {
	// No env vars set — verify defaults.
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if cfg.Addr != ":8080" {
		t.Errorf("Addr: want :8080, got %q", cfg.Addr)
	}
	if cfg.LeaderElect {
		t.Error("LeaderElect: want false by default")
	}
	if cfg.MaxIterations != 5 {
		t.Errorf("MaxIterations: want 5, got %d", cfg.MaxIterations)
	}
	if cfg.DiagnosisConcurrency != 4 {
		t.Errorf("DiagnosisConcurrency: want 4, got %d", cfg.DiagnosisConcurrency)
	}
	if !cfg.RedactEnabled {
		t.Error("RedactEnabled: want true by default")
	}
	if cfg.DBPath != "kscribe.db" {
		t.Errorf("DBPath: want kscribe.db, got %q", cfg.DBPath)
	}
	if cfg.ResyncPeriod != 10*time.Minute {
		t.Errorf("ResyncPeriod: want 10m, got %v", cfg.ResyncPeriod)
	}
}

func TestEnvParsing(t *testing.T) {
	t.Setenv("KSCRIBE_ADDR", ":9090")
	t.Setenv("KSCRIBE_LEADER_ELECT", "true")
	t.Setenv("KSCRIBE_OPERATOR_NAMESPACE", "production")
	t.Setenv("KSCRIBE_MAX_ITERATIONS", "10")
	t.Setenv("KSCRIBE_DIAGNOSIS_CONCURRENCY", "8")
	t.Setenv("KSCRIBE_REDACT_ENABLED", "false")
	t.Setenv("KSCRIBE_DB_PATH", "/data/kscribe.db")
	t.Setenv("KSCRIBE_RESYNC_PERIOD", "30m")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	if cfg.Addr != ":9090" {
		t.Errorf("Addr: want :9090, got %q", cfg.Addr)
	}
	if !cfg.LeaderElect {
		t.Error("LeaderElect: want true")
	}
	if cfg.OperatorNamespace != "production" {
		t.Errorf("OperatorNamespace: want production, got %q", cfg.OperatorNamespace)
	}
	if cfg.MaxIterations != 10 {
		t.Errorf("MaxIterations: want 10, got %d", cfg.MaxIterations)
	}
	if cfg.DiagnosisConcurrency != 8 {
		t.Errorf("DiagnosisConcurrency: want 8, got %d", cfg.DiagnosisConcurrency)
	}
	if cfg.RedactEnabled {
		t.Error("RedactEnabled: want false")
	}
	if cfg.DBPath != "/data/kscribe.db" {
		t.Errorf("DBPath: want /data/kscribe.db, got %q", cfg.DBPath)
	}
	if cfg.ResyncPeriod != 30*time.Minute {
		t.Errorf("ResyncPeriod: want 30m, got %v", cfg.ResyncPeriod)
	}
}

func TestSliceParsing(t *testing.T) {
	t.Setenv("KSCRIBE_EVENT_REASON_ALLOWLIST", "BackOff,OOMKilling,CrashLoop")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	want := []string{"BackOff", "OOMKilling", "CrashLoop"}
	if len(cfg.EventReasonAllowlist) != len(want) {
		t.Fatalf("EventReasonAllowlist length: want %d, got %d", len(want), len(cfg.EventReasonAllowlist))
	}
	for i, v := range want {
		if cfg.EventReasonAllowlist[i] != v {
			t.Errorf("EventReasonAllowlist[%d]: want %q, got %q", i, v, cfg.EventReasonAllowlist[i])
		}
	}
}

func TestFlagOverrides(t *testing.T) {
	t.Setenv("KSCRIBE_ADDR", ":9090")
	t.Setenv("KSCRIBE_LEADER_ELECT", "false")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	leaderElect := true
	cfg.ApplyFlags(FlagOverrides{
		Addr:              ":7070",
		LeaderElect:       &leaderElect,
		OperatorNamespace: "staging",
	})

	if cfg.Addr != ":7070" {
		t.Errorf("Addr after flag override: want :7070, got %q", cfg.Addr)
	}
	if !cfg.LeaderElect {
		t.Error("LeaderElect after flag override: want true")
	}
	if cfg.OperatorNamespace != "staging" {
		t.Errorf("OperatorNamespace after flag override: want staging, got %q", cfg.OperatorNamespace)
	}
}

func TestFlagOverridesZeroValuesSkipped(t *testing.T) {
	t.Setenv("KSCRIBE_ADDR", ":9090")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	// Empty FlagOverrides — nothing should change.
	cfg.ApplyFlags(FlagOverrides{})

	if cfg.Addr != ":9090" {
		t.Errorf("Addr should be unchanged, got %q", cfg.Addr)
	}
}

func TestDuration(t *testing.T) {
	t.Setenv("KSCRIBE_RESYNC_PERIOD", "1h30m")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	want := 90 * time.Minute
	if cfg.ResyncPeriod != want {
		t.Errorf("ResyncPeriod: want %v, got %v", want, cfg.ResyncPeriod)
	}
}
