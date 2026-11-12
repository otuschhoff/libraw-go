// SPDX-License-Identifier: LGPL-2.1-only

package libraw

import (
	"bytes"
	"encoding/binary"
	"fmt"

	"github.com/otuschhoff/libraw-go/internal/bounded"
	"github.com/otuschhoff/libraw-go/internal/tiff"
)

type stripPlan struct {
	offset int64
	length uint64
	row    uint64
	rows   uint64
}

// Plans are private, immutable, and independent of temporary TIFF entry buffers.
// Layout recognition is not a promise that a pixel decoder is implemented.
type decodePlan struct {
	layout        string
	strips        []stripPlan
	width, height int
	bits          uint8
	rightShift    uint8
	outputBytes   uint64
	scratchBytes  uint64
	geometry      Geometry
	cfa           CFA
	storageBits   uint8
	byteOrder     binary.ByteOrder
	bitOrder      string
	headerBytes   uint8
	rowBytes      uint64
}

func (p *parser) makePlan(d directory) (decodePlan, string, error) {
	m := p.metadata
	plan := decodePlan{width: m.Geometry.Width, height: m.Geometry.Height, bits: m.BitDepth.Value, geometry: m.Geometry, cfa: m.CFA.Value}
	pixels, err := bounded.Mul(uint64(plan.width), uint64(plan.height))
	if err != nil {
		return plan, "", err
	}
	output, err := bounded.Mul(pixels, 2)
	if err != nil {
		return plan, "", err
	}
	plan.outputBytes = output
	if _, err := bounded.Int(output); err != nil {
		return plan, "", err
	}
	// No pixel allocation is performed here. DecodeRaw enforces the operation's
	// output, scratch and diagnostic storage quota before allocating.
	offsets, hasOffsets := d.entries[0x111]
	counts, hasCounts := d.entries[0x117]
	rowsEntry, hasRows := d.entries[0x116]
	if hasOffsets != hasCounts {
		return plan, "", corrupt("strip descriptors", "incomplete offset/byte-count pair")
	}
	tiles, hasTiles := d.entries[0x144]
	tileCounts, hasTileCounts := d.entries[0x145]
	if hasTiles != hasTileCounts {
		return plan, "", corrupt("tile descriptors", "incomplete offset/byte-count pair")
	}
	if hasTiles {
		if hasOffsets {
			return plan, "", corrupt("raw organization", "both strip and tile payload descriptors")
		}
		if err := p.payloadExtents(tiles, tileCounts); err != nil {
			return plan, "", err
		}
		return plan, "unsupported raw organization: tiled reconstruction is deferred", nil
	}
	if !hasOffsets {
		return plan, "unsupported raw organization: no evidenced payload descriptors", nil
	}
	if err := p.payloadExtents(offsets, counts); err != nil {
		return plan, "", err
	}
	if !hasRows {
		return plan, "", corrupt("strip descriptors", "missing rows per strip")
	}
	if offsets.Type != tiff.Long || counts.Type != tiff.Long || offsets.Count != counts.Count {
		return plan, "", badEntry(offsets, "strip offsets/counts require equal LONG arrays")
	}
	rows, err := scalar(rowsEntry, tiff.Short, tiff.Long)
	if err != nil {
		return plan, "", err
	}
	if rows == 0 {
		return plan, "", badEntry(rowsEntry, "zero rows per strip")
	}
	expected := (uint64(plan.height) + rows - 1) / rows
	if uint64(offsets.Count) != expected {
		return plan, "", badEntry(offsets, "strip count does not cover rows exactly")
	}
	if err := p.reader.Reserve(uint64(offsets.Count) * 64); err != nil {
		return plan, "", err
	}
	plan.strips = make([]stripPlan, offsets.Count)
	word := true
	for i := range plan.strips {
		if err := p.source.Check(); err != nil {
			return plan, "", err
		}
		offset, err := offsets.Uint(uint32(i))
		if err != nil {
			return plan, "", err
		}
		length, err := counts.Uint(uint32(i))
		if err != nil {
			return plan, "", err
		}
		if offset < 8 || length == 0 {
			return plan, "", badEntry(offsets, "strip overlaps header or has zero byte count")
		}
		if err := p.source.Extent(int64(offset), length); err != nil {
			return plan, "", err
		}
		row := uint64(i) * rows
		stripRows := min(rows, uint64(plan.height)-row)
		plan.strips[i] = stripPlan{offset: int64(offset), length: length, row: row, rows: stripRows}
		if length != uint64(plan.width)*stripRows*2 {
			word = false
		}
		// Initial evidence covers physically contiguous logical strips. Other
		// orders/gaps remain metadata-only; never concatenate on assumption.
		if i > 0 {
			previous := plan.strips[i-1]
			if uint64(offset) < uint64(previous.offset)+previous.length {
				word = false
			}
			if uint64(offset) != uint64(previous.offset)+previous.length {
				word = false
			}
		}
	}
	bits, err := scalar(d.entries[0x102], tiff.Short)
	if err != nil {
		return plan, "", err
	}
	compression, err := scalar(d.entries[0x103], tiff.Short)
	if err != nil {
		return plan, "", err
	}
	photo, err := scalar(d.entries[0x106], tiff.Short)
	if err != nil {
		return plan, "", err
	}
	if bits != 16 || compression != 1 || photo != 1 || p.validBits != 12 || !m.CFA.Present() {
		return plan, "unsupported raw encoding: evidenced 16-bit container, compression=1, 12 valid bits and Bayer CFA required", nil
	}
	for _, control := range []struct {
		tag  uint16
		typ  tiff.Type
		name string
	}{{0x10a, tiff.Short, "FillOrder"}, {0x11c, tiff.Short, "PlanarConfiguration"},
		{0x13d, tiff.Short, "Predictor"}, {0x153, tiff.Short, "SampleFormat"}, {0x80e5, tiff.Long, "ImageDepth"}} {
		if entry, ok := d.entries[control.tag]; ok {
			value, err := scalar(entry, control.typ)
			if err != nil {
				return plan, "", err
			}
			if value != 1 {
				return plan, fmt.Sprintf("unsupported raw encoding: %s=%d (only default 1 is evidenced)", control.name, value), nil
			}
		}
	}
	plan.storageBits, plan.byteOrder = 16, binary.LittleEndian
	model, identity := m.NormalizedModel, p.cameraID
	if m.NormalizedMake != "Olympus" {
		return plan, "unsupported camera make; no evidenced layout selector", nil
	}
	expectedMode := "SpecialMode=0 0 0;DriveMode=0 0 0"
	if model == "E-1" {
		expectedMode = "SpecialMode=0 0 0;DriveMode=0 0"
	}
	em5 := model == "E-M5" && identity == "S0036" && plan.width == 4640 && plan.height == 3472 && em5CaptureMode(p.mode)
	if p.mode != expectedMode && !(model == "E-410" && p.mode == "SpecialMode=2 1 0;DriveMode=1 1 0") && !em5 {
		return plan, "unsupported capture mode; only fixture-evidenced single stored Bayer frames are planned", nil
	}
	if p.makerForm == "OLYMP" && ((model == "E-1" && identity == "D4040") || (model == "E-400" && identity == "S0009")) && word {
		if m.CFA.Value != (CFA{{Green1, Red}, {Blue, Green2}}) {
			return plan, "unsupported raw CFA for evidenced word layout", nil
		}
		plan.layout, plan.rightShift = "olympus-word16le-high12", 4
		plan.rowBytes = uint64(plan.width) * 2
		plan.scratchBytes = uint64(plan.width) * 2
		return plan, "identified olympus-word16le-high12", nil
	}
	if p.makerForm == "OLYMPUS" && ((model == "E-410" && identity == "S0013") || (model == "E-510" && identity == "S0010") || em5) &&
		len(plan.strips) == 1 && !word {
		if plan.width%2 != 0 {
			return plan, "unsupported odd coded width for evidenced legacy predictive layout", nil
		}
		if m.CFA.Value != (CFA{{Red, Green1}, {Green2, Blue}}) {
			return plan, "unsupported raw CFA for evidenced predictive layout", nil
		}
		var prefix [7]byte
		if plan.strips[0].length < uint64(len(prefix)) {
			return plan, "", corrupt("legacy raw prefix", "compressed strip shorter than prefix")
		}
		if err := p.source.ReadAt(prefix[:], plan.strips[0].offset); err != nil {
			return plan, "", err
		}
		if !bytes.Equal(prefix[:], []byte{0, 0, 0, 0, 1, 0, 0}) {
			return plan, "unsupported compressed prefix; newer parameterized codecs are not legacy predictive", nil
		}
		plan.layout = "olympus-legacy-predictive12"
		plan.headerBytes, plan.bitOrder = 7, "MSB-first"
		plan.scratchBytes = legacyBufferBytes
		return plan, "identified olympus-legacy-predictive12", nil
	}
	return plan, "unsupported model/identity, MakerNote variant, or strip layout; no camera-name-only decoder guess", nil
}

func em5CaptureMode(mode string) bool {
	switch mode {
	case "SpecialMode=2 1 0;DriveMode=5 3 1", "SpecialMode=2 1 0;DriveMode=5 4 1",
		"SpecialMode=2 1 0;DriveMode=5 5 1", "SpecialMode=2 2 0;DriveMode=5 4 1",
		"SpecialMode=2 2 0;DriveMode=5 5 1", "SpecialMode=2 3 0;DriveMode=5 5 1":
		return true
	}
	if mode == "SpecialMode=0 0 0;DriveMode=0 0 0" {
		return true
	}
	for i := 1; i <= 14; i++ {
		if mode == fmt.Sprintf("SpecialMode=2 %d 0;DriveMode=1 %d 0", i, i) {
			return true
		}
	}
	for i := 1; i <= 5; i++ {
		if mode == fmt.Sprintf("SpecialMode=2 %d 0;DriveMode=5 %d 1", i, i) {
			return true
		}
	}
	return false
}

func (p *parser) payloadExtents(offsets, counts tiff.Entry) error {
	if offsets.Type != tiff.Long || counts.Type != tiff.Long || offsets.Count != counts.Count {
		return badEntry(offsets, "payload offsets/counts require equal LONG arrays")
	}
	if err := p.reader.Reserve(uint64(offsets.Count) * 64); err != nil {
		return err
	}
	spans := make([]fileExtent, offsets.Count)
	for i := uint32(0); i < offsets.Count; i++ {
		if err := p.source.Check(); err != nil {
			return err
		}
		offset, err := offsets.Uint(i)
		if err != nil {
			return err
		}
		count, err := counts.Uint(i)
		if err != nil {
			return err
		}
		if offset < 8 || count == 0 {
			return badEntry(offsets, "payload overlaps header or has zero byte count")
		}
		if err := p.source.Extent(int64(offset), count); err != nil {
			return err
		}
		for _, table := range p.tables {
			if err := p.source.Check(); err != nil {
				return err
			}
			if offset < uint64(table.offset)+table.length && uint64(table.offset) < offset+count {
				return badEntry(offsets, "payload overlaps a parsed directory table")
			}
		}
		for _, value := range p.values {
			if err := p.source.Check(); err != nil {
				return err
			}
			if offset < uint64(value.offset)+value.length && uint64(value.offset) < offset+count {
				return badEntry(offsets, "payload overlaps materialized metadata")
			}
		}
		for _, preview := range p.previews {
			if err := p.source.Check(); err != nil {
				return err
			}
			if offset < uint64(preview.offset)+preview.length && uint64(preview.offset) < offset+count {
				return badEntry(offsets, "raw payload overlaps an embedded JPEG extent")
			}
		}
		spans[i] = fileExtent{offset: int64(offset), length: count}
	}
	if err := p.sortExtents(spans); err != nil {
		return err
	}
	for i := 1; i < len(spans); i++ {
		if err := p.source.Check(); err != nil {
			return err
		}
		if uint64(spans[i].offset) < uint64(spans[i-1].offset)+spans[i-1].length {
			return badEntry(offsets, "overlapping payload extents")
		}
	}
	return nil
}

// Bottom-up merge sorting permits cancellation during large descriptor sorts.
// The two 16-byte arrays fit the pre-reserved 64-byte-per-descriptor allowance.
func (p *parser) sortExtents(spans []fileExtent) error {
	if err := p.source.Check(); err != nil {
		return err
	}
	if len(spans) < 2 {
		return p.source.Check()
	}
	scratch := make([]fileExtent, len(spans))
	from, to := spans, scratch
	for width := 1; width < len(spans); width *= 2 {
		for start := 0; start < len(spans); start += 2 * width {
			middle, end := min(start+width, len(spans)), min(start+2*width, len(spans))
			left, right := start, middle
			for i := start; i < end; i++ {
				if i&255 == 0 {
					if err := p.source.Check(); err != nil {
						return err
					}
				}
				if left < middle && (right == end || from[left].offset <= from[right].offset) {
					to[i], left = from[left], left+1
				} else {
					to[i], right = from[right], right+1
				}
			}
		}
		from, to = to, from
	}
	for i := range spans {
		if i&255 == 0 {
			if err := p.source.Check(); err != nil {
				return err
			}
		}
		spans[i] = from[i]
	}
	return p.source.Check()
}
