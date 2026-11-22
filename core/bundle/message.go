package bundle

import (
	"errors"
	"fmt"
	"io"
	"time"
	"unicode/utf8"

	"google.golang.org/protobuf/proto"

	"github.com/satyams018/offline-mesh-chat/core/crypto"
	meshv1 "github.com/satyams018/offline-mesh-chat/proto/mesh/v1"
)

// MaxBodyBytes is the largest message body (SPEC.md §3).
const MaxBodyBytes = 16 * 1024

// MessageIDSize is the length of a random per-message ID.
const MessageIDSize = 16

// Message errors.
var (
	ErrBodyEmpty   = errors.New("message: empty body")
	ErrBodyTooLong = fmt.Errorf("message: body longer than %d bytes", MaxBodyBytes)
	ErrBodyUTF8    = errors.New("message: body is not valid UTF-8")
)

// Message is the plaintext only the recipient sees (InnerMessage on the wire).
type Message struct {
	Sender crypto.NodeID
	SentAt time.Time // millisecond precision on the wire
	ID     [MessageIDSize]byte
	Body   string
}

// ValidateBody checks a body typed by the user or received from the network.
func ValidateBody(body string) error {
	switch {
	case body == "":
		return ErrBodyEmpty
	case len(body) > MaxBodyBytes:
		return ErrBodyTooLong
	case !utf8.ValidString(body):
		return ErrBodyUTF8
	}
	return nil
}

// NewPlain builds a bundle carrying m unencrypted. Milestones 1–2 only:
// milestone 3 replaces this with a sealed (encrypted, signed) message.
func NewPlain(dest crypto.NodeID, m Message, lifetime time.Duration) (*Bundle, error) {
	if err := ValidateBody(m.Body); err != nil {
		return nil, err
	}
	inner := &meshv1.InnerMessage{
		SenderId:  m.Sender[:],
		SentAtMs:  uint64(m.SentAt.UnixMilli()), // #nosec G115 -- clocks before 1970 are not supported
		MessageId: m.ID[:],
		Body:      m.Body,
	}
	ct, err := proto.Marshal(inner)
	if err != nil {
		return nil, fmt.Errorf("encode message: %w", err)
	}
	return &Bundle{
		Version:     Version,
		Destination: dest,
		LifetimeMs:  uint64(lifetime.Milliseconds()), // #nosec G115 -- lifetimes are positive
		Ciphertext:  ct,
	}, nil
}

// OpenPlain decodes the message carried by a plaintext bundle.
func OpenPlain(b *Bundle) (Message, error) {
	var inner meshv1.InnerMessage
	if err := proto.Unmarshal(b.Ciphertext, &inner); err != nil {
		return Message{}, fmt.Errorf("decode message: %w", err)
	}
	sender, err := crypto.NodeIDFromBytes(inner.GetSenderId())
	if err != nil {
		return Message{}, fmt.Errorf("message sender: %w", err)
	}
	if len(inner.GetMessageId()) != MessageIDSize {
		return Message{}, fmt.Errorf("message id: got %d bytes, want %d", len(inner.GetMessageId()), MessageIDSize)
	}
	if err := ValidateBody(inner.GetBody()); err != nil {
		return Message{}, err
	}
	m := Message{
		Sender: sender,
		SentAt: time.UnixMilli(int64(inner.GetSentAtMs())), // #nosec G115 -- display only
		Body:   inner.GetBody(),
	}
	copy(m.ID[:], inner.GetMessageId())
	return m, nil
}

// NewMessageID reads a random message ID from r (crypto/rand in production).
func NewMessageID(r io.Reader) ([MessageIDSize]byte, error) {
	var id [MessageIDSize]byte
	if _, err := io.ReadFull(r, id[:]); err != nil {
		return id, fmt.Errorf("message id: %w", err)
	}
	return id, nil
}
