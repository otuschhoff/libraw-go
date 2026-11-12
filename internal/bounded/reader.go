// SPDX-License-Identifier: LGPL-2.1-only

// Package bounded supplies checked file extents and allocation arithmetic.
package bounded

import (
	"io"
	"math"
	"reflect"

	"github.com/otuschhoff/libraw-go/internal/fault"
)

func Add(a, b uint64) (uint64, error) {
	if b > math.MaxUint64-a {
		return 0, &fault.Error{Op: "add", Kind: fault.Corrupt, Detail: "unsigned addition overflow"}
	}
	return a + b, nil
}

func Mul(a, b uint64) (uint64, error) {
	if a != 0 && b > math.MaxUint64/a {
		return 0, &fault.Error{Op: "multiply", Kind: fault.Corrupt, Detail: "unsigned multiplication overflow"}
	}
	return a * b, nil
}

// Int checks a byte or element count before using it as a Go allocation/index.
func Int(n uint64) (int, error) {
	if n > uint64(^uint(0)>>1) {
		return 0, &fault.Error{Op: "index", Kind: fault.Limit, Detail: "count exceeds platform int"}
	}
	return int(n), nil
}

// Reader is immutable and safe for concurrent calls when its borrowed input is.
// The caller owns the input and must keep it unchanged for the reader's lifetime.
type Reader struct {
	input io.ReaderAt
	size  int64
}

func New(input io.ReaderAt, size, maxInput int64) (*Reader, error) {
	if input == nil || nilValue(input) || size < 0 || maxInput <= 0 {
		return nil, &fault.Error{Op: "input", Kind: fault.Options, Detail: "nil reader, negative size, or nonpositive input limit"}
	}
	if size > maxInput {
		return nil, &fault.Error{Op: "input", Kind: fault.Limit, Detail: "declared input exceeds size limit"}
	}
	return &Reader{input: input, size: size}, nil
}

func nilValue(input io.ReaderAt) bool {
	value := reflect.ValueOf(input)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}

func (r *Reader) Size() int64 { return r.size }

// Offset resolves a relative offset against an explicit base without overflow.
func (r *Reader) Offset(base int64, relative uint64) (int64, error) {
	if base < 0 {
		return 0, &fault.Error{Op: "offset", Kind: fault.Corrupt, Detail: "negative offset base"}
	}
	sum, err := Add(uint64(base), relative)
	if err != nil {
		return 0, err
	}
	if sum > math.MaxInt64 {
		return 0, &fault.Error{Op: "offset", Kind: fault.Corrupt, Detail: "offset exceeds int64"}
	}
	offset := int64(sum)
	if err := r.Extent(offset, 0); err != nil {
		return 0, err
	}
	return offset, nil
}

// Extent permits an empty range at EOF, but never a range beyond declared size.
func (r *Reader) Extent(offset int64, length uint64) error {
	if offset < 0 {
		return &fault.Error{Op: "extent", Kind: fault.Corrupt, Offset: &offset, Detail: "negative offset"}
	}
	if offset > r.size || length > uint64(r.size-offset) {
		return &fault.Error{Op: "extent", Kind: fault.Truncated, Offset: &offset, Detail: "range exceeds declared input"}
	}
	return nil
}

func (r *Reader) ReadAt(data []byte, offset int64) error {
	if err := r.Extent(offset, uint64(len(data))); err != nil {
		return err
	}
	if len(data) == 0 {
		return nil
	}
	n, err := r.input.ReadAt(data, offset)
	if n < 0 || n > len(data) {
		return &fault.Error{Op: "read", Kind: fault.Corrupt, Cause: err, Offset: &offset, Detail: "ReaderAt returned invalid byte count"}
	}
	// A full read with EOF is permitted by ReaderAt. Other failures remain real
	// failures even if the implementation returned all requested bytes.
	if n == len(data) && (err == nil || err == io.EOF) {
		return nil
	}
	if n != len(data) {
		if err == nil {
			err = io.ErrUnexpectedEOF
		}
		return &fault.Error{Op: "read", Kind: fault.Truncated, Cause: err, Offset: &offset, Detail: "short ReaderAt read"}
	}
	return &fault.Error{Op: "read", Cause: err, Offset: &offset}
}

// Bytes validates the extent, platform int, and caller budget before allocation.
func (r *Reader) Bytes(offset int64, length, maxBytes uint64) ([]byte, error) {
	if err := r.Extent(offset, length); err != nil {
		return nil, err
	}
	if length > maxBytes {
		return nil, &fault.Error{Op: "allocate", Kind: fault.Limit, Offset: &offset, Detail: "buffer exceeds byte budget"}
	}
	count, err := Int(length)
	if err != nil {
		return nil, err
	}
	data := make([]byte, count)
	if err := r.ReadAt(data, offset); err != nil {
		return nil, err
	}
	return data, nil
}
