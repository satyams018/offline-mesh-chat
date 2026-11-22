package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/satyams018/offline-mesh-chat/core/crypto"
	"github.com/satyams018/offline-mesh-chat/core/node"
)

func TestRunFlags(t *testing.T) {
	tests := []struct {
		name       string
		args       []string
		wantCode   int
		wantStdout string
		wantStderr string
	}{
		{name: "version flag", args: []string{"-version"}, wantCode: 0, wantStdout: "meshd dev\n"},
		{name: "help exits cleanly", args: []string{"-h"}, wantCode: 0, wantStderr: "-peer"},
		{name: "unknown flag", args: []string{"-nope"}, wantCode: 2, wantStderr: "flag provided but not defined"},
		{name: "stray argument", args: []string{"oops"}, wantCode: 2, wantStderr: "unexpected argument"},
		{name: "bad name", args: []string{"-name", "a\x1bb"}, wantCode: 2, wantStderr: "-name"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := run(context.Background(), tt.args, strings.NewReader(""), &stdout, &stderr)
			if code != tt.wantCode {
				t.Errorf("exit code = %d, want %d", code, tt.wantCode)
			}
			if tt.wantStdout != "" && stdout.String() != tt.wantStdout {
				t.Errorf("stdout = %q, want %q", stdout.String(), tt.wantStdout)
			}
			if !strings.Contains(stderr.String(), tt.wantStderr) {
				t.Errorf("stderr = %q, want it to contain %q", stderr.String(), tt.wantStderr)
			}
		})
	}
}

// End to end through run(): start, print the banner, and exit on /quit.
func TestRunStartsAndQuits(t *testing.T) {
	var stdout, stderr bytes.Buffer
	args := []string{"-name", "alice", "-listen", "127.0.0.1:0", "-data-dir", t.TempDir()}
	code := run(context.Background(), args, strings.NewReader("/id\n/peers\n/quit\n"), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code %d, stderr: %s", code, stderr.String())
	}
	for _, want := range []string{"alice", "listening on 127.0.0.1:", "you are ", "no peers connected"} {
		if !strings.Contains(stdout.String(), want) {
			t.Errorf("stdout missing %q:\n%s", want, stdout.String())
		}
	}
}

func peerInfo(name string, first byte) node.PeerInfo {
	var id crypto.NodeID
	id[0] = first
	return node.PeerInfo{ID: id, Name: name}
}

func TestParseSend(t *testing.T) {
	bob, bobby := peerInfo("Bob", 0x00), peerInfo("bobby", 0xFF)
	bobFP := bob.ID.Fingerprint()[:4]
	tests := []struct {
		name     string
		line     string
		peers    []node.PeerInfo
		wantTo   string
		wantBody string
		wantErr  string
	}{
		{"implicit only peer", "hello", []node.PeerInfo{bob}, "Bob", "hello", ""},
		{"implicit needs one peer", "hello", []node.PeerInfo{bob, bobby}, "", "", "2 peers"},
		{"implicit with no peers", "hello", nil, "", "", "0 peers"},
		{"by name, any case", "@bob  hi there ", []node.PeerInfo{bob, bobby}, "Bob", "hi there", ""},
		{"by fingerprint prefix", "@" + strings.ToLower(bobFP) + " yo", []node.PeerInfo{bob, bobby}, "Bob", "yo", ""},
		{"unknown name", "@carol hi", []node.PeerInfo{bob}, "", "", "no connected peer"},
		{"missing body", "@bob", []node.PeerInfo{bob}, "", "", "usage"},
		{"ambiguous name", "@bob hi", []node.PeerInfo{bob, peerInfo("BOB", 0x80)}, "", "", "ambiguous"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			to, body, err := parseSend(tt.line, tt.peers)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want it to contain %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if to.Name != tt.wantTo || body != tt.wantBody {
				t.Errorf("got (%q, %q), want (%q, %q)", to.Name, body, tt.wantTo, tt.wantBody)
			}
		})
	}
}

func TestSanitize(t *testing.T) {
	if got := sanitize("hi\x1b[2J\x07 there\nline2\tok"); got != "hi[2J there\nline2\tok" {
		t.Errorf("sanitize = %q", got)
	}
}
