// Command understudy is a local AWS emulator for development machines and CI.
//
// The command tree is built here and nowhere else; behavior lives in internal
// packages. The version is stamped at link time:
//
//	go build -ldflags "-X main.version=v0.1.0" ./cmd/understudy
package main

import (
	"fmt"
	"io"
	"os"
)

var version = "dev"

func main() {
	if err := run(os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "understudy: cannot write the version to stdout: %v\n", err)
		os.Exit(1)
	}
}

func run(stdout io.Writer) error {
	_, err := fmt.Fprintf(stdout, "understudy %s\n", version)
	return err
}
