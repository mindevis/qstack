// Package ids generates and parses resource IDs of the form `<prefix>-<ulid>`
// (spec 02 §3, REQ-PLT-020), e.g. `ins-01jb2w3k8x9yq4m5n6p7r8s9t0`.
//
// The ULID part is 26 characters of lowercase Crockford base32: a 48-bit
// millisecond timestamp followed by 80 random bits, so IDs sort roughly by
// creation time. Only the canonical lowercase form is accepted by Parse, which
// guarantees that one ID has exactly one string representation.
package ids

import (
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/mindevis/qstack/pkg/clock"
)

// Kind identifies a resource type by its ID prefix.
type Kind string

// Resource kinds (spec 02 §3). Never change an existing prefix.
const (
	Organization            Kind = "org"
	Project                 Kind = "prj"
	User                    Kind = "usr"
	ServiceAccount          Kind = "sa"
	Group                   Kind = "grp"
	AccessBinding           Kind = "bnd"
	APIKey                  Kind = "key"
	AccessKey               Kind = "ak"
	AuthorizedKey           Kind = "ajk"
	Operation               Kind = "op"
	Host                    Kind = "hst"
	RouteTable              Kind = "rt"
	TargetGroup             Kind = "tg"
	BillingAccount          Kind = "bac"
	Instance                Kind = "ins"
	Image                   Kind = "img"
	Disk                    Kind = "dsk"
	Snapshot                Kind = "snp"
	PlacementGroup          Kind = "plg"
	Network                 Kind = "vpc"
	Subnet                  Kind = "sub"
	SecurityGroup           Kind = "sg"
	NetworkInterface        Kind = "nic"
	Address                 Kind = "adr"
	NatGateway              Kind = "nat"
	NetworkLoadBalancer     Kind = "nlb"
	ApplicationLoadBalancer Kind = "alb"
	Invoice                 Kind = "inv"
)

// kindNames maps a kind to the resource type name used in error messages.
var kindNames = map[Kind]string{
	Organization:            "organization",
	Project:                 "project",
	User:                    "user",
	ServiceAccount:          "service account",
	Group:                   "group",
	AccessBinding:           "access binding",
	APIKey:                  "API key",
	AccessKey:               "access key",
	AuthorizedKey:           "authorized key",
	Operation:               "operation",
	Host:                    "host",
	RouteTable:              "route table",
	TargetGroup:             "target group",
	BillingAccount:          "billing account",
	Instance:                "instance",
	Image:                   "image",
	Disk:                    "disk",
	Snapshot:                "snapshot",
	PlacementGroup:          "placement group",
	Network:                 "network",
	Subnet:                  "subnet",
	SecurityGroup:           "security group",
	NetworkInterface:        "network interface",
	Address:                 "address",
	NatGateway:              "NAT gateway",
	NetworkLoadBalancer:     "network load balancer",
	ApplicationLoadBalancer: "application load balancer",
	Invoice:                 "invoice",
}

// Prefix returns the ID prefix, e.g. "ins".
func (k Kind) Prefix() string { return string(k) }

// Name returns the human-readable resource type, e.g. "instance".
func (k Kind) Name() string { return kindNames[k] }

// Valid reports whether k is a known kind.
func (k Kind) Valid() bool {
	_, ok := kindNames[k]
	return ok
}

// Kinds returns all known kinds.
func Kinds() []Kind {
	out := make([]Kind, 0, len(kindNames))
	for k := range kindNames {
		out = append(out, k)
	}
	return out
}

// ErrInvalidID is wrapped by every Parse error.
var ErrInvalidID = errors.New("invalid id")

const (
	ulidLen  = 26
	alphabet = "0123456789abcdefghjkmnpqrstvwxyz" // Crockford base32, lowercase
)

// decodeTable maps an ASCII byte to its 5-bit value, or 0xff if not canonical.
var decodeTable = func() [256]byte {
	var t [256]byte
	for i := range t {
		t[i] = 0xff
	}
	for i := range len(alphabet) {
		t[alphabet[i]] = byte(i)
	}
	return t
}()

// Generator creates IDs. The zero value is not usable; use NewGenerator.
type Generator struct {
	clock clock.Clock

	mu      sync.Mutex // guards entropy, which need not be goroutine-safe
	entropy io.Reader
}

// NewGenerator returns a Generator using the given clock and entropy source
// (crypto/rand.Reader in production; a fixed reader in tests).
func NewGenerator(c clock.Clock, entropy io.Reader) *Generator {
	return &Generator{clock: c, entropy: entropy}
}

// New returns a new ID of kind k. It panics if k is unknown or the entropy
// source fails, both of which are programming or system errors.
func (g *Generator) New(k Kind) string {
	if !k.Valid() {
		panic(fmt.Sprintf("ids: unknown kind %q", string(k)))
	}
	var b [16]byte
	var ts [8]byte
	binary.BigEndian.PutUint64(ts[:], uint64(g.clock.Now().UnixMilli())) //nolint:gosec // pre-1970 clocks are not supported
	copy(b[0:6], ts[2:])                                                 // low 48 bits of the millisecond timestamp

	g.mu.Lock()
	_, err := io.ReadFull(g.entropy, b[6:])
	g.mu.Unlock()
	if err != nil {
		panic(fmt.Sprintf("ids: entropy source failed: %v", err))
	}
	return k.Prefix() + "-" + encode(b)
}

var defaultGenerator = NewGenerator(clock.Real(), rand.Reader)

// New returns a new ID of kind k using the system clock and crypto/rand.
func New(k Kind) string { return defaultGenerator.New(k) }

// Parse validates id and returns its kind.
func Parse(id string) (Kind, error) {
	prefix, ulid, ok := strings.Cut(id, "-")
	if !ok {
		return "", fmt.Errorf("%w: %q has no prefix", ErrInvalidID, id)
	}
	k := Kind(prefix)
	if !k.Valid() {
		return "", fmt.Errorf("%w: unknown prefix %q", ErrInvalidID, prefix)
	}
	if _, err := decode(ulid); err != nil {
		return "", fmt.Errorf("%w: %q: %w", ErrInvalidID, id, err)
	}
	return k, nil
}

// ParseKind validates id and checks that it is of kind want.
func ParseKind(id string, want Kind) error {
	k, err := Parse(id)
	if err != nil {
		return err
	}
	if k != want {
		return fmt.Errorf("%w: %q is a %s ID, want %s", ErrInvalidID, id, k.Name(), want.Name())
	}
	return nil
}

// Time returns the creation time encoded in id (millisecond precision, UTC).
func Time(id string) (time.Time, error) {
	if _, err := Parse(id); err != nil {
		return time.Time{}, err
	}
	_, ulid, _ := strings.Cut(id, "-")
	b, _ := decode(ulid)
	var ts [8]byte
	copy(ts[2:], b[0:6])
	ms := binary.BigEndian.Uint64(ts[:])
	return time.UnixMilli(int64(ms)).UTC(), nil //nolint:gosec // 48-bit value always fits
}

// encode converts 128 bits into 26 base32 characters (2 leading zero bits).
func encode(b [16]byte) string {
	hi := binary.BigEndian.Uint64(b[0:8])
	lo := binary.BigEndian.Uint64(b[8:16])
	var out [ulidLen]byte
	for i := ulidLen - 1; i >= 0; i-- {
		out[i] = alphabet[lo&0x1f]
		lo = lo>>5 | hi<<59
		hi >>= 5
	}
	return string(out[:])
}

// decode is the inverse of encode; it accepts only the canonical form.
func decode(s string) ([16]byte, error) {
	var b [16]byte
	if len(s) != ulidLen {
		return b, fmt.Errorf("ulid must be %d characters, got %d", ulidLen, len(s))
	}
	var hi, lo uint64
	for i := range ulidLen {
		v := decodeTable[s[i]]
		if v == 0xff {
			return b, fmt.Errorf("invalid character %q at position %d", s[i], i)
		}
		if i == 0 && v > 7 {
			return b, errors.New("ulid overflows 128 bits")
		}
		hi = hi<<5 | lo>>59
		lo = lo<<5 | uint64(v)
	}
	binary.BigEndian.PutUint64(b[0:8], hi)
	binary.BigEndian.PutUint64(b[8:16], lo)
	return b, nil
}
