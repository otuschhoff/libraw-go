// SPDX-License-Identifier: LGPL-2.1-only

// Package tiff implements bounded classic IFD and tag access, not container
// recognition, directory traversal, image selection, or MakerNote policy.
package tiff

import (
	"encoding/binary"
	"math"

	"github.com/otuschhoff/libraw-go/internal/bounded"
	"github.com/otuschhoff/libraw-go/internal/fault"
)

type Type uint16

const (
	Byte Type = 1 + iota
	ASCII
	Short
	Long
	Rational
	SByte
	Undefined
	SShort
	SLong
	SRational
	Float
	Double
	IFDType
)

func (t Type) Size() (uint64, error) {
	switch t {
	case Byte, ASCII, SByte, Undefined:
		return 1, nil
	case Short, SShort:
		return 2, nil
	case Long, SLong, Float, IFDType:
		return 4, nil
	case Rational, SRational, Double:
		return 8, nil
	default:
		return 0, &fault.Error{Op: "TIFF type", Kind: fault.Unsupported, Detail: "unknown classic TIFF field type"}
	}
}

// Entry owns its bytes. Offset is the absolute position of the value, including
// inline values, not the offset of its directory record.
type Entry struct {
	Tag    uint16
	Type   Type
	Count  uint32
	Offset int64
	Data   []byte
	order  binary.ByteOrder
}

type IFD struct {
	Offset  int64
	Base    int64
	Entries []Entry
	Next    uint32 // relative to Base; zero means no next IFD
}

// Reader's counters belong to one sequential parse operation. Sharing a Reader
// between concurrent operations is not supported; create independent readers.
type Reader struct {
	source     *bounded.Reader
	order      binary.ByteOrder
	maxEntries uint64
	remaining  *uint64
}

// New accepts only explicit little/big endian order and positive budgets.
// Budget is an aggregate materialization quota: charges are not refunded when
// an IFD is discarded. It includes a conservative entry-structure allowance.
func New(source *bounded.Reader, order binary.ByteOrder, maxEntries, budget uint64) (*Reader, error) {
	if source == nil || (order != binary.LittleEndian && order != binary.BigEndian) || maxEntries == 0 || budget == 0 {
		return nil, &fault.Error{Op: "TIFF options", Kind: fault.Options, Detail: "missing source, byte order, or positive budgets"}
	}
	return &Reader{source: source, order: order, maxEntries: maxEntries, remaining: &budget}, nil
}

func (r *Reader) charge(bytes uint64) error {
	if bytes > *r.remaining {
		return &fault.Error{Op: "TIFF metadata", Kind: fault.Limit, Detail: "aggregate metadata budget exceeded"}
	}
	*r.remaining -= bytes
	return nil
}

// Reserve accounts for parser-owned metadata outside IFD entry storage.
func (r *Reader) Reserve(bytes uint64) error { return r.charge(bytes) }

func (r *Reader) Remaining() uint64 { return *r.remaining }

// WithOrder shares the operation's aggregate budget across endian domains.
func (r *Reader) WithOrder(order binary.ByteOrder) (*Reader, error) {
	if order != binary.LittleEndian && order != binary.BigEndian {
		return nil, &fault.Error{Op: "TIFF byte order", Kind: fault.Options}
	}
	copy := *r
	copy.order = order
	return &copy, nil
}

// ReadIFD reads one classic directory. offset and base are absolute file byte
// positions; out-of-line values and Next are relative to base. It intentionally
// does not follow pointers. MakerNote framing requires explicit ReadDirectory
// policy rather than relaxing this classic entry point.
func (r *Reader) ReadIFD(offset, base int64) (*IFD, error) {
	return r.ReadDirectory(offset, base, true, nil)
}

// ReadDirectory supports explicitly framed MakerNote directories without a
// next-IFD pointer. selectTag controls materialization only: every entry's type,
// count, and extent is still checked. Unselected entries retain Offset/Count,
// but Data is nil and typed access is invalid.
func (r *Reader) ReadDirectory(offset, base int64, withNext bool, selectTag func(uint16) bool) (*IFD, error) {
	if _, err := r.source.Offset(base, 0); err != nil {
		return nil, err
	}
	var header [2]byte
	if err := r.source.ReadAt(header[:], offset); err != nil {
		return nil, err
	}
	count := uint64(r.order.Uint16(header[:]))
	if count > r.maxEntries {
		return nil, &fault.Error{Op: "TIFF IFD", Kind: fault.Limit, IFD: &offset, Detail: "entry count exceeds quota"}
	}
	tableBytes := count * 12 // count is a uint16, so these operations cannot overflow.
	if withNext {
		tableBytes += 4
	}
	tableOffset, err := r.source.Offset(offset, 2)
	if err != nil {
		return nil, err
	}
	// Entry is smaller than 96 bytes on both supported 32/64-bit platforms.
	// Include table, entry storage, and the IFD header in the aggregate quota.
	if err := r.charge(tableBytes + count*96 + 96); err != nil {
		return nil, err
	}
	table, err := r.source.Bytes(tableOffset, tableBytes, tableBytes)
	if err != nil {
		return nil, err
	}
	entries := make([]Entry, int(count))
	for i := range entries {
		record := table[i*12 : (i+1)*12]
		tag := r.order.Uint16(record)
		fieldType := Type(r.order.Uint16(record[2:]))
		n := r.order.Uint32(record[4:])
		selected := selectTag == nil || selectTag(tag)
		entry, err := r.readEntry(tag, fieldType, n, record[8:12], tableOffset+int64(i*12)+8, base, selected)
		if err != nil {
			return nil, &fault.Error{Op: "TIFF entry", Cause: err, IFD: &offset, Tag: &tag}
		}
		entries[i] = entry
	}
	var next uint32
	if withNext {
		next = r.order.Uint32(table[count*12:])
	}
	return &IFD{Offset: offset, Base: base, Entries: entries, Next: next}, nil
}

func (r *Reader) readEntry(tag uint16, typ Type, count uint32, inline []byte, inlineOffset, base int64, selected bool) (Entry, error) {
	entry := Entry{Tag: tag, Type: typ, Count: count, order: r.order}
	size, err := typ.Size()
	if err != nil {
		return Entry{}, err
	}
	length, err := bounded.Mul(uint64(count), size)
	if err != nil {
		return Entry{}, err
	}
	if count == 0 {
		return Entry{}, &fault.Error{Op: "TIFF value", Kind: fault.Corrupt, Detail: "zero field count"}
	}
	if selected {
		if err := r.charge(length); err != nil {
			return Entry{}, err
		}
	}
	if length <= 4 {
		entry.Offset = inlineOffset
	} else {
		entry.Offset, err = r.source.Offset(base, uint64(r.order.Uint32(inline)))
		if err != nil {
			return Entry{}, err
		}
	}
	if err := r.source.Extent(entry.Offset, length); err != nil {
		return Entry{}, err
	}
	if !selected {
		return entry, nil
	}
	if length <= 4 {
		entry.Data = make([]byte, int(length))
		copy(entry.Data, inline)
	} else {
		entry.Data, err = r.source.Bytes(entry.Offset, length, length)
		if err != nil {
			return Entry{}, err
		}
	}
	return entry, nil
}

func (e Entry) element(index uint32) ([]byte, error) {
	size, err := e.Type.Size()
	if err != nil {
		return nil, err
	}
	if e.order != binary.LittleEndian && e.order != binary.BigEndian {
		return nil, e.invalid("missing byte order")
	}
	if index >= e.Count || uint64(len(e.Data)) != uint64(e.Count)*size {
		return nil, e.invalid("index or value length does not match field count")
	}
	start := uint64(index) * size
	return e.Data[start : start+size], nil
}

func (e Entry) invalid(detail string) error {
	tag, offset := e.Tag, e.Offset
	return &fault.Error{Op: "TIFF access", Kind: fault.Corrupt, Tag: &tag, Offset: &offset, Detail: detail}
}

// Uint accesses unsigned integer and IFD-pointer types without conversions from
// signed, floating, rational, or byte-string types.
func (e Entry) Uint(index uint32) (uint64, error) {
	data, err := e.element(index)
	if err != nil {
		return 0, err
	}
	switch e.Type {
	case Byte:
		return uint64(data[0]), nil
	case Short:
		return uint64(e.order.Uint16(data)), nil
	case Long, IFDType:
		return uint64(e.order.Uint32(data)), nil
	default:
		return 0, e.invalid("not an unsigned integer field")
	}
}

func (e Entry) Int(index uint32) (int64, error) {
	data, err := e.element(index)
	if err != nil {
		return 0, err
	}
	switch e.Type {
	case SByte:
		return int64(int8(data[0])), nil
	case SShort:
		return int64(int16(e.order.Uint16(data))), nil
	case SLong:
		return int64(int32(e.order.Uint32(data))), nil
	default:
		return 0, e.invalid("not a signed integer field")
	}
}

// Fraction preserves exact numerator/denominator values (including signed
// denominators). A zero denominator is corrupt, never an implicit zero/NaN.
func (e Entry) Fraction(index uint32) (numerator, denominator int64, err error) {
	data, err := e.element(index)
	if err != nil {
		return 0, 0, err
	}
	switch e.Type {
	case Rational:
		numerator, denominator = int64(e.order.Uint32(data)), int64(e.order.Uint32(data[4:]))
	case SRational:
		numerator, denominator = int64(int32(e.order.Uint32(data))), int64(int32(e.order.Uint32(data[4:])))
	default:
		return 0, 0, e.invalid("not a rational field")
	}
	if denominator == 0 {
		return 0, 0, e.invalid("zero rational denominator")
	}
	return numerator, denominator, nil
}

// Real accesses rationals and IEEE floating fields; nonfinite values are errors.
func (e Entry) Real(index uint32) (float64, error) {
	if e.Type == Rational || e.Type == SRational {
		n, d, err := e.Fraction(index)
		if err != nil {
			return 0, err
		}
		return float64(n) / float64(d), nil
	}
	data, err := e.element(index)
	if err != nil {
		return 0, err
	}
	var value float64
	switch e.Type {
	case Float:
		value = float64(math.Float32frombits(e.order.Uint32(data)))
	case Double:
		value = math.Float64frombits(e.order.Uint64(data))
	default:
		return 0, e.invalid("not a real-valued field")
	}
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return 0, e.invalid("nonfinite floating value")
	}
	return value, nil
}

// Text requires a NUL-terminated ASCII field. It removes only trailing NULs,
// preserving spaces and other original bytes for later metadata policy.
func (e Entry) Text() (string, error) {
	if e.Type != ASCII {
		return "", e.invalid("not an ASCII field")
	}
	if _, err := e.element(0); err != nil {
		return "", err
	}
	end := len(e.Data)
	if e.Data[end-1] != 0 {
		return "", e.invalid("ASCII field lacks NUL terminator")
	}
	for end > 0 && e.Data[end-1] == 0 {
		end--
	}
	for _, value := range e.Data[:end] {
		if value >= 128 {
			return "", e.invalid("non-ASCII text byte")
		}
	}
	return string(e.Data[:end]), nil
}
