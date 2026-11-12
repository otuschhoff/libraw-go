// SPDX-License-Identifier: LGPL-2.1-only

package libraw

import "github.com/otuschhoff/libraw-go/internal/fault"

// Sentinels classify errors through errors.Is, independently of wrapped I/O
// failures or cancellation. Error supplies additional context through errors.As.
var (
	ErrNotORF                 = fault.NotORF
	ErrUnsupported            = fault.Unsupported
	ErrCorrupt                = fault.Corrupt
	ErrTruncated              = fault.Truncated
	ErrLimitExceeded          = fault.Limit
	ErrCalibrationUnavailable = fault.Calibration
	ErrInvalidOptions         = fault.Options
	ErrNoPreview              = fault.NoPreview
)

// Error identifies an operation, classification, cause, and optional location.
// Offset/IFD are byte offsets, Bit is a zero-based bit position within a byte,
// and Tag is a TIFF tag identifier. Context is present only when known.
type Error = fault.Error
