// SPDX-License-Identifier: LGPL-2.1-only
//
// Development-only phase-zero tooling. Run by filename, without initializing
// the production module or depending on any third-party Go package.
package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
)

const cache = ".cache/reference"
const fixtureCache = ".cache/fixtures"
const goldenDir = "testdata/reference"

type source struct {
	Version             string   `json:"version"`
	Commit              string   `json:"commit"`
	URL                 string   `json:"url"`
	SHA256              string   `json:"sha256"`
	Archive             string   `json:"archive"`
	Directory           string   `json:"directory"`
	Configure           []string `json:"configure,omitempty"`
	DecoderSourceSHA256 string   `json:"decoder_source_sha256,omitempty"`
}

type lock struct {
	SchemaVersion      int    `json:"schema_version"`
	GoReferenceVersion string `json:"go_reference_version"`
	LibRaw             source `json:"libraw"`
	ExifTool           source `json:"exiftool"`
}

type fixture struct {
	ID        string `json:"id"`
	ArchiveID int    `json:"archive_id"`
	Model     string `json:"model"`
	URL       string `json:"url"`
	SHA256    string `json:"sha256"`
	Layout    string `json:"layout"`
	Decoder   string `json:"decoder"`
}

type manifest struct {
	SchemaVersion   int       `json:"schema_version"`
	License         string    `json:"license"`
	LicenseURL      string    `json:"license_url"`
	LicenseEvidence string    `json:"license_evidence"`
	Fixtures        []fixture `json:"fixtures"`
}

type nativeMetadata struct {
	Decoder     string `json:"decoder"`
	Model       string `json:"model"`
	RawWidth    int    `json:"raw_width"`
	RawHeight   int    `json:"raw_height"`
	ValidBits   int    `json:"valid_bits"`
	LoadFlags   uint   `json:"load_flags"`
	DataOffset  int64  `json:"data_offset"`
	SampleCount int64  `json:"sample_count"`
	SampleMin   uint16 `json:"sample_min"`
	SampleMax   uint16 `json:"sample_max"`
}

type artifact struct {
	SHA256 string `json:"sha256"`
	Bytes  int64  `json:"bytes"`
}

type fixtureResult struct {
	InputSHA256        string              `json:"input_sha256"`
	SamplesSHA256      string              `json:"samples_u16le_sha256"`
	SampleCount        int64               `json:"sample_count"`
	Artifacts          map[string]artifact `json:"artifacts"`
	WordMismatchCount  *int64              `json:"word_mismatch_count,omitempty"`
	StorageNonzeroLow4 *int64              `json:"storage_nonzero_low4,omitempty"`
}

type index struct {
	SchemaVersion int                      `json:"schema_version"`
	Fixtures      map[string]fixtureResult `json:"fixtures"`
}

type layoutEvidence struct {
	HeaderHex       string   `json:"header_hex"`
	ByteOrder       string   `json:"byte_order"`
	IFD0Offset      uint32   `json:"ifd0_offset"`
	IFD0Entries     uint16   `json:"ifd0_entries"`
	Width           uint32   `json:"width"`
	Height          uint32   `json:"height"`
	BitsPerSample   uint32   `json:"bits_per_sample"`
	Compression     uint32   `json:"compression"`
	Photometric     uint32   `json:"photometric"`
	RowsPerStrip    uint32   `json:"rows_per_strip"`
	StripOffsets    []uint32 `json:"strip_offsets"`
	StripByteCounts []uint32 `json:"strip_byte_counts"`
	Contiguous      bool     `json:"strips_contiguous"`
	PayloadBytes    uint64   `json:"payload_bytes"`
	PayloadPrefix   string   `json:"payload_prefix_hex"`
	ExifIFDOffset   uint32   `json:"exif_ifd_offset"`
	MakerNoteOffset uint32   `json:"maker_note_offset"`
	MakerNoteBytes  uint32   `json:"maker_note_bytes"`
	MakerNoteHeader string   `json:"maker_note_header_hex"`
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "reference:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: go run tools/reference/main.go {fetch|build|generate OUTPUT_DIR|verify|reproduce}")
	}
	var pins lock
	var corpus manifest
	if err := readJSON("tools/reference/lock.json", &pins); err != nil {
		return err
	}
	if err := readJSON("testdata/fixtures.json", &corpus); err != nil {
		return err
	}
	if err := validateConfig(pins, corpus); err != nil {
		return err
	}
	switch args[0] {
	case "fetch":
		if len(args) != 1 {
			return errors.New("fetch takes no arguments")
		}
		for _, src := range []source{pins.LibRaw, pins.ExifTool} {
			if err := download(src.URL, filepath.Join(cache, src.Archive), src.SHA256); err != nil {
				return err
			}
		}
		for _, f := range corpus.Fixtures {
			if err := download(f.URL, inputPath(f), f.SHA256); err != nil {
				return err
			}
		}
		return nil
	case "build":
		if len(args) != 1 {
			return errors.New("build takes no arguments")
		}
		return build(pins)
	case "generate":
		if len(args) != 2 {
			return errors.New("generate requires a new output directory; never overwrites goldens")
		}
		_, err := generate(pins, corpus, args[1])
		return err
	case "verify":
		if len(args) != 1 {
			return errors.New("verify takes no arguments")
		}
		return verify(corpus)
	case "reproduce":
		if len(args) != 1 {
			return errors.New("reproduce takes no arguments")
		}
		if err := verify(corpus); err != nil {
			return err
		}
		temp, err := os.MkdirTemp(cache, "reproduce-")
		if err != nil {
			return err
		}
		defer os.RemoveAll(temp)
		for pass := 1; pass <= 2; pass++ {
			generated, err := generate(pins, corpus, filepath.Join(temp, fmt.Sprint(pass)))
			if err != nil {
				return err
			}
			var expected index
			if err := readJSON(filepath.Join(goldenDir, "index.json"), &expected); err != nil {
				return err
			}
			// Build environment descriptions may differ between platforms; all
			// generated artifacts, including metadata, must match on this platform.
			want, err := json.Marshal(expected)
			if err != nil {
				return err
			}
			got, err := json.Marshal(generated)
			if err != nil {
				return err
			}
			if !bytes.Equal(want, got) {
				return fmt.Errorf("reference pass %d differs from committed index; rerun generate with a new output directory to inspect", pass)
			}
			fmt.Printf("reference pass %d: byte-identical to committed artifacts\n", pass)
		}
		return nil
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func validateConfig(pins lock, corpus manifest) error {
	if pins.SchemaVersion != 1 || corpus.SchemaVersion != 1 || len(corpus.Fixtures) < 4 ||
		corpus.License != "CC0-1.0" || pins.GoReferenceVersion != "go1.26.0" {
		return errors.New("unsupported lock/manifest schema, license, or incomplete corpus")
	}
	seen := map[string]bool{}
	for _, f := range corpus.Fixtures {
		if f.ID == "" || filepath.Base(f.ID) != f.ID || strings.ContainsAny(f.ID, `/\`) ||
			f.ID == "." || f.ID == ".." || seen[f.ID] {
			return fmt.Errorf("invalid/duplicate fixture ID %q", f.ID)
		}
		seen[f.ID] = true
		if !validDigest(f.SHA256) || !strings.HasPrefix(f.URL, "https://raw.pixls.us/getfile.php/") {
			return fmt.Errorf("%s: invalid hash or source URL", f.ID)
		}
	}
	for _, src := range []source{pins.LibRaw, pins.ExifTool} {
		if !validDigest(src.SHA256) || len(src.Commit) != 40 ||
			filepath.Base(src.Archive) != src.Archive || filepath.Base(src.Directory) != src.Directory ||
			src.Archive == "" || src.Directory == "" || src.Directory == "." || src.Directory == ".." ||
			!strings.HasPrefix(src.URL, "https://") {
			return errors.New("invalid reference source pin")
		}
	}
	return nil
}

func verifyProvenance(corpus manifest) error {
	var snapshot struct {
		Source string  `json:"source"`
		Data   [][]any `json:"data"`
	}
	if err := readJSON(corpus.LicenseEvidence, &snapshot); err != nil {
		return err
	}
	if snapshot.Source != "https://raw.pixls.us/json/getrepository.php" {
		return errors.New("unexpected fixture license evidence source")
	}
	for _, f := range corpus.Fixtures {
		matches := 0
		for _, row := range snapshot.Data {
			if len(row) != 9 {
				return errors.New("unexpected raw.pixls.us evidence row shape")
			}
			model, modelOK := row[1].(string)
			license, licenseOK := row[5].(string)
			download, downloadOK := row[7].(string)
			if !modelOK || !licenseOK || !downloadOK {
				return errors.New("unexpected raw.pixls.us evidence cell types")
			}
			if strings.Contains(download, fmt.Sprintf("/getfile.php/%d/", f.ArchiveID)) {
				matches++
				if model != f.Model || !strings.Contains(download, f.SHA256) ||
					!strings.Contains(license, "href='"+corpus.LicenseURL+"'") {
					return fmt.Errorf("%s: model, hash, or CC0 dedication differs from license evidence", f.ID)
				}
			}
		}
		if matches != 1 {
			return fmt.Errorf("%s: expected one per-file license evidence row, got %d", f.ID, matches)
		}
	}
	return nil
}

func validDigest(value string) bool {
	data, err := hex.DecodeString(value)
	return err == nil && len(data) == sha256.Size && strings.ToLower(value) == value
}

func inputPath(f fixture) string { return filepath.Join(fixtureCache, f.ID+".orf") }

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

func jsonBytes(value any) ([]byte, error) {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

func writeJSON(path string, value any) error {
	data, err := jsonBytes(value)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}

func digest(path string) (artifact, error) {
	file, err := os.Open(path)
	if err != nil {
		return artifact{}, err
	}
	defer file.Close()
	hash := sha256.New()
	n, err := io.Copy(hash, file)
	if err != nil {
		return artifact{}, err
	}
	return artifact{hex.EncodeToString(hash.Sum(nil)), n}, nil
}

func requireHash(path, want string) error {
	got, err := digest(path)
	if err != nil {
		return err
	}
	if got.SHA256 != want {
		return fmt.Errorf("%s: SHA-256 mismatch: got %s, want %s", path, got.SHA256, want)
	}
	return nil
}

func download(url, path, want string) error {
	if _, err := os.Stat(path); err == nil {
		if err := requireHash(path, want); err != nil {
			return err
		}
		fmt.Println("verified cached", path)
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	client := &http.Client{Timeout: 5 * time.Minute}
	response, err := client.Get(url)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: HTTP %s", url, response.Status)
	}
	temp, err := os.CreateTemp(filepath.Dir(path), ".download-")
	if err != nil {
		return err
	}
	defer os.Remove(temp.Name())
	n, copyErr := io.Copy(temp, io.LimitReader(response.Body, (256<<20)+1))
	closeErr := temp.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	if n > 256<<20 {
		return errors.New("download exceeds 256 MiB budget")
	}
	if err := requireHash(temp.Name(), want); err != nil {
		return err
	}
	if err := os.Rename(temp.Name(), path); err != nil {
		return err
	}
	fmt.Println("downloaded and verified", path)
	return nil
}

func extract(src source, destination string) error {
	archive := filepath.Join(cache, src.Archive)
	if err := requireHash(archive, src.SHA256); err != nil {
		return err
	}
	file, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer file.Close()
	gz, err := gzip.NewReader(file)
	if err != nil {
		return err
	}
	defer gz.Close()
	reader := tar.NewReader(gz)
	var total int64
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if header.Typeflag == tar.TypeXGlobalHeader {
			continue
		}
		name := filepath.Clean(header.Name)
		if name != src.Directory && !strings.HasPrefix(name, src.Directory+string(filepath.Separator)) {
			return fmt.Errorf("archive path outside pinned root: %q", header.Name)
		}
		path := filepath.Join(destination, name)
		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(path, 0755); err != nil {
				return err
			}
		case tar.TypeReg, tar.TypeRegA:
			total += header.Size
			if header.Size < 0 || total > 256<<20 {
				return errors.New("archive exceeds extraction budget")
			}
			if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
				return err
			}
			mode := os.FileMode(0644)
			if header.Mode&0111 != 0 {
				mode = 0755
			}
			out, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
			if err != nil {
				return err
			}
			_, copyErr := io.Copy(out, reader)
			closeErr := out.Close()
			if copyErr != nil {
				return copyErr
			}
			if closeErr != nil {
				return closeErr
			}
		default:
			return fmt.Errorf("unsupported archive entry type %d: %s", header.Typeflag, header.Name)
		}
	}
}

func command(directory, name string, args ...string) ([]byte, error) {
	cmd := exec.Command(name, args...)
	cmd.Dir = directory
	cmd.Env = append(os.Environ(), "LC_ALL=C", "TZ=UTC")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("%s %q: %w\n%s", name, args, err, stderr.String())
	}
	if stderr.Len() != 0 {
		fmt.Fprint(os.Stderr, stderr.String())
	}
	return output, nil
}

func build(pins lock) error {
	if err := os.MkdirAll(cache, 0755); err != nil {
		return err
	}
	temp, err := os.MkdirTemp(cache, "build-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(temp)
	for _, src := range []source{pins.LibRaw, pins.ExifTool} {
		if err := extract(src, temp); err != nil {
			return err
		}
	}
	libraw, err := filepath.Abs(filepath.Join(temp, pins.LibRaw.Directory))
	if err != nil {
		return err
	}
	if err := requireHash(filepath.Join(libraw, "src/decoders/decoders_dcraw.cpp"), pins.LibRaw.DecoderSourceSHA256); err != nil {
		return err
	}
	fmt.Println("configuring pinned LibRaw")
	output, err := command(libraw, "./configure", pins.LibRaw.Configure...)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(cache, "configure.log"), output, 0644); err != nil {
		return err
	}
	fmt.Println("building pinned static LibRaw")
	output, err = command(libraw, "make", "-j2")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(cache, "build.log"), output, 0644); err != nil {
		return err
	}
	target, err := filepath.Abs(filepath.Join(temp, "reference-export"))
	if err != nil {
		return err
	}
	if _, err := command(".", "c++", "-std=c++11", "-O2", "-Wall", "-Wextra", "-Werror",
		"-I"+libraw, "tools/reference/export.cpp", filepath.Join(libraw, "lib/.libs/libraw.a"), "-o", target); err != nil {
		return err
	}
	exifSource := filepath.Join(temp, pins.ExifTool.Directory)
	version, err := command(".", "perl", filepath.Join(exifSource, "exiftool"), "-ver")
	if err != nil {
		return err
	}
	if strings.TrimSpace(string(version)) != pins.ExifTool.Version {
		return errors.New("ExifTool version differs from pin")
	}
	// Each successful build publishes a fresh tool directory; previous copies
	// remain cache-only and are never trusted to build a new exporter.
	published, err := os.MkdirTemp(cache, "tools-")
	if err != nil {
		return err
	}
	if err := os.Rename(exifSource, filepath.Join(published, "exiftool")); err != nil {
		return err
	}
	if err := os.Rename(target, filepath.Join(published, "reference-export")); err != nil {
		return err
	}
	exportHash, err := digest("tools/reference/export.cpp")
	if err != nil {
		return err
	}
	toolHash, err := digest("tools/reference/main.go")
	if err != nil {
		return err
	}
	executableHash, err := digest(filepath.Join(published, "reference-export"))
	if err != nil {
		return err
	}
	compiler, err := command(".", "c++", "--version")
	if err != nil {
		return err
	}
	buildInfo := map[string]any{
		"schema_version": 1, "tools_directory": published, "goos": runtime.GOOS, "goarch": runtime.GOARCH,
		"compiler": strings.Split(string(compiler), "\n")[0], "configure": pins.LibRaw.Configure,
		"libraw_commit": pins.LibRaw.Commit, "exiftool_commit": pins.ExifTool.Commit,
		"exporter_source_sha256": exportHash.SHA256, "tool_source_sha256": toolHash.SHA256,
		"executable_sha256": executableHash.SHA256,
	}
	lockHash, err := digest("tools/reference/lock.json")
	if err != nil {
		return err
	}
	buildInfo["lock_sha256"] = lockHash.SHA256
	if err := writeJSON(filepath.Join(cache, "build.json"), buildInfo); err != nil {
		return err
	}
	fmt.Println("built", published)
	return nil
}

func generate(pins lock, corpus manifest, output string) (index, error) {
	results := index{1, map[string]fixtureResult{}}
	if err := referenceToolchain(pins, runtime.Version()); err != nil {
		return results, err
	}
	var buildInfo struct {
		ToolsDirectory string `json:"tools_directory"`
		ExporterHash   string `json:"exporter_source_sha256"`
		ToolHash       string `json:"tool_source_sha256"`
		ExecutableHash string `json:"executable_sha256"`
		LibRawCommit   string `json:"libraw_commit"`
		ExifToolCommit string `json:"exiftool_commit"`
		LockHash       string `json:"lock_sha256"`
	}
	if err := readJSON(filepath.Join(cache, "build.json"), &buildInfo); err != nil {
		return results, err
	}
	if buildInfo.LibRawCommit != pins.LibRaw.Commit || buildInfo.ExifToolCommit != pins.ExifTool.Commit {
		return results, errors.New("reference pins changed; rebuild tools")
	}
	for path, want := range map[string]string{
		"tools/reference/export.cpp":                                buildInfo.ExporterHash,
		"tools/reference/main.go":                                   buildInfo.ToolHash,
		"tools/reference/lock.json":                                 buildInfo.LockHash,
		filepath.Join(buildInfo.ToolsDirectory, "reference-export"): buildInfo.ExecutableHash,
	} {
		if err := requireHash(path, want); err != nil {
			return results, fmt.Errorf("reference build is stale: %w", err)
		}
		if err := verifyProvenance(corpus); err != nil {
			return results, err
		}
	}
	if err := os.Mkdir(output, 0755); err != nil {
		return results, fmt.Errorf("output must be a new directory: %w", err)
	}
	for _, f := range corpus.Fixtures {
		if err := requireHash(inputPath(f), f.SHA256); err != nil {
			return results, err
		}
		evidence, err := inspect(inputPath(f))
		if err != nil {
			return results, fmt.Errorf("%s: %w", f.ID, err)
		}
		path := filepath.Join(output, f.ID+".u16le")
		rawMetadata, err := command(".", filepath.Join(buildInfo.ToolsDirectory, "reference-export"), inputPath(f), path)
		if err != nil {
			return results, err
		}
		var native nativeMetadata
		if err := json.Unmarshal(rawMetadata, &native); err != nil {
			return results, err
		}
		if native.Decoder != f.Decoder || native.Model != f.Model || native.ValidBits != 12 ||
			native.RawWidth != int(evidence.Width) || native.RawHeight != int(evidence.Height) {
			return results, fmt.Errorf("%s: reference output contradicts fixture declaration", f.ID)
		}
		rawArtifact, err := digest(path)
		if err != nil {
			return results, err
		}
		if native.SampleCount <= 0 || rawArtifact.Bytes != native.SampleCount*2 {
			return results, fmt.Errorf("%s: reference sample count mismatch", f.ID)
		}
		result := fixtureResult{InputSHA256: f.SHA256, SamplesSHA256: rawArtifact.SHA256,
			SampleCount: native.SampleCount, Artifacts: map[string]artifact{}}
		if f.Layout == "olympus-word16le-high12" {
			mismatches, low4, err := compareWords(inputPath(f), path, evidence, native.LoadFlags)
			if err != nil {
				return results, err
			}
			result.WordMismatchCount, result.StorageNonzeroLow4 = &mismatches, &low4
			if mismatches != 0 {
				return results, fmt.Errorf("%s: %d word-layout mismatches", f.ID, mismatches)
			}
		}
		exif, err := exifMetadata(filepath.Join(buildInfo.ToolsDirectory, "exiftool/exiftool"), inputPath(f), pins.ExifTool.Version)
		if err != nil {
			return results, err
		}
		metadata := map[string]any{
			"schema_version": 1, "fixture_id": f.ID, "input_sha256": f.SHA256,
			"libraw_commit": pins.LibRaw.Commit, "exiftool_commit": pins.ExifTool.Commit,
			"exporter_source_sha256": buildInfo.ExporterHash, "tool_source_sha256": buildInfo.ToolHash,
			"reference_lock_sha256": buildInfo.LockHash, "exporter_version": 1,
			"reference_go_version": pins.GoReferenceVersion,
			"sample_encoding":      "row-major uint16 little-endian; no padding or header",
			"libraw":               json.RawMessage(rawMetadata), "exiftool": exif,
		}
		if err := writeJSON(filepath.Join(output, f.ID+".json"), metadata); err != nil {
			return results, err
		}
		if err := writeJSON(filepath.Join(output, f.ID+".layout.json"), evidence); err != nil {
			return results, err
		}
		if err := compress(path); err != nil {
			return results, err
		}
		if err := os.Remove(path); err != nil {
			return results, err
		}
		for _, suffix := range []string{".json", ".layout.json", ".u16le.gz"} {
			name := f.ID + suffix
			a, err := digest(filepath.Join(output, name))
			if err != nil {
				return results, err
			}
			result.Artifacts[name] = a
		}
		results.Fixtures[f.ID] = result
		fmt.Printf("%s: %d samples, SHA-256 %s\n", f.ID, native.SampleCount, rawArtifact.SHA256)
	}
	return results, writeJSON(filepath.Join(output, "index.json"), results)
}

func referenceToolchain(pins lock, version string) error {
	if version != pins.GoReferenceVersion {
		return fmt.Errorf("byte-identical gzip generation requires %s; rerun with GOTOOLCHAIN=%s (running %s)",
			pins.GoReferenceVersion, pins.GoReferenceVersion, version)
	}
	return nil
}

func compress(path string) error {
	in, err := os.Open(path)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(path+".gz", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer out.Close()
	gz, err := gzip.NewWriterLevel(out, gzip.BestCompression)
	if err != nil {
		return err
	}
	if _, err := io.Copy(gz, in); err != nil {
		gz.Close()
		return err
	}
	if err := gz.Close(); err != nil {
		return err
	}
	return out.Close()
}

func exifMetadata(script, input, version string) (map[string]any, error) {
	args := []string{script, "-j", "-n", "-G1:4", "-a", "-s",
		"-Validate", "-Warning", "-Error",
		"-ExifToolVersion", "-Make", "-Model", "-Software", "-FirmwareVersion",
		"-ImageWidth", "-ImageHeight", "-BitsPerSample", "-Compression",
		"-RowsPerStrip", "-PhotometricInterpretation", "-CFAPattern", "-Orientation",
		"-CameraType2", "-ValidBits", "-BlackLevel", "-BlackLevel2", "-WB_RBLevels",
		"-CropLeft", "-CropTop", "-CropWidth", "-CropHeight", "-SensorCalibration",
		"-ColorMatrix", "-ColorMatrix2", "-RawDevColorSpace", "-SpecialMode",
		"-ExposureTime", "-FNumber", "-ISO", "-FocalLength", "-DriveMode",
		"-PreviewImageStart", "-PreviewImageLength", "-ThumbnailOffset", "-ThumbnailLength", input}
	data, err := command(".", "perl", args...)
	if err != nil {
		return nil, err
	}
	return decodeExif(data, version)
}

func decodeExif(data []byte, version string) (map[string]any, error) {
	var records []map[string]any
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(&records); err != nil {
		return nil, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err != nil {
			return nil, fmt.Errorf("trailing ExifTool output: %w", err)
		}
		return nil, errors.New("multiple JSON values in ExifTool output")
	}
	if len(records) != 1 || fmt.Sprint(records[0]["ExifTool:ExifToolVersion"]) != version {
		return nil, errors.New("unexpected ExifTool record/version")
	}
	for key, value := range records[0] {
		if strings.HasSuffix(key, ":Error") {
			return nil, fmt.Errorf("ExifTool reported %s: %v", key, value)
		}
	}
	delete(records[0], "SourceFile")
	return records[0], nil
}

func verify(corpus manifest) error {
	if err := verifyProvenance(corpus); err != nil {
		return err
	}
	var expected index
	if err := readJSON(filepath.Join(goldenDir, "index.json"), &expected); err != nil {
		return err
	}
	if expected.SchemaVersion != 1 || len(expected.Fixtures) != len(corpus.Fixtures) {
		return errors.New("golden index does not match fixture manifest")
	}
	for _, f := range corpus.Fixtures {
		if err := requireHash(inputPath(f), f.SHA256); err != nil {
			return err
		}
		result, ok := expected.Fixtures[f.ID]
		if !ok || result.InputSHA256 != f.SHA256 || len(result.Artifacts) != 3 {
			return fmt.Errorf("%s: incomplete golden index", f.ID)
		}
		if result.SampleCount <= 0 || result.SampleCount > 100_000_000 || !validDigest(result.SamplesSHA256) {
			return fmt.Errorf("%s: invalid golden sample count/hash", f.ID)
		}
		for _, suffix := range []string{".json", ".layout.json", ".u16le.gz"} {
			name := f.ID + suffix
			want, ok := result.Artifacts[name]
			if !ok {
				return fmt.Errorf("%s: missing artifact", name)
			}
			got, err := digest(filepath.Join(goldenDir, name))
			if err != nil {
				return err
			}
			if got != want {
				return fmt.Errorf("%s: golden artifact hash/length mismatch", name)
			}
		}
		file, err := os.Open(filepath.Join(goldenDir, f.ID+".u16le.gz"))
		if err != nil {
			return err
		}
		gz, err := gzip.NewReader(file)
		if err != nil {
			file.Close()
			return err
		}
		hash := sha256.New()
		n, readErr := io.Copy(hash, io.LimitReader(gz, result.SampleCount*2+1))
		gz.Close()
		file.Close()
		if readErr != nil {
			return readErr
		}
		if n != result.SampleCount*2 || hex.EncodeToString(hash.Sum(nil)) != result.SamplesSHA256 {
			return fmt.Errorf("%s: decompressed sample hash/length mismatch", f.ID)
		}
		evidence, err := inspect(inputPath(f))
		if err != nil {
			return err
		}
		data, err := jsonBytes(evidence)
		if err != nil {
			return err
		}
		want, err := os.ReadFile(filepath.Join(goldenDir, f.ID+".layout.json"))
		if err != nil {
			return err
		}
		if !bytes.Equal(data, want) {
			return fmt.Errorf("%s: measured layout differs from golden", f.ID)
		}
		fmt.Printf("%s: fixture, artifact, raw hash, and measured layout verified\n", f.ID)
	}
	return nil
}

// inspect intentionally handles only the small IFD0/ExifIFD subset measured
// in this corpus. It is an evidence instrument, not the production TIFF parser.
func inspect(path string) (layoutEvidence, error) {
	var result layoutEvidence
	file, err := os.Open(path)
	if err != nil {
		return result, err
	}
	defer file.Close()
	stat, err := file.Stat()
	if err != nil {
		return result, err
	}
	read := func(offset int64, count int) ([]byte, error) {
		if count < 0 || offset < 0 || int64(count) > stat.Size() || offset > stat.Size()-int64(count) {
			return nil, fmt.Errorf("invalid evidence read at %d, length %d", offset, count)
		}
		data := make([]byte, count)
		_, err := file.ReadAt(data, offset)
		return data, err
	}
	header, err := read(0, 8)
	if err != nil {
		return result, err
	}
	if string(header[:4]) != "IIRO" {
		return result, errors.New("evidence scanner supports only measured IIRO headers")
	}
	result.HeaderHex = hex.EncodeToString(header)
	result.ByteOrder = "little-endian"
	result.IFD0Offset = binary.LittleEndian.Uint32(header[4:])
	type tag struct {
		typ    uint16
		count  uint32
		value  []byte
		offset uint32
	}
	readIFD := func(offset uint32) (map[uint16]tag, uint16, error) {
		countData, err := read(int64(offset), 2)
		if err != nil {
			return nil, 0, err
		}
		count := binary.LittleEndian.Uint16(countData)
		if count > 4096 {
			return nil, 0, errors.New("evidence IFD entry budget exceeded")
		}
		entries, err := read(int64(offset)+2, int(count)*12+4)
		if err != nil {
			return nil, 0, err
		}
		tags := map[uint16]tag{}
		for i := 0; i < int(count); i++ {
			entry := entries[i*12 : (i+1)*12]
			id := binary.LittleEndian.Uint16(entry)
			if _, exists := tags[id]; exists {
				return nil, 0, fmt.Errorf("duplicate evidence tag 0x%04x", id)
			}
			tags[id] = tag{binary.LittleEndian.Uint16(entry[2:]), binary.LittleEndian.Uint32(entry[4:]),
				entry[8:12], binary.LittleEndian.Uint32(entry[8:])}
		}
		return tags, count, nil
	}
	numbers := func(value tag) ([]uint32, error) {
		size := 0
		switch value.typ {
		case 3:
			size = 2
		case 4:
			size = 4
		default:
			return nil, fmt.Errorf("unsupported evidence numeric type %d", value.typ)
		}
		if value.count == 0 || value.count > 4096 {
			return nil, errors.New("evidence tag count outside budget")
		}
		data := value.value
		if uint64(value.count)*uint64(size) > 4 {
			var err error
			data, err = read(int64(value.offset), int(value.count)*size)
			if err != nil {
				return nil, err
			}
		}
		values := make([]uint32, value.count)
		for i := range values {
			if size == 2 {
				values[i] = uint32(binary.LittleEndian.Uint16(data[i*size:]))
			} else {
				values[i] = binary.LittleEndian.Uint32(data[i*size:])
			}
		}
		return values, nil
	}
	tags, count, err := readIFD(result.IFD0Offset)
	if err != nil {
		return result, err
	}
	result.IFD0Entries = count
	scalars := map[uint16]*uint32{
		0x100: &result.Width, 0x101: &result.Height, 0x102: &result.BitsPerSample,
		0x103: &result.Compression, 0x106: &result.Photometric,
		0x116: &result.RowsPerStrip, 0x8769: &result.ExifIFDOffset,
	}
	ids := make([]int, 0, len(scalars))
	for id := range scalars {
		ids = append(ids, int(id))
	}
	sort.Ints(ids)
	for _, id := range ids {
		values, err := numbers(tags[uint16(id)])
		if err != nil || len(values) != 1 {
			return result, fmt.Errorf("IFD0 scalar 0x%04x: expected one numeric value: %v", id, err)
		}
		*scalars[uint16(id)] = values[0]
	}
	result.StripOffsets, err = numbers(tags[0x111])
	if err != nil {
		return result, err
	}
	result.StripByteCounts, err = numbers(tags[0x117])
	if err != nil {
		return result, err
	}
	if len(result.StripOffsets) == 0 || len(result.StripOffsets) != len(result.StripByteCounts) {
		return result, errors.New("invalid strip arrays")
	}
	result.Contiguous = true
	for i, offset := range result.StripOffsets {
		count := result.StripByteCounts[i]
		if count == 0 || uint64(offset)+uint64(count) > uint64(stat.Size()) {
			return result, errors.New("strip outside fixture")
		}
		result.PayloadBytes += uint64(count)
		if i > 0 && uint64(offset) != uint64(result.StripOffsets[i-1])+uint64(result.StripByteCounts[i-1]) {
			result.Contiguous = false
		}
	}
	if result.StripByteCounts[0] < 16 {
		return result, errors.New("payload too short for evidence prefix")
	}
	prefix, err := read(int64(result.StripOffsets[0]), 16)
	if err != nil {
		return result, err
	}
	result.PayloadPrefix = hex.EncodeToString(prefix)
	exif, _, err := readIFD(result.ExifIFDOffset)
	if err != nil {
		return result, err
	}
	maker, ok := exif[0x927c]
	if !ok || maker.typ != 7 || maker.count < 12 {
		return result, errors.New("missing or unexpected MakerNote")
	}
	result.MakerNoteOffset, result.MakerNoteBytes = maker.offset, maker.count
	if uint64(maker.offset)+uint64(maker.count) > uint64(stat.Size()) {
		return result, errors.New("MakerNote outside fixture")
	}
	makerHeader, err := read(int64(maker.offset), 12)
	if err != nil {
		return result, err
	}
	result.MakerNoteHeader = hex.EncodeToString(makerHeader)
	return result, nil
}

func compareWords(input, samples string, evidence layoutEvidence, shift uint) (int64, int64, error) {
	if shift != 4 || !evidence.Contiguous || evidence.ByteOrder != "little-endian" ||
		evidence.PayloadBytes != uint64(evidence.Width)*uint64(evidence.Height)*2 {
		return 0, 0, errors.New("word fixture contradicts high12 layout")
	}
	in, err := os.Open(input)
	if err != nil {
		return 0, 0, err
	}
	defer in.Close()
	out, err := os.Open(samples)
	if err != nil {
		return 0, 0, err
	}
	defer out.Close()
	storedRow := make([]byte, int(evidence.Width)*2)
	sampleRow := make([]byte, len(storedRow))
	var mismatches, low4 int64
	offset := int64(evidence.StripOffsets[0])
	for y := uint32(0); y < evidence.Height; y++ {
		if _, err := in.ReadAt(storedRow, offset); err != nil {
			return 0, 0, err
		}
		if _, err := io.ReadFull(out, sampleRow); err != nil {
			return 0, 0, err
		}
		for x := 0; x < len(storedRow); x += 2 {
			word := binary.LittleEndian.Uint16(storedRow[x:])
			sample := binary.LittleEndian.Uint16(sampleRow[x:])
			if word>>shift != sample {
				mismatches++
			}
			if word&15 != 0 {
				low4++
			}
		}
		offset += int64(len(storedRow))
	}
	return mismatches, low4, nil
}
