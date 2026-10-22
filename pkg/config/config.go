// Package config loads service configuration (spec 02 §14): a YAML file whose
// unknown keys are rejected, then QS_* environment overrides declared with
// `env` struct tags, then validation.
//
//	type Config struct {
//		config.Common `yaml:",inline"`
//		DatabaseDSN string `yaml:"database_dsn" env:"QS_DATABASE_DSN"`
//	}
//
// Defaults are whatever dst holds before Load is called.
package config

import (
	"errors"
	"fmt"
	"io"
	"os"
	"reflect"
	"strconv"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

// Validator is implemented by configs that check themselves after loading.
type Validator interface {
	Validate() error
}

// Load reads path (skipped when empty), applies environment overrides and
// calls Validate if dst implements Validator. dst must be a pointer to a struct.
func Load(path string, dst any) error {
	return LoadWith(path, dst, os.LookupEnv)
}

// LoadWith is Load with an explicit environment lookup (for tests).
func LoadWith(path string, dst any, lookup func(string) (string, bool)) error {
	rv := reflect.ValueOf(dst)
	if rv.Kind() != reflect.Pointer || rv.Elem().Kind() != reflect.Struct {
		return fmt.Errorf("config: dst must be a pointer to a struct, got %T", dst)
	}
	if path != "" {
		if err := decodeFile(path, dst); err != nil {
			return err
		}
	}
	if err := applyEnv(rv.Elem(), lookup); err != nil {
		return fmt.Errorf("config: environment: %w", err)
	}
	if v, ok := dst.(Validator); ok {
		if err := v.Validate(); err != nil {
			return fmt.Errorf("config: invalid: %w", err)
		}
	}
	return nil
}

func decodeFile(path string, dst any) error {
	f, err := os.Open(path) //nolint:gosec // path comes from the operator, not from requests
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}
	defer func() { _ = f.Close() }()

	dec := yaml.NewDecoder(f)
	dec.KnownFields(true) // unknown keys are an error (spec 02 §14)
	if err := dec.Decode(dst); err != nil && !errors.Is(err, io.EOF) {
		return fmt.Errorf("config: %s: %w", path, err)
	}
	return nil
}

var durationType = reflect.TypeFor[time.Duration]()

// applyEnv walks exported struct fields and sets those whose `env` variable is present.
func applyEnv(v reflect.Value, lookup func(string) (string, bool)) error {
	var errs []error
	t := v.Type()
	for i := range t.NumField() {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		fv := v.Field(i)
		name, hasEnv := f.Tag.Lookup("env")
		if !hasEnv {
			if fv.Kind() == reflect.Struct {
				errs = append(errs, applyEnv(fv, lookup))
			}
			continue
		}
		raw, ok := lookup(name)
		if !ok {
			continue
		}
		if err := setValue(fv, raw); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", name, err))
		}
	}
	return errors.Join(errs...)
}

func setValue(v reflect.Value, raw string) error {
	if v.Type() == durationType {
		d, err := time.ParseDuration(raw)
		if err != nil {
			return err
		}
		v.SetInt(int64(d))
		return nil
	}
	switch v.Kind() {
	case reflect.String:
		v.SetString(raw)
	case reflect.Bool:
		b, err := strconv.ParseBool(raw)
		if err != nil {
			return err
		}
		v.SetBool(b)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		n, err := strconv.ParseInt(raw, 10, v.Type().Bits())
		if err != nil {
			return err
		}
		v.SetInt(n)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		n, err := strconv.ParseUint(raw, 10, v.Type().Bits())
		if err != nil {
			return err
		}
		v.SetUint(n)
	case reflect.Float32, reflect.Float64:
		n, err := strconv.ParseFloat(raw, v.Type().Bits())
		if err != nil {
			return err
		}
		v.SetFloat(n)
	case reflect.Slice:
		if v.Type().Elem().Kind() != reflect.String {
			return fmt.Errorf("unsupported slice type %s", v.Type())
		}
		var parts []string
		for p := range strings.SplitSeq(raw, ",") {
			if p = strings.TrimSpace(p); p != "" {
				parts = append(parts, p)
			}
		}
		v.Set(reflect.ValueOf(parts))
	default:
		return fmt.Errorf("unsupported type %s", v.Type())
	}
	return nil
}
