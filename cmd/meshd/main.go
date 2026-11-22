// Command meshd runs an Offline Mesh Chat node on a laptop.
//
// Milestone 0 only wires up flag parsing; networking arrives in milestone 1.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
)

// version is overridden at build time: go build -ldflags "-X main.version=v0.1.0".
var version = "dev"

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run is separated from main so tests can call it with fake args and buffers.
// It returns the process exit code.
func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("meshd", flag.ContinueOnError)
	fs.SetOutput(stderr)
	showVersion := fs.Bool("version", false, "print version and exit")

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}

	if *showVersion {
		fmt.Fprintf(stdout, "meshd %s\n", version)
		return 0
	}

	fmt.Fprintln(stderr, "meshd: node runtime not implemented yet (milestone 1)")
	return 1
}
