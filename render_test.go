// SPDX-License-Identifier: LGPL-2.1-only

package libraw

import (
	"context"
	"errors"
	"image"
	"math"
	"reflect"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
)

func renderRaw(width, height int) *RawImage {
	return &RawImage{
		Width: width, Height: height, Stride: width, Pixels: make([]uint16, width*height),
		ActiveArea: image.Rect(0, 0, width, height), CFA: CFA{{Red, Green1}, {Green2, Blue}},
		BitDepth: 12, Orientation: OrientationTopLeft,
		Calibration: Calibration{
			Black:        Value[Levels]{Origin: OriginFile},
			White:        Value[Levels]{Value: Levels{{1000, 1000}, {1000, 1000}}, Origin: OriginFile},
			WhiteBalance: Value[Levels]{Value: Levels{{1, 1}, {1, 1}}, Origin: OriginFile},
			Color:        Value[ColorTransform]{Value: ColorTransform{CameraToXYZ: [3][3]float64(rgbToXYZ()), ReferenceWhite: d65White()}, Origin: OriginFile},
		},
	}
}

func closeFloat(t *testing.T, got, want float64) {
	t.Helper()
	if !finite(got) || math.Abs(got-want) > 1e-6 {
		t.Fatalf("got %.17g, want %.17g within 1e-6", got, want)
	}
}

func TestColorStageEquations(t *testing.T) {
	a := matrix3{{1, 2, 3}, {0, 1, 4}, {5, 6, 0}}
	inverse, ok := inverse3(a)
	if !ok {
		t.Fatal("invertible matrix rejected")
	}
	want := matrix3{{-24, 18, 5}, {20, -15, -4}, {-5, 4, 1}}
	for i := range 3 {
		for j := range 3 {
			closeFloat(t, inverse[i][j], want[i][j])
		}
	}
	for _, invalid := range []matrix3{{}, {{1, 0, 0}, {0, 1, 0}, {0, 0, 1e-14}}, {{math.NaN()}}, {{math.Inf(1)}}} {
		if _, ok := inverse3(invalid); ok {
			t.Fatal("invalid/singular matrix accepted")
		}
	}
	for _, scale := range []float64{1e-200, 1e200} {
		scaled := matrix3{{scale, 0, 0}, {0, scale, 0}, {0, 0, scale}}
		inverse, ok := inverse3(scaled)
		if !ok {
			t.Fatal("well-conditioned scaled matrix rejected")
		}
		closeFloat(t, inverse[0][0]*scale, 1)
	}
	color := renderRaw(2, 2).Calibration.Color.Value
	m, ok := renderColorMatrix(color)
	if !ok {
		t.Fatal("sRGB-primary transform rejected")
	}
	got := transform3(m, [3]float64{0.25, 0.5, 0.75})
	for c, want := range [3]float64{0.25, 0.5, 0.75} {
		closeFloat(t, got[c], want)
	}
	color.CameraToXYZ = [3][3]float64{{1, 0, 0}, {0, 1, 0}, {0, 0, 1}}
	color.ReferenceWhite = [3]float64{0.96422, 1, 0.82521}
	m, ok = renderColorMatrix(color)
	if !ok {
		t.Fatal("D50 profile rejected")
	}
	// Published Bradford D50->D65 matrix, independently rounded to seven places.
	adapt := multiply3(rgbToXYZ(), m)
	expected := matrix3{{0.9555766, -0.0230393, 0.0631636}, {-0.0282895, 1.0099416, 0.0210077}, {0.0122982, -0.0204830, 1.3299098}}
	for i := range 3 {
		for j := range 3 {
			closeFloat(t, adapt[i][j], expected[i][j])
		}
	}
	for _, white := range [][3]float64{{0, 1, 1}, {1, 2, 1}, {math.NaN(), 1, 1}, {100, 1, 1}} {
		color.ReferenceWhite = white
		if _, ok := renderColorMatrix(color); ok {
			t.Fatal("invalid reference white accepted")
		}
	}
	for _, test := range []struct {
		input float64
		want  uint16
	}{{-1, 0}, {0, 0}, {0.0031308, 2651}, {0.18, 30235}, {0.5, 48192}, {1, 65535}, {3, 65535}} {
		if got := encodeSRGB(test.input); absCode(got, test.want) > 1 {
			t.Fatalf("sRGB(%g)=%d, want %d", test.input, got, test.want)
		}
	}
}

func absCode(a, b uint16) int { return int(math.Abs(float64(a) - float64(b))) }

func TestNormalizationAndWhiteBalance(t *testing.T) {
	raw := renderRaw(2, 2)
	raw.Calibration.Black.Value = Levels{{10, 20}, {30, 40}}
	raw.Calibration.White.Value = Levels{{110, 220}, {330, 440}}
	raw.Calibration.WhiteBalance.Value = Levels{{4, 2}, {3, 6}}
	copy(raw.Pixels, []uint16{60, 120, 180, 240})
	plan, err := makeRenderPlan(raw, RenderOptions{}, true)
	if err != nil {
		t.Fatal(err)
	}
	want := Levels{{1, 0.5}, {0.75, 1.5}}
	for y := range 2 {
		for x := range 2 {
			closeFloat(t, (float64(raw.Pixels[y*2+x])-plan.black[y][x])*plan.scale[y][x], want[y][x])
		}
	}
	image, err := Render(context.Background(), raw, RenderOptions{ExposureEV: -1})
	if err != nil {
		t.Fatal(err)
	}
	// At red (0,0), the two normalized greens average to .625. Headroom
	// 1.5 in blue survives until -1 EV makes it .75, not an earlier .5.
	pixel := image.NRGBA64At(0, 0)
	for c, value := range []uint16{pixel.R, pixel.G, pixel.B} {
		want := []float64{0.5, 0.3125, 0.75}[c]
		if absCode(value, encodeSRGB(want)) > 1 {
			t.Fatalf("normalization/WB/exposure channel %d: %d", c, value)
		}
	}
	raw.Pixels[0] = 0
	image, err = Render(context.Background(), raw, RenderOptions{})
	if err != nil || image.NRGBA64At(0, 0).R != 0 {
		t.Fatal("negative black-subtracted signal not clamped", err)
	}
}

func TestBilinearKnownSamplesAndBorders(t *testing.T) {
	cfa := CFA{{Red, Green1}, {Green2, Blue}}
	rows := []float64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12}
	for _, test := range []struct {
		x, y int
		want [3]float64
	}{{0, 0, [3]float64{1, 3.5, 6}}, {1, 0, [3]float64{2, 2, 6}}, {1, 1, [3]float64{6, 6, 6}}, {3, 1, [3]float64{7, 23.0 / 3, 8}}} {
		got := demosaic(rows, 4, 3, test.x, test.y, image.Point{}, cfa)
		for c := range 3 {
			closeFloat(t, got[c], test.want[c])
		}
	}
}

func TestRenderAllCFAPhasesCropsAndSmallImages(t *testing.T) {
	for shiftY := range 2 {
		for shiftX := range 2 {
			for _, dimensions := range [][2]int{{2, 2}, {2, 3}, {3, 2}, {3, 3}, {5, 4}} {
				raw := renderRaw(dimensions[0]+2, dimensions[1]+2)
				base := raw.CFA
				for y := range 2 {
					for x := range 2 {
						raw.CFA[y][x] = base[(y+shiftY)&1][(x+shiftX)&1]
						raw.Calibration.Black.Value[y][x] = float64(10 + y*2 + x)
						raw.Calibration.White.Value[y][x] = raw.Calibration.Black.Value[y][x] + 1000
					}
				}
				// Odd-origin crop checks phase in sensor coordinates, not crop coordinates.
				raw.ActiveArea = image.Rect(1, 1, raw.Width-1, raw.Height-1)
				for y := range raw.Height {
					for x := range raw.Width {
						raw.Pixels[y*raw.Stride+x] = uint16(raw.Calibration.Black.Value[y&1][x&1]) + []uint16{200, 400, 600}[channel(raw.CFA[y&1][x&1])]
					}
				}
				out, err := Render(context.Background(), raw, RenderOptions{})
				if err != nil || out.Bounds().Dx() != dimensions[0] || out.Bounds().Dy() != dimensions[1] {
					t.Fatal("phase/crop/tiny render", err)
				}
				for y := range out.Bounds().Dy() {
					for x := range out.Bounds().Dx() {
						pixel := out.NRGBA64At(x, y)
						if absCode(pixel.R, encodeSRGB(.2)) > 1 || absCode(pixel.G, encodeSRGB(.4)) > 1 ||
							absCode(pixel.B, encodeSRGB(.6)) > 1 || pixel.A != 65535 {
							t.Fatalf("phase (%d,%d) dimensions %v pixel %+v", shiftX, shiftY, dimensions, pixel)
						}
					}
				}
			}
		}
	}
	for _, size := range [][2]int{{1, 1}, {1, 4}, {4, 1}} {
		if out, err := Render(context.Background(), renderRaw(size[0], size[1]), RenderOptions{}); out != nil || !errors.Is(err, ErrUnsupported) {
			t.Fatal("missing tiny-image channels must not be invented")
		}
	}
	raw := renderRaw(3, 2)
	raw.Stride = 5
	raw.Pixels = make([]uint16, 10)
	if _, err := Render(context.Background(), raw, RenderOptions{}); err != nil {
		t.Fatal("padded caller-owned stride", err)
	}
}

func TestCropIsolationAndFinalGamutClipping(t *testing.T) {
	raw := renderRaw(6, 6)
	for i := range raw.Pixels {
		raw.Pixels[i] = 65535 // Deliberately invalid outside the requested crop.
	}
	raw.ActiveArea = image.Rect(2, 2, 4, 4)
	for y := 2; y < 4; y++ {
		for x := 2; x < 4; x++ {
			raw.Pixels[y*6+x] = []uint16{200, 400, 600}[channel(raw.CFA[y&1][x&1])]
		}
	}
	color := multiply3(rgbToXYZ(), matrix3{{1, -1, 0}, {0, 1, 0}, {0, 0, 2}})
	raw.Calibration.Color.Value.CameraToXYZ = [3][3]float64(color)
	out, err := Render(context.Background(), raw, RenderOptions{})
	if err != nil {
		t.Fatal("outside-crop sites must not participate", err)
	}
	for y := range 2 {
		for x := range 2 {
			pixel := out.NRGBA64At(x, y)
			if pixel.R != 0 || absCode(pixel.G, encodeSRGB(.4)) > 1 || pixel.B != 65535 {
				t.Fatal("final color/gamut clipping", pixel)
			}
		}
	}
	if out, err := Render(context.Background(), raw, RenderOptions{ActiveArea: image.Rect(2, 2, 3, 4)}); out != nil || !errors.Is(err, ErrUnsupported) {
		t.Fatal("one-column crop must not borrow outside sensor neighbors")
	}
}

func TestEveryRenderOptionPreservesInputAndIndependentOutput(t *testing.T) {
	for _, opts := range []RenderOptions{
		{}, {ExposureEV: 2}, {PreserveOrientation: true}, {AllowEstimatedWhite: true},
		{ActiveArea: image.Rect(1, 1, 3, 4)}, {Calibration: renderRaw(2, 2).Calibration},
		{Limits: Limits{MetadataBytes: 1, PreviewBytes: 1, WorkingBytes: 2048}},
		{ActiveArea: image.Rect(1, 1, 3, 4), ExposureEV: -2, PreserveOrientation: true,
			Calibration: renderRaw(2, 2).Calibration, AllowEstimatedWhite: true},
	} {
		raw := renderRaw(4, 5)
		raw.Orientation = OrientationRightBottom
		before := *raw
		before.Pixels = slices.Clone(raw.Pixels)
		optionsBefore := opts
		first, err := Render(context.Background(), raw, opts)
		if err != nil {
			t.Fatal(err)
		}
		expected := slices.Clone(first.Pix)
		first.Pix[0]++
		second, err := Render(context.Background(), raw, opts)
		if err != nil || !slices.Equal(expected, second.Pix) {
			t.Fatal("render output aliases earlier output", err)
		}
		if opts != optionsBefore || !reflect.DeepEqual(before, *raw) {
			t.Fatal("a render option mutated RAW/options")
		}
	}
	small, tall := renderRaw(4, 4), renderRaw(4, 64)
	allocations := func(raw *RawImage) float64 {
		return testing.AllocsPerRun(10, func() {
			if _, err := Render(context.Background(), raw, RenderOptions{}); err != nil {
				t.Fatal(err)
			}
		})
	}
	if allocations(small) != allocations(tall) {
		t.Fatal("render allocations grow with row/pixel count")
	}
}

func TestAllEXIFOrientationMappings(t *testing.T) {
	// Independent destination grids for a 3x2 source labelled 1..6.
	expected := [][]int{
		{1, 2, 3, 4, 5, 6}, {3, 2, 1, 6, 5, 4}, {6, 5, 4, 3, 2, 1}, {4, 5, 6, 1, 2, 3},
		{1, 4, 2, 5, 3, 6}, {4, 1, 5, 2, 6, 3}, {6, 3, 5, 2, 4, 1}, {3, 6, 2, 5, 1, 4},
	}
	raw := renderRaw(3, 2)
	copy(raw.Pixels, []uint16{100, 200, 300, 400, 500, 600})
	sensor, err := Render(context.Background(), raw, RenderOptions{PreserveOrientation: true})
	if err != nil {
		t.Fatal(err)
	}
	for orientation := OrientationTopLeft; orientation <= OrientationLeftBottom; orientation++ {
		width, height := 3, 2
		if orientation >= OrientationLeftTop {
			width, height = height, width
		}
		grid := make([]int, 6)
		for y := range 2 {
			for x := range 3 {
				dx, dy := orientPixel(x, y, 3, 2, orientation)
				grid[dy*width+dx] = y*3 + x + 1
			}
		}
		if !slices.Equal(grid, expected[orientation-1]) {
			t.Fatalf("orientation %d: %v", orientation, grid)
		}
		raw.Orientation = orientation
		out, err := Render(context.Background(), raw, RenderOptions{})
		if err != nil || out.Bounds() != image.Rect(0, 0, width, height) {
			t.Fatal("oriented output bounds", err)
		}
		for i, source := range expected[orientation-1] {
			if out.NRGBA64At(i%width, i/width) != sensor.NRGBA64At((source-1)%3, (source-1)/3) {
				t.Fatal("Render did not apply EXIF mapping")
			}
		}
		unrotated, err := Render(context.Background(), raw, RenderOptions{PreserveOrientation: true})
		if err != nil || !slices.Equal(unrotated.Pix, sensor.Pix) {
			t.Fatal("preserve sensor orientation", err)
		}
	}
}

func TestRenderInvalidInputsAndOverrides(t *testing.T) {
	for _, test := range []struct {
		name string
		edit func(*RawImage, *RenderOptions)
		kind error
	}{
		{"width", func(r *RawImage, o *RenderOptions) { r.Width = 0 }, ErrCorrupt},
		{"stride", func(r *RawImage, o *RenderOptions) { r.Stride = 1 }, ErrCorrupt},
		{"slice", func(r *RawImage, o *RenderOptions) { r.Pixels = nil }, ErrCorrupt},
		{"bits", func(r *RawImage, o *RenderOptions) { r.BitDepth = 17 }, ErrCorrupt},
		{"sample", func(r *RawImage, o *RenderOptions) { r.Pixels[0] = 4096 }, ErrCorrupt},
		{"cfa", func(r *RawImage, o *RenderOptions) { r.CFA[0][0] = Green1 }, ErrUnsupported},
		{"unknown cfa", func(r *RawImage, o *RenderOptions) { r.CFA[0][0] = ColorUnknown }, ErrUnsupported},
		{"non-bayer", func(r *RawImage, o *RenderOptions) { r.CFA = CFA{{Red, Blue}, {Green1, Green2}} }, ErrUnsupported},
		{"crop missing", func(r *RawImage, o *RenderOptions) { r.ActiveArea = image.Rectangle{} }, ErrCalibrationUnavailable},
		{"crop invalid", func(r *RawImage, o *RenderOptions) { r.ActiveArea.Max.X++ }, ErrCorrupt},
		{"override crop", func(r *RawImage, o *RenderOptions) { o.ActiveArea = image.Rect(-1, 0, 2, 2) }, ErrInvalidOptions},
		{"empty override crop", func(r *RawImage, o *RenderOptions) { o.ActiveArea = image.Rect(1, 1, 1, 2) }, ErrInvalidOptions},
		{"orientation missing", func(r *RawImage, o *RenderOptions) { r.Orientation = 0 }, ErrCalibrationUnavailable},
		{"orientation invalid", func(r *RawImage, o *RenderOptions) { r.Orientation = 9 }, ErrCorrupt},
		{"black missing", func(r *RawImage, o *RenderOptions) { r.Calibration.Black.Origin = 0 }, ErrCalibrationUnavailable},
		{"white missing", func(r *RawImage, o *RenderOptions) { r.Calibration.White.Origin = 0 }, ErrCalibrationUnavailable},
		{"WB missing", func(r *RawImage, o *RenderOptions) { r.Calibration.WhiteBalance.Origin = 0 }, ErrCalibrationUnavailable},
		{"color missing", func(r *RawImage, o *RenderOptions) { r.Calibration.Color.Origin = 0 }, ErrCalibrationUnavailable},
		{"estimated white", func(r *RawImage, o *RenderOptions) { r.Calibration.White.Origin = OriginEstimate }, ErrCalibrationUnavailable},
		{"estimated color", func(r *RawImage, o *RenderOptions) { r.Calibration.Color.Origin = OriginEstimate }, ErrCalibrationUnavailable},
		{"estimated black", func(r *RawImage, o *RenderOptions) { r.Calibration.Black.Origin = OriginEstimate }, ErrCalibrationUnavailable},
		{"estimated WB", func(r *RawImage, o *RenderOptions) { r.Calibration.WhiteBalance.Origin = OriginEstimate }, ErrCalibrationUnavailable},
		{"bad origin", func(r *RawImage, o *RenderOptions) { r.Calibration.Black.Origin = 250 }, ErrCalibrationUnavailable},
		{"singular color", func(r *RawImage, o *RenderOptions) {
			r.Calibration.Color.Value = ColorTransform{ReferenceWhite: d65White()}
		}, ErrCalibrationUnavailable},
		{"bad WB", func(r *RawImage, o *RenderOptions) { r.Calibration.WhiteBalance.Value[0][1] = 0 }, ErrCalibrationUnavailable},
		{"bad red WB", func(r *RawImage, o *RenderOptions) { r.Calibration.WhiteBalance.Value[0][0] = -1 }, ErrCalibrationUnavailable},
		{"underflow WB", func(r *RawImage, o *RenderOptions) {
			r.Calibration.WhiteBalance.Value[0][0] = math.SmallestNonzeroFloat64
		}, ErrCalibrationUnavailable},
		{"overflow scale", func(r *RawImage, o *RenderOptions) {
			r.Calibration.White.Value[0][0] = math.SmallestNonzeroFloat64
		}, ErrCalibrationUnavailable},
		{"bad range", func(r *RawImage, o *RenderOptions) { r.Calibration.White.Value[0][0] = 0 }, ErrCalibrationUnavailable},
		{"white domain", func(r *RawImage, o *RenderOptions) { r.Calibration.White.Value[0][0] = 4096 }, ErrCalibrationUnavailable},
		{"NaN EV", func(r *RawImage, o *RenderOptions) { o.ExposureEV = math.NaN() }, ErrInvalidOptions},
		{"overflow EV", func(r *RawImage, o *RenderOptions) { o.ExposureEV = 1024 }, ErrInvalidOptions},
		{"underflow EV", func(r *RawImage, o *RenderOptions) { o.ExposureEV = -1075 }, ErrInvalidOptions},
		{"override NaN", func(r *RawImage, o *RenderOptions) {
			o.Calibration.Black = Value[Levels]{Value: Levels{{math.NaN()}}, Origin: OriginOverride}
		}, ErrInvalidOptions},
		{"override range", func(r *RawImage, o *RenderOptions) { o.Calibration.White = Value[Levels]{Origin: OriginOverride} }, ErrInvalidOptions},
		{"override origin", func(r *RawImage, o *RenderOptions) { o.Calibration.White = Value[Levels]{Origin: 250} }, ErrInvalidOptions},
		{"finite exposure overflow", func(r *RawImage, o *RenderOptions) {
			r.Pixels[0] = 4095
			o.ExposureEV = 1023
		}, ErrInvalidOptions},
		{"override color", func(r *RawImage, o *RenderOptions) {
			o.Calibration.Color = Value[ColorTransform]{Origin: OriginOverride}
		}, ErrInvalidOptions},
		{"dimension limit", func(r *RawImage, o *RenderOptions) { o.Limits.Dimension = 1 }, ErrLimitExceeded},
		{"pixel limit", func(r *RawImage, o *RenderOptions) { o.Limits.Pixels = 3 }, ErrLimitExceeded},
		{"invalid limits", func(r *RawImage, o *RenderOptions) { o.Limits.Pixels = -1 }, ErrInvalidOptions},
	} {
		t.Run(test.name, func(t *testing.T) {
			raw, opts := renderRaw(2, 2), RenderOptions{}
			test.edit(raw, &opts)
			if out, err := Render(context.Background(), raw, opts); out != nil || !errors.Is(err, test.kind) {
				t.Fatalf("got %v, want %v", err, test.kind)
			}
		})
	}
	if out, err := Render(nil, renderRaw(2, 2), RenderOptions{}); out != nil || !errors.Is(err, ErrInvalidOptions) {
		t.Fatal("nil context")
	}
	if out, err := Render(context.Background(), nil, RenderOptions{}); out != nil || !errors.Is(err, ErrInvalidOptions) {
		t.Fatal("nil RAW")
	}
	raw := renderRaw(2, 2)
	raw.ActiveArea, raw.Orientation, raw.Calibration = image.Rectangle{}, 0, Calibration{}
	opts := RenderOptions{ActiveArea: image.Rect(0, 0, 2, 2), PreserveOrientation: true, Calibration: renderRaw(2, 2).Calibration}
	if _, err := Render(context.Background(), raw, opts); err != nil {
		t.Fatal("explicit overrides did not restore missing calibration/crop", err)
	}
	raw = renderRaw(2, 2)
	raw.Calibration.White.Origin = OriginEstimate
	if _, err := Render(context.Background(), raw, RenderOptions{AllowEstimatedWhite: true}); err != nil {
		t.Fatal("estimated-white consent rejected", err)
	}
}

type renderCancelContext struct {
	context.Context
	calls atomic.Int32
	after int32
}

func (c *renderCancelContext) Err() error {
	if c.calls.Add(1) > c.after {
		return context.Canceled
	}
	return nil
}

func TestRenderQuotasCancellationAndOwnership(t *testing.T) {
	raw := renderRaw(3, 4)
	for _, orientation := range []Orientation{OrientationTopLeft, OrientationRightTop} {
		raw.Orientation = orientation
		required := int64(3*4*8 + 3*24)
		opts := RenderOptions{Limits: Limits{WorkingBytes: required, MetadataBytes: 1, PreviewBytes: 1}}
		if _, err := Render(context.Background(), raw, opts); err != nil {
			t.Fatal("exact working quota", err)
		}
		opts.Limits.WorkingBytes--
		if out, err := Render(context.Background(), raw, opts); out != nil || !errors.Is(err, ErrLimitExceeded) {
			t.Fatal("working quota did not precede allocation")
		}
	}
	for _, after := range []int32{0, 1, 3, 6} {
		ctx := &renderCancelContext{Context: context.Background(), after: after}
		if out, err := Render(ctx, raw, RenderOptions{}); out != nil || !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation after %d polls: %v", after, err)
		}
	}
	before := *raw
	before.Pixels = slices.Clone(raw.Pixels)
	opts := RenderOptions{ExposureEV: -1, PreserveOrientation: true}
	optionsBefore := opts
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 5 {
				out, err := Render(context.Background(), raw, opts)
				if err != nil {
					t.Error(err)
					return
				}
				out.Pix[0]++
			}
		}()
	}
	wg.Wait()
	if !reflect.DeepEqual(*raw, before) || opts != optionsBefore {
		t.Fatal("rendering changed/aliased RAW or options")
	}
	huge := renderRaw(2, 2)
	huge.Width, huge.Height, huge.Stride = 32768, 32768, 32768
	huge.ActiveArea = image.Rect(0, 0, 32768, 32768)
	if _, err := makeRenderPlan(huge, RenderOptions{Limits: Limits{Pixels: 1 << 30}}, false); !errors.Is(err, ErrLimitExceeded) {
		t.Fatal("huge output not rejected before allocation", err)
	}
	largeOpts := RenderOptions{Limits: Limits{Pixels: 1 << 30, WorkingBytes: 1 << 35}}
	plan, err := makeRenderPlan(huge, largeOpts, false)
	if strconv.IntSize == 32 {
		if !errors.Is(err, ErrLimitExceeded) {
			t.Fatal("render output must fit platform int before allocation", err)
		}
	} else if err != nil || uint64(plan.outputBytes) != 8<<30 {
		t.Fatal("explicitly raised 64-bit render limit", err)
	}
}

func FuzzRender(f *testing.F) {
	f.Add([]byte{0, 0, 0, 0, 0, 0, 0, 0}, uint8(0), uint8(1), int8(0))
	f.Fuzz(func(t *testing.T, input []byte, phase, orientation uint8, ev int8) {
		if len(input) > 512 {
			return
		}
		raw := renderRaw(2+len(input)%13, 2+len(input)%11)
		base := raw.CFA
		for y := range 2 {
			for x := range 2 {
				raw.CFA[y][x] = base[(y+int(phase&1))&1][(x+int((phase>>1)&1))&1]
			}
		}
		for i, value := range input {
			raw.Pixels[i%len(raw.Pixels)] = uint16(value) * 16
		}
		raw.Orientation = Orientation(orientation)
		before := slices.Clone(raw.Pixels)
		opts := RenderOptions{ExposureEV: float64(ev) / 8, Limits: Limits{Pixels: 256, Dimension: 32, MetadataBytes: 1, PreviewBytes: 1, WorkingBytes: 8192}}
		out, err := Render(context.Background(), raw, opts)
		if err != nil {
			if out != nil {
				t.Fatal("partial output on failure")
			}
		} else {
			if out.Bounds().Dx()*out.Bounds().Dy() != raw.Width*raw.Height {
				t.Fatal("wrong output shape")
			}
			for y := range out.Bounds().Dy() {
				for x := range out.Bounds().Dx() {
					if out.NRGBA64At(x, y).A != 65535 {
						t.Fatal("nonopaque output")
					}
				}
			}
		}
		if !slices.Equal(raw.Pixels, before) {
			t.Fatal("RAW mutation")
		}
	})
}
