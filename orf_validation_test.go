// SPDX-License-Identifier: LGPL-2.1-only

package libraw

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"testing"

	"github.com/otuschhoff/libraw-go/internal/tiff"
)

func TestRequiredMetadataTypesAndCounts(t *testing.T) {
	for _, path := range []struct {
		at  int
		tag uint16
	}{
		{8, 0x100}, {8, 0x101}, {8, 0x102}, {8, 0x103}, {8, 0x106},
		{8, 0x112}, {8, 0x115}, {8, 0x116}, {8, 0x8769},
		{512, 0x8827}, {512, 0xa302}, {512, 0x927c},
		{1032, 0x0200}, {1400, 0x0100}, {1500, 0x0100}, {1500, 0x0600},
		{1800, 0x0100}, {1800, 0x0600}, {1800, 0x0611}, {1800, 0x0612},
		{1800, 0x0613}, {1800, 0x0614}, {1800, 0x0615},
	} {
		for _, changeType := range []bool{false, true} {
			if path.at == 1400 && !changeType {
				continue // ASCII length is variable, not a fixed-count contract.
			}
			t.Run(fmt.Sprintf("%d/%04x/type=%v", path.at, path.tag, changeType), func(t *testing.T) {
				data := makeTestORF(false)
				r := recordAt(data, path.at, path.tag)
				if changeType {
					binary.LittleEndian.PutUint16(r[2:], uint16(tiff.Byte))
				} else {
					count := binary.LittleEndian.Uint32(r[4:])
					binary.LittleEndian.PutUint32(r[4:], count+1)
				}
				file, err := Open(bytes.NewReader(data), int64(len(data)), OpenOptions{})
				// Changing the MakerNote length alone can still bound a complete
				// root; it does not change the location of pointed metadata.
				if path.at == 512 && path.tag == 0x927c && !changeType {
					if err != nil {
						t.Fatal(err)
					}
					return
				}
				// DriveMode's other evidenced length is valid, but cannot select
				// this fixture's plan with the altered inline/out-of-line data.
				if path.at == 1500 && path.tag == 0x0600 && !changeType && err == nil {
					if file.Metadata().Layout.Present() {
						t.Fatal("unevidenced drive-mode vector planned")
					}
					return
				}
				if file != nil || err == nil {
					t.Fatalf("malformed field accepted: %+v, %v", file, err)
				}
			})
		}
	}
}

func TestAdditionalMetadataAndCalibrationValidation(t *testing.T) {
	data := makeTestORF(false)
	builder := orfBuilder{data: data, free: 5000}
	rational := func(tag uint16, n, d uint32) testEntry {
		value := make([]byte, 8)
		binary.LittleEndian.PutUint32(value, n)
		binary.LittleEndian.PutUint32(value[4:], d)
		return testEntry{tag: tag, typ: tiff.Rational, count: 1, data: value}
	}
	builder.directory(512, 0, []testEntry{
		{tag: 0xa302, typ: tiff.Undefined, count: 8, data: []byte{2, 0, 2, 0, 1, 0, 2, 1}},
		{tag: 0x927c, typ: tiff.Undefined, count: 1024, at: 1024},
		rational(0x829a, 1, 200), rational(0x829d, 63, 10), rational(0x920a, 14, 1),
		uintEntry(0x8827, tiff.Short, 100),
	}, 0, true)
	m := openTest(t, data, OpenOptions{}).Metadata()
	if m.ExposureTime.Value != 0.005 || m.FNumber.Value != 6.3 || m.FocalLength.Value != 14 {
		t.Fatal("rational metadata differs")
	}
	for _, test := range []struct {
		name string
		edit func([]byte)
	}{
		{"zero denominator", func(b []byte) {
			at := binary.LittleEndian.Uint32(recordAt(b, 512, 0x829a)[8:])
			binary.LittleEndian.PutUint32(b[at+4:], 0)
		}},
		{"zero exposure", func(b []byte) {
			at := binary.LittleEndian.Uint32(recordAt(b, 512, 0x829a)[8:])
			binary.LittleEndian.PutUint32(b[at:], 0)
		}},
		{"wrong rational type", func(b []byte) {
			binary.LittleEndian.PutUint16(recordAt(b, 512, 0x829a)[2:], uint16(tiff.SRational))
		}},
		{"wrong rational count", func(b []byte) {
			binary.LittleEndian.PutUint32(recordAt(b, 512, 0x829d)[4:], 2)
		}},
		{"zero WB", func(b []byte) { binary.LittleEndian.PutUint16(recordAt(b, 1800, 0x0100)[8:], 0) }},
		{"black outside sensor domain", func(b []byte) {
			at := binary.LittleEndian.Uint32(recordAt(b, 1800, 0x0600)[8:])
			binary.LittleEndian.PutUint16(b[at:], 4096)
		}},
		{"zero valid bits", func(b []byte) { binary.LittleEndian.PutUint16(recordAt(b, 1800, 0x0611)[8:], 0) }},
		{"invalid valid bits component", func(b []byte) { binary.LittleEndian.PutUint16(recordAt(b, 1800, 0x0611)[10:], 1) }},
		{"invalid valid bits range", func(b []byte) { binary.LittleEndian.PutUint16(recordAt(b, 1800, 0x0611)[8:], 17) }},
		{"invalid crop component", func(b []byte) { binary.LittleEndian.PutUint16(recordAt(b, 1800, 0x0612)[10:], 1) }},
		{"partial crop", func(b []byte) { binary.LittleEndian.PutUint16(recordAt(b, 1800, 0x0614), 0x0714) }},
		{"zero crop width", func(b []byte) { binary.LittleEndian.PutUint32(recordAt(b, 1800, 0x0614)[8:], 0) }},
		{"bad text", func(b []byte) {
			r := recordAt(b, 8, 0x10f)
			at, count := binary.LittleEndian.Uint32(r[8:]), binary.LittleEndian.Uint32(r[4:])
			b[at+count-1] = 'x'
		}},
		{"short MakerNote header", func(b []byte) { binary.LittleEndian.PutUint32(recordAt(b, 512, 0x927c)[4:], 5) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			altered := bytes.Clone(data)
			test.edit(altered)
			if file, err := Open(bytes.NewReader(altered), int64(len(altered)), OpenOptions{}); file != nil || !errors.Is(err, ErrCorrupt) {
				t.Fatalf("wanted corrupt; got %+v, %v", file, err)
			}
		})
	}
	// Consistent duplicate semantic tags across old root/ImageProcessing are
	// permitted; contradictory values are not.
	for _, conflict := range []bool{false, true} {
		altered := bytes.Clone(data)
		b := orfBuilder{data: altered, free: 5500}
		values := []uint32{64, 65, 66, 67}
		if conflict {
			values[0]++
		}
		b.directory(1032, 0, []testEntry{
			uintEntry(0x0200, tiff.Long, 0, 0, 0),
			uintEntry(0x1012, tiff.Short, values...), uintEntry(0x102c, tiff.Short, 12, 0),
			{tag: 0x2010, typ: tiff.Undefined, count: 12, at: 1400},
			{tag: 0x2020, typ: tiff.Undefined, count: 24, at: 1500},
			{tag: 0x2040, typ: tiff.Undefined, count: 84, at: 1800},
		}, 0, false)
		file, err := Open(bytes.NewReader(altered), int64(len(altered)), OpenOptions{})
		if conflict {
			if file != nil || !errors.Is(err, ErrCorrupt) {
				t.Fatalf("conflicting black accepted: %v", err)
			}
		} else if err != nil {
			t.Fatal(err)
		}
	}
}

func TestPayloadOrganizationsAndLogicalOrdering(t *testing.T) {
	for _, test := range []struct {
		name string
		edit func([]byte)
		kind error
	}{
		{"tile metadata only", func(b []byte) {
			binary.LittleEndian.PutUint16(recordAt(b, 8, 0x111), 0x144)
			binary.LittleEndian.PutUint16(recordAt(b, 8, 0x117), 0x145)
		}, nil},
		{"incomplete strip pair", func(b []byte) { binary.LittleEndian.PutUint16(recordAt(b, 8, 0x117), 0x0700) }, ErrCorrupt},
		{"incomplete tile pair", func(b []byte) { binary.LittleEndian.PutUint16(recordAt(b, 8, 0x111), 0x144) }, ErrCorrupt},
		{"missing strip rows", func(b []byte) { binary.LittleEndian.PutUint16(recordAt(b, 8, 0x116), 0x0700) }, ErrCorrupt},
		{"raw overlaps table", func(b []byte) { binary.LittleEndian.PutUint32(recordAt(b, 8, 0x111)[8:], 8) }, ErrCorrupt},
		{"raw overlaps metadata", func(b []byte) {
			at := binary.LittleEndian.Uint32(recordAt(b, 1800, 0x0600)[8:])
			binary.LittleEndian.PutUint32(recordAt(b, 8, 0x111)[8:], at)
		}, ErrCorrupt},
	} {
		t.Run(test.name, func(t *testing.T) {
			data := makeTestORF(false)
			test.edit(data)
			file, err := Open(bytes.NewReader(data), int64(len(data)), OpenOptions{})
			if test.kind == nil {
				if err != nil || file.Metadata().Layout.Present() || file.Capabilities().Raw.Available {
					t.Fatalf("unsupported organization not safe metadata-only: %+v, %v", file, err)
				}
			} else if file != nil || !errors.Is(err, test.kind) {
				t.Fatalf("wanted %v, got %+v, %v", test.kind, file, err)
			}
		})
	}
	for _, order := range []struct {
		name    string
		offsets []uint32
		planned bool
		kind    error
	}{
		{"contiguous", []uint32{8192, 8320}, true, nil},
		{"reversed", []uint32{8320, 8192}, false, nil},
		{"gap", []uint32{8192, 8400}, false, nil},
		{"overlap", []uint32{8192, 8256}, false, ErrCorrupt},
	} {
		t.Run(order.name, func(t *testing.T) {
			data := makeTestORF(false)
			b := orfBuilder{data: data, free: 5500}
			for _, entry := range []testEntry{
				uintEntry(0x111, tiff.Long, order.offsets...),
				uintEntry(0x117, tiff.Long, 128, 128),
			} {
				// Write the replacement entry through the same fixture encoder.
				b.directory(6000, 0, []testEntry{entry}, 0, false)
				copy(recordAt(data, 8, entry.tag), data[6002:6014])
			}
			binary.LittleEndian.PutUint32(recordAt(data, 8, 0x116)[8:], 4)
			file, err := Open(bytes.NewReader(data), int64(len(data)), OpenOptions{})
			if order.kind != nil {
				if file != nil || !errors.Is(err, order.kind) {
					t.Fatalf("overlap accepted: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if file.Metadata().Layout.Present() != order.planned || file.plan.strips[0].offset != int64(order.offsets[0]) ||
				file.plan.strips[1].row != 4 || file.plan.strips[1].rows != 4 {
				t.Fatalf("logical order or eligibility changed: %+v", file.plan)
			}
		})
	}
}

func TestRawSelectionNotFirstAndOldFramingBoundaries(t *testing.T) {
	data, _, _ := withPreviews(t)
	binary.LittleEndian.PutUint32(data[4:], 256)
	binary.LittleEndian.PutUint32(data[256+2+3*12:], 8)
	binary.LittleEndian.PutUint32(data[8+2+14*12:], 0)
	file := openTest(t, data, OpenOptions{})
	if file.Metadata().NormalizedModel != "E-410" || file.plan.layout != "olympus-legacy-predictive12" || !file.Capabilities().Preview.Available {
		t.Fatal("sensor directory was not selected behind a preview IFD")
	}
	data = makeTestORF(false)
	equipment := recordAt(data, 1032, 0x2010)
	binary.LittleEndian.PutUint32(equipment[4:], 14)
	if f := openTest(t, data, OpenOptions{}); f.Metadata().CameraID.Value != "D4040" {
		t.Fatal("exact old block framing not supported")
	}
	binary.LittleEndian.PutUint32(equipment[4:], 12)
	binary.LittleEndian.PutUint32(equipment[8:], uint32(len(data)-12))
	binary.LittleEndian.PutUint16(data[len(data)-12:], 1)
	if f, err := Open(bytes.NewReader(data), int64(len(data)), OpenOptions{}); f != nil || !errors.Is(err, ErrTruncated) {
		t.Fatalf("old two-byte omission waived actual file bounds: %v", err)
	}
}

func TestPreviewInvalidHeadersTieBreaksAndQuotas(t *testing.T) {
	for _, edit := range []func([]byte){
		func(b []byte) {
			at := bytes.Index(b[11000:], []byte{0xff, 0xc0})
			if at < 0 {
				panic("test JPEG lacks baseline SOF")
			}
			b[11000+at+4] = 12 // Unsupported JPEG sample precision.
		},
		func(b []byte) {
			binary.LittleEndian.PutUint32(recordAt(b, 1500, 0x0102)[8:], 4)
			copy(b[11000:], []byte{0xff, 0xd8, 0xff, 0xd9})
		},
	} {
		data, _, _ := withPreviews(t)
		edit(data)
		file := openTest(t, data, OpenOptions{})
		preview, err := file.Preview(context.Background())
		if err != nil || preview.Width != 3 {
			t.Fatalf("invalid primary header did not select thumbnail: %v", err)
		}
	}
	data, small, _ := withPreviews(t)
	copy(data[11000:], small)
	binary.LittleEndian.PutUint32(recordAt(data, 1500, 0x0102)[8:], uint32(len(small)))
	file := openTest(t, data, OpenOptions{})
	if file.preview.offset != 10000 {
		t.Fatal("equal-area/equal-length tie did not choose lowest offset")
	}
	lowWorking := OpenOptions{Limits: Limits{MetadataBytes: 32768, PreviewBytes: 16384, WorkingBytes: 65536}}
	limited := openTest(t, data, lowWorking)
	if limited.Capabilities().Preview.Available || !limited.Capabilities().Metadata.Available {
		t.Fatal("JPEG scratch quota not enforced independently")
	}
	for _, test := range []struct {
		edit func([]byte)
		kind error
	}{
		{func(b []byte) { binary.LittleEndian.PutUint16(recordAt(b, 256, 0x202), 0x0302) }, ErrCorrupt},
		{func(b []byte) { binary.LittleEndian.PutUint32(recordAt(b, 256, 0x201)[8:], uint32(len(b)-1)) }, ErrTruncated},
		{func(b []byte) { binary.LittleEndian.PutUint32(recordAt(b, 1500, 0x0100)[8:], 2) }, ErrCorrupt},
		{func(b []byte) { binary.LittleEndian.PutUint16(recordAt(b, 1500, 0x0101), 0x0301) }, ErrCorrupt},
		{func(b []byte) { binary.LittleEndian.PutUint32(recordAt(b, 1500, 0x0102)[8:], 0) }, ErrCorrupt},
	} {
		altered := bytes.Clone(data)
		test.edit(altered)
		if f, err := Open(bytes.NewReader(altered), int64(len(altered)), OpenOptions{}); f != nil || !errors.Is(err, test.kind) {
			t.Fatalf("preview structure accepted: %v", err)
		}
	}
	// Reader failures during JPEG inspection must not become "no preview."
	diskErr := errors.New("JPEG header read failed")
	reader := &rangeFailure{input: bytes.NewReader(data), at: 10000, err: diskErr}
	if f, err := Open(reader, int64(len(data)), OpenOptions{}); f != nil || !errors.Is(err, diskErr) {
		t.Fatalf("JPEG read error lost: %v", err)
	}
	// The stream's zero-length/EOF contract is independent of JPEG buffering.
	stream := &extentStream{file: file, pos: 10000, end: 10000}
	if n, err := stream.Read(nil); n != 0 || err != nil {
		t.Fatal("zero length read")
	}
	if n, err := stream.Read(make([]byte, 1)); n != 0 || err != io.EOF {
		t.Fatal("missing bounded EOF")
	}
}

type rangeFailure struct {
	input io.ReaderAt
	at    int64
	err   error
}

func (r *rangeFailure) ReadAt(data []byte, at int64) (int, error) {
	if at == r.at {
		return 0, r.err
	}
	return r.input.ReadAt(data, at)
}

func TestDimensionPlatformBounds(t *testing.T) {
	data := makeTestORF(false)
	binary.LittleEndian.PutUint32(recordAt(data, 8, 0x100)[8:], 65536)
	binary.LittleEndian.PutUint32(recordAt(data, 8, 0x101)[8:], 65536)
	opts := OpenOptions{Limits: Limits{Dimension: 1 << 20, Pixels: 1 << 34}}
	file, err := Open(bytes.NewReader(data), int64(len(data)), opts)
	if ^uint(0)>>32 == 0 {
		if file != nil || !errors.Is(err, ErrLimitExceeded) {
			t.Fatalf("32-bit geometry guard: %v", err)
		}
	} else if !errors.Is(err, ErrCorrupt) {
		// On 64-bit, the later row-coverage check rejects this synthetic payload.
		t.Fatalf("64-bit geometry unexpectedly rejected before layout validation: %v", err)
	}
}
