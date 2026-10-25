// Package config is the configuration of the Resource Manager binaries
// (spec 02 §14). Secrets — the database DSN, page-token keys, TLS keys —
// are files written by OpenBao (REQ-SEC-002), never values in the config.
package config

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/mindevis/qstack/pkg/config"
	"github.com/mindevis/qstack/pkg/db"
)

// Database configures the `rm` database.
type Database struct {
	// DSNFile holds the PostgreSQL DSN (with the password).
	DSNFile  string        `yaml:"dsn_file" env:"QS_DB_DSN_FILE"`
	MaxConns int32         `yaml:"max_conns" env:"QS_DB_MAX_CONNS"`
	Lifetime time.Duration `yaml:"max_conn_lifetime" env:"QS_DB_MAX_CONN_LIFETIME"`
}

// TLS is the service's mTLS identity (REQ-SEC-001).
type TLS struct {
	CertFile string `yaml:"cert_file" env:"QS_TLS_CERT_FILE"`
	KeyFile  string `yaml:"key_file" env:"QS_TLS_KEY_FILE"`
	CAFile   string `yaml:"ca_file" env:"QS_TLS_CA_FILE"`
}

// API is the configuration of qs-resource-manager-api.
type API struct {
	config.Common `yaml:",inline"`
	GRPCAddr      string   `yaml:"grpc_addr" env:"QS_GRPC_ADDR"`
	Database      Database `yaml:"database"`
	TLS           TLS      `yaml:"tls"`
	// AccessAddr is the gRPC target of iam.internal.v1.AccessService
	// (qs-authz-cache), e.g. dns:///qs-authz-cache:9090.
	AccessAddr string `yaml:"access_addr" env:"QS_ACCESS_ADDR"`
	// PageTokenKeyFiles hold the HMAC keys of page tokens; the first signs.
	PageTokenKeyFiles []string `yaml:"page_token_key_files"`
	// Migrate applies the database migrations on start (single replica,
	// L0); otherwise a migration job does (T-1.67).
	Migrate bool `yaml:"migrate" env:"QS_MIGRATE"`
}

// DefaultAPI returns the defaults of the API binary.
func DefaultAPI() API {
	return API{Common: config.DefaultCommon("qs-resource-manager-api"), GRPCAddr: ":8080"}
}

// Validate implements config.Validator.
func (c *API) Validate() error {
	errs := []error{c.Common.Validate()}
	if c.GRPCAddr == "" {
		errs = append(errs, errors.New("grpc_addr is required"))
	}
	if c.Database.DSNFile == "" {
		errs = append(errs, errors.New("database.dsn_file is required"))
	}
	if c.AccessAddr == "" {
		errs = append(errs, errors.New("access_addr is required"))
	}
	if c.TLS.CertFile == "" || c.TLS.KeyFile == "" || c.TLS.CAFile == "" {
		errs = append(errs, errors.New("tls.cert_file, tls.key_file and tls.ca_file are required"))
	}
	if len(c.PageTokenKeyFiles) == 0 {
		errs = append(errs, errors.New("page_token_key_files is required"))
	}
	return errors.Join(errs...)
}

// Controller is the configuration of qs-resource-manager-controller.
type Controller struct {
	config.Common `yaml:",inline"`
	Database      Database `yaml:"database"`
	// NATSURL is the NATS server, e.g. nats://nats:4222.
	NATSURL string `yaml:"nats_url" env:"QS_NATS_URL"`
	// StreamReplicas is 3 in the ha profile, 1 in single.
	StreamReplicas int `yaml:"stream_replicas" env:"QS_STREAM_REPLICAS"`
}

// DefaultController returns the defaults of the controller binary.
func DefaultController() Controller {
	return Controller{Common: config.DefaultCommon("qs-resource-manager-controller"), StreamReplicas: 1}
}

// Validate implements config.Validator.
func (c *Controller) Validate() error {
	errs := []error{c.Common.Validate()}
	if c.Database.DSNFile == "" {
		errs = append(errs, errors.New("database.dsn_file is required"))
	}
	if c.NATSURL == "" {
		errs = append(errs, errors.New("nats_url is required"))
	}
	if c.StreamReplicas != 1 && c.StreamReplicas != 3 {
		errs = append(errs, errors.New("stream_replicas must be 1 or 3"))
	}
	return errors.Join(errs...)
}

// DB returns the pool configuration; it reads the DSN file.
func (d Database) DB(app string) (db.Config, error) {
	dsn, err := readSecret(d.DSNFile)
	if err != nil {
		return db.Config{}, err
	}
	return db.Config{DSN: dsn, ApplicationName: app, MaxConns: d.MaxConns, MaxConnLifetime: d.Lifetime}, nil
}

// PageKeys reads the page-token keys.
func (c *API) PageKeys() ([][]byte, error) {
	keys := make([][]byte, len(c.PageTokenKeyFiles))
	for i, f := range c.PageTokenKeyFiles {
		k, err := readSecret(f)
		if err != nil {
			return nil, err
		}
		keys[i] = []byte(k)
	}
	return keys, nil
}

// ClientTLS returns the mTLS client configuration (TLS 1.3).
func (t TLS) ClientTLS() (*tls.Config, error) {
	cert, err := tls.LoadX509KeyPair(t.CertFile, t.KeyFile)
	if err != nil {
		return nil, fmt.Errorf("tls: load key pair: %w", err)
	}
	ca, err := os.ReadFile(t.CAFile)
	if err != nil {
		return nil, fmt.Errorf("tls: read CA: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(ca) {
		return nil, errors.New("tls: no CA certificates in ca_file")
	}
	return &tls.Config{Certificates: []tls.Certificate{cert}, RootCAs: pool, MinVersion: tls.VersionTLS13}, nil
}

// ServerTLS returns the mTLS server configuration (TLS 1.3, client
// certificate required).
func (t TLS) ServerTLS() (*tls.Config, error) {
	c, err := t.ClientTLS()
	if err != nil {
		return nil, err
	}
	return &tls.Config{
		Certificates: c.Certificates, ClientCAs: c.RootCAs,
		ClientAuth: tls.RequireAndVerifyClientCert, MinVersion: tls.VersionTLS13,
	}, nil
}

// readSecret reads a secret file; errors name the file, never the content.
func readSecret(path string) (string, error) {
	b, err := os.ReadFile(path) //nolint:gosec // the path comes from the operator's config
	if err != nil {
		return "", fmt.Errorf("read secret file %s: %w", path, err)
	}
	s := strings.TrimSpace(string(b))
	if s == "" {
		return "", fmt.Errorf("secret file %s is empty", path)
	}
	return s, nil
}
