// SPDX-License-Identifier: LGPL-2.1-only

package libraw

import (
	"context"
	"io"

	"github.com/otuschhoff/libraw-go/internal/bounded"
)

// File borrows an immutable ReaderAt; the caller must keep it open and unchanged.
// Metadata and plans are immutable. Concurrent operations are supported when
// the supplied ReaderAt permits concurrent reads. Close must not overlap any
// use of the File, and never closes the borrowed ReaderAt.
type File struct {
	owner    *metadataOwner
	budget   *Budget
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
func Open(ctx context.Context, input io.ReaderAt, size int64, opts OpenOptions) (*File, error) {
	if ctx == nil {
		return nil, &Error{Op: "open", Kind: ErrInvalidOptions, Detail: "nil context"}
	}
	if err := ctx.Err(); err != nil {
		return nil, &Error{Op: "open", Cause: err}
	}
	limits, err := opts.Resolve()
	if err != nil {
		return nil, err
	}
	source, err := bounded.New(input, size, limits.InputBytes)
	if err != nil {
		return nil, err
	}
	budget, account, err := operationAccount(opts.Budget, limits.WorkingBytes)
	if err != nil {
		return nil, err
	}
	f, err := openORF(source.WithContext(ctx), limits, account)
	if err == nil {
		err = ctx.Err()
	}
	if err != nil {
		account.Close()
		return nil, &Error{Op: "open", Cause: err}
	}
	f.source = source
	f.budget = budget
	f.owner = &metadataOwner{refs: 1, account: account}
	return f, nil
}

// Metadata returns a value whose diagnostic slice is borrowed, read-only and
// valid until File.Close. Use Clone for a caller-owned, unbudgeted snapshot.
func (f *File) Metadata() Metadata { return f.metadata }

// Close invalidates the File and releases its metadata ownership reference.
// Dependent RAW/preview handles retain the conservative parse reservation.
// It does not close the borrowed ReaderAt or outputs. No operation may overlap
// Close. Calling Close more than once is safe.
func (f *File) Close() error {
	if f == nil {
		return nil
	}
	f.source = nil
	f.metadata = Metadata{}
	f.plan = decodePlan{}
	f.preview = nil
	if f.owner != nil {
		f.owner.release()
		f.owner = nil
	}
	return nil
}

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
