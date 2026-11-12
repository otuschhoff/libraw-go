//go:build integration

// SPDX-License-Identifier: LGPL-2.1-only

package libraw

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"image"
	"image/jpeg"
	"io"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// This explicit gate fails, never silently skips, if original fixtures are
// missing. Normal go test remains offline and photograph-independent.
func TestReferenceFixtureMetadataAndPreviews(t *testing.T) {
	var manifest struct {
		Fixtures []struct {
			ID, SHA256, Layout string
		}
	}
	readJSON(t, "testdata/fixtures.json", &manifest)
	if len(manifest.Fixtures) != 4 {
		t.Fatal("update the phase-two evidence gate deliberately when extending its corpus")
	}
	dir := os.Getenv("ORF_FIXTURE_DIR")
	if dir == "" {
		dir = ".cache/fixtures"
	}
	for _, fixture := range manifest.Fixtures {
		t.Run(fixture.ID, func(t *testing.T) {
			input, err := os.Open(filepath.Join(dir, fixture.ID+".orf"))
			if err != nil {
				t.Fatalf("required fixture missing; run reference fetch: %v", err)
			}
			defer input.Close()
			info, err := input.Stat()
			if err != nil {
				t.Fatal(err)
			}
			hash := sha256.New()
			if _, err := io.Copy(hash, input); err != nil {
				t.Fatal(err)
			}
			if hex.EncodeToString(hash.Sum(nil)) != fixture.SHA256 {
				t.Fatal("input hash differs from licensed oracle")
			}
			var reference struct {
				Exiftool map[string]json.RawMessage
				Libraw   struct {
					RawWidth  int `json:"raw_width"`
					RawHeight int `json:"raw_height"`
				}
			}
			readJSON(t, "testdata/reference/"+fixture.ID+".json", &reference)
			text := func(key string) string {
				t.Helper()
				var value string
				if err := json.Unmarshal(reference.Exiftool[key], &value); err != nil {
					t.Fatalf("oracle %s: %v", key, err)
				}
				return value
			}
			number := func(key string) float64 {
				t.Helper()
				var value float64
				if err := json.Unmarshal(reference.Exiftool[key], &value); err != nil {
					t.Fatalf("oracle %s: %v", key, err)
				}
				return value
			}
			word := strings.Contains(fixture.Layout, "word16")
			rawStart := int64(655360)
			if fixture.ID == "olympus-e-1-1800" {
				rawStart = 344064
			} else if fixture.ID == "olympus-e-400-2151" {
				rawStart = 650752
			}
			spy := &fixtureReader{input: input, rawStart: rawStart, allowPrefix: !word}
			file, err := Open(spy, info.Size(), OpenOptions{})
			if err != nil {
				t.Fatal(err)
			}
			m := file.Metadata()
			if strings.TrimSpace(m.Make) != text("IFD0:Make") || strings.TrimSpace(m.Model) != text("IFD0:Model") ||
				m.NormalizedMake != "Olympus" || m.NormalizedModel != text("IFD0:Model") ||
				m.CameraID.Value != text("Olympus:CameraType2") || m.Layout.Value != fixture.Layout {
				t.Fatalf("identity/layout differs: %+v", m)
			}
			if m.Geometry.Width != reference.Libraw.RawWidth || m.Geometry.Height != reference.Libraw.RawHeight ||
				m.Geometry.StoredWidth != int(number("IFD0:ImageWidth")) || m.Geometry.StoredHeight != int(number("IFD0:ImageHeight")) {
				t.Fatalf("geometry differs: %+v", m.Geometry)
			}
			cropLeft := parseOracleNumbers(t, text("Olympus:CropLeft"))[0]
			cropTop := parseOracleNumbers(t, text("Olympus:CropTop"))[0]
			rect := image.Rect(int(cropLeft), int(cropTop), int(cropLeft+number("Olympus:CropWidth")), int(cropTop+number("Olympus:CropHeight")))
			if !m.Geometry.ActiveArea.Present() || m.Geometry.ActiveArea.Value != rect {
				t.Fatalf("crop differs: %+v, want %v", m.Geometry, rect)
			}
			for _, pair := range []struct {
				got Value[float64]
				key string
			}{{m.ExposureTime, "ExifIFD:ExposureTime"}, {m.FNumber, "ExifIFD:FNumber"}, {m.ISO, "ExifIFD:ISO"}, {m.FocalLength, "ExifIFD:FocalLength"}} {
				if pair.got.Origin != OriginFile || math.Abs(pair.got.Value-number(pair.key)) > 1e-12 {
					t.Fatalf("%s differs: %+v", pair.key, pair.got)
				}
			}
			if m.BitDepth.Value != 12 || m.BitDepth.Origin != OriginFile || int(m.Orientation.Value) != int(number("IFD0:Orientation")) ||
				strings.TrimSpace(m.Software.Value) != text("IFD0:Software") || m.Firmware.Present() ||
				!m.Calibration.White.Present() || m.Calibration.Color.Origin != OriginProfile {
				t.Fatal("bit depth, orientation, software, or profile calibration differs")
			}
			if (word && m.Calibration.White.Origin != OriginEstimate) || (!word && m.Calibration.White.Origin != OriginProfile) {
				t.Fatal("estimated versus profile saturation provenance differs")
			}
			cfa := parseOracleNumbers(t, text("ExifIFD:CFAPattern"))[2:]
			black := parseOracleNumbers(t, text("Olympus:BlackLevel2"))
			wb := parseOracleNumbers(t, text("Olympus:WB_RBLevels"))
			green := 0
			for i, value := range cfa {
				color, blackIndex, gain := Red, 0, wb[0]/256
				switch value {
				case 1:
					color, blackIndex, gain = Green1, 1, 1
					if green != 0 {
						color, blackIndex = Green2, 2
					}
					green++
				case 2:
					color, blackIndex, gain = Blue, 3, wb[1]/256
				}
				if m.CFA.Value[i/2][i%2] != color ||
					m.Calibration.Black.Value[i/2][i%2] != black[blackIndex] ||
					m.Calibration.WhiteBalance.Value[i/2][i%2] != gain {
					t.Fatalf("CFA/black/WB differs at %d: %+v", i, m)
				}
			}
			if !m.Calibration.Black.Present() || !m.Calibration.WhiteBalance.Present() {
				t.Fatal("file calibration absent")
			}
			expectedMode := "SpecialMode=" + text("Olympus:SpecialMode") + ";DriveMode=" + text("Olympus:DriveMode")
			if m.CaptureMode.Value != expectedMode {
				t.Fatalf("capture mode differs: %q, want %q", m.CaptureMode.Value, expectedMode)
			}
			preview, err := file.Preview(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			offset, length := int64(number("Olympus:PreviewImageStart")), int(number("Olympus:PreviewImageLength"))
			expected := make([]byte, length)
			if _, err := input.ReadAt(expected, offset); err != nil {
				t.Fatal(err)
			}
			config, err := jpeg.DecodeConfig(bytes.NewReader(expected))
			if err != nil || !bytes.Equal(preview.JPEG, expected) || preview.Width != config.Width || preview.Height != config.Height {
				t.Fatalf("preview is not exact reference bytes/header: %v", err)
			}
			if preview.Orientation.Present() || len(preview.Diagnostics) == 0 {
				t.Fatal("unverified embedded-pixel orientation assumed")
			}
			t.Logf("exact preview: offset=%d length=%d dimensions=%dx%d sha256=%x", offset, length, preview.Width, preview.Height, sha256.Sum256(preview.JPEG))
			if spy.err != nil || spy.bytes > int64(length)+(256<<10) {
				t.Fatalf("Open/Preview over-read raw payload or materialized excess data: %v, %d bytes", spy.err, spy.bytes)
			}
			caps := file.Capabilities()
			if !caps.Metadata.Available || !caps.Preview.Available || !caps.Raw.Available || caps.Render.Available == word {
				t.Fatalf("incorrect capability report: %+v", caps)
			}
		})
	}
}

func readJSON(t testing.TB, path string, out any) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, out); err != nil {
		t.Fatal(err)
	}
}

func parseOracleNumbers(t *testing.T, text string) []float64 {
	t.Helper()
	parts := strings.Fields(text)
	values := make([]float64, len(parts))
	for i, part := range parts {
		var err error
		values[i], err = strconv.ParseFloat(part, 64)
		if err != nil {
			t.Fatal(err)
		}
	}
	return values
}

type fixtureReader struct {
	input       io.ReaderAt
	rawStart    int64
	allowPrefix bool
	bytes       int64
	err         error
}

func (r *fixtureReader) ReadAt(data []byte, offset int64) (int, error) {
	r.bytes += int64(len(data))
	if offset+int64(len(data)) > r.rawStart && !(r.allowPrefix && offset == r.rawStart && len(data) == 7) {
		r.err = fmt.Errorf("unexpected raw read offset=%d length=%d", offset, len(data))
		return 0, r.err
	}
	return r.input.ReadAt(data, offset)
}
