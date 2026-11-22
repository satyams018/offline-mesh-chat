// Command meshd runs an Offline Mesh Chat node on a laptop.
//
//	meshd -name alice                                  # listen on :7777
//	meshd -name bob -peer 192.168.43.10:7777           # and dial alice
//
// Milestone 1: direct chat between connected peers, in the terminal.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/satyams018/offline-mesh-chat/core/crypto"
	"github.com/satyams018/offline-mesh-chat/core/node"
	"github.com/satyams018/offline-mesh-chat/runtime"
)

// version is overridden at build time: go build -ldflags "-X main.version=v0.1.0".
var version = "dev"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(run(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

type options struct {
	name    string
	listen  string
	peers   []string
	dataDir string
	verbose bool
}

// parseFlags returns the options, or an exit code if the program should stop.
func parseFlags(args []string, stdout, stderr io.Writer) (options, int, bool) {
	var o options
	fs := flag.NewFlagSet("meshd", flag.ContinueOnError)
	fs.SetOutput(stderr)
	showVersion := fs.Bool("version", false, "print version and exit")
	fs.StringVar(&o.name, "name", defaultName(), "display name shown to peers")
	fs.StringVar(&o.listen, "listen", ":7777", `TCP address to accept peers on ("" to disable)`)
	fs.Func("peer", "peer address host:port to keep connected to (repeatable)", func(s string) error {
		o.peers = append(o.peers, s)
		return nil
	})
	fs.StringVar(&o.dataDir, "data-dir", defaultDataDir(), "directory for this node's identity key")
	fs.BoolVar(&o.verbose, "v", false, "verbose logging")

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return o, 0, false
		}
		return o, 2, false
	}
	if *showVersion {
		fmt.Fprintf(stdout, "meshd %s\n", version)
		return o, 0, false
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "meshd: unexpected argument %q\n", fs.Arg(0))
		return o, 2, false
	}
	if err := node.ValidateName(o.name); err != nil {
		fmt.Fprintf(stderr, "meshd: -name: %v\n", err)
		return o, 2, false
	}
	if o.dataDir == "" {
		fmt.Fprintln(stderr, "meshd: -data-dir is required (no user config dir found)")
		return o, 2, false
	}
	return o, 0, true
}

// run is separated from main so tests can call it with fake args and streams.
// It returns the process exit code.
func run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	o, code, ok := parseFlags(args, stdout, stderr)
	if !ok {
		return code
	}

	level := slog.LevelWarn
	if o.verbose {
		level = slog.LevelDebug
	}
	log := slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: level}))

	ident, err := crypto.LoadOrCreateIdentity(o.dataDir)
	if err != nil {
		fmt.Fprintf(stderr, "meshd: %v\n", err)
		return 1
	}
	rt, err := runtime.New(node.Config{Self: ident.ID(), Name: o.name}, log)
	if err != nil {
		fmt.Fprintf(stderr, "meshd: %v\n", err)
		return 1
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	listening := "not listening"
	if o.listen != "" {
		addr, err := rt.Listen(ctx, o.listen)
		if err != nil {
			fmt.Fprintf(stderr, "meshd: %v\n", err)
			return 1
		}
		listening = "listening on " + addr.String()
	}
	runDone := make(chan struct{})
	go func() { rt.Run(ctx); close(runDone) }()
	for _, p := range o.peers {
		go rt.DialLoop(ctx, p)
	}

	fmt.Fprintf(stdout, "meshd %s · %s (%s) · %s\n", version, o.name, ident.ID().Fingerprint(), listening)
	if len(o.peers) > 0 {
		fmt.Fprintf(stdout, "connecting to %s\n", strings.Join(o.peers, ", "))
	}
	fmt.Fprintln(stdout, "type /help for commands")

	r := &repl{rt: rt, self: ident.ID(), out: stdout}
	r.loop(ctx, stdin)
	cancel()
	<-runDone
	return 0
}

func defaultName() string {
	h, err := os.Hostname()
	if err != nil {
		return "anonymous"
	}
	h = strings.TrimSuffix(h, ".local")
	if node.ValidateName(h) != nil {
		return "anonymous"
	}
	return h
}

func defaultDataDir() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "offline-mesh-chat")
}
