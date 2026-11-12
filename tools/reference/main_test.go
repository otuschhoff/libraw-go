// SPDX-License-Identifier: LGPL-2.1-only
package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestMain(m *testing.M) {
	_, path, _, ok := runtime.Caller(0)
	if !ok {
		fmt.Fprintln(os.Stderr, "cannot locate reference test source")
		os.Exit(1)
	}
	if err := os.Chdir(filepath.Join(filepath.Dir(path), "../..")); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(m.Run())
}

func TestDigestAndDownload(t *testing.T) {
	payload := []byte("hash-checked fixture\n")
	path := filepath.Join(t.TempDir(), "payload")
	if err := os.WriteFile(path, payload, 0600); err != nil {
		t.Fatal(err)
	}
	want, err := digest(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name   string
		status int
		body   []byte
		ok     bool
	}{
		{"valid", 200, payload, true},
		{"success-shaped-error", 200, []byte("invalid download URL"), false},
		{"http-error", 404, payload, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(test.status)
				if _, err := w.Write(test.body); err != nil {
					t.Error(err)
				}
			}))
			defer server.Close()
			target := filepath.Join(t.TempDir(), "download")
			err := download(server.URL, target, want.SHA256)
			if (err == nil) != test.ok {
				t.Fatalf("download error = %v, want success %v", err, test.ok)
			}
			if !test.ok {
				if _, err := os.Stat(target); !os.IsNotExist(err) {
					t.Fatalf("failed download left destination: %v", err)
				}
				return
			}
			server.Close()
			if err := download(server.URL, target, want.SHA256); err != nil {
				t.Fatalf("verified cache required network: %v", err)
			}
			if err := os.WriteFile(target, []byte("corrupt cache"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := download(server.URL, target, want.SHA256); err == nil {
				t.Fatal("corrupt cache was accepted")
			}
		})
	}
}

func TestDeterministicCompression(t *testing.T) {
	data := bytes.Repeat([]byte{0x34, 0x12, 0x00, 0x0f}, 10000)
	var first []byte
	for pass := 0; pass < 2; pass++ {
		path := filepath.Join(t.TempDir(), "samples.u16le")
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
		if err := compress(path); err != nil {
			t.Fatal(err)
		}
		compressed, err := os.ReadFile(path + ".gz")
		if err != nil {
			t.Fatal(err)
		}
		if pass == 0 {
			first = compressed
		} else if !bytes.Equal(first, compressed) {
			t.Fatal("gzip output differs between runs")
		}
		reader, err := gzip.NewReader(bytes.NewReader(compressed))
		if err != nil {
			t.Fatal(err)
		}
		got, err := io.ReadAll(reader)
		if err != nil {
			t.Fatal(err)
		}
		if err := reader.Close(); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, data) {
			t.Fatal("samples changed during gzip round trip")
		}
		if err := compress(path); err == nil {
			t.Fatal("existing compressed golden was overwritten")
		}
	}
}

func syntheticORF() []byte {
	data := make([]byte, 400)
	copy(data, []byte{'I', 'I', 'R', 'O', 8, 0, 0, 0})
	type entry struct {
		id    uint16
		typ   uint16
		count uint32
		value uint32
	}
	entries := []entry{
		{0x100, 4, 1, 2}, {0x101, 4, 1, 4}, {0x102, 3, 1, 16},
		{0x103, 3, 1, 1}, {0x106, 3, 1, 1}, {0x111, 4, 1, 300},
		{0x116, 4, 1, 4}, {0x117, 4, 1, 16}, {0x8769, 4, 1, 180},
	}
	binary.LittleEndian.PutUint16(data[8:], uint16(len(entries)))
	for i, entry := range entries {
		offset := 10 + i*12
		binary.LittleEndian.PutUint16(data[offset:], entry.id)
		binary.LittleEndian.PutUint16(data[offset+2:], entry.typ)
		binary.LittleEndian.PutUint32(data[offset+4:], entry.count)
		binary.LittleEndian.PutUint32(data[offset+8:], entry.value)
	}
	binary.LittleEndian.PutUint16(data[180:], 1)
	binary.LittleEndian.PutUint16(data[182:], 0x927c)
	binary.LittleEndian.PutUint16(data[184:], 7)
	binary.LittleEndian.PutUint32(data[186:], 16)
	binary.LittleEndian.PutUint32(data[190:], 240)
	copy(data[240:], []byte("OLYMPUS\x00II\x03\x00"))
	for i := 0; i < 8; i++ {
		binary.LittleEndian.PutUint16(data[300+i*2:], uint16(i*16))
	}
	return data
}

func TestInspectAndWordEvidence(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "input.orf")
	data := syntheticORF()
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	evidence, err := inspect(path)
	if err != nil {
		t.Fatal(err)
	}
	if evidence.Width != 2 || evidence.Height != 4 || !evidence.Contiguous ||
		evidence.PayloadBytes != 16 || evidence.MakerNoteOffset != 240 {
		t.Fatalf("unexpected evidence: %+v", evidence)
	}
	samples := make([]byte, 16)
	for i := 0; i < 8; i++ {
		binary.LittleEndian.PutUint16(samples[i*2:], uint16(i))
	}
	samplePath := filepath.Join(directory, "samples")
	if err := os.WriteFile(samplePath, samples, 0600); err != nil {
		t.Fatal(err)
	}
	count, low4, err := compareWords(path, samplePath, evidence, 4)
	if err != nil || count != 0 || low4 != 0 {
		t.Fatalf("word comparison: mismatches %d, low4 %d, error %v", count, low4, err)
	}
	samples[0] = 1
	if err := os.WriteFile(samplePath, samples, 0600); err != nil {
		t.Fatal(err)
	}
	count, _, err = compareWords(path, samplePath, evidence, 4)
	if err != nil || count != 1 {
		t.Fatalf("failed to detect changed sample: %d, %v", count, err)
	}
	if _, _, err := compareWords(path, samplePath, evidence, 0); err == nil {
		t.Fatal("unproved numeric shift accepted")
	}
}

func TestInspectRejectsMalformed(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func([]byte) []byte
	}{
		{"short-header", func(b []byte) []byte { return b[:3] }},
		{"not-orf", func(b []byte) []byte { b[2] = 42; return b }},
		{"ifd-outside", func(b []byte) []byte { binary.LittleEndian.PutUint32(b[4:], 0xffffffff); return b }},
		{"entry-budget", func(b []byte) []byte { binary.LittleEndian.PutUint16(b[8:], 65535); return b }},
		{"duplicate-tag", func(b []byte) []byte { copy(b[22:24], b[10:12]); return b }},
		{"count-budget", func(b []byte) []byte { binary.LittleEndian.PutUint32(b[14:], 0xffffffff); return b }},
		{"strip-outside", func(b []byte) []byte { binary.LittleEndian.PutUint32(b[78:], 0xfffffff0); return b }},
		{"missing-maker", func(b []byte) []byte { binary.LittleEndian.PutUint16(b[182:], 0x927d); return b }},
		{"maker-outside", func(b []byte) []byte { binary.LittleEndian.PutUint32(b[190:], 390); return b }},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "bad.orf")
			if err := os.WriteFile(path, test.mutate(syntheticORF()), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := inspect(path); err == nil {
				t.Fatal("malformed evidence accepted")
			}
		})
	}
}

func TestConfig(t *testing.T) {
	var pins lock
	var corpus manifest
	if err := readJSON("tools/reference/lock.json", &pins); err != nil {
		t.Fatal(err)
	}
	if err := readJSON("testdata/fixtures.json", &corpus); err != nil {
		t.Fatal(err)
	}
	if err := validateConfig(pins, corpus); err != nil {
		t.Fatal(err)
	}
	if err := verifyProvenance(corpus); err != nil {
		t.Fatal(err)
	}
	corpus.Fixtures[0].SHA256 = strings.Repeat("a", 64)
	if err := verifyProvenance(corpus); err == nil {
		t.Fatal("fixture hash contradicted license evidence without error")
	}
	corpus.Fixtures[0].ID = "../outside"
	if err := validateConfig(pins, corpus); err == nil {
		t.Fatal("path traversal fixture accepted")
	}
	if validDigest(strings.Repeat("x", 64)) || validDigest("") {
		t.Fatal("invalid digest accepted")
	}
	if !validDigest(strings.Repeat("a", 64)) {
		t.Fatal("valid digest rejected")
	}
	if err := referenceToolchain(pins, "go1.26.0"); err != nil {
		t.Fatal(err)
	}
	if err := referenceToolchain(pins, "go1.27.3"); err == nil {
		t.Fatal("unpinned gzip toolchain accepted for golden generation")
	}
}

func TestExifVersionAndCanonicalization(t *testing.T) {
	got, err := decodeExif([]byte(`[{"SourceFile":"unreproducible/path","ExifTool:ExifToolVersion":13.40,"IFD0:Model":"E-1"}]`), "13.40")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := got["SourceFile"]; ok {
		t.Fatal("source filename leaked into reproducible metadata")
	}
	if got["IFD0:Model"] != "E-1" {
		t.Fatal("metadata lost")
	}
	for _, data := range []string{
		`[]`, `[{}]`, `[{"ExifTool:ExifToolVersion":13.41}]`, `[{},{}]`, `not JSON`,
		`[{"ExifTool:ExifToolVersion":13.40,"ExifTool:Error":"bad metadata"}]`,
		`[{"ExifTool:ExifToolVersion":13.40}] []`,
		`[{"ExifTool:ExifToolVersion":13.40}] trailing`,
	} {
		if _, err := decodeExif([]byte(data), "13.40"); err == nil {
			t.Fatalf("unexpected ExifTool response accepted: %s", data)
		}
	}
	duplicates, err := decodeExif([]byte(`[{"ExifTool:ExifToolVersion":13.40,"Olympus:ValidBits":"12 0","Olympus:Copy1:ValidBits":"12 0","ExifTool:Warning":"known quirk","ExifTool:Copy1:Warning":"second quirk"}]`), "13.40")
	if err != nil || len(duplicates) != 5 {
		t.Fatalf("duplicate-safe tags/diagnostics lost: %v, %v", duplicates, err)
	}
}

func TestCommandArgumentValidation(t *testing.T) {
	for _, args := range [][]string{
		nil, {"unknown"}, {"fetch", "unexpected"}, {"build", "unexpected"},
		{"verify", "unexpected"}, {"reproduce", "unexpected"}, {"generate"},
	} {
		if err := run(args); err == nil {
			t.Fatalf("invalid command accepted: %v", args)
		}
	}
}

func TestArchiveExtraction(t *testing.T) {
	// extract uses the pinned cache path, so give each test a unique archive
	// filename and remove only that file afterward.
	if err := os.MkdirAll(cache, 0755); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name string
		path string
		typ  byte
		ok   bool
	}{
		{"regular", "pinned/file", tar.TypeReg, true},
		{"traversal", "pinned/../../outside", tar.TypeReg, false},
		{"absolute", "/outside", tar.TypeReg, false},
		{"symlink", "pinned/link", tar.TypeSymlink, false},
		{"global-pax", "pax_global_header", tar.TypeXGlobalHeader, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			file, err := os.CreateTemp(cache, "test-archive-")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { os.Remove(file.Name()) })
			gz := gzip.NewWriter(file)
			writer := tar.NewWriter(gz)
			header := &tar.Header{Name: test.path, Mode: 0644, Typeflag: test.typ}
			if test.typ == tar.TypeReg {
				header.Size = 3
			} else if test.typ == tar.TypeXGlobalHeader {
				header = &tar.Header{Typeflag: tar.TypeXGlobalHeader,
					PAXRecords: map[string]string{"comment": "pinned source archive"}}
			} else {
				header.Linkname = "/outside"
			}
			if err := writer.WriteHeader(header); err != nil {
				t.Fatal(err)
			}
			if test.typ == tar.TypeReg {
				if _, err := writer.Write([]byte("abc")); err != nil {
					t.Fatal(err)
				}
			}
			if test.typ == tar.TypeXGlobalHeader {
				if err := writer.WriteHeader(&tar.Header{Name: "pinned/file", Mode: 0644, Size: 3}); err != nil {
					t.Fatal(err)
				}
				if _, err := writer.Write([]byte("abc")); err != nil {
					t.Fatal(err)
				}
			}
			for _, closer := range []io.Closer{writer, gz, file} {
				if err := closer.Close(); err != nil {
					t.Fatal(err)
				}
			}
			hash, err := digest(file.Name())
			if err != nil {
				t.Fatal(err)
			}
			src := source{Archive: filepath.Base(file.Name()), Directory: "pinned", SHA256: hash.SHA256}
			err = extract(src, t.TempDir())
			if (err == nil) != test.ok {
				t.Fatalf("extract error %v, want success %v", err, test.ok)
			}
		})
	}
}

func TestJSONCanonical(t *testing.T) {
	first, err := jsonBytes(map[string]int{"b": 2, "a": 1})
	if err != nil {
		t.Fatal(err)
	}
	second, err := jsonBytes(map[string]int{"a": 1, "b": 2})
	if err != nil || !bytes.Equal(first, second) {
		t.Fatal("JSON map serialization is not deterministic")
	}
	var decoded map[string]int
	if err := json.Unmarshal(first, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded["a"] != 1 || decoded["b"] != 2 {
		t.Fatal("JSON serialization changed values")
	}
}
