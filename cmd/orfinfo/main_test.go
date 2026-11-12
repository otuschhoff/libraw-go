// SPDX-License-Identifier: LGPL-2.1-only

package main

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestCommandUsageAndErrors(t *testing.T) {
	for _, test := range []struct {
		args []string
		code int
	}{{nil, 2}, {[]string{"one", "two"}, 2}, {[]string{"--help"}, 0}, {[]string{"-h"}, 0}, {[]string{filepath.Join(t.TempDir(), "missing")}, 1}} {
		var out, stderr bytes.Buffer
		if code := run(test.args, &out, &stderr); code != test.code || out.Len() != 0 || stderr.Len() == 0 {
			t.Fatalf("%v: code %d, out %q, stderr %q", test.args, code, out.String(), stderr.String())
		}
	}
	path := filepath.Join(t.TempDir(), "not.orf")
	if err := os.WriteFile(path, []byte("not an ORF"), 0600); err != nil {
		t.Fatal(err)
	}
	var out, stderr bytes.Buffer
	if code := run([]string{path}, &out, &stderr); code != 1 || out.Len() != 0 || stderr.Len() == 0 {
		t.Fatal("invalid ORF command succeeded")
	}
}

func TestCommandDeterministicJSONAndOutputFailure(t *testing.T) {
	// A safe metadata-only unsupported sensor IFD, independent of photographs.
	data := make([]byte, 128)
	copy(data, "IIRO\x08\x00\x00\x00")
	tags := []struct {
		tag, typ uint16
		value    uint32
	}{{0x100, 4, 2}, {0x101, 4, 2}, {0x102, 3, 16}, {0x103, 3, 1}, {0x106, 3, 1}, {0x115, 3, 1}}
	binary.LittleEndian.PutUint16(data[8:], uint16(len(tags)))
	for i, tag := range tags {
		r := data[10+i*12:]
		binary.LittleEndian.PutUint16(r, tag.tag)
		binary.LittleEndian.PutUint16(r[2:], tag.typ)
		binary.LittleEndian.PutUint32(r[4:], 1)
		binary.LittleEndian.PutUint32(r[8:], tag.value)
	}
	path := filepath.Join(t.TempDir(), "input.without-orf-extension")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	var first, second, stderr bytes.Buffer
	if run([]string{path}, &first, &stderr) != 0 || run([]string{path}, &second, &stderr) != 0 ||
		!bytes.Equal(first.Bytes(), second.Bytes()) || stderr.Len() != 0 {
		t.Fatal("non-deterministic or failed report")
	}
	var result struct {
		SchemaVersion int
		Capabilities  struct {
			Metadata, Raw struct{ Available bool }
		}
	}
	if err := json.Unmarshal(first.Bytes(), &result); err != nil || result.SchemaVersion != 1 ||
		!result.Capabilities.Metadata.Available || result.Capabilities.Raw.Available {
		t.Fatalf("wrong report shape: %+v, %v", result, err)
	}
	if code := run([]string{path}, brokenWriter{}, &stderr); code != 1 || stderr.Len() == 0 {
		t.Fatal("output failure not reported")
	}
}

type brokenWriter struct{}

func (brokenWriter) Write([]byte) (int, error) { return 0, errors.New("test output failure") }
