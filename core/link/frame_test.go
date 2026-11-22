package link

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"testing"

	"google.golang.org/protobuf/proto"

	meshv1 "github.com/satyams018/offline-mesh-chat/proto/mesh/v1"
)

func hello(name string) *meshv1.Frame {
	return &meshv1.Frame{Kind: &meshv1.Frame_Hello{Hello: &meshv1.Hello{ProtocolVersion: 1, DisplayName: name}}}
}

func TestRoundTripSeveralFrames(t *testing.T) {
	var buf bytes.Buffer
	for _, name := range []string{"a", "b", "c"} {
		if err := WriteFrame(&buf, hello(name)); err != nil {
			t.Fatal(err)
		}
	}
	for _, want := range []string{"a", "b", "c"} {
		f, err := ReadFrame(&buf)
		if err != nil {
			t.Fatal(err)
		}
		if got := f.GetHello().GetDisplayName(); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	}
	if _, err := ReadFrame(&buf); !errors.Is(err, io.EOF) {
		t.Errorf("at end of stream: err = %v, want io.EOF", err)
	}
}

func TestReadRejectsOversizedLengthWithoutAllocating(t *testing.T) {
	var hdr [4]byte
	binary.BigEndian.PutUint32(hdr[:], 0xFFFFFFFF)
	if _, err := ReadFrame(bytes.NewReader(hdr[:])); !errors.Is(err, ErrFrameTooLarge) {
		t.Fatalf("err = %v, want ErrFrameTooLarge", err)
	}
}

func TestWriteRejectsOversizedFrame(t *testing.T) {
	big := &meshv1.Frame{Kind: &meshv1.Frame_Bundle{Bundle: &meshv1.Bundle{Ciphertext: make([]byte, MaxFrameSize)}}}
	if err := WriteFrame(io.Discard, big); !errors.Is(err, ErrFrameTooLarge) {
		t.Fatalf("err = %v, want ErrFrameTooLarge", err)
	}
}

func TestReadTruncated(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteFrame(&buf, hello("abc")); err != nil {
		t.Fatal(err)
	}
	data := buf.Bytes()
	for _, cut := range []int{2, len(data) - 1} {
		_, err := ReadFrame(bytes.NewReader(data[:cut]))
		if err == nil || errors.Is(err, io.EOF) {
			t.Errorf("cut at %d: err = %v, want a non-EOF error", cut, err)
		}
	}
}

// FuzzReadFrame: the decoder parses bytes from strangers, so it must never
// panic, and anything it accepts must re-encode to an equal frame.
func FuzzReadFrame(f *testing.F) {
	var seed bytes.Buffer
	_ = WriteFrame(&seed, hello("seed"))
	f.Add(seed.Bytes())
	f.Add([]byte{0, 0, 0, 0})
	f.Add([]byte{0xFF, 0xFF, 0xFF, 0xFF})
	f.Fuzz(func(t *testing.T, data []byte) {
		fr, err := ReadFrame(bytes.NewReader(data))
		if err != nil {
			return
		}
		var out bytes.Buffer
		if err := WriteFrame(&out, fr); err != nil {
			t.Fatalf("re-encode failed: %v", err)
		}
		again, err := ReadFrame(&out)
		if err != nil {
			t.Fatalf("re-decode failed: %v", err)
		}
		if !proto.Equal(fr, again) {
			t.Fatal("frame changed after a round trip")
		}
	})
}
