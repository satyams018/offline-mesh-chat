// Package crypto holds node identity keys. Sealing and signing of messages
// arrive in milestone 3 (SPEC.md §8).
package crypto

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base32"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// NodeIDSize is the length of a node ID: an Ed25519 public key.
const NodeIDSize = ed25519.PublicKeySize

// identityFile is the seed file inside the data directory.
const identityFile = "identity.key"

// NodeID identifies a node on the mesh. It is the node's Ed25519 public key.
type NodeID [NodeIDSize]byte

// NodeIDFromBytes converts a wire value into a NodeID, checking its length.
func NodeIDFromBytes(b []byte) (NodeID, error) {
	var id NodeID
	if len(b) != NodeIDSize {
		return id, fmt.Errorf("node id: got %d bytes, want %d", len(b), NodeIDSize)
	}
	copy(id[:], b)
	return id, nil
}

// String returns the full ID in hex.
func (id NodeID) String() string { return hex.EncodeToString(id[:]) }

// Less orders node IDs bytewise. Used to break ties deterministically.
func (id NodeID) Less(other NodeID) bool { return bytes.Compare(id[:], other[:]) < 0 }

// Fingerprint is a short, human-comparable form: the first 10 bytes in base32,
// grouped in fours, e.g. "MFRG-GZDF-MZTW-Q2LK".
func (id NodeID) Fingerprint() string {
	s := base32.StdEncoding.EncodeToString(id[:10]) // 10 bytes -> exactly 16 chars, no padding
	return s[0:4] + "-" + s[4:8] + "-" + s[8:12] + "-" + s[12:16]
}

// NormalizeFingerprint uppercases a user-typed fingerprint and drops dashes and
// spaces, so it can be prefix-matched against Fingerprint output.
func NormalizeFingerprint(s string) string {
	return strings.ToUpper(strings.NewReplacer("-", "", " ", "").Replace(s))
}

// Identity is a node's long-term signing key.
type Identity struct {
	priv ed25519.PrivateKey
}

// NewIdentity generates a fresh identity from r (crypto/rand in production).
func NewIdentity(r io.Reader) (*Identity, error) {
	_, priv, err := ed25519.GenerateKey(r)
	if err != nil {
		return nil, fmt.Errorf("generate identity: %w", err)
	}
	return &Identity{priv: priv}, nil
}

// ID returns the node ID (public key).
func (i *Identity) ID() NodeID {
	var id NodeID
	copy(id[:], i.priv.Public().(ed25519.PublicKey))
	return id
}

// LoadOrCreateIdentity reads the identity seed from dir, creating dir and a new
// identity on first start. The seed file is written with mode 0600.
func LoadOrCreateIdentity(dir string) (*Identity, error) {
	path := filepath.Join(dir, identityFile)
	seed, err := os.ReadFile(path) // #nosec G304 -- path is the user's own data dir
	switch {
	case err == nil:
		if len(seed) != ed25519.SeedSize {
			return nil, fmt.Errorf("identity file %s: got %d bytes, want %d", path, len(seed), ed25519.SeedSize)
		}
		return &Identity{priv: ed25519.NewKeyFromSeed(seed)}, nil
	case !errors.Is(err, fs.ErrNotExist):
		return nil, fmt.Errorf("read identity: %w", err)
	}

	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create data dir: %w", err)
	}
	id, err := NewIdentity(rand.Reader)
	if err != nil {
		return nil, err
	}
	// O_EXCL: never overwrite an identity that appeared since we checked.
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) // #nosec G304 -- see above
	if err != nil {
		return nil, fmt.Errorf("create identity: %w", err)
	}
	if _, err := f.Write(id.priv.Seed()); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("write identity: %w", err)
	}
	if err := f.Close(); err != nil {
		return nil, fmt.Errorf("write identity: %w", err)
	}
	return id, nil
}
