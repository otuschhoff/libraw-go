// SPDX-License-Identifier: LGPL-2.1-only

package libraw

// Limits are per-operation quotas. Budget separately bounds aggregate live
// reservations across handles and concurrent calls. Bytes cover explicit
// buffers and conservative parser allowances, not Go runtime RSS/GC overhead.
// Zero selects the documented default; negative values are invalid.
type Limits struct {
	InputBytes    int64
	Pixels        int64
	Dimension     int64
	IFDs          int64
	Depth         int64
	EntriesPerIFD int64
	MetadataBytes int64
	PreviewBytes  int64
	WorkingBytes  int64
}

// DefaultLimits returns an independent value with the design's resource defaults.
func DefaultLimits() Limits {
	return Limits{
		InputBytes: 1 << 30, Pixels: 100_000_000, Dimension: 32768,
		IFDs: 128, Depth: 16, EntriesPerIFD: 4096,
		MetadataBytes: 16 << 20, PreviewBytes: 64 << 20, WorkingBytes: 1 << 30,
	}
}

type OpenOptions struct {
	Limits Limits
	// Budget optionally shares aggregate accounting across pipelines. If nil,
	// Open creates a budget of WorkingBytes, inherited by its RAW/preview
	// results and by Render unless RenderOptions supplies another budget.
	Budget *Budget
}

// Resolve validates limits and replaces zero-valued limits with defaults.
// Depth cannot exceed the total IFD quota; the per-operation metadata/preview
// budgets cannot exceed the working-buffer budget. Larger positive limits are
// allowed, but do not disable arithmetic or platform-allocation checks.
func (o OpenOptions) Resolve() (Limits, error) {
	limits := o.Limits
	defaults := DefaultLimits()
	fields := []struct {
		name string
		dst  *int64
		def  int64
	}{
		{"input bytes", &limits.InputBytes, defaults.InputBytes},
		{"pixels", &limits.Pixels, defaults.Pixels},
		{"dimension", &limits.Dimension, defaults.Dimension},
		{"IFDs", &limits.IFDs, defaults.IFDs},
		{"depth", &limits.Depth, defaults.Depth},
		{"entries per IFD", &limits.EntriesPerIFD, defaults.EntriesPerIFD},
		{"metadata bytes", &limits.MetadataBytes, defaults.MetadataBytes},
		{"preview bytes", &limits.PreviewBytes, defaults.PreviewBytes},
		{"working bytes", &limits.WorkingBytes, defaults.WorkingBytes},
	}
	for _, field := range fields {
		if *field.dst < 0 {
			return Limits{}, &Error{Op: "options", Kind: ErrInvalidOptions, Detail: field.name + " must not be negative"}
		}
		if *field.dst == 0 {
			*field.dst = field.def
		}
	}
	if limits.Depth > limits.IFDs {
		return Limits{}, &Error{Op: "options", Kind: ErrInvalidOptions, Detail: "depth exceeds total IFD quota"}
	}
	if limits.MetadataBytes > limits.WorkingBytes || limits.PreviewBytes > limits.WorkingBytes {
		return Limits{}, &Error{Op: "options", Kind: ErrInvalidOptions, Detail: "metadata or preview budget exceeds working bytes"}
	}
	return limits, nil
}
