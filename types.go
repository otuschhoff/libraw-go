// SPDX-License-Identifier: LGPL-2.1-only

package libraw

import "image"

// Origin identifies the provenance of a present value; zero means unavailable.
type Origin uint8

const (
	OriginUnavailable Origin = iota
	OriginFile
	OriginProfile
	OriginEstimate
	OriginOverride
)

// Value distinguishes an unavailable value from a legitimate zero. Source is a
// tag path, profile identifier, estimate rationale, or caller description.
type Value[T any] struct {
	Value  T
	Origin Origin
	Source string
}

func (v Value[T]) Present() bool { return v.Origin != OriginUnavailable }

// Color preserves the two green positions for calibration.
type Color uint8

const (
	ColorUnknown Color = iota
	Red
	Green1
	Blue
	Green2
)

// CFA is indexed [y parity][x parity], anchored at raw coordinate (0,0).
type CFA [2][2]Color

// Orientation is the original EXIF orientation, including mirrored forms.
type Orientation uint8

const (
	OrientationUnknown Orientation = iota
	OrientationTopLeft
	OrientationTopRight
	OrientationBottomRight
	OrientationBottomLeft
	OrientationLeftTop
	OrientationRightTop
	OrientationRightBottom
	OrientationLeftBottom
)

// Levels contains sensor-code levels indexed by raw CFA position [y][x].
type Levels [2][2]float64

// ColorTransform explicitly describes linear camera RGB to XYZ. ReferenceWhite
// is XYZ normalized to Y=1. Rows are output XYZ; columns are input RGB.
// No MakerNote matrix may be assigned here without establishing its semantics.
type ColorTransform struct {
	CameraToXYZ    [3][3]float64
	ReferenceWhite [3]float64
}

// Calibration values share the raw samples' numeric domain. WhiteBalance is
// indexed by raw CFA position and uses green-reference gain one.
type Calibration struct {
	Black        Value[Levels]
	White        Value[Levels]
	WhiteBalance Value[Levels]
	Color        Value[ColorTransform]
}

// Geometry distinguishes coded payload dimensions from meaningful decoded
// dimensions and the half-open active crop in decoded sensor coordinates.
type Geometry struct {
	StoredWidth, StoredHeight int
	Width, Height             int
	ActiveArea                Value[image.Rectangle]
}

// Diagnostic is an explicitly reported nonfatal limitation or metadata issue.
// Code is a stable machine-readable identifier; Message is human-readable.
type Diagnostic struct {
	Code    string
	Message string
	Source  string
}

// Metadata contains caller-owned information; absent values have zero Origin.
// ExposureTime is seconds, FocalLength is millimeters, and FNumber is unitless.
type Metadata struct {
	Make, Model                     string
	NormalizedMake, NormalizedModel string
	CameraID                        Value[string]
	Firmware                        Value[string]
	Software                        Value[string]
	// Layout identifies an evidenced storage plan, not an available decoder.
	Layout       Value[string]
	CaptureMode  Value[string]
	Geometry     Geometry
	CFA          Value[CFA]
	BitDepth     Value[uint8]
	Orientation  Value[Orientation]
	ExposureTime Value[float64]
	FNumber      Value[float64]
	ISO          Value[float64]
	FocalLength  Value[float64]
	Calibration  Calibration
	Diagnostics  []Diagnostic
}

// Clone returns an independent snapshot, including its diagnostic slice.
func (m Metadata) Clone() Metadata {
	if m.Diagnostics != nil {
		m.Diagnostics = append([]Diagnostic{}, m.Diagnostics...)
	}
	return m
}

// Capability explains availability independently for each operation.
type Capability struct {
	Available bool
	Reason    string
}

type Capabilities struct {
	Metadata Capability
	Preview  Capability
	Raw      Capability
	Render   Capability
}

// RawImage owns unprocessed sensor-code samples; no crop, orientation, black
// subtraction, WB, or scaling has been applied. Initially Stride equals Width
// and len(Pixels) equals Stride*Height. ActiveArea is half-open and CFA is
// anchored to the uncropped plane. Callers may mutate their own results.
type RawImage struct {
	Pixels      []uint16
	Width       int
	Height      int
	Stride      int // samples, not bytes
	ActiveArea  image.Rectangle
	CFA         CFA
	BitDepth    uint8
	Calibration Calibration
	Orientation Orientation
	Diagnostics []Diagnostic
}

// Preview owns original embedded JPEG bytes, not a decoded/re-encoded image.
// Orientation describes the transform needed to display these bytes, not an
// assumption that its encoded pixels share the sensor's orientation.
type Preview struct {
	JPEG        []byte
	Width       int
	Height      int
	Orientation Value[Orientation]
	Diagnostics []Diagnostic
}
