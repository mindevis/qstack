package authz

import (
	"container/list"
	"context"
	"strings"
	"sync"
	"time"

	"google.golang.org/grpc"

	iaminternalv1 "github.com/mindevis/qstack/gen/go/qstack/iam/internalv1"
	"github.com/mindevis/qstack/pkg/clock"
)

// Cache defaults (REQ-IAM-011).
const (
	DefaultCacheTTL  = 5 * time.Second
	DefaultCacheSize = 10000
)

// ClientOptions configures a Client.
type ClientOptions struct {
	Clock clock.Clock
	// CacheTTL and CacheSize default to 5s and 10000 decisions.
	CacheTTL  time.Duration
	CacheSize int
}

// Client is an Authorizer backed by iam.internal.v1.AccessService
// (qs-authz-cache) with an LRU cache of decisions. Both allow and deny are
// cached; errors are not.
type Client struct {
	svc   iaminternalv1.AccessServiceClient
	cache *lru
}

// NewClient returns a Client over conn.
func NewClient(conn grpc.ClientConnInterface, opts ClientOptions) *Client {
	if opts.Clock == nil {
		opts.Clock = clock.Real()
	}
	if opts.CacheTTL <= 0 {
		opts.CacheTTL = DefaultCacheTTL
	}
	if opts.CacheSize <= 0 {
		opts.CacheSize = DefaultCacheSize
	}
	return &Client{svc: iaminternalv1.NewAccessServiceClient(conn), cache: newLRU(opts.CacheSize, opts.CacheTTL, opts.Clock)}
}

// Authorize implements Authorizer.
func (c *Client) Authorize(ctx context.Context, subject, permission string, path Path) (bool, error) {
	key := cacheKey(subject, permission, path)
	if allowed, ok := c.cache.get(key); ok {
		return allowed, nil
	}
	resp, err := c.svc.Authorize(ctx, &iaminternalv1.AuthorizeRequest{
		SubjectId: subject, Permission: permission, Resource: toProto(path),
	})
	if err != nil {
		return false, err
	}
	c.cache.put(key, resp.GetAllowed())
	return resp.GetAllowed(), nil
}

func toProto(p Path) *iaminternalv1.ResourcePath {
	rp := &iaminternalv1.ResourcePath{}
	for _, r := range p {
		rp.Resources = append(rp.Resources, &iaminternalv1.Resource{Type: r.Type, Id: r.ID})
	}
	return rp
}

// cacheKey joins the parts with a separator that IDs and permissions never
// contain.
func cacheKey(subject, permission string, path Path) string {
	var b strings.Builder
	b.WriteString(subject)
	b.WriteByte(0)
	b.WriteString(permission)
	for _, r := range path {
		b.WriteByte(0)
		b.WriteString(r.Type)
		b.WriteByte(0)
		b.WriteString(r.ID)
	}
	return b.String()
}

// lru is a size-bounded cache with per-entry expiry.
type lru struct {
	mu    sync.Mutex
	size  int
	ttl   time.Duration
	clock clock.Clock
	order *list.List // front = most recent
	items map[string]*list.Element
}

type entry struct {
	key     string
	allowed bool
	expires time.Time
}

func newLRU(size int, ttl time.Duration, clk clock.Clock) *lru {
	return &lru{size: size, ttl: ttl, clock: clk, order: list.New(), items: map[string]*list.Element{}}
}

func (l *lru) get(key string) (bool, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	el, ok := l.items[key]
	if !ok {
		return false, false
	}
	e := el.Value.(*entry)
	if !l.clock.Now().Before(e.expires) {
		l.order.Remove(el)
		delete(l.items, key)
		return false, false
	}
	l.order.MoveToFront(el)
	return e.allowed, true
}

func (l *lru) put(key string, allowed bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	exp := l.clock.Now().Add(l.ttl)
	if el, ok := l.items[key]; ok {
		el.Value = &entry{key: key, allowed: allowed, expires: exp}
		l.order.MoveToFront(el)
		return
	}
	l.items[key] = l.order.PushFront(&entry{key: key, allowed: allowed, expires: exp})
	for l.order.Len() > l.size {
		last := l.order.Back()
		l.order.Remove(last)
		delete(l.items, last.Value.(*entry).key)
	}
}
