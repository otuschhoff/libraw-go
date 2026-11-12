// SPDX-License-Identifier: LGPL-2.1-only

package libraw

import (
	"context"
	"errors"
	"image"
	"image/jpeg"
	"io"

	"github.com/otuschhoff/libraw-go/internal/bounded"
	"github.com/otuschhoff/libraw-go/internal/tiff"
)

const jpegScratchBytes = 64 << 10

type previewPlan struct {
	offset        int64
	length        uint64
	width, height int
	orientation   Value[Orientation]
	source        string
}

func (p *parser) tiffPreview(d directory) error {
	start, hasStart := d.entries[0x201]
	length, hasLength := d.entries[0x202]
	if !hasStart && !hasLength {
		return nil
	}
	if !hasStart || !hasLength {
		return corrupt("TIFF preview", "incomplete JPEG offset/length pair")
	}
	offset, err := scalar(start, tiff.Long)
	if err != nil {
		return err
	}
	count, err := scalar(length, tiff.Long)
	if err != nil {
		return err
	}
	var orientation Value[Orientation]
	if e, ok := d.entries[0x112]; ok {
		value, err := scalar(e, tiff.Short)
		if err != nil {
			return err
		}
		if value < 1 || value > 8 {
			return badEntry(e, "preview orientation outside EXIF 1..8")
		}
		orientation = fileValue(Orientation(value), "preview IFD/0x0112")
	}
	return p.addPreview(int64(offset), count, orientation, "TIFF/JPEGInterchangeFormat")
}

func (p *parser) addPreview(offset int64, length uint64, orientation Value[Orientation], source string) error {
	if length < 4 || offset < 8 {
		return corrupt("preview extent", "empty/short JPEG or offset overlaps ORF header")
	}
	if err := p.source.Extent(offset, length); err != nil {
		return err
	}
	if err := p.reader.Reserve(256); err != nil {
		return err
	}
	p.previews = append(p.previews, previewPlan{offset: offset, length: length, orientation: orientation, source: source})
	return nil
}

func (p *parser) selectPreview() (*previewPlan, error) {
	var best *previewPlan
	for _, candidate := range p.previews {
		if err := p.source.Check(); err != nil {
			return nil, err
		}
		used := uint64(p.limits.MetadataBytes) - p.reader.Remaining()
		if candidate.length > uint64(p.limits.PreviewBytes) || candidate.length+64 > uint64(p.limits.WorkingBytes) || used+jpegScratchBytes > uint64(p.limits.WorkingBytes) {
			if err := p.diagnostic("preview-limit", "embedded JPEG exceeds preview or header-working quota", candidate.source); err != nil {
				return nil, err
			}
			continue
		}
		if _, err := bounded.Int(candidate.length); err != nil {
			if err := p.diagnostic("preview-limit", "embedded JPEG exceeds platform allocation size", candidate.source); err != nil {
				return nil, err
			}
			continue
		}
		var markers [4]byte
		if err := p.source.ReadAt(markers[:2], candidate.offset); err != nil {
			return nil, err
		}
		if err := p.source.ReadAt(markers[2:], candidate.offset+int64(candidate.length)-2); err != nil {
			return nil, err
		}
		if markers != [4]byte{0xff, 0xd8, 0xff, 0xd9} {
			if err := p.diagnostic("invalid-preview", "embedded JPEG lacks SOI/terminal EOI markers", candidate.source); err != nil {
				return nil, err
			}
			continue
		}
		config, err := p.previewConfig(candidate)
		if err != nil {
			var format jpeg.FormatError
			var unsupported jpeg.UnsupportedError
			if errors.As(err, &format) || errors.As(err, &unsupported) || err == io.EOF || err == io.ErrUnexpectedEOF {
				if err := p.diagnostic("invalid-preview", err.Error(), candidate.source); err != nil {
					return nil, err
				}
				continue
			}
			return nil, &Error{Op: "preview header", Cause: err}
		}
		if config.Width <= 0 || config.Height <= 0 ||
			int64(config.Width) > p.limits.Dimension || int64(config.Height) > p.limits.Dimension ||
			uint64(config.Width)*uint64(config.Height) > uint64(p.limits.Pixels) {
			if err := p.diagnostic("preview-limit", "embedded JPEG dimensions exceed quota", candidate.source); err != nil {
				return nil, err
			}
			continue
		}
		candidate.width, candidate.height = config.Width, config.Height
		area := uint64(candidate.width) * uint64(candidate.height)
		if best == nil || area > uint64(best.width)*uint64(best.height) ||
			(area == uint64(best.width)*uint64(best.height) && (candidate.length > best.length ||
				(candidate.length == best.length && candidate.offset < best.offset))) {
			copy := candidate
			best = &copy
		}
	}
	return best, nil
}

func (p *parser) previewConfig(candidate previewPlan) (image.Config, error) {
	// DecodeConfig uses fixed-size standard-library JPEG state, not a decoded
	// pixel plane. Keep its conservative allowance live alongside metadata.
	if err := p.reader.AccountReserve(jpegScratchBytes); err != nil {
		return image.Config{}, err
	}
	defer p.reader.AccountRelease(jpegScratchBytes)
	reader := &extentStream{file: &File{source: p.source}, ctx: p.source.Context(), pos: candidate.offset, end: candidate.offset + int64(candidate.length)}
	return jpeg.DecodeConfig(reader)
}

// Preview copies the largest supported JPEG (pixel area, then byte length, then
// lowest file offset). It never decodes/re-encodes pixels or reads the raw plane.
// Validation covers bounded extents, SOI/EOI and a supported JPEG frame header;
// entropy-coded pixels are not decoded or certified. Missing/invalid/over-limit
// candidates return ErrNoPreview. Cancellation and reader failures are preserved.
func (f *File) Preview(ctx context.Context) (*Preview, error) {
	if err := f.operation(ctx); err != nil {
		return nil, err
	}
	if f.preview == nil {
		return nil, &Error{Op: "preview", Kind: ErrNoPreview, Detail: f.caps.Preview.Reason}
	}
	plan := f.preview
	if plan.length > uint64(f.limits.PreviewBytes) {
		return nil, &Error{Op: "preview", Kind: ErrLimitExceeded, Detail: "preview exceeds operation buffer quota"}
	}
	count, err := bounded.Int(plan.length)
	if err != nil {
		return nil, err
	}
	_, account, err := operationAccount(f.budget, f.limits.WorkingBytes)
	if err != nil {
		return nil, err
	}
	diagnosticBytes := uint64(0)
	if !plan.orientation.Present() {
		diagnosticBytes = 64
	}
	if err := account.Reserve(plan.length + diagnosticBytes); err != nil {
		return nil, err
	}
	success := false
	defer func() {
		if !success {
			account.Close()
		}
	}()
	if err := ctx.Err(); err != nil {
		return nil, &Error{Op: "preview", Cause: err}
	}
	data := make([]byte, count)
	stream := &extentStream{file: f, ctx: ctx, pos: plan.offset, end: plan.offset + int64(plan.length)}
	for position := 0; position < len(data); {
		end := min(position+(32<<10), len(data))
		n, err := stream.Read(data[position:end])
		if err != nil {
			return nil, &Error{Op: "preview", Cause: err}
		}
		position += n
	}
	if err := ctx.Err(); err != nil {
		return nil, &Error{Op: "preview", Cause: err}
	}
	result := &Preview{account: account, JPEG: data, Width: plan.width, Height: plan.height, Orientation: plan.orientation}
	if !plan.orientation.Present() {
		result.Diagnostics = []Diagnostic{{Code: "preview-orientation-unavailable", Message: "no verified transform for embedded JPEG pixels; sensor orientation is not assumed", Source: plan.source}}
	}
	result.owner = f.owner
	result.owner.retain()
	success = true
	return result, nil
}

type extentStream struct {
	file     *File
	ctx      context.Context
	pos, end int64
}

func (r *extentStream) Read(data []byte) (int, error) {
	if r.ctx != nil {
		if err := r.ctx.Err(); err != nil {
			return 0, err
		}
	}
	if len(data) == 0 {
		return 0, nil
	}
	if r.pos == r.end {
		return 0, io.EOF
	}
	data = data[:min(int64(len(data)), r.end-r.pos)]
	source := r.file.source
	if r.ctx != nil {
		source = source.WithContext(r.ctx)
	}
	if err := source.ReadAt(data, r.pos); err != nil {
		return 0, err
	}
	r.pos += int64(len(data))
	return len(data), nil
}
