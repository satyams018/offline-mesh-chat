package node

import (
	"crypto/rand"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/satyams018/offline-mesh-chat/core/bundle"
	"github.com/satyams018/offline-mesh-chat/core/crypto"
	meshv1 "github.com/satyams018/offline-mesh-chat/proto/mesh/v1"
)

var t0 = time.Date(2026, 11, 23, 10, 0, 0, 0, time.UTC)

func newNode(t *testing.T, name string) *Node {
	t.Helper()
	id, err := crypto.NewIdentity(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	n, err := New(Config{Self: id.ID(), Name: name})
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// pair wires two nodes over one simulated link (handle 1 on each side) and
// delivers SendFrame actions synchronously: a two-node memnet.
type pair struct {
	a, b *Node
	now  time.Time
	got  map[*Node][]Action // non-SendFrame actions each node produced
}

func connect(t *testing.T) *pair {
	t.Helper()
	p := &pair{a: newNode(t, "alice"), b: newNode(t, "bob"), now: t0, got: map[*Node][]Action{}}
	// Like the runtime: both ends register the connection before frames flow.
	upA := p.a.Handle(p.now, PeerUp{Peer: 1, Outbound: true})
	upB := p.b.Handle(p.now, PeerUp{Peer: 1, Outbound: false})
	p.deliver(p.a, upA)
	p.deliver(p.b, upB)
	return p
}

func (p *pair) other(n *Node) *Node {
	if n == p.a {
		return p.b
	}
	return p.a
}

func (p *pair) feed(n *Node, ev Event) { p.deliver(n, n.Handle(p.now, ev)) }

func (p *pair) deliver(n *Node, acts []Action) {
	for _, act := range acts {
		if sf, ok := act.(SendFrame); ok {
			p.feed(p.other(n), FrameReceived(sf))
			continue
		}
		p.got[n] = append(p.got[n], act)
	}
}

func (p *pair) take(n *Node) []Action {
	acts := p.got[n]
	delete(p.got, n)
	return acts
}

func TestHelloMakesPeersReady(t *testing.T) {
	p := connect(t)
	for n, wantName := range map[*Node]string{p.a: "bob", p.b: "alice"} {
		acts := p.take(n)
		if len(acts) != 1 {
			t.Fatalf("%s: got %v, want one PeerReady", n.cfg.Name, acts)
		}
		ready, ok := acts[0].(PeerReady)
		if !ok || ready.Info.Name != wantName || ready.Info.ID != p.other(n).cfg.Self {
			t.Errorf("%s: got %+v", n.cfg.Name, acts[0])
		}
		if peers := n.Peers(); len(peers) != 1 || peers[0].Name != wantName {
			t.Errorf("%s: Peers() = %+v", n.cfg.Name, peers)
		}
	}
}

func TestDirectMessageIsDeliveredOnce(t *testing.T) {
	p := connect(t)
	p.take(p.a)
	p.take(p.b)

	p.feed(p.a, AppSend{To: p.b.cfg.Self, Body: "chai at 5?"})
	acts := p.take(p.b)
	if len(acts) != 1 {
		t.Fatalf("bob got %v, want one DeliverToApp", acts)
	}
	d := acts[0].(DeliverToApp)
	if d.Message.Body != "chai at 5?" || d.Message.Sender != p.a.cfg.Self || !d.Message.SentAt.Equal(t0) {
		t.Errorf("delivered %+v", d.Message)
	}
	if got := p.take(p.a); len(got) != 0 {
		t.Errorf("alice got unexpected actions %v", got)
	}
}

func TestDuplicateBundleIsDropped(t *testing.T) {
	a, b := newNode(t, "alice"), newNode(t, "bob")
	b.Handle(t0, PeerUp{Peer: 7})
	b.Handle(t0, FrameReceived{Peer: 7, Frame: a.hello()})

	bf := bundleFrame(t, a.cfg.Self, b.cfg.Self, func(*bundle.Bundle) {})
	if acts := b.Handle(t0, FrameReceived{Peer: 7, Frame: bf}); len(acts) != 1 {
		t.Fatalf("first copy: %v", acts)
	}
	if acts := b.Handle(t0, FrameReceived{Peer: 7, Frame: bf}); len(acts) != 0 {
		t.Fatalf("replayed copy was not dropped: %v", acts)
	}
}

func TestInvalidBundlesAreDroppedButLinkStays(t *testing.T) {
	tests := map[string]func(*bundle.Bundle){
		"expired":   func(b *bundle.Bundle) { b.AgeMs = b.LifetimeMs },
		"hop limit": func(b *bundle.Bundle) { b.HopCount = bundle.DefaultMaxHops },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			a, b := newNode(t, "alice"), newNode(t, "bob")
			b.Handle(t0, PeerUp{Peer: 7})
			b.Handle(t0, FrameReceived{Peer: 7, Frame: a.hello()})
			acts := b.Handle(t0, FrameReceived{Peer: 7, Frame: bundleFrame(t, a.cfg.Self, b.cfg.Self, mutate)})
			if len(acts) != 0 {
				t.Errorf("got %v, want the bundle silently dropped", acts)
			}
		})
	}
}

func TestProtocolViolationsDropTheLink(t *testing.T) {
	alice := newNode(t, "alice")
	badHello := func(mutate func(*meshv1.Hello)) *meshv1.Frame {
		f := alice.hello()
		mutate(f.GetHello())
		return f
	}
	tests := []struct {
		name   string
		frames func(self *Node) []*meshv1.Frame
		reason string
	}{
		{"frame before hello", func(*Node) []*meshv1.Frame {
			return []*meshv1.Frame{{Kind: &meshv1.Frame_Summary{Summary: &meshv1.SummaryVector{}}}}
		}, "before hello"},
		{"empty frame", func(*Node) []*meshv1.Frame { return []*meshv1.Frame{alice.hello(), {}} }, "unknown frame"},
		{"duplicate hello", func(*Node) []*meshv1.Frame { return []*meshv1.Frame{alice.hello(), alice.hello()} }, "duplicate hello"},
		{"wrong version", func(*Node) []*meshv1.Frame {
			return []*meshv1.Frame{badHello(func(h *meshv1.Hello) { h.ProtocolVersion = 99 })}
		}, "version"},
		{"short node id", func(*Node) []*meshv1.Frame {
			return []*meshv1.Frame{badHello(func(h *meshv1.Hello) { h.NodeId = h.NodeId[:5] })}
		}, "node id"},
		{"terminal escape in name", func(*Node) []*meshv1.Frame {
			return []*meshv1.Frame{badHello(func(h *meshv1.Hello) { h.DisplayName = "eve\x1b[2J" })}
		}, "display name"},
		{"self connection", func(self *Node) []*meshv1.Frame { return []*meshv1.Frame{self.hello()} }, "self"},
		{"malformed bundle", func(*Node) []*meshv1.Frame {
			return []*meshv1.Frame{alice.hello(), {Kind: &meshv1.Frame_Bundle{Bundle: &meshv1.Bundle{Destination: []byte{1}}}}}
		}, "malformed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bob := newNode(t, "bob")
			bob.Handle(t0, PeerUp{Peer: 3})
			var last []Action
			for _, f := range tt.frames(bob) {
				last = bob.Handle(t0, FrameReceived{Peer: 3, Frame: f})
			}
			if len(last) != 1 {
				t.Fatalf("got %v, want one DisconnectPeer", last)
			}
			d, ok := last[0].(DisconnectPeer)
			if !ok || d.Peer != 3 || !strings.Contains(d.Reason, tt.reason) {
				t.Errorf("got %+v, want a disconnect mentioning %q", last[0], tt.reason)
			}
		})
	}
}

func TestAppSendFailures(t *testing.T) {
	p := connect(t)
	stranger := newNode(t, "carol").cfg.Self
	tests := []struct {
		name string
		ev   AppSend
		want error
	}{
		{"not connected", AppSend{To: stranger, Body: "hi"}, ErrNotConnected},
		{"to self", AppSend{To: p.a.cfg.Self, Body: "hi"}, ErrSendToSelf},
		{"empty body", AppSend{To: p.b.cfg.Self, Body: ""}, bundle.ErrBodyEmpty},
		{"body too long", AppSend{To: p.b.cfg.Self, Body: strings.Repeat("x", bundle.MaxBodyBytes+1)}, bundle.ErrBodyTooLong},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			acts := p.a.Handle(t0, tt.ev)
			if len(acts) != 1 {
				t.Fatalf("got %v", acts)
			}
			if sf, ok := acts[0].(SendFailed); !ok || !errors.Is(sf.Err, tt.want) {
				t.Errorf("got %+v, want SendFailed(%v)", acts[0], tt.want)
			}
		})
	}
}

func TestPeerDownReportsPeerGone(t *testing.T) {
	p := connect(t)
	p.take(p.a)
	acts := p.a.Handle(t0, PeerDown{Peer: 1})
	if len(acts) != 1 {
		t.Fatalf("got %v", acts)
	}
	if g, ok := acts[0].(PeerGone); !ok || g.Info.Name != "bob" {
		t.Errorf("got %+v", acts[0])
	}
	if len(p.a.Peers()) != 0 {
		t.Error("peer still listed after PeerDown")
	}
	if acts := p.a.Handle(t0, PeerDown{Peer: 1}); len(acts) != 0 {
		t.Errorf("second PeerDown: %v", acts)
	}
}

// When both sides dial each other at once there are two connections. Each
// side decides independently, and both must close the same one.
func TestSimultaneousDialKeepsTheSameConnectionOnBothSides(t *testing.T) {
	a, b := newNode(t, "alice"), newNode(t, "bob")
	// Connection X: a dialed b (handle 10 on a, 20 on b).
	// Connection Y: b dialed a (handle 11 on a, 21 on b).
	a.Handle(t0, PeerUp{Peer: 10, Outbound: true})
	a.Handle(t0, PeerUp{Peer: 11, Outbound: false})
	b.Handle(t0, PeerUp{Peer: 20, Outbound: false})
	b.Handle(t0, PeerUp{Peer: 21, Outbound: true})

	// Hellos arrive in opposite orders on the two sides.
	closed := map[string]PeerHandle{}
	record := func(who string, acts []Action) {
		for _, act := range acts {
			if d, ok := act.(DisconnectPeer); ok {
				closed[who] = d.Peer
			}
		}
	}
	record("a", a.Handle(t0, FrameReceived{Peer: 10, Frame: b.hello()}))
	record("a", a.Handle(t0, FrameReceived{Peer: 11, Frame: b.hello()}))
	record("b", b.Handle(t0, FrameReceived{Peer: 21, Frame: a.hello()}))
	record("b", b.Handle(t0, FrameReceived{Peer: 20, Frame: a.hello()}))

	aDialerWins := a.cfg.Self.Less(b.cfg.Self)
	wantA, wantB := PeerHandle(11), PeerHandle(21) // close Y, keep X (dialed by a)
	if !aDialerWins {
		wantA, wantB = 10, 20
	}
	if closed["a"] != wantA || closed["b"] != wantB {
		t.Fatalf("closed a=%d b=%d, want a=%d b=%d", closed["a"], closed["b"], wantA, wantB)
	}
	if len(a.Peers()) != 1 || len(b.Peers()) != 1 {
		t.Error("each side should still list exactly one peer")
	}
	// The closed connection's PeerDown must not report the peer as gone.
	if acts := a.Handle(t0, PeerDown{Peer: wantA}); len(acts) != 0 {
		t.Errorf("PeerDown of the replaced connection: %v", acts)
	}
}

func TestSeenSetIsPrunedAndBounded(t *testing.T) {
	n := newNode(t, "alice")
	n.cfg.MaxSeen = 3
	for i := range 5 {
		n.markSeen(t0, bundle.ID{byte(i)}, time.Duration(i+1)*time.Minute)
	}
	if len(n.seen) != 3 {
		t.Fatalf("seen has %d entries, want 3", len(n.seen))
	}
	if _, ok := n.seen[bundle.ID{0}]; ok {
		t.Error("entry expiring soonest was not evicted")
	}
	n.Handle(t0.Add(4*time.Minute), Tick{})
	if len(n.seen) != 1 {
		t.Errorf("after Tick: %d entries, want 1", len(n.seen))
	}
}

func TestNewRejectsBadName(t *testing.T) {
	for _, name := range []string{"", strings.Repeat("a", MaxNameBytes+1), "tab\there"} {
		if _, err := New(Config{Name: name}); err == nil {
			t.Errorf("name %q accepted", name)
		}
	}
}

func bundleFrame(t *testing.T, from, to crypto.NodeID, mutate func(*bundle.Bundle)) *meshv1.Frame {
	t.Helper()
	b, err := bundle.NewPlain(to, bundle.Message{Sender: from, SentAt: t0, ID: [16]byte{1}, Body: "hi"}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	b.HopCount = 1
	mutate(b)
	return &meshv1.Frame{Kind: &meshv1.Frame_Bundle{Bundle: b.ToProto()}}
}
