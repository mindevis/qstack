package testenv

import "testing"

func TestWithDatabase(t *testing.T) {
	got, err := withDatabase("postgres://u:p@db:5432/postgres?sslmode=disable", "test_abc")
	if err != nil || got != "postgres://u:p@db:5432/test_abc?sslmode=disable" {
		t.Errorf("withDatabase() = %q, %v", got, err)
	}
	if _, err := withDatabase("host=db user=u dbname=postgres", "x"); err == nil {
		t.Error("key/value DSN must be rejected (URL form required)")
	}
}
