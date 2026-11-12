// SPDX-License-Identifier: LGPL-2.1-only

package libraw

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"image"
	"image/jpeg"
	"io"
	"strings"
	"sync"
	"testing"

	"github.com/otuschhoff/libraw-go/internal/tiff"
)

type testEntry struct {
	tag   uint16
	typ   tiff.Type
	count uint32
	data  []byte
	at    uint32
}

func uintEntry(tag uint16, typ tiff.Type, values ...uint32) testEntry {
	size := 4
	if typ == tiff.Short {
		size = 2
	}
	data := make([]byte, len(values)*size)
	for i, value := range values {
		if size == 2 {
			binary.LittleEndian.PutUint16(data[i*size:], uint16(value))
		} else {
			binary.LittleEndian.PutUint32(data[i*size:], value)
		}
	}
	return testEntry{tag: tag, typ: typ, count: uint32(len(values)), data: data}
}

func textEntry(tag uint16, value string) testEntry {
	return testEntry{tag: tag, typ: tiff.ASCII, count: uint32(len(value) + 1), data: append([]byte(value), 0)}
}

type orfBuilder struct {
	data []byte
	free int
}

func (b *orfBuilder) directory(at, base int, entries []testEntry, next uint32, classic bool) {
	binary.LittleEndian.PutUint16(b.data[at:], uint16(len(entries)))
	for i, entry := range entries {
		record := b.data[at+2+i*12 : at+14+i*12]
		binary.LittleEndian.PutUint16(record, entry.tag)
		binary.LittleEndian.PutUint16(record[2:], uint16(entry.typ))
		binary.LittleEndian.PutUint32(record[4:], entry.count)
		if entry.at != 0 {
			binary.LittleEndian.PutUint32(record[8:], entry.at-uint32(base))
		} else if len(entry.data) <= 4 {
			copy(record[8:], entry.data)
		} else {
			binary.LittleEndian.PutUint32(record[8:], uint32(b.free-base))
			copy(b.data[b.free:], entry.data)
			b.free += len(entry.data)
		}
	}
	if classic {
		binary.LittleEndian.PutUint32(b.data[at+2+len(entries)*12:], next)
	}
}

func makeTestORF(modern bool) []byte {
	b := orfBuilder{data: make([]byte, 16384), free: 4096}
	copy(b.data, []byte{'I', 'I', 'R', 'O', 8, 0, 0, 0})
	model, id, header, base, start := "E-1", "D4040", "OLYMP\x00\x02\x00", 0, 1032
	cfa := []byte{2, 0, 2, 0, 1, 0, 2, 1}
	if modern {
		model, id, header, base, start = "E-410", "S0013", "OLYMPUS\x00II\x03\x00", 1024, 1036
		cfa = []byte{2, 0, 2, 0, 0, 1, 1, 2}
	}
	copy(b.data[1024:], header)
	imageEntries := []testEntry{
		uintEntry(0x100, tiff.Long, 16), uintEntry(0x101, tiff.Long, 8),
		uintEntry(0x102, tiff.Short, 16), uintEntry(0x103, tiff.Short, 1),
		uintEntry(0x106, tiff.Short, 1), textEntry(0x10f, "OLYMPUS CORPORATION "),
		textEntry(0x110, model+" "), uintEntry(0x111, tiff.Long, 8192),
		uintEntry(0x112, tiff.Short, 6), uintEntry(0x115, tiff.Short, 1),
		uintEntry(0x116, tiff.Long, 8), uintEntry(0x117, tiff.Long, 256),
		textEntry(0x131, "Version test"), uintEntry(0x8769, tiff.Long, 512),
	}
	if modern {
		imageEntries[11] = uintEntry(0x117, tiff.Long, 20)
		copy(b.data[8192:], []byte{0, 0, 0, 0, 1, 0, 0})
	}
	b.directory(8, 0, imageEntries, 0, true)
	b.directory(512, 0, []testEntry{
		{tag: 0xa302, typ: tiff.Undefined, count: 8, data: cfa},
		{tag: 0x927c, typ: tiff.Undefined, count: 1024, at: 1024},
		uintEntry(0x8827, tiff.Short, 100),
	}, 0, true)
	ip := []testEntry{
		uintEntry(0x0100, tiff.Short, 512, 384), uintEntry(0x0600, tiff.Short, 64, 65, 66, 67),
		uintEntry(0x0611, tiff.Short, 12, 0), uintEntry(0x0612, tiff.Short, 1, 0),
		uintEntry(0x0613, tiff.Short, 2, 0), uintEntry(0x0614, tiff.Long, 14),
		uintEntry(0x0615, tiff.Long, 5),
	}
	drive := uintEntry(0x0600, tiff.Short, 0, 0)
	if modern {
		drive = uintEntry(0x0600, tiff.Short, 0, 0, 0)
	}
	children := []struct {
		tag     uint16
		at      int
		entries []testEntry
	}{{0x2010, 1400, []testEntry{textEntry(0x0100, id)}},
		{0x2020, 1500, []testEntry{uintEntry(0x0100, tiff.Long, 0), drive}},
		{0x2040, 1800, ip}}
	root := []testEntry{uintEntry(0x0200, tiff.Long, 0, 0, 0)}
	for _, child := range children {
		if modern {
			root = append(root, uintEntry(child.tag, tiff.IFDType, uint32(child.at-base)))
		} else {
			root = append(root, testEntry{tag: child.tag, typ: tiff.Undefined, count: uint32(len(child.entries) * 12), at: uint32(child.at)})
		}
		b.directory(child.at, base, child.entries, 0, false)
	}
	b.directory(start, base, root, 0, false)
	return b.data
}

func recordAt(data []byte, at int, tag uint16) []byte {
	n := int(binary.LittleEndian.Uint16(data[at:]))
	for i := range n {
		record := data[at+2+i*12 : at+14+i*12]
		if binary.LittleEndian.Uint16(record) == tag {
			return record
		}
	}
	panic("test tag absent")
}

func openTest(t *testing.T, data []byte, opts OpenOptions) *File {
	t.Helper()
	file, err := Open(bytes.NewReader(data), int64(len(data)), opts)
	if err != nil {
		t.Fatal(err)
	}
	return file
}

func TestORFMetadataPlansAndOwnership(t *testing.T) {
	for _, modern := range []bool{false, true} {
		t.Run(map[bool]string{false: "OLYMP", true: "OLYMPUS"}[modern], func(t *testing.T) {
			data := makeTestORF(modern)
			f := openTest(t, data, OpenOptions{})
			m := f.Metadata()
			wantCFA := CFA{{Green1, Red}, {Blue, Green2}}
			wantBlack := Levels{{65, 64}, {67, 66}}
			wantWB := Levels{{1, 2}, {1.5, 1}}
			layout := "olympus-word16le-high12"
			if modern {
				wantCFA, wantBlack, wantWB = CFA{{Red, Green1}, {Green2, Blue}}, Levels{{64, 65}, {66, 67}}, Levels{{2, 1}, {1, 1.5}}
				layout = "olympus-legacy-predictive12"
			}
			if m.Make != "OLYMPUS CORPORATION " || m.NormalizedMake != "Olympus" || !strings.HasSuffix(m.Model, " ") || m.NormalizedModel != strings.TrimSpace(m.Model) {
				t.Fatalf("camera identity lost: %+v", m)
			}
			if m.Geometry.ActiveArea.Value != image.Rect(1, 2, 15, 7) || m.Geometry.Width != 16 || m.Geometry.Height != 8 ||
				m.CFA.Value != wantCFA || m.BitDepth.Value != 12 || m.Layout.Value != layout ||
				m.Calibration.Black.Value != wantBlack || m.Calibration.WhiteBalance.Value != wantWB {
				t.Fatalf("incorrect parsed metadata: %+v", m)
			}
			if m.Calibration.White.Present() || m.Calibration.Color.Present() || m.Firmware.Present() || m.Orientation.Value != OrientationRightTop {
				t.Fatalf("manufactured calibration or changed orientation: %+v", m)
			}
			if f.plan.outputBytes != 256 || f.plan.width != 16 || len(f.plan.strips) != 1 || f.plan.strips[0].row != 0 || f.plan.strips[0].rows != 8 {
				t.Fatalf("incorrect plan: %+v", f.plan)
			}
			if !f.Capabilities().Metadata.Available || !f.Capabilities().Raw.Available || f.Capabilities().Render.Available {
				t.Fatal("unsupported operations advertised")
			}
			m.Diagnostics[0].Message = "caller mutation"
			caps := f.Capabilities()
			caps.Raw.Available = false
			if f.Metadata().Diagnostics[0].Message == "caller mutation" || !f.Capabilities().Raw.Available {
				t.Fatal("snapshot aliases internal state")
			}
			result, err := f.DecodeRaw(context.Background())
			if modern {
				if result != nil || (!errors.Is(err, ErrCorrupt) && !errors.Is(err, ErrTruncated)) {
					t.Fatalf("invalid compressed test payload must fail: %+v, %v", result, err)
				}
			} else if err != nil || len(result.Pixels) != 128 {
				t.Fatalf("word decode: %+v, %v", result, err)
			}
			if result, err := f.Preview(context.Background()); result != nil || !errors.Is(err, ErrNoPreview) {
				t.Fatalf("missing preview: %+v, %v", result, err)
			}
		})
	}
}

func TestORFMalformedGraphsAndLimits(t *testing.T) {
	tests := []struct {
		name string
		edit func([]byte)
		opts OpenOptions
		kind error
	}{
		{"next cycle", func(b []byte) { binary.LittleEndian.PutUint32(b[8+2+14*12:], 8) }, OpenOptions{}, ErrCorrupt},
		{"SubIFD cycle", func(b []byte) {
			r := recordAt(b, 8, 0x131)
			binary.LittleEndian.PutUint16(r, 0x14a)
			binary.LittleEndian.PutUint16(r[2:], uint16(tiff.Long))
			binary.LittleEndian.PutUint32(r[4:], 1)
			binary.LittleEndian.PutUint32(r[8:], 8)
		}, OpenOptions{}, ErrCorrupt},
		{"ambiguous raw", func(b []byte) { copy(b[256:256+174], b[8:8+174]); binary.LittleEndian.PutUint32(b[8+2+14*12:], 256) }, OpenOptions{}, ErrCorrupt},
		{"duplicate tag", func(b []byte) { binary.LittleEndian.PutUint16(recordAt(b, 8, 0x110), 0x10f) }, OpenOptions{}, ErrCorrupt},
		{"null EXIF", func(b []byte) { binary.LittleEndian.PutUint32(recordAt(b, 8, 0x8769)[8:], 0) }, OpenOptions{}, ErrCorrupt},
		{"EXIF alias", func(b []byte) { binary.LittleEndian.PutUint32(recordAt(b, 8, 0x8769)[8:], 8) }, OpenOptions{}, ErrCorrupt},
		{"EXIF next", func(b []byte) { binary.LittleEndian.PutUint32(b[512+2+3*12:], 512) }, OpenOptions{}, ErrCorrupt},
		{"old incorrect base", func(b []byte) { r := recordAt(b, 1032, 0x2010); binary.LittleEndian.PutUint32(r[8:], 1400-1024) }, OpenOptions{}, ErrCorrupt},
		{"old incorrect size", func(b []byte) { r := recordAt(b, 1032, 0x2010); binary.LittleEndian.PutUint32(r[4:], 11) }, OpenOptions{}, ErrCorrupt},
		{"MakerNote root extent", func(b []byte) { binary.LittleEndian.PutUint32(recordAt(b, 512, 0x927c)[4:], 32) }, OpenOptions{}, ErrCorrupt},
		{"crop bounds", func(b []byte) { binary.LittleEndian.PutUint32(recordAt(b, 1800, 0x0614)[8:], 16) }, OpenOptions{}, ErrCorrupt},
		{"strip truncated", func(b []byte) { binary.LittleEndian.PutUint32(recordAt(b, 8, 0x111)[8:], uint32(len(b)-128)) }, OpenOptions{}, ErrTruncated},
		{"strip zero bytes", func(b []byte) { binary.LittleEndian.PutUint32(recordAt(b, 8, 0x117)[8:], 0) }, OpenOptions{}, ErrCorrupt},
		{"strip row coverage", func(b []byte) { binary.LittleEndian.PutUint32(recordAt(b, 8, 0x116)[8:], 1) }, OpenOptions{}, ErrCorrupt},
		{"zero rows", func(b []byte) { binary.LittleEndian.PutUint32(recordAt(b, 8, 0x116)[8:], 0) }, OpenOptions{}, ErrCorrupt},
		{"strip type", func(b []byte) { binary.LittleEndian.PutUint16(recordAt(b, 8, 0x111)[2:], uint16(tiff.Short)) }, OpenOptions{}, ErrCorrupt},
		{"wrong width count", func(b []byte) { binary.LittleEndian.PutUint32(recordAt(b, 8, 0x100)[4:], 2) }, OpenOptions{}, ErrCorrupt},
		{"zero width", func(b []byte) { binary.LittleEndian.PutUint32(recordAt(b, 8, 0x100)[8:], 0) }, OpenOptions{}, ErrCorrupt},
		{"orientation invalid", func(b []byte) { binary.LittleEndian.PutUint16(recordAt(b, 8, 0x112)[8:], 9) }, OpenOptions{}, ErrCorrupt},
		{"dimension limit", func([]byte) {}, OpenOptions{Limits: Limits{Dimension: 15}}, ErrLimitExceeded},
		{"pixel limit", func([]byte) {}, OpenOptions{Limits: Limits{Pixels: 127}}, ErrLimitExceeded},
		{"metadata limit", func([]byte) {}, OpenOptions{Limits: Limits{MetadataBytes: 512}}, ErrLimitExceeded},
		{"input limit", func([]byte) {}, OpenOptions{Limits: Limits{InputBytes: 16383}}, ErrLimitExceeded},
		{"IFD limit", func([]byte) {}, OpenOptions{Limits: Limits{IFDs: 3, Depth: 3}}, ErrLimitExceeded},
		{"depth limit", func([]byte) {}, OpenOptions{Limits: Limits{Depth: 3}}, ErrLimitExceeded},
		{"entry limit", func([]byte) {}, OpenOptions{Limits: Limits{EntriesPerIFD: 13}}, ErrLimitExceeded},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			data := makeTestORF(false)
			test.edit(data)
			file, err := Open(bytes.NewReader(data), int64(len(data)), test.opts)
			if file != nil || !errors.Is(err, test.kind) {
				t.Fatalf("want %v; got %v, %v", test.kind, file, err)
			}
		})
	}
}

func TestORFRecognitionAndUnsupported(t *testing.T) {
	for _, sig := range []string{"IIRS", "MMOR", "II*\x00", "junk"} {
		data := makeTestORF(false)
		copy(data, sig)
		_, err := Open(bytes.NewReader(data), int64(len(data)), OpenOptions{})
		want := ErrNotORF
		if sig == "IIRS" || sig == "MMOR" {
			want = ErrUnsupported
		}
		if !errors.Is(err, want) {
			t.Fatalf("%q: %v", sig, err)
		}
	}
	for n := range 8 {
		data := makeTestORF(false)[:n]
		if f, err := Open(bytes.NewReader(data), int64(n), OpenOptions{}); f != nil || !errors.Is(err, ErrTruncated) {
			t.Fatalf("short header %d: %v", n, err)
		}
	}
	for _, change := range []func([]byte){
		func(b []byte) {
			r := recordAt(b, 8, 0x110)
			at := binary.LittleEndian.Uint32(r[8:])
			copy(b[at:], "X-1")
		},
		func(b []byte) { binary.LittleEndian.PutUint16(recordAt(b, 8, 0x103)[8:], 7) },
		func(b []byte) { binary.LittleEndian.PutUint16(recordAt(b, 1800, 0x0611)[8:], 14) },
		func(b []byte) { copy(b[1024:], "UNKNOWN!") },
		func(b []byte) { binary.LittleEndian.PutUint16(recordAt(b, 1500, 0x0600)[8:], 5) },
		func(b []byte) {
			binary.LittleEndian.PutUint16(recordAt(b, 512, 0xa302)[2:], uint16(tiff.Undefined))
			at := binary.LittleEndian.Uint32(recordAt(b, 512, 0xa302)[8:])
			b[at+4], b[at+5] = 0, 2
		},
	} {
		data := makeTestORF(false)
		change(data)
		file := openTest(t, data, OpenOptions{})
		if !file.Capabilities().Metadata.Available || file.Capabilities().Raw.Available || file.Metadata().Layout.Present() {
			t.Fatalf("unsupported mode falsely planned: %+v", file.Metadata())
		}
	}
	modern := makeTestORF(true)
	modern[8192] = 14
	if f := openTest(t, modern, OpenOptions{}); f.Metadata().Layout.Present() || f.Capabilities().Raw.Available {
		t.Fatal("new codec treated as legacy")
	}
	modern = makeTestORF(true)
	binary.LittleEndian.PutUint32(recordAt(modern, 1036, 0x2010)[8:], 1400)
	if f, err := Open(bytes.NewReader(modern), int64(len(modern)), OpenOptions{}); err == nil && (f.Metadata().Layout.Present() || f.Metadata().CameraID.Present()) {
		t.Fatal("wrong modern base supplied camera identity or a decode plan")
	}
}

func jpegBytes(t testing.TB, width, height int) []byte {
	t.Helper()
	var buffer bytes.Buffer
	if err := jpeg.Encode(&buffer, image.NewGray(image.Rect(0, 0, width, height)), nil); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func withPreviews(t testing.TB) ([]byte, []byte, []byte) {
	t.Helper()
	data := makeTestORF(true)
	small, large := jpegBytes(t, 3, 2), jpegBytes(t, 5, 3)
	copy(data[10000:], small)
	copy(data[11000:], large)
	b := orfBuilder{data: data, free: 5000}
	b.directory(1500, 1024, []testEntry{
		uintEntry(0x0100, tiff.Long, 1), uintEntry(0x0101, tiff.Long, 11000-1024),
		uintEntry(0x0102, tiff.Long, uint32(len(large))), uintEntry(0x0600, tiff.Short, 0, 0, 0),
	}, 0, false)
	b.directory(256, 0, []testEntry{
		uintEntry(0x201, tiff.Long, 10000), uintEntry(0x202, tiff.Long, uint32(len(small))),
		uintEntry(0x112, tiff.Short, 8),
	}, 0, true)
	binary.LittleEndian.PutUint32(data[8+2+14*12:], 256)
	return data, small, large
}

func TestPreviewSelectionBytesOwnershipAndConcurrency(t *testing.T) {
	data, _, large := withPreviews(t)
	f := openTest(t, data, OpenOptions{})
	if !f.Capabilities().Preview.Available || f.preview.offset != 11000 {
		t.Fatalf("wrong preview selected: %+v", f.preview)
	}
	p, err := f.Preview(context.Background())
	if err != nil || !bytes.Equal(p.JPEG, large) || p.Width != 5 || p.Height != 3 || p.Orientation.Present() || len(p.Diagnostics) == 0 {
		t.Fatalf("preview mismatch: %+v, %v", p, err)
	}
	p.JPEG[0] = 0
	p.Diagnostics[0].Message = "caller"
	next, err := f.Preview(context.Background())
	if err != nil || !bytes.Equal(next.JPEG, large) || next.Diagnostics[0].Message == "caller" {
		t.Fatal("preview not caller-owned")
	}
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 10 {
				got, err := f.Preview(context.Background())
				if err != nil || !bytes.Equal(got.JPEG, large) {
					t.Errorf("concurrent preview: %v", err)
				}
				m := f.Metadata()
				m.Diagnostics[0].Message = "private"
			}
		}()
	}
	wg.Wait()
	data[11000] = 0 // This violates input immutability, solely to construct another File.
	smaller := openTest(t, data, OpenOptions{})
	preview, err := smaller.Preview(context.Background())
	if err != nil || preview.Width != 3 || preview.Orientation.Value != OrientationLeftBottom {
		t.Fatalf("invalid primary did not select oriented thumbnail: %+v, %v", preview, err)
	}
}

func TestPreviewLimitsCancellationAndIO(t *testing.T) {
	data, small, _ := withPreviews(t)
	f := openTest(t, data, OpenOptions{Limits: Limits{PreviewBytes: int64(len(small) - 1)}})
	if f.Capabilities().Preview.Available {
		t.Fatal("over-limit preview available")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if p, err := f.Preview(ctx); p != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation lost: %v", err)
	}
	if p, err := f.DecodeRaw(ctx); p != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("decode cancellation lost: %v", err)
	}
	if _, err := f.Preview(nil); !errors.Is(err, ErrInvalidOptions) {
		t.Fatalf("nil context: %v", err)
	}
	var empty File
	if _, err := empty.Preview(context.Background()); !errors.Is(err, ErrInvalidOptions) {
		t.Fatalf("unopened file: %v", err)
	}
	diskErr := errors.New("test disk failure")
	reader := &switchReader{input: bytes.NewReader(data)}
	file, err := Open(reader, int64(len(data)), OpenOptions{})
	if err != nil {
		t.Fatal(err)
	}
	reader.err = diskErr
	if result, err := file.Preview(context.Background()); result != nil || !errors.Is(err, diskErr) {
		t.Fatalf("preview reader error: %+v, %v", result, err)
	}
	reader.err = nil
	ctx, cancel = context.WithCancel(context.Background())
	reader.cancel = cancel
	if result, err := file.Preview(ctx); result != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("mid-operation cancellation: %+v, %v", result, err)
	}
	reader.cancel, reader.err = nil, diskErr
	if f, err := Open(reader, int64(len(data)), OpenOptions{}); f != nil || !errors.Is(err, diskErr) {
		t.Fatalf("open reader error: %+v, %v", f, err)
	}
}

type switchReader struct {
	input  io.ReaderAt
	err    error
	cancel context.CancelFunc
}

func (r *switchReader) ReadAt(data []byte, offset int64) (int, error) {
	if r.err != nil {
		return 0, r.err
	}
	if r.cancel != nil {
		r.cancel()
	}
	return r.input.ReadAt(data, offset)
}

func FuzzOpenORF(f *testing.F) {
	f.Add(makeTestORF(false))
	f.Add(makeTestORF(true))
	em5 := em5TestORF("E-M5", "S0036")
	wb := recordAt(em5, 1800, 0x0100)
	binary.LittleEndian.PutUint32(wb[4:], 4)
	binary.LittleEndian.PutUint32(wb[8:], 7400-1024)
	for i, value := range []uint16{384, 512, 256, 256} {
		binary.LittleEndian.PutUint16(em5[7400+i*2:], value)
	}
	f.Add(em5)
	wordSeed, _ := wordORF(16, 8, 3)
	f.Add(wordSeed)
	predictiveSeed, _ := encodeLegacy(legacyPlane(16, 8), 16, 8)
	f.Add(legacyORF(16, 8, predictiveSeed))
	f.Add([]byte("IIRO\x08\x00\x00\x00"))
	previewSeed, _, _ := withPreviews(f)
	f.Add(previewSeed)
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 1<<20 {
			return
		}
		opts := OpenOptions{Limits: Limits{InputBytes: 1 << 20, MetadataBytes: 64 << 10, IFDs: 16, Depth: 8, EntriesPerIFD: 64, Pixels: 1024, Dimension: 64, PreviewBytes: 64 << 10, WorkingBytes: 256 << 10}}
		file, err := Open(bytes.NewReader(data), int64(len(data)), opts)
		if err != nil {
			if file != nil {
				t.Fatal("partial file on error")
			}
			return
		}
		checkFuzzOperations(t, file)
	})
}
