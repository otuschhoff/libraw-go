// SPDX-License-Identifier: LGPL-2.1-only

// Command orfexport demonstrates independent preview, sensor-code and PNG export.
package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"flag"
	"fmt"
	"image/png"
	"io"
	"os"
	"os/signal"
	"path/filepath"

	libraw "github.com/otuschhoff/libraw-go"
)

func run(ctx context.Context, args []string, stderr io.Writer) int {
	flags := flag.NewFlagSet("orfexport", flag.ContinueOnError)
	flags.SetOutput(stderr)
	format := flags.String("format", "", "raw (u16le), preview (original JPEG), or png (16-bit sRGB)")
	output := flags.String("output", "", "new output file; existing files are never replaced")
	allow := flags.Bool("allow-estimated-white", false, "explicitly permit estimated white levels for PNG rendering")
	ev := flags.Float64("exposure", 0, "linear exposure compensation in EV (PNG only)")
	preserve := flags.Bool("preserve-orientation", false, "retain sensor orientation (PNG only)")
	flags.Usage = func() {
		fmt.Fprintln(stderr, "usage: orfexport -format raw|preview|png -output FILE [options] INPUT.orf")
		flags.PrintDefaults()
	}
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if flags.NArg() != 1 || *output == "" || (*format != "raw" && *format != "preview" && *format != "png") ||
		(*format != "png" && (*allow || *ev != 0 || *preserve)) {
		flags.Usage()
		return 2
	}
	if err := export(ctx, flags.Arg(0), *output, *format, libraw.RenderOptions{
		AllowEstimatedWhite: *allow, ExposureEV: *ev, PreserveOrientation: *preserve,
	}); err != nil {
		fmt.Fprintln(stderr, "orfexport:", err)
		return 1
	}
	return 0
}

func export(ctx context.Context, path, output, format string, opts libraw.RenderOptions) error {
	if ctx == nil {
		return fmt.Errorf("%w: nil context", libraw.ErrInvalidOptions)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	input, err := os.Open(path)
	if err != nil {
		return err
	}
	defer input.Close()
	info, err := input.Stat()
	if err != nil {
		return err
	}
	file, err := libraw.Open(ctx, input, info.Size(), libraw.OpenOptions{})
	if err != nil {
		return err
	}
	defer file.Close()
	if format == "preview" {
		preview, err := file.Preview(ctx)
		if err != nil {
			return err
		}
		defer preview.Close()
		return publish(ctx, output, func(w io.Writer) error {
			_, err := io.Copy(w, bytes.NewReader(preview.JPEG))
			return err
		})
	}
	raw, err := file.DecodeRaw(ctx)
	if err != nil {
		return err
	}
	defer raw.Close()
	if format == "raw" {
		return publish(ctx, output, func(w io.Writer) error { return writeRaw(ctx, w, raw) })
	}
	rgb, err := libraw.Render(ctx, raw, opts)
	if err != nil {
		return err
	}
	defer rgb.Close()
	return publish(ctx, output, func(w io.Writer) error { return png.Encode(w, rgb) })
}

func writeRaw(ctx context.Context, out io.Writer, raw *libraw.RawImage) error {
	row := make([]byte, raw.Width*2)
	for y := range raw.Height {
		if err := ctx.Err(); err != nil {
			return err
		}
		for x := range raw.Width {
			binary.LittleEndian.PutUint16(row[x*2:], raw.Pixels[y*raw.Stride+x])
		}
		if n, err := out.Write(row); err != nil {
			return err
		} else if n != len(row) {
			return io.ErrShortWrite
		}
	}
	return ctx.Err()
}

type contextWriter struct {
	ctx context.Context
	out io.Writer
}

func (w contextWriter) Write(data []byte) (int, error) {
	if err := w.ctx.Err(); err != nil {
		return 0, err
	}
	return w.out.Write(data)
}

// Link only after successful write/close: publication is atomic and never
// replaces an existing destination on the supported Linux/macOS filesystems.
func publish(ctx context.Context, path string, write func(io.Writer) error) (err error) {
	temp, err := os.CreateTemp(filepath.Dir(path), ".orfexport-*")
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, os.Remove(temp.Name())) }()
	writeErr := write(contextWriter{ctx: ctx, out: temp})
	if err := errors.Join(writeErr, temp.Close()); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return os.Link(temp.Name(), path)
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	code := run(ctx, os.Args[1:], os.Stderr)
	stop()
	os.Exit(code)
}
