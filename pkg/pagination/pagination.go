// Package pagination implements List paging (spec 02 §5.2, REQ-PLT-031):
// page_size defaults to 100 and is capped at 1000; page_token is an opaque,
// HMAC-signed keyset cursor bound to the request parameters and valid for
// one hour. Lists are always in a deterministic order ending with id.
//
// The HMAC keys are service secrets from OpenBao (REQ-SEC-002); the first
// key signs and all keys verify, so keys can be rotated without breaking
// tokens in flight.
package pagination

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/mindevis/qstack/pkg/apierr"
	"github.com/mindevis/qstack/pkg/clock"
)

// Spec 02 §5.2.
const (
	DefaultPageSize = 100
	MaxPageSize     = 1000
	TokenTTL        = time.Hour
	// MinKeyLength is the minimum HMAC key size (SHA-256 block security).
	MinKeyLength = 32
)

// PageSize applies the default and the cap to a requested page_size.
func PageSize(requested int32) (int, error) {
	switch {
	case requested < 0:
		return 0, apierr.InvalidFields(apierr.FieldViolation{Field: "page_size", Description: "must not be negative"})
	case requested == 0:
		return DefaultPageSize, nil
	default:
		return int(min(requested, MaxPageSize)), nil
	}
}

// Trim cuts the size+1 rows a query fetched to size and reports whether
// another page exists.
func Trim[T any](rows []T, size int) ([]T, bool) {
	if len(rows) > size {
		return rows[:size], true
	}
	return rows, false
}

// Binding identifies the request parameters a token belongs to: a hash of
// the request without page_token and page_size (a client may change the
// page size between pages, nothing else).
func Binding(req proto.Message) []byte {
	m := proto.Clone(req).ProtoReflect()
	fields := m.Descriptor().Fields()
	for _, name := range []protoreflect.Name{"page_token", "page_size"} {
		if fd := fields.ByName(name); fd != nil {
			m.Clear(fd)
		}
	}
	b, _ := proto.MarshalOptions{Deterministic: true}.Marshal(m.Interface())
	h := sha256.New()
	h.Write([]byte(m.Descriptor().FullName()))
	h.Write([]byte{0})
	h.Write(b)
	return h.Sum(nil)
}

// Signer creates and verifies page tokens.
type Signer struct {
	keys  [][]byte
	clock clock.Clock
}

// NewSigner returns a Signer; keys[0] signs, every key verifies.
func NewSigner(clk clock.Clock, keys ...[]byte) (*Signer, error) {
	if len(keys) == 0 {
		return nil, errors.New("pagination: no signing key")
	}
	for _, k := range keys {
		if len(k) < MinKeyLength {
			return nil, fmt.Errorf("pagination: signing keys must be at least %d bytes", MinKeyLength)
		}
	}
	return &Signer{keys: keys, clock: clk}, nil
}

// tokenVersion prefixes the payload so the format can evolve.
const tokenVersion = 1

type payload struct {
	V int      `json:"v"`
	E int64    `json:"e"` // expiry, unix seconds
	K []string `json:"k"` // sort key values of the last returned row
}

// Encode returns a token for the page after the row with sort key values
// cursor (as produced by Order.Cursor), bound to binding.
func (s *Signer) Encode(binding []byte, cursor []string) (string, error) {
	p, err := json.Marshal(payload{V: tokenVersion, E: s.clock.Now().Add(TokenTTL).Unix(), K: cursor})
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(p) + "." +
		base64.RawURLEncoding.EncodeToString(mac(s.keys[0], p, binding)), nil
}

// Decode verifies token against binding and returns the cursor; any
// failure — tampering, other parameters, expiry — is INVALID_ARGUMENT on
// page_token, without telling which.
func (s *Signer) Decode(token string, binding []byte) ([]string, error) {
	bad := apierr.InvalidFields(apierr.FieldViolation{Field: "page_token", Description: "invalid or expired; restart listing from the first page"})
	ps, ms, ok := strings.Cut(token, ".")
	if !ok {
		return nil, bad
	}
	p, err1 := base64.RawURLEncoding.DecodeString(ps)
	sig, err2 := base64.RawURLEncoding.DecodeString(ms)
	if err1 != nil || err2 != nil {
		return nil, bad
	}
	if !slices.ContainsFunc(s.keys, func(k []byte) bool { return hmac.Equal(sig, mac(k, p, binding)) }) {
		return nil, bad
	}
	var pl payload
	d := json.NewDecoder(bytes.NewReader(p))
	d.DisallowUnknownFields()
	if err := d.Decode(&pl); err != nil || pl.V != tokenVersion || s.clock.Now().Unix() >= pl.E {
		return nil, bad
	}
	return pl.K, nil
}

func mac(key, payload, binding []byte) []byte {
	h := hmac.New(sha256.New, key)
	h.Write(binding)
	h.Write([]byte{0})
	h.Write(payload)
	return h.Sum(nil)
}

// SortField is a column lists may be ordered by.
type SortField struct {
	Column string
	// Type is the PostgreSQL type cursor values are cast to, e.g.
	// "timestamptz" or "text".
	Type string
}

// Key is one ordering key.
type Key struct {
	Field string
	SortField
	Desc bool
}

// Order is a parsed order_by, always ending with id as tiebreaker.
type Order []Key

// ParseOrder parses order_by ("create_time desc, name"); fields must be in
// allowed, which must contain "id". Empty means def (or create_time, id).
func ParseOrder(orderBy string, allowed map[string]SortField, def string) (Order, error) {
	if _, ok := allowed["id"]; !ok {
		return nil, errors.New("pagination: allowed sort fields must include id")
	}
	if strings.TrimSpace(orderBy) == "" {
		orderBy = def
		if orderBy == "" {
			orderBy = "create_time"
		}
	}
	bad := func(desc string) error {
		return apierr.InvalidFields(apierr.FieldViolation{Field: "order_by", Description: desc})
	}
	var o Order
	seen := map[string]bool{}
	for part := range strings.SplitSeq(orderBy, ",") {
		words := strings.Fields(part)
		if len(words) == 0 || len(words) > 2 {
			return nil, bad(fmt.Sprintf("invalid term %q", strings.TrimSpace(part)))
		}
		sf, ok := allowed[words[0]]
		if !ok {
			return nil, bad(fmt.Sprintf("cannot order by %q", words[0]))
		}
		if seen[words[0]] {
			return nil, bad(fmt.Sprintf("%q repeated", words[0]))
		}
		seen[words[0]] = true
		k := Key{Field: words[0], SortField: sf}
		if len(words) == 2 {
			switch strings.ToLower(words[1]) {
			case "asc":
			case "desc":
				k.Desc = true
			default:
				return nil, bad(fmt.Sprintf("invalid direction %q", words[1]))
			}
		}
		o = append(o, k)
	}
	if !seen["id"] {
		o = append(o, Key{Field: "id", SortField: allowed["id"], Desc: o[len(o)-1].Desc})
	} else if o[len(o)-1].Field != "id" {
		return nil, bad("id must be the last sort field")
	}
	return o, nil
}

// SQL renders the ORDER BY list.
func (o Order) SQL() string {
	parts := make([]string, len(o))
	for i, k := range o {
		parts[i] = pgx.Identifier{k.Column}.Sanitize()
		if k.Desc {
			parts[i] += " DESC"
		}
	}
	return strings.Join(parts, ", ")
}

// After renders the keyset condition "row comes after cursor" with
// placeholders from $next, e.g. for (create_time, id):
//
//	("create_time" > $1::timestamptz OR ("create_time" = $1::timestamptz AND "id" > $2::text))
//
// An empty cursor renders "TRUE". Sort columns must be NOT NULL.
func (o Order) After(cursor []string, next int) (string, []any, error) {
	if len(cursor) == 0 {
		return "TRUE", nil, nil
	}
	if len(cursor) != len(o) {
		return "", nil, apierr.InvalidFields(apierr.FieldViolation{Field: "page_token", Description: "does not match order_by"})
	}
	args := make([]any, len(cursor))
	ph := make([]string, len(cursor))
	for i, v := range cursor {
		args[i] = v
		ph[i] = "$" + strconv.Itoa(next+i) + "::" + o[i].Type
	}
	var ors []string
	for i := range o {
		var ands []string
		for j := range i {
			ands = append(ands, pgx.Identifier{o[j].Column}.Sanitize()+" = "+ph[j])
		}
		cmp := " > "
		if o[i].Desc {
			cmp = " < "
		}
		ands = append(ands, pgx.Identifier{o[i].Column}.Sanitize()+cmp+ph[i])
		ors = append(ors, "("+strings.Join(ands, " AND ")+")")
	}
	return "(" + strings.Join(ors, " OR ") + ")", args, nil
}

// TimeKey formats a timestamp as a cursor value for a timestamptz column.
func TimeKey(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }
