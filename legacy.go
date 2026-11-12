// SPDX-License-Identifier: LGPL-2.1-only
//
// Source-informed by LibRaw 0.21.4 olympus_load_raw and getbithuff:
// copyright 2019-2021 LibRaw LLC and 1997-2018 Dave Coffin.
// See THIRD_PARTY.md and docs/orf-layouts.md.

package libraw

import (
	"context"
	"errors"
	"fmt"

	"github.com/otuschhoff/libraw-go/internal/bounded"
)

const legacyBufferBytes = 4096

// legacyBits has no speculative lookahead. Buffered bytes are always inside
// the declared strip; consumed counts only bits belonging to decoded fields.
type legacyBits struct {
	source   *bounded.Reader
	buffer   []byte
	start    int64
	next     int64
	end      int64
	at, size int
	value    uint32
	valid    uint8
	consumed uint64
}

func (b *legacyBits) read(width uint8) (uint32, error) {
	if width > 16 {
		return 0, &Error{Op: "read legacy bits", Kind: ErrCorrupt, Detail: "field width exceeds 16"}
	}
	if width == 0 {
		return 0, nil
	}
	for b.valid < width {
		if b.at == b.size {
			if b.next == b.end {
				missing := b.consumed + uint64(b.valid)
				offset, bit := b.start+int64(missing/8), uint8(missing%8)
				return 0, &Error{Op: "read legacy bits", Kind: ErrTruncated, Offset: &offset, Bit: &bit, Detail: "missing required field bits"}
			}
			size := int(min(int64(len(b.buffer)), b.end-b.next))
			if err := b.source.ReadAt(b.buffer[:size], b.next); err != nil {
				return 0, err
			}
			b.at, b.size = 0, size
			b.next += int64(size)
		}
		b.value = b.value<<8 | uint32(b.buffer[b.at])
		b.at++
		b.valid += 8
	}
	b.valid -= width
	b.consumed += uint64(width)
	return (b.value >> b.valid) & ((1 << width) - 1), nil
}

func (b *legacyBits) high(width uint8) (uint32, error) {
	if width < 2 || width > 16 {
		return 0, &Error{Op: "legacy prefix", Kind: ErrCorrupt, Detail: "suffix width outside 2..16"}
	}
	for high := uint32(0); high < 12; high++ {
		bit, err := b.read(1)
		if err != nil {
			return 0, err
		}
		if bit != 0 {
			return high, nil
		}
	}
	escape, err := b.read(16 - width)
	return escape >> 1, err
}

type legacyState struct {
	magnitude uint32
	bias      int32
	small     uint8
}

func (s legacyState) width() uint8 {
	aux := uint8(0)
	if s.small < 3 {
		aux = 2
	}
	width := 2 + aux
	for uint16(s.magnitude)>>(width+aux) != 0 {
		width++
	}
	return width
}

func (s *legacyState) residual(b *legacyBits) (int32, uint32, error) {
	width := s.width()
	flags, err := b.read(3)
	if err != nil {
		return 0, 0, err
	}
	high, err := b.high(width)
	if err != nil {
		return 0, 0, err
	}
	suffix, err := b.read(width)
	if err != nil {
		return 0, 0, err
	}
	magnitude := high<<width | suffix
	diff := int32(magnitude)
	if flags&4 != 0 {
		diff = -diff - 1
	}
	diff += s.bias
	s.bias = (3*diff + s.bias) >> 5
	s.magnitude = magnitude
	if magnitude > 16 {
		s.small = 0
	} else {
		s.small = min(s.small+1, 3)
	}
	return diff, flags & 3, nil
}

func legacyPredictor(pixels []uint16, width, x, y int) int32 {
	if y < 2 {
		if x < 2 {
			return 0
		}
		return int32(pixels[y*width+x-2])
	}
	n := int32(pixels[(y-2)*width+x])
	if x < 2 {
		return n
	}
	w := int32(pixels[y*width+x-2])
	nw := int32(pixels[(y-2)*width+x-2])
	dw, dn := w-nw, n-nw
	if dw < 0 {
		dw = -dw
	}
	if dn < 0 {
		dn = -dn
	}
	if (w < nw && nw < n) || (n < nw && nw < w) {
		if dw > 32 || dn > 32 {
			return w + n - nw
		}
		return (w + n) / 2
	}
	if dw > dn {
		return w
	}
	return n
}

func decodeLegacyRows(ctx context.Context, b *legacyBits, pixels []uint16, width, codedWidth, height int) error {
	for y := range height {
		if err := ctx.Err(); err != nil {
			return err
		}
		var states [2]legacyState
		for x := range codedWidth {
			sampleStart := b.consumed
			diff, low, err := states[x&1].residual(b)
			if err != nil {
				offset, bit := b.start+int64(sampleStart/8), uint8(sampleStart%8)
				return &Error{Op: "decode legacy residual", Cause: err, Offset: &offset, Bit: &bit, Detail: fmt.Sprintf("row=%d column=%d", y, x)}
			}
			if x >= width {
				continue
			}
			sample := legacyPredictor(pixels, width, x, y) + 4*diff + int32(low)
			if sample < 0 || sample > 4095 {
				offset, bit := b.start+int64(sampleStart/8), uint8(sampleStart%8)
				return &Error{Op: "decode legacy sample", Kind: ErrCorrupt, Offset: &offset, Bit: &bit, Detail: fmt.Sprintf("row=%d column=%d sample=%d outside 0..4095", y, x, sample)}
			}
			pixels[y*width+x] = uint16(sample)
		}
	}
	return ctx.Err()
}

func (f *File) legacyReader(buffer []byte) legacyBits {
	strip := f.plan.strips[0]
	start := strip.offset + int64(f.plan.headerBytes)
	return legacyBits{source: f.source, buffer: buffer, start: start, next: start, end: strip.offset + int64(strip.length)}
}

func (f *File) decodeLegacy(ctx context.Context) (*RawImage, error) {
	count, scratchBytes, diagnostics, err := f.rawResources()
	if err != nil {
		return nil, err
	}
	pixels := make([]uint16, count)
	bits := f.legacyReader(make([]byte, scratchBytes))
	if err := decodeLegacyRows(ctx, &bits, pixels, f.plan.width, f.plan.width, f.plan.height); err != nil {
		failure := &Error{Op: "decode raw", Cause: err, Layout: f.plan.layout}
		var location *Error
		if errors.As(err, &location) {
			failure.Offset, failure.Bit = location.Offset, location.Bit
		}
		return nil, failure
	}
	return f.rawImage(pixels, diagnostics), nil
}
