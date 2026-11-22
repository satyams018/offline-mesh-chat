// Package bundle defines the unit of store-and-forward: a Bundle, its
// content-addressed ID and its validation rules (SPEC.md §6).
package bundle

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/satyams018/offline-mesh-chat/core/crypto"
	meshv1 "github.com/satyams018/offline-mesh-chat/proto/mesh/v1"
)

// Protocol constants and defaults.
const (
	Version         uint32 = 1
	DefaultLifetime        = 24 * time.Hour
	DefaultMaxHops  uint32 = 16

	// MaxLifetime caps lifetime_ms from the network. Without it a peer could
	// send ~2^64 ms, which overflows time.Duration and breaks dedup expiry.
	MaxLifetime = time.Duration(maxLifetimeMs) * time.Millisecond

	maxLifetimeMs uint64 = 7 * 24 * 60 * 60 * 1000

	// MaxCiphertext bounds the encoded InnerMessage: a 16 KiB body plus
	// headroom for the other fields and (from M3) the AEAD tag.
	MaxCiphertext = MaxBodyBytes + 1024

	ephPubSize = 32
	nonceSize  = 24
	idPrefix   = "mesh-bundle-v1"
)

// Validation errors. Callers drop bundles that fail validation.
var (
	ErrVersion    = errors.New("bundle: unsupported version")
	ErrTooLarge   = errors.New("bundle: ciphertext too large")
	ErrEmpty      = errors.New("bundle: empty ciphertext")
	ErrNoLifetime = errors.New("bundle: zero lifetime")
	ErrLifetime   = fmt.Errorf("bundle: lifetime longer than %s", MaxLifetime)
	ErrExpired    = errors.New("bundle: expired")
	ErrHopLimit   = errors.New("bundle: hop limit reached")
)

// ID is the SHA-256 content address of a bundle's immutable fields.
type ID [sha256.Size]byte

// String returns a short hex prefix, enough for logs.
func (id ID) String() string { return hex.EncodeToString(id[:6]) }

// Bundle is one message in transit. Only the immutable fields are covered by ID;
// relays update the mutable routing block.
type Bundle struct {
	// Immutable.
	Version     uint32
	Destination crypto.NodeID
	LifetimeMs  uint64
	EphPub      [ephPubSize]byte // zero until M3
	Nonce       [nonceSize]byte  // zero until M3
	Ciphertext  []byte

	// Mutable routing block.
	AgeMs    uint64
	HopCount uint32
	Copies   uint32
}

// ID hashes the immutable fields in a fixed, hand-defined byte layout with
// fixed-width big-endian integers. Protobuf bytes are not used because protobuf
// encoding is not canonical across implementations (decision D7).
func (b *Bundle) ID() ID {
	h := sha256.New()
	h.Write([]byte(idPrefix))
	var u32 [4]byte
	binary.BigEndian.PutUint32(u32[:], b.Version)
	h.Write(u32[:])
	h.Write(b.Destination[:])
	var u64 [8]byte
	binary.BigEndian.PutUint64(u64[:], b.LifetimeMs)
	h.Write(u64[:])
	h.Write(b.EphPub[:])
	h.Write(b.Nonce[:])
	h.Write(b.Ciphertext) // last field, so no length prefix is needed
	var id ID
	h.Sum(id[:0])
	return id
}

// Expired reports whether the bundle has used up its lifetime. Age is measured
// by holding time, not wall clocks, so unsynchronized clocks don't matter (D6).
func (b *Bundle) Expired() bool { return b.AgeMs >= b.LifetimeMs }

// RemainingLifetime is how long the bundle may still live, at most MaxLifetime.
func (b *Bundle) RemainingLifetime() time.Duration {
	if b.Expired() {
		return 0
	}
	ms := min(b.LifetimeMs-b.AgeMs, maxLifetimeMs)
	return time.Duration(ms) * time.Millisecond // #nosec G115 -- ms <= maxLifetimeMs
}

// Validate checks a received bundle against protocol limits.
func (b *Bundle) Validate(maxHops uint32) error {
	switch {
	case b.Version != Version:
		return fmt.Errorf("%w: %d", ErrVersion, b.Version)
	case len(b.Ciphertext) == 0:
		return ErrEmpty
	case len(b.Ciphertext) > MaxCiphertext:
		return fmt.Errorf("%w: %d bytes", ErrTooLarge, len(b.Ciphertext))
	case b.LifetimeMs == 0:
		return ErrNoLifetime
	case b.LifetimeMs > maxLifetimeMs:
		return ErrLifetime
	case b.Expired():
		return ErrExpired
	case b.HopCount >= maxHops:
		return fmt.Errorf("%w: %d hops", ErrHopLimit, b.HopCount)
	}
	return nil
}

// ToProto converts to the wire type.
func (b *Bundle) ToProto() *meshv1.Bundle {
	p := &meshv1.Bundle{
		Version:     b.Version,
		Destination: b.Destination[:],
		LifetimeMs:  b.LifetimeMs,
		Ciphertext:  b.Ciphertext,
		AgeMs:       b.AgeMs,
		HopCount:    b.HopCount,
		Copies:      b.Copies,
	}
	// Until M3 the crypto fields are all zero; leave them off the wire.
	if b.EphPub != ([ephPubSize]byte{}) || b.Nonce != ([nonceSize]byte{}) {
		p.EphPub = b.EphPub[:]
		p.Nonce = b.Nonce[:]
	}
	return p
}

// FromProto converts from the wire type, checking field lengths. It does not
// run Validate, so callers can tell malformed bundles (drop the link) from
// merely expired ones (drop the bundle).
func FromProto(p *meshv1.Bundle) (*Bundle, error) {
	if p == nil {
		return nil, errors.New("bundle: nil")
	}
	dest, err := crypto.NodeIDFromBytes(p.GetDestination())
	if err != nil {
		return nil, fmt.Errorf("bundle destination: %w", err)
	}
	b := &Bundle{
		Version:     p.GetVersion(),
		Destination: dest,
		LifetimeMs:  p.GetLifetimeMs(),
		Ciphertext:  p.GetCiphertext(),
		AgeMs:       p.GetAgeMs(),
		HopCount:    p.GetHopCount(),
		Copies:      p.GetCopies(),
	}
	if err := copyFixed(b.EphPub[:], p.GetEphPub(), "eph_pub"); err != nil {
		return nil, err
	}
	if err := copyFixed(b.Nonce[:], p.GetNonce(), "nonce"); err != nil {
		return nil, err
	}
	return b, nil
}

// copyFixed accepts either an empty field (zero value) or exactly len(dst) bytes.
func copyFixed(dst, src []byte, name string) error {
	if len(src) != 0 && len(src) != len(dst) {
		return fmt.Errorf("bundle %s: got %d bytes, want %d", name, len(src), len(dst))
	}
	copy(dst, src)
	return nil
}
