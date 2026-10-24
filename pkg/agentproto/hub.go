package agentproto

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sync"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"

	agentv1 "github.com/mindevis/qstack/gen/go/qstack/agent/internalv1"
)

// Key identifies a desired-state item of a host.
type Key struct {
	Kind string // e.g. "instance"
	ID   string // e.g. "ins-…"
}

func (k Key) proto() *agentv1.ItemKey { return &agentv1.ItemKey{Kind: k.Kind, Id: k.ID} }

func keyOf(k *agentv1.ItemKey) Key { return Key{Kind: k.GetKind(), ID: k.GetId()} }

// DefaultHistory is how many changes per host the Hub keeps for deltas;
// an agent further behind gets a snapshot.
const DefaultHistory = 1024

// Hub holds the desired state of every host served by this controller
// replica. Each change of a host's state bumps its revision; revisions are
// valid within the Hub's epoch, which is new for every Hub, so an agent
// coming from another replica or a restarted controller gets a snapshot.
type Hub struct {
	epoch   string
	history int

	mu    sync.Mutex
	hosts map[string]*hostState
}

type hostState struct {
	revision uint64
	items    map[Key]*anypb.Any
	log      []change // ascending revisions, at most history entries
	changed  chan struct{}
}

type change struct {
	revision uint64
	upserts  map[Key]*anypb.Any
	deletes  []Key
}

// NewHub returns an empty Hub; history ≤ 0 means DefaultHistory.
func NewHub(history int) *Hub {
	if history <= 0 {
		history = DefaultHistory
	}
	var b [8]byte
	_, _ = rand.Read(b[:])
	return &Hub{epoch: hex.EncodeToString(b[:]), history: history, hosts: map[string]*hostState{}}
}

// Epoch identifies this Hub's revision sequence.
func (h *Hub) Epoch() string { return h.epoch }

func (h *Hub) host(id string) *hostState {
	hs, ok := h.hosts[id]
	if !ok {
		hs = &hostState{items: map[Key]*anypb.Any{}, changed: make(chan struct{})}
		h.hosts[id] = hs
	}
	return hs
}

// Revision is the current revision of a host's desired state.
func (h *Hub) Revision(host string) uint64 {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.host(host).revision
}

// Update applies upserts and deletes to a host's desired state as one
// change. Items equal to the current ones are skipped; if nothing changes
// the revision stays.
func (h *Hub) Update(host string, upserts map[Key]proto.Message, deletes []Key) (uint64, error) {
	packed, err := pack(upserts)
	if err != nil {
		return 0, err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.updateLocked(h.host(host), packed, deletes), nil
}

// Set upserts one item.
func (h *Hub) Set(host string, k Key, m proto.Message) (uint64, error) {
	return h.Update(host, map[Key]proto.Message{k: m}, nil)
}

// Delete removes one item.
func (h *Hub) Delete(host string, k Key) (uint64, error) {
	return h.Update(host, nil, []Key{k})
}

// Replace makes items the complete desired state of host (e.g. after the
// controller rebuilt it from its database) as one change.
func (h *Hub) Replace(host string, items map[Key]proto.Message) (uint64, error) {
	packed, err := pack(items)
	if err != nil {
		return 0, err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	hs := h.host(host)
	var gone []Key
	for k := range hs.items {
		if _, keep := packed[k]; !keep {
			gone = append(gone, k)
		}
	}
	return h.updateLocked(hs, packed, gone), nil
}

func pack(items map[Key]proto.Message) (map[Key]*anypb.Any, error) {
	packed := make(map[Key]*anypb.Any, len(items))
	for k, m := range items {
		if k.Kind == "" || k.ID == "" {
			return nil, errors.New("agentproto: empty item key")
		}
		a, err := anypb.New(m)
		if err != nil {
			return nil, err
		}
		packed[k] = a
	}
	return packed, nil
}

func (h *Hub) updateLocked(hs *hostState, packed map[Key]*anypb.Any, deletes []Key) uint64 {
	c := change{upserts: map[Key]*anypb.Any{}}
	for k, a := range packed {
		if cur, ok := hs.items[k]; !ok || !proto.Equal(cur, a) {
			c.upserts[k] = a
		}
	}
	for _, k := range deletes {
		if _, ok := hs.items[k]; ok {
			if _, up := c.upserts[k]; !up {
				c.deletes = append(c.deletes, k)
			}
		}
	}
	if len(c.upserts) == 0 && len(c.deletes) == 0 {
		return hs.revision
	}
	hs.revision++
	c.revision = hs.revision
	for k, a := range c.upserts {
		hs.items[k] = a
	}
	for _, k := range c.deletes {
		delete(hs.items, k)
	}
	hs.log = append(hs.log, c)
	if len(hs.log) > h.history {
		hs.log = hs.log[len(hs.log)-h.history:]
	}
	close(hs.changed)
	hs.changed = make(chan struct{})
	return hs.revision
}

// since returns the events that bring an agent at (epoch, revision) to the
// current state — a snapshot, or the deltas after revision — and a channel
// closed at the next change.
func (h *Hub) since(host, epoch string, revision uint64) ([]*agentv1.DesiredStateEvent, <-chan struct{}) {
	h.mu.Lock()
	defer h.mu.Unlock()
	hs := h.host(host)
	if epoch == h.epoch && revision == hs.revision {
		return nil, hs.changed
	}
	covered := epoch == h.epoch && revision < hs.revision &&
		len(hs.log) > 0 && hs.log[0].revision <= revision+1
	if !covered {
		snap := &agentv1.Snapshot{}
		for k, a := range hs.items {
			snap.Items = append(snap.Items, &agentv1.Item{Key: k.proto(), Spec: a})
		}
		return []*agentv1.DesiredStateEvent{{Epoch: h.epoch, Revision: hs.revision,
			Event: &agentv1.DesiredStateEvent_Snapshot{Snapshot: snap}}}, hs.changed
	}
	var evs []*agentv1.DesiredStateEvent
	for _, c := range hs.log {
		if c.revision <= revision {
			continue
		}
		d := &agentv1.Delta{}
		for k, a := range c.upserts {
			d.Upserts = append(d.Upserts, &agentv1.Item{Key: k.proto(), Spec: a})
		}
		for _, k := range c.deletes {
			d.Deletes = append(d.Deletes, k.proto())
		}
		evs = append(evs, &agentv1.DesiredStateEvent{Epoch: h.epoch, Revision: c.revision,
			Event: &agentv1.DesiredStateEvent_Delta{Delta: d}})
	}
	return evs, hs.changed
}
