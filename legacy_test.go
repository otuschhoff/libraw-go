// SPDX-License-Identifier: LGPL-2.1-only

package libraw

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math/bits"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/otuschhoff/libraw-go/internal/bounded"
)

func testLegacyBits(t testing.TB, data []byte, bufferSize int) *legacyBits {
	t.Helper()
	source, err := bounded.New(bytes.NewReader(data), int64(len(data)), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	return &legacyBits{source: source, buffer: make([]byte, bufferSize), end: int64(len(data))}
}

func bitString(text string) []byte {
	out := make([]byte, (len(text)+7)/8)
	for i, bit := range text {
		if bit == '1' {
			out[i/8] |= 1 << (7 - i%8)
		} else if bit != '0' {
			panic("invalid test bit")
		}
	}
	return out
}

func TestLegacyBitOrderBufferBoundariesAndEOF(t *testing.T) {
	for _, bufferSize := range []int{1, 2, 4096} {
		b := testLegacyBits(t, []byte{0xab, 0xcd, 0xef}, bufferSize)
		for _, field := range []struct {
			width uint8
			want  uint32
		}{{0, 0}, {3, 5}, {9, 0xbc}, {4, 0xd}, {8, 0xef}, {0, 0}} {
			got, err := b.read(field.width)
			if err != nil || got != field.want {
				t.Fatalf("buffer=%d width=%d got=%x want=%x: %v", bufferSize, field.width, got, field.want, err)
			}
		}
		if b.consumed != 24 {
			t.Fatal("bit consumption")
		}
		_, err := b.read(1)
		var detail *Error
		if !errors.Is(err, ErrTruncated) || !errors.As(err, &detail) ||
			detail.Offset == nil || *detail.Offset != 3 || detail.Bit == nil || *detail.Bit != 0 {
			t.Fatalf("missing EOF position: %v", err)
		}
	}
	b := testLegacyBits(t, []byte{0xff}, 1)
	if _, err := b.read(17); !errors.Is(err, ErrCorrupt) {
		t.Fatal("invalid field width accepted")
	}
	if _, err := b.read(16); !errors.Is(err, ErrTruncated) || b.consumed != 0 {
		t.Fatal("incomplete field zero-filled or committed")
	}
}

func TestLegacyPrefixesAndEscapeConsumption(t *testing.T) {
	for high := uint32(0); high < 12; high++ {
		text := strings.Repeat("0", int(high)) + "1"
		b := testLegacyBits(t, bitString(text), 1)
		got, err := b.high(4)
		if err != nil || got != high || b.consumed != uint64(len(text)) {
			t.Fatalf("prefix %q: %d, %v", text, got, err)
		}
	}
	b := testLegacyBits(t, bitString("000000000000"+"000000000011"+"0010"), 1)
	high, err := b.high(4)
	if err != nil || high != 1 || b.consumed != 24 {
		t.Fatal("escape discarded bit was not consumed")
	}
	suffix, err := b.read(4)
	if err != nil || suffix != 2 || b.consumed != 28 {
		t.Fatal("escape suffix misaligned")
	}
	for _, width := range []uint8{2, 16} {
		b := testLegacyBits(t, make([]byte, 8), 1)
		got, err := b.high(width)
		if err != nil || got != 0 || b.consumed != uint64(28-width) {
			t.Fatalf("zero escape width=%d: %d %v", width, got, err)
		}
	}
	for _, width := range []uint8{0, 1, 17, 255} {
		if _, err := b.high(width); !errors.Is(err, ErrCorrupt) {
			t.Fatal("invalid prefix width")
		}
	}
	for _, test := range []struct {
		bytes int
		width uint8
	}{{0, 4}, {1, 4}, {2, 4}} {
		b := testLegacyBits(t, make([]byte, test.bytes), 1)
		if _, err := b.high(test.width); !errors.Is(err, ErrTruncated) {
			t.Fatal("missing prefix/escape bits zero-filled")
		}
	}
}

func TestLegacyRefillsAndDeclaredStripBoundary(t *testing.T) {
	b := testLegacyBits(t, bytes.Repeat([]byte{0xa5, 0x5a}, 5000), legacyBufferBytes)
	b.end = 9001
	for range 4500 {
		if value, err := b.read(16); err != nil || value != 0xa55a {
			t.Fatalf("refill changed stream: %x %v", value, err)
		}
	}
	if value, err := b.read(8); err != nil || value != 0xa5 {
		t.Fatal("final partial buffer")
	}
	if _, err := b.read(1); !errors.Is(err, ErrTruncated) || b.next != 9001 {
		t.Fatal("read physically present bytes outside the declared strip")
	}
}

func TestLegacyResidualFieldTruncation(t *testing.T) {
	for _, test := range []struct {
		name string
		data []byte
		skip uint8
	}{
		{"flags", []byte{0}, 6},
		{"prefix", []byte{0}, 0},
		{"escape", []byte{0, 0}, 0},
		{"suffix", []byte{0x08}, 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			b := testLegacyBits(t, test.data, 1)
			if _, err := b.read(test.skip); err != nil {
				t.Fatal(err)
			}
			s := legacyState{}
			if _, _, err := s.residual(b); !errors.Is(err, ErrTruncated) || s != (legacyState{}) {
				t.Fatalf("incomplete field accepted or changed state: %+v %v", s, err)
			}
		})
	}
}

func TestLegacyAdaptiveWidthsAndResidualVectors(t *testing.T) {
	for _, test := range []struct {
		magnitude uint32
		small     uint8
		width     uint8
	}{{0, 0, 4}, {0, 3, 2}, {63, 0, 4}, {64, 0, 5}, {65535, 0, 14},
		{65535, 3, 16}, {65536, 0, 4}, {65536, 3, 2}} {
		s := legacyState{magnitude: test.magnitude, small: test.small}
		if got := s.width(); got != test.width {
			t.Fatalf("width %+v: %d", test, got)
		}
	}
	for _, test := range []struct {
		name       string
		text       string
		state      legacyState
		diff, bias int32
		magnitude  uint32
		low        uint32
		small      uint8
	}{
		{"positive", "001" + "1" + "0010", legacyState{}, 2, 0, 2, 1, 1},
		{"complement", "100" + "1" + "0000", legacyState{}, -1, -1, 0, 0, 1},
		{"negative bias", "000" + "1" + "0000", legacyState{bias: -1}, -1, -1, 0, 0, 1},
		{"positive bias", "011" + "1" + "0010", legacyState{bias: 31}, 33, 4, 2, 3, 1},
		{"escape", "000" + "000000000000" + "000000000011" + "0010", legacyState{}, 18, 1, 18, 0, 0},
		{"small boundary", "010" + "01" + "0000", legacyState{}, 16, 1, 16, 2, 1},
		{"large boundary", "000" + "01" + "0001", legacyState{}, 17, 1, 17, 0, 0},
		{"count saturation", "000" + "1" + "00", legacyState{small: 3}, 0, 0, 0, 0, 3},
		{"maximum magnitude", "000" + "000000000001" + "1111111111111111", legacyState{magnitude: 65535, small: 3}, 786431, 73727, 786431, 0, 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			s := test.state
			b := testLegacyBits(t, bitString(test.text), 1)
			diff, low, err := s.residual(b)
			if err != nil || diff != test.diff || low != test.low ||
				s.bias != test.bias || s.magnitude != test.magnitude || s.small != test.small ||
				b.consumed != uint64(len(test.text)) {
				t.Fatalf("diff=%d low=%d state=%+v bits=%d: %v", diff, low, s, b.consumed, err)
			}
		})
	}
	for _, low := range []uint32{0, 1, 2, 3} {
		b := testLegacyBits(t, bitString(fmt.Sprintf("%03b10010", low)), 1)
		s := legacyState{}
		diff, gotLow, err := s.residual(b)
		if err != nil || diff != 2 || gotLow != low {
			t.Fatal("low-bit combinations")
		}
	}
}

func TestLegacyPredictorBranches(t *testing.T) {
	for _, test := range []struct {
		name             string
		x, y             int
		w, n, nw, expect uint16
	}{
		{"origin", 0, 0, 0, 0, 0, 0}, {"first 2x2", 1, 1, 0, 0, 0, 0},
		{"first rows", 2, 1, 80, 0, 0, 80}, {"first columns", 1, 2, 0, 90, 0, 90},
		{"smooth", 2, 2, 80, 120, 100, 100}, {"threshold equal", 2, 2, 68, 132, 100, 100},
		{"threshold exceeded", 2, 2, 67, 132, 100, 99},
		{"reverse straddle", 2, 2, 132, 67, 100, 99},
		{"reverse smooth odd", 2, 2, 121, 80, 100, 100},
		{"far west", 2, 2, 140, 120, 100, 140}, {"far north", 2, 2, 120, 140, 100, 140},
		{"distance tie", 2, 2, 120, 120, 100, 120},
		{"northwest equals west", 2, 2, 100, 120, 100, 120},
		{"northwest equals north", 2, 2, 120, 100, 100, 120},
		{"opposite distance tie", 2, 2, 80, 120, 100, 100},
	} {
		t.Run(test.name, func(t *testing.T) {
			pixels := make([]uint16, 9)
			if test.x >= 2 {
				pixels[test.y*3+test.x-2] = test.w
			}
			if test.y >= 2 {
				pixels[(test.y-2)*3+test.x] = test.n
				if test.x >= 2 {
					pixels[(test.y-2)*3+test.x-2] = test.nw
				}
			}
			if got := legacyPredictor(pixels, 3, test.x, test.y); got != int32(test.expect) {
				t.Fatalf("predictor=%d want=%d", got, test.expect)
			}
		})
	}
}

// The encoder uses independent width arithmetic. Predictor arithmetic has
// separate fixed vectors; this helper provides varied complete ORF streams.
func encodeLegacy(pixels []uint16, width, height int) ([]byte, int) {
	var text strings.Builder
	for y := range height {
		var magnitude [2]uint32
		var bias [2]int32
		var small [2]int
		for x := range width {
			c := x & 1
			delta := int32(pixels[y*width+x]) - legacyPredictor(pixels, width, x, y)
			diff, low := delta>>2, uint32(delta&3)
			value := diff - bias[c]
			sign := uint32(0)
			if value < 0 {
				sign, value = 4, -value-1
			}
			aux := 0
			if small[c] < 3 {
				aux = 2
			}
			n := max(2+aux, bits.Len16(uint16(magnitude[c]))-aux)
			magnitude[c] = uint32(value)
			high, suffix := uint32(value)>>n, uint32(value)&((1<<n)-1)
			fmt.Fprintf(&text, "%03b", sign|low)
			if high < 12 {
				text.WriteString(strings.Repeat("0", int(high)) + "1")
			} else {
				text.WriteString("000000000000")
				fmt.Fprintf(&text, "%0*b", 16-n, high*2+1)
			}
			fmt.Fprintf(&text, "%0*b", n, suffix)
			bias[c] = (3*diff + bias[c]) >> 5
			if value > 16 {
				small[c] = 0
			} else {
				small[c]++
			}
		}
	}
	return bitString(text.String()), text.Len()
}

func legacyORF(width, height int, payload []byte) []byte {
	data := makeTestORF(true)
	for tag, value := range map[uint16]int{0x100: width, 0x101: height, 0x116: height, 0x117: len(payload) + 7} {
		binary.LittleEndian.PutUint32(recordAt(data, 8, tag)[8:], uint32(value))
	}
	for tag, value := range map[uint16]int{0x0612: 0, 0x0613: 0, 0x0614: width, 0x0615: height} {
		r := recordAt(data, 1800, tag)
		if tag < 0x0614 {
			binary.LittleEndian.PutUint16(r[8:], uint16(value))
		} else {
			binary.LittleEndian.PutUint32(r[8:], uint32(value))
		}
	}
	data = append(data[:8192:8192], 0, 0, 0, 0, 1, 0, 0)
	return append(data, payload...)
}

func legacyPlane(width, height int) []uint16 {
	pixels := make([]uint16, width*height)
	for y := range height {
		for x := range width {
			pixels[y*width+x] = uint16(64 + (x&1)*128 + (y&1)*256 + x/2*8 + y/2*4 + (x+y)%4)
		}
	}
	return pixels
}

func TestLegacyCompletePlanesAndTrailingPolicy(t *testing.T) {
	for _, shape := range [][2]int{{2, 1}, {2, 2}, {4, 3}, {8, 7}, {16, 8}, {32, 31}} {
		want := legacyPlane(shape[0], shape[1])
		payload, consumed := encodeLegacy(want, shape[0], shape[1])
		for _, tail := range [][]byte{nil, {0, 0xff, 0xab}} {
			data := legacyORF(shape[0], shape[1], append(append([]byte(nil), payload...), tail...))
			file := openTest(t, data, OpenOptions{})
			before := file.Metadata()
			raw, err := file.DecodeRaw(context.Background())
			if err != nil || !reflect.DeepEqual(raw.Pixels, want) ||
				!file.Capabilities().Raw.Available || raw.Width != shape[0] || raw.Height != shape[1] ||
				raw.Stride != shape[0] || raw.CFA != before.CFA.Value ||
				raw.ActiveArea != before.Geometry.ActiveArea.Value || raw.BitDepth != 12 ||
				raw.Calibration != before.Calibration || raw.Orientation != before.Orientation.Value ||
				!reflect.DeepEqual(before, file.Metadata()) {
				t.Fatalf("shape=%v raw=%+v: %v", shape, raw, err)
			}
			b := file.legacyReader(make([]byte, 1))
			if err := decodeLegacyRows(context.Background(), &b, raw.Pixels, raw.Width, raw.Width, raw.Height); err != nil || b.consumed != uint64(consumed) {
				t.Fatalf("stream consumption: %d want=%d: %v", b.consumed, consumed, err)
			}
		}
	}
	for _, sample := range []uint16{0, 4095} {
		want := []uint16{sample, sample}
		payload, _ := encodeLegacy(want, 2, 1)
		raw, err := openTest(t, legacyORF(2, 1, payload), OpenOptions{}).DecodeRaw(context.Background())
		if err != nil || !reflect.DeepEqual(raw.Pixels, want) {
			t.Fatalf("endpoint %d: %+v %v", sample, raw, err)
		}
	}
}

func TestLegacyCodedColumnsBeyondOutput(t *testing.T) {
	want := legacyPlane(4, 5)
	payload, consumed := encodeLegacy(want, 4, 5)
	out := make([]uint16, 2*5)
	b := testLegacyBits(t, payload, 1)
	if err := decodeLegacyRows(context.Background(), b, out, 2, 4, 5); err != nil {
		t.Fatal(err)
	}
	for y := range 5 {
		if !reflect.DeepEqual(out[y*2:y*2+2], want[y*4:y*4+2]) {
			t.Fatal("coded columns shifted output or row state")
		}
	}
	if b.consumed != uint64(consumed) {
		t.Fatal("discarded coded columns were not consumed")
	}
}

func TestLegacyUnusedLastByteBits(t *testing.T) {
	want := []uint16{100, 100}
	payload, consumed := encodeLegacy(want, 2, 1)
	unused := (8 - consumed%8) % 8
	if unused == 0 {
		t.Fatal("test requires a partially used final byte")
	}
	mask := byte(1<<unused - 1)
	for value := 0; value <= int(mask); value++ {
		candidate := append([]byte(nil), payload...)
		candidate[len(candidate)-1] = candidate[len(candidate)-1]&^mask | byte(value)
		file := openTest(t, legacyORF(2, 1, candidate), OpenOptions{})
		raw, err := file.DecodeRaw(context.Background())
		if err != nil || !reflect.DeepEqual(raw.Pixels, want) {
			t.Fatalf("unused-bit pattern %x affects samples: %v", value, err)
		}
	}
}

func TestLegacyRejectsIncompleteFieldsAndInvalidSamples(t *testing.T) {
	for _, test := range []struct {
		text   string
		offset int64
		bit    uint8
	}{
		{"100" + "1" + "0000", 8199, 0},
		{"000" + "000000000000" + "000010000001" + "0000", 8199, 0},
		{"000" + "01" + "0000" + "100" + "1" + "0000", 8200, 1},
	} {
		raw, err := openTest(t, legacyORF(2, 1, bitString(test.text)), OpenOptions{}).DecodeRaw(context.Background())
		var location *Error
		if raw != nil || !errors.Is(err, ErrCorrupt) || !errors.As(err, &location) ||
			location.Layout != "olympus-legacy-predictive12" ||
			location.Offset == nil || *location.Offset != test.offset || location.Bit == nil || *location.Bit != test.bit {
			t.Fatalf("out-of-range sample lacks classification/location: %+v %v", raw, err)
		}
	}
	for _, text := range []string{
		"", "0", "000000000000000", "00000000000000000000000",
		"00000000000000000000000000000000",
		"10010000",                         // negative first sample
		"00000000000000111111111111111111", // oversized first sample
	} {
		data := legacyORF(8, 2, bitString(text))
		file := openTest(t, data, OpenOptions{})
		raw, err := file.DecodeRaw(context.Background())
		if raw != nil || (!errors.Is(err, ErrCorrupt) && !errors.Is(err, ErrTruncated)) {
			t.Fatalf("malformed %q: %+v %v", text, raw, err)
		}
	}
	want := legacyPlane(16, 8)
	payload, _ := encodeLegacy(want, 16, 8)
	for length := 0; length < len(payload); length++ {
		data := legacyORF(16, 8, payload)
		binary.LittleEndian.PutUint32(recordAt(data, 8, 0x117)[8:], uint32(7+length))
		file := openTest(t, data, OpenOptions{})
		if raw, err := file.DecodeRaw(context.Background()); raw != nil || !errors.Is(err, ErrTruncated) {
			t.Fatalf("truncation %d/%d escaped declared extent: %+v %v", length, len(payload), raw, err)
		}
	}
}

func TestLegacyWorkingQuotaOwnershipCancellationAndIO(t *testing.T) {
	want := legacyPlane(16, 8)
	payload, _ := encodeLegacy(want, 16, 8)
	data := legacyORF(16, 8, payload)
	file := openTest(t, data, OpenOptions{})
	needed := file.plan.outputBytes + legacyBufferBytes + uint64(len(file.metadata.Diagnostics))*64
	for _, delta := range []int64{-1, 0, 1} {
		limited := *file
		limited.limits.WorkingBytes = int64(needed) + delta
		raw, err := limited.DecodeRaw(context.Background())
		if delta < 0 {
			if raw != nil || !errors.Is(err, ErrLimitExceeded) {
				t.Fatal("legacy working quota")
			}
		} else if err != nil || !reflect.DeepEqual(raw.Pixels, want) {
			t.Fatal("exact legacy working quota")
		}
	}
	for _, mode := range []string{"disk", "short", "short nil", "full error", "joined EOF", "cancel refill", "cancel before", "deadline"} {
		t.Run(mode, func(t *testing.T) {
			reader := &rawReader{input: bytes.NewReader(data)}
			f, err := Open(context.Background(), reader, int64(len(data)), OpenOptions{})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			disk := errors.New("legacy disk failure")
			expected := disk
			switch mode {
			case "disk":
				reader.err = disk
			case "short":
				reader.input, expected = bytes.NewReader(data[:len(data)-1]), ErrTruncated
			case "short nil":
				reader.shortNil, expected = true, ErrTruncated
			case "full error":
				reader.fullErr, reader.err = true, disk
			case "joined EOF":
				reader.fullErr, reader.err = true, errors.Join(io.EOF, disk)
			case "cancel refill":
				reader.calls = 0
				reader.cancel, reader.cancelCall, expected = cancel, 1, context.Canceled
			case "cancel before":
				cancel()
				expected = context.Canceled
			case "deadline":
				var stop context.CancelFunc
				ctx, stop = context.WithDeadline(context.Background(), time.Unix(1, 0))
				defer stop()
				expected = context.DeadlineExceeded
			}
			raw, err := f.DecodeRaw(ctx)
			if raw != nil || !errors.Is(err, expected) {
				t.Fatalf("IO/cancellation: %+v %v, want %v", raw, err, expected)
			}
		})
	}
	raw, err := file.DecodeRaw(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	raw.Pixels[0] = 4095
	raw.Diagnostics[0].Message = "caller-owned"
	raw.Calibration.Black.Value[0][0] = 0
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 5 {
				raw, err := file.DecodeRaw(context.Background())
				if err != nil || !reflect.DeepEqual(raw.Pixels, want) ||
					raw.Diagnostics[0].Message == "caller-owned" || raw.Calibration != file.Metadata().Calibration {
					t.Errorf("shared legacy state: %v", err)
					return
				}
				raw.Pixels[0]++
			}
		}()
	}
	wg.Wait()
	for tag := uint16(0x0612); tag <= 0x0615; tag++ {
		binary.LittleEndian.PutUint16(recordAt(data, 1800, tag), tag+0x100)
	}
	for _, tag := range []uint16{0x0100, 0x0600} {
		binary.LittleEndian.PutUint16(recordAt(data, 1800, tag), tag+0x1000)
	}
	raw, err = openTest(t, data, OpenOptions{}).DecodeRaw(context.Background())
	if err != nil || !reflect.DeepEqual(raw.Pixels, want) || !raw.ActiveArea.Empty() ||
		raw.Diagnostics[len(raw.Diagnostics)-1].Code != "active-area-unavailable" ||
		raw.Calibration.Black.Present() || raw.Calibration.WhiteBalance.Present() {
		t.Fatalf("missing calibration/crop prevented or changed raw decoding: %+v %v", raw, err)
	}
}

func TestLegacyPublicWorkingQuotaAndLazyOpen(t *testing.T) {
	payload, _ := encodeLegacy(legacyPlane(128, 256), 128, 256)
	data := legacyORF(128, 256, payload)
	reader := &rawReader{input: bytes.NewReader(data)}
	file, err := Open(context.Background(), reader, int64(len(data)), OpenOptions{})
	if err != nil || reader.calls != 1 {
		t.Fatalf("Open must read only the seven-byte raw prefix: %d reads, %v", reader.calls, err)
	}
	required := file.plan.outputBytes + file.plan.scratchBytes + uint64(len(file.metadata.Diagnostics))*64
	for _, delta := range []int64{-1, 0, 1} {
		opts := OpenOptions{Limits: Limits{MetadataBytes: 32768, PreviewBytes: 1, WorkingBytes: int64(required) + delta}}
		opts.Budget, _ = NewBudget(1 << 30)
		file, err := Open(context.Background(), reader, int64(len(data)), opts)
		if err != nil || file.Capabilities().Raw.Available != (delta >= 0) {
			t.Fatalf("public working-quota capability boundary: %v", err)
		}
		reader.calls = 0
		raw, err := file.DecodeRaw(context.Background())
		if delta < 0 {
			if raw != nil || !errors.Is(err, ErrLimitExceeded) || reader.calls != 0 || file.Capabilities().Raw.Reason == "" {
				t.Fatal("quota must be checked before raw read/allocation")
			}
		} else if err != nil || reader.calls < 2 {
			t.Fatalf("exact quota / repeated buffered reads: %v, %d", err, reader.calls)
		}
	}
}

func TestLegacyFinalRowCancellation(t *testing.T) {
	payload, _ := encodeLegacy(legacyPlane(2, 1), 2, 1)
	data := legacyORF(2, 1, payload)
	reader := &rawReader{input: bytes.NewReader(data)}
	file, err := Open(context.Background(), reader, int64(len(data)), OpenOptions{})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reader.calls = 0
	reader.cancel, reader.cancelCall = cancel, 1
	if raw, err := file.DecodeRaw(ctx); raw != nil || !errors.Is(err, context.Canceled) {
		t.Fatal("cancellation during the final row returned success")
	}
}

func TestLegacyAllocationsBoundedByBuffers(t *testing.T) {
	var allocations [2]float64
	for i, height := range []int{1, 31} {
		payload, _ := encodeLegacy(legacyPlane(32, height), 32, height)
		file := openTest(t, legacyORF(32, height, payload), OpenOptions{})
		allocations[i] = testing.AllocsPerRun(20, func() {
			if _, err := file.DecodeRaw(context.Background()); err != nil {
				t.Fatal(err)
			}
		})
	}
	if allocations[1] > allocations[0] {
		t.Fatalf("legacy allocation grows with sample/row count: %v", allocations)
	}
}

func FuzzDecodeLegacy(f *testing.F) {
	valid, _ := encodeLegacy(legacyPlane(16, 8), 16, 8)
	f.Add(uint8(7), uint8(7), valid)
	f.Add(uint8(0), uint8(0), []byte{})
	f.Add(uint8(2), uint8(4), []byte{0xff, 0, 0xff})
	f.Fuzz(func(t *testing.T, w, h uint8, payload []byte) {
		if len(payload) > 4096 {
			return
		}

		width, height := 2+2*(int(w)%16), 1+int(h)%32
		data := legacyORF(width, height, payload)
		file := openTest(t, data, OpenOptions{Limits: Limits{MetadataBytes: 32768, PreviewBytes: 1, Pixels: 1024, Dimension: 64, WorkingBytes: 65536}})
		raw, err := file.DecodeRaw(context.Background())
		if err != nil {
			if raw != nil || (!errors.Is(err, ErrCorrupt) && !errors.Is(err, ErrTruncated) && !errors.Is(err, ErrUnsupported)) {
				t.Fatalf("invalid legacy failure: %+v %v", raw, err)
			}
			return
		}
		if raw.Stride != width || raw.Width != width || raw.Height != height || len(raw.Pixels) != width*height || !file.Capabilities().Raw.Available {
			t.Fatal("invalid legacy output shape")
		}
		for _, sample := range raw.Pixels {
			if sample > 4095 {
				t.Fatal("sample outside sensor domain")
			}
		}
	})
}

func FuzzLegacyRoundTrip(f *testing.F) {
	f.Add(uint8(7), uint8(7), []byte{0, 1, 255, 254, 0, 255, 127})
	f.Add(uint8(0), uint8(0), []byte{255})
	f.Fuzz(func(t *testing.T, w, h uint8, data []byte) {
		if len(data) == 0 || len(data) > 4096 {
			return
		}
		width, height := 2+2*(int(w)%8), 1+int(h)%16
		want := make([]uint16, width*height)
		for i := range want {
			want[i] = uint16(data[i%len(data)])*16 + uint16(i%16)
		}
		payload, consumed := encodeLegacy(want, width, height)
		b := testLegacyBits(t, payload, 7)
		got := make([]uint16, len(want))
		if err := decodeLegacyRows(context.Background(), b, got, width, width, height); err != nil ||
			!reflect.DeepEqual(got, want) || b.consumed != uint64(consumed) {
			t.Fatalf("round-trip values or bit consumption: %v", err)
		}
	})
}
