package agentproto

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"sync"
	"sync/atomic"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"

	agentv1 "github.com/mindevis/qstack/gen/go/qstack/agent/internalv1"
	"github.com/mindevis/qstack/pkg/clock"
)

// State is a host's desired state at a revision.
type State struct {
	Epoch    string
	Revision uint64
	Items    map[Key]*anypb.Any
}

func (st State) clone() State {
	c := State{Epoch: st.Epoch, Revision: st.Revision, Items: make(map[Key]*anypb.Any, len(st.Items))}
	for k, v := range st.Items {
		c.Items[k] = proto.Clone(v).(*anypb.Any)
	}
	return c
}

func (st State) snapshotEvent() *agentv1.DesiredStateEvent {
	snap := &agentv1.Snapshot{}
	for k, a := range st.Items {
		snap.Items = append(snap.Items, &agentv1.Item{Key: k.proto(), Spec: a})
	}
	return &agentv1.DesiredStateEvent{Epoch: st.Epoch, Revision: st.Revision,
		Event: &agentv1.DesiredStateEvent_Snapshot{Snapshot: snap}}
}

func stateFromSnapshot(ev *agentv1.DesiredStateEvent) State {
	st := State{Epoch: ev.GetEpoch(), Revision: ev.GetRevision(), Items: map[Key]*anypb.Any{}}
	for _, it := range ev.GetSnapshot().GetItems() {
		st.Items[keyOf(it.GetKey())] = it.GetSpec()
	}
	return st
}

// Applier makes the host match the desired state. It is level-triggered:
// called with the complete state after every change and again after a
// failure, it must be idempotent and remove what the state no longer lists.
type Applier func(ctx context.Context, st State) error

// ItemStatus is the observed status of one item.
type ItemStatus struct {
	Key    Key
	Status proto.Message
}

// ClientOptions configures a Client.
type ClientOptions struct {
	// Conn reaches the controller with the host certificate (ClientTLS).
	Conn grpc.ClientConnInterface
	// CachePath is the desired-state cache file, e.g.
	// /var/lib/qstack/compute-agent/desired-state.pb.
	CachePath string
	Apply     Applier
	Version   string
	Clock     clock.Clock
	Logger    *slog.Logger
	// HeartbeatInterval defaults to 10 s (spec 02 §10); MinBackoff and
	// MaxBackoff bound reconnect delays (default 1 s and 30 s, jittered).
	HeartbeatInterval      time.Duration
	MinBackoff, MaxBackoff time.Duration
}

// Client is the agent side of the protocol.
type Client struct {
	opts ClientOptions
	svc  agentv1.HostAgentServiceClient

	mu        sync.Mutex
	state     State
	applyOK   bool
	statuses  chan []ItemStatus
	connected bool

	onEvent atomic.Pointer[func(*agentv1.DesiredStateEvent)] // tests observe events
}

// NewClient returns a Client.
func NewClient(opts ClientOptions) (*Client, error) {
	if opts.Conn == nil || opts.CachePath == "" || opts.Apply == nil || opts.Clock == nil || opts.Logger == nil {
		return nil, errors.New("agentproto: conn, cache path, applier, clock and logger are required")
	}
	if opts.HeartbeatInterval <= 0 {
		opts.HeartbeatInterval = 10 * time.Second
	}
	if opts.MinBackoff <= 0 {
		opts.MinBackoff = time.Second
	}
	if opts.MaxBackoff < opts.MinBackoff {
		opts.MaxBackoff = max(30*time.Second, opts.MinBackoff)
	}
	return &Client{opts: opts, svc: agentv1.NewHostAgentServiceClient(opts.Conn),
		state: State{Items: map[Key]*anypb.Any{}}, statuses: make(chan []ItemStatus, 64)}, nil
}

// State returns a copy of the current desired state.
func (c *Client) State() State {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.state.clone()
}

// Connected reports whether a Watch stream is open (for the agent's
// metrics: connection state, REQ-OBS-002).
func (c *Client) Connected() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.connected
}

// Run applies the cached desired state at once — so a host starting
// without the control plane keeps its workloads (static stability) — then
// watches the controller, reconnecting with the last applied revision
// until ctx is cancelled.
func (c *Client) Run(ctx context.Context) error {
	if st, ok, err := loadCache(c.opts.CachePath); err != nil {
		c.opts.Logger.ErrorContext(ctx, "agentproto: ignoring desired-state cache", "error", err)
	} else if ok {
		c.mu.Lock()
		c.state = st
		c.mu.Unlock()
		c.opts.Logger.InfoContext(ctx, "agentproto: applying cached desired state", "revision", st.Revision, "items", len(st.Items))
		c.apply(ctx)
	}

	var wg sync.WaitGroup
	defer wg.Wait()
	wg.Go(func() { c.reportLoop(ctx) })

	delay := c.opts.MinBackoff
	for {
		progressed, err := c.watch(ctx)
		c.setConnected(false)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if progressed {
			delay = c.opts.MinBackoff
		}
		c.opts.Logger.WarnContext(ctx, "agentproto: watch interrupted; reconnecting", "error", err, "retry_in", delay)
		if !sleep(ctx, jitter(delay)) {
			return ctx.Err()
		}
		delay = min(delay*2, c.opts.MaxBackoff)
	}
}

// watch runs one Watch stream; progressed reports whether any event arrived.
func (c *Client) watch(ctx context.Context) (progressed bool, err error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	cur := c.State()
	stream, err := c.svc.Watch(ctx, &agentv1.WatchRequest{Epoch: cur.Epoch, LastRevision: cur.Revision, AgentVersion: c.opts.Version})
	if err != nil {
		return false, err
	}
	for {
		ev, err := stream.Recv()
		if err != nil {
			return progressed, err
		}
		c.setConnected(true)
		if err := c.handle(ctx, ev); err != nil {
			return progressed, err
		}
		progressed = true
	}
}

// errResync drops the local revision so that the next Watch gets a snapshot.
var errResync = errors.New("agentproto: revision gap; resyncing")

func (c *Client) handle(ctx context.Context, ev *agentv1.DesiredStateEvent) error {
	if f := c.onEvent.Load(); f != nil {
		(*f)(ev)
	}
	c.mu.Lock()
	next := c.state.clone()
	c.mu.Unlock()
	switch e := ev.GetEvent().(type) {
	case *agentv1.DesiredStateEvent_Snapshot:
		next = stateFromSnapshot(ev)
	case *agentv1.DesiredStateEvent_Delta:
		if ev.GetEpoch() != next.Epoch || ev.GetRevision() != next.Revision+1 {
			c.mu.Lock()
			c.state.Epoch, c.state.Revision = "", 0
			c.mu.Unlock()
			return errResync
		}
		for _, it := range e.Delta.GetUpserts() {
			next.Items[keyOf(it.GetKey())] = it.GetSpec()
		}
		for _, k := range e.Delta.GetDeletes() {
			delete(next.Items, keyOf(k))
		}
		next.Revision = ev.GetRevision()
	default:
		return fmt.Errorf("agentproto: unknown event at revision %d", ev.GetRevision())
	}
	// Persist before applying: after a restart the host converges to the
	// newest desired state even without the control plane.
	if err := saveCache(c.opts.CachePath, next); err != nil {
		c.opts.Logger.ErrorContext(ctx, "agentproto: cache not saved", "error", err)
	}
	c.mu.Lock()
	c.state = next
	c.mu.Unlock()
	c.apply(ctx)
	return nil
}

func (c *Client) apply(ctx context.Context) {
	st := c.State()
	err := c.opts.Apply(ctx, st)
	c.mu.Lock()
	c.applyOK = err == nil
	c.mu.Unlock()
	if err != nil {
		c.opts.Logger.WarnContext(ctx, "agentproto: apply failed; retrying at next heartbeat", "revision", st.Revision, "error", err)
	}
}

// ReportStatus queues item status for the next status message (sent at once).
func (c *Client) ReportStatus(items ...ItemStatus) {
	select {
	case c.statuses <- items:
	default: // full: the next heartbeat-driven re-apply reports again
	}
}

// ReportInventory sends the host inventory.
func (c *Client) ReportInventory(ctx context.Context, details proto.Message) error {
	a, err := anypb.New(details)
	if err != nil {
		return err
	}
	_, err = c.svc.ReportInventory(ctx, &agentv1.ReportInventoryRequest{AgentVersion: c.opts.Version, Details: a})
	return err
}

// reportLoop keeps a ReportStatus stream: a heartbeat every interval and
// queued item status; it also retries a failed apply on each heartbeat.
func (c *Client) reportLoop(ctx context.Context) {
	delay := c.opts.MinBackoff
	for ctx.Err() == nil {
		err := c.reportOnce(ctx)
		if ctx.Err() != nil {
			return
		}
		c.opts.Logger.DebugContext(ctx, "agentproto: status stream interrupted", "error", err)
		if !sleep(ctx, jitter(delay)) {
			return
		}
		delay = min(delay*2, c.opts.MaxBackoff)
	}
}

func (c *Client) reportOnce(ctx context.Context) error {
	stream, err := c.svc.ReportStatus(ctx)
	if err != nil {
		return err
	}
	tick := time.NewTicker(c.opts.HeartbeatInterval)
	defer tick.Stop()
	send := func(items []ItemStatus) error {
		st := c.State()
		msg := &agentv1.ReportStatusRequest{AppliedRevision: st.Revision, Epoch: st.Epoch}
		for _, it := range items {
			a, err := anypb.New(it.Status)
			if err != nil {
				return err
			}
			msg.Items = append(msg.Items, &agentv1.ItemStatus{Key: it.Key.proto(), Status: a})
		}
		return stream.Send(msg)
	}
	if err := send(nil); err != nil {
		return err
	}
	for {
		select {
		case <-ctx.Done():
			_, _ = stream.CloseAndRecv()
			return ctx.Err()
		case items := <-c.statuses:
			if err := send(items); err != nil {
				return err
			}
		case <-tick.C:
			c.mu.Lock()
			retry := !c.applyOK && len(c.state.Items) > 0
			c.mu.Unlock()
			if retry {
				c.apply(ctx)
			}
			if err := send(nil); err != nil {
				return err
			}
		}
	}
}

func (c *Client) setConnected(v bool) {
	c.mu.Lock()
	c.connected = v
	c.mu.Unlock()
}

func jitter(d time.Duration) time.Duration {
	return d/2 + time.Duration(rand.Int64N(int64(d/2)+1)) //nolint:gosec // jitter needs no crypto randomness
}

func sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
