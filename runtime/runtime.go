// Package runtime is the I/O side of a node: TCP connections, timers and the
// event loop that drives the sans-IO core (SPEC.md §4, decision D15).
//
// Concurrency model: a single goroutine (Run) owns the core.Node. Each
// connection has a reader and a writer goroutine that talk to the loop only
// through channels, so the core needs no locks and stays deterministic.
package runtime

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/satyams018/offline-mesh-chat/core/crypto"
	"github.com/satyams018/offline-mesh-chat/core/link"
	"github.com/satyams018/offline-mesh-chat/core/node"
	meshv1 "github.com/satyams018/offline-mesh-chat/proto/mesh/v1"
)

// Tunables. Exposed as variables only where tests need to change them.
const (
	outQueueSize  = 64               // frames buffered per connection before we drop a slow peer
	writeTimeout  = 10 * time.Second // per-frame write deadline
	dialTimeout   = 5 * time.Second
	maxBackoff    = 30 * time.Second
	tickInterval  = 5 * time.Second
	updateBufSize = 256
)

var errStopped = errors.New("runtime: stopped")

// Runtime drives one node.
type Runtime struct {
	node    *node.Node
	log     *slog.Logger
	events  chan inbound
	updates chan node.Action
	stopped chan struct{} // closed when Run returns

	nextHandle atomic.Uint64
	conns      map[node.PeerHandle]*conn // owned by the Run goroutine

	mu    sync.Mutex
	peers []node.PeerInfo // snapshot for Peers(), refreshed by the loop
}

type inbound struct {
	ev   node.Event
	conn *conn // set for PeerUp only
}

type conn struct {
	h      node.PeerHandle
	nc     net.Conn
	out    chan *meshv1.Frame
	closed chan struct{}
	once   sync.Once
	// helloID is the node ID the peer announced, used by DialLoop to avoid
	// redialing a peer we are already connected to through another link.
	helloID atomic.Pointer[crypto.NodeID]
}

func (c *conn) close() {
	c.once.Do(func() {
		close(c.closed)
		_ = c.nc.Close()
	})
}

// New creates a runtime around a core node built from cfg.
func New(cfg node.Config, log *slog.Logger) (*Runtime, error) {
	n, err := node.New(cfg)
	if err != nil {
		return nil, err
	}
	if log == nil {
		log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return &Runtime{
		node:    n,
		log:     log,
		events:  make(chan inbound),
		updates: make(chan node.Action, updateBufSize),
		stopped: make(chan struct{}),
		conns:   make(map[node.PeerHandle]*conn),
	}, nil
}

// Updates delivers UI-relevant actions: DeliverToApp, PeerReady, PeerGone and
// SendFailed. The caller must keep draining it; it is closed when Run returns.
func (r *Runtime) Updates() <-chan node.Action { return r.updates }

// Peers returns the currently connected, identified peers.
func (r *Runtime) Peers() []node.PeerInfo {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]node.PeerInfo(nil), r.peers...)
}

// Send asks the node to send body to a peer. Failures arrive as SendFailed.
func (r *Runtime) Send(to crypto.NodeID, body string) error {
	if !r.post(inbound{ev: node.AppSend{To: to, Body: body}}) {
		return errStopped
	}
	return nil
}

// Run is the event loop. It returns when ctx is cancelled.
func (r *Runtime) Run(ctx context.Context) {
	ticker := time.NewTicker(tickInterval)
	defer func() {
		ticker.Stop()
		for _, c := range r.conns {
			c.close()
		}
		close(r.stopped)
		close(r.updates)
	}()

	for {
		select {
		case <-ctx.Done():
			return
		case in := <-r.events:
			if up, ok := in.ev.(node.PeerUp); ok {
				r.conns[up.Peer] = in.conn
			}
			if fr, ok := in.ev.(node.FrameReceived); ok {
				r.noteHello(fr)
			}
			if !r.apply(ctx, r.node.Handle(time.Now(), in.ev)) {
				return
			}
			if down, ok := in.ev.(node.PeerDown); ok {
				delete(r.conns, down.Peer)
			}
		case <-ticker.C:
			if !r.apply(ctx, r.node.Handle(time.Now(), node.Tick{})) {
				return
			}
		}
	}
}

func (r *Runtime) noteHello(fr node.FrameReceived) {
	h := fr.Frame.GetHello()
	c := r.conns[fr.Peer]
	if h == nil || c == nil {
		return
	}
	if id, err := crypto.NodeIDFromBytes(h.GetNodeId()); err == nil {
		c.helloID.Store(&id)
	}
}

// apply performs the node's actions. It returns false if ctx ended while
// waiting for the UI to drain updates.
func (r *Runtime) apply(ctx context.Context, acts []node.Action) bool {
	for _, act := range acts {
		switch a := act.(type) {
		case node.SendFrame:
			c := r.conns[a.Peer]
			if c == nil {
				continue
			}
			select {
			case c.out <- a.Frame:
			default:
				r.log.Warn("peer too slow, disconnecting", "peer", c.nc.RemoteAddr())
				c.close()
			}
		case node.DisconnectPeer:
			if c := r.conns[a.Peer]; c != nil {
				r.log.Info("disconnecting peer", "peer", c.nc.RemoteAddr(), "reason", a.Reason)
				c.close()
			}
		default: // UI notifications
			if _, ok := a.(node.PeerReady); ok {
				r.refreshPeers()
			}
			if _, ok := a.(node.PeerGone); ok {
				r.refreshPeers()
			}
			select {
			case r.updates <- act:
			case <-ctx.Done():
				return false
			}
		}
	}
	return true
}

func (r *Runtime) refreshPeers() {
	peers := r.node.Peers()
	r.mu.Lock()
	r.peers = peers
	r.mu.Unlock()
}

// post hands an event to the loop. It returns false once the loop has stopped.
func (r *Runtime) post(in inbound) bool {
	select {
	case r.events <- in:
		return true
	case <-r.stopped:
		return false
	}
}

// Listen accepts inbound connections on addr (e.g. ":7777") until ctx ends.
// It returns the bound address, which is useful with port 0 in tests.
func (r *Runtime) Listen(ctx context.Context, addr string) (net.Addr, error) {
	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("listen %s: %w", addr, err)
	}
	go func() {
		select {
		case <-ctx.Done():
		case <-r.stopped:
		}
		_ = ln.Close()
	}()
	go func() {
		for {
			nc, err := ln.Accept()
			if err != nil {
				if !errors.Is(err, net.ErrClosed) {
					r.log.Error("accept failed", "err", err)
				}
				return
			}
			r.log.Debug("inbound connection", "peer", nc.RemoteAddr())
			r.addConn(nc, false)
		}
	}()
	return ln.Addr(), nil
}

// DialLoop keeps a connection to addr open, redialing with exponential
// backoff, until ctx ends. Use it for --peer addresses.
func (r *Runtime) DialLoop(ctx context.Context, addr string) {
	backoff := time.Second
	d := net.Dialer{Timeout: dialTimeout}
	for {
		nc, err := d.DialContext(ctx, "tcp", addr)
		if err == nil {
			backoff = time.Second
			c := r.addConn(nc, true)
			select {
			case <-c.closed:
			case <-ctx.Done():
				return
			}
			// If the link was closed because we're already connected to this
			// peer another way (duplicate connection), wait until that ends.
			if id := c.helloID.Load(); id != nil {
				r.waitWhileConnected(ctx, *id)
			}
		} else {
			r.log.Debug("dial failed", "addr", addr, "err", err, "retry_in", backoff)
		}
		select {
		case <-time.After(backoff):
		case <-ctx.Done():
			return
		case <-r.stopped:
			return
		}
		backoff = min(backoff*2, maxBackoff)
	}
}

func (r *Runtime) waitWhileConnected(ctx context.Context, id crypto.NodeID) {
	for r.isConnected(id) {
		select {
		case <-time.After(2 * time.Second):
		case <-ctx.Done():
			return
		case <-r.stopped:
			return
		}
	}
}

func (r *Runtime) isConnected(id crypto.NodeID) bool {
	for _, p := range r.Peers() {
		if p.ID == id {
			return true
		}
	}
	return false
}

// addConn registers nc with the loop and starts its reader and writer.
func (r *Runtime) addConn(nc net.Conn, outbound bool) *conn {
	c := &conn{
		h:      node.PeerHandle(r.nextHandle.Add(1)),
		nc:     nc,
		out:    make(chan *meshv1.Frame, outQueueSize),
		closed: make(chan struct{}),
	}
	// PeerUp is posted before the reader starts, so the loop always sees it
	// before any FrameReceived from this connection.
	if !r.post(inbound{ev: node.PeerUp{Peer: c.h, Outbound: outbound}, conn: c}) {
		c.close()
		return c
	}
	go r.readLoop(c)
	go r.writeLoop(c)
	return c
}

func (r *Runtime) readLoop(c *conn) {
	defer func() {
		c.close()
		r.post(inbound{ev: node.PeerDown{Peer: c.h}})
	}()
	br := bufio.NewReader(c.nc)
	for {
		f, err := link.ReadFrame(br)
		if err != nil {
			select {
			case <-c.closed: // we closed it; not an error
			default:
				if !errors.Is(err, io.EOF) {
					r.log.Info("link error", "peer", c.nc.RemoteAddr(), "err", err)
				}
			}
			return
		}
		if !r.post(inbound{ev: node.FrameReceived{Peer: c.h, Frame: f}}) {
			return
		}
	}
}

func (r *Runtime) writeLoop(c *conn) {
	for {
		select {
		case f := <-c.out:
			if err := c.nc.SetWriteDeadline(time.Now().Add(writeTimeout)); err != nil {
				c.close()
				return
			}
			if err := link.WriteFrame(c.nc, f); err != nil {
				r.log.Info("write failed", "peer", c.nc.RemoteAddr(), "err", err)
				c.close()
				return
			}
		case <-c.closed:
			return
		}
	}
}
