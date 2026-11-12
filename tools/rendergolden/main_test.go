// SPDX-License-Identifier: LGPL-2.1-only

package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	libraw "github.com/otuschhoff/libraw-go"
)

func TestCertificationCannotPassWithOnePhotoOrCopies(t *testing.T) {
	var cases []renderCase
	for i, model := range []string{"E-1", "E-400", "E-410", "E-510"} {
		cases = append(cases, renderCase{ID: model, ArchiveID: i + 1,
			InputSHA256: strings.Repeat(string(rune('a'+i)), 64), Metadata: libraw.Metadata{
				NormalizedModel: model, CameraID: libraw.Value[string]{Value: model},
				CaptureMode: libraw.Value[string]{Value: "same-mode"},
			}})
	}
	if err := certification(cases); err == nil || !strings.Contains(err.Error(), "BLOCKED") {
		t.Fatal("one photo per target silently certified", err)
	}
	copies := append(append([]renderCase{}, cases...), cases...)
	if err := certification(copies); err == nil {
		t.Fatal("copies counted as independent inputs")
	}
	for _, item := range cases {
		item.InputSHA256 = strings.Repeat("f", 64)
		copies = append(copies, item)
	}
	if err := certification(copies); err == nil {
		t.Fatal("different bytes with the same archive identity counted as separate originals")
	}
	for _, item := range cases {
		item.InputSHA256 = strings.Repeat("f", 64)
		item.ArchiveID += 100
		copies = append(copies, item)
	}
	if err := certification(copies); err != nil {
		t.Fatal("two distinct originals per mode rejected", err)
	}
	copies[0].Metadata.CaptureMode.Value = "another-mode"
	if err := certification(copies); err == nil {
		t.Fatal("different capture modes pooled for certification")
	}
	if err := certification(nil); err == nil {
		t.Fatal("empty corpus certified")
	}
}

func TestCommittedProvenanceAndBlockedCertification(t *testing.T) {
	t.Chdir("../..")
	if _, _, err := loadInputs(); err != nil {
		t.Fatal(err)
	}
	var index renderIndex
	if err := readJSON(filepath.Join(baselineDir, "index.json"), &index); err != nil {
		t.Fatal(err)
	}
	err := certification(index.Cases)
	if err == nil || !strings.Contains(err.Error(), "BLOCKED") {
		t.Fatal("missing second originals must keep release blocked", err)
	}
	for _, model := range []string{"E-1/", "E-400/", "E-410/", "E-510/"} {
		if !strings.Contains(err.Error(), model) {
			t.Fatal("missing camera/mode gap not reported", model, err)
		}
	}
	t.Log(err)
	for _, mutate := range []func(*renderIndex){
		func(i *renderIndex) { i.ImageLicense = "unknown" },
		func(i *renderIndex) { i.Platform = "" },
		func(i *renderIndex) { i.Cases = i.Cases[:3] },
		func(i *renderIndex) { i.RendererSources["render.go"] = "bad" },
		func(i *renderIndex) { i.Cases[0].ID = "../escape" },
		func(i *renderIndex) { i.Cases[0].Metadata.NormalizedModel = "invented" },
		func(i *renderIndex) { i.Cases[0].Metadata.CaptureMode.Value = "another mode" },
		func(i *renderIndex) { i.Cases[0].Metadata.Geometry.Width++ },
		func(i *renderIndex) { i.Cases[0].Options.ExposureEV = 1 },
	} {
		var changed renderIndex
		if err := readJSON(filepath.Join(baselineDir, "index.json"), &changed); err != nil {
			t.Fatal(err)
		}
		mutate(&changed)
		dir := t.TempDir()
		data, err := json.Marshal(changed)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "index.json"), data, 0600); err != nil {
			t.Fatal(err)
		}
		if err := verify(dir); err == nil {
			t.Fatal("malformed provenance/options accepted")
		}
	}
}

func TestGenerationFailsWithoutPublishingOrOverwriting(t *testing.T) {
	t.Chdir("../..")
	dir := t.TempDir()
	if err := generate(dir); !errors.Is(err, os.ErrExist) {
		t.Fatal("existing directory not protected", err)
	}
	if runtime.Version() != "go1.26.0" {
		if err := run([]string{"generate", filepath.Join(dir, "wrong-toolchain")}); err == nil ||
			!strings.Contains(err.Error(), "generation requires go1.26.0") {
			t.Fatal("unpinned toolchain permitted freezing", err)
		}
	}
	input := filepath.Join(dir, "not-orf")
	if err := os.WriteFile(input, []byte("not an ORF"), 0600); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "must-not-exist.png")
	if _, err := generateCase(fixture{SHA256: strings.Repeat("0", 64)}, rawIndex{}, input, out); err == nil {
		t.Fatal("wrong input hash accepted")
	}
	hash, _, err := digest(input)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := generateCase(fixture{SHA256: hash}, rawIndex{}, input, out); !errors.Is(err, libraw.ErrNotORF) {
		t.Fatal("non-ORF input accepted", err)
	}
	if _, err := os.Stat(out); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("failed generation published a PNG")
	}
	if err := readJSON(input, &renderIndex{}); err == nil {
		t.Fatal("malformed JSON accepted")
	}
}

func TestPNGVerificationRejectsDriftAndEightBitOutput(t *testing.T) {
	path := filepath.Join(t.TempDir(), "baseline.png")
	rgb := image.NewNRGBA64(image.Rect(0, 0, 2, 2))
	for y := range 2 {
		for x := range 2 {
			rgb.SetNRGBA64(x, y, color.NRGBA64{R: uint16(x*1000 + 1), G: uint16(y*1000 + 2), B: 12345, A: 65535})
		}
	}
	write := func(img image.Image) {
		t.Helper()
		file, err := os.Create(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := png.Encode(file, img); err != nil {
			t.Fatal(err)
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
	}
	write(rgb)
	hash, size, err := digest(path)
	if err != nil {
		t.Fatal(err)
	}
	pixels := sha256.Sum256(rgb.Pix)
	item := renderCase{Width: 2, Height: 2, PNGSHA256: hash, PNGBytes: size, PixelsSHA256: hex.EncodeToString(pixels[:])}
	if err := verifyPNG(path, item); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*renderCase){
		func(c *renderCase) { c.PNGBytes++ }, func(c *renderCase) { c.Width++ },
		func(c *renderCase) { c.PixelsSHA256 = strings.Repeat("0", 64) },
	} {
		changed := item
		mutate(&changed)
		if err := verifyPNG(path, changed); err == nil {
			t.Fatal("drifted regression accepted")
		}
	}
	write(image.NewNRGBA(image.Rect(0, 0, 2, 2)))
	item.PNGSHA256, item.PNGBytes, _ = digest(path)
	if err := verifyPNG(path, item); err == nil {
		t.Fatal("8-bit PNG accepted as 16-bit regression")
	}
	rgb.SetNRGBA64(0, 0, color.NRGBA64{R: 123, A: 1})
	write(rgb)
	item.PNGSHA256, item.PNGBytes, _ = digest(path)
	if err := verifyPNG(path, item); err == nil {
		t.Fatal("nonopaque regression accepted")
	}
}

func TestArgumentsAndDigests(t *testing.T) {
	for _, args := range [][]string{nil, {"unknown"}, {"generate"}} {
		if err := run(args); err == nil {
			t.Fatal("bad arguments accepted")
		}
	}
	if validDigest("bad") || !validDigest(strings.Repeat("0", 64)) {
		t.Fatal("invalid digest validation")
	}
	if _, _, err := digest(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("missing artifact silently accepted")
	}
}
