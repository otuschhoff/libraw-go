// SPDX-License-Identifier: LGPL-2.1-only

package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	libraw "github.com/otuschhoff/libraw-go"
)

func TestArgumentsAndFailures(t *testing.T) {
	for _, args := range [][]string{nil, {"-unknown"}, {"-format", "bad", "-output", "x", "x.orf"},
		{"-format", "raw", "-output", "x", "-exposure", "1", "x.orf"},
		{"-format", "preview", "-output", "x", "-allow-estimated-white", "x.orf"},
		{"-format", "raw", "-output", "x", "-preserve-orientation", "x.orf"}} {
		var stderr bytes.Buffer
		if code := run(context.Background(), args, &stderr); code != 2 || stderr.Len() == 0 {
			t.Fatalf("arguments %v: code %d, stderr %q", args, code, stderr.String())
		}
	}
	var stderr bytes.Buffer
	if code := run(context.Background(), []string{"-h"}, &stderr); code != 0 || !strings.Contains(stderr.String(), "16-bit") {
		t.Fatal("documented help missing")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.orf")
	if err := os.WriteFile(path, []byte("not an ORF"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, input := range []string{path, filepath.Join(dir, "missing.orf")} {
		var stderr bytes.Buffer
		output := filepath.Join(dir, "output.png")
		if code := run(context.Background(), []string{"-format", "png", "-output", output, input}, &stderr); code != 1 || stderr.Len() == 0 {
			t.Fatal("input failure not explicit")
		}
		if _, err := os.Stat(output); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("failed export created an output")
		}
	}
}

func TestRawExportAndWriteErrors(t *testing.T) {
	raw := &libraw.RawImage{Width: 2, Height: 2, Stride: 3, Pixels: []uint16{0, 4095, 99, 123, 65535, 99}}
	var output bytes.Buffer
	if err := writeRaw(context.Background(), &output, raw); err != nil {
		t.Fatal(err)
	}
	for i, want := range []uint16{0, 4095, 123, 65535} {
		if got := binary.LittleEndian.Uint16(output.Bytes()[i*2:]); got != want {
			t.Fatal("not unrotated, unscaled, tightly packed u16le")
		}
	}
	boom := errors.New("disk failed")
	if err := writeRaw(context.Background(), failingWriter{boom}, raw); !errors.Is(err, boom) {
		t.Fatal("write cause lost", err)
	}
	if err := writeRaw(context.Background(), shortWriter{}, raw); !errors.Is(err, io.ErrShortWrite) {
		t.Fatal("short write not rejected")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := writeRaw(ctx, io.Discard, raw); !errors.Is(err, context.Canceled) {
		t.Fatal("cancellation lost")
	}
}

type failingWriter struct{ err error }

func (w failingWriter) Write([]byte) (int, error) { return 0, w.err }

type shortWriter struct{}

func (shortWriter) Write(data []byte) (int, error) { return len(data) - 1, nil }

func TestAtomicNoReplacePublication(t *testing.T) {
	dir := t.TempDir()
	output := filepath.Join(dir, "result")
	write := func(w io.Writer) error {
		_, err := w.Write([]byte("complete"))
		return err
	}
	if err := publish(context.Background(), output, write); err != nil {
		t.Fatal(err)
	}
	if err := publish(context.Background(), output, write); !errors.Is(err, os.ErrExist) {
		t.Fatal("existing output not protected", err)
	}
	got, err := os.ReadFile(output)
	if err != nil || string(got) != "complete" {
		t.Fatal("existing output changed")
	}
	failed := filepath.Join(dir, "failed")
	boom := errors.New("encoder failed")
	if err := publish(context.Background(), failed, func(w io.Writer) error {
		if _, err := w.Write([]byte("partial")); err != nil {
			return err
		}
		return boom
	}); !errors.Is(err, boom) {
		t.Fatal("encoder failure lost", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	if err := publish(ctx, failed, func(w io.Writer) error {
		cancel()
		_, err := w.Write([]byte("canceled"))
		return err
	}); !errors.Is(err, context.Canceled) {
		t.Fatal("canceled output published", err)
	}
	if _, err := os.Stat(failed); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("partial output published")
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 || entries[0].Name() != "result" {
		t.Fatal("temporary output leaked", err)
	}
}
