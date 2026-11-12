// SPDX-License-Identifier: LGPL-2.1-only

// Development-only rendered regression freezing. This is not a RAW oracle.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	libraw "github.com/otuschhoff/libraw-go"
)

const baselineDir = "testdata/render"

type fixture struct {
	ID, Model, SHA256, Layout string
	ArchiveID                 int    `json:"archive_id"`
	SpecialMode               string `json:"special_mode"`
	DriveMode                 string `json:"drive_mode"`
}

type manifest struct {
	SchemaVersion   int `json:"schema_version"`
	License         string
	LicenseURL      string `json:"license_url"`
	LicenseEvidence string `json:"license_evidence"`
	Fixtures        []fixture
}

type rawIndex struct {
	SchemaVersion int `json:"schema_version"`
	Fixtures      map[string]struct {
		InputSHA256   string `json:"input_sha256"`
		SamplesSHA256 string `json:"samples_u16le_sha256"`
		SampleCount   int    `json:"sample_count"`
	}
}

type renderCase struct {
	ID               string               `json:"id"`
	ArchiveID        int                  `json:"archive_id"`
	InputSHA256      string               `json:"input_sha256"`
	RawSamplesSHA256 string               `json:"raw_samples_sha256"`
	Metadata         libraw.Metadata      `json:"metadata"`
	Options          libraw.RenderOptions `json:"options"`
	Width            int                  `json:"width"`
	Height           int                  `json:"height"`
	PNGBytes         int64                `json:"png_bytes"`
	PNGSHA256        string               `json:"png_sha256"`
	PixelsSHA256     string               `json:"nrgba64_sha256"`
}

type renderIndex struct {
	SchemaVersion    int               `json:"schema_version"`
	ImageLicense     string            `json:"image_license"`
	MetadataLicense  string            `json:"metadata_license"`
	Generator        string            `json:"generator"`
	GoVersion        string            `json:"go_version"`
	Platform         string            `json:"platform"`
	RawIndexSHA256   string            `json:"raw_index_sha256"`
	ProvenanceSHA256 string            `json:"provenance_sha256"`
	RendererSources  map[string]string `json:"renderer_sources_sha256"`
	Cases            []renderCase      `json:"cases"`
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "rendergolden:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 1 && args[0] == "verify" {
		return verify(baselineDir)
	}
	if len(args) == 1 && args[0] == "check-certification" {
		if err := verify(baselineDir); err != nil {
			return err
		}
		var index renderIndex
		if err := readJSON(filepath.Join(baselineDir, "index.json"), &index); err != nil {
			return err
		}
		return certification(index.Cases)
	}
	if len(args) == 2 && args[0] == "generate" {
		if runtime.Version() != "go1.26.0" {
			return fmt.Errorf("generation requires go1.26.0, got %s", runtime.Version())
		}
		return generate(args[1])
	}
	return errors.New("usage: go run ./tools/rendergolden {generate NEW_DIRECTORY|verify|check-certification}")
}

func readJSON(path string, value any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, value); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return nil
}

func digest(path string) (string, int64, error) {
	input, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer input.Close()
	hash := sha256.New()
	n, err := io.Copy(hash, input)
	return hex.EncodeToString(hash.Sum(nil)), n, err
}

func loadInputs() (manifest, rawIndex, error) {
	var corpus manifest
	var oracle rawIndex
	if err := readJSON("testdata/fixtures.json", &corpus); err != nil {
		return corpus, oracle, err
	}
	if err := readJSON("testdata/reference/index.json", &oracle); err != nil {
		return corpus, oracle, err
	}
	if corpus.SchemaVersion != 1 || oracle.SchemaVersion != 1 || corpus.License != "CC0-1.0" || len(corpus.Fixtures) < 4 ||
		corpus.LicenseURL != "https://creativecommons.org/publicdomain/zero/1.0/" {
		return corpus, oracle, errors.New("unsupported/incomplete fixture manifest or license")
	}
	var evidence struct {
		Source string
		Data   [][]any
	}
	if err := readJSON(corpus.LicenseEvidence, &evidence); err != nil {
		return corpus, oracle, err
	}
	if evidence.Source != "https://raw.pixls.us/json/getrepository.php" {
		return corpus, oracle, errors.New("unexpected fixture evidence source")
	}
	seen := map[string]bool{}
	for _, f := range corpus.Fixtures {
		matches := 0
		for _, row := range evidence.Data {
			if len(row) != 9 {
				return corpus, oracle, errors.New("unexpected fixture evidence row shape")
			}
			model, modelOK := row[1].(string)
			license, licenseOK := row[5].(string)
			source, sourceOK := row[7].(string)
			if !modelOK || !licenseOK || !sourceOK {
				return corpus, oracle, errors.New("unexpected fixture evidence cell types")
			}
			if strings.Contains(source, fmt.Sprintf("/getfile.php/%d/", f.ArchiveID)) {
				if model != f.Model || !strings.Contains(license, "href='"+corpus.LicenseURL+"'") ||
					!strings.Contains(source, f.SHA256) {
					return corpus, oracle, errors.New("fixture differs from its individual CC0 dedication")
				}
				matches++
			}
		}
		native, ok := oracle.Fixtures[f.ID]
		if f.ID == "" || filepath.Base(f.ID) != f.ID || strings.ContainsAny(f.ID, `/\`) ||
			f.ID == "." || f.ID == ".." || seen[f.ID] || !validDigest(f.SHA256) ||
			!ok || native.InputSHA256 != f.SHA256 || !validDigest(native.SamplesSHA256) ||
			native.SampleCount <= 0 || native.SampleCount > 100_000_000 || matches != 1 {
			return corpus, oracle, fmt.Errorf("%s: fixture identity, RAW oracle or CC0 evidence mismatch", f.ID)
		}
		seen[f.ID] = true
	}
	return corpus, oracle, nil
}

func validDigest(value string) bool {
	data, err := hex.DecodeString(value)
	return err == nil && len(data) == sha256.Size && strings.ToLower(value) == value
}

func generate(output string) error {
	corpus, oracle, err := loadInputs()
	if err != nil {
		return err
	}
	index := renderIndex{SchemaVersion: 1, ImageLicense: "CC0-1.0", MetadataLicense: "LGPL-2.1-only", Generator: "libraw-go-render-v1",
		GoVersion: runtime.Version(), Platform: runtime.GOOS + "/" + runtime.GOARCH, RendererSources: map[string]string{}}
	index.RawIndexSHA256, _, err = digest("testdata/reference/index.json")
	if err != nil {
		return err
	}
	index.ProvenanceSHA256, _, err = digest(corpus.LicenseEvidence)
	if err != nil {
		return err
	}
	for _, path := range []string{"render.go", "color.go", "profiles.go"} {
		hash, _, err := digest(path)
		if err != nil {
			return err
		}
		index.RendererSources[path] = hash
	}
	if err := os.Mkdir(output, 0755); err != nil {
		return fmt.Errorf("output must be a new directory: %w", err)
	}
	dir := os.Getenv("ORF_FIXTURE_DIR")
	if dir == "" {
		dir = ".cache/fixtures"
	}
	for _, f := range corpus.Fixtures {
		item, err := generateCase(f, oracle, filepath.Join(dir, f.ID+".orf"), filepath.Join(output, f.ID+".png"))
		if err != nil {
			return fmt.Errorf("%s: %w (candidate directory retained for inspection)", f.ID, err)
		}
		index.Cases = append(index.Cases, item)
		fmt.Printf("%s: %dx%d PNG frozen after exact RAW hash verification\n", f.ID, item.Width, item.Height)
	}
	data, err := json.MarshalIndent(index, "", "  ")
	if err != nil {
		return err
	}
	out, err := os.OpenFile(filepath.Join(output, "index.json"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
	if err != nil {
		return err
	}
	_, writeErr := out.Write(append(data, '\n'))
	return errors.Join(writeErr, out.Close())
}

func generateCase(f fixture, oracle rawIndex, inputPath, outputPath string) (renderCase, error) {
	var item renderCase
	hash, _, err := digest(inputPath)
	if err != nil {
		return item, err
	}
	if hash != f.SHA256 {
		return item, fmt.Errorf("input digest %s != %s", hash, f.SHA256)
	}
	input, err := os.Open(inputPath)
	if err != nil {
		return item, err
	}
	defer input.Close()
	info, err := input.Stat()
	if err != nil {
		return item, err
	}
	file, err := libraw.Open(input, info.Size(), libraw.OpenOptions{})
	if err != nil {
		return item, err
	}
	metadata := file.Metadata()
	if metadata.NormalizedModel != f.Model || metadata.Layout.Value != f.Layout {
		return item, errors.New("manifest identity/layout differs from parsed input")
	}
	raw, err := file.DecodeRaw(context.Background())
	if err != nil {
		return item, err
	}
	samples := sha256.New()
	row := make([]byte, raw.Width*2)
	for y := range raw.Height {
		for x := range raw.Width {
			binary.LittleEndian.PutUint16(row[x*2:], raw.Pixels[y*raw.Stride+x])
		}
		samples.Write(row)
	}
	native := oracle.Fixtures[f.ID]
	sampleHash := hex.EncodeToString(samples.Sum(nil))
	if sampleHash != native.SamplesSHA256 || raw.Width*raw.Height != native.SampleCount {
		return item, errors.New("RAW samples differ from pinned independent oracle; refusing to freeze rendering")
	}
	opts := libraw.RenderOptions{AllowEstimatedWhite: raw.Calibration.White.Origin == libraw.OriginEstimate}
	rgb, err := libraw.Render(context.Background(), raw, opts)
	if err != nil {
		return item, err
	}
	out, err := os.OpenFile(outputPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
	if err != nil {
		return item, err
	}
	if err := errors.Join(png.Encode(out, rgb), out.Close()); err != nil {
		return item, err
	}
	pngHash, size, err := digest(outputPath)
	if err != nil {
		return item, err
	}
	pixels := sha256.Sum256(rgb.Pix)
	return renderCase{ID: f.ID, ArchiveID: f.ArchiveID, InputSHA256: f.SHA256, RawSamplesSHA256: sampleHash,
		Metadata: metadata, Options: opts, Width: rgb.Rect.Dx(), Height: rgb.Rect.Dy(),
		PNGBytes: size, PNGSHA256: pngHash, PixelsSHA256: hex.EncodeToString(pixels[:])}, nil
}

func verify(dir string) error {
	corpus, oracle, err := loadInputs()
	if err != nil {
		return err
	}
	var index renderIndex
	if err := readJSON(filepath.Join(dir, "index.json"), &index); err != nil {
		return err
	}
	if index.SchemaVersion != 1 || index.ImageLicense != "CC0-1.0" || index.MetadataLicense != "LGPL-2.1-only" ||
		index.Generator != "libraw-go-render-v1" ||
		index.GoVersion != "go1.26.0" || len(index.Cases) != len(corpus.Fixtures) || len(index.RendererSources) != 3 {
		return errors.New("render index schema, provenance or coverage mismatch")
	}
	for _, path := range []string{"render.go", "color.go", "profiles.go"} {
		if !validDigest(index.RendererSources[path]) {
			return errors.New("missing/invalid renderer source provenance")
		}
	}
	switch index.Platform {
	case "linux/amd64", "linux/arm64", "darwin/amd64", "darwin/arm64":
	default:
		return errors.New("unknown baseline generation platform")
	}
	for path, want := range map[string]string{"testdata/reference/index.json": index.RawIndexSHA256, corpus.LicenseEvidence: index.ProvenanceSHA256} {
		got, _, err := digest(path)
		if err != nil {
			return err
		}
		if got != want {
			return fmt.Errorf("%s: bound reference/provenance digest mismatch", path)
		}
	}
	byID := map[string]fixture{}
	for _, f := range corpus.Fixtures {
		byID[f.ID] = f
	}
	seen := map[string]bool{}
	for _, item := range index.Cases {
		f, ok := byID[item.ID]
		metadata := item.Metadata
		crop := metadata.Geometry.ActiveArea.Value
		width, height := crop.Dx(), crop.Dy()
		if metadata.Orientation.Value >= libraw.OrientationLeftTop {
			width, height = height, width
		}
		if !ok || seen[item.ID] || item.InputSHA256 != f.SHA256 || item.ArchiveID != f.ArchiveID ||
			item.RawSamplesSHA256 != oracle.Fixtures[item.ID].SamplesSHA256 ||
			metadata.NormalizedModel != f.Model || metadata.Layout.Value != f.Layout ||
			metadata.NormalizedMake != "Olympus" || !metadata.CameraID.Present() || !metadata.CaptureMode.Present() ||
			metadata.CaptureMode.Value != "SpecialMode="+f.SpecialMode+";DriveMode="+f.DriveMode ||
			metadata.Geometry.Width <= 0 || metadata.Geometry.Width > 32768 ||
			metadata.Geometry.Height <= 0 || metadata.Geometry.Height > 32768 ||
			int64(metadata.Geometry.Width)*int64(metadata.Geometry.Height) != int64(oracle.Fixtures[item.ID].SampleCount) ||
			!metadata.Geometry.ActiveArea.Present() || crop.Empty() ||
			!crop.In(image.Rect(0, 0, metadata.Geometry.Width, metadata.Geometry.Height)) ||
			item.Width != width || item.Height != height ||
			item.Options != (libraw.RenderOptions{AllowEstimatedWhite: metadata.Calibration.White.Origin == libraw.OriginEstimate}) ||
			item.Width <= 0 || item.Height <= 0 || item.Width > 32768 || item.Height > 32768 ||
			int64(item.Width)*int64(item.Height) > 100_000_000 {
			return fmt.Errorf("%s: invalid/duplicate rendered case or source binding", item.ID)
		}
		seen[item.ID] = true
		if err := verifyPNG(filepath.Join(dir, item.ID+".png"), item); err != nil {
			return fmt.Errorf("%s: %w", item.ID, err)
		}
		fmt.Printf("%s: PNG bytes, 16-bit pixels and RAW/input bindings verified\n", item.ID)
	}
	return nil
}

func verifyPNG(path string, item renderCase) error {
	hash, size, err := digest(path)
	if err != nil {
		return err
	}
	if hash != item.PNGSHA256 || size != item.PNGBytes || !validDigest(item.PixelsSHA256) {
		return errors.New("PNG artifact digest/length mismatch")
	}
	input, err := os.Open(path)
	if err != nil {
		return err
	}
	defer input.Close()
	config, err := png.DecodeConfig(input)
	if err != nil || config.Width != item.Width || config.Height != item.Height {
		return errors.Join(err, errors.New("PNG dimensions mismatch"))
	}
	if _, err := input.Seek(0, io.SeekStart); err != nil {
		return err
	}
	decoded, err := png.Decode(input)
	if err != nil {
		return err
	}
	var pixels []byte
	switch value := decoded.(type) {
	case *image.RGBA64:
		pixels = value.Pix
	case *image.NRGBA64:
		pixels = value.Pix
	default:
		return errors.New("regression PNG must retain 16-bit channels")
	}
	for i := 6; i < len(pixels); i += 8 {
		if binary.BigEndian.Uint16(pixels[i:]) != 65535 {
			return errors.New("nonopaque regression PNG")
		}
	}
	pixelHash := sha256.Sum256(pixels)
	if hex.EncodeToString(pixelHash[:]) != item.PixelsSHA256 {
		return errors.New("decoded NRGBA64 digest mismatch")
	}
	return nil
}

func certification(cases []renderCase) error {
	groups := map[string]map[string]bool{}
	archives := map[string]map[int]bool{}
	models := map[string]bool{}
	for _, item := range cases {
		m := item.Metadata
		models[m.NormalizedModel] = true
		key := fmt.Sprintf("%s/%s/%s/%s/%dx%d", m.NormalizedModel, m.CameraID.Value, m.CaptureMode.Value,
			m.Layout.Value, m.Geometry.Width, m.Geometry.Height)
		if groups[key] == nil {
			groups[key] = map[string]bool{}
			archives[key] = map[int]bool{}
		}
		if !validDigest(item.InputSHA256) || item.ArchiveID <= 0 {
			return errors.New("invalid input/source identity in certification evidence")
		}
		groups[key][item.InputSHA256] = true
		archives[key][item.ArchiveID] = true
	}
	var findings []string
	for _, model := range []string{"E-1", "E-400", "E-410", "E-510"} {
		if !models[model] {
			findings = append(findings, model+": missing planned camera")
		}
	}
	for key, hashes := range groups {
		count := min(len(hashes), len(archives[key]))
		if count < 2 {
			findings = append(findings, fmt.Sprintf("%s: %d distinct input/source(s), need at least 2 independently captured licensed originals", key, count))
		}
	}
	sort.Strings(findings)
	if len(findings) != 0 {
		return fmt.Errorf("camera certification and M1+M2 release BLOCKED:\n%s", strings.Join(findings, "\n"))
	}
	return nil
}
