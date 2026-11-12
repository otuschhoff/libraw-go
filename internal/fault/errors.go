// SPDX-License-Identifier: LGPL-2.1-only

package fault

import (
	"errors"
	"fmt"
)

var (
	NotORF      = errors.New("not an ORF container")
	Unsupported = errors.New("unsupported layout or mode")
	Corrupt     = errors.New("corrupt input")
	Truncated   = errors.New("truncated input")
	Limit       = errors.New("resource limit exceeded")
	Calibration = errors.New("required calibration unavailable")
	Options     = errors.New("invalid options")
	NoPreview   = errors.New("no supported embedded JPEG preview")
)

// Error preserves both classification and the underlying cause. Location is
// optional: use nil rather than offset zero to indicate an unknown location.
type Error struct {
	Op     string
	Kind   error
	Cause  error
	Offset *int64
	Bit    *uint8
	Tag    *uint16
	IFD    *int64
	Layout string
	Detail string
}

func (e *Error) Error() string {
	text := e.Op
	if e.Layout != "" {
		text += " layout=" + e.Layout
	}
	if e.IFD != nil {
		text += fmt.Sprintf(" IFD=%d", *e.IFD)
	}
	if e.Tag != nil {
		text += fmt.Sprintf(" tag=0x%04x", *e.Tag)
	}
	if e.Offset != nil {
		text += fmt.Sprintf(" offset=%d", *e.Offset)
	}
	if e.Bit != nil {
		text += fmt.Sprintf(" bit=%d", *e.Bit)
	}
	if e.Kind != nil {
		text += ": " + e.Kind.Error()
	}
	if e.Detail != "" {
		text += ": " + e.Detail
	}
	if e.Cause != nil {
		text += ": " + e.Cause.Error()
	}
	return text
}

func (e *Error) Unwrap() []error {
	var causes []error
	if e.Kind != nil {
		causes = append(causes, e.Kind)
	}
	if e.Cause != nil {
		causes = append(causes, e.Cause)
	}
	return causes
}
