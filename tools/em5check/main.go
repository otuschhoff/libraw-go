// SPDX-License-Identifier: LGPL-2.1-only

// Command em5check performs explicit, local-only validation of private E-M5 files.
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"image"
	"image/png"
	"io"
	"math"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"

	libraw "github.com/otuschhoff/libraw-go"
)

type sample struct {
	path     string
	metadata libraw.Metadata
	render   bool
}

type report struct {
	Files            int            `json:"files"`
	Modes            map[string]int `json:"capture_modes"`
	RawFiles         int64          `json:"raw_files_compared"`
	Samples          int64          `json:"exact_raw_samples"`
	Rendered         int64          `json:"rendered_parity_cases"`
	GoVersion        string         `json:"go_version"`
	Platform         string         `json:"platform"`
	DistinctRendered map[string]int `json:"distinct_representatives_per_mode"`
}

type nativeMetadata struct {
	Version  string     `json:"libraw_version"`
	Decoder  string     `json:"decoder"`
	CameraID string     `json:"camera_id"`
	Width    int        `json:"raw_width"`
	Height   int        `json:"raw_height"`
	Export   [4]int     `json:"export_region"`
	Crop     [4]int     `json:"inset_crop"`
	Bits     uint8      `json:"valid_bits"`
	Count    int        `json:"sample_count"`
	Black    float64    `json:"black"`
	CBlack   []float64  `json:"cblack"`
	WB       [4]float64 `json:"cam_mul"`
	Maximum  float64    `json:"maximum"`
	Errors   int        `json:"data_errors"`
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	err := run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	if errors.Is(err, flag.ErrHelp) {
		return
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "em5check:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, out, stderr io.Writer) (err error) {
	flags := flag.NewFlagSet("em5check", flag.ContinueOnError)
	flags.SetOutput(stderr)
	root := flags.String("root", "", "private photo directory; required, never downloaded")
	reference := flags.String("reference", "", "local pinned LibRaw 0.21.4 reference-export executable; required")
	all := flags.Bool("all", false, "compare every RAW plane, not only distinct representative originals")
	workers := flags.Int("workers", 2, "parallel checks (1..8); working limits apply per worker")
	perMode := flags.Int("per-mode", 2, "maximum distinct originals per observed mode to render (at least 2); scarcity is reported, not certification")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *root == "" || *reference == "" || flags.NArg() != 0 || *workers < 1 || *workers > 8 || *perMode < 2 {
		return errors.New("required: -root DIRECTORY -reference EXECUTABLE [-all] [-workers 1..8] [-per-mode N>=2]")
	}
	cases, result, err := inventory(ctx, *root, *perMode)
	if err != nil {
		return err
	}
	if !*all {
		selected := cases[:0]
		for _, s := range cases {
			if s.render {
				selected = append(selected, s)
			}
		}
		cases = selected
	}
	temp, err := os.MkdirTemp("", "em5-private-check-")
	if err != nil {
		return err
	}
	// Only named per-worker scratch files are created; never remove a photo tree.
	defer func() { err = errors.Join(err, os.Remove(temp)) }()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var cursor, rawCount, total, rendered atomic.Int64
	var wg sync.WaitGroup
	var mu sync.Mutex
	var firstErr error
	for worker := 0; worker < *workers; worker++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for {
				if ctx.Err() != nil {
					return
				}
				i := int(cursor.Add(1) - 1)
				if i >= len(cases) {
					return
				}
				s := cases[i]
				n, err := check(ctx, s, *reference, filepath.Join(temp, fmt.Sprintf("worker-%d.u16le", worker)))
				if err != nil {
					mu.Lock()
					if firstErr == nil {
						firstErr = fmt.Errorf("%s: %w", s.path, err)
					}
					mu.Unlock()
					cancel()
					return
				}
				total.Add(int64(n))
				if s.render {
					rendered.Add(1)
				}
				done := rawCount.Add(1)
				if done%100 == 0 {
					mu.Lock()
					fmt.Fprintf(stderr, "verified %d/%d RAW planes\n", done, len(cases))
					mu.Unlock()
				}
			}
		}(worker)
	}
	wg.Wait()
	if firstErr != nil {
		return firstErr
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if rawCount.Load() != int64(len(cases)) {
		return errors.New("incomplete RAW validation; refusing a partial success report")
	}
	result.RawFiles, result.Samples, result.Rendered = rawCount.Load(), total.Load(), rendered.Load()
	return json.NewEncoder(out).Encode(result)
}

func inventory(ctx context.Context, root string, perMode int) ([]sample, report, error) {
	result := report{Modes: map[string]int{}, GoVersion: runtime.Version(), Platform: runtime.GOOS + "/" + runtime.GOARCH}
	var cases []sample
	selected := map[string]map[[32]byte]bool{}
	orientations := map[libraw.Orientation]bool{}
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if !d.Type().IsRegular() || !strings.EqualFold(filepath.Ext(path), ".orf") {
			return nil
		}
		input, err := os.Open(path)
		if err != nil {
			return err
		}
		defer input.Close()
		stat, err := input.Stat()
		if err != nil {
			return err
		}
		file, err := libraw.Open(input, stat.Size(), libraw.OpenOptions{})
		if err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		m := file.Metadata()
		if m.NormalizedMake != "Olympus" || m.NormalizedModel != "E-M5" || m.CameraID.Value != "S0036" ||
			m.Geometry.Width != 4640 || m.Geometry.Height != 3472 || m.BitDepth.Value != 12 ||
			m.Geometry.ActiveArea.Value != image.Rect(8, 8, 4616, 3464) ||
			m.CFA.Value != (libraw.CFA{{libraw.Red, libraw.Green1}, {libraw.Green2, libraw.Blue}}) ||
			!file.Capabilities().Raw.Available || !file.Capabilities().Render.Available || !file.Capabilities().Preview.Available {
			return fmt.Errorf("%s: outside evidenced E-M5 scope or missing operation capability: %+v", path, file.Capabilities())
		}
		mode := m.CaptureMode.Value
		result.Modes[mode]++
		if selected[mode] == nil {
			selected[mode] = map[[32]byte]bool{}
		}
		render := false
		if len(selected[mode]) < perMode {
			h := sha256.New()
			if _, err := io.Copy(h, input); err != nil {
				return err
			}
			var sum [32]byte
			copy(sum[:], h.Sum(nil))
			if !selected[mode][sum] {
				selected[mode][sum] = true
				render = true
			}
		}
		if !orientations[m.Orientation.Value] {
			orientations[m.Orientation.Value] = true
			render = true
		}
		cases = append(cases, sample{path: path, metadata: m, render: render})
		return nil
	})
	if err != nil {
		return nil, result, err
	}
	if len(cases) == 0 {
		return nil, result, errors.New("no private ORFs found; no checks silently skipped")
	}
	result.DistinctRendered = map[string]int{}
	for mode, hashes := range selected {
		result.DistinctRendered[mode] = len(hashes)
	}
	result.Files = len(cases)
	return cases, result, nil
}

func check(ctx context.Context, s sample, reference, scratch string) (count int, err error) {
	defer func() {
		removeErr := os.Remove(scratch)
		if removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			err = errors.Join(err, removeErr)
		}
	}()
	command := exec.CommandContext(ctx, reference, s.path, scratch)
	var stderr bytes.Buffer
	command.Stderr = &stderr
	data, err := command.Output()
	if err != nil {
		return 0, errors.Join(ctx.Err(), fmt.Errorf("independent local reference: %w: %s", err, &stderr))
	}
	var native nativeMetadata
	if err := json.Unmarshal(data, &native); err != nil {
		return 0, err
	}
	input, err := os.Open(s.path)
	if err != nil {
		return 0, err
	}
	defer input.Close()
	info, err := input.Stat()
	if err != nil {
		return 0, err
	}
	file, err := libraw.Open(input, info.Size(), libraw.OpenOptions{})
	if err != nil {
		return 0, err
	}
	if !reflect.DeepEqual(file.Metadata(), s.metadata) {
		return 0, errors.New("input metadata changed during validation")
	}
	raw, err := file.DecodeRaw(ctx)
	if err != nil {
		return 0, err
	}
	if native.Version != "0.21.4-Release" || native.Decoder != "olympus_load_raw()" ||
		native.CameraID != "S0036" || native.Errors != 0 || native.Width != raw.Width || native.Height != raw.Height ||
		native.Bits != raw.BitDepth || native.Count != len(raw.Pixels) || native.Export != [4]int{0, 0, raw.Width, raw.Height} ||
		native.Crop != [4]int{raw.ActiveArea.Min.X, raw.ActiveArea.Min.Y, raw.ActiveArea.Dx(), raw.ActiveArea.Dy()} || len(native.CBlack) < 4 {
		return 0, errors.New("independent decoder, geometry, crop, depth or initialized-region mismatch")
	}
	colorIndices := [2][2]int{{0, 1}, {3, 2}}
	for y := range 2 {
		for x := range 2 {
			color := colorIndices[y][x]
			gain := native.WB[color]
			if color == 3 && gain == 0 {
				gain = native.WB[1]
			}
			cal := raw.Calibration
			if math.Abs(cal.Black.Value[y][x]-(native.Black+native.CBlack[color])) > 1e-6 ||
				math.Abs(cal.WhiteBalance.Value[y][x]-gain) > 1e-6 || cal.White.Value[y][x] != native.Maximum {
				return 0, errors.New("independent black/WB/white calibration mismatch")
			}
		}
	}
	golden, err := os.Open(scratch)
	if err != nil {
		return 0, err
	}
	defer golden.Close()
	row := make([]byte, raw.Width*2)
	mismatches, maxError, first := 0, 0, -1
	for y := range raw.Height {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		if _, err := io.ReadFull(golden, row); err != nil {
			return 0, err
		}
		for x := range raw.Width {
			i := y*raw.Width + x
			got, want := int(raw.Pixels[i]), int(binary.LittleEndian.Uint16(row[x*2:]))
			delta := got - want
			if delta < 0 {
				delta = -delta
			}
			if delta > 0 {
				mismatches++
				if first < 0 {
					first = i
				}
				maxError = max(maxError, delta)
			}
		}
	}
	var trailing [1]byte
	if n, err := golden.Read(trailing[:]); n != 0 || err != io.EOF {
		return 0, errors.New("unexpected independent sample extent")
	}
	if mismatches != 0 {
		return 0, fmt.Errorf("RAW mismatches=%d max_error=%d first=(%d,%d)", mismatches, maxError, first%raw.Width, first/raw.Width)
	}
	if s.render {
		if err := parity(ctx, input, file, raw); err != nil {
			return 0, err
		}
	}
	return len(raw.Pixels), nil
}

func parity(ctx context.Context, input *os.File, file *libraw.File, raw *libraw.RawImage) error {
	if _, err := input.Seek(0, io.SeekStart); err != nil {
		return err
	}
	data, err := io.ReadAll(input)
	if err != nil {
		return err
	}
	before := sha256.Sum256(data)
	memory, err := libraw.Open(bytes.NewReader(data), int64(len(data)), libraw.OpenOptions{})
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(memory.Metadata(), file.Metadata()) || memory.Capabilities() != file.Capabilities() {
		return errors.New("metadata/capability ReaderAt parity failure")
	}
	preview, err := file.Preview(ctx)
	if err != nil {
		return err
	}
	otherPreview, err := memory.Preview(ctx)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(preview, otherPreview) {
		return errors.New("preview ReaderAt parity failure")
	}
	otherRaw, err := memory.DecodeRaw(ctx)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(raw, otherRaw) {
		return errors.New("RAW ReaderAt parity failure")
	}
	rgb, err := libraw.Render(ctx, raw, libraw.RenderOptions{})
	if err != nil {
		return err
	}
	otherRGB, err := libraw.Render(ctx, otherRaw, libraw.RenderOptions{})
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(rgb, otherRGB) {
		return errors.New("render ReaderAt parity failure")
	}
	width, height := raw.ActiveArea.Dx(), raw.ActiveArea.Dy()
	if raw.Orientation >= libraw.OrientationLeftTop {
		width, height = height, width
	}
	if rgb.Rect != image.Rect(0, 0, width, height) {
		return errors.New("rendered crop/orientation shape mismatch")
	}
	for i := 6; i < len(rgb.Pix); i += 8 {
		if binary.BigEndian.Uint16(rgb.Pix[i:]) != 65535 {
			return errors.New("nonopaque rendering")
		}
	}
	if err := png.Encode(io.Discard, rgb); err != nil {
		return err
	}
	otherRaw.Pixels[0] ^= 1
	otherRGB.Pix[0] ^= 1
	otherPreview.JPEG[0] ^= 1
	if raw.Pixels[0] == otherRaw.Pixels[0] || rgb.Pix[0] == otherRGB.Pix[0] || preview.JPEG[0] == otherPreview.JPEG[0] || sha256.Sum256(data) != before {
		return errors.New("input mutation or result aliasing")
	}
	return nil
}
