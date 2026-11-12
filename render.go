// SPDX-License-Identifier: LGPL-2.1-only

package libraw

import (
	"context"
	"encoding/binary"
	"image"
	"math"

	"github.com/otuschhoff/libraw-go/internal/bounded"
)

// RenderOptions has deterministic zero defaults: file calibration, verified
// active crop, as-shot WB, zero EV, sRGB and applied EXIF orientation. Present
// Calibration fields explicitly replace corresponding RAW calibration fields.
// ActiveArea's zero rectangle uses raw.ActiveArea; any other value is an
// explicit sensor-coordinate crop. Limits resolve as for OpenOptions.
type RenderOptions struct {
	Limits              Limits
	Budget              *Budget
	Calibration         Calibration
	ActiveArea          image.Rectangle
	ExposureEV          float64
	PreserveOrientation bool
	AllowEstimatedWhite bool
}

type renderPlan struct {
	crop                       image.Rectangle
	width, height, outputBytes int
	workingBytes               uint64
	orientation                Orientation
	black, scale               Levels
	color                      matrix3
	exposure                   float64
	maximum                    uint16
}

// Render develops a regular Bayer plane into opaque, caller-owned sRGB.
// It never changes or retains raw, its samples, or options. The caller must
// not modify or close them during rendering. Working storage is output plus
// three normalized Bayer rows. A library-owned RAW plane remains charged to
// its inherited Budget until closed; caller-constructed RAW storage is excluded.
// Crops narrower or shorter than two pixels return ErrUnsupported rather than
// inventing missing channels. Missing crop/calibration/orientation is explicit.
func Render(ctx context.Context, raw *RawImage, opts RenderOptions) (*RenderedImage, error) {
	if ctx == nil || raw == nil {
		return nil, &Error{Op: "render", Kind: ErrInvalidOptions, Detail: "nil context or RAW image"}
	}
	if err := ctx.Err(); err != nil {
		return nil, &Error{Op: "render", Cause: err}
	}
	plan, err := makeRenderPlan(raw, opts, true)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, &Error{Op: "render", Cause: err}
	}
	limits, err := (OpenOptions{Limits: opts.Limits}).Resolve()
	if err != nil {
		return nil, err
	}
	budget := opts.Budget
	if budget == nil {
		budget = raw.budget
	}
	_, account, err := operationAccount(budget, limits.WorkingBytes)
	if err != nil {
		return nil, err
	}
	if err := account.Reserve(plan.workingBytes); err != nil {
		return nil, err
	}
	success := false
	defer func() {
		if !success {
			account.Close()
		}
	}()
	if err := ctx.Err(); err != nil {
		return nil, &Error{Op: "render", Cause: err}
	}
	output := &image.NRGBA64{Pix: make([]byte, plan.outputBytes), Stride: plan.width * 8, Rect: image.Rect(0, 0, plan.width, plan.height)}
	width, height := plan.crop.Dx(), plan.crop.Dy()
	rows := make([]float64, 3*width)
	cached := [3]int{-1, -1, -1}
	for y := range height {
		if err := ctx.Err(); err != nil {
			return nil, &Error{Op: "render", Cause: err}
		}
		for row := max(0, y-1); row <= min(height-1, y+1); row++ {
			slot := row % 3
			if cached[slot] == row {
				continue
			}
			sensorY := plan.crop.Min.Y + row
			for x := range width {
				if x&255 == 0 {
					if err := ctx.Err(); err != nil {
						return nil, &Error{Op: "render", Cause: err}
					}
				}
				sensorX := plan.crop.Min.X + x
				sample := raw.Pixels[sensorY*raw.Stride+sensorX]
				if sample > plan.maximum {
					return nil, &Error{Op: "render", Kind: ErrCorrupt, Detail: "active sensor sample exceeds bit depth"}
				}
				value := max(0, float64(sample)-plan.black[sensorY&1][sensorX&1]) * plan.scale[sensorY&1][sensorX&1]
				if !finite(value) {
					return nil, &Error{Op: "render", Kind: ErrCalibrationUnavailable, Detail: "normalization exceeds finite range"}
				}
				rows[slot*width+x] = value
			}
			cached[slot] = row
		}
		for x := range width {
			if x&255 == 0 {
				if err := ctx.Err(); err != nil {
					return nil, &Error{Op: "render", Cause: err}
				}
			}
			camera := demosaic(rows, width, height, x, y, plan.crop.Min, raw.CFA)
			linear := transform3(plan.color, camera)
			dx, dy := orientPixel(x, y, width, height, plan.orientation)
			pixel := output.Pix[dy*output.Stride+dx*8 : dy*output.Stride+dx*8+8]
			for c := range 3 {
				value := linear[c] * plan.exposure
				if !finite(value) {
					return nil, &Error{Op: "render", Kind: ErrInvalidOptions, Detail: "color/exposure exceeds finite range"}
				}
				binary.BigEndian.PutUint16(pixel[c*2:], encodeSRGB(value))
			}
			binary.BigEndian.PutUint16(pixel[6:], 65535)
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, &Error{Op: "render", Cause: err}
	}
	account.Release(plan.workingBytes - uint64(plan.outputBytes))
	success = true
	return &RenderedImage{NRGBA64: output, account: account}, nil
}

func makeRenderPlan(raw *RawImage, opts RenderOptions, pixels bool) (renderPlan, error) {
	var plan renderPlan
	fail := func(kind error, detail string) (renderPlan, error) {
		return renderPlan{}, &Error{Op: "render", Kind: kind, Detail: detail}
	}
	limits, err := (OpenOptions{Limits: opts.Limits}).Resolve()
	if err != nil {
		return plan, err
	}
	if raw.Width <= 0 || raw.Height <= 0 || raw.Stride < raw.Width || raw.BitDepth == 0 || raw.BitDepth > 16 {
		return fail(ErrCorrupt, "invalid RAW geometry, stride or bit depth")
	}
	sensorPixels, err := bounded.Mul(uint64(raw.Width), uint64(raw.Height))
	if err != nil {
		return plan, err
	}
	if int64(raw.Width) > limits.Dimension || int64(raw.Height) > limits.Dimension ||
		sensorPixels > uint64(limits.Pixels) {
		return fail(ErrLimitExceeded, "RAW dimensions/pixels exceed render limits")
	}
	count, err := bounded.Mul(uint64(raw.Stride), uint64(raw.Height))
	if err != nil {
		return plan, err
	}
	if pixels && count != uint64(len(raw.Pixels)) {
		return fail(ErrCorrupt, "RAW sample slice does not match stride and height")
	}
	if !regularBayer(raw.CFA) {
		return fail(ErrUnsupported, "regular Bayer CFA with distinct green positions required")
	}
	plan.crop = raw.ActiveArea
	if opts.ActiveArea != (image.Rectangle{}) {
		plan.crop = opts.ActiveArea
		if plan.crop.Empty() || !plan.crop.In(image.Rect(0, 0, raw.Width, raw.Height)) {
			return fail(ErrInvalidOptions, "override crop outside RAW geometry or empty")
		}
	}
	if plan.crop.Empty() {
		return fail(ErrCalibrationUnavailable, "verified active crop or explicit crop required")
	}
	if !plan.crop.In(image.Rect(0, 0, raw.Width, raw.Height)) {
		return fail(ErrCorrupt, "RAW active crop outside sensor geometry")
	}
	if plan.crop.Dx() < 2 || plan.crop.Dy() < 2 {
		return fail(ErrUnsupported, "crop must contain at least two rows and columns")
	}
	plan.orientation = OrientationTopLeft
	if !opts.PreserveOrientation {
		if raw.Orientation == OrientationUnknown {
			return fail(ErrCalibrationUnavailable, "orientation unavailable; explicitly preserve sensor orientation")
		}
		if raw.Orientation > OrientationLeftBottom {
			return fail(ErrCorrupt, "invalid EXIF orientation")
		}
		plan.orientation = raw.Orientation
	}
	if !finite(opts.ExposureEV) {
		return fail(ErrInvalidOptions, "exposure must be finite")
	}
	plan.exposure = math.Exp2(opts.ExposureEV)
	if !finite(plan.exposure) || plan.exposure == 0 {
		return fail(ErrInvalidOptions, "exposure gain must be finite and positive")
	}
	calibration := raw.Calibration
	overrides := opts.Calibration
	for _, pair := range []struct {
		dst *Value[Levels]
		src Value[Levels]
	}{{&calibration.Black, overrides.Black}, {&calibration.White, overrides.White}, {&calibration.WhiteBalance, overrides.WhiteBalance}} {
		if pair.src.Present() {
			if !validLevels(pair.src.Value) || pair.src.Origin > OriginOverride {
				return fail(ErrInvalidOptions, "nonfinite or invalid calibration override")
			}
			*pair.dst = pair.src
			pair.dst.Origin = OriginOverride
		}
	}
	if overrides.Color.Present() {
		if _, ok := renderColorMatrix(overrides.Color.Value); !ok || overrides.Color.Origin > OriginOverride {
			return fail(ErrInvalidOptions, "invalid color transform override")
		}
		calibration.Color = overrides.Color
		calibration.Color.Origin = OriginOverride
	}
	if !calibration.Black.Present() || !calibration.White.Present() ||
		!calibration.WhiteBalance.Present() || !calibration.Color.Present() {
		return fail(ErrCalibrationUnavailable, "black, white, as-shot WB and camera-to-XYZ calibration required")
	}
	if calibration.White.Origin == OriginEstimate && !opts.AllowEstimatedWhite {
		return fail(ErrCalibrationUnavailable, "estimated white requires explicit consent or override")
	}
	if calibration.Black.Origin == OriginEstimate || calibration.WhiteBalance.Origin == OriginEstimate || calibration.Color.Origin == OriginEstimate {
		return fail(ErrCalibrationUnavailable, "estimated black, WB or color is not supported")
	}
	kind := ErrCalibrationUnavailable
	if overrides.Black.Present() || overrides.White.Present() || overrides.WhiteBalance.Present() {
		kind = ErrInvalidOptions
	}
	plan.maximum = uint16(uint32(1)<<raw.BitDepth - 1)
	var green float64
	for y := range 2 {
		for x := range 2 {
			if raw.CFA[y][x] == Green1 {
				green = calibration.WhiteBalance.Value[y][x]
			}
		}
	}
	if !finite(green) || green <= 0 {
		return fail(kind, "invalid reference green WB")
	}
	for y := range 2 {
		for x := range 2 {
			black, white, gain := calibration.Black.Value[y][x], calibration.White.Value[y][x], calibration.WhiteBalance.Value[y][x]/green
			if !finite(black) || !finite(white) || !finite(gain) || black < 0 ||
				white <= black || white > float64(plan.maximum) || gain <= 0 {
				return fail(kind, "invalid sensor-domain levels or WB gains")
			}
			plan.black[y][x], plan.scale[y][x] = black, gain/(white-black)
			if !finite(plan.scale[y][x]) || plan.scale[y][x] <= 0 || !finite(float64(plan.maximum)*plan.scale[y][x]) {
				return fail(kind, "normalization exceeds finite range")
			}
		}
	}
	var colorOK bool
	plan.color, colorOK = renderColorMatrix(calibration.Color.Value)
	if !colorOK || calibration.Color.Origin > OriginOverride {
		return fail(ErrCalibrationUnavailable, "invalid camera-to-XYZ calibration")
	}
	for _, origin := range []Origin{calibration.Black.Origin, calibration.White.Origin, calibration.WhiteBalance.Origin} {
		if origin > OriginOverride {
			return fail(ErrCalibrationUnavailable, "invalid calibration provenance")
		}
	}
	plan.width, plan.height = plan.crop.Dx(), plan.crop.Dy()
	if plan.orientation >= OrientationLeftTop {
		plan.width, plan.height = plan.height, plan.width
	}
	outputPixels, err := bounded.Mul(uint64(plan.width), uint64(plan.height))
	if err != nil {
		return plan, err
	}
	output, err := bounded.Mul(outputPixels, 8)
	if err != nil {
		return plan, err
	}
	scratch, err := bounded.Mul(uint64(plan.crop.Dx()), 24)
	if err != nil {
		return plan, err
	}
	plan.workingBytes, err = bounded.Add(output, scratch)
	if err != nil {
		return plan, err
	}
	if plan.workingBytes > uint64(limits.WorkingBytes) {
		return fail(ErrLimitExceeded, "render output and three-row scratch exceed working quota")
	}
	if _, err := bounded.Int(plan.workingBytes); err != nil {
		return plan, err
	}
	plan.outputBytes = int(output)
	return plan, nil
}

func validLevels(levels Levels) bool {
	for _, row := range levels {
		for _, value := range row {
			if !finite(value) {
				return false
			}
		}
	}
	return true
}

func regularBayer(cfa CFA) bool {
	var seen [5]int
	for _, row := range cfa {
		for _, color := range row {
			if color < Red || color > Green2 {
				return false
			}
			seen[color]++
		}
	}
	return seen[Red] == 1 && seen[Blue] == 1 && seen[Green1] == 1 && seen[Green2] == 1 &&
		((cfa[0][0] == Red && cfa[1][1] == Blue) || (cfa[0][0] == Blue && cfa[1][1] == Red) ||
			(cfa[0][1] == Red && cfa[1][0] == Blue) || (cfa[0][1] == Blue && cfa[1][0] == Red))
}

func channel(color Color) int {
	switch color {
	case Red:
		return 0
	case Blue:
		return 2
	default:
		return 1
	}
}

func demosaic(rows []float64, width, height, x, y int, origin image.Point, cfa CFA) [3]float64 {
	known := channel(cfa[(origin.Y+y)&1][(origin.X+x)&1])
	var sum [3]float64
	var count [3]int
	for ny := max(0, y-1); ny <= min(height-1, y+1); ny++ {
		for nx := max(0, x-1); nx <= min(width-1, x+1); nx++ {
			c := channel(cfa[(origin.Y+ny)&1][(origin.X+nx)&1])
			if c != known {
				sum[c] += rows[(ny%3)*width+nx]
				count[c]++
			}
		}
	}
	for c := range 3 {
		if c == known {
			sum[c] = rows[(y%3)*width+x]
		} else {
			sum[c] /= float64(count[c])
		}
	}
	return sum
}

func orientPixel(x, y, width, height int, orientation Orientation) (int, int) {
	switch orientation {
	case OrientationTopRight:
		return width - 1 - x, y
	case OrientationBottomRight:
		return width - 1 - x, height - 1 - y
	case OrientationBottomLeft:
		return x, height - 1 - y
	case OrientationLeftTop:
		return y, x
	case OrientationRightTop:
		return height - 1 - y, x
	case OrientationRightBottom:
		return height - 1 - y, width - 1 - x
	case OrientationLeftBottom:
		return y, width - 1 - x
	default:
		return x, y
	}
}
