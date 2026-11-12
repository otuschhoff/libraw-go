// SPDX-License-Identifier: LGPL-2.1-only

package libraw

import (
	"encoding/binary"
	"fmt"
	"strings"

	"github.com/otuschhoff/libraw-go/internal/tiff"
)

func (p *parser) makerAt(entry tiff.Entry, depth int64) error {
	if entry.Count < 8 {
		return badEntry(entry, "short Olympus MakerNote header")
	}
	var header [12]byte
	if err := p.source.ReadAt(header[:8], entry.Offset); err != nil {
		return err
	}
	base, start := int64(0), entry.Offset+8
	switch string(header[:8]) {
	case "OLYMP\x00\x02\x00":
		p.makerForm = "OLYMP"
	case "OLYMPUS\x00":
		if entry.Count < 12 {
			return badEntry(entry, "short OLYMPUS header")
		}
		if err := p.source.ReadAt(header[8:], entry.Offset+8); err != nil {
			return err
		}
		if string(header[8:]) != "II\x03\x00" {
			return p.diagnostic("unsupported-makernote", "unevidenced Olympus MakerNote order/version", "EXIF/0x927c")
		}
		p.makerForm, base, start = "OLYMPUS", entry.Offset, entry.Offset+12
	default:
		return p.diagnostic("unsupported-makernote", "unevidenced Olympus MakerNote signature", "EXIF/0x927c")
	}
	var count [2]byte
	if err := p.source.ReadAt(count[:], start); err != nil {
		return err
	}
	needed := uint64(start-entry.Offset) + 2 + uint64(binary.LittleEndian.Uint16(count[:]))*12
	if needed > uint64(entry.Count) {
		return badEntry(entry, "MakerNote root directory exceeds declared block")
	}
	root, err := p.directory(start, base, depth, false, p.reader, func(tag uint16) bool {
		return tag == 0x0200 || tag == 0x1012 || tag == 0x102c || (tag >= 0x2010 && tag <= 0x2050 && tag%0x10 == 0 && p.makerForm == "OLYMPUS")
	})
	if err != nil {
		return err
	}
	if e, ok := root.entries[0x0200]; ok {
		values, err := numbers(e, tiff.Long, 3)
		if err != nil {
			return err
		}
		p.mode = fmt.Sprintf("SpecialMode=%d %d %d", values[0], values[1], values[2])
	}
	if e, ok := root.entries[0x1012]; ok {
		if err := p.blackEntry(e); err != nil {
			return err
		}
	}
	if e, ok := root.entries[0x102c]; ok {
		if err := p.bitsEntry(e); err != nil {
			return err
		}
	}
	for _, tag := range []uint16{0x2010, 0x2020, 0x2030, 0x2040, 0x2050} {
		e, ok := root.entries[tag]
		if !ok {
			continue
		}
		var offset int64
		if p.makerForm == "OLYMP" {
			if e.Type != tiff.Undefined {
				return badEntry(e, "old Olympus subdirectory must be UNDEFINED")
			}
			offset = e.Offset
			if err := p.source.ReadAt(count[:], offset); err != nil {
				return err
			}
			expected := uint64(binary.LittleEndian.Uint16(count[:])) * 12
			switch uint64(e.Count) {
			case expected:
				if err := p.diagnostic("olympus-short-directory", "old Olympus block length omits its two-byte entry count; physical table is file-bounded", fmt.Sprintf("Olympus/0x%04x", tag)); err != nil {
					return err
				}
			case expected + 2:
			default:
				return badEntry(e, "old Olympus subdirectory length is neither exact nor the evidenced two-byte omission")
			}
		} else {
			relative, err := scalar(e, tiff.IFDType)
			if err != nil {
				return err
			}
			if relative == 0 {
				return badEntry(e, "null Olympus subdirectory reference")
			}
			offset, err = p.source.Offset(base, relative)
			if err != nil {
				return err
			}
		}
		child, err := p.directory(offset, base, depth+1, false, p.reader, makerSelector(tag))
		if err != nil {
			return err
		}
		switch tag {
		case 0x2010:
			if e, ok := child.entries[0x0100]; ok {
				value, err := p.text(e)
				if err != nil {
					return err
				}
				p.cameraID, err = p.trimText(value)
				if err != nil {
					return err
				}
			}
		case 0x2020:
			if err := p.cameraSettings(child, base); err != nil {
				return err
			}
		case 0x2040:
			if err := p.imageProcessing(child); err != nil {
				return err
			}
		}
	}
	return nil
}

func makerSelector(parent uint16) func(uint16) bool {
	return func(tag uint16) bool {
		switch parent {
		case 0x2010:
			return tag == 0x0100
		case 0x2020:
			return tag == 0x0100 || tag == 0x0101 || tag == 0x0102 || tag == 0x0600
		case 0x2040:
			return tag == 0x0100 || tag == 0x0600 || (tag >= 0x0611 && tag <= 0x0615)
		}
		return false
	}
}

func (p *parser) blackEntry(e tiff.Entry) error {
	values, err := numbers(e, tiff.Short, 4)
	if err != nil {
		return err
	}
	value := [4]uint64{values[0], values[1], values[2], values[3]}
	if p.hasBlack && value != p.black {
		return badEntry(e, "conflicting Olympus black levels")
	}
	p.black, p.hasBlack = value, true
	return nil
}

func (p *parser) bitsEntry(e tiff.Entry) error {
	values, err := numbers(e, tiff.Short, 2)
	if err != nil {
		return err
	}
	if values[0] == 0 || values[1] != 0 {
		return badEntry(e, "invalid or unevidenced ValidBits components")
	}
	if p.validBits != 0 && p.validBits != values[0] {
		return badEntry(e, "conflicting Olympus ValidBits values")
	}
	p.validBits = values[0]
	return nil
}

func (p *parser) cameraSettings(d directory, base int64) error {
	valid, exists := d.entries[0x0100]
	if exists {
		value, err := scalar(valid, tiff.Long)
		if err != nil {
			return err
		}
		if value == 1 {
			start, startOK := d.entries[0x0101]
			length, lengthOK := d.entries[0x0102]
			if !startOK || !lengthOK {
				return badEntry(valid, "valid preview lacks start/length")
			}
			relative, err := scalar(start, tiff.Long)
			if err != nil {
				return err
			}
			count, err := scalar(length, tiff.Long)
			if err != nil {
				return err
			}
			offset, err := p.source.Offset(base, relative)
			if err != nil {
				return err
			}
			if err := p.addPreview(offset, count, Value[Orientation]{}, "Olympus/CameraSettings"); err != nil {
				return err
			}
		} else if value != 0 {
			return badEntry(valid, "invalid PreviewImageValid")
		}
	}
	if e, ok := d.entries[0x0600]; ok {
		if e.Type != tiff.Short || (e.Count != 2 && e.Count != 3) {
			return badEntry(e, "unevidenced DriveMode encoding")
		}
		values, err := numbers(e, tiff.Short, e.Count)
		if err != nil {
			return err
		}
		parts := make([]string, len(values))
		for i, value := range values {
			parts[i] = fmt.Sprint(value)
		}
		p.mode += ";DriveMode=" + strings.Join(parts, " ")
	}
	return nil
}

func (p *parser) imageProcessing(d directory) error {
	if e, ok := d.entries[0x0100]; ok {
		count := uint32(2)
		if p.metadata.NormalizedMake == "Olympus" && p.metadata.NormalizedModel == "E-M5" && p.cameraID == "S0036" && e.Count == 4 {
			count = 4
		}
		values, err := numbers(e, tiff.Short, count)
		if err != nil {
			return err
		}
		if count == 4 && (values[2] != 256 || values[3] != 256) {
			return badEntry(e, "unevidenced E-M5 green-reference WB components")
		}
		p.wb, p.hasWB = [2]uint64{values[0], values[1]}, true
	}
	if e, ok := d.entries[0x0600]; ok {
		if err := p.blackEntry(e); err != nil {
			return err
		}
	}
	if e, ok := d.entries[0x0611]; ok {
		if err := p.bitsEntry(e); err != nil {
			return err
		}
	}
	for i := range 4 {
		if e, ok := d.entries[uint16(0x0612+i)]; ok {
			var value uint64
			if i < 2 {
				values, err := numbers(e, tiff.Short, 2)
				if err != nil {
					return err
				}
				if values[1] != 0 {
					return badEntry(e, "unevidenced crop component")
				}
				value = values[0]
			} else {
				var err error
				value, err = scalar(e, tiff.Long)
				if err != nil {
					return err
				}
			}
			p.crop[i] = value
			p.cropFields++
		}
	}
	return nil
}
