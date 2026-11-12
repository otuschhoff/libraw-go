// SPDX-License-Identifier: LGPL-2.1-only

package libraw

import (
	"context"
	"io"

	"github.com/otuschhoff/libraw-go/internal/bounded"
)

// File borrows an immutable ReaderAt; the caller must keep it open and unchanged.
// Metadata and plans are immutable. Concurrent operations are supported when
// the supplied ReaderAt permits concurrent reads. There is no Close method.
type File struct {
	source   *bounded.Reader
	limits   Limits
	metadata Metadata
	caps     Capabilities
	plan     decodePlan
	preview  *previewPlan
}

// Open recognizes evidenced ORF framing, parses bounded metadata, and identifies
// candidate payload layouts without allocating or decoding the raw plane or
// preview pixels. JPEG headers are inspected to select an embedded preview.
// Recognized but unsupported raw layouts may still supply metadata and previews.
func Open(input io.ReaderAt, size int64, opts OpenOptions) (*File, error) {
	limits, err := opts.Resolve()
	if err != nil {
		return nil, err
	}
	source, err := bounded.New(input, size, limits.InputBytes)
	if err != nil {
		return nil, err
	}
	return openORF(source, limits)
}

// Metadata returns an independent snapshot, preserving original camera strings.
func (f *File) Metadata() Metadata { return f.metadata.Clone() }

// Capabilities distinguishes working operations from identified future layouts.
func (f *File) Capabilities() Capabilities { return f.caps }

// DecodeRaw returns caller-owned, unprocessed sensor codes for an evidenced
// word or legacy predictive layout. It retains all sensor pixels without
// cropping, orientation, black subtraction or scaling. Other layouts return
// ErrUnsupported; an embedded JPEG is never substituted. Cancellation cannot
// interrupt a blocked caller-supplied ReaderAt, but is checked between rows and
// before success.
func (f *File) DecodeRaw(ctx context.Context) (*RawImage, error) {
	if err := f.operation(ctx); err != nil {
		return nil, err
	}
	switch f.plan.layout {
	case "olympus-word16le-high12":
		return f.decodeWord(ctx)
	case "olympus-legacy-predictive12":
		return f.decodeLegacy(ctx)
	default:
		return nil, &Error{Op: "decode raw", Kind: ErrUnsupported, Layout: f.plan.layout, Detail: f.caps.Raw.Reason}
	}
}

func (f *File) operation(ctx context.Context) error {
	if f == nil || f.source == nil || ctx == nil {
		return &Error{Op: "file operation", Kind: ErrInvalidOptions, Detail: "unopened File or nil context"}
	}
	if err := ctx.Err(); err != nil {
		return &Error{Op: "file operation", Cause: err}
	}
	return nil
}
