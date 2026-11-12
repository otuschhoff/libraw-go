// SPDX-License-Identifier: LGPL-2.1-only

package libraw

import (
	"encoding/binary"
	"fmt"
	"image"
	"strings"

	"github.com/otuschhoff/libraw-go/internal/bounded"
	"github.com/otuschhoff/libraw-go/internal/tiff"
)

type directory struct {
	entries map[uint16]tiff.Entry
	offset  int64
	depth   int64
	next    uint32
}

type parser struct {
	source     *bounded.Reader
	reader     *tiff.Reader
	limits     Limits
	visited    map[int64]bool
	images     []directory
	metadata   Metadata
	previews   []previewPlan
	makerForm  string
	cameraID   string
	validBits  uint64
	crop       [4]uint64
	cropFields int
	black      [4]uint64
	hasBlack   bool
	wb         [2]uint64
	hasWB      bool
	mode       string
	tables     []fileExtent
	values     []fileExtent
}

type fileExtent struct {
	offset int64
	length uint64
}

func openORF(source *bounded.Reader, limits Limits, account *bounded.Account) (*File, error) {
	var header [8]byte
	if err := source.ReadAt(header[:4], 0); err != nil {
		return nil, err
	}
	switch string(header[:4]) {
	case "IIRO":
	case "IIRS", "MMOR":
		return nil, &Error{Op: "recognize ORF", Kind: ErrUnsupported, Detail: "ORF signature lacks fixture-backed parser support"}
	default:
		return nil, &Error{Op: "recognize ORF", Kind: ErrNotORF}
	}
	if err := source.ReadAt(header[4:], 4); err != nil {
		return nil, err
	}
	first := binary.LittleEndian.Uint32(header[4:])
	if first < 8 {
		return nil, &Error{Op: "ORF header", Kind: ErrCorrupt, Detail: "first IFD overlaps header"}
	}
	reader, err := tiff.New(source, binary.LittleEndian, uint64(limits.EntriesPerIFD), uint64(limits.MetadataBytes))
	if err != nil {
		return nil, err
	}
	reader.SetAccount(account)
	if err := reader.Reserve(2048); err != nil {
		return nil, err
	}
	p := &parser{source: source, reader: reader, limits: limits, visited: map[int64]bool{}}
	if err := p.imagesAt(int64(first), 1); err != nil {
		return nil, err
	}
	var selected *directory
	for i := range p.images {
		if err := p.source.Check(); err != nil {
			return nil, err
		}
		candidate, err := rawCandidate(p.images[i])
		if err != nil {
			return nil, err
		}
		if candidate {
			if selected != nil {
				return nil, &Error{Op: "select raw IFD", Kind: ErrCorrupt, Detail: "ambiguous sensor directories"}
			}
			selected = &p.images[i]
		}
	}
	if selected == nil {
		return nil, &Error{Op: "select raw IFD", Kind: ErrUnsupported, Detail: "no unambiguous supported sensor-IFD structure"}
	}
	if err := p.imageMetadata(*selected); err != nil {
		return nil, err
	}
	if exif, ok := selected.entries[0x8769]; ok {
		offset, err := scalar(exif, tiff.Long, tiff.IFDType)
		if err != nil {
			return nil, err
		}
		if offset == 0 {
			return nil, corrupt("EXIF pointer", "null directory reference")
		}
		if err := p.exifAt(int64(offset), selected.depth+1); err != nil {
			return nil, err
		}
	}
	if err := p.finishCalibration(); err != nil {
		return nil, err
	}
	plan, reason, err := p.makePlan(*selected)
	if err != nil {
		return nil, err
	}
	if plan.layout != "" {
		p.metadata.Layout = Value[string]{Value: plan.layout, Origin: OriginProfile, Source: "phase0/evidenced-layout-selector"}
		if p.metadata.NormalizedModel == "E-M5" && p.cameraID == "S0036" {
			p.metadata.Layout.Source = "E-M5/S0036/private-corpus-layout-selector"
		}
	}
	if err := p.applyProfile(plan); err != nil {
		return nil, err
	}
	preview, err := p.selectPreview()
	if err != nil {
		return nil, err
	}
	f := &File{source: source, limits: limits, metadata: p.metadata, plan: plan, preview: preview}
	f.caps = Capabilities{
		Metadata: Capability{Available: true},
		Raw:      Capability{Reason: reason},
		Render:   Capability{Reason: "RAW decoding unavailable"},
		Preview:  Capability{Available: preview != nil, Reason: "no validated JPEG preview within configured limits"},
	}
	if preview != nil {
		f.caps.Preview.Reason = ""
	}
	if plan.layout == "olympus-word16le-high12" || plan.layout == "olympus-legacy-predictive12" {
		if _, _, _, err := f.rawResources(); err != nil {
			f.caps.Raw.Reason = err.Error()
		} else {
			f.caps.Raw = Capability{Available: true}
		}
	}
	if f.caps.Raw.Available {
		raw := &RawImage{Width: plan.width, Height: plan.height, Stride: plan.width, BitDepth: plan.bits,
			ActiveArea: p.metadata.Geometry.ActiveArea.Value, CFA: plan.cfa,
			Calibration: p.metadata.Calibration, Orientation: p.metadata.Orientation.Value}
		if _, err := makeRenderPlan(raw, RenderOptions{Limits: limits}, false); err != nil {
			f.caps.Render.Reason = err.Error()
		} else {
			f.caps.Render = Capability{Available: true}
		}
	}
	return f, nil
}

func (p *parser) directory(offset, base, depth int64, classic bool, reader *tiff.Reader, selectTag func(uint16) bool) (directory, error) {
	if depth > p.limits.Depth || int64(len(p.visited)) >= p.limits.IFDs {
		return directory{}, &Error{Op: "IFD graph", Kind: ErrLimitExceeded, IFD: &offset, Detail: "depth or total directory quota exceeded"}
	}
	if p.visited[offset] {
		return directory{}, &Error{Op: "IFD graph", Kind: ErrCorrupt, IFD: &offset, Detail: "cyclic or aliased directory reference"}
	}
	if offset < 8 {
		return directory{}, &Error{Op: "IFD graph", Kind: ErrCorrupt, IFD: &offset, Detail: "directory overlaps ORF header"}
	}
	p.visited[offset] = true
	if err := reader.Reserve(512); err != nil {
		return directory{}, err
	}
	ifd, err := reader.ReadDirectory(offset, base, classic, selectTag)
	if err != nil {
		return directory{}, err
	}
	// Include map capacity rounding and selected-value extent storage, not
	// merely the size of one key/value pair at full occupancy.
	if err := reader.Reserve(uint64(len(ifd.Entries)) * 256); err != nil {
		return directory{}, err
	}
	length := uint64(2 + len(ifd.Entries)*12)
	if classic {
		length += 4
	}
	for _, table := range p.tables {
		if err := p.source.Check(); err != nil {
			return directory{}, err
		}
		if uint64(offset) < uint64(table.offset)+table.length && uint64(table.offset) < uint64(offset)+length {
			return directory{}, &Error{Op: "IFD graph", Kind: ErrCorrupt, IFD: &offset, Detail: "overlapping directory tables"}
		}
	}
	p.tables = append(p.tables, fileExtent{offset: offset, length: length})
	d := directory{entries: make(map[uint16]tiff.Entry, len(ifd.Entries)), offset: offset, depth: depth, next: ifd.Next}
	for _, entry := range ifd.Entries {
		if err := p.source.Check(); err != nil {
			return directory{}, err
		}
		if _, exists := d.entries[entry.Tag]; exists {
			tag := entry.Tag
			return directory{}, &Error{Op: "IFD entries", Kind: ErrCorrupt, IFD: &offset, Tag: &tag, Detail: "duplicate tag"}
		}
		d.entries[entry.Tag] = entry
		if len(entry.Data) > 4 {
			p.values = append(p.values, fileExtent{offset: entry.Offset, length: uint64(len(entry.Data))})
		}
	}
	return d, nil
}

func imageTag(tag uint16) bool {
	switch tag {
	case 0xfe, 0x100, 0x101, 0x102, 0x103, 0x106, 0x10f, 0x110, 0x111, 0x112,
		0x115, 0x116, 0x117, 0x131, 0x14a, 0x201, 0x202, 0x8769, 0x142, 0x143, 0x144, 0x145,
		0x10a, 0x11c, 0x13d, 0x153, 0x80e5:
		return true
	}
	return false
}

func (p *parser) imagesAt(offset, depth int64) error {
	for offset != 0 {
		d, err := p.directory(offset, 0, depth, true, p.reader, imageTag)
		if err != nil {
			return err
		}
		p.images = append(p.images, d)
		if err := p.tiffPreview(d); err != nil {
			return err
		}
		if subs, ok := d.entries[0x14a]; ok {
			if subs.Type != tiff.Long && subs.Type != tiff.IFDType {
				return badEntry(subs, "SubIFDs must contain LONG/IFD offsets")
			}
			for i := uint32(0); i < subs.Count; i++ {
				sub, err := subs.Uint(i)
				if err != nil {
					return err
				}
				if sub == 0 {
					return badEntry(subs, "null SubIFD reference")
				}
				if err := p.imagesAt(int64(sub), depth+1); err != nil {
					return err
				}
			}
		}
		offset = int64(d.next)
	}
	return nil
}

func rawCandidate(d directory) (bool, error) {
	for _, tag := range []uint16{0x100, 0x101, 0x102, 0x103, 0x106, 0x115} {
		if _, ok := d.entries[tag]; !ok {
			return false, nil
		}
	}
	photo, err := scalar(d.entries[0x106], tiff.Short)
	if err != nil {
		return false, err
	}
	samples, err := scalar(d.entries[0x115], tiff.Short)
	if err != nil {
		return false, err
	}
	compression, err := scalar(d.entries[0x103], tiff.Short)
	if err != nil {
		return false, err
	}
	if reduced, ok := d.entries[0xfe]; ok {
		value, err := scalar(reduced, tiff.Long)
		if err != nil {
			return false, err
		}
		if value != 0 {
			return false, nil
		}
	}
	return samples == 1 && (photo == 1 || photo == 32803) && compression != 6, nil
}

func (p *parser) imageMetadata(d directory) error {
	m := &p.metadata
	for _, field := range []struct {
		tag uint16
		dst *string
	}{{0x10f, &m.Make}, {0x110, &m.Model}} {
		if entry, ok := d.entries[field.tag]; ok {
			text, err := p.text(entry)
			if err != nil {
				return err
			}
			*field.dst = text
		}
	}
	var err error
	m.NormalizedModel, err = p.trimText(m.Model)
	if err != nil {
		return err
	}
	makeName, err := p.trimText(m.Make)
	if err != nil {
		return err
	}
	if makeName == "OLYMPUS CORPORATION" || makeName == "OLYMPUS IMAGING CORP." {
		m.NormalizedMake = "Olympus"
	} else if makeName == "OM Digital Solutions" || makeName == "OM SYSTEM" {
		m.NormalizedMake = "OM System"
	} else {
		m.NormalizedMake = makeName
	}
	if e, ok := d.entries[0x131]; ok {
		value, err := p.text(e)
		if err != nil {
			return err
		}
		m.Software = fileValue(value, "IFD/0x0131")
	}
	width, err := scalar(d.entries[0x100], tiff.Long, tiff.Short)
	if err != nil {
		return err
	}
	height, err := scalar(d.entries[0x101], tiff.Long, tiff.Short)
	if err != nil {
		return err
	}
	if err := p.dimensions(width, height); err != nil {
		return err
	}
	m.Geometry = Geometry{StoredWidth: int(width), StoredHeight: int(height), Width: int(width), Height: int(height)}
	if e, ok := d.entries[0x112]; ok {
		value, err := scalar(e, tiff.Short)
		if err != nil {
			return err
		}
		if value < 1 || value > 8 {
			return badEntry(e, "orientation outside EXIF 1..8")
		}
		m.Orientation = fileValue(Orientation(value), "IFD/0x0112")
	}
	return nil
}

func (p *parser) dimensions(width, height uint64) error {
	if width == 0 || height == 0 {
		return corrupt("geometry", "zero sensor dimension")
	}
	pixels, err := bounded.Mul(width, height)
	if err != nil {
		return err
	}
	if width > uint64(p.limits.Dimension) || height > uint64(p.limits.Dimension) || pixels > uint64(p.limits.Pixels) {
		return &Error{Op: "geometry", Kind: ErrLimitExceeded, Detail: "dimensions or pixel count exceed quota"}
	}
	if _, err := bounded.Int(pixels); err != nil {
		return err
	}
	return nil
}

func (p *parser) text(e tiff.Entry) (string, error) {
	if err := p.reader.Reserve(uint64(len(e.Data))); err != nil {
		return "", err
	}
	return e.Text()
}

func (p *parser) trimText(text string) (string, error) {
	left, right := 0, len(text)
	for left < right && strings.IndexByte(" \t\n\v\f\r", text[left]) >= 0 {
		if left&4095 == 0 {
			if err := p.source.Check(); err != nil {
				return "", err
			}
		}
		left++
	}
	for right > left && strings.IndexByte(" \t\n\v\f\r", text[right-1]) >= 0 {
		if right&4095 == 0 {
			if err := p.source.Check(); err != nil {
				return "", err
			}
		}
		right--
	}
	return text[left:right], p.source.Check()
}

func (p *parser) exifAt(offset, depth int64) error {
	d, err := p.directory(offset, 0, depth, true, p.reader, func(tag uint16) bool {
		switch tag {
		case 0x829a, 0x829d, 0x8827, 0x920a, 0xa302:
			return true
		}
		return false
	})
	if err != nil {
		return err
	}
	if d.next != 0 {
		return corrupt("EXIF", "unexpected next-IFD link")
	}
	for _, field := range []struct {
		tag uint16
		dst *Value[float64]
	}{{0x829a, &p.metadata.ExposureTime}, {0x829d, &p.metadata.FNumber}, {0x920a, &p.metadata.FocalLength}} {
		if e, ok := d.entries[field.tag]; ok {
			if e.Type != tiff.Rational || e.Count != 1 {
				return badEntry(e, "expected one unsigned rational")
			}
			value, err := e.Real(0)
			if err != nil {
				return err
			}
			if value <= 0 {
				return badEntry(e, "exposure, aperture, or focal length must be positive")
			}
			*field.dst = fileValue(value, fmt.Sprintf("EXIF/0x%04x", field.tag))
		}
	}
	if e, ok := d.entries[0x8827]; ok {
		value, err := scalar(e, tiff.Short)
		if err != nil {
			return err
		}
		p.metadata.ISO = fileValue(float64(value), "EXIF/0x8827")
	}
	if e, ok := d.entries[0xa302]; ok {
		if e.Type != tiff.Undefined || e.Count != 8 || len(e.Data) != 8 {
			return badEntry(e, "expected 2x2 EXIF CFA encoding")
		}
		if binary.LittleEndian.Uint16(e.Data) != 2 || binary.LittleEndian.Uint16(e.Data[2:]) != 2 {
			if err := p.diagnostic("unsupported-cfa", "EXIF CFA is not 2x2 Bayer", "EXIF/0xa302"); err != nil {
				return err
			}
		} else {
			var cfa CFA
			greens := 0
			counts := [3]int{}
			valid := true
			for i, value := range e.Data[4:] {
				if value > 2 {
					valid = false
					continue
				}
				counts[value]++
				color := Red
				switch value {
				case 1:
					color = Green1
					if greens != 0 {
						color = Green2
					}
					greens++
				case 2:
					color = Blue
				}
				cfa[i/2][i%2] = color
			}
			greenDiagonal := (e.Data[4] == 1 && e.Data[7] == 1) || (e.Data[5] == 1 && e.Data[6] == 1)
			if valid && counts == [3]int{1, 2, 1} && greenDiagonal {
				p.metadata.CFA = fileValue(cfa, "EXIF/0xa302")
			} else {
				if err := p.diagnostic("unsupported-cfa", "EXIF CFA is not regular Bayer", "EXIF/0xa302"); err != nil {
					return err
				}
			}
		}
	}
	if maker, ok := d.entries[0x927c]; ok {
		if maker.Type != tiff.Undefined {
			return badEntry(maker, "MakerNote must be UNDEFINED bytes")
		}
		return p.makerAt(maker, depth+1)
	}
	return nil
}

func fileValue[T any](value T, source string) Value[T] {
	return Value[T]{Value: value, Origin: OriginFile, Source: source}
}

func corrupt(op, detail string) error { return &Error{Op: op, Kind: ErrCorrupt, Detail: detail} }

func badEntry(e tiff.Entry, detail string) error {
	tag, offset := e.Tag, e.Offset
	return &Error{Op: "metadata", Kind: ErrCorrupt, Tag: &tag, Offset: &offset, Detail: detail}
}

func scalar(e tiff.Entry, types ...tiff.Type) (uint64, error) {
	if e.Count != 1 {
		return 0, badEntry(e, "expected one scalar")
	}
	for _, typ := range types {
		if e.Type == typ {
			return e.Uint(0)
		}
	}
	return 0, badEntry(e, "unexpected scalar type")
}

func numbers(e tiff.Entry, typ tiff.Type, count uint32) ([]uint64, error) {
	if e.Type != typ || e.Count != count {
		return nil, badEntry(e, "unexpected numeric array type/count")
	}
	values := make([]uint64, count)
	for i := range values {
		value, err := e.Uint(uint32(i))
		if err != nil {
			return nil, err
		}
		values[i] = value
	}
	return values, nil
}

func (p *parser) diagnostic(code, message, source string) error {
	// Double the element allowance covers append capacity growth and both
	// old/new arrays during replacement. Strings may be parser-generated.
	if err := p.reader.Reserve(192 + uint64(len(code)) + uint64(len(message)) + uint64(len(source))); err != nil {
		return err
	}
	p.metadata.Diagnostics = append(p.metadata.Diagnostics, Diagnostic{Code: code, Message: message, Source: source})
	return nil
}

func (p *parser) finishCalibration() error {
	m := &p.metadata
	if p.validBits != 0 {
		if p.validBits > 16 {
			return corrupt("valid bits", "sensor depth exceeds uint16 storage")
		}
		m.BitDepth = fileValue(uint8(p.validBits), "Olympus/ValidBits")
	}
	if p.cropFields != 0 && p.cropFields != 4 {
		return corrupt("crop", "incomplete Olympus crop rectangle")
	}
	if p.cropFields == 4 {
		left, top, width, height := p.crop[0], p.crop[1], p.crop[2], p.crop[3]
		if width == 0 || height == 0 || left+width > uint64(m.Geometry.Width) || top+height > uint64(m.Geometry.Height) {
			return corrupt("crop", "active rectangle outside decoded geometry")
		}
		m.Geometry.ActiveArea = fileValue(image.Rect(int(left), int(top), int(left+width), int(top+height)), "Olympus/ImageProcessing/0x0612..0x0615")
	}
	if m.CFA.Present() {
		var black, wb Levels
		for y := range 2 {
			for x := range 2 {
				switch m.CFA.Value[y][x] {
				case Red:
					black[y][x], wb[y][x] = float64(p.black[0]), float64(p.wb[0])/256
				case Green1:
					black[y][x], wb[y][x] = float64(p.black[1]), 1
				case Green2:
					black[y][x], wb[y][x] = float64(p.black[2]), 1
				case Blue:
					black[y][x], wb[y][x] = float64(p.black[3]), float64(p.wb[1])/256
				}
			}
		}
		if p.hasBlack {
			if p.validBits != 0 {
				maximum := uint64(1)<<p.validBits - 1
				for _, value := range p.black {
					if value > maximum {
						return corrupt("black level", "calibration exceeds the sensor-code domain")
					}
				}
			}
			m.Calibration.Black = fileValue(black, "Olympus/BlackLevel (R,G1,G2,B)")
		}
		if p.hasWB {
			if p.wb[0] == 0 || p.wb[1] == 0 {
				return corrupt("white balance", "zero red/blue gain")
			}
			m.Calibration.WhiteBalance = fileValue(wb, "Olympus/WB_RBLevels / 256")
		}
	}
	if p.cameraID != "" {
		m.CameraID = fileValue(p.cameraID, "Olympus/Equipment/0x0100")
	}
	if p.mode != "" {
		m.CaptureMode = fileValue(p.mode, "Olympus/SpecialMode;CameraSettings/DriveMode")
	}
	return nil
}
