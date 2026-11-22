// Package link implements framing: each frame on a stream is a 4-byte
// big-endian length followed by a protobuf Frame (SPEC.md §4.3).
package link

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"

	"google.golang.org/protobuf/proto"

	meshv1 "github.com/satyams018/offline-mesh-chat/proto/mesh/v1"
)

// MaxFrameSize is the largest frame payload accepted or sent.
const MaxFrameSize = 64 * 1024

// ErrFrameTooLarge means a peer announced or we tried to send an oversized frame.
// Receivers must drop the link: the stream can't be resynchronized safely.
var ErrFrameTooLarge = fmt.Errorf("link: frame larger than %d bytes", MaxFrameSize)

// WriteFrame encodes f and writes it with its length prefix in one Write call.
func WriteFrame(w io.Writer, f *meshv1.Frame) error {
	payload, err := proto.Marshal(f)
	if err != nil {
		return fmt.Errorf("link: encode frame: %w", err)
	}
	if len(payload) > MaxFrameSize {
		return ErrFrameTooLarge
	}
	buf := make([]byte, 4+len(payload))
	binary.BigEndian.PutUint32(buf, uint32(len(payload))) // #nosec G115 -- bounded by MaxFrameSize
	copy(buf[4:], payload)
	if _, err := w.Write(buf); err != nil {
		return fmt.Errorf("link: write frame: %w", err)
	}
	return nil
}

// ReadFrame reads one frame. The size is checked *before* allocating, so a
// peer can't make us allocate gigabytes by sending a huge length.
// It returns io.EOF only on a clean end of stream between frames.
func ReadFrame(r io.Reader) (*meshv1.Frame, error) {
	var hdr [4]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, io.EOF
		}
		return nil, fmt.Errorf("link: read header: %w", err)
	}
	n := binary.BigEndian.Uint32(hdr[:])
	if n > MaxFrameSize {
		return nil, ErrFrameTooLarge
	}
	payload := make([]byte, n)
	if _, err := io.ReadFull(r, payload); err != nil {
		return nil, fmt.Errorf("link: read payload: %w", err)
	}
	f := &meshv1.Frame{}
	if err := proto.Unmarshal(payload, f); err != nil {
		return nil, fmt.Errorf("link: decode frame: %w", err)
	}
	return f, nil
}
