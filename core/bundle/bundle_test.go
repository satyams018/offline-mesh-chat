package bundle

import (
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/satyams018/offline-mesh-chat/core/crypto"
	meshv1 "github.com/satyams018/offline-mesh-chat/proto/mesh/v1"
)

func sample() *Bundle {
	b := &Bundle{
		Version:    Version,
		LifetimeMs: 60_000,
		Ciphertext: []byte("hello"),
	}
	for i := range b.Destination {
		b.Destination[i] = byte(i)
	}
	return b
}

// The bundle ID is part of the protocol: other implementations (Kotlin in v2)
// must compute the same value. This golden value catches accidental changes.
// It was cross-checked with an independent Python implementation of SPEC.md §6.
func TestIDGolden(t *testing.T) {
	const want = "d8344737b01f"
	if got := sample().ID().String(); got != want {
		t.Fatalf("ID = %s, want %s (did the canonical layout change?)", got, want)
	}
}

func TestIDIgnoresMutableFields(t *testing.T) {
	a, b := sample(), sample()
	b.AgeMs, b.HopCount, b.Copies = 999, 7, 3
	if a.ID() != b.ID() {
		t.Fatal("mutable routing fields changed the bundle ID")
	}
}

func TestIDCoversImmutableFields(t *testing.T) {
	base := sample().ID()
	mutations := map[string]func(*Bundle){
		"version":     func(b *Bundle) { b.Version++ },
		"destination": func(b *Bundle) { b.Destination[0] ^= 1 },
		"lifetime":    func(b *Bundle) { b.LifetimeMs++ },
		"eph_pub":     func(b *Bundle) { b.EphPub[0] = 1 },
		"nonce":       func(b *Bundle) { b.Nonce[0] = 1 },
		"ciphertext":  func(b *Bundle) { b.Ciphertext = []byte("hellp") },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			b := sample()
			mutate(b)
			if b.ID() == base {
				t.Errorf("changing %s did not change the ID", name)
			}
		})
	}
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Bundle)
		want   error
	}{
		{"valid", func(*Bundle) {}, nil},
		{"wrong version", func(b *Bundle) { b.Version = 2 }, ErrVersion},
		{"empty", func(b *Bundle) { b.Ciphertext = nil }, ErrEmpty},
		{"too large", func(b *Bundle) { b.Ciphertext = make([]byte, MaxCiphertext+1) }, ErrTooLarge},
		{"zero lifetime", func(b *Bundle) { b.LifetimeMs = 0 }, ErrNoLifetime},
		{"huge lifetime", func(b *Bundle) { b.LifetimeMs = math.MaxUint64 }, ErrLifetime},
		{"max lifetime", func(b *Bundle) { b.LifetimeMs = maxLifetimeMs }, nil},
		{"expired", func(b *Bundle) { b.AgeMs = b.LifetimeMs }, ErrExpired},
		{"hop limit", func(b *Bundle) { b.HopCount = DefaultMaxHops }, ErrHopLimit},
		{"one below hop limit", func(b *Bundle) { b.HopCount = DefaultMaxHops - 1 }, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := sample()
			tt.mutate(b)
			if err := b.Validate(DefaultMaxHops); !errors.Is(err, tt.want) {
				t.Errorf("Validate = %v, want %v", err, tt.want)
			}
		})
	}
}

// A remaining lifetime from the network must never overflow into a negative
// duration, or a replayed bundle would slip past the seen-set.
func TestRemainingLifetimeNeverNegative(t *testing.T) {
	b := sample()
	b.LifetimeMs = math.MaxUint64
	if got := b.RemainingLifetime(); got <= 0 || got > MaxLifetime {
		t.Fatalf("RemainingLifetime = %v, want in (0, %v]", got, MaxLifetime)
	}
}

func TestProtoRoundTrip(t *testing.T) {
	for _, withCrypto := range []bool{false, true} {
		b := sample()
		b.AgeMs, b.HopCount, b.Copies = 5, 2, 4
		if withCrypto {
			b.EphPub[3], b.Nonce[5] = 9, 9
		}
		wire, err := proto.Marshal(b.ToProto())
		if err != nil {
			t.Fatal(err)
		}
		var p meshv1.Bundle
		if err := proto.Unmarshal(wire, &p); err != nil {
			t.Fatal(err)
		}
		got, err := FromProto(&p)
		if err != nil {
			t.Fatalf("FromProto: %v", err)
		}
		if got.ID() != b.ID() || got.AgeMs != 5 || got.HopCount != 2 || got.Copies != 4 {
			t.Errorf("round trip (crypto=%v) changed the bundle: %+v", withCrypto, got)
		}
	}
}

func TestFromProtoRejectsBadLengths(t *testing.T) {
	good := sample().ToProto()
	tests := map[string]func(*meshv1.Bundle){
		"short destination": func(p *meshv1.Bundle) { p.Destination = p.Destination[:31] },
		"short eph_pub":     func(p *meshv1.Bundle) { p.EphPub = make([]byte, 31) },
		"long nonce":        func(p *meshv1.Bundle) { p.Nonce = make([]byte, 25) },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			p := proto.Clone(good).(*meshv1.Bundle)
			mutate(p)
			if _, err := FromProto(p); err == nil {
				t.Error("expected an error")
			}
		})
	}
	if _, err := FromProto(nil); err == nil {
		t.Error("nil accepted")
	}
}

func TestPlainMessageRoundTrip(t *testing.T) {
	var sender, dest crypto.NodeID
	sender[0], dest[0] = 1, 2
	m := Message{Sender: sender, SentAt: time.UnixMilli(1_700_000_000_123), ID: [16]byte{7}, Body: "namaste 👋"}

	b, err := NewPlain(dest, m, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if err := b.Validate(DefaultMaxHops); err != nil {
		t.Fatalf("new bundle is invalid: %v", err)
	}
	got, err := OpenPlain(b)
	if err != nil {
		t.Fatal(err)
	}
	if got != m {
		t.Errorf("got %+v, want %+v", got, m)
	}
}

func TestValidateBody(t *testing.T) {
	tests := []struct {
		body string
		want error
	}{
		{"hi", nil},
		{"", ErrBodyEmpty},
		{strings.Repeat("a", MaxBodyBytes), nil},
		{strings.Repeat("a", MaxBodyBytes+1), ErrBodyTooLong},
		{"\xff", ErrBodyUTF8},
	}
	for _, tt := range tests {
		if err := ValidateBody(tt.body); !errors.Is(err, tt.want) {
			t.Errorf("ValidateBody(len %d) = %v, want %v", len(tt.body), err, tt.want)
		}
	}
}

// FuzzFromProto feeds arbitrary bytes through the bundle decoding path a
// stranger's frame would take. It must never panic.
func FuzzFromProto(f *testing.F) {
	seed, _ := proto.Marshal(sample().ToProto())
	f.Add(seed)
	f.Add([]byte{})
	f.Fuzz(func(_ *testing.T, data []byte) {
		var p meshv1.Bundle
		if proto.Unmarshal(data, &p) != nil {
			return
		}
		b, err := FromProto(&p)
		if err != nil {
			return
		}
		_ = b.Validate(DefaultMaxHops)
		_ = b.ID()
		_, _ = OpenPlain(b)
	})
}
