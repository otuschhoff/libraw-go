// SPDX-License-Identifier: LGPL-2.1-only

// Command orfinfo writes deterministic, versioned JSON metadata/capabilities.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"

	libraw "github.com/otuschhoff/libraw-go"
)

func run(args []string, out, stderr io.Writer) int {
	if len(args) != 1 || args[0] == "-h" || args[0] == "--help" {
		fmt.Fprintln(stderr, "usage: orfinfo FILE.orf")
		if len(args) == 1 && (args[0] == "-h" || args[0] == "--help") {
			return 0
		}
		return 2
	}
	input, err := os.Open(args[0])
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer input.Close()
	info, err := input.Stat()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	file, err := libraw.Open(context.Background(), input, info.Size(), libraw.OpenOptions{})
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer file.Close()
	report := struct {
		SchemaVersion int
		Metadata      libraw.Metadata
		Capabilities  libraw.Capabilities
	}{1, file.Metadata(), file.Capabilities()}
	encoder := json.NewEncoder(out)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(report); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }
