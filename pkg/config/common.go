package config

import (
	"errors"
	"fmt"
	"slices"
)

// Common holds settings shared by every qStack binary. Embed it inline:
//
//	config.Common `yaml:",inline"`
type Common struct {
	// Service is the binary name used in logs, metrics and traces, e.g. "qs-iam".
	Service string `yaml:"service" env:"QS_SERVICE"`
	// AdminAddr serves /healthz, /readyz and /metrics.
	AdminAddr string    `yaml:"admin_addr" env:"QS_ADMIN_ADDR"`
	Log       Log       `yaml:"log"`
	Telemetry Telemetry `yaml:"telemetry"`
	// Features toggles preview functionality: features.<name>: true.
	Features map[string]bool `yaml:"features"`
}

// Log configures structured logging.
type Log struct {
	// Level is debug, info, warn or error.
	Level string `yaml:"level" env:"QS_LOG_LEVEL"`
}

// Telemetry configures tracing export.
type Telemetry struct {
	// OTLPEndpoint is the OTLP/gRPC collector address (host:port); empty disables export.
	OTLPEndpoint string `yaml:"otlp_endpoint" env:"QS_OTLP_ENDPOINT"`
	// OTLPInsecure disables TLS to the collector (in-cluster only).
	OTLPInsecure bool `yaml:"otlp_insecure" env:"QS_OTLP_INSECURE"`
	// SampleRatio is the fraction of new traces recorded, 0..1.
	SampleRatio float64 `yaml:"sample_ratio" env:"QS_TRACE_SAMPLE_RATIO"`
}

// DefaultCommon returns sensible defaults for a service.
func DefaultCommon(service string) Common {
	return Common{
		Service:   service,
		AdminAddr: ":9090",
		Log:       Log{Level: "info"},
		Telemetry: Telemetry{SampleRatio: 0.05},
	}
}

var logLevels = []string{"debug", "info", "warn", "error"}

// Validate checks the common settings.
func (c Common) Validate() error {
	var errs []error
	if c.Service == "" {
		errs = append(errs, errors.New("service is required"))
	}
	if c.AdminAddr == "" {
		errs = append(errs, errors.New("admin_addr is required"))
	}
	if !slices.Contains(logLevels, c.Log.Level) {
		errs = append(errs, fmt.Errorf("log.level %q must be one of %v", c.Log.Level, logLevels))
	}
	if c.Telemetry.SampleRatio < 0 || c.Telemetry.SampleRatio > 1 {
		errs = append(errs, fmt.Errorf("telemetry.sample_ratio %v must be within [0, 1]", c.Telemetry.SampleRatio))
	}
	return errors.Join(errs...)
}

// Feature reports whether a feature flag is enabled.
func (c Common) Feature(name string) bool { return c.Features[name] }
