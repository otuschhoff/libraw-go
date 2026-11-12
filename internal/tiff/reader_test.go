// SPDX-License-Identifier: LGPL-2.1-only
package tiff

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"math"
	"reflect"
	"strconv"
	"testing"

	"github.com/otuschhoff/libraw-go/internal/bounded"
	"github.com/otuschhoff/libraw-go/internal/fault"
)

func parser(t *testing.T, data []byte, order binary.ByteOrder, entries, budget uint64) *Reader {
	t.Helper()
	source, err := bounded.New(bytes.NewReader(data), int64(len(data)), 1<<30)
	if err != nil {
		t.Fatal(err)
	}
	reader, err := New(source, order, entries, budget)
	if err != nil {
		t.Fatal(err)
	}
	return reader
}

func oneIFD(order binary.ByteOrder, typ Type, count uint32, value []byte, base int) []byte {
	data := make([]byte, base+128+len(value))
	order.PutUint16(data[base+8:], 1)
	order.PutUint16(data[base+10:], 0x1234)
	order.PutUint16(data[base+12:], uint16(typ))
	order.PutUint32(data[base+14:], count)
	if len(value) <= 4 {
		copy(data[base+18:base+22], value)
	} else {
		order.PutUint32(data[base+18:], 128)
		copy(data[base+128:], value)
	}
	order.PutUint32(data[base+22:], 64)
	return data
}

func TestByteOrdersInlineAndOffsetBases(t *testing.T) {
	for _, order := range []binary.ByteOrder{binary.LittleEndian, binary.BigEndian} {
		t.Run(order.String(), func(t *testing.T) {
			for _, base := range []int{0, 40} {
				for _, count := range []uint32{1, 2, 3} {
					values := make([]byte, count*2)
					for i := uint32(0); i < count; i++ {
						order.PutUint16(values[i*2:], uint16(100+i))
					}
					reader := parser(t, oneIFD(order, Short, count, values, base), order, 16, 4096)
					ifd, err := reader.ReadIFD(int64(base+8), int64(base))
					if err != nil {
						t.Fatal(err)
					}
					if ifd.Base != int64(base) || ifd.Offset != int64(base+8) || ifd.Next != 64 || len(ifd.Entries) != 1 {
						t.Fatalf("IFD framing: %+v", ifd)
					}
					e := ifd.Entries[0]
					wantOffset := int64(base + 18)
					if count > 2 {
						wantOffset = int64(base + 128)
					}
					if e.Tag != 0x1234 || e.Offset != wantOffset {
						t.Fatalf("entry context: %+v", e)
					}
					for i := uint32(0); i < count; i++ {
						if got, err := e.Uint(i); err != nil || got != uint64(100+i) {
							t.Fatalf("unsigned value %d: %d, %v", i, got, err)
						}
					}
					if _, err := e.Uint(count); !errors.Is(err, fault.Corrupt) {
						t.Fatalf("index beyond count accepted: %v", err)
					}
				}
			}
		})
	}
}

func TestRationalArrayAndIndependentValues(t *testing.T) {
	order := binary.BigEndian
	values := make([]byte, 16)
	order.PutUint32(values[0:], 1)
	order.PutUint32(values[4:], 2)
	order.PutUint32(values[8:], 3)
	order.PutUint32(values[12:], 4)
	data := oneIFD(order, Rational, 2, values, 0)
	p := parser(t, data, order, 10, 4096)
	first, err := p.ReadIFD(8, 0)
	if err != nil {
		t.Fatal(err)
	}
	second, err := p.ReadIFD(8, 0)
	if err != nil {
		t.Fatal(err)
	}
	for i, want := range []float64{.5, .75} {
		got, err := first.Entries[0].Real(uint32(i))
		if err != nil || got != want {
			t.Fatalf("rational array[%d]: %g, %v", i, got, err)
		}
	}
	first.Entries[0].Data[0] = 100
	if !bytes.Equal(second.Entries[0].Data, values) || !bytes.Equal(data[128:], values) {
		t.Fatal("returned value aliases another IFD or borrowed source")
	}
}

func TestMultipleRecordsAndErrorLocations(t *testing.T) {
	order := binary.LittleEndian
	data := make([]byte, 128)
	order.PutUint16(data[8:], 2)
	for i, tag := range []uint16{0x200, 0x100} {
		record := data[10+i*12 : 22+i*12]
		order.PutUint16(record, tag)
		order.PutUint16(record[2:], uint16(Long))
		order.PutUint32(record[4:], 1)
		order.PutUint32(record[8:], uint32(10+i))
	}
	p := parser(t, data, order, 2, 4096)
	ifd, err := p.ReadIFD(8, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(ifd.Entries) != 2 || ifd.Entries[0].Tag != 0x200 || ifd.Entries[1].Tag != 0x100 {
		t.Fatalf("directory order changed: %+v", ifd)
	}
	for i, e := range ifd.Entries {
		got, err := e.Uint(0)
		if err != nil || got != uint64(10+i) {
			t.Fatalf("record %d: %d, %v", i, got, err)
		}
	}
	// Force the second record out-of-line and one byte outside the file.
	order.PutUint32(data[26:], 2)
	order.PutUint32(data[30:], 121)
	p = parser(t, data, order, 2, 4096)
	ifd, err = p.ReadIFD(8, 0)
	var typed *fault.Error
	if ifd != nil || !errors.Is(err, fault.Truncated) || !errors.As(err, &typed) ||
		typed.Tag == nil || *typed.Tag != 0x100 || typed.IFD == nil || *typed.IFD != 8 {
		t.Fatalf("tag/IFD failure context missing: %+v, %v", ifd, err)
	}
}

func TestNumericTypes(t *testing.T) {
	for _, order := range []binary.ByteOrder{binary.LittleEndian, binary.BigEndian} {
		for _, test := range []struct {
			typ   Type
			bits  uint64
			wantU uint64
			wantI int64
			wantR float64
		}{
			{typ: Byte, bits: 255, wantU: 255},
			{typ: Short, bits: 65535, wantU: 65535},
			{typ: Long, bits: math.MaxUint32, wantU: math.MaxUint32},
			{typ: IFDType, bits: 128, wantU: 128},
			{typ: SByte, bits: 0x80, wantI: -128},
			{typ: SShort, bits: 0x8000, wantI: -32768},
			{typ: SLong, bits: 0x80000000, wantI: -2147483648},
			{typ: Float, bits: uint64(math.Float32bits(1.25)), wantR: 1.25},
			{typ: Double, bits: math.Float64bits(-2.5), wantR: -2.5},
		} {
			size, err := test.typ.Size()
			if err != nil {
				t.Fatal(err)
			}
			value := make([]byte, size)
			switch size {
			case 1:
				value[0] = byte(test.bits)
			case 2:
				order.PutUint16(value, uint16(test.bits))
			case 4:
				order.PutUint32(value, uint32(test.bits))
			case 8:
				order.PutUint64(value, test.bits)
			}
			reader := parser(t, oneIFD(order, test.typ, 1, value, 0), order, 10, 4096)
			ifd, err := reader.ReadIFD(8, 0)
			if err != nil {
				t.Fatal(err)
			}
			e := ifd.Entries[0]
			switch test.typ {
			case Byte, Short, Long, IFDType:
				if got, err := e.Uint(0); err != nil || got != test.wantU {
					t.Fatalf("%v unsigned type %d: %d, %v", order, test.typ, got, err)
				}
				if _, err := e.Int(0); !errors.Is(err, fault.Corrupt) {
					t.Fatal("unsigned silently converted to signed")
				}
			case SByte, SShort, SLong:
				if got, err := e.Int(0); err != nil || got != test.wantI {
					t.Fatalf("%v signed type %d: %d, %v", order, test.typ, got, err)
				}
				if _, err := e.Uint(0); !errors.Is(err, fault.Corrupt) {
					t.Fatal("signed silently converted to unsigned")
				}
			default:
				if got, err := e.Real(0); err != nil || got != test.wantR {
					t.Fatalf("%v real type %d: %g, %v", order, test.typ, got, err)
				}
			}
		}
	}
}

func TestRationalsAndNonfinite(t *testing.T) {
	for _, order := range []binary.ByteOrder{binary.LittleEndian, binary.BigEndian} {
		for _, test := range []struct {
			typ  Type
			n, d uint32
			want float64
		}{
			{Rational, 1, 4, .25},
			{Rational, math.MaxUint32, 1, float64(math.MaxUint32)},
			{SRational, 0xfffffffd, 2, -1.5},
			{SRational, 3, 0xfffffffe, -1.5},
		} {
			data := make([]byte, 8)
			order.PutUint32(data, test.n)
			order.PutUint32(data[4:], test.d)
			p := parser(t, oneIFD(order, test.typ, 1, data, 0), order, 10, 4096)
			ifd, err := p.ReadIFD(8, 0)
			if err != nil {
				t.Fatal(err)
			}
			e := ifd.Entries[0]
			got, err := e.Real(0)
			if err != nil || got != test.want {
				t.Fatalf("rational = %g, %v; want %g", got, err, test.want)
			}
			n, d, err := e.Fraction(0)
			if err != nil || float64(n)/float64(d) != test.want {
				t.Fatalf("fraction %d/%d: %v", n, d, err)
			}
			for i := 4; i < 8; i++ {
				e.Data[i] = 0
			}
			if _, err := e.Real(0); !errors.Is(err, fault.Corrupt) {
				t.Fatalf("zero denominator: %v", err)
			}
		}
		for _, typ := range []Type{Float, Double} {
			for _, value := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
				data := make([]byte, 8)
				if typ == Float {
					order.PutUint32(data, math.Float32bits(float32(value)))
					data = data[:4]
				} else {
					order.PutUint64(data, math.Float64bits(value))
				}
				p := parser(t, oneIFD(order, typ, 1, data, 0), order, 10, 4096)
				ifd, err := p.ReadIFD(8, 0)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := ifd.Entries[0].Real(0); !errors.Is(err, fault.Corrupt) {
					t.Fatalf("nonfinite %g accepted: %v", value, err)
				}
			}
		}
	}
}

func TestASCIIAndUndefined(t *testing.T) {
	for _, value := range [][]byte{[]byte(" A \x00"), []byte("hello\x00\x00"), {0}} {
		p := parser(t, oneIFD(binary.LittleEndian, ASCII, uint32(len(value)), value, 0), binary.LittleEndian, 10, 4096)
		ifd, err := p.ReadIFD(8, 0)
		if err != nil {
			t.Fatal(err)
		}
		got, err := ifd.Entries[0].Text()
		if err != nil || got != string(bytes.TrimRight(value, "\x00")) {
			t.Fatalf("text %q: %q, %v", value, got, err)
		}
	}
	for _, value := range [][]byte{{'x'}, {0xff, 0}} {
		p := parser(t, oneIFD(binary.BigEndian, ASCII, uint32(len(value)), value, 0), binary.BigEndian, 10, 4096)
		ifd, err := p.ReadIFD(8, 0)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := ifd.Entries[0].Text(); !errors.Is(err, fault.Corrupt) {
			t.Fatalf("invalid text accepted: %v", err)
		}
	}
	p := parser(t, oneIFD(binary.LittleEndian, Undefined, 4, []byte{1, 2, 3, 4}, 0), binary.LittleEndian, 10, 4096)
	ifd, err := p.ReadIFD(8, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(ifd.Entries[0].Data, []byte{1, 2, 3, 4}) {
		t.Fatal("undefined bytes changed")
	}
	for _, access := range []func() error{
		func() error { _, err := ifd.Entries[0].Uint(0); return err },
		func() error { _, err := ifd.Entries[0].Int(0); return err },
		func() error { _, err := ifd.Entries[0].Real(0); return err },
		func() error { _, _, err := ifd.Entries[0].Fraction(0); return err },
		func() error { _, err := ifd.Entries[0].Text(); return err },
	} {
		if err := access(); !errors.Is(err, fault.Corrupt) {
			t.Fatalf("invalid type conversion accepted: %v", err)
		}
	}
}

func TestBudgetsAndMalformedIFDs(t *testing.T) {
	order := binary.LittleEndian
	good := oneIFD(order, Short, 1, []byte{1, 0}, 0)
	// 16 table bytes + 96 entry allowance + 96 IFD allowance + 2 value bytes.
	for _, budget := range []uint64{209, 210} {
		p := parser(t, good, order, 1, budget)
		ifd, err := p.ReadIFD(8, 0)
		if budget == 209 {
			if ifd != nil || !errors.Is(err, fault.Limit) {
				t.Fatalf("budget just below threshold: %+v, %v", ifd, err)
			}
		} else if err != nil || ifd == nil {
			t.Fatalf("exact budget rejected: %v", err)
		}
	}
	p := parser(t, good, order, 1, 419)
	if _, err := p.ReadIFD(8, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := p.ReadIFD(8, 0); !errors.Is(err, fault.Limit) {
		t.Fatalf("aggregate budget not enforced: %v", err)
	}
	for _, test := range []struct {
		name string
		edit func([]byte) []byte
		kind error
	}{
		{"truncated count", func(b []byte) []byte { return b[:9] }, fault.Truncated},
		{"truncated table", func(b []byte) []byte { return b[:24] }, fault.Truncated},
		{"too many entries", func(b []byte) []byte { order.PutUint16(b[8:], 2); return b }, fault.Limit},
		{"unknown type", func(b []byte) []byte { order.PutUint16(b[12:], 99); return b }, fault.Unsupported},
		{"zero count", func(b []byte) []byte { order.PutUint32(b[14:], 0); return b }, fault.Corrupt},
		{"huge count", func(b []byte) []byte { order.PutUint32(b[14:], math.MaxUint32); return b }, fault.Limit},
		{"outside value", func(b []byte) []byte {
			order.PutUint32(b[14:], 3)
			order.PutUint32(b[18:], math.MaxUint32)
			return b
		}, fault.Truncated},
	} {
		t.Run(test.name, func(t *testing.T) {
			data := test.edit(bytes.Clone(good))
			p := parser(t, data, order, 1, 4096)
			ifd, err := p.ReadIFD(8, 0)
			if ifd != nil || !errors.Is(err, test.kind) {
				t.Fatalf("malformed returned %+v, %v", ifd, err)
			}
		})
	}
}

func TestParserOptionsAndInvalidEntries(t *testing.T) {
	source, err := bounded.New(bytes.NewReader(nil), 0, 1)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		source         *bounded.Reader
		order          binary.ByteOrder
		entries, bytes uint64
	}{
		{nil, binary.LittleEndian, 1, 1},
		{source, nil, 1, 1}, {source, binary.LittleEndian, 0, 1},
		{source, binary.BigEndian, 1, 0},
	} {
		if _, err := New(test.source, test.order, test.entries, test.bytes); !errors.Is(err, fault.Options) {
			t.Fatalf("invalid parser options: %v", err)
		}
	}
	for _, entry := range []Entry{
		{}, {Type: Short, Count: 1, Data: []byte{1, 0}},
		{Type: Short, Count: 2, Data: []byte{1, 0}, order: binary.LittleEndian},
	} {
		if _, err := entry.Uint(0); err == nil {
			t.Fatal("invalid/mutated Entry accepted")
		}
		if _, err := entry.Int(0); err == nil {
			t.Fatal("invalid signed access accepted")
		}
		if _, err := entry.Real(0); err == nil {
			t.Fatal("invalid real access accepted")
		}
		if _, _, err := entry.Fraction(0); err == nil {
			t.Fatal("invalid fraction access accepted")
		}
	}
	badText := Entry{Type: ASCII, Count: 1, order: binary.LittleEndian}
	if _, err := badText.Text(); !errors.Is(err, fault.Corrupt) {
		t.Fatalf("truncated text: %v", err)
	}
	empty := make([]byte, 6)
	p := parser(t, empty, binary.LittleEndian, 1, 4096)
	ifd, err := p.ReadIFD(0, 0)
	if err != nil || len(ifd.Entries) != 0 || ifd.Next != 0 {
		t.Fatalf("empty IFD: %+v, %v", ifd, err)
	}
	if _, err := p.ReadIFD(0, -1); !errors.Is(err, fault.Corrupt) {
		t.Fatalf("negative base: %v", err)
	}
}

func TestBudgetAllowanceAndPlatformCount(t *testing.T) {
	if reflect.TypeFor[Entry]().Size() > 96 || reflect.TypeFor[IFD]().Size() > 96 {
		t.Fatal("entry/IFD representation exceeds conservative allocation charge")
	}
	if strconv.IntSize != 32 {
		return // 32-bit test binaries exercise the following allocation guard.
	}
	data := oneIFD(binary.LittleEndian, Byte, 1, []byte{1}, 0)
	binary.LittleEndian.PutUint32(data[14:], 1<<31)
	binary.LittleEndian.PutUint32(data[18:], 128)
	input := sparseReader{data: data}
	source, err := bounded.New(input, 1<<33, 1<<33)
	if err != nil {
		t.Fatal(err)
	}
	reader, err := New(source, binary.LittleEndian, 1, 1<<33)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reader.ReadIFD(8, 0); !errors.Is(err, fault.Limit) {
		t.Fatalf("32-bit TIFF value allocation not rejected: %v", err)
	}
}

type sparseReader struct{ data []byte }

func (s sparseReader) ReadAt(p []byte, off int64) (int, error) {
	if off < 0 || off >= int64(len(s.data)) {
		return 0, io.EOF
	}
	n := copy(p, s.data[off:])
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}

type failReader struct{ err error }

func (r failReader) ReadAt([]byte, int64) (int, error) { return 0, r.err }

func TestParserPreservesIOCause(t *testing.T) {
	disk := errors.New("disk unavailable")
	source, err := bounded.New(failReader{disk}, 100, 100)
	if err != nil {
		t.Fatal(err)
	}
	p, err := New(source, binary.LittleEndian, 1, 100)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.ReadIFD(0, 0); !errors.Is(err, disk) || !errors.Is(err, fault.Truncated) {
		t.Fatalf("wrapped read lost cause: %v", err)
	}
	source, err = bounded.New(bytes.NewReader(nil), 100, 100)
	if err != nil {
		t.Fatal(err)
	}
	p, err = New(source, binary.LittleEndian, 1, 100)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.ReadIFD(0, 0); !errors.Is(err, io.EOF) {
		t.Fatalf("EOF lost: %v", err)
	}
}

func FuzzReadIFD(f *testing.F) {
	f.Add(oneIFD(binary.LittleEndian, Short, 1, []byte{1, 0}, 0), false)
	f.Add(oneIFD(binary.BigEndian, Rational, 1, []byte{0, 0, 0, 1, 0, 0, 0, 2}, 0), true)
	f.Add([]byte{}, false)
	f.Fuzz(func(t *testing.T, data []byte, big bool) {
		var order binary.ByteOrder = binary.LittleEndian
		if big {
			order = binary.BigEndian
		}
		p := parser(t, data, order, 32, 4096)
		ifd, err := p.ReadIFD(8, 0)
		if err != nil {
			var typed *fault.Error
			if !errors.As(err, &typed) {
				t.Fatalf("untyped malformed-input error: %v", err)
			}
			return
		}
		for _, e := range ifd.Entries {
			// Wrong-type accessors must return errors, not panic.
			_, _ = e.Uint(0)
			_, _ = e.Int(0)
			_, _ = e.Real(0)
			_, _, _ = e.Fraction(0)
			_, _ = e.Text()
		}
	})
}
