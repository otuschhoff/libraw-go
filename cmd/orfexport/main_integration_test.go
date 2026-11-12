//go:build integration

// SPDX-License-Identifier: LGPL-2.1-only

package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	libraw "github.com/otuschhoff/libraw-go"
)

func TestReferenceExports(t *testing.T) {
	dir := os.Getenv("ORF_FIXTURE_DIR")
	if dir == "" {
		dir = ".cache/fixtures"
	}
	if !filepath.IsAbs(dir) {
		dir = filepath.Join("../..", dir)
	}
	inputPath := filepath.Join(dir, "olympus-e-410-1173.orf")
	input, err := os.Open(inputPath)
	if err != nil {
		t.Fatal("required fixture missing; run reference fetch", err)
	}
	defer input.Close()
	info, err := input.Stat()
	if err != nil {
		t.Fatal(err)
	}
	file, err := libraw.Open(input, info.Size(), libraw.OpenOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var oracle struct {
		Fixtures map[string]struct {
			SamplesSHA256 string `json:"samples_u16le_sha256"`
			SampleCount   int    `json:"sample_count"`
		}
	}
	index, err := os.ReadFile("../../testdata/reference/index.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(index, &oracle); err != nil {
		t.Fatal(err)
	}
	for _, format := range []string{"raw", "preview", "png"} {
		t.Run(format, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "output")
			var stderr bytes.Buffer
			if code := run(context.Background(), []string{"-format", format, "-output", path, inputPath}, &stderr); code != 0 || stderr.Len() != 0 {
				t.Fatalf("export failed: exit=%d, %s", code, &stderr)
			}
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			switch format {
			case "raw":
				want := oracle.Fixtures["olympus-e-410-1173"]
				hash := sha256.Sum256(data)
				if len(data) != want.SampleCount*2 || hex.EncodeToString(hash[:]) != want.SamplesSHA256 {
					t.Fatal("export changed sensor codes/geometry/byte order")
				}
			case "preview":
				want, err := file.Preview(context.Background())
				if err != nil || !bytes.Equal(data, want.JPEG) {
					t.Fatal("export re-encoded/substituted embedded preview", err)
				}
			case "png":
				decoded, err := png.Decode(bytes.NewReader(data))
				if err != nil {
					t.Fatal(err)
				}
				var pixels []byte
				switch value := decoded.(type) {
				case *image.RGBA64:
					pixels = value.Pix
				case *image.NRGBA64:
					pixels = value.Pix
				default:
					t.Fatal("PNG lost 16-bit channels")
				}
				raw, err := file.DecodeRaw(context.Background())
				if err != nil {
					t.Fatal(err)
				}
				want, err := libraw.Render(context.Background(), raw, libraw.RenderOptions{})
				if err != nil || decoded.Bounds() != want.Rect || !bytes.Equal(pixels, want.Pix) {
					t.Fatal("PNG differs from independent RAW development", err)
				}
			}
		})
	}
}

func TestEstimatedWhiteExportRequiresExplicitConsent(t *testing.T) {
	dir := os.Getenv("ORF_FIXTURE_DIR")
	if dir == "" {
		dir = ".cache/fixtures"
	}
	if !filepath.IsAbs(dir) {
		dir = filepath.Join("../..", dir)
	}
	path := filepath.Join(dir, "olympus-e-1-1800.orf")
	output := filepath.Join(t.TempDir(), "out.png")
	if err := export(context.Background(), path, output, "png", libraw.RenderOptions{}); !errors.Is(err, libraw.ErrCalibrationUnavailable) {
		t.Fatal("estimated saturation accepted without consent", err)
	}
	if _, err := os.Stat(output); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("rejected rendering published an output")
	}
	var stderr bytes.Buffer
	if code := run(context.Background(), []string{"-format", "png", "-output", output, "-allow-estimated-white", path}, &stderr); code != 0 {
		t.Fatalf("explicit estimated-white consent rejected: exit=%d, %s", code, &stderr)
	}
}
