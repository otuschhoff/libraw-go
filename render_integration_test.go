//go:build integration

// SPDX-License-Identifier: LGPL-2.1-only

package libraw

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"image"
	"image/png"
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
	var baselines struct{ Cases []renderBaseline }
	readJSON(t, "testdata/fixtures.json", &manifest)
	readJSON(t, "testdata/render/index.json", &baselines)
	if len(manifest.Fixtures) != 4 || len(baselines.Cases) != len(manifest.Fixtures) {
		t.Fatal("review rendering coverage when required fixtures change")
	}
	byID := map[string]renderBaseline{}
	for _, baseline := range baselines.Cases {
		if _, exists := byID[baseline.ID]; exists {
			t.Fatal("duplicate render baseline")
		}
		byID[baseline.ID] = baseline
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
			baseline, exists := byID[fixture.ID]
			if !exists || baseline.InputSHA256 != fixture.SHA256 || baseline.Options != opts {
				t.Fatal("frozen rendering options or input binding changed")
			}
			metadata := file.Metadata()
			if !renderMetadataEqual(metadata, baseline.Metadata) {
				t.Fatalf("frozen metadata/calibration changed:\ncurrent=%+v\nfrozen=%+v", metadata, baseline.Metadata)
			}
			if metadata.Calibration.Color.Value.CameraToXYZ != baseline.Metadata.Calibration.Color.Value.CameraToXYZ {
				t.Logf("profile matrix roundoff within 1e-6: current=%.17g frozen=%.17g",
					metadata.Calibration.Color.Value.CameraToXYZ, baseline.Metadata.Calibration.Color.Value.CameraToXYZ)
			}
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
			compareRenderedBaseline(t, out, baseline)
			checkMemoryReaderParity(t, filepath.Join(dir, fixture.ID+".orf"), file, raw, opts, out)
			out.Pix[0]++
			if !reflect.DeepEqual(before, *raw) {
				t.Fatal("real-fixture rendering changed/aliased RAW")
			}
			t.Logf("rendered %dx%d, nonzero=%d, estimated-white=%v", width, height, nonzero, estimated)
		})
	}
}

type renderBaseline struct {
	ID           string
	InputSHA256  string `json:"input_sha256"`
	Metadata     Metadata
	Options      RenderOptions
	PNGBytes     int64  `json:"png_bytes"`
	PNGSHA256    string `json:"png_sha256"`
	PixelsSHA256 string `json:"nrgba64_sha256"`
}

func renderMetadataEqual(got, want Metadata) bool {
	if got.Calibration.Color.Origin == OriginProfile && want.Calibration.Color.Origin == OriginProfile {
		for i := range 3 {
			for j := range 3 {
				a, b := got.Calibration.Color.Value.CameraToXYZ[i][j], want.Calibration.Color.Value.CameraToXYZ[i][j]
				if !finite(a) || !finite(b) || math.Abs(a-b) > 1e-6 {
					return false
				}
			}
		}
		got.Calibration.Color.Value.CameraToXYZ = want.Calibration.Color.Value.CameraToXYZ
	}
	return reflect.DeepEqual(got, want)
}

func TestFrozenRenderMetadataTolerance(t *testing.T) {
	want := Metadata{NormalizedModel: "E-410", Calibration: Calibration{
		Color: Value[ColorTransform]{Origin: OriginProfile, Source: "approved-profile"},
	}}
	for _, test := range []struct {
		name   string
		mutate func(*Metadata)
		equal  bool
	}{
		{"unchanged", func(*Metadata) {}, true},
		{"last bit", func(m *Metadata) { m.Calibration.Color.Value.CameraToXYZ[0][0] = math.SmallestNonzeroFloat64 }, true},
		{"positive bound", func(m *Metadata) { m.Calibration.Color.Value.CameraToXYZ[0][0] = 1e-6 }, true},
		{"negative bound", func(m *Metadata) { m.Calibration.Color.Value.CameraToXYZ[2][2] = -1e-6 }, true},
		{"over bound", func(m *Metadata) { m.Calibration.Color.Value.CameraToXYZ[0][0] = math.Nextafter(1e-6, math.Inf(1)) }, false},
		{"nan", func(m *Metadata) { m.Calibration.Color.Value.CameraToXYZ[1][1] = math.NaN() }, false},
		{"infinity", func(m *Metadata) { m.Calibration.Color.Value.CameraToXYZ[1][1] = math.Inf(1) }, false},
		{"origin", func(m *Metadata) { m.Calibration.Color.Origin = OriginFile }, false},
		{"source", func(m *Metadata) { m.Calibration.Color.Source = "other" }, false},
		{"model", func(m *Metadata) { m.NormalizedModel = "other" }, false},
		{"geometry", func(m *Metadata) { m.Geometry.Width++ }, false},
		{"reference white", func(m *Metadata) { m.Calibration.Color.Value.ReferenceWhite[0] = math.SmallestNonzeroFloat64 }, false},
		{"file value", func(m *Metadata) { m.ExposureTime.Value = math.SmallestNonzeroFloat64 }, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := want.Clone()
			test.mutate(&got)
			if renderMetadataEqual(got, want) != test.equal {
				t.Fatal("frozen metadata tolerance or exact provenance/field binding violated")
			}
		})
	}
	fileValue := want.Clone()
	fileValue.Calibration.Color.Origin = OriginFile
	changed := fileValue.Clone()
	changed.Calibration.Color.Value.CameraToXYZ[0][0] = math.SmallestNonzeroFloat64
	if renderMetadataEqual(changed, fileValue) {
		t.Fatal("file-supplied values must remain exact, not acquire a derived-profile tolerance")
	}
}

func compareRenderedBaseline(t *testing.T, got *image.NRGBA64, baseline renderBaseline) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata/render", baseline.ID+".png"))
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(data)
	if int64(len(data)) != baseline.PNGBytes || hex.EncodeToString(hash[:]) != baseline.PNGSHA256 {
		t.Fatal("rendered PNG artifact digest/length changed")
	}
	decoded, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	var pixels []byte
	var stride int
	switch value := decoded.(type) {
	case *image.RGBA64:
		pixels, stride = value.Pix, value.Stride
	case *image.NRGBA64:
		pixels, stride = value.Pix, value.Stride
	default:
		t.Fatal("baseline lost 16-bit channels")
	}
	if decoded.Bounds() != got.Rect || stride != got.Stride {
		t.Fatal("baseline crop/orientation differs")
	}
	pixelHash := sha256.Sum256(pixels)
	if hex.EncodeToString(pixelHash[:]) != baseline.PixelsSHA256 {
		t.Fatal("decoded baseline digest changed")
	}
	differences, mismatches, maxError, first := 0, 0, 0, -1
	for y := range got.Rect.Dy() {
		for x := range got.Rect.Dx() {
			offset := y*stride + x*8
			if binary.BigEndian.Uint16(got.Pix[offset+6:]) != 65535 ||
				binary.BigEndian.Uint16(pixels[offset+6:]) != 65535 {
				t.Fatalf("nonopaque output/baseline at (%d,%d)", x, y)
			}
			for c := range 3 {
				i := offset + c*2
				delta := absCode(binary.BigEndian.Uint16(got.Pix[i:]), binary.BigEndian.Uint16(pixels[i:]))
				maxError = max(maxError, delta)
				if delta != 0 {
					differences++
				}
				if delta > 1 {
					mismatches++
					if first < 0 {
						first = i
					}
				}
			}
		}
	}
	if mismatches != 0 {
		t.Fatalf("render regression: %d out-of-tolerance channels, max error=%d; first=(%d,%d), channel=%d",
			mismatches, maxError, first%stride/8, first/stride, first%8/2)
	}
	t.Logf("full rendered baseline: %d RGB channels, differences=%d, max error=%d (allowed 1)",
		got.Rect.Dx()*got.Rect.Dy()*3, differences, maxError)
}

func checkMemoryReaderParity(t *testing.T, path string, disk *File, raw *RawImage, opts RenderOptions, rgb *image.NRGBA64) {
	t.Helper()
	input, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	before := sha256.Sum256(input)
	memory, err := Open(bytes.NewReader(input), int64(len(input)), OpenOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(memory.Metadata(), disk.Metadata()) || memory.Capabilities() != disk.Capabilities() {
		t.Fatal("metadata/capabilities differ by ReaderAt implementation")
	}
	diskPreview, err := disk.Preview(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	memoryPreview, err := memory.Preview(context.Background())
	if err != nil || !reflect.DeepEqual(memoryPreview, diskPreview) {
		t.Fatal("preview differs by ReaderAt implementation", err)
	}
	memoryPreview.JPEG[0] ^= 1
	if memoryPreview.JPEG[0] == diskPreview.JPEG[0] {
		t.Fatal("previews alias")
	}
	memoryRaw, err := memory.DecodeRaw(context.Background())
	if err != nil || !reflect.DeepEqual(memoryRaw, raw) {
		t.Fatal("RAW samples, geometry or calibration differ by ReaderAt implementation", err)
	}
	memoryRGB, err := Render(context.Background(), memoryRaw, opts)
	if err != nil || !reflect.DeepEqual(memoryRGB, rgb) {
		t.Fatal("rendering differs by ReaderAt implementation", err)
	}
	memoryRaw.Pixels[0] ^= 1
	memoryRGB.Pix[0] ^= 1
	if memoryRaw.Pixels[0] == raw.Pixels[0] || memoryRGB.Pix[0] == rgb.Pix[0] || sha256.Sum256(input) != before {
		t.Fatal("in-memory results alias each other or modify borrowed input")
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
