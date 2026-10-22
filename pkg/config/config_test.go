package config

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

type testConfig struct {
	Common      `yaml:",inline"`
	DatabaseDSN string        `yaml:"database_dsn" env:"QS_DATABASE_DSN"`
	Replicas    int           `yaml:"replicas" env:"QS_REPLICAS"`
	Shards      uint16        `yaml:"shards" env:"QS_SHARDS"`
	Timeout     time.Duration `yaml:"timeout" env:"QS_TIMEOUT"`
	Zones       []string      `yaml:"zones" env:"QS_ZONES"`
	Ratio       float64       `yaml:"ratio" env:"QS_RATIO"`
	Strict      bool          `yaml:"strict" env:"QS_STRICT"`
	NoEnv       string        `yaml:"no_env"`
}

func (c testConfig) Validate() error {
	if err := c.Common.Validate(); err != nil {
		return err
	}
	if c.DatabaseDSN == "" {
		return errors.New("database_dsn is required")
	}
	return nil
}

func writeFile(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func env(m map[string]string) func(string) (string, bool) {
	return func(k string) (string, bool) { v, ok := m[k]; return v, ok }
}

func defaults() *testConfig {
	return &testConfig{Common: DefaultCommon("qs-test"), Replicas: 1}
}

// Spec 02 §14: file, then env overrides, then validation.
func TestLoadFileThenEnv(t *testing.T) {
	path := writeFile(t, `
service: qs-iam
log:
  level: debug
telemetry:
  otlp_endpoint: otel:4317
features:
  preview_x: true
database_dsn: postgres://file
replicas: 3
timeout: 5s
zones: [a, b]
no_env: from-file
`)
	cfg := defaults()
	err := LoadWith(path, cfg, env(map[string]string{
		"QS_DATABASE_DSN": "postgres://env",
		"QS_ZONES":        "ru-a, ru-b,,ru-c",
		"QS_SHARDS":       "16",
		"QS_RATIO":        "0.25",
		"QS_STRICT":       "true",
		"QS_LOG_LEVEL":    "warn",
		"QS_TIMEOUT":      "1m30s",
	}))
	if err != nil {
		t.Fatal(err)
	}
	checks := []struct {
		name      string
		got, want any
	}{
		{"service (file)", cfg.Service, "qs-iam"},
		{"admin addr (default)", cfg.AdminAddr, ":9090"},
		{"log level (env over file)", cfg.Log.Level, "warn"},
		{"otlp (file)", cfg.Telemetry.OTLPEndpoint, "otel:4317"},
		{"sample ratio (default)", cfg.Telemetry.SampleRatio, 0.05},
		{"dsn (env over file)", cfg.DatabaseDSN, "postgres://env"},
		{"replicas (file)", cfg.Replicas, 3},
		{"shards (env)", cfg.Shards, uint16(16)},
		{"timeout (env)", cfg.Timeout, 90 * time.Second},
		{"ratio (env)", cfg.Ratio, 0.25},
		{"strict (env)", cfg.Strict, true},
		{"no_env (file)", cfg.NoEnv, "from-file"},
		{"feature", cfg.Feature("preview_x"), true},
		{"unknown feature", cfg.Feature("other"), false},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s = %v, want %v", c.name, c.got, c.want)
		}
	}
	if !slices.Equal(cfg.Zones, []string{"ru-a", "ru-b", "ru-c"}) {
		t.Errorf("zones = %v", cfg.Zones)
	}
}

func TestLoadErrors(t *testing.T) {
	tests := []struct {
		name    string
		file    string
		env     map[string]string
		wantSub string
	}{
		{"unknown key", "database_dsn: x\ndatabse_dsn: typo\n", nil, "databse_dsn"},
		// The YAML decoder reports the line, not the key.
		{"bad yaml type", "database_dsn: x\nreplicas: many\n", nil, "line 2"},
		{"bad env int", "database_dsn: x\n", map[string]string{"QS_REPLICAS": "lots"}, "QS_REPLICAS"},
		{"env overflow", "database_dsn: x\n", map[string]string{"QS_SHARDS": "70000"}, "QS_SHARDS"},
		{"bad env duration", "database_dsn: x\n", map[string]string{"QS_TIMEOUT": "soon"}, "QS_TIMEOUT"},
		{"validation", "service: qs-x\n", nil, "database_dsn is required"},
		{"common validation", "database_dsn: x\nlog: {level: loud}\n", nil, "log.level"},
		{"sample ratio", "database_dsn: x\ntelemetry: {sample_ratio: 2}\n", nil, "sample_ratio"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := LoadWith(writeFile(t, tt.file), defaults(), env(tt.env))
			if err == nil || !strings.Contains(err.Error(), tt.wantSub) {
				t.Errorf("err = %v, want it to mention %q", err, tt.wantSub)
			}
		})
	}
}

func TestLoadWithoutFile(t *testing.T) {
	cfg := defaults()
	if err := LoadWith("", cfg, env(map[string]string{"QS_DATABASE_DSN": "postgres://env"})); err != nil {
		t.Fatal(err)
	}
	if cfg.DatabaseDSN != "postgres://env" || cfg.Service != "qs-test" {
		t.Errorf("cfg = %+v", cfg)
	}
}

func TestLoadEmptyFileKeepsDefaults(t *testing.T) {
	cfg := defaults()
	if err := LoadWith(writeFile(t, ""), cfg, env(map[string]string{"QS_DATABASE_DSN": "x"})); err != nil {
		t.Fatal(err)
	}
	if cfg.Replicas != 1 {
		t.Errorf("replicas = %d, want default 1", cfg.Replicas)
	}
}

func TestLoadRejectsNonPointer(t *testing.T) {
	if err := LoadWith("", testConfig{}, env(nil)); err == nil {
		t.Error("want error for non-pointer dst")
	}
	if err := LoadWith("", new(int), env(nil)); err == nil {
		t.Error("want error for pointer to non-struct")
	}
}

func TestLoadMissingFile(t *testing.T) {
	if err := LoadWith(filepath.Join(t.TempDir(), "absent.yaml"), defaults(), env(nil)); err == nil {
		t.Error("want error for missing file")
	}
}
