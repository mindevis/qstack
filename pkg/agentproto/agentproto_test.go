package agentproto

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"log/slog"
	"math/big"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/wrapperspb"

	agentv1 "github.com/mindevis/qstack/gen/go/qstack/agent/internalv1"
	"github.com/mindevis/qstack/pkg/clock"
)

const (
	region = "ru-central1"
	hostA  = "hst-01m5hqpzg00000000000000000"
	hostB  = "hst-01m5hqpzg00000000000000001"
)

var logger = slog.New(slog.DiscardHandler)

// --- PKI ---

type pki struct {
	ca     *x509.Certificate
	caKey  *ecdsa.PrivateKey
	pool   *x509.CertPool
	server tls.Certificate
}

func newPKI(t *testing.T) *pki {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "qstack test CA"},
		NotBefore: time.Unix(0, 0), NotAfter: time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC), IsCA: true, BasicConstraintsValid: true,
		KeyUsage: x509.KeyUsageCertSign}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	ca, _ := x509.ParseCertificate(der)
	p := &pki{ca: ca, caKey: key, pool: x509.NewCertPool()}
	p.pool.AddCert(ca)
	p.server = p.issue(t, func(c *x509.Certificate) {
		c.DNSNames = []string{"controller"}
		c.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}
	})
	return p
}

func (p *pki) issue(t *testing.T, edit func(*x509.Certificate)) tls.Certificate {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	serial, _ := rand.Int(rand.Reader, big.NewInt(1<<62))
	tmpl := &x509.Certificate{SerialNumber: serial, NotBefore: time.Unix(0, 0), NotAfter: time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}
	edit(tmpl)
	der, err := x509.CreateCertificate(rand.Reader, tmpl, p.ca, &key.PublicKey, p.caKey)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

func (p *pki) host(t *testing.T, region, host string) tls.Certificate {
	t.Helper()
	return p.issue(t, func(c *x509.Certificate) { c.URIs = []*url.URL{HostURI(region, host)} })
}

// --- restartable controller ---

type controller struct {
	t    *testing.T
	pki  *pki
	hub  *Hub
	opts ServerOptions

	mu  sync.Mutex
	lis *bufconn.Listener
	srv *grpc.Server
	api *Server
}

func newController(t *testing.T, p *pki, hub *Hub, edit func(*ServerOptions)) *controller {
	t.Helper()
	c := &controller{t: t, pki: p, hub: hub,
		opts: ServerOptions{Region: region, Hub: hub, Clock: clock.Real(), Logger: logger, OwnershipCheck: 50 * time.Millisecond}}
	if edit != nil {
		edit(&c.opts)
	}
	c.start()
	t.Cleanup(c.stop)
	return c
}

func (c *controller) start() {
	c.mu.Lock()
	defer c.mu.Unlock()
	api, err := NewServer(c.opts)
	if err != nil {
		c.t.Fatal(err)
	}
	c.api = api
	c.lis = bufconn.Listen(1 << 20)
	c.srv = grpc.NewServer(grpc.Creds(credentials.NewTLS(ServerTLS(c.pki.server, c.pki.pool))))
	agentv1.RegisterHostAgentServiceServer(c.srv, api)
	go func(srv *grpc.Server, lis net.Listener) { _ = srv.Serve(lis) }(c.srv, c.lis)
}

func (c *controller) stop() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.srv != nil {
		c.srv.Stop()
		c.srv = nil
	}
}

func (c *controller) server() *Server {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.api
}

// dial connects with a host certificate; it follows controller restarts.
func (c *controller) dial(t *testing.T, cert tls.Certificate) *grpc.ClientConn {
	t.Helper()
	conn, err := grpc.NewClient("passthrough:///controller",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			c.mu.Lock()
			lis, up := c.lis, c.srv != nil
			c.mu.Unlock()
			if !up {
				return nil, errors.New("controller down")
			}
			return lis.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(credentials.NewTLS(ClientTLS(cert, c.pki.pool, "controller"))))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

// --- agent ---

type agent struct {
	c       *Client
	applied chan State
	stop    context.CancelFunc
	done    chan struct{}
}

func runAgent(t *testing.T, conn *grpc.ClientConn, cache string, apply Applier) *agent {
	t.Helper()
	a := &agent{applied: make(chan State, 100), done: make(chan struct{})}
	if apply == nil {
		apply = func(context.Context, State) error { return nil }
	}
	c, err := NewClient(ClientOptions{Conn: conn, CachePath: cache, Clock: clock.Real(), Logger: logger, Version: "test",
		HeartbeatInterval: 50 * time.Millisecond, MinBackoff: 20 * time.Millisecond, MaxBackoff: 100 * time.Millisecond,
		Apply: func(ctx context.Context, st State) error {
			err := apply(ctx, st)
			if err == nil {
				a.applied <- st
			}
			return err
		}})
	if err != nil {
		t.Fatal(err)
	}
	a.c = c
	ctx, cancel := context.WithCancel(context.Background())
	a.stop = cancel
	go func() { defer close(a.done); _ = c.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-a.done })
	return a
}

// waitFor returns the first applied state satisfying cond.
func (a *agent) waitFor(t *testing.T, what string, cond func(State) bool) State {
	t.Helper()
	timeout := time.After(5 * time.Second)
	for {
		select {
		case st := <-a.applied:
			if cond(st) {
				return st
			}
		case <-timeout:
			t.Fatalf("timed out waiting for %s; last state %+v", what, a.c.State())
		}
	}
}

func value(st State, k Key) string {
	a, ok := st.Items[k]
	if !ok {
		return ""
	}
	v := &wrapperspb.StringValue{}
	_ = a.UnmarshalTo(v)
	return v.GetValue()
}

var (
	vm1 = Key{"instance", "ins-1"}
	vm2 = Key{"instance", "ins-2"}
)

// must fails the test if a Hub update fails.
func must(t *testing.T) func(uint64, error) {
	t.Helper()
	return func(_ uint64, err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
}

// --- tests ---

func TestHub(t *testing.T) {
	h := NewHub(2)
	r1, _ := h.Set(hostA, vm1, wrapperspb.String("a"))
	if r, _ := h.Set(hostA, vm1, wrapperspb.String("a")); r != r1 {
		t.Error("unchanged item bumped the revision")
	}
	must(t)(h.Set(hostA, vm2, wrapperspb.String("b")))
	must(t)(h.Delete(hostA, vm1))

	if evs, _ := h.since(hostA, "other-epoch", 3); len(evs) != 1 || evs[0].GetSnapshot() == nil {
		t.Error("foreign epoch must get a snapshot")
	}
	if evs, _ := h.since(hostA, h.Epoch(), 1); len(evs) != 2 || evs[0].GetRevision() != 2 || evs[1].GetDelta().GetDeletes()[0].GetId() != "ins-1" {
		t.Errorf("deltas after 1: %v", evs)
	}
	if evs, _ := h.since(hostA, h.Epoch(), 0); len(evs) != 1 || evs[0].GetSnapshot() == nil || len(evs[0].GetSnapshot().GetItems()) != 1 {
		t.Errorf("beyond history must get a snapshot: %v", evs)
	}
	if evs, _ := h.since(hostA, h.Epoch(), 3); len(evs) != 0 {
		t.Error("up-to-date agent got events")
	}
	must(t)(h.Replace(hostA, map[Key]proto.Message{vm1: wrapperspb.String("c")}))
	st := h.Revision(hostA)
	if evs, _ := h.since(hostA, h.Epoch(), 3); st != 4 || len(evs) != 1 || len(evs[0].GetDelta().GetDeletes()) != 1 || len(evs[0].GetDelta().GetUpserts()) != 1 {
		t.Errorf("Replace as one change: rev %d, %v", st, evs)
	}
}

// REQ-PLT-100: the agent receives its host's desired state; another host's
// state never reaches it.
func TestWatchOwnHostOnly(t *testing.T) {
	p := newPKI(t)
	hub := NewHub(0)
	must(t)(hub.Set(hostA, vm1, wrapperspb.String("a-1")))
	must(t)(hub.Set(hostB, vm2, wrapperspb.String("b-2")))
	ctl := newController(t, p, hub, nil)
	a := runAgent(t, ctl.dial(t, p.host(t, region, hostA)), filepath.Join(t.TempDir(), "cache.pb"), nil)

	a.waitFor(t, "initial state", func(st State) bool { return value(st, vm1) == "a-1" })
	must(t)(hub.Set(hostA, vm2, wrapperspb.String("a-2")))
	st := a.waitFor(t, "delta", func(st State) bool { return value(st, vm2) == "a-2" })
	if len(st.Items) != 2 || st.Epoch != hub.Epoch() || st.Revision != hub.Revision(hostA) {
		t.Errorf("state %+v", st)
	}
	must(t)(hub.Delete(hostA, vm1))
	a.waitFor(t, "deletion", func(st State) bool { _, ok := st.Items[vm1]; return !ok })
	for _, it := range a.c.State().Items {
		if v := &(wrapperspb.StringValue{}); it.UnmarshalTo(v) == nil && v.GetValue() == "b-2" {
			t.Error("received another host's item")
		}
	}
}

// Roadmap T-0.11 acceptance (1): after a broken stream the agent reconnects
// with its revision and receives only the delta.
func TestReconnectGetsDelta(t *testing.T) {
	p := newPKI(t)
	hub := NewHub(0)
	must(t)(hub.Set(hostA, vm1, wrapperspb.String("one")))
	ctl := newController(t, p, hub, nil)
	var snapshots atomic.Int32
	a := runAgent(t, ctl.dial(t, p.host(t, region, hostA)), filepath.Join(t.TempDir(), "cache.pb"), nil)
	count := func(ev *agentv1.DesiredStateEvent) {
		if ev.GetSnapshot() != nil {
			snapshots.Add(1)
		}
	}
	a.c.onEvent.Store(&count)
	a.waitFor(t, "initial state", func(st State) bool { return value(st, vm1) == "one" })
	snapshots.Store(0)

	ctl.stop() // stream breaks
	must(t)(hub.Set(hostA, vm2, wrapperspb.String("while down")))
	ctl.start()
	a.waitFor(t, "change made while disconnected", func(st State) bool { return value(st, vm2) == "while down" })
	if snapshots.Load() != 0 {
		t.Errorf("reconnect received %d snapshots, want a delta", snapshots.Load())
	}
}

// Roadmap T-0.11 acceptance (2): an agent starting without the control
// plane applies its cached desired state.
func TestStartWithoutServerAppliesCache(t *testing.T) {
	p := newPKI(t)
	hub := NewHub(0)
	must(t)(hub.Set(hostA, vm1, wrapperspb.String("cached")))
	ctl := newController(t, p, hub, nil)
	cache := filepath.Join(t.TempDir(), "agent", "desired-state.pb")
	first := runAgent(t, ctl.dial(t, p.host(t, region, hostA)), cache, nil)
	first.waitFor(t, "initial state", func(st State) bool { return value(st, vm1) == "cached" })
	first.stop()
	<-first.done

	ctl.stop() // control plane down
	second := runAgent(t, ctl.dial(t, p.host(t, region, hostA)), cache, nil)
	st := second.waitFor(t, "cached state", func(st State) bool { return value(st, vm1) == "cached" })
	if st.Revision != hub.Revision(hostA) || second.c.Connected() {
		t.Errorf("cached state %+v, connected %v", st, second.c.Connected())
	}
	if runtime.GOOS != "windows" {
		if fi, err := os.Stat(cache); err != nil || fi.Mode().Perm() != 0o600 {
			t.Errorf("cache mode %v (%v), want 0600", fi.Mode(), err)
		}
	}
}

// A new epoch (controller restart, shard moved) gives a snapshot, which
// removes items no longer desired.
func TestNewEpochResyncs(t *testing.T) {
	p := newPKI(t)
	hub := NewHub(0)
	must(t)(hub.Set(hostA, vm1, wrapperspb.String("old")))
	must(t)(hub.Set(hostA, vm2, wrapperspb.String("gone")))
	ctl := newController(t, p, hub, nil)
	a := runAgent(t, ctl.dial(t, p.host(t, region, hostA)), filepath.Join(t.TempDir(), "c.pb"), nil)
	a.waitFor(t, "initial", func(st State) bool { return len(st.Items) == 2 })

	ctl.stop()
	fresh := NewHub(0)
	must(t)(fresh.Set(hostA, vm1, wrapperspb.String("new")))
	ctl.opts.Hub = fresh
	ctl.start()
	st := a.waitFor(t, "snapshot of new epoch", func(st State) bool { return st.Epoch == fresh.Epoch() })
	if len(st.Items) != 1 || value(st, vm1) != "new" {
		t.Errorf("state after resync %+v", st)
	}
}

// Wrong region, no SPIFFE ID or no client certificate → no access.
func TestAuthentication(t *testing.T) {
	p := newPKI(t)
	ctl := newController(t, p, NewHub(0), nil)
	watch := func(cert tls.Certificate) error {
		stream, err := agentv1.NewHostAgentServiceClient(ctl.dial(t, cert)).Watch(context.Background(), &agentv1.WatchRequest{})
		if err == nil {
			_, err = stream.Recv()
		}
		return err
	}
	if err := watch(p.host(t, "eu-west1", hostA)); status.Code(err) != codes.Unauthenticated {
		t.Errorf("other region: %v", err)
	}
	if err := watch(p.issue(t, func(*x509.Certificate) {})); status.Code(err) != codes.Unauthenticated {
		t.Errorf("no SPIFFE ID: %v", err)
	}
	if err := watch(tls.Certificate{}); err == nil {
		t.Error("connection without a client certificate accepted")
	}
	other := newPKI(t) // certificate from a foreign CA
	if err := watch(other.host(t, region, hostA)); err == nil {
		t.Error("certificate of a foreign CA accepted")
	}
}

// A replica that does not own the host turns the agent away; it retries
// until it reaches the owner.
func TestNotOwnerRetries(t *testing.T) {
	p := newPKI(t)
	hub := NewHub(0)
	must(t)(hub.Set(hostA, vm1, wrapperspb.String("x")))
	var owns atomic.Bool
	ctl := newController(t, p, hub, func(o *ServerOptions) { o.Owns = func(string) bool { return owns.Load() } })
	a := runAgent(t, ctl.dial(t, p.host(t, region, hostA)), filepath.Join(t.TempDir(), "c.pb"), nil)
	time.Sleep(100 * time.Millisecond)
	if len(a.c.State().Items) != 0 {
		t.Fatal("state received from a non-owner")
	}
	owns.Store(true)
	a.waitFor(t, "state from owner", func(st State) bool { return value(st, vm1) == "x" })
}

// Heartbeats update LastSeen; item status and inventory reach the
// controller; a failed apply is retried.
func TestReportingAndApplyRetry(t *testing.T) {
	p := newPKI(t)
	hub := NewHub(0)
	must(t)(hub.Set(hostA, vm1, wrapperspb.String("x")))
	statuses := make(chan string, 10)
	inventory := make(chan string, 1)
	ctl := newController(t, p, hub, func(o *ServerOptions) {
		o.OnStatus = func(_ context.Context, host string, st *agentv1.ReportStatusRequest) {
			for _, it := range st.GetItems() {
				v := &wrapperspb.StringValue{}
				_ = it.GetStatus().UnmarshalTo(v)
				statuses <- host + "/" + it.GetKey().GetId() + "=" + v.GetValue()
			}
		}
		o.OnInventory = func(_ context.Context, host string, inv *agentv1.ReportInventoryRequest) error {
			v := &wrapperspb.StringValue{}
			_ = inv.GetDetails().UnmarshalTo(v)
			inventory <- host + ":" + v.GetValue()
			return nil
		}
	})
	var calls atomic.Int32
	a := runAgent(t, ctl.dial(t, p.host(t, region, hostA)), filepath.Join(t.TempDir(), "c.pb"),
		func(context.Context, State) error {
			if calls.Add(1) == 1 {
				return errors.New("libvirt busy")
			}
			return nil
		})
	a.waitFor(t, "apply retried after failure", func(st State) bool { return value(st, vm1) == "x" })

	a.c.ReportStatus(ItemStatus{Key: vm1, Status: wrapperspb.String("RUNNING")})
	select {
	case s := <-statuses:
		if s != hostA+"/ins-1=RUNNING" {
			t.Errorf("status %q", s)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("status not received")
	}
	if err := a.c.ReportInventory(context.Background(), wrapperspb.String("64 vCPU")); err != nil {
		t.Fatal(err)
	}
	if got := <-inventory; got != hostA+":64 vCPU" {
		t.Errorf("inventory %q", got)
	}
	if _, ok := ctl.server().LastSeen(hostA); !ok {
		t.Error("no heartbeat recorded")
	}
}

func TestCorruptCacheIsIgnored(t *testing.T) {
	path := filepath.Join(t.TempDir(), "c.pb")
	if err := os.WriteFile(path, []byte("garbage"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := loadCache(path); ok || err == nil {
		t.Error("corrupt cache accepted")
	}
	if _, ok, err := loadCache(filepath.Join(t.TempDir(), "missing")); ok || err != nil {
		t.Error("missing cache must be no state and no error")
	}
}

func TestParseHostURI(t *testing.T) {
	r, h, err := ParseHostURI(HostURI(region, hostA))
	if err != nil || r != region || h != hostA {
		t.Errorf("round trip: %s %s %v", r, h, err)
	}
	for _, s := range []string{"spiffe://qstack/ru/svc/x", "spiffe://other/ru/host/" + hostA, "https://qstack/ru/host/" + hostA, "spiffe://qstack/ru/host/ins-01m5hqpzg00000000000000000"} {
		u, _ := url.Parse(s)
		if _, _, err := ParseHostURI(u); err == nil {
			t.Errorf("%s accepted", s)
		}
	}
}
