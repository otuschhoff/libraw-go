// SPDX-License-Identifier: LGPL-2.1-only

package libraw_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image/png"
	"io"
	"os"
	"time"

	libraw "github.com/otuschhoff/libraw-go"
)

func ExampleOpen() {
	data := []byte("not an ORF")
	_, err := libraw.Open(context.Background(), bytes.NewReader(data), int64(len(data)), libraw.OpenOptions{})
	fmt.Println(errors.Is(err, libraw.ErrNotORF))
	// Output: true
}

// The caller keeps input open while File operations are in progress.
func ExampleFile_Metadata() {
	read := func(path string) (libraw.Metadata, libraw.Capabilities, error) {
		input, err := os.Open(path)
		if err != nil {
			return libraw.Metadata{}, libraw.Capabilities{}, err
		}
		defer input.Close()
		info, err := input.Stat()
		if err != nil {
			return libraw.Metadata{}, libraw.Capabilities{}, err
		}
		file, err := libraw.Open(context.Background(), input, info.Size(), libraw.OpenOptions{})
		if err != nil {
			return libraw.Metadata{}, libraw.Capabilities{}, err
		}
		defer file.Close()
		return file.Metadata().Clone(), file.Capabilities(), nil
	}
	_ = read
}

func ExampleFile_Preview() {
	extract := func(ctx context.Context, file *libraw.File, output io.Writer) error {
		preview, err := file.Preview(ctx)
		if err != nil {
			return err
		}
		defer preview.Close()
		_, err = io.Copy(output, bytes.NewReader(preview.JPEG))
		return err
	}
	_ = extract // Writes original embedded bytes, not a rendered RAW image.
}

func ExampleFile_DecodeRaw() {
	decode := func(ctx context.Context, file *libraw.File) (*libraw.RawImage, error) {
		return file.DecodeRaw(ctx)
	}
	_ = decode // Pixels are caller-owned unprocessed sensor codes.
}

func ExampleRender() {
	writePNG := func(ctx context.Context, raw *libraw.RawImage, output io.Writer) error {
		rgb, err := libraw.Render(ctx, raw, libraw.RenderOptions{})
		if err != nil {
			return err
		}
		defer rgb.Close()
		return png.Encode(output, rgb)
	}
	_ = writePNG
}

func ExampleBudget() {
	exportPNG := func(parent context.Context, input io.ReaderAt, size int64, dst io.Writer) error {
		ctx, cancel := context.WithTimeout(parent, 5*time.Second)
		defer cancel()
		budget, err := libraw.NewBudget(256 << 20)
		if err != nil {
			return err
		}
		limits := libraw.DefaultLimits()
		limits.WorkingBytes = 192 << 20
		limits.MetadataBytes = 8 << 20
		limits.PreviewBytes = 32 << 20
		file, err := libraw.Open(ctx, input, size, libraw.OpenOptions{Limits: limits, Budget: budget})
		if err != nil {
			return err
		}
		defer file.Close()
		raw, err := file.DecodeRaw(ctx)
		if err != nil {
			return err
		}
		defer raw.Close()
		rgb, err := libraw.Render(ctx, raw, libraw.RenderOptions{Limits: limits})
		if err != nil {
			return err
		}
		defer rgb.Close()
		return png.Encode(dst, rgb)
	}
	_ = exportPNG
}
