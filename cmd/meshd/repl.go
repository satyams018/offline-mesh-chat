package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"strings"
	"unicode"

	"github.com/satyams018/offline-mesh-chat/core/crypto"
	"github.com/satyams018/offline-mesh-chat/core/node"
	"github.com/satyams018/offline-mesh-chat/runtime"
)

const helpText = `commands:
  @name message     send to a peer by display name or fingerprint prefix
  message           send to your only connected peer
  /peers            list connected peers
  /id               show your fingerprint
  /quit             exit (Ctrl-D also works)`

// repl is the terminal chat UI. All writes to out happen on the loop
// goroutine, so lines never interleave.
type repl struct {
	rt   *runtime.Runtime
	self crypto.NodeID
	out  io.Writer
}

func (r *repl) loop(ctx context.Context, stdin io.Reader) {
	lines := make(chan string)
	go func() {
		defer close(lines)
		sc := bufio.NewScanner(stdin)
		sc.Buffer(make([]byte, 64*1024), 64*1024)
		for sc.Scan() {
			select {
			case lines <- sc.Text():
			case <-ctx.Done():
				return
			}
		}
	}()

	updates := r.rt.Updates()
	for {
		select {
		case <-ctx.Done():
			return
		case line, ok := <-lines:
			if !ok || !r.handleLine(line) {
				return
			}
		case act, ok := <-updates:
			if !ok {
				return
			}
			r.show(act)
		}
	}
}

// handleLine runs one input line. It returns false to quit.
func (r *repl) handleLine(line string) bool {
	line = strings.TrimSpace(line)
	switch {
	case line == "":
	case line == "/quit" || line == "/exit":
		return false
	case line == "/help":
		fmt.Fprintln(r.out, helpText)
	case line == "/id":
		fmt.Fprintf(r.out, "you are %s\n", r.self.Fingerprint())
	case line == "/peers":
		peers := r.rt.Peers()
		if len(peers) == 0 {
			fmt.Fprintln(r.out, "no peers connected")
		}
		for _, p := range peers {
			fmt.Fprintf(r.out, "  %s  %s\n", p.ID.Fingerprint(), p.Name)
		}
	case strings.HasPrefix(line, "/"):
		fmt.Fprintf(r.out, "unknown command %s (try /help)\n", line)
	default:
		to, body, err := parseSend(line, r.rt.Peers())
		if err != nil {
			fmt.Fprintf(r.out, "! %v\n", err)
			return true
		}
		if err := r.rt.Send(to.ID, body); err != nil {
			fmt.Fprintf(r.out, "! %v\n", err)
		}
	}
	return true
}

func (r *repl) show(act node.Action) {
	switch a := act.(type) {
	case node.DeliverToApp:
		fmt.Fprintf(r.out, "[%s] %s\n", r.senderLabel(a.Message.Sender), sanitize(a.Message.Body))
	case node.PeerReady:
		fmt.Fprintf(r.out, "* %s (%s) connected\n", a.Info.Name, a.Info.ID.Fingerprint())
	case node.PeerGone:
		fmt.Fprintf(r.out, "* %s disconnected\n", a.Info.Name)
	case node.SendFailed:
		fmt.Fprintf(r.out, "! not sent: %v\n", a.Err)
	}
}

func (r *repl) senderLabel(id crypto.NodeID) string {
	for _, p := range r.rt.Peers() {
		if p.ID == id {
			return p.Name
		}
	}
	return id.Fingerprint()
}

// parseSend turns "@target body" or "body" into a recipient and message.
func parseSend(line string, peers []node.PeerInfo) (node.PeerInfo, string, error) {
	if !strings.HasPrefix(line, "@") {
		if len(peers) != 1 {
			return node.PeerInfo{}, "", fmt.Errorf("%d peers connected: say who with @name message", len(peers))
		}
		return peers[0], line, nil
	}
	target, body, _ := strings.Cut(line[1:], " ")
	body = strings.TrimSpace(body)
	if target == "" || body == "" {
		return node.PeerInfo{}, "", fmt.Errorf("usage: @name message")
	}
	p, err := resolvePeer(target, peers)
	return p, body, err
}

// resolvePeer matches a display name (case-insensitive) first, then a
// fingerprint prefix of at least 4 characters.
func resolvePeer(target string, peers []node.PeerInfo) (node.PeerInfo, error) {
	var matches []node.PeerInfo
	for _, p := range peers {
		if strings.EqualFold(p.Name, target) {
			matches = append(matches, p)
		}
	}
	if fp := crypto.NormalizeFingerprint(target); len(matches) == 0 && len(fp) >= 4 {
		for _, p := range peers {
			if strings.HasPrefix(crypto.NormalizeFingerprint(p.ID.Fingerprint()), fp) {
				matches = append(matches, p)
			}
		}
	}
	switch len(matches) {
	case 0:
		return node.PeerInfo{}, fmt.Errorf("no connected peer matches %q (see /peers)", target)
	case 1:
		return matches[0], nil
	default:
		return node.PeerInfo{}, fmt.Errorf("%q is ambiguous: use a fingerprint prefix (see /peers)", target)
	}
}

// sanitize removes control characters (except newline and tab) from text that
// came from the network, so a peer can't send terminal escape sequences.
func sanitize(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) && r != '\n' && r != '\t' {
			return -1
		}
		return r
	}, s)
}
