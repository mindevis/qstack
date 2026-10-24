package agentproto

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"time"

	agentv1 "github.com/mindevis/qstack/gen/go/qstack/agent/internalv1"
	"github.com/mindevis/qstack/pkg/apierr"
	"github.com/mindevis/qstack/pkg/clock"
)

// ServerOptions configures a Server.
type ServerOptions struct {
	// Region of this control plane; host certificates of other regions are
	// rejected.
	Region string
	Hub    *Hub
	// Owns reports whether this replica serves host (its shard lease,
	// pkg/reconcile); nil serves every host. Agents of other hosts get
	// UNAVAILABLE and reconnect, reaching another replica.
	Owns func(host string) bool
	// OnStatus receives heartbeats and item status; OnInventory the inventory.
	OnStatus    func(ctx context.Context, host string, st *agentv1.ReportStatusRequest)
	OnInventory func(ctx context.Context, host string, inv *agentv1.ReportInventoryRequest) error
	Clock       clock.Clock
	Logger      *slog.Logger
	// OwnershipCheck is how often an open Watch re-checks Owns; default 5s.
	OwnershipCheck time.Duration
}

// Server implements HostAgentService.
type Server struct {
	agentv1.UnimplementedHostAgentServiceServer
	opts ServerOptions

	mu       sync.Mutex
	lastSeen map[string]time.Time
}

// NewServer returns a Server.
func NewServer(opts ServerOptions) (*Server, error) {
	if opts.Region == "" || opts.Hub == nil || opts.Clock == nil || opts.Logger == nil {
		return nil, errors.New("agentproto: region, hub, clock and logger are required")
	}
	if opts.OwnershipCheck <= 0 {
		opts.OwnershipCheck = 5 * time.Second
	}
	return &Server{opts: opts, lastSeen: map[string]time.Time{}}, nil
}

func (s *Server) owns(host string) bool { return s.opts.Owns == nil || s.opts.Owns(host) }

var errNotOwner = apierr.Unavailable("host is served by another controller replica; reconnect")

// Watch implements HostAgentService.
func (s *Server) Watch(req *agentv1.WatchRequest, stream agentv1.HostAgentService_WatchServer) error {
	ctx := stream.Context()
	host, err := HostFromContext(ctx, s.opts.Region)
	if err != nil {
		return err
	}
	if !s.owns(host) {
		return errNotOwner
	}
	s.seen(host)
	epoch, rev := req.GetEpoch(), req.GetLastRevision()
	tick := time.NewTicker(s.opts.OwnershipCheck)
	defer tick.Stop()
	for {
		evs, changed := s.opts.Hub.since(host, epoch, rev)
		for _, ev := range evs {
			if err := stream.Send(ev); err != nil {
				return err
			}
			epoch, rev = ev.GetEpoch(), ev.GetRevision()
		}
		select {
		case <-ctx.Done():
			return nil
		case <-changed:
		case <-tick.C:
			if !s.owns(host) {
				return errNotOwner
			}
		}
	}
}

// ReportStatus implements HostAgentService.
func (s *Server) ReportStatus(stream agentv1.HostAgentService_ReportStatusServer) error {
	ctx := stream.Context()
	host, err := HostFromContext(ctx, s.opts.Region)
	if err != nil {
		return err
	}
	for {
		st, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			return stream.SendAndClose(&agentv1.ReportStatusResponse{})
		}
		if err != nil {
			return err
		}
		s.seen(host)
		if s.opts.OnStatus != nil {
			s.opts.OnStatus(ctx, host, st)
		}
	}
}

// ReportInventory implements HostAgentService.
func (s *Server) ReportInventory(ctx context.Context, req *agentv1.ReportInventoryRequest) (*agentv1.ReportInventoryResponse, error) {
	host, err := HostFromContext(ctx, s.opts.Region)
	if err != nil {
		return nil, err
	}
	s.seen(host)
	if s.opts.OnInventory != nil {
		if err := s.opts.OnInventory(ctx, host, req); err != nil {
			return nil, err
		}
	}
	return &agentv1.ReportInventoryResponse{}, nil
}

func (s *Server) seen(host string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastSeen[host] = s.opts.Clock.Now()
}

// LastSeen is when the host last called this replica. The controller marks
// a host UNREACHABLE after 60 s without a heartbeat (spec 02 §10).
func (s *Server) LastSeen(host string) (time.Time, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.lastSeen[host]
	return t, ok
}
