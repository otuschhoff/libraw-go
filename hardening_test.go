// SPDX-License-Identifier: LGPL-2.1-only

package libraw

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
)

type sampleComparison struct {
	mismatches, first int
	got, want         uint16
	maxError          int
}

func (c *sampleComparison) add(index int, got, want uint16) {
	if got == want {
		return
	}
	if c.mismatches == 0 {
		c.first, c.got, c.want = index, got, want
	}
	c.mismatches++
	diff := int(got) - int(want)
	if diff < 0 {
		diff = -diff
	}
	c.maxError = max(c.maxError, diff)
}

func (c sampleComparison) summary(width int, layout string) string {
	return fmt.Sprintf("raw mismatch layout=%s first-sample=%d x=%d y=%d coordinate-domain=sensor got=%d want=%d mismatches=%d maximum-absolute-error=%d",
		layout, c.first, c.first%width, c.first/width, c.got, c.want, c.mismatches, c.maxError)
}

func TestSampleComparisonDiagnostics(t *testing.T) {
	var comparison sampleComparison
	comparison.add(0, 10, 10)
	if comparison.mismatches != 0 || comparison.maxError != 0 {
		t.Fatal("equal samples must have zero mismatches and error")
	}
	comparison.add(4, 100, 101)
	comparison.add(5, 0, 65535)
	comparison.add(6, 65535, 0)
	if comparison.mismatches != 3 || comparison.maxError != 65535 {
		t.Fatal("mismatch count or unsigned sample difference overflow")
	}
	for _, field := range []string{"layout=test", "first-sample=4", "x=1", "y=1", "got=100", "want=101", "mismatches=3", "maximum-absolute-error=65535"} {
		if !strings.Contains(comparison.summary(3, "test"), field) {
			t.Fatalf("missing mismatch diagnostic %s", field)
		}
	}
}

type metadataOnlyReader struct {
	input       *bytes.Reader
	allowPrefix bool
	rawReads    int
}

func (r *metadataOnlyReader) ReadAt(data []byte, offset int64) (int, error) {
	if offset >= 8192 {
		if r.allowPrefix && offset == 8192 && len(data) == 7 {
			return r.input.ReadAt(data, offset)
		}
		r.rawReads++
		return 0, errors.New("unexpected huge raw read")
	}
	return r.input.ReadAt(data, offset)
}

func TestRawArithmeticAtPlatformBoundary(t *testing.T) {
	for _, modern := range []bool{false, true} {
		data := makeTestORF(modern)
		for _, tag := range []uint16{0x100, 0x101, 0x116} {
			binary.LittleEndian.PutUint32(recordAt(data, 8, tag)[8:], 32768)
		}
		size := int64(len(data))
		if !modern {
			binary.LittleEndian.PutUint32(recordAt(data, 8, 0x117)[8:], 1<<31)
			size = 8192 + int64(1<<31)
		}
		reader := &metadataOnlyReader{input: bytes.NewReader(data), allowPrefix: modern}
		opts := OpenOptions{Limits: Limits{InputBytes: 1 << 33, Pixels: 1 << 30, WorkingBytes: 128 << 20}}
		file, err := Open(reader, size, opts)
		if strconv.IntSize == 32 {
			if file != nil || !errors.Is(err, ErrLimitExceeded) {
				t.Fatalf("2 GiB output must exceed platform int before allocation: %v", err)
			}
		} else {
			if err != nil || file.Capabilities().Raw.Available || file.Capabilities().Raw.Reason == "" {
				t.Fatalf("huge geometry/quota capability: %v", err)
			}
			if raw, err := file.DecodeRaw(context.Background()); raw != nil || !errors.Is(err, ErrLimitExceeded) {
				t.Fatal("oversized raw allocation/read was not rejected")
			}
		}
		if reader.rawReads != 0 {
			t.Fatal("huge raw payload read before output/quota checks")
		}
	}
}

func TestConcurrentFileOperations(t *testing.T) {
	data, _, _ := withPreviews(t)
	want := legacyPlane(16, 8)
	payload, _ := encodeLegacy(want, 16, 8)
	copy(data[8199:], payload)
	binary.LittleEndian.PutUint32(recordAt(data, 8, 0x117)[8:], uint32(7+len(payload)))
	file := openTest(t, data, OpenOptions{})
	before, caps := file.Metadata(), file.Capabilities()
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 10 {
				metadata := file.Metadata()
				if !reflect.DeepEqual(metadata, before) || file.Capabilities() != caps {
					t.Error("metadata/capabilities changed during concurrent operations")
					return
				}
				metadata.Diagnostics[0].Message = "caller-owned"
				preview, err := file.Preview(context.Background())
				if err != nil || len(preview.JPEG) == 0 {
					t.Errorf("concurrent preview: %v", err)
					return
				}
				preview.JPEG[0] = 0
				raw, err := file.DecodeRaw(context.Background())
				if err != nil || !reflect.DeepEqual(raw.Pixels, want) {
					t.Errorf("concurrent raw: %v", err)
					return
				}
				raw.Pixels[0]++
			}
		}()
	}
	wg.Wait()
	if !reflect.DeepEqual(before, file.Metadata()) {
		t.Fatal("operation results alias retained metadata")
	}
}

func checkFuzzOperations(t *testing.T, file *File) {
	t.Helper()
	m := file.Metadata()
	if !reflect.DeepEqual(m, file.Metadata()) || file.Capabilities().Render.Available ||
		(file.Capabilities().Raw.Available && m.Layout.Value != "olympus-word16le-high12" && m.Layout.Value != "olympus-legacy-predictive12") {
		t.Fatal("unstable metadata or false capabilities")
	}
	preview, err := file.Preview(context.Background())
	if err != nil && preview != nil {
		t.Fatal("partial preview on error")
	}
	raw, err := file.DecodeRaw(context.Background())
	if err != nil {
		if raw != nil || (file.Capabilities().Raw.Available && errors.Is(err, ErrUnsupported)) {
			t.Fatalf("partial raw result or implemented codec unsupported: %v", err)
		}
	} else if !file.Capabilities().Raw.Available || raw.Stride != raw.Width || len(raw.Pixels) != raw.Width*raw.Height {
		t.Fatal("invalid decoded raw shape or capability")
	}
	if !reflect.DeepEqual(m, file.Metadata()) {
		t.Fatal("decoding mutated metadata")
	}
}

func FuzzMakerNotes(f *testing.F) {
	for _, modern := range []bool{false, true} {
		seed := makeTestORF(modern)
		f.Add(uint16(0), seed[1024:2048], modern)
	}
	f.Fuzz(func(t *testing.T, at uint16, mutation []byte, modern bool) {
		if len(mutation) > 1024 {
			return
		}
		data := makeTestORF(modern)
		copy(data[1024+int(at)%1024:2048], mutation)
		opts := OpenOptions{Limits: Limits{InputBytes: 32768, MetadataBytes: 32768, PreviewBytes: 1, WorkingBytes: 65536,
			Pixels: 1024, Dimension: 64, IFDs: 16, Depth: 8, EntriesPerIFD: 64}}
		file, err := Open(bytes.NewReader(data), int64(len(data)), opts)
		if err != nil {
			if file != nil {
				t.Fatal("partial file on malformed MakerNote")
			}
			return
		}
		checkFuzzOperations(t, file)
	})
}
