// Package node is the sans-IO protocol core (SPEC.md §4.1, decision D2).
//
// A Node never touches sockets, clocks or goroutines. The runtime feeds it
// events with the current time and performs the actions it returns. This makes
// tests deterministic and lets the simulator run exactly this code.
//
// A Node is not safe for concurrent use: one goroutine must own it.
package node

import (
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"sort"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/satyams018/offline-mesh-chat/core/bundle"
	"github.com/satyams018/offline-mesh-chat/core/crypto"
	meshv1 "github.com/satyams018/offline-mesh-chat/proto/mesh/v1"
)

// ProtocolVersion is sent in Hello. Peers with another version are dropped.
const ProtocolVersion uint32 = 1

// MaxNameBytes bounds display names, which come from strangers.
const MaxNameBytes = 64

// DefaultMaxSeen bounds the dedup set so a flooder can't exhaust memory.
const DefaultMaxSeen = 100_000

// Errors reported through SendFailed.
var (
	ErrNotConnected = errors.New("recipient is not directly connected (store-and-forward arrives in milestone 2)")
	ErrSendToSelf   = errors.New("cannot send a message to yourself")
)

// Config configures a Node. Zero values get defaults in New.
type Config struct {
	Self     crypto.NodeID
	Name     string
	MaxHops  uint32        // default bundle.DefaultMaxHops
	Lifetime time.Duration // default bundle.DefaultLifetime
	MaxSeen  int           // default DefaultMaxSeen
	Rand     io.Reader     // default crypto/rand.Reader
}

type peer struct {
	outbound bool
	ready    bool // Hello received and accepted
	info     PeerInfo
}

// Node is the protocol state machine.
type Node struct {
	cfg   Config
	peers map[PeerHandle]*peer
	byID  map[crypto.NodeID]PeerHandle // ready peers only
	seen  map[bundle.ID]time.Time      // bundle ID -> when we may forget it
}

// New validates cfg and returns a Node.
func New(cfg Config) (*Node, error) {
	if err := ValidateName(cfg.Name); err != nil {
		return nil, err
	}
	if cfg.MaxHops == 0 {
		cfg.MaxHops = bundle.DefaultMaxHops
	}
	if cfg.Lifetime == 0 {
		cfg.Lifetime = bundle.DefaultLifetime
	}
	if cfg.MaxSeen == 0 {
		cfg.MaxSeen = DefaultMaxSeen
	}
	if cfg.Rand == nil {
		cfg.Rand = rand.Reader
	}
	return &Node{
		cfg:   cfg,
		peers: make(map[PeerHandle]*peer),
		byID:  make(map[crypto.NodeID]PeerHandle),
		seen:  make(map[bundle.ID]time.Time),
	}, nil
}

// ValidateName checks a display name: 1–64 bytes of UTF-8 with no control
// characters, so a hostile peer can't inject terminal escape sequences.
func ValidateName(name string) error {
	if name == "" || len(name) > MaxNameBytes || !utf8.ValidString(name) {
		return fmt.Errorf("display name must be 1–%d bytes of valid UTF-8", MaxNameBytes)
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return errors.New("display name must not contain control characters")
		}
	}
	return nil
}

// Peers lists identified peers, sorted by name.
func (n *Node) Peers() []PeerInfo {
	out := make([]PeerInfo, 0, len(n.byID))
	for _, h := range n.byID {
		out = append(out, n.peers[h].info)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Handle consumes one event and returns the actions to perform, in order.
func (n *Node) Handle(now time.Time, ev Event) []Action {
	switch ev := ev.(type) {
	case PeerUp:
		n.peers[ev.Peer] = &peer{outbound: ev.Outbound}
		return []Action{SendFrame{Peer: ev.Peer, Frame: n.hello()}}
	case PeerDown:
		return n.handlePeerDown(ev.Peer)
	case FrameReceived:
		return n.handleFrame(now, ev.Peer, ev.Frame)
	case Tick:
		n.pruneSeen(now)
		return nil
	case AppSend:
		return n.handleAppSend(now, ev)
	default:
		panic(fmt.Sprintf("node: unknown event %T", ev)) // programming error
	}
}

func (n *Node) hello() *meshv1.Frame {
	return &meshv1.Frame{Kind: &meshv1.Frame_Hello{Hello: &meshv1.Hello{
		ProtocolVersion: ProtocolVersion,
		NodeId:          n.cfg.Self[:],
		DisplayName:     n.cfg.Name,
	}}}
}

func (n *Node) handlePeerDown(h PeerHandle) []Action {
	p, ok := n.peers[h]
	if !ok {
		return nil // already replaced by a duplicate-connection decision
	}
	delete(n.peers, h)
	if p.ready && n.byID[p.info.ID] == h {
		delete(n.byID, p.info.ID)
		return []Action{PeerGone{Info: p.info}}
	}
	return nil
}

func (n *Node) handleFrame(now time.Time, h PeerHandle, f *meshv1.Frame) []Action {
	p, ok := n.peers[h]
	if !ok {
		return nil // frame raced with a disconnect
	}
	if hello := f.GetHello(); hello != nil {
		return n.handleHello(h, p, hello)
	}
	if !p.ready {
		return n.drop(h, "frame before hello")
	}
	switch k := f.GetKind().(type) {
	case *meshv1.Frame_Bundle:
		return n.handleBundle(now, h, k.Bundle)
	case *meshv1.Frame_Summary, *meshv1.Frame_Request:
		return nil // contact protocol arrives in milestone 2
	default:
		return n.drop(h, "unknown frame")
	}
}

func (n *Node) handleHello(h PeerHandle, p *peer, hello *meshv1.Hello) []Action {
	if p.ready {
		return n.drop(h, "duplicate hello")
	}
	if v := hello.GetProtocolVersion(); v != ProtocolVersion {
		return n.drop(h, fmt.Sprintf("unsupported protocol version %d", v))
	}
	id, err := crypto.NodeIDFromBytes(hello.GetNodeId())
	if err != nil {
		return n.drop(h, "bad node id")
	}
	if id == n.cfg.Self {
		return n.drop(h, "connected to self")
	}
	if err := ValidateName(hello.GetDisplayName()); err != nil {
		return n.drop(h, "bad display name")
	}
	p.info = PeerInfo{ID: id, Name: hello.GetDisplayName()}

	if old, dup := n.byID[id]; dup {
		return n.resolveDuplicate(h, p, old)
	}
	p.ready = true
	n.byID[id] = h
	return []Action{PeerReady{Peer: h, Info: p.info}}
}

// resolveDuplicate handles two connections to the same peer, which happens
// when both sides dial each other at once. Both sides must close the *same*
// connection, so the rule uses only facts both agree on: keep the connection
// dialed by the smaller node ID.
func (n *Node) resolveDuplicate(newH PeerHandle, newP *peer, oldH PeerHandle) []Action {
	oldP := n.peers[oldH]
	preferred := n.cfg.Self
	if newP.info.ID.Less(preferred) {
		preferred = newP.info.ID
	}
	dialer := func(p *peer) crypto.NodeID {
		if p.outbound {
			return n.cfg.Self
		}
		return p.info.ID
	}
	if dialer(newP) == preferred && dialer(oldP) != preferred {
		delete(n.peers, oldH) // its PeerDown becomes a no-op
		newP.ready = true
		n.byID[newP.info.ID] = newH
		return []Action{DisconnectPeer{Peer: oldH, Reason: "duplicate connection"}}
	}
	return n.drop(newH, "duplicate connection")
}

func (n *Node) handleBundle(now time.Time, h PeerHandle, pb *meshv1.Bundle) []Action {
	b, err := bundle.FromProto(pb)
	if err != nil {
		return n.drop(h, "malformed bundle")
	}
	if b.Validate(n.cfg.MaxHops) != nil {
		return nil // expired, too many hops, …: drop the bundle, keep the link
	}
	id := b.ID()
	if _, dup := n.seen[id]; dup {
		return nil
	}
	n.markSeen(now, id, b.RemainingLifetime())
	if b.Destination != n.cfg.Self {
		return nil // relaying arrives in milestone 2
	}
	msg, err := bundle.OpenPlain(b)
	if err != nil {
		return nil
	}
	return []Action{DeliverToApp{Message: msg}}
}

func (n *Node) handleAppSend(now time.Time, ev AppSend) []Action {
	fail := func(err error) []Action { return []Action{SendFailed{To: ev.To, Err: err}} }
	if ev.To == n.cfg.Self {
		return fail(ErrSendToSelf)
	}
	if err := bundle.ValidateBody(ev.Body); err != nil {
		return fail(err)
	}
	h, ok := n.byID[ev.To]
	if !ok {
		return fail(ErrNotConnected)
	}
	mid, err := bundle.NewMessageID(n.cfg.Rand)
	if err != nil {
		return fail(err)
	}
	msg := bundle.Message{Sender: n.cfg.Self, SentAt: now, ID: mid, Body: ev.Body}
	b, err := bundle.NewPlain(ev.To, msg, n.cfg.Lifetime)
	if err != nil {
		return fail(err)
	}
	n.markSeen(now, b.ID(), n.cfg.Lifetime)
	b.HopCount = 1 // the copy on the wire has travelled one hop when it arrives
	return []Action{SendFrame{Peer: h, Frame: &meshv1.Frame{Kind: &meshv1.Frame_Bundle{Bundle: b.ToProto()}}}}
}

// drop forgets a connection and tells the runtime to close it.
func (n *Node) drop(h PeerHandle, reason string) []Action {
	delete(n.peers, h)
	return []Action{DisconnectPeer{Peer: h, Reason: reason}}
}

// markSeen remembers id until its lifetime ends. When the set is full it
// forgets the entry that expires soonest (O(n), but only under flooding).
func (n *Node) markSeen(now time.Time, id bundle.ID, ttl time.Duration) {
	if len(n.seen) >= n.cfg.MaxSeen {
		var victim bundle.ID
		var earliest time.Time
		first := true
		for k, exp := range n.seen {
			if first || exp.Before(earliest) {
				victim, earliest, first = k, exp, false
			}
		}
		delete(n.seen, victim)
	}
	n.seen[id] = now.Add(ttl)
}

func (n *Node) pruneSeen(now time.Time) {
	for id, exp := range n.seen {
		if !now.Before(exp) {
			delete(n.seen, id)
		}
	}
}
