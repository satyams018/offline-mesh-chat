package node

import (
	"github.com/satyams018/offline-mesh-chat/core/bundle"
	"github.com/satyams018/offline-mesh-chat/core/crypto"
	meshv1 "github.com/satyams018/offline-mesh-chat/proto/mesh/v1"
)

// PeerHandle identifies one transport connection. The runtime picks it; the
// node learns which NodeID is behind it from the peer's Hello.
type PeerHandle uint64

// PeerInfo is what the node knows about a connected, identified peer.
type PeerInfo struct {
	ID   crypto.NodeID
	Name string
}

// Event is an input to Node.Handle.
type Event interface{ isEvent() }

// PeerUp means the runtime opened a connection. Outbound is true if we dialed it.
type PeerUp struct {
	Peer     PeerHandle
	Outbound bool
}

// PeerDown means the connection closed (for any reason).
type PeerDown struct{ Peer PeerHandle }

// FrameReceived means a decoded frame arrived on a connection.
type FrameReceived struct {
	Peer  PeerHandle
	Frame *meshv1.Frame
}

// Tick triggers periodic housekeeping (expiry). The runtime sends one every few seconds.
type Tick struct{}

// AppSend means the user wants to send Body to To.
type AppSend struct {
	To   crypto.NodeID
	Body string
}

func (PeerUp) isEvent()        {}
func (PeerDown) isEvent()      {}
func (FrameReceived) isEvent() {}
func (Tick) isEvent()          {}
func (AppSend) isEvent()       {}

// Action is an output of Node.Handle that the runtime must perform.
type Action interface{ isAction() }

// SendFrame asks the runtime to write Frame to the connection.
type SendFrame struct {
	Peer  PeerHandle
	Frame *meshv1.Frame
}

// DisconnectPeer asks the runtime to close the connection.
type DisconnectPeer struct {
	Peer   PeerHandle
	Reason string
}

// DeliverToApp hands a message addressed to us to the UI.
type DeliverToApp struct{ Message bundle.Message }

// PeerReady reports that a connection completed its Hello (UI notification).
type PeerReady struct {
	Peer PeerHandle
	Info PeerInfo
}

// PeerGone reports that an identified peer disconnected (UI notification).
type PeerGone struct{ Info PeerInfo }

// SendFailed reports that an AppSend was rejected (UI notification).
type SendFailed struct {
	To  crypto.NodeID
	Err error
}

func (SendFrame) isAction()      {}
func (DisconnectPeer) isAction() {}
func (DeliverToApp) isAction()   {}
func (PeerReady) isAction()      {}
func (PeerGone) isAction()       {}
func (SendFailed) isAction()     {}
