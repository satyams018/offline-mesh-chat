package runtime

import (
	"context"
	"crypto/rand"
	"errors"
	"testing"
	"time"

	"github.com/satyams018/offline-mesh-chat/core/crypto"
	"github.com/satyams018/offline-mesh-chat/core/node"
)

type testNode struct {
	rt     *Runtime
	id     crypto.NodeID
	addr   string
	cancel context.CancelFunc
	done   chan struct{}
}

// start runs a real node listening on a random localhost port.
func start(t *testing.T, name string) *testNode {
	t.Helper()
	ident, err := crypto.NewIdentity(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	rt, err := New(node.Config{Self: ident.ID(), Name: name}, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	addr, err := rt.Listen(ctx, "127.0.0.1:0")
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	tn := &testNode{rt: rt, id: ident.ID(), addr: addr.String(), cancel: cancel, done: make(chan struct{})}
	go func() { rt.Run(ctx); close(tn.done) }()
	t.Cleanup(tn.stop)
	return tn
}

func (tn *testNode) stop() {
	tn.cancel()
	<-tn.done
}

// waitFor returns the first update of type T, failing after a timeout.
func waitFor[T node.Action](t *testing.T, tn *testNode) T {
	t.Helper()
	timeout := time.After(5 * time.Second)
	for {
		select {
		case act, ok := <-tn.rt.Updates():
			if !ok {
				t.Fatal("updates closed")
			}
			if v, ok := act.(T); ok {
				return v
			}
		case <-timeout:
			var zero T
			t.Fatalf("timed out waiting for %T", zero)
		}
	}
}

func TestTwoNodesChatOverTCP(t *testing.T) {
	alice, bob := start(t, "alice"), start(t, "bob")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go bob.rt.DialLoop(ctx, alice.addr)

	if got := waitFor[node.PeerReady](t, alice); got.Info.Name != "bob" {
		t.Fatalf("alice sees %q", got.Info.Name)
	}
	waitFor[node.PeerReady](t, bob)

	if err := bob.rt.Send(alice.id, "hello from bob"); err != nil {
		t.Fatal(err)
	}
	msg := waitFor[node.DeliverToApp](t, alice).Message
	if msg.Body != "hello from bob" || msg.Sender != bob.id {
		t.Fatalf("alice got %+v", msg)
	}

	if err := alice.rt.Send(bob.id, "hi bob"); err != nil {
		t.Fatal(err)
	}
	if got := waitFor[node.DeliverToApp](t, bob).Message.Body; got != "hi bob" {
		t.Fatalf("bob got %q", got)
	}

	// Sending to someone who isn't connected fails with a clear error.
	if err := alice.rt.Send(crypto.NodeID{9}, "anyone?"); err != nil {
		t.Fatal(err)
	}
	if sf := waitFor[node.SendFailed](t, alice); !errors.Is(sf.Err, node.ErrNotConnected) {
		t.Fatalf("got %v", sf.Err)
	}

	// When bob goes away, alice notices.
	cancel()
	bob.stop()
	if got := waitFor[node.PeerGone](t, alice); got.Info.Name != "bob" {
		t.Fatalf("PeerGone for %q", got.Info.Name)
	}
}

// Both nodes have each other as --peer: two TCP connections appear, and the
// duplicate-connection rule must leave exactly one, without reconnect churn.
func TestMutualDialSettlesOnOneConnection(t *testing.T) {
	alice, bob := start(t, "alice"), start(t, "bob")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go alice.rt.DialLoop(ctx, bob.addr)
	go bob.rt.DialLoop(ctx, alice.addr)

	waitFor[node.PeerReady](t, alice)
	waitFor[node.PeerReady](t, bob)

	// Give both dial loops time to retry; nobody should be reported gone.
	deadline := time.After(2500 * time.Millisecond)
	for done := false; !done; {
		select {
		case act := <-alice.rt.Updates():
			if _, gone := act.(node.PeerGone); gone {
				t.Fatal("connection churn: alice saw PeerGone")
			}
		case <-deadline:
			done = true
		}
	}
	if n := len(alice.rt.Peers()); n != 1 {
		t.Fatalf("alice lists %d peers, want 1", n)
	}
	if err := alice.rt.Send(bob.id, "still here?"); err != nil {
		t.Fatal(err)
	}
	if got := waitFor[node.DeliverToApp](t, bob).Message.Body; got != "still here?" {
		t.Fatalf("bob got %q", got)
	}
}
