// Package filter parses List filters (spec 02 §5.2, REQ-PLT-032):
//
//	name = "web-1" AND status IN (RUNNING, STOPPED) AND labels.env != prod
//
// Terms are `<field> <op> <value>` joined by AND (case-insensitive); ops are
// =, != and IN (...). Values are bare words or quoted strings ("…" or '…'
// with backslash escapes). Only fields declared in the resource's Schema
// are accepted, and the result is rendered as parameterised SQL.
package filter

import (
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/mindevis/qstack/pkg/apierr"
)

// Limits keep filters cheap to evaluate.
const (
	MaxLength   = 1024
	MaxTerms    = 20
	MaxInValues = 100
)

// Field describes a filterable field.
type Field struct {
	// Column is the SQL column (a trusted identifier from the service).
	Column string
	// Labels marks a jsonb map column filtered as "<field>.<key>", e.g.
	// labels.env.
	Labels bool
	// Values, if set, is the closed set of allowed values (enums).
	Values []string
}

// Schema maps field names to fields, e.g. {"name": {Column: "name"},
// "status": {Column: "status", Values: [...]}, "labels": {Column: "labels", Labels: true}}.
type Schema map[string]Field

// Op is a comparison operator.
type Op string

// Operators.
const (
	Eq    Op = "="
	NotEq Op = "!="
	In    Op = "IN"
)

// Term is one comparison.
type Term struct {
	Field    string // field name as written, e.g. "labels.env"
	Column   string
	LabelKey string // set for label fields
	Op       Op
	Values   []string // one value except for IN
}

// Filter is a parsed filter; the zero value matches everything.
type Filter struct{ Terms []Term }

var labelKey = regexp.MustCompile(`^[a-z][a-z0-9_./-]{0,62}$`)

// Parse parses s against schema. Errors are INVALID_ARGUMENT
// (QS_INVALID_FIELD) on the "filter" field.
func Parse(s string, schema Schema) (Filter, error) {
	if len(s) > MaxLength {
		return Filter{}, invalid(fmt.Sprintf("longer than %d characters", MaxLength))
	}
	toks, err := lex(s)
	if err != nil {
		return Filter{}, invalid(err.Error())
	}
	p := parser{toks: toks, schema: schema}
	f, err := p.parse()
	if err != nil {
		return Filter{}, invalid(err.Error())
	}
	return f, nil
}

func invalid(desc string) error {
	return apierr.InvalidFields(apierr.FieldViolation{Field: "filter", Description: desc})
}

// SQL renders the filter as a boolean SQL expression using placeholders
// $next, $next+1, … and returns it with its arguments; an empty filter
// renders "TRUE".
func (f Filter) SQL(next int) (string, []any) {
	if len(f.Terms) == 0 {
		return "TRUE", nil
	}
	var parts []string
	var args []any
	ph := func(v any) string {
		args = append(args, v)
		return "$" + strconv.Itoa(next+len(args)-1)
	}
	for _, t := range f.Terms {
		col := pgx.Identifier{t.Column}.Sanitize()
		if t.LabelKey != "" {
			col = "(" + col + " ->> " + ph(t.LabelKey) + ")"
		}
		switch t.Op {
		case Eq:
			parts = append(parts, col+" = "+ph(t.Values[0]))
		case NotEq:
			// A missing label is "not equal" too.
			parts = append(parts, col+" IS DISTINCT FROM "+ph(t.Values[0]))
		case In:
			parts = append(parts, col+" = ANY("+ph(t.Values)+")")
		}
	}
	return strings.Join(parts, " AND "), args
}

// --- lexer ---

type tokKind int

const (
	tWord tokKind = iota // bare word: field, value, AND, IN
	tString
	tOp // = or !=
	tLParen
	tRParen
	tComma
)

type token struct {
	kind tokKind
	text string
}

func isWordChar(r byte) bool {
	return r == '_' || r == '-' || r == '.' || r == '/' || r == ':' ||
		('a' <= r && r <= 'z') || ('A' <= r && r <= 'Z') || ('0' <= r && r <= '9')
}

func lex(s string) ([]token, error) {
	var toks []token
	for i := 0; i < len(s); {
		c := s[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			i++
		case c == '=':
			toks = append(toks, token{tOp, "="})
			i++
		case c == '!' && i+1 < len(s) && s[i+1] == '=':
			toks = append(toks, token{tOp, "!="})
			i += 2
		case c == '(':
			toks = append(toks, token{tLParen, "("})
			i++
		case c == ')':
			toks = append(toks, token{tRParen, ")"})
			i++
		case c == ',':
			toks = append(toks, token{tComma, ","})
			i++
		case c == '"' || c == '\'':
			var b strings.Builder
			j := i + 1
			for ; j < len(s) && s[j] != c; j++ {
				if s[j] == '\\' && j+1 < len(s) {
					j++
				}
				b.WriteByte(s[j])
			}
			if j >= len(s) {
				return nil, fmt.Errorf("unterminated string at position %d", i+1)
			}
			toks = append(toks, token{tString, b.String()})
			i = j + 1
		case isWordChar(c):
			j := i
			for j < len(s) && isWordChar(s[j]) {
				j++
			}
			toks = append(toks, token{tWord, s[i:j]})
			i = j
		default:
			return nil, fmt.Errorf("unexpected character %q at position %d", c, i+1)
		}
	}
	return toks, nil
}

// --- parser ---

type parser struct {
	toks   []token
	pos    int
	schema Schema
}

func (p *parser) peek() (token, bool) {
	if p.pos >= len(p.toks) {
		return token{}, false
	}
	return p.toks[p.pos], true
}

func (p *parser) next() (token, bool) {
	t, ok := p.peek()
	if ok {
		p.pos++
	}
	return t, ok
}

func isKeyword(t token, kw string) bool { return t.kind == tWord && strings.EqualFold(t.text, kw) }

func (p *parser) parse() (Filter, error) {
	var f Filter
	if len(p.toks) == 0 {
		return f, nil
	}
	for {
		term, err := p.term()
		if err != nil {
			return Filter{}, err
		}
		f.Terms = append(f.Terms, term)
		if len(f.Terms) > MaxTerms {
			return Filter{}, fmt.Errorf("more than %d terms", MaxTerms)
		}
		t, ok := p.next()
		if !ok {
			return f, nil
		}
		if !isKeyword(t, "AND") {
			return Filter{}, fmt.Errorf("expected AND, got %q (only AND is supported)", t.text)
		}
	}
}

func (p *parser) term() (Term, error) {
	ft, ok := p.next()
	if !ok || ft.kind != tWord || isKeyword(ft, "AND") || isKeyword(ft, "IN") {
		return Term{}, fmt.Errorf("expected a field name")
	}
	term, field, err := p.field(ft.text)
	if err != nil {
		return Term{}, err
	}

	op, ok := p.next()
	switch {
	case ok && op.kind == tOp:
		term.Op = Op(op.text)
		v, err := p.value()
		if err != nil {
			return Term{}, err
		}
		term.Values = []string{v}
	case ok && isKeyword(op, "IN"):
		term.Op = In
		if t, ok := p.next(); !ok || t.kind != tLParen {
			return Term{}, fmt.Errorf("expected ( after IN")
		}
		for {
			v, err := p.value()
			if err != nil {
				return Term{}, err
			}
			term.Values = append(term.Values, v)
			if len(term.Values) > MaxInValues {
				return Term{}, fmt.Errorf("more than %d values in IN", MaxInValues)
			}
			t, ok := p.next()
			if ok && t.kind == tRParen {
				break
			}
			if !ok || t.kind != tComma {
				return Term{}, fmt.Errorf("expected , or ) in IN list")
			}
		}
	default:
		return Term{}, fmt.Errorf("expected =, != or IN after %s", term.Field)
	}

	if len(field.Values) > 0 {
		for _, v := range term.Values {
			if !slices.Contains(field.Values, v) {
				return Term{}, fmt.Errorf("%s: unknown value %q", term.Field, v)
			}
		}
	}
	return term, nil
}

func (p *parser) field(name string) (Term, Field, error) {
	if f, ok := p.schema[name]; ok && !f.Labels {
		return Term{Field: name, Column: f.Column}, f, nil
	}
	if base, key, ok := strings.Cut(name, "."); ok {
		if f, ok := p.schema[base]; ok && f.Labels {
			if !labelKey.MatchString(key) {
				return Term{}, Field{}, fmt.Errorf("invalid label key %q", key)
			}
			return Term{Field: name, Column: f.Column, LabelKey: key}, f, nil
		}
	}
	return Term{}, Field{}, fmt.Errorf("unknown field %q (allowed: %s)", name, strings.Join(p.schema.names(), ", "))
}

func (p *parser) value() (string, error) {
	t, ok := p.next()
	if !ok || (t.kind != tWord && t.kind != tString) {
		return "", fmt.Errorf("expected a value")
	}
	return t.text, nil
}

func (s Schema) names() []string {
	out := make([]string, 0, len(s))
	for n, f := range s {
		if f.Labels {
			n += ".<key>"
		}
		out = append(out, n)
	}
	slices.Sort(out)
	return out
}
