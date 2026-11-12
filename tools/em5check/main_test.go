// SPDX-License-Identifier: LGPL-2.1-only

package main

import (
	"context"
	"errors"
	"flag"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestExplicitOptInAndLimits(t *testing.T) {
	for _, args := range [][]string{
		nil, {"-root", "x"}, {"-reference", "x"}, {"-root", "x", "-reference", "x", "-workers", "0"},
		{"-root", "x", "-reference", "x", "-workers", "9"},
		{"-root", "x", "-reference", "x", "-per-mode", "1"},
	} {
		if err := run(context.Background(), args, io.Discard, io.Discard); err == nil {
			t.Fatal("invalid options accepted", args)
		}
	}
	if err := run(context.Background(), []string{"-h"}, io.Discard, io.Discard); !errors.Is(err, flag.ErrHelp) {
		t.Fatal(err)
	}
}

func TestNoSilentEmptyOrMalformedCorpus(t *testing.T) {
	dir := t.TempDir()
	if _, _, err := inventory(context.Background(), dir, 2); err == nil {
		t.Fatal("empty corpus accepted")
	}
	if err := os.WriteFile(filepath.Join(dir, "bad.ORF"), []byte("not an ORF"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := inventory(context.Background(), dir, 2); err == nil {
		t.Fatal("bad private file skipped")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := inventory(ctx, dir, 2); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
