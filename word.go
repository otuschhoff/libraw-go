// SPDX-License-Identifier: LGPL-2.1-only

package libraw

import (
	"context"
	"encoding/binary"

	"github.com/otuschhoff/libraw-go/internal/bounded"
)

// rawResources checks explicit per-call buffers. Shared Budget accounting also
// retains File metadata and other live results, but not Go runtime RSS.
// The diagnostic allowance covers each slice
// element (three strings) on both 32- and 64-bit platforms.
func (f *File) rawResources() (pixels, rowBytes, diagnostics int, err error) {
	diagnostics = len(f.metadata.Diagnostics)
	if !f.metadata.Geometry.ActiveArea.Present() {
		diagnostics++
	}
	diagnosticBytes, err := bounded.Mul(uint64(diagnostics), 64)
	if err != nil {
		return 0, 0, 0, err
	}
	working, err := bounded.Add(f.plan.outputBytes, f.plan.scratchBytes)
	if err != nil {
		return 0, 0, 0, err
	}
	working, err = bounded.Add(working, diagnosticBytes)
	if err != nil {
		return 0, 0, 0, err
	}
	if working > uint64(f.limits.WorkingBytes) {
		return 0, 0, 0, &Error{Op: "decode raw", Kind: ErrLimitExceeded, Layout: f.plan.layout, Detail: "output plane, codec scratch and diagnostics exceed working buffer quota"}
	}
	pixels, err = bounded.Int(f.plan.outputBytes / 2)
	if err != nil {
		return 0, 0, 0, err
	}
	// Check bytes as well as uint16 elements before a platform allocation.
	if _, err := bounded.Int(f.plan.outputBytes); err != nil {
		return 0, 0, 0, err
	}
	rowBytes, err = bounded.Int(f.plan.scratchBytes)
	return pixels, rowBytes, diagnostics, err
}

func (f *File) decodeWord(ctx context.Context) (*RawImage, error) {
	count, rowBytes, diagnosticCount, err := f.rawResources()
	if err != nil {
		return nil, err
	}
	account, err := f.reserveRaw()
	if err != nil {
		return nil, err
	}
	success := false
	defer func() {
		if !success {
			account.Close()
		}
	}()
	if err := ctx.Err(); err != nil {
		return nil, &Error{Op: "decode raw", Cause: err}
	}
	source := f.source.WithContext(ctx)
	pixels := make([]uint16, count)
	row := make([]byte, rowBytes)
	for _, strip := range f.plan.strips {
		for y := uint64(0); y < strip.rows; y++ {
			if err := ctx.Err(); err != nil {
				return nil, &Error{Op: "decode raw", Cause: err, Layout: f.plan.layout}
			}
			offset := strip.offset + int64(y*f.plan.rowBytes)
			if err := source.ReadAt(row, offset); err != nil {
				position := offset
				return nil, &Error{Op: "decode raw row", Cause: err, Offset: &position, Layout: f.plan.layout}
			}
			start := int((strip.row + y) * uint64(f.plan.width))
			output := pixels[start : start+f.plan.width]
			for x := range output {
				if x&255 == 0 {
					if err := ctx.Err(); err != nil {
						return nil, &Error{Op: "decode raw", Cause: err}
					}
				}
				output[x] = binary.LittleEndian.Uint16(row[x*2:]) >> f.plan.rightShift
			}
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, &Error{Op: "decode raw", Cause: err, Layout: f.plan.layout}
	}
	raw := f.rawImage(pixels, diagnosticCount)
	account.Release(f.plan.scratchBytes)
	raw.budget, raw.account = f.budget, account
	raw.owner = f.owner
	raw.owner.retain()
	success = true
	return raw, nil
}

func (f *File) reserveRaw() (*bounded.Account, error) {
	_, account, err := operationAccount(f.budget, f.limits.WorkingBytes)
	if err != nil {
		return nil, err
	}
	// rawResources checked the overflow-safe sum and platform allocation sizes.
	diagnostics := len(f.metadata.Diagnostics)
	if !f.metadata.Geometry.ActiveArea.Present() {
		diagnostics++
	}
	if err := account.Reserve(f.plan.outputBytes + f.plan.scratchBytes + uint64(diagnostics)*64); err != nil {
		return nil, err
	}
	return account, nil
}

func (f *File) rawImage(pixels []uint16, diagnosticCount int) *RawImage {
	m := f.metadata
	diagnostics := make([]Diagnostic, diagnosticCount)
	copy(diagnostics, m.Diagnostics)
	if !m.Geometry.ActiveArea.Present() {
		diagnostics[len(diagnostics)-1] = Diagnostic{Code: "active-area-unavailable", Message: "no verified active crop; all decoded sensor pixels are retained", Source: "raw geometry"}
	}
	return &RawImage{
		Pixels: pixels, Width: f.plan.width, Height: f.plan.height, Stride: f.plan.width,
		ActiveArea: m.Geometry.ActiveArea.Value, CFA: f.plan.cfa, BitDepth: f.plan.bits,
		Calibration: m.Calibration, Orientation: m.Orientation.Value, Diagnostics: diagnostics,
	}
}
