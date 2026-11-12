// SPDX-License-Identifier: LGPL-2.1-only
package bounded

import (
	"bytes"
	"errors"
	"io"
	"math"
	"strconv"
	"sync"
	"testing"

	"github.com/otuschhoff/libraw-go/internal/fault"
)

type readFunc func([]byte, int64) (int, error)

func (f readFunc) ReadAt(p []byte, off int64) (int, error) { return f(p, off) }

func TestArithmetic(t *testing.T) {
	for _, test := range []struct{ a, b, sum uint64 }{
		{0, 0, 0}, {math.MaxUint64, 0, math.MaxUint64}, {20, 22, 42},
	} {
		got, err := Add(test.a, test.b)
		if err != nil || got != test.sum {
			t.Fatalf("Add %+v = %d, %v", test, got, err)
		}
	}
	if _, err := Add(math.MaxUint64, 1); !errors.Is(err, fault.Corrupt) {
		t.Fatalf("overflow: %v", err)
	}
	for _, test := range []struct{ a, b, product uint64 }{
		{0, math.MaxUint64, 0}, {math.MaxUint64, 1, math.MaxUint64}, {6, 7, 42},
	} {
		if got, err := Mul(test.a, test.b); err != nil || got != test.product {
			t.Fatalf("Mul %+v = %d, %v", test, got, err)
		}
	}
	if _, err := Mul(math.MaxUint64, 2); !errors.Is(err, fault.Corrupt) {
		t.Fatalf("overflow: %v", err)
	}
	max := uint64(^uint(0) >> 1)
	if got, err := Int(max); err != nil || uint64(got) != max {
		t.Fatalf("platform max: %d, %v", got, err)
	}
	if _, err := Int(max + 1); !errors.Is(err, fault.Limit) {
		t.Fatalf("platform max+1: %v", err)
	}
	if strconv.IntSize == 32 {
		if _, err := Int(1 << 31); !errors.Is(err, fault.Limit) {
			t.Fatalf("32-bit allocation would overflow: %v", err)
		}
	}
}

func TestInputValidation(t *testing.T) {
	var typedNil *bytes.Reader
	for _, input := range []io.ReaderAt{nil, typedNil} {
		if _, err := New(input, 0, 1); !errors.Is(err, fault.Options) {
			t.Fatalf("nil input: %v", err)
		}
	}
	for _, test := range []struct {
		size, limit int64
		kind        error
	}{
		{-1, 1, fault.Options}, {1, 0, fault.Options}, {2, 1, fault.Limit},
	} {
		if _, err := New(bytes.NewReader(nil), test.size, test.limit); !errors.Is(err, test.kind) {
			t.Fatalf("size/limit %+v: %v", test, err)
		}
	}
}

func TestExtentsAndAllocation(t *testing.T) {
	input := bytes.NewReader([]byte("abcd"))
	reader, err := New(input, 4, 4)
	if err != nil {
		t.Fatal(err)
	}
	if reader.Size() != 4 {
		t.Fatal("declared input size lost")
	}
	for _, test := range []struct {
		off int64
		n   uint64
	}{
		{0, 4}, {4, 0}, {2, 2},
	} {
		if err := reader.Extent(test.off, test.n); err != nil {
			t.Fatal(err)
		}
	}
	for _, test := range []struct {
		off  int64
		n    uint64
		kind error
	}{
		{-1, 0, fault.Corrupt}, {5, 0, fault.Truncated},
		{4, 1, fault.Truncated}, {1, math.MaxUint64, fault.Truncated},
	} {
		if err := reader.Extent(test.off, test.n); !errors.Is(err, test.kind) {
			t.Fatalf("extent %+v: %v", test, err)
		}
	}
	data, err := reader.Bytes(1, 2, 2)
	if err != nil || string(data) != "bc" {
		t.Fatalf("bounded read: %q, %v", data, err)
	}
	if got, err := reader.Bytes(0, 4, 3); got != nil || !errors.Is(err, fault.Limit) {
		t.Fatalf("allocation budget: %q, %v", got, err)
	}
	if data, err := reader.Bytes(4, 1, 1); data != nil || !errors.Is(err, fault.Truncated) {
		t.Fatalf("out-of-extent allocation: %v, %v", data, err)
	}
	if err := reader.ReadAt(make([]byte, 1), 4); !errors.Is(err, fault.Truncated) {
		t.Fatalf("out-of-extent read: %v", err)
	}
	for _, test := range []struct {
		base int64
		rel  uint64
		kind error
	}{
		{-1, 0, fault.Corrupt}, {1, math.MaxUint64, fault.Corrupt},
		{0, math.MaxInt64 + 1, fault.Corrupt}, {4, 1, fault.Truncated},
	} {
		if _, err := reader.Offset(test.base, test.rel); !errors.Is(err, test.kind) {
			t.Fatalf("offset %+v: %v", test, err)
		}
	}
	if off, err := reader.Offset(1, 3); err != nil || off != 4 {
		t.Fatalf("relative offset: %d, %v", off, err)
	}
	// A lying declared size must not permit platform int overflow before I/O.
	called := false
	huge, err := New(readFunc(func([]byte, int64) (int, error) {
		called = true
		return 0, nil
	}), math.MaxInt64, math.MaxInt64)
	if err != nil {
		t.Fatal(err)
	}
	if strconv.IntSize == 32 {
		if _, err := huge.Bytes(0, 1<<31, 1<<31); !errors.Is(err, fault.Limit) || called {
			t.Fatalf("allocation was not rejected before read: %v", err)
		}
	}
}

func TestShortReadsAndIOCauses(t *testing.T) {
	disk := errors.New("disk failed")
	for _, test := range []struct {
		name string
		n    int
		err  error
		kind error
	}{
		{"full", 4, nil, nil}, {"full EOF", 4, io.EOF, nil},
		{"short EOF", 3, io.EOF, fault.Truncated},
		{"short nil", 3, nil, fault.Truncated},
		{"short disk", 2, disk, fault.Truncated},
		{"full disk", 4, disk, disk},
		{"full joined EOF and disk", 4, errors.Join(io.EOF, disk), disk},
		{"invalid negative", -1, disk, fault.Corrupt},
		{"invalid excess", 5, nil, fault.Corrupt},
	} {
		t.Run(test.name, func(t *testing.T) {
			r, err := New(readFunc(func([]byte, int64) (int, error) { return test.n, test.err }), 4, 4)
			if err != nil {
				t.Fatal(err)
			}
			err = r.ReadAt(make([]byte, 4), 0)
			if test.kind == nil {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if !errors.Is(err, test.kind) {
				t.Fatalf("classification: %v", err)
			}
			if test.err != nil && !errors.Is(err, test.err) {
				t.Fatalf("I/O cause lost: %v", err)
			}
			if test.name == "short nil" && !errors.Is(err, io.ErrUnexpectedEOF) {
				t.Fatalf("missing short-read cause: %v", err)
			}
		})
	}
}

func TestBytesNeverReturnsPartialData(t *testing.T) {
	input := readFunc(func(data []byte, _ int64) (int, error) {
		copy(data, "x")
		return 1, io.EOF
	})
	reader, err := New(input, 4, 4)
	if err != nil {
		t.Fatal(err)
	}
	data, err := reader.Bytes(0, 4, 4)
	if data != nil || !errors.Is(err, fault.Truncated) || !errors.Is(err, io.EOF) {
		t.Fatalf("partial success escaped: %q, %v", data, err)
	}
}

func TestEmptyAndConcurrentReads(t *testing.T) {
	r, err := New(bytes.NewReader([]byte("data")), 4, 4)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.ReadAt(nil, 4); err != nil {
		t.Fatal(err)
	}
	var group sync.WaitGroup
	for range 8 {
		group.Go(func() {
			for range 50 {
				data, err := r.Bytes(0, 4, 4)
				if err != nil || string(data) != "data" {
					t.Errorf("concurrent read: %q, %v", data, err)
				}
			}
		})
	}
	group.Wait()
}
