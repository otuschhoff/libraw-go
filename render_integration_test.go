//go:build integration

// SPDX-License-Identifier: LGPL-2.1-only

package libraw

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"image"
	"io"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
)

func TestReferenceRendering(t *testing.T) {
	var manifest struct{ Fixtures []struct{ ID, SHA256 string } }
	readJSON(t, "testdata/fixtures.json", &manifest)
	if len(manifest.Fixtures) != 4 {
		t.Fatal("review rendering coverage when required fixtures change")
	}
	dir := os.Getenv("ORF_FIXTURE_DIR")
	if dir == "" {
		dir = ".cache/fixtures"
	}
	for _, fixture := range manifest.Fixtures {
		t.Run(fixture.ID, func(t *testing.T) {
			input, err := os.Open(filepath.Join(dir, fixture.ID+".orf"))
			if err != nil {
				t.Fatal(err)
			}
			defer input.Close()
			info, err := input.Stat()
			if err != nil {
				t.Fatal(err)
			}
			hash := sha256.New()
			if _, err := io.Copy(hash, input); err != nil || hex.EncodeToString(hash.Sum(nil)) != fixture.SHA256 {
				t.Fatal("required render fixture read/hash mismatch", err)
			}
			file, err := Open(input, info.Size(), OpenOptions{})
			if err != nil {
				t.Fatal(err)
			}
			raw, err := file.DecodeRaw(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			before := *raw
			before.Pixels = slices.Clone(raw.Pixels)
			before.Diagnostics = slices.Clone(raw.Diagnostics)
			estimated := raw.Calibration.White.Origin == OriginEstimate
			if file.Capabilities().Render.Available == estimated {
				t.Fatal("default render capability must reflect estimated-white consent")
			}
			if !estimated {
				limited, err := Open(input, info.Size(), OpenOptions{Limits: Limits{WorkingBytes: 64 << 20}})
				if err != nil || !limited.Capabilities().Raw.Available || limited.Capabilities().Render.Available {
					t.Fatal("render resources must be independent of RAW capability", err)
				}
				if out, err := Render(context.Background(), raw, RenderOptions{Limits: Limits{WorkingBytes: 64 << 20}}); out != nil || !errors.Is(err, ErrLimitExceeded) {
					t.Fatal("real-fixture render quota must fail before output allocation", err)
				}
			}
			if estimated {
				if out, err := Render(context.Background(), raw, RenderOptions{}); out != nil || !errors.Is(err, ErrCalibrationUnavailable) {
					t.Fatal("estimated saturation silently enabled rendering")
				}
			}
			opts := RenderOptions{AllowEstimatedWhite: estimated}
			out, err := Render(context.Background(), raw, opts)
			if err != nil {
				t.Fatal(err)
			}
			width, height := raw.ActiveArea.Dx(), raw.ActiveArea.Dy()
			if raw.Orientation >= OrientationLeftTop {
				width, height = height, width
			}
			if out.Rect != image.Rect(0, 0, width, height) || out.Stride != width*8 {
				t.Fatal("incorrect crop/oriented shape")
			}
			nonzero := 0
			for y := range height {
				for x := range width {
					pixel := out.NRGBA64At(x, y)
					if pixel.A != 65535 {
						t.Fatal("nonopaque rendered pixel")
					}
					if pixel.R != 0 || pixel.G != 0 || pixel.B != 0 {
						nonzero++
					}
				}
			}
			if nonzero < 1000 {
				t.Fatal("full fixture unexpectedly rendered as black")
			}
			// Compare deterministic sensor-coordinate probes with a separate,
			// direct-array implementation, including every crop edge/corner.
			for i := range 257 {
				x, y := (i*7919)%raw.ActiveArea.Dx(), (i*3571)%raw.ActiveArea.Dy()
				if i < 4 {
					x, y = (i&1)*(raw.ActiveArea.Dx()-1), ((i>>1)&1)*(raw.ActiveArea.Dy()-1)
				}
				want := referenceRenderPixel(raw, x, y)
				dx, dy := orientPixel(x, y, raw.ActiveArea.Dx(), raw.ActiveArea.Dy(), raw.Orientation)
				pixel := out.NRGBA64At(dx, dy)
				for c, got := range []uint16{pixel.R, pixel.G, pixel.B} {
					if absCode(got, want[c]) > 1 {
						t.Fatalf("sensor probe (%d,%d) channel %d: got %d want %d", x, y, c, got, want[c])
					}
				}
			}
			out.Pix[0]++
			if !reflect.DeepEqual(before, *raw) {
				t.Fatal("real-fixture rendering changed/aliased RAW")
			}
			t.Logf("rendered %dx%d, nonzero=%d, estimated-white=%v", width, height, nonzero, estimated)
		})
	}
}

func referenceRenderPixel(raw *RawImage, x, y int) [3]uint16 {
	x += raw.ActiveArea.Min.X
	y += raw.ActiveArea.Min.Y
	normalize := func(x, y int) float64 {
		cal := raw.Calibration
		value := math.Max(0, (float64(raw.Pixels[y*raw.Stride+x])-cal.Black.Value[y&1][x&1])/
			(cal.White.Value[y&1][x&1]-cal.Black.Value[y&1][x&1]))
		return value * cal.WhiteBalance.Value[y&1][x&1]
	}
	var camera [3]float64
	center := channel(raw.CFA[y&1][x&1])
	for c := range 3 {
		if c == center {
			camera[c] = normalize(x, y)
			continue
		}
		n := 0
		for dy := -1; dy <= 1; dy++ {
			for dx := -1; dx <= 1; dx++ {
				nx, ny := x+dx, y+dy
				if image.Pt(nx, ny).In(raw.ActiveArea) && channel(raw.CFA[ny&1][nx&1]) == c {
					camera[c] += normalize(nx, ny)
					n++
				}
			}
		}
		camera[c] /= float64(n)
	}
	var xyz [3]float64
	for c := range 3 {
		for k := range 3 {
			xyz[c] += raw.Calibration.Color.Value.CameraToXYZ[c][k] * camera[k]
		}
	}
	toRGB := [3][3]float64{{3.2404542, -1.5371385, -0.4985314}, {-0.9692660, 1.8760108, 0.0415560}, {0.0556434, -0.2040259, 1.0572252}}
	var out [3]uint16
	for c := range 3 {
		value := 0.0
		for k := range 3 {
			value += toRGB[c][k] * xyz[k]
		}
		value = math.Min(1, math.Max(0, value))
		if value <= 0.0031308 {
			value *= 12.92
		} else {
			value = 1.055*math.Pow(value, 1/2.4) - 0.055
		}
		out[c] = uint16(math.Floor(value*65535 + .5))
	}
	return out
}
