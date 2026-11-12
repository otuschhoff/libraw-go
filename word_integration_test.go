//go:build integration

// SPDX-License-Identifier: LGPL-2.1-only

package libraw

import (
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"image"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestReferenceWordRawSamples(t *testing.T) {
	testReferenceRawSamples(t, "olympus-word16le-high12", 15_859_904)
}

func TestReferenceLegacyRawSamples(t *testing.T) {
	testReferenceRawSamples(t, "olympus-legacy-predictive12", 20_832_000)
}

func testReferenceRawSamples(t *testing.T, layout string, expectedSamples int) {
	t.Helper()
	var manifest struct {
		Fixtures []struct {
			ID, SHA256, Layout string
			Capabilities       struct {
				ProductionDecode bool `json:"production_decode"`
			}
		}
	}
	var index struct {
		Fixtures map[string]struct {
			InputSHA256   string `json:"input_sha256"`
			SamplesSHA256 string `json:"samples_u16le_sha256"`
			SampleCount   int    `json:"sample_count"`
			Artifacts     map[string]struct {
				SHA256 string
				Bytes  int64
			}
		}
	}
	readJSON(t, "testdata/fixtures.json", &manifest)
	readJSON(t, "testdata/reference/index.json", &index)
	dir := os.Getenv("ORF_FIXTURE_DIR")
	if dir == "" {
		dir = ".cache/fixtures"
	}
	fixtures, samples := 0, 0
	for _, fixture := range manifest.Fixtures {
		if fixture.Layout != layout {
			continue
		}
		fixtures++
		t.Run(fixture.ID, func(t *testing.T) {
			oracle, exists := index.Fixtures[fixture.ID]
			if !exists || oracle.InputSHA256 != fixture.SHA256 || !fixture.Capabilities.ProductionDecode {
				t.Fatal("fixture manifest/index/capability disagreement")
			}
			input, err := os.Open(filepath.Join(dir, fixture.ID+".orf"))
			if err != nil {
				t.Fatalf("required fixture missing; run reference fetch: %v", err)
			}
			defer input.Close()
			info, err := input.Stat()
			if err != nil {
				t.Fatal(err)
			}
			inputHash := sha256.New()
			if _, err := io.Copy(inputHash, input); err != nil || hex.EncodeToString(inputHash.Sum(nil)) != fixture.SHA256 {
				t.Fatalf("input hash/read failure: %v", err)
			}
			file, err := Open(input, info.Size(), OpenOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if !file.Capabilities().Raw.Available || file.Capabilities().Render.Available {
				t.Fatal("incorrect raw capability")
			}
			before := file.Metadata()
			raw, err := file.DecodeRaw(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			var reference struct {
				Libraw struct {
					RawWidth     int    `json:"raw_width"`
					RawHeight    int    `json:"raw_height"`
					ValidBits    uint8  `json:"valid_bits"`
					SampleCount  int    `json:"sample_count"`
					ExportRegion [4]int `json:"export_region"`
					InsetCrop    [4]int `json:"inset_crop"`
				}
			}
			readJSON(t, "testdata/reference/"+fixture.ID+".json", &reference)
			native := reference.Libraw
			crop := image.Rect(native.InsetCrop[0], native.InsetCrop[1], native.InsetCrop[0]+native.InsetCrop[2], native.InsetCrop[1]+native.InsetCrop[3])
			if raw.Width != native.RawWidth || raw.Height != native.RawHeight || raw.Stride != native.RawWidth ||
				len(raw.Pixels) != oracle.SampleCount || len(raw.Pixels) != native.SampleCount ||
				native.ExportRegion != [4]int{0, 0, raw.Width, raw.Height} || raw.ActiveArea != crop ||
				raw.BitDepth != native.ValidBits || raw.CFA != before.CFA.Value ||
				raw.Orientation != before.Orientation.Value || raw.Calibration != before.Calibration ||
				!reflect.DeepEqual(before, file.Metadata()) {
				t.Fatal("raw geometry/calibration/metadata differs from pinned expectations")
			}
			path := filepath.Join("testdata/reference", fixture.ID+".u16le.gz")
			golden, err := os.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer golden.Close()
			artifact, exists := oracle.Artifacts[fixture.ID+".u16le.gz"]
			goldenInfo, err := golden.Stat()
			if !exists || err != nil || goldenInfo.Size() != artifact.Bytes {
				t.Fatalf("golden artifact size/index: %v", err)
			}
			compressedHash := sha256.New()
			stream, err := gzip.NewReader(io.TeeReader(golden, compressedHash))
			if err != nil {
				t.Fatal(err)
			}
			defer stream.Close()
			row := make([]byte, raw.Width*2)
			decodedRow := make([]byte, len(row))
			expectedHash, actualHash := sha256.New(), sha256.New()
			var comparison sampleComparison
			for y := range raw.Height {
				if _, err := io.ReadFull(stream, row); err != nil {
					t.Fatalf("golden row %d: %v", y, err)
				}
				expectedHash.Write(row)
				for x := range raw.Width {
					i := y*raw.Width + x
					expected := binary.LittleEndian.Uint16(row[x*2:])
					comparison.add(i, raw.Pixels[i], expected)
					binary.LittleEndian.PutUint16(decodedRow[x*2:], raw.Pixels[i])
				}
				actualHash.Write(decodedRow)
			}
			// Reading to EOF verifies the gzip trailer, not only its sample bytes.
			var extra [1]byte
			if n, err := stream.Read(extra[:]); n != 0 || err != io.EOF {
				t.Fatalf("golden trailing data/checksum: n=%d err=%v", n, err)
			}
			if comparison.mismatches != 0 || comparison.maxError != 0 {
				t.Fatal(comparison.summary(raw.Width, layout))
			}
			if hex.EncodeToString(compressedHash.Sum(nil)) != artifact.SHA256 ||
				hex.EncodeToString(expectedHash.Sum(nil)) != oracle.SamplesSHA256 ||
				hex.EncodeToString(actualHash.Sum(nil)) != oracle.SamplesSHA256 {
				t.Fatal("compressed artifact or full sensor sample hash mismatch")
			}
			if layout == "olympus-legacy-predictive12" {
				bits := file.legacyReader(make([]byte, legacyBufferBytes))
				if err := decodeLegacyRows(context.Background(), &bits, raw.Pixels, raw.Width, raw.Width, raw.Height); err != nil {
					t.Fatal(err)
				}
				expectedBits := map[string]uint64{"olympus-e-410-1173": 77_063_789, "olympus-e-510-5449": 81_447_495}
				if bits.consumed != expectedBits[fixture.ID] {
					t.Fatalf("coded bit count=%d want=%d", bits.consumed, expectedBits[fixture.ID])
				}
				required := uint64(file.plan.headerBytes) + (bits.consumed+7)/8
				t.Logf("consumed=%d bits; required strip bytes=%d; opaque trailing bytes=%d; unused last-byte bits=%d",
					bits.consumed, required, file.plan.strips[0].length-required, (8-bits.consumed%8)%8)
				boundedFile := *file
				boundedFile.plan.strips = append([]stripPlan(nil), file.plan.strips...)
				boundedFile.plan.strips[0].length = required
				exact, err := boundedFile.DecodeRaw(context.Background())
				if err != nil || !reflect.DeepEqual(exact.Pixels, raw.Pixels) {
					t.Fatalf("exactly exhausted stream failed or required lookahead: %v", err)
				}
				boundedFile.plan.strips[0].length--
				if partial, err := boundedFile.DecodeRaw(context.Background()); partial != nil || !errors.Is(err, ErrTruncated) {
					t.Fatalf("missing final field bits zero-filled or read past strip: %+v %v", partial, err)
				}
			}
			samples += len(raw.Pixels)
			t.Logf("%d exact sensor samples; sha256=%s", len(raw.Pixels), oracle.SamplesSHA256)
		})
	}
	if fixtures != 2 || samples != expectedSamples {
		t.Fatalf("required %s evidence incomplete: %d fixtures, %d samples", layout, fixtures, samples)
	}
}

func BenchmarkReferenceDecode(b *testing.B) {
	benchmarkReferenceOperation(b, true)
}

func BenchmarkReferenceOpen(b *testing.B) {
	benchmarkReferenceOperation(b, false)
}

func benchmarkReferenceOperation(b *testing.B, decode bool) {
	b.Helper()
	var manifest struct {
		Fixtures []struct{ ID, SHA256, Layout string }
	}
	readJSON(b, "testdata/fixtures.json", &manifest)
	dir := os.Getenv("ORF_FIXTURE_DIR")
	if dir == "" {
		dir = ".cache/fixtures"
	}
	for _, fixture := range manifest.Fixtures {
		b.Run(fixture.ID, func(b *testing.B) {
			input, err := os.Open(filepath.Join(dir, fixture.ID+".orf"))
			if err != nil {
				b.Fatal(err)
			}
			defer input.Close()
			info, err := input.Stat()
			if err != nil {
				b.Fatal(err)
			}
			hash := sha256.New()
			if _, err := io.Copy(hash, input); err != nil || hex.EncodeToString(hash.Sum(nil)) != fixture.SHA256 {
				b.Fatalf("benchmark input hash/read mismatch: %v", err)
			}
			file, err := Open(input, info.Size(), OpenOptions{})
			if err != nil {
				b.Fatal(err)
			}
			b.Logf("input_sha256=%s dimensions=%dx%d", fixture.SHA256, file.plan.width, file.plan.height)
			b.ReportAllocs()
			if decode {
				b.SetBytes(int64(file.plan.outputBytes))
			}
			b.ResetTimer()
			for b.Loop() {
				if decode {
					if _, err := file.DecodeRaw(context.Background()); err != nil {
						b.Fatal(err)
					}
				} else {
					if _, err := Open(input, info.Size(), OpenOptions{}); err != nil {
						b.Fatal(err)
					}
				}
			}
			if decode {
				b.ReportMetric(float64(file.plan.width*file.plan.height)*float64(b.N)/b.Elapsed().Seconds()/1e6, "MP/s")
			}
		})
	}
}
