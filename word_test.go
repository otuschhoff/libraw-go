// SPDX-License-Identifier: LGPL-2.1-only

package libraw

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"io"
	"reflect"
	"sync"
	"testing"
	"time"
	"unsafe"

	"github.com/otuschhoff/libraw-go/internal/tiff"
)

func wordORF(width, height, rows int) ([]byte, []uint16) {
	data := makeTestORF(false)
	if required := 8192 + width*height*2; required > len(data) {
		grown := make([]byte, required)
		copy(grown, data)
		data = grown
	}
	for tag, value := range map[uint16]int{0x100: width, 0x101: height, 0x116: rows} {
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
	var offsets, counts []uint32
	for y := 0; y < height; y += rows {
		offsets = append(offsets, uint32(8192+y*width*2))
		counts = append(counts, uint32(min(rows, height-y)*width*2))
	}
	b := orfBuilder{data: data, free: 5500}
	for _, entry := range []testEntry{uintEntry(0x111, tiff.Long, offsets...), uintEntry(0x117, tiff.Long, counts...)} {
		b.directory(6000, 0, []testEntry{entry}, 0, false)
		copy(recordAt(data, 8, entry.tag), data[6002:6014])
	}
	words := []uint16{0, 0x0010, 0x123f, 0xfff0, 0xffff, 0xabcd, 0x0102}
	samples := []uint16{0, 1, 291, 4095, 4095, 2748, 16}
	want := make([]uint16, width*height)
	for i := range want {
		binary.LittleEndian.PutUint16(data[8192+i*2:], words[i%len(words)])
		want[i] = samples[i%len(samples)]
	}
	return data[:8192+width*height*2], want
}

func TestWordVectorsGeometryAndStripTransitions(t *testing.T) {
	for _, shape := range [][3]int{{1, 1, 1}, {5, 3, 2}, {7, 7, 3}, {5, 146, 130}, {5, 88, 32}, {16, 8, 100}} {
		t.Run(fmt.Sprint(shape), func(t *testing.T) {
			data, want := wordORF(shape[0], shape[1], shape[2])
			file := openTest(t, data, OpenOptions{})
			if !file.Capabilities().Raw.Available || file.Capabilities().Raw.Reason != "" {
				t.Fatal("working word decoder unavailable")
			}
			raw, err := file.DecodeRaw(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(raw.Pixels, want) || raw.Width != shape[0] || raw.Height != shape[1] ||
				raw.Stride != shape[0] || raw.ActiveArea != image.Rect(0, 0, shape[0], shape[1]) ||
				raw.CFA != (CFA{{Green1, Red}, {Blue, Green2}}) || raw.BitDepth != 12 ||
				raw.Orientation != OrientationRightTop || raw.Calibration != file.Metadata().Calibration {
				t.Fatalf("sensor values/geometry changed: %+v", raw)
			}
			if raw.Calibration.White.Present() || raw.Calibration.Color.Present() {
				t.Fatal("missing calibration guessed")
			}
		})
	}
}

func TestWordOwnershipConcurrencyAndUnknownCrop(t *testing.T) {
	data, want := wordORF(5, 7, 3)
	file := openTest(t, data, OpenOptions{})
	first, err := file.DecodeRaw(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	first.Pixels[0] = 1234
	first.Diagnostics[0].Message = "caller-owned"
	first.Calibration.Black.Value[0][0] = 0
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 5 {
				raw, err := file.DecodeRaw(context.Background())
				if err != nil || !reflect.DeepEqual(raw.Pixels, want) ||
					raw.Diagnostics[0].Message == "caller-owned" || raw.Calibration != file.Metadata().Calibration {
					t.Errorf("shared decode state: %+v, %v", raw, err)
					return
				}
				raw.Pixels[0]++
				raw.Diagnostics[0].Message = "private"
			}
		}()
	}
	wg.Wait()
	for tag := uint16(0x0612); tag <= 0x0615; tag++ {
		binary.LittleEndian.PutUint16(recordAt(data, 1800, tag), tag+0x100)
	}
	file = openTest(t, data, OpenOptions{})
	raw, err := file.DecodeRaw(context.Background())
	if err != nil || !raw.ActiveArea.Empty() || !reflect.DeepEqual(raw.Pixels, want) ||
		raw.Diagnostics[len(raw.Diagnostics)-1].Code != "active-area-unavailable" {
		t.Fatalf("unknown crop guessed or pixels lost: %+v, %v", raw, err)
	}
	for _, tag := range []uint16{0x0100, 0x0600} {
		binary.LittleEndian.PutUint16(recordAt(data, 1800, tag), tag+0x1000)
	}
	file = openTest(t, data, OpenOptions{})
	raw, err = file.DecodeRaw(context.Background())
	if err != nil || !reflect.DeepEqual(raw.Pixels, want) || raw.Calibration.Black.Present() ||
		raw.Calibration.WhiteBalance.Present() || raw.Calibration.White.Present() || raw.Calibration.Color.Present() {
		t.Fatalf("missing calibration prevented or altered raw extraction: %+v, %v", raw, err)
	}
}

func TestWordWorkingQuotaAndNoPayloadReadOnOpen(t *testing.T) {
	data, _ := wordORF(16, 8, 3)
	reader := &rawReader{input: bytes.NewReader(data)}
	file, err := Open(context.Background(), reader, int64(len(data)), OpenOptions{})
	if err != nil || reader.calls != 0 {
		t.Fatalf("Open decoded/buffered raw: %v, %d reads", err, reader.calls)
	}
	needed := file.plan.outputBytes + file.plan.scratchBytes + uint64(len(file.metadata.Diagnostics))*64
	for _, delta := range []int64{-1, 0, 1} {
		file.limits.WorkingBytes = int64(needed) + delta
		reader.calls = 0
		raw, err := file.DecodeRaw(context.Background())
		if delta < 0 {
			if raw != nil || !errors.Is(err, ErrLimitExceeded) || reader.calls != 0 {
				t.Fatalf("quota not checked before allocation/read: %+v, %v", raw, err)
			}
		} else if err != nil || reader.calls != 8 {
			t.Fatalf("exact quota/one row buffer failed: %v, %d reads", err, reader.calls)
		}
	}
	if unsafe.Sizeof(Diagnostic{}) > 64 {
		t.Fatal("diagnostic working allowance must cover its representation")
	}
	// Exercise the public capability gate without mutating an existing File.
	opts := OpenOptions{Limits: Limits{MetadataBytes: 32768, PreviewBytes: 1, WorkingBytes: 32768}}
	// Increase just the stored geometry/output to exceed working bytes while
	// keeping metadata parsable and the input physically present.
	large := make([]byte, 8192+128*256*2)
	copy(large, makeTestORF(false))
	for tag, value := range map[uint16]uint32{0x100: 128, 0x101: 256, 0x116: 256, 0x117: 128 * 256 * 2} {
		binary.LittleEndian.PutUint32(recordAt(large, 8, tag)[8:], value)
	}
	limited := openTest(t, large, opts)
	if limited.Capabilities().Raw.Available || limited.Capabilities().Raw.Reason == "" {
		t.Fatal("over-budget decode advertised")
	}
	if raw, err := limited.DecodeRaw(context.Background()); raw != nil || !errors.Is(err, ErrLimitExceeded) {
		t.Fatalf("over-budget decode result: %+v, %v", raw, err)
	}
	largeNeeded := limited.plan.outputBytes + limited.plan.scratchBytes + uint64(len(limited.metadata.Diagnostics))*64
	for _, delta := range []int64{-1, 0, 1} {
		opts.Budget, _ = NewBudget(1 << 30)
		opts.Limits.WorkingBytes = int64(largeNeeded) + delta
		f := openTest(t, large, opts)
		if f.Capabilities().Raw.Available != (delta >= 0) {
			t.Fatal("public exact working-quota capability boundary")
		}
		raw, err := f.DecodeRaw(context.Background())
		if delta < 0 {
			if raw != nil || !errors.Is(err, ErrLimitExceeded) {
				t.Fatal("public working-quota failure")
			}
		} else if err != nil {
			t.Fatal(err)
		}
	}
}

func TestWordIOFailureAndCancellation(t *testing.T) {
	data, _ := wordORF(5, 7, 3)
	diskErr := errors.New("raw disk failure")
	for _, mode := range []string{"disk", "short", "short nil", "full error", "joined EOF", "cancel first", "cancel last", "deadline"} {
		t.Run(mode, func(t *testing.T) {
			reader := &rawReader{input: bytes.NewReader(data)}
			file, err := Open(context.Background(), reader, int64(len(data)), OpenOptions{})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			want := diskErr
			switch mode {
			case "disk":
				reader.err = diskErr
			case "short":
				reader.input = bytes.NewReader(data[:len(data)-1])
				want = ErrTruncated
			case "short nil":
				reader.shortNil = true
				want = ErrTruncated
			case "full error":
				reader.fullErr, reader.err = true, diskErr
			case "joined EOF":
				reader.fullErr, reader.err = true, errors.Join(io.EOF, diskErr)
			case "cancel first":
				reader.cancel, reader.cancelCall, want = cancel, 1, context.Canceled
			case "cancel last":
				reader.cancel, reader.cancelCall, want = cancel, 7, context.Canceled
			case "deadline":
				var stop context.CancelFunc
				ctx, stop = context.WithDeadline(context.Background(), time.Unix(1, 0))
				defer stop()
				want = context.DeadlineExceeded
			}
			raw, err := file.DecodeRaw(ctx)
			if raw != nil || !errors.Is(err, want) {
				t.Fatalf("failure yielded image or lost cause: %+v, %v", raw, err)
			}
			var detail *Error
			if !errors.As(err, &detail) {
				t.Fatal("error lacks operation context")
			}
			if (mode == "deadline" && reader.calls != 0) || (mode == "cancel first" && reader.calls != 1) ||
				(mode == "cancel last" && reader.calls != 7) {
				t.Fatal("cancellation polling did not bound raw reads")
			}
		})
	}
	file := openTest(t, data, OpenOptions{})
	if _, err := file.DecodeRaw(nil); !errors.Is(err, ErrInvalidOptions) {
		t.Fatalf("nil decode context: %v", err)
	}
	var unopened *File
	if _, err := unopened.DecodeRaw(context.Background()); !errors.Is(err, ErrInvalidOptions) {
		t.Fatalf("nil file: %v", err)
	}
	reader := &rawReader{input: bytes.NewReader(data), fullErr: true, err: io.EOF}
	file, err := Open(context.Background(), reader, int64(len(data)), OpenOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if raw, err := file.DecodeRaw(context.Background()); err != nil || len(raw.Pixels) != 35 {
		t.Fatalf("literal EOF on full raw reads was not accepted: %v", err)
	}
}

type rawReader struct {
	input      io.ReaderAt
	calls      int
	err        error
	fullErr    bool
	shortNil   bool
	cancel     context.CancelFunc
	cancelCall int
}

func (r *rawReader) ReadAt(data []byte, at int64) (int, error) {
	if at < 8192 {
		return r.input.ReadAt(data, at)
	}
	r.calls++
	if r.cancel != nil && r.calls == r.cancelCall {
		r.cancel()
	}
	if r.shortNil {
		return len(data) - 1, nil
	}
	if r.err != nil && !r.fullErr {
		return 0, r.err
	}
	n, err := r.input.ReadAt(data, at)
	if r.fullErr {
		return n, r.err
	}
	return n, err
}

func TestWordRejectsUnevidencedEncoding(t *testing.T) {
	for _, control := range []struct {
		tag uint16
		typ tiff.Type
	}{{0x10a, tiff.Short}, {0x11c, tiff.Short}, {0x13d, tiff.Short}, {0x153, tiff.Short}, {0x80e5, tiff.Long}} {
		for _, value := range []uint32{1, 2, 3} {
			data, _ := wordORF(5, 7, 3)
			b := orfBuilder{data: data, free: 5800}
			b.directory(6000, 0, []testEntry{uintEntry(control.tag, control.typ, value)}, 0, false)
			copy(data[178:190], data[6002:6014])
			clear(data[190:194])
			binary.LittleEndian.PutUint16(data[8:], 15)
			if value == 3 {
				binary.LittleEndian.PutUint16(data[180:], uint16(tiff.Byte))
			}
			file, err := Open(context.Background(), bytes.NewReader(data), int64(len(data)), OpenOptions{})
			if value == 3 {
				if file != nil || !errors.Is(err, ErrCorrupt) {
					t.Fatalf("malformed encoding control %04x accepted: %v", control.tag, err)
				}
				continue
			}
			if err != nil {
				t.Fatal(err)
			}
			if file.Capabilities().Raw.Available != (value == 1) {
				t.Fatalf("encoding control %04x=%d not enforced", control.tag, value)
			}
			raw, err := file.DecodeRaw(context.Background())
			if value == 2 && (raw != nil || !errors.Is(err, ErrUnsupported)) {
				t.Fatalf("encoding control %04x decoded as words: %v", control.tag, err)
			}
			if value == 1 && err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, mode := range []string{"padded", "odd byte", "big endian", "wrong depth", "wrong make", "unknown model"} {
		t.Run(mode, func(t *testing.T) {
			data, _ := wordORF(5, 7, 7)
			switch mode {
			case "padded":
				data = append(data, 0, 0)
				binary.LittleEndian.PutUint32(recordAt(data, 8, 0x117)[8:], 72)
			case "odd byte":
				binary.LittleEndian.PutUint32(recordAt(data, 8, 0x117)[8:], 69)
			case "big endian":
				copy(data, "MMOR")
			case "wrong depth":
				binary.LittleEndian.PutUint16(recordAt(data, 8, 0x102)[8:], 12)
			case "wrong make":
				at := binary.LittleEndian.Uint32(recordAt(data, 8, 0x10f)[8:])
				data[at] = 'X'
			case "unknown model":
				at := binary.LittleEndian.Uint32(recordAt(data, 8, 0x110)[8:])
				data[at] = 'X'
			}
			file, err := Open(context.Background(), bytes.NewReader(data), int64(len(data)), OpenOptions{})
			if mode == "big endian" {
				if file != nil || !errors.Is(err, ErrUnsupported) {
					t.Fatal("unevidenced byte order accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if file.Capabilities().Raw.Available {
				t.Fatal("unevidenced word layout advertised")
			}
			if raw, err := file.DecodeRaw(context.Background()); raw != nil || !errors.Is(err, ErrUnsupported) {
				t.Fatalf("unevidenced layout decoded: %v", err)
			}
		})
	}
}

func TestWordTruncatedPayloadAndStripArrays(t *testing.T) {
	for _, mode := range []string{"payload", "offset array", "count array"} {
		t.Run(mode, func(t *testing.T) {
			data, _ := wordORF(5, 7, 3)
			switch mode {
			case "payload":
				data = data[:len(data)-1]
			case "offset array":
				binary.LittleEndian.PutUint32(recordAt(data, 8, 0x111)[8:], uint32(len(data)-1))
			case "count array":
				binary.LittleEndian.PutUint32(recordAt(data, 8, 0x117)[8:], uint32(len(data)-1))
			}
			if file, err := Open(context.Background(), bytes.NewReader(data), int64(len(data)), OpenOptions{}); file != nil || !errors.Is(err, ErrTruncated) {
				t.Fatalf("truncated word layout returned a File: %v", err)
			}
		})
	}
}

func BenchmarkDecodeWord(b *testing.B) {
	data, _ := wordORF(31, 31, 7)
	file, err := Open(context.Background(), bytes.NewReader(data), int64(len(data)), OpenOptions{})
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.SetBytes(31 * 31 * 2)
	b.ResetTimer()
	for b.Loop() {
		result, err := file.DecodeRaw(context.Background())
		if err != nil {
			b.Fatal(err)
		}
		result.Close()
	}
}

func TestWordAllocationsDoNotGrowWithRowCount(t *testing.T) {
	var allocations [2]float64
	for i, height := range []int{1, 31} {
		data, _ := wordORF(31, height, 7)
		file := openTest(t, data, OpenOptions{})
		allocations[i] = testing.AllocsPerRun(20, func() {
			if _, err := file.DecodeRaw(context.Background()); err != nil {
				t.Fatal(err)
			}
		})
	}
	if allocations[1] > allocations[0] {
		t.Fatalf("raw decode allocates per row: one=%g many=%g", allocations[0], allocations[1])
	}
}

func FuzzDecodeWord(f *testing.F) {
	f.Add(uint8(4), uint8(6), uint8(2), []byte{0, 0, 0x10, 0, 0x3f, 0x12, 0xff, 0xff})
	f.Add(uint8(0), uint8(0), uint8(0), []byte{})
	f.Add(uint8(30), uint8(30), uint8(30), []byte{0xff, 0, 0, 0xff})
	f.Fuzz(func(t *testing.T, w, h, r uint8, payload []byte) {
		if len(payload) > 4096 {
			return
		}
		width, height := 1+int(w)%31, 1+int(h)%31
		data, _ := wordORF(width, height, 1+int(r)%height)
		if len(payload) == 0 {
			payload = []byte{0}
		}
		for i := 8192; i < len(data); i++ {
			data[i] = payload[(i-8192)%len(payload)]
		}
		file := openTest(t, data, OpenOptions{Limits: Limits{MetadataBytes: 32768, PreviewBytes: 1, Pixels: 1024, Dimension: 64, WorkingBytes: 65536}})
		raw, err := file.DecodeRaw(context.Background())
		if err != nil || !file.Capabilities().Raw.Available {
			t.Fatalf("valid bounded word payload failed: %v", err)
		}
		for i, value := range raw.Pixels {
			at := 8192 + i*2
			want := (uint16(data[at+1])*256 + uint16(data[at])) / 16
			if value != want {
				t.Fatalf("word conversion mismatch sample=%d got=%d want=%d", i, value, want)
			}
		}
	})
}
