package filter

import (
	"fmt"
	"strings"
	"testing"

	apipb "github.com/mindevis/qstack/gen/go/qstack/api"
	"github.com/mindevis/qstack/pkg/apierr"
)

var schema = Schema{
	"name":   {Column: "name"},
	"status": {Column: "status", Values: []string{"RUNNING", "STOPPED", "ERROR"}},
	"labels": {Column: "labels", Labels: true},
}

// REQ-PLT-032: `<field> <op> <value>` joined by AND, ops =, !=, IN, rendered
// as parameterised SQL.
func TestParseAndSQL(t *testing.T) {
	for _, tc := range []struct {
		in, sql, args string
	}{
		{"", "TRUE", "[]"},
		{`name = "web-1"`, `"name" = $3`, "[web-1]"},
		{`status IN (RUNNING, STOPPED) and labels.env != prod`,
			`"status" = ANY($3) AND ("labels" ->> $4) IS DISTINCT FROM $5`, "[[RUNNING STOPPED] env prod]"},
		{`labels.app.kubernetes.io/name = "a b" AND name != x`,
			`("labels" ->> $3) = $4 AND "name" IS DISTINCT FROM $5`, "[app.kubernetes.io/name a b x]"},
		{`name = "quote\"inside"`, `"name" = $3`, `[quote"inside]`},
		{`labels.tier IN ('db')`, `("labels" ->> $3) = ANY($4)`, "[tier [db]]"},
	} {
		f, err := Parse(tc.in, schema)
		if err != nil {
			t.Errorf("Parse(%q): %v", tc.in, err)
			continue
		}
		sql, args := f.SQL(3)
		if sql != tc.sql || fmt.Sprint(args) != tc.args {
			t.Errorf("Parse(%q).SQL = %s %v\nwant %s %s", tc.in, sql, args, tc.sql, tc.args)
		}
	}
}

func TestErrors(t *testing.T) {
	for in, wantDesc := range map[string]string{
		`id = x`:               "unknown field",
		`name = x OR name = y`: "only AND",
		`name ~ x`:             "unexpected character",
		`name = `:              "expected a value",
		`name x`:               "expected =, != or IN",
		`status = DELETED`:     "unknown value",
		`labels.Env = x`:       "invalid label key",
		`labels = x`:           "unknown field",
		`name = "open`:         "unterminated",
		`status IN (RUNNING`:   "expected , or )",
		`name = x AND`:         "expected a field",
		`name = 'it''s'`:       "expected AND",
		strings.Repeat("name = x AND ", 20) + "name = x": "more than 20 terms",
		`name = x; DROP TABLE instances`:                 "unexpected character",
	} {
		_, err := Parse(in, schema)
		if !apierr.IsReason(err, apipb.ErrorReason_QS_INVALID_FIELD) || !strings.Contains(err.Error(), wantDesc) {
			t.Errorf("Parse(%q) err = %v, want QS_INVALID_FIELD containing %q", in, err, wantDesc)
		}
	}
	if _, err := Parse(strings.Repeat("a", MaxLength+1), schema); err == nil {
		t.Error("over-long filter accepted")
	}
}

func FuzzParse(f *testing.F) {
	for _, s := range []string{`name = "a"`, `status IN (RUNNING, STOPPED)`, `labels.env != prod AND name = x`, `"`, `(`} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		fl, err := Parse(s, schema)
		if err != nil {
			return
		}
		sql, args := fl.SQL(1)
		// Values never reach the SQL text: one placeholder per argument.
		if strings.Count(sql, "$") != len(args) {
			t.Fatalf("placeholders %q vs %d args", sql, len(args))
		}
	})
}
